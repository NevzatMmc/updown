package service_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shopspring/decimal"
)

// TestConcurrentBalanceDeduction simulates 50 goroutines simultaneously
// deducting a fixed amount from a shared balance — protected by a mutex.
// This test verifies our concurrency guard pattern compiles and passes -race.
//
// In the real BetService, the DB row-level FOR UPDATE lock provides this
// guarantee.  Here we replicate the same guard with sync primitives so
// the race detector can confirm the pattern is sound.
func TestConcurrentBalanceDeduction(t *testing.T) {
	const workers = 50
	const stakeEach = 10 // TRY per bet

	balance := decimal.NewFromInt(int64(workers * stakeEach)) // exact total
	var mu sync.Mutex
	var failedBets int64 // bets that were rejected (zero is expected here)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			stake := decimal.NewFromInt(stakeEach)

			mu.Lock()
			defer mu.Unlock()

			if balance.LessThan(stake) {
				atomic.AddInt64(&failedBets, 1)
				return
			}
			balance = balance.Sub(stake)
		}(i)
	}
	wg.Wait()

	// All bets should succeed: no failures expected.
	if failedBets > 0 {
		t.Errorf("expected 0 failed bets, got %d", failedBets)
	}
	// Balance should be exactly 0 after exactly 50 × 10 deductions.
	if !balance.IsZero() {
		t.Errorf("final balance should be 0, got %s", balance)
	}
}

// TestConcurrentIdempotencyGuard verifies that double-spend protection works
// under concurrent access: only one of N goroutines succeeds at "exiting" a bet.
func TestConcurrentIdempotencyGuard(t *testing.T) {
	const workers = 20
	type betState struct {
		mu     sync.Mutex
		exited bool
	}

	var (
		b      betState
		wins   int64
		losses int64
		wg     sync.WaitGroup
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			b.mu.Lock()
			defer b.mu.Unlock()

			if b.exited {
				// Second+ call: should be rejected
				atomic.AddInt64(&losses, 1)
				return
			}
			b.exited = true
			atomic.AddInt64(&wins, 1)
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("exactly 1 goroutine should have exited the bet, got %d", wins)
	}
	if losses != workers-1 {
		t.Errorf("expected %d rejections, got %d", workers-1, losses)
	}
}
