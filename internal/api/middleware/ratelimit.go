package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ──────────────────────────────────────────────────────────────────────────────
// Token Bucket Rate Limiter
// ──────────────────────────────────────────────────────────────────────────────

// bucket is a simple in-memory token bucket for one IP address.
type bucket struct {
	tokens    float64
	lastRefil time.Time
	mu        sync.Mutex
}

// rateLimiter holds per-IP buckets and the shared read-write lock.
type rateLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64 // maximum token capacity
}

// newRateLimiter creates a rate limiter with the given requests-per-second
// allowance.  The burst capacity is set to max(10, rate) so short spikes are
// absorbed.
func newRateLimiter(rps int) *rateLimiter {
	burst := float64(rps)
	if burst < 10 {
		burst = 10
	}
	return &rateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(rps),
		burst:   burst,
	}
}

// allow returns true when the given key is allowed to proceed and deducts one
// token from its bucket.
func (rl *rateLimiter) allow(key string) bool {
	// Fast path: bucket exists
	rl.mu.RLock()
	b, ok := rl.buckets[key]
	rl.mu.RUnlock()

	if !ok {
		// Slow path: create a new full bucket for this IP
		rl.mu.Lock()
		if b, ok = rl.buckets[key]; !ok {
			b = &bucket{tokens: rl.burst, lastRefil: time.Now()}
			rl.buckets[key] = b
		}
		rl.mu.Unlock()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefil).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastRefil = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RateLimitMiddleware returns a gin.HandlerFunc that enforces a per-IP token
// bucket rate limit of rps requests per second.  Clients exceeding the limit
// receive 429 Too Many Requests.
func RateLimitMiddleware(rps int) gin.HandlerFunc {
	rl := newRateLimiter(rps)

	// Background goroutine to evict stale buckets every 5 minutes to prevent
	// the map from growing without bound.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			rl.mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for ip, b := range rl.buckets {
				b.mu.Lock()
				if b.lastRefil.Before(cutoff) {
					delete(rl.buckets, ip)
				}
				b.mu.Unlock()
			}
			rl.mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !rl.allow(ip) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "too many requests — please slow down",
			})
			return
		}
		c.Next()
	}
}
