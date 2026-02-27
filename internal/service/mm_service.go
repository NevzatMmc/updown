package service

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// MMStats contains aggregated monitoring data for the Market Maker.
type MMStats struct {
	DailySpend         decimal.Decimal `json:"daily_spend"`         // total TRY injected today
	DailyPnL           decimal.Decimal `json:"daily_pnl"`           // realized profit - loss today
	TotalInterventions int             `json:"total_interventions"` // count today
	PlatformReserve    decimal.Decimal `json:"platform_reserve"`    // current balance
}

// ──────────────────────────────────────────────────────────────────────────────
// MMService
// ──────────────────────────────────────────────────────────────────────────────

// MMService implements the Rebalancer interface (declared in bet_service.go).
// It monitors pool imbalances and injects liquidity from the platform wallet
// to keep odds competitive on both sides.
type MMService struct {
	db         *sqlx.DB
	betRepo    *repository.BetRepository
	marketRepo *repository.MarketRepository
	walletRepo *repository.WalletRepository
	cfg        *config.Config
	mu         sync.Mutex // prevents concurrent rebalances for the same market
}

// NewMMService creates an MMService.
func NewMMService(
	db *sqlx.DB,
	betRepo *repository.BetRepository,
	marketRepo *repository.MarketRepository,
	walletRepo *repository.WalletRepository,
	cfg *config.Config,
) *MMService {
	return &MMService{
		db:         db,
		betRepo:    betRepo,
		marketRepo: marketRepo,
		walletRepo: walletRepo,
		cfg:        cfg,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Rebalance — implements Rebalancer interface
// ──────────────────────────────────────────────────────────────────────────────

// Rebalance inspects the current market pool and injects liquidity on the
// thin side if the imbalance exceeds the configured thresholds.
// Uses TryLock so overlapping async calls are silently skipped.
func (s *MMService) Rebalance(ctx context.Context, marketID uuid.UUID) error {
	// TryLock — if another rebalance is already running, skip this call.
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()

	// Load market and confirm it is still open.
	market, err := s.marketRepo.GetByID(ctx, marketID)
	if err != nil {
		return fmt.Errorf("mm_service.Rebalance: get market: %w", err)
	}
	if !market.IsOpen() {
		return nil // market already closed — nothing to do
	}

	up := market.PoolUp
	down := market.PoolDown

	// ── Decision tree ────────────────────────────────────────────────────────

	triggerThreshold := decimal.NewFromFloat(s.cfg.MM.TriggerThreshold)
	minBalanceRatio := decimal.NewFromFloat(0.20) // target thin side at 20 % of thick
	seedRatio := decimal.NewFromFloat(0.30)       // seed at 30 % of existing side
	minBet := decimal.NewFromFloat(s.cfg.MM.MinMMBet)

	switch {
	// (a) DOWN pool is completely empty, UP has bets → seed DOWN
	case down.IsZero() && !up.IsZero():
		seed := up.Mul(seedRatio).RoundDown(4)
		_ = s.placePlatformBet(ctx, marketID, domain.OutcomeDown, seed, "seed_down")

	// (b) UP pool is completely empty, DOWN has bets → seed UP
	case up.IsZero() && !down.IsZero():
		seed := down.Mul(seedRatio).RoundDown(4)
		_ = s.placePlatformBet(ctx, marketID, domain.OutcomeUp, seed, "seed_up")

	// (c) DOWN severely under-represented relative to UP
	case !up.IsZero() && down.Div(up).LessThan(triggerThreshold):
		target := up.Mul(minBalanceRatio).RoundDown(4)
		needed := target.Sub(down)
		if needed.GreaterThan(minBet) {
			_ = s.placePlatformBet(ctx, marketID, domain.OutcomeDown, needed, "rebalance_down")
		}

	// (d) UP severely under-represented relative to DOWN
	case !down.IsZero() && up.Div(down).LessThan(triggerThreshold):
		target := down.Mul(minBalanceRatio).RoundDown(4)
		needed := target.Sub(up)
		if needed.GreaterThan(minBet) {
			_ = s.placePlatformBet(ctx, marketID, domain.OutcomeUp, needed, "rebalance_up")
		}

	// (e) Pools are balanced — nothing to do
	default:
		return nil
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// placePlatformBet — guarded liquidity injection
// ──────────────────────────────────────────────────────────────────────────────

// placePlatformBet applies all limit guards, then deducts from the platform
// wallet, updates the market pool, and records the MM position — all within
// a single PostgreSQL transaction.
func (s *MMService) placePlatformBet(
	ctx context.Context,
	marketID uuid.UUID,
	outcome domain.Outcome,
	amount decimal.Decimal,
	reason string,
) error {
	minBet := decimal.NewFromFloat(s.cfg.MM.MinMMBet)
	maxExposure := decimal.NewFromFloat(s.cfg.MM.MaxExposurePerMarket)
	maxDailyLoss := decimal.NewFromFloat(s.cfg.MM.MaxDailyLoss)
	minReserve := decimal.NewFromFloat(s.cfg.MM.MinReserve)

	// Guard 1: below minimum bet size
	if amount.LessThan(minBet) {
		return nil
	}

	// Guard 2: daily loss limit
	dailyExposure, err := s.betRepo.GetPlatformDailyExposure(ctx)
	if err != nil {
		return fmt.Errorf("mm_service.placePlatformBet: daily exposure: %w", err)
	}
	if dailyExposure.Add(amount).GreaterThan(maxDailyLoss) {
		log.Printf("[mm] DAILY LOSS LIMIT REACHED: exposure=%s limit=%s — suspending MM",
			dailyExposure.StringFixed(4), maxDailyLoss.StringFixed(4))
		return domain.ErrMMDailyLossExceeded
	}

	// Guard 3: platform reserve check
	platformWallet, err := s.walletRepo.GetPlatformWallet(ctx)
	if err != nil {
		return fmt.Errorf("mm_service.placePlatformBet: platform wallet: %w", err)
	}
	if platformWallet.Balance.LessThan(minReserve.Add(amount)) {
		log.Printf("[mm] ALARM: platform reserve %s below minimum %s — MM blocked",
			platformWallet.Balance.StringFixed(4), minReserve.StringFixed(4))
		return domain.ErrMMReserveInsufficient
	}

	// Guard 4: per-market exposure cap
	marketExposure, err := s.walletRepo.GetMarketMMExposure(ctx, marketID)
	if err != nil {
		return fmt.Errorf("mm_service.placePlatformBet: market exposure: %w", err)
	}
	if marketExposure.Add(amount).GreaterThan(maxExposure) {
		log.Printf("[mm] per-market cap reached for market %s: exposure=%s cap=%s",
			marketID, marketExposure.StringFixed(4), maxExposure.StringFixed(4))
		return nil
	}

	// ── Atomic transaction ────────────────────────────────────────────────────
	tx, txErr := s.db.BeginTxx(ctx, nil)
	if txErr != nil {
		return fmt.Errorf("mm_service.placePlatformBet: begin tx: %w", txErr)
	}
	defer func() {
		if txErr != nil {
			_ = tx.Rollback()
		}
	}()

	// Deduct from platform wallet (FOR UPDATE internally)
	if txErr = s.walletRepo.DeductPlatformBalance(ctx, tx, amount); txErr != nil {
		return fmt.Errorf("mm_service.placePlatformBet: deduct platform: %w", txErr)
	}

	// Add amount to the market pool
	if txErr = s.marketRepo.UpdatePools(ctx, tx, marketID, outcome, amount); txErr != nil {
		return fmt.Errorf("mm_service.placePlatformBet: update pools: %w", txErr)
	}

	// Record the MM position (mm_positions table)
	if txErr = s.betRepo.CreatePlatformBet(ctx, marketID, outcome, amount, reason); txErr != nil {
		return fmt.Errorf("mm_service.placePlatformBet: create platform bet: %w", txErr)
	}

	if txErr = tx.Commit(); txErr != nil {
		return fmt.Errorf("mm_service.placePlatformBet: commit: %w", txErr)
	}

	log.Printf("[mm] injected %s TRY on %s for market %s (reason=%s)",
		amount.StringFixed(4), outcome, marketID, reason)
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// GetMMStats
// ──────────────────────────────────────────────────────────────────────────────

// GetMMStats returns aggregated MM statistics for the monitoring dashboard.
func (s *MMService) GetMMStats(ctx context.Context) (*MMStats, error) {
	// Daily spend = sum of all open + closed mm_positions today
	var dailySpend decimal.Decimal
	err := s.db.GetContext(ctx, &dailySpend, `
		SELECT COALESCE(SUM(amount), 0)
		FROM mm_positions
		WHERE created_at >= date_trunc('day', now())`)
	if err != nil {
		return nil, fmt.Errorf("mm_service.GetMMStats: daily spend: %w", err)
	}

	// Daily P&L = sum of pnl for positions closed today
	var dailyPnL decimal.Decimal
	err = s.db.GetContext(ctx, &dailyPnL, `
		SELECT COALESCE(SUM(pnl), 0)
		FROM mm_positions
		WHERE closed_at >= date_trunc('day', now())
		  AND status IN ('won', 'lost')`)
	if err != nil {
		return nil, fmt.Errorf("mm_service.GetMMStats: daily pnl: %w", err)
	}

	// Total interventions today
	var interventions int
	err = s.db.GetContext(ctx, &interventions, `
		SELECT COUNT(*)
		FROM mm_positions
		WHERE created_at >= date_trunc('day', now())`)
	if err != nil {
		return nil, fmt.Errorf("mm_service.GetMMStats: interventions: %w", err)
	}

	// Current platform reserve balance
	platformWallet, err := s.walletRepo.GetPlatformWallet(ctx)
	if err != nil {
		return nil, fmt.Errorf("mm_service.GetMMStats: platform wallet: %w", err)
	}

	return &MMStats{
		DailySpend:         dailySpend,
		DailyPnL:           dailyPnL,
		TotalInterventions: interventions,
		PlatformReserve:    platformWallet.Balance,
	}, nil
}
