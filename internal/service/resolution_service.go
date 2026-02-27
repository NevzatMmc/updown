package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// ResolutionService handles market settlement: determines the winner,
// distributes pari-mutuel payouts, settles MM positions, and handles
// full refunds when a market is cancelled.
//
// It implements the Refunder interface declared in market_service.go.
type ResolutionService struct {
	db           *sqlx.DB
	marketRepo   *repository.MarketRepository
	betRepo      *repository.BetRepository
	walletRepo   *repository.WalletRepository
	priceService *PriceService
	cfg          *config.Config
}

// NewResolutionService builds a ResolutionService.
func NewResolutionService(
	db *sqlx.DB,
	marketRepo *repository.MarketRepository,
	betRepo *repository.BetRepository,
	walletRepo *repository.WalletRepository,
	priceService *PriceService,
	cfg *config.Config,
) *ResolutionService {
	return &ResolutionService{
		db:           db,
		marketRepo:   marketRepo,
		betRepo:      betRepo,
		walletRepo:   walletRepo,
		priceService: priceService,
		cfg:          cfg,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ResolveExpiredMarkets — called by the Scheduler every tick
// ──────────────────────────────────────────────────────────────────────────────

// ResolveExpiredMarkets fetches every market whose closing time has passed but
// is still in StatusOpen, and resolves each one.  A single failing market does
// NOT abort the others.
func (s *ResolutionService) ResolveExpiredMarkets(ctx context.Context) error {
	markets, err := s.marketRepo.GetExpiredUnresolved(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("resolution_service.ResolveExpiredMarkets: fetch: %w", err)
	}

	for _, m := range markets {
		if err := s.resolveMarket(ctx, m); err != nil {
			log.Printf("[resolution] ERROR resolving market %s: %v", m.ID, err)
			// Continue: do not block other markets because one failed.
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// resolveMarket — core settlement logic for a single market
// ──────────────────────────────────────────────────────────────────────────────

func (s *ResolutionService) resolveMarket(ctx context.Context, market *domain.Market) error {
	// ── Step 1: Fetch closing price ──────────────────────────────────────────
	closePrice, _, err := s.priceService.GetWeightedPrice(ctx)
	if err != nil {
		// Price feed failure → suspend market, do NOT resolve
		suspendErr := s.marketRepo.Suspend(ctx, market.ID, "price_source_error")
		if suspendErr != nil {
			log.Printf("[resolution] WARN: could not suspend market %s after price failure: %v", market.ID, suspendErr)
		}
		return fmt.Errorf("resolution_service.resolveMarket %s: price: %w", market.ID, err)
	}

	// ── Step 2: Determine winner ─────────────────────────────────────────────
	var winner domain.Outcome
	if market.OpenPrice == nil || closePrice.GreaterThanOrEqual(*market.OpenPrice) {
		winner = domain.OutcomeUp
	} else {
		winner = domain.OutcomeDown
	}
	loser := domain.OutcomeDown
	if winner == domain.OutcomeDown {
		loser = domain.OutcomeUp
	}

	// ── Step 3: Pool arithmetic ──────────────────────────────────────────────
	commission := decimal.NewFromFloat(s.cfg.Wallet.CommissionRate)
	one := decimal.NewFromInt(1)

	winnerPool := market.PoolUp
	loserPool := market.PoolDown
	if winner == domain.OutcomeDown {
		winnerPool = market.PoolDown
		loserPool = market.PoolUp
	}

	totalPool := market.TotalPool()
	commissionAmt := totalPool.Mul(commission)
	distributable := loserPool.Mul(one.Sub(commission)) // loser pool after commission

	// ── Step 4: Fetch winning user bets ─────────────────────────────────────
	winningBets, err := s.betRepo.GetByMarketAndOutcome(ctx, market.ID, winner)
	if err != nil {
		return fmt.Errorf("resolution_service.resolveMarket: get winning bets: %w", err)
	}

	// ── Step 5: Atomic settlement transaction ────────────────────────────────
	tx, txErr := s.db.BeginTxx(ctx, nil)
	if txErr != nil {
		return fmt.Errorf("resolution_service.resolveMarket: begin tx: %w", txErr)
	}
	defer func() {
		if txErr != nil {
			_ = tx.Rollback()
		}
	}()

	// --- Pay out winners ----------------------------------------------------
	for _, bet := range winningBets {
		payout, payErr := s.calculatePayout(bet, winnerPool, distributable)
		if payErr != nil {
			txErr = fmt.Errorf("calculate payout for bet %s: %w", bet.ID, payErr)
			return txErr
		}

		if txErr = s.walletRepo.AddBalance(ctx, tx, bet.UserID, payout); txErr != nil {
			return fmt.Errorf("resolution_service: add balance (bet %s): %w", bet.ID, txErr)
		}

		now := time.Now().UTC()
		betIDCopy := bet.ID
		wallet, wErr := s.walletRepo.GetByUserID(ctx, bet.UserID)
		if wErr != nil {
			txErr = fmt.Errorf("resolution_service: get wallet (bet %s): %w", bet.ID, wErr)
			return txErr
		}
		txn := &domain.Transaction{
			ID:            uuid.New(),
			WalletID:      wallet.ID,
			Type:          domain.TxPayout,
			Amount:        payout,
			BalanceBefore: wallet.Balance,
			BalanceAfter:  wallet.Balance.Add(payout),
			RefID:         &betIDCopy,
			Description:   fmt.Sprintf("Payout: market %s, won %s TRY", market.ID, payout.StringFixed(4)),
			CreatedAt:     now,
		}
		if txErr = s.walletRepo.LogTransaction(ctx, tx, txn); txErr != nil {
			return fmt.Errorf("resolution_service: log payout tx (bet %s): %w", bet.ID, txErr)
		}

		payoutCopy := payout
		if txErr = s.betRepo.UpdateStatus(ctx, tx, bet.ID, domain.BetStatusWon, &payoutCopy); txErr != nil {
			return fmt.Errorf("resolution_service: mark bet won %s: %w", bet.ID, txErr)
		}
	}

	// --- Bulk-mark loser bets -----------------------------------------------
	if txErr = s.betRepo.UpdateStatusBulk(ctx, tx, market.ID, loser, domain.BetStatusLost); txErr != nil {
		return fmt.Errorf("resolution_service: bulk mark lost: %w", txErr)
	}

	// --- Settle MM platform positions ---------------------------------------
	if txErr = s.resolvePlatformBets(ctx, tx, market, winner, winnerPool, distributable); txErr != nil {
		return fmt.Errorf("resolution_service: settle platform bets: %w", txErr)
	}

	// --- Record commission in house treasury --------------------------------
	if txErr = s.recordCommission(ctx, tx, market.ID, commissionAmt); txErr != nil {
		return fmt.Errorf("resolution_service: record commission: %w", txErr)
	}

	// --- Close the market ---------------------------------------------------
	if txErr = s.marketRepo.Resolve(ctx, market.ID, closePrice, winner); txErr != nil {
		return fmt.Errorf("resolution_service: resolve market: %w", txErr)
	}

	if txErr = tx.Commit(); txErr != nil {
		return fmt.Errorf("resolution_service.resolveMarket: commit: %w", txErr)
	}

	log.Printf("[resolution] market %s resolved: winner=%s close=%.2f commission=%s",
		market.ID, winner, closePrice.InexactFloat64(), commissionAmt.StringFixed(4))

	return nil
}

// ── Payout helper ────────────────────────────────────────────────────────────

// calculatePayout computes refund + profit for a single winning bet.
//
//	share      = bet.Amount / winnerPool
//	profit     = share × distributable
//	payout     = bet.Amount + profit
func (s *ResolutionService) calculatePayout(bet *domain.Bet, winnerPool, distributable decimal.Decimal) (decimal.Decimal, error) {
	if winnerPool.IsZero() {
		return decimal.Zero, fmt.Errorf("winner pool is zero for bet %s", bet.ID)
	}
	share := bet.Amount.Div(winnerPool)
	profit := share.Mul(distributable)
	return bet.Amount.Add(profit).RoundDown(4), nil
}

// ── Platform MM position settlement ─────────────────────────────────────────

// resolvePlatformBets iterates over all open mm_positions for the market and
// credits / debits the platform MM wallet accordingly.
func (s *ResolutionService) resolvePlatformBets(
	ctx context.Context,
	tx *sqlx.Tx,
	market *domain.Market,
	winner domain.Outcome,
	winnerPool, distributable decimal.Decimal,
) error {
	positions, err := s.betRepo.GetMMLogsByMarket(ctx, market.ID)
	if err != nil {
		return fmt.Errorf("resolvePlatformBets: get positions: %w", err)
	}

	platformWallet, err := s.walletRepo.GetPlatformWallet(ctx)
	if err != nil {
		return fmt.Errorf("resolvePlatformBets: get platform wallet: %w", err)
	}

	for _, pos := range positions {
		if pos.Status != "open" {
			continue
		}

		var pnl decimal.Decimal
		var finalStatus string

		if pos.Direction == winner {
			// Platform bet on the winning side → payout
			payout, calcErr := s.calculatePayout(
				&domain.Bet{Amount: pos.Amount, ID: pos.ID, UserID: platformWallet.UserID},
				winnerPool, distributable,
			)
			if calcErr != nil {
				return fmt.Errorf("resolvePlatformBets: calc payout pos %s: %w", pos.ID, calcErr)
			}
			pnl = payout.Sub(pos.Amount)
			finalStatus = "won"
			if err = s.walletRepo.AddBalance(ctx, tx, platformWallet.UserID, payout); err != nil {
				return fmt.Errorf("resolvePlatformBets: add win to platform wallet: %w", err)
			}
		} else {
			// Platform bet on the losing side → loss equals the stake
			pnl = pos.Amount.Neg()
			finalStatus = "lost"
			// No balance adjustment needed: the stake was already deducted when
			// MM injected liquidity (Step 10 will handle that).
		}

		if err = s.betRepo.UpdateMMPositionStatus(ctx, tx, pos.ID, finalStatus, pnl); err != nil {
			return fmt.Errorf("resolvePlatformBets: update position %s: %w", pos.ID, err)
		}
	}

	return nil
}

// ── Commission ledger ────────────────────────────────────────────────────────

func (s *ResolutionService) recordCommission(ctx context.Context, tx *sqlx.Tx, marketID uuid.UUID, amount decimal.Decimal) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO house_treasury (market_id, commission_earned, mm_pnl, cashout_fees_earned, created_at)
		VALUES ($1, $2, 0, 0, now())
		ON CONFLICT DO NOTHING`,
		marketID, amount)
	if err != nil {
		return fmt.Errorf("recordCommission: %w", err)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// RefundAll — implements the Refunder interface (used by MarketService.CancelMarket)
// ──────────────────────────────────────────────────────────────────────────────

// RefundAll refunds every active bet in a cancelled market back to their owners.
// All refunds happen inside a single transaction so the operation is atomic.
func (s *ResolutionService) RefundAll(ctx context.Context, marketID uuid.UUID) error {
	activeBets, err := s.betRepo.GetActiveByMarket(ctx, marketID)
	if err != nil {
		return fmt.Errorf("resolution_service.RefundAll: get active bets: %w", err)
	}
	if len(activeBets) == 0 {
		return nil // nothing to refund
	}

	tx, txErr := s.db.BeginTxx(ctx, nil)
	if txErr != nil {
		return fmt.Errorf("resolution_service.RefundAll: begin tx: %w", txErr)
	}
	defer func() {
		if txErr != nil {
			_ = tx.Rollback()
		}
	}()

	for _, bet := range activeBets {
		// Credit original stake back to user
		if txErr = s.walletRepo.AddBalance(ctx, tx, bet.UserID, bet.Amount); txErr != nil {
			return fmt.Errorf("resolution_service.RefundAll: add balance (bet %s): %w", bet.ID, txErr)
		}

		// Audit record
		wallet, wErr := s.walletRepo.GetByUserID(ctx, bet.UserID)
		if wErr != nil {
			txErr = wErr
			return fmt.Errorf("resolution_service.RefundAll: get wallet (bet %s): %w", bet.ID, wErr)
		}
		now := time.Now().UTC()
		betIDCopy := bet.ID
		txn := &domain.Transaction{
			ID:            uuid.New(),
			WalletID:      wallet.ID,
			Type:          domain.TxRefund,
			Amount:        bet.Amount,
			BalanceBefore: wallet.Balance,
			BalanceAfter:  wallet.Balance.Add(bet.Amount),
			RefID:         &betIDCopy,
			Description:   fmt.Sprintf("Refund: market %s cancelled", marketID),
			CreatedAt:     now,
		}
		if txErr = s.walletRepo.LogTransaction(ctx, tx, txn); txErr != nil {
			return fmt.Errorf("resolution_service.RefundAll: log refund tx (bet %s): %w", bet.ID, txErr)
		}

		// Mark bet as refunded (cancelled)
		zero := decimal.Zero
		if txErr = s.betRepo.UpdateStatus(ctx, tx, bet.ID, domain.BetStatusRefunded, &zero); txErr != nil {
			return fmt.Errorf("resolution_service.RefundAll: mark refunded (bet %s): %w", bet.ID, txErr)
		}
	}

	if txErr = tx.Commit(); txErr != nil {
		return fmt.Errorf("resolution_service.RefundAll: commit: %w", txErr)
	}

	log.Printf("[resolution] RefundAll: refunded %d bets for cancelled market %s", len(activeBets), marketID)
	return nil
}
