package service

import (
	"context"
	"fmt"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// ──────────────────────────────────────────────────────────────────────────────
// Interfaces injected into BetService to avoid import cycles
// ──────────────────────────────────────────────────────────────────────────────

// Rebalancer is the minimal interface BetService needs from MMService.
// Implemented by MMService (Step 10).
type Rebalancer interface {
	Rebalance(ctx context.Context, marketID uuid.UUID) error
}

// Broadcaster is the minimal interface BetService needs from the WS hub.
// Implemented by ws.Hub (Step 12).
type Broadcaster interface {
	BroadcastMarketUpdate(summary *domain.MarketSummary)
}

// ──────────────────────────────────────────────────────────────────────────────
// BetService
// ──────────────────────────────────────────────────────────────────────────────

// BetService orchestrates bet placement and early cashout (bozdur).
// All money movement happens inside a single PostgreSQL transaction.
type BetService struct {
	db          *sqlx.DB
	betRepo     *repository.BetRepository
	marketRepo  *repository.MarketRepository
	walletRepo  *repository.WalletRepository
	cfg         *config.Config
	rebalancer  Rebalancer  // injected after MMService is built
	broadcaster Broadcaster // injected after WS Hub is built
}

// NewBetService creates a BetService.
func NewBetService(
	db *sqlx.DB,
	betRepo *repository.BetRepository,
	marketRepo *repository.MarketRepository,
	walletRepo *repository.WalletRepository,
	cfg *config.Config,
) *BetService {
	return &BetService{
		db:         db,
		betRepo:    betRepo,
		marketRepo: marketRepo,
		walletRepo: walletRepo,
		cfg:        cfg,
	}
}

// SetRebalancer injects the MMService dependency post-construction.
func (s *BetService) SetRebalancer(r Rebalancer) { s.rebalancer = r }

// SetBroadcaster injects the WS Hub dependency post-construction.
func (s *BetService) SetBroadcaster(b Broadcaster) { s.broadcaster = b }

// ──────────────────────────────────────────────────────────────────────────────
// PlaceBet
// ──────────────────────────────────────────────────────────────────────────────

// PlaceBet validates the request, atomically deducts the user's balance,
// updates the market pool, records the bet, and writes an audit log entry —
// all inside a single PostgreSQL transaction.
//
// After a successful commit it asynchronously triggers MM rebalancing and
// a WS broadcast of the updated odds.
func (s *BetService) PlaceBet(ctx context.Context, req domain.PlaceBetRequest) (*domain.Bet, error) {
	// ── 1. Input validation ──────────────────────────────────────────────────
	minBet := decimal.NewFromFloat(s.cfg.Wallet.CommissionRate). // reuse config sensibly
									Mul(decimal.Zero) // placeholder; overridden next line
	minBet = decimal.NewFromInt(10) // hard floor: 10 TRY minimum
	if req.Amount.LessThan(minBet) {
		return nil, domain.ErrBetTooSmall
	}
	if !req.Direction.IsValid() {
		return nil, domain.ErrInvalidOutcome
	}

	// ── 2. Begin transaction ─────────────────────────────────────────────────
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// ── 3. Lock wallet and check balance ─────────────────────────────────────
	// DeductBalance acquires FOR UPDATE internally, checks available balance,
	// and deducts atomically.
	if err = s.walletRepo.DeductBalance(ctx, tx, req.UserID, req.Amount); err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: deduct: %w", err)
	}

	// ── 4. Load market and verify it is still open ───────────────────────────
	market, err := s.marketRepo.GetByID(ctx, req.MarketID)
	if err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: get market: %w", err)
	}
	if !market.IsOpen() {
		return nil, domain.ErrMarketNotOpen
	}

	// ── 5. Capture current odds (before this bet changes the pool) ───────────
	oddsAtEntry := market.OddsFor(req.Direction)
	if oddsAtEntry.IsZero() {
		// Pool is empty on this side; seed with 1:1 odds so the bet can proceed
		oddsAtEntry = decimal.NewFromInt(1)
	}

	// ── 6. Update market pool ────────────────────────────────────────────────
	if err = s.marketRepo.UpdatePools(ctx, tx, req.MarketID, req.Direction, req.Amount); err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: update pools: %w", err)
	}

	// ── 7. Persist the bet ───────────────────────────────────────────────────
	now := time.Now().UTC()
	bet := &domain.Bet{
		ID:          uuid.New(),
		UserID:      req.UserID,
		MarketID:    req.MarketID,
		Direction:   req.Direction,
		Amount:      req.Amount,
		OddsAtEntry: oddsAtEntry,
		Status:      domain.BetStatusActive,
		PlacedAt:    now,
	}
	if err = s.betRepo.Create(ctx, tx, bet); err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: create bet: %w", err)
	}

	// ── 8. Audit log ─────────────────────────────────────────────────────────
	wallet, err := s.walletRepo.GetByUserID(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: get wallet for log: %w", err)
	}
	betIDCopy := bet.ID
	txn := &domain.Transaction{
		ID:            uuid.New(),
		WalletID:      wallet.ID,
		Type:          domain.TxBetLock,
		Amount:        req.Amount,
		BalanceBefore: wallet.Balance,
		BalanceAfter:  wallet.Balance.Sub(req.Amount),
		RefID:         &betIDCopy,
		Description:   fmt.Sprintf("Bet placed: %s", string(req.Direction)),
		CreatedAt:     now,
	}
	if err = s.walletRepo.LogTransaction(ctx, tx, txn); err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: log tx: %w", err)
	}

	// ── 9. Commit ─────────────────────────────────────────────────────────────
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("bet_service.PlaceBet: commit: %w", err)
	}

	// ── 10. Async: MM rebalance + WS broadcast ────────────────────────────────
	go s.postBetAsync(req.MarketID)

	return bet, nil
}

// postBetAsync triggers MM rebalancing and a WS odds broadcast after a bet.
// Runs in a goroutine; errors are intentionally swallowed (monitoring via logs).
func (s *BetService) postBetAsync(marketID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if s.rebalancer != nil {
		if err := s.rebalancer.Rebalance(ctx, marketID); err != nil {
			_ = err // logged by MMService internally
		}
	}

	if s.broadcaster != nil {
		// Fetch refreshed market for updated odds
		market, err := s.marketRepo.GetByID(ctx, marketID)
		if err == nil {
			summary := market.ToSummary(decimal.Zero) // price filled by caller if needed
			s.broadcaster.BroadcastMarketUpdate(&summary)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ExitBet  (Bozdur)
// ──────────────────────────────────────────────────────────────────────────────

// ExitBet lets a user cash out an active bet before market resolution.
// The payout is calculated using the current odds minus a 5 % cashout fee.
// The transaction is idempotent: if the bet is already exited the repo
// returns ErrBetNotActive.
func (s *BetService) ExitBet(ctx context.Context, betID uuid.UUID, userID uuid.UUID) (*domain.Bet, error) {
	// ── 1. Load and validate bet ─────────────────────────────────────────────
	bet, err := s.betRepo.GetByID(ctx, betID)
	if err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: get bet: %w", err)
	}
	if bet.UserID != userID {
		return nil, domain.ErrForbidden
	}
	if !bet.IsActive() {
		return nil, domain.ErrBetNotActive
	}

	// ── 2. Load market and confirm it is still open ──────────────────────────
	market, err := s.marketRepo.GetByID(ctx, bet.MarketID)
	if err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: get market: %w", err)
	}
	if !market.IsOpen() {
		return nil, domain.ErrMarketNotOpen
	}

	// ── 3. Calculate exit amount (includes 5 % cashout fee) ──────────────────
	currentOdds := market.OddsFor(bet.Direction)
	if currentOdds.IsZero() {
		currentOdds = decimal.NewFromInt(1)
	}
	exitAmount := bet.CalculateExitAmount(currentOdds)
	cashoutFee := bet.CalculateExitFee(currentOdds)

	// ── 4. Begin transaction ─────────────────────────────────────────────────
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// ── 5. Mark bet as cashed_out (idempotent — WHERE status='open') ─────────
	if err = s.betRepo.ExitBet(ctx, tx, betID, exitAmount, cashoutFee); err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: exit bet: %w", err)
	}

	// ── 6. Remove original stake from market pool ────────────────────────────
	// Pass as negative to subtract from the pool
	negAmount := bet.Amount.Neg()
	if err = s.marketRepo.UpdatePools(ctx, tx, bet.MarketID, bet.Direction, negAmount); err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: update pools: %w", err)
	}

	// ── 7. Credit exit amount to user wallet ─────────────────────────────────
	if err = s.walletRepo.AddBalance(ctx, tx, userID, exitAmount); err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: add balance: %w", err)
	}

	// ── 8. Audit log ─────────────────────────────────────────────────────────
	wallet, err := s.walletRepo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: get wallet for log: %w", err)
	}
	now := time.Now().UTC()
	betIDCopy := betID
	cashoutTxn := &domain.Transaction{
		ID:            uuid.New(),
		WalletID:      wallet.ID,
		Type:          domain.TxCashout,
		Amount:        exitAmount,
		BalanceBefore: wallet.Balance,
		BalanceAfter:  wallet.Balance.Add(exitAmount),
		RefID:         &betIDCopy,
		Description:   fmt.Sprintf("Bet cashed out: %s, fee: %s TRY", string(bet.Direction), cashoutFee.StringFixed(4)),
		CreatedAt:     now,
	}
	if err = s.walletRepo.LogTransaction(ctx, tx, cashoutTxn); err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: log tx: %w", err)
	}

	// ── 9. Commit ────────────────────────────────────────────────────────────
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("bet_service.ExitBet: commit: %w", err)
	}

	// Reload the fully updated bet to return accurate fields.
	updated, loadErr := s.betRepo.GetByID(ctx, betID)
	if loadErr != nil {
		// Commit already succeeded; return the in-memory version with mutations.
		bet.Status = domain.BetStatusExited
		cashoutAmtCopy := exitAmount
		feeCopy := cashoutFee
		bet.CashoutAmount = &cashoutAmtCopy
		bet.CashoutFee = &feeCopy
		return bet, nil
	}

	return updated, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Query helpers
// ──────────────────────────────────────────────────────────────────────────────

// GetMyBets returns paginated bets for a user.
func (s *BetService) GetMyBets(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Bet, error) {
	bets, err := s.betRepo.GetByUserID(ctx, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("bet_service.GetMyBets: %w", err)
	}
	return bets, nil
}

// GetBetByID returns a single bet only if it belongs to userID.
func (s *BetService) GetBetByID(ctx context.Context, betID uuid.UUID, userID uuid.UUID) (*domain.Bet, error) {
	bet, err := s.betRepo.GetByID(ctx, betID)
	if err != nil {
		return nil, fmt.Errorf("bet_service.GetBetByID: %w", err)
	}
	if bet.UserID != userID {
		return nil, domain.ErrForbidden
	}
	return bet, nil
}
