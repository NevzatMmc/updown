package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/google/uuid"
)

// ──────────────────────────────────────────────────────────────────────────────
// Refunder interface — implemented by ResolutionService (Step 9)
// Declared here to break the import cycle between market_service and
// resolution_service.
// ──────────────────────────────────────────────────────────────────────────────

// Refunder is the minimal interface MarketService needs from ResolutionService.
type Refunder interface {
	RefundAll(ctx context.Context, marketID uuid.UUID) error
}

// ──────────────────────────────────────────────────────────────────────────────
// MarketService
// ──────────────────────────────────────────────────────────────────────────────

// MarketService handles market lifecycle: creation, querying, suspension and
// cancellation.
type MarketService struct {
	marketRepo   *repository.MarketRepository
	priceService *PriceService
	refunder     Refunder // injected after ResolutionService is built
	cfg          *config.Config

	// 500 ms active-market cache
	activeMu        sync.RWMutex
	activeMarket    *domain.Market
	activeCacheTime time.Time
}

// NewMarketService creates a MarketService.  Call SetRefunder() after
// constructing ResolutionService to inject the dependency.
func NewMarketService(
	marketRepo *repository.MarketRepository,
	priceService *PriceService,
	cfg *config.Config,
) *MarketService {
	return &MarketService{
		marketRepo:   marketRepo,
		priceService: priceService,
		cfg:          cfg,
	}
}

// SetRefunder injects the ResolutionService after both services are constructed
// (avoids constructor-cycle issues).
func (s *MarketService) SetRefunder(r Refunder) {
	s.refunder = r
}

// ──────────────────────────────────────────────────────────────────────────────
// CreateMarket
// ──────────────────────────────────────────────────────────────────────────────

// CreateMarket fetches the current weighted BTC price, opens a new market with
// status=open and persists it to the database.  startTime and endTime define the
// betting window (scheduled by the Scheduler goroutine).
func (s *MarketService) CreateMarket(ctx context.Context, startTime, endTime time.Time) (*domain.Market, error) {
	// Fetch weighted open price from all exchanges
	price, _, err := s.priceService.GetWeightedPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("market_service.CreateMarket: fetch price: %w", err)
	}

	now := time.Now().UTC()
	m := &domain.Market{
		ID:              uuid.New(),
		Status:          domain.StatusOpen,
		OpenPrice:       &price,
		PoolUp:          decimalZero(),
		PoolDown:        decimalZero(),
		CommissionTaken: decimalZero(),
		OpensAt:         startTime.UTC(),
		ClosesAt:        endTime.UTC(),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := s.marketRepo.Create(ctx, m); err != nil {
		return nil, fmt.Errorf("market_service.CreateMarket: db: %w", err)
	}

	// Invalidate the active-market cache so next read fetches the new one.
	s.invalidateActiveCache()

	return m, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// GetActiveMarket
// ──────────────────────────────────────────────────────────────────────────────

// GetActiveMarket returns the currently open market.  The result is cached for
// 500 ms to reduce DB pressure during high-frequency WS broadcasts.
func (s *MarketService) GetActiveMarket(ctx context.Context) (*domain.Market, error) {
	const cacheDuration = 500 * time.Millisecond

	s.activeMu.RLock()
	if s.activeMarket != nil && time.Since(s.activeCacheTime) < cacheDuration {
		m := s.activeMarket
		s.activeMu.RUnlock()
		return m, nil
	}
	s.activeMu.RUnlock()

	m, err := s.marketRepo.GetActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("market_service.GetActiveMarket: %w", err)
	}

	s.activeMu.Lock()
	s.activeMarket = m
	s.activeCacheTime = time.Now()
	s.activeMu.Unlock()

	return m, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// GetMarketWithOdds
// ──────────────────────────────────────────────────────────────────────────────

// GetMarketWithOdds fetches a market by ID and returns it together with all
// computed odds fields (UpOdds, DownOdds, UpPercent, DownPercent).  Because the
// domain.Market struct carries live pool values, callers invoke the method
// receivers directly on the returned struct.
func (s *MarketService) GetMarketWithOdds(ctx context.Context, id uuid.UUID) (*domain.Market, error) {
	m, err := s.marketRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("market_service.GetMarketWithOdds: %w", err)
	}
	return m, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// ListMarkets / GetMarketHistory
// ──────────────────────────────────────────────────────────────────────────────

// ListMarkets returns a paginated list of markets.
// status="" returns all statuses.  Returns (markets, total, error).
func (s *MarketService) ListMarkets(ctx context.Context, limit, offset int, status string) ([]*domain.Market, int, error) {
	markets, total, err := s.marketRepo.List(ctx, limit, offset, status)
	if err != nil {
		return nil, 0, fmt.Errorf("market_service.ListMarkets: %w", err)
	}
	return markets, total, nil
}

// GetMarketHistory returns resolved/cancelled markets in descending order.
func (s *MarketService) GetMarketHistory(ctx context.Context, limit, offset int) ([]*domain.Market, error) {
	markets, err := s.marketRepo.GetHistory(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("market_service.GetMarketHistory: %w", err)
	}
	return markets, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Admin operations
// ──────────────────────────────────────────────────────────────────────────────

// SuspendMarket transitions a market to 'suspended'.  Only callable from the
// back-office admin layer.
func (s *MarketService) SuspendMarket(ctx context.Context, marketID uuid.UUID, reason string) error {
	if err := s.marketRepo.Suspend(ctx, marketID, reason); err != nil {
		return fmt.Errorf("market_service.SuspendMarket: %w", err)
	}
	s.invalidateActiveCache()
	return nil
}

// CancelMarket transitions a market to 'cancelled' and triggers a full refund
// of all active bets via the injected Refunder.
func (s *MarketService) CancelMarket(ctx context.Context, marketID uuid.UUID) error {
	if s.refunder == nil {
		return fmt.Errorf("market_service.CancelMarket: refunder not set (call SetRefunder first)")
	}

	// Refund all active bets first; if this fails, do NOT cancel the market.
	if err := s.refunder.RefundAll(ctx, marketID); err != nil {
		return fmt.Errorf("market_service.CancelMarket: refund: %w", err)
	}

	if err := s.marketRepo.Cancel(ctx, marketID); err != nil {
		return fmt.Errorf("market_service.CancelMarket: db: %w", err)
	}

	s.invalidateActiveCache()
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// GetSummary — convenience for WS broadcast
// ──────────────────────────────────────────────────────────────────────────────

// GetSummary returns a MarketSummary enriched with the current live price.
// Returns ErrNoOpenMarket if there is no active market.
func (s *MarketService) GetSummary(ctx context.Context) (*domain.MarketSummary, error) {
	m, err := s.GetActiveMarket(ctx)
	if err != nil {
		return nil, err
	}

	price, ok := s.priceService.GetCachedPrice()
	if !ok {
		// If cache is cold, do a fresh fetch (non-blocking: we accept the latency)
		price, _, err = s.priceService.GetWeightedPrice(ctx)
		if err != nil {
			return nil, fmt.Errorf("market_service.GetSummary: price fetch: %w", err)
		}
	}

	summary := m.ToSummary(price)
	return &summary, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func (s *MarketService) invalidateActiveCache() {
	s.activeMu.Lock()
	s.activeMarket = nil
	s.activeCacheTime = time.Time{}
	s.activeMu.Unlock()
}
