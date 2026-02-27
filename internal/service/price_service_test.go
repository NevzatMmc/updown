package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/service"
	"github.com/shopspring/decimal"
)

// ── Mock exchange HTTP servers ────────────────────────────────────────────────

func mockBinanceOK(price float64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]string{"price": decimal.NewFromFloat(price).StringFixed(2)}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// Bybit expects: {"result":{"list":[{"lastPrice":"..."}]}}
func mockBybitOK(price float64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outer := struct {
			Result struct {
				List []struct {
					LastPrice string `json:"lastPrice"`
				} `json:"list"`
			} `json:"result"`
		}{}
		outer.Result.List = []struct {
			LastPrice string `json:"lastPrice"`
		}{{LastPrice: decimal.NewFromFloat(price).StringFixed(2)}}
		_ = json.NewEncoder(w).Encode(outer)
	})
}

// OKX expects: {"data":[{"last":"..."}]}
func mockOKXOK(price float64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outer := struct {
			Data []struct {
				Last string `json:"last"`
			} `json:"data"`
		}{
			Data: []struct {
				Last string `json:"last"`
			}{{Last: decimal.NewFromFloat(price).StringFixed(2)}},
		}
		_ = json.NewEncoder(w).Encode(outer)
	})
}

func mockServerError() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
}

func buildPriceConfig(binanceURL, bybitURL, okxURL string, cacheTTL time.Duration) *config.Config {
	return &config.Config{
		Price: config.PriceConfig{
			BinanceURL:    binanceURL,
			BybitURL:      bybitURL,
			OKXURL:        okxURL,
			FetchTimeout:  3 * time.Second,
			CacheTTL:      cacheTTL,
			BinanceWeight: 50,
			BybitWeight:   30,
			OKXWeight:     20,
		},
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestPriceService_AllSources confirms weighted average with all 3 sources healthy.
// Binance 90000 (×50) + Bybit 91000 (×30) + OKX 92000 (×20) = 90700 / 100
func TestPriceService_AllSources(t *testing.T) {
	sBinance := httptest.NewServer(mockBinanceOK(90000))
	defer sBinance.Close()
	sBybit := httptest.NewServer(mockBybitOK(91000))
	defer sBybit.Close()
	sOKX := httptest.NewServer(mockOKXOK(92000))
	defer sOKX.Close()

	cfg := buildPriceConfig(sBinance.URL, sBybit.URL, sOKX.URL, 0)
	svc := service.NewPriceService(cfg)

	price, sources, err := svc.GetWeightedPrice(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if price.IsZero() {
		t.Error("expected non-zero price")
	}
	if len(sources) == 0 {
		t.Error("expected at least one source")
	}

	// Weighted: 90000*50 + 91000*30 + 92000*20 = 4500000+2730000+1840000 = 9070000 / 100 = 90700
	want := decimal.NewFromFloat(90700)
	if price.Sub(want).Abs().GreaterThan(decimal.NewFromFloat(1)) {
		t.Errorf("weighted price = %s, want ~%s", price, want)
	}
	t.Logf("weighted price = %s, sources = %d", price, len(sources))
}

// TestPriceServiceFallback_BinanceDown verifies Bybit+OKX provide a price
// when Binance returns HTTP 503.
func TestPriceServiceFallback_BinanceDown(t *testing.T) {
	sBinance := httptest.NewServer(mockServerError())
	defer sBinance.Close()
	sBybit := httptest.NewServer(mockBybitOK(91000))
	defer sBybit.Close()
	sOKX := httptest.NewServer(mockOKXOK(92000))
	defer sOKX.Close()

	cfg := buildPriceConfig(sBinance.URL, sBybit.URL, sOKX.URL, 0)
	svc := service.NewPriceService(cfg)

	price, sources, err := svc.GetWeightedPrice(context.Background())
	if err != nil {
		t.Fatalf("partial failure should still return price, got err: %v", err)
	}
	if price.IsZero() {
		t.Error("expected non-zero fallback price")
	}
	// Only Bybit+OKX sources
	if len(sources) != 2 {
		t.Errorf("expected 2 sources (Bybit+OKX), got %d", len(sources))
	}
	// Weighted: 91000*30 + 92000*20 = 2730000+1840000 = 4570000 / 50 = 91400
	want := decimal.NewFromFloat(91400)
	if price.Sub(want).Abs().GreaterThan(decimal.NewFromFloat(1)) {
		t.Errorf("fallback price = %s, want ~%s", price, want)
	}
	t.Logf("fallback price=%s sources=%d", price, len(sources))
}

// TestPriceServiceFallback_AllDown confirms error returned when all sources fail.
func TestPriceServiceFallback_AllDown(t *testing.T) {
	sBinance := httptest.NewServer(mockServerError())
	defer sBinance.Close()
	sBybit := httptest.NewServer(mockServerError())
	defer sBybit.Close()
	sOKX := httptest.NewServer(mockServerError())
	defer sOKX.Close()

	cfg := buildPriceConfig(sBinance.URL, sBybit.URL, sOKX.URL, 0)
	svc := service.NewPriceService(cfg)

	_, _, err := svc.GetWeightedPrice(context.Background())
	if err == nil {
		t.Fatal("expected error when all price sources are down")
	}
	t.Logf("all-sources-down error: %v", err)
}

// TestPriceService_CachedPrice checks that GetCachedPrice() returns the price
// after a successful warm-up fetch when TTL is long.
func TestPriceService_CachedPrice(t *testing.T) {
	sBinance := httptest.NewServer(mockBinanceOK(87000))
	defer sBinance.Close()
	sBybit := httptest.NewServer(mockBybitOK(87000))
	defer sBybit.Close()
	sOKX := httptest.NewServer(mockOKXOK(87000))
	defer sOKX.Close()

	cfg := buildPriceConfig(sBinance.URL, sBybit.URL, sOKX.URL, 60*time.Second)
	svc := service.NewPriceService(cfg)

	// Warm cache
	if _, _, err := svc.GetWeightedPrice(context.Background()); err != nil {
		t.Fatalf("warm cache fetch failed: %v", err)
	}

	price, ok := svc.GetCachedPrice()
	if !ok {
		t.Error("expected cache hit after successful fetch with 60s TTL")
	}
	if price.IsZero() {
		t.Error("cached price should not be zero")
	}
	t.Logf("cached price=%s", price)
}

// TestPriceService_CacheExpires confirms that with TTL=0 the cache is always stale.
func TestPriceService_CacheExpires(t *testing.T) {
	sBinance := httptest.NewServer(mockBinanceOK(87000))
	defer sBinance.Close()
	sBybit := httptest.NewServer(mockBybitOK(87000))
	defer sBybit.Close()
	sOKX := httptest.NewServer(mockOKXOK(87000))
	defer sOKX.Close()

	cfg := buildPriceConfig(sBinance.URL, sBybit.URL, sOKX.URL, 0) // instant expiry
	svc := service.NewPriceService(cfg)

	// Even after a fetch, with TTL=0 the cache is already expired
	if _, _, err := svc.GetWeightedPrice(context.Background()); err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	_, ok := svc.GetCachedPrice()
	if ok {
		t.Error("with TTL=0, cache should be considered expired immediately")
	}
}
