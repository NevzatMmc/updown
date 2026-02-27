package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/shopspring/decimal"
)

// ──────────────────────────────────────────────────────────────────────────────
// Exchange weight constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	exchangeBinance = "binance"
	exchangeBybit   = "bybit"
	exchangeOKX     = "okx"
)

// exchangeDef describes a single price-feed source.
type exchangeDef struct {
	name   string
	weight decimal.Decimal // 0–100
	fetch  func(ctx context.Context) (decimal.Decimal, error)
}

// ──────────────────────────────────────────────────────────────────────────────
// PriceService
// ──────────────────────────────────────────────────────────────────────────────

// PriceService fetches BTC/USDT prices from multiple exchanges in parallel,
// computes a weighted average, and caches the result.
type PriceService struct {
	client *http.Client
	cfg    *config.PriceConfig

	// in-memory cache
	mu          sync.RWMutex
	cachedPrice decimal.Decimal
	cacheTime   time.Time
	lastSources []domain.PriceSource

	// per-exchange last-success timestamp (for ExchangeStatus)
	statusMu    sync.RWMutex
	lastSuccess map[string]time.Time
	exchanges   []exchangeDef
}

// NewPriceService constructs a PriceService from the given config.
func NewPriceService(cfg *config.Config) *PriceService {
	ps := &PriceService{
		client: &http.Client{Timeout: cfg.Price.FetchTimeout},
		cfg:    &cfg.Price,
		lastSuccess: map[string]time.Time{
			exchangeBinance: {},
			exchangeBybit:   {},
			exchangeOKX:     {},
		},
	}

	ps.exchanges = []exchangeDef{
		{
			name:   exchangeBinance,
			weight: decimal.NewFromInt(int64(cfg.Price.BinanceWeight)),
			fetch:  ps.fetchBinance,
		},
		{
			name:   exchangeBybit,
			weight: decimal.NewFromInt(int64(cfg.Price.BybitWeight)),
			fetch:  ps.fetchBybit,
		},
		{
			name:   exchangeOKX,
			weight: decimal.NewFromInt(int64(cfg.Price.OKXWeight)),
			fetch:  ps.fetchOKX,
		},
	}

	return ps
}

// ──────────────────────────────────────────────────────────────────────────────
// Public API
// ──────────────────────────────────────────────────────────────────────────────

// GetWeightedPrice returns the current BTC/USDT price as a weighted average of
// all configured exchanges.  If the in-memory cache is still fresh (< CacheTTL)
// the cached value is returned immediately.
//
// Partial failures are handled by re-normalising the weights over the available
// sources.  The method requires at least 1 successful source; if all fail it
// returns an error.
//
// The returned []domain.PriceSource slice contains one entry per successful
// exchange fetch, useful for monitoring dashboards.
func (ps *PriceService) GetWeightedPrice(ctx context.Context) (decimal.Decimal, []domain.PriceSource, error) {
	// ── Cache check ──────────────────────────────────────────────────────────
	ps.mu.RLock()
	if !ps.cacheTime.IsZero() && time.Since(ps.cacheTime) < ps.cfg.CacheTTL {
		price := ps.cachedPrice
		sources := ps.lastSources
		ps.mu.RUnlock()
		return price, sources, nil
	}
	ps.mu.RUnlock()

	// ── Parallel fetch with per-exchange timeout ──────────────────────────────
	type result struct {
		name  string
		price decimal.Decimal
		err   error
	}

	fetchCtx, cancel := context.WithTimeout(ctx, ps.client.Timeout)
	defer cancel()

	resultCh := make(chan result, len(ps.exchanges))
	for _, ex := range ps.exchanges {
		ex := ex // capture
		go func() {
			p, err := ex.fetch(fetchCtx)
			resultCh <- result{name: ex.name, price: p, err: err}
		}()
	}

	// Collect results
	rawResults := make(map[string]result, len(ps.exchanges))
	for range ps.exchanges {
		r := <-resultCh
		rawResults[r.name] = r
	}

	// ── Build sources list & compute weighted average ─────────────────────────
	var sources []domain.PriceSource
	var sumWeighted, sumWeights decimal.Decimal
	now := time.Now()

	for _, ex := range ps.exchanges {
		r := rawResults[ex.name]
		if r.err != nil || r.price.IsZero() {
			continue
		}
		sources = append(sources, domain.PriceSource{
			Exchange:  ex.name,
			Price:     r.price,
			Weight:    ex.weight,
			FetchedAt: now,
		})
		sumWeighted = sumWeighted.Add(r.price.Mul(ex.weight))
		sumWeights = sumWeights.Add(ex.weight)

		// Record last-success timestamp per exchange
		ps.statusMu.Lock()
		ps.lastSuccess[ex.name] = now
		ps.statusMu.Unlock()
	}

	if len(sources) == 0 {
		return decimal.Zero, nil, fmt.Errorf("price_service: all exchange fetches failed")
	}

	// Normalize over available weights (handles missing exchange gracefully)
	weightedAvg := sumWeighted.Div(sumWeights)

	// ── Update cache ─────────────────────────────────────────────────────────
	ps.mu.Lock()
	ps.cachedPrice = weightedAvg
	ps.cacheTime = now
	ps.lastSources = sources
	ps.mu.Unlock()

	return weightedAvg, sources, nil
}

// GetCachedPrice returns the most recently cached price and true if the cache
// is still within its TTL.  Returns (Zero, false) when the cache is stale.
func (ps *PriceService) GetCachedPrice() (decimal.Decimal, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.cacheTime.IsZero() || time.Since(ps.cacheTime) >= ps.cfg.CacheTTL {
		return decimal.Zero, false
	}
	return ps.cachedPrice, true
}

// ExchangeStatus returns a map of exchange name → whether it was reachable in
// the last 5 seconds.  Used by the back-office health dashboard.
func (ps *PriceService) ExchangeStatus() map[string]bool {
	threshold := 5 * time.Second
	ps.statusMu.RLock()
	defer ps.statusMu.RUnlock()

	status := make(map[string]bool, len(ps.lastSuccess))
	for name, t := range ps.lastSuccess {
		status[name] = !t.IsZero() && time.Since(t) < threshold
	}
	return status
}

// ──────────────────────────────────────────────────────────────────────────────
// Exchange fetchers
// ──────────────────────────────────────────────────────────────────────────────

// fetchBinance fetches the BTC/USDT spot price from Binance REST API.
//
//	GET /api/v3/ticker/price?symbol=BTCUSDT
//	{"symbol":"BTCUSDT","price":"87350.00"}
func (ps *PriceService) fetchBinance(ctx context.Context) (decimal.Decimal, error) {
	url := ps.cfg.BinanceURL + "/api/v3/ticker/price?symbol=BTCUSDT"
	body, err := ps.doGet(ctx, url)
	if err != nil {
		return decimal.Zero, fmt.Errorf("binance: %w", err)
	}

	var resp struct {
		Price string `json:"price"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("binance parse: %w", err)
	}
	if resp.Price == "" {
		return decimal.Zero, fmt.Errorf("binance: empty price field")
	}
	price, err := decimal.NewFromString(resp.Price)
	if err != nil {
		return decimal.Zero, fmt.Errorf("binance decimal: %w", err)
	}
	return price, nil
}

// fetchBybit fetches the BTC/USDT spot price from Bybit REST API.
//
//	GET /v5/market/tickers?category=spot&symbol=BTCUSDT
//	{"result":{"list":[{"lastPrice":"87350.00",...}]}}
func (ps *PriceService) fetchBybit(ctx context.Context) (decimal.Decimal, error) {
	url := ps.cfg.BybitURL + "/v5/market/tickers?category=spot&symbol=BTCUSDT"
	body, err := ps.doGet(ctx, url)
	if err != nil {
		return decimal.Zero, fmt.Errorf("bybit: %w", err)
	}

	var resp struct {
		Result struct {
			List []struct {
				LastPrice string `json:"lastPrice"`
			} `json:"list"`
		} `json:"result"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("bybit parse: %w", err)
	}
	if len(resp.Result.List) == 0 || resp.Result.List[0].LastPrice == "" {
		return decimal.Zero, fmt.Errorf("bybit: empty result list")
	}
	price, err := decimal.NewFromString(resp.Result.List[0].LastPrice)
	if err != nil {
		return decimal.Zero, fmt.Errorf("bybit decimal: %w", err)
	}
	return price, nil
}

// fetchOKX fetches the BTC/USDT spot price from OKX REST API.
//
//	GET /api/v5/market/ticker?instId=BTC-USDT
//	{"data":[{"last":"87350.00",...}]}
func (ps *PriceService) fetchOKX(ctx context.Context) (decimal.Decimal, error) {
	url := ps.cfg.OKXURL + "/api/v5/market/ticker?instId=BTC-USDT"
	body, err := ps.doGet(ctx, url)
	if err != nil {
		return decimal.Zero, fmt.Errorf("okx: %w", err)
	}

	var resp struct {
		Data []struct {
			Last string `json:"last"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("okx parse: %w", err)
	}
	if len(resp.Data) == 0 || resp.Data[0].Last == "" {
		return decimal.Zero, fmt.Errorf("okx: empty data field")
	}
	price, err := decimal.NewFromString(resp.Data[0].Last)
	if err != nil {
		return decimal.Zero, fmt.Errorf("okx decimal: %w", err)
	}
	return price, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// HTTP helper
// ──────────────────────────────────────────────────────────────────────────────

// doGet performs an HTTP GET with the service's client and returns the body
// bytes, or an error for any non-200 status code.
func (ps *PriceService) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "evetabi-prediction/1.0")

	resp, err := ps.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
