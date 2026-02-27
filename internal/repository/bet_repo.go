package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// BetRepository handles all database operations for Bets and MM platform bets.
type BetRepository struct {
	db *sqlx.DB
}

// NewBetRepository creates a new BetRepository.
func NewBetRepository(db *sqlx.DB) *BetRepository {
	return &BetRepository{db: db}
}

// Create inserts a new bet inside an existing transaction.
func (r *BetRepository) Create(ctx context.Context, tx *sqlx.Tx, b *domain.Bet) error {
	query := `
		INSERT INTO bets
			(id, user_id, market_id, direction, amount, odds_at_entry, status, placed_at)
		VALUES
			(:id, :user_id, :market_id, :direction, :amount, :odds_at_entry, :status, :placed_at)`
	if _, err := tx.NamedExecContext(ctx, query, b); err != nil {
		return fmt.Errorf("bet_repo.Create: %w", err)
	}
	return nil
}

// GetByID fetches a bet by its primary key.
func (r *BetRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Bet, error) {
	var b domain.Bet
	err := r.db.GetContext(ctx, &b, `SELECT * FROM bets WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrBetNotActive // treat missing bet as not-active
		}
		return nil, fmt.Errorf("bet_repo.GetByID: %w", err)
	}
	return &b, nil
}

// GetByMarketAndOutcome returns all bets in a market for a specific direction.
func (r *BetRepository) GetByMarketAndOutcome(ctx context.Context, marketID uuid.UUID, outcome domain.Outcome) ([]*domain.Bet, error) {
	var bets []*domain.Bet
	err := r.db.SelectContext(ctx, &bets,
		`SELECT * FROM bets WHERE market_id = $1 AND direction = $2 ORDER BY placed_at ASC`,
		marketID, string(outcome))
	if err != nil {
		return nil, fmt.Errorf("bet_repo.GetByMarketAndOutcome: %w", err)
	}
	return bets, nil
}

// GetByUserID returns a user's bet history, paginated.
func (r *BetRepository) GetByUserID(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Bet, error) {
	var bets []*domain.Bet
	err := r.db.SelectContext(ctx, &bets,
		`SELECT * FROM bets WHERE user_id = $1 ORDER BY placed_at DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("bet_repo.GetByUserID: %w", err)
	}
	return bets, nil
}

// GetActiveByMarket returns all bets in open status for a given market.
func (r *BetRepository) GetActiveByMarket(ctx context.Context, marketID uuid.UUID) ([]*domain.Bet, error) {
	var bets []*domain.Bet
	err := r.db.SelectContext(ctx, &bets,
		`SELECT * FROM bets WHERE market_id = $1 AND status = 'open' ORDER BY placed_at ASC`,
		marketID)
	if err != nil {
		return nil, fmt.Errorf("bet_repo.GetActiveByMarket: %w", err)
	}
	return bets, nil
}

// UpdateStatus sets the status and payout of a bet inside a transaction.
// Designed for use by the resolution service.
func (r *BetRepository) UpdateStatus(ctx context.Context, tx *sqlx.Tx, betID uuid.UUID, status domain.BetStatus, payout *decimal.Decimal) error {
	query := `
		UPDATE bets
		SET status      = $1,
		    payout      = $2,
		    resolved_at = now()
		WHERE id = $3`
	if _, err := tx.ExecContext(ctx, query, string(status), payout, betID); err != nil {
		return fmt.Errorf("bet_repo.UpdateStatus: %w", err)
	}
	return nil
}

// ExitBet marks a bet as cashed_out with its exit amount, inside a transaction.
// Only updates bets that are still active (status='open') to prevent double-exits.
func (r *BetRepository) ExitBet(ctx context.Context, tx *sqlx.Tx, betID uuid.UUID, exitAmount, cashoutFee decimal.Decimal) error {
	query := `
		UPDATE bets
		SET status         = 'cashed_out',
		    cashout_amount = $1,
		    cashout_fee    = $2,
		    resolved_at    = now()
		WHERE id = $3 AND status = 'open'`
	res, err := tx.ExecContext(ctx, query, exitAmount, cashoutFee, betID)
	if err != nil {
		return fmt.Errorf("bet_repo.ExitBet: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrBetNotActive
	}
	return nil
}

// GetPlatformDailyExposure sums the platform's open MM positions for today.
func (r *BetRepository) GetPlatformDailyExposure(ctx context.Context) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.db.GetContext(ctx, &total, `
		SELECT COALESCE(SUM(amount), 0)
		FROM mm_positions
		WHERE status = 'open'
		  AND created_at >= date_trunc('day', now())`)
	if err != nil {
		return decimal.Zero, fmt.Errorf("bet_repo.GetPlatformDailyExposure: %w", err)
	}
	return total, nil
}

// CreatePlatformBet inserts an MM liquidity injection record in mm_positions.
func (r *BetRepository) CreatePlatformBet(ctx context.Context, marketID uuid.UUID, outcome domain.Outcome, amount decimal.Decimal, reason string) error {
	query := `
		INSERT INTO mm_positions (market_id, direction, amount, status, created_at)
		VALUES ($1, $2, $3, 'open', $4)`
	if _, err := r.db.ExecContext(ctx, query, marketID, string(outcome), amount, time.Now()); err != nil {
		return fmt.Errorf("bet_repo.CreatePlatformBet: %w", err)
	}
	return nil
}

// GetMMLogsByMarket returns all MM positions for a given market.
func (r *BetRepository) GetMMLogsByMarket(ctx context.Context, marketID uuid.UUID) ([]*domain.MMLog, error) {
	var logs []*domain.MMLog
	err := r.db.SelectContext(ctx, &logs,
		`SELECT id, market_id, direction, amount, status, pnl, '' AS reason, created_at, closed_at
		 FROM mm_positions
		 WHERE market_id = $1
		 ORDER BY created_at ASC`,
		marketID)
	if err != nil {
		return nil, fmt.Errorf("bet_repo.GetMMLogsByMarket: %w", err)
	}
	return logs, nil
}

// UpdateStatusBulk sets status = lost (or refunded) for every open bet of a
// given direction in a market, inside the resolution transaction.
// Only touches bets that are still status='open' to avoid double-processing.
func (r *BetRepository) UpdateStatusBulk(ctx context.Context, tx *sqlx.Tx, marketID uuid.UUID, direction domain.Outcome, status domain.BetStatus) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE bets
		SET status      = $1,
		    resolved_at = now()
		WHERE market_id = $2
		  AND direction  = $3
		  AND status     = 'open'`,
		string(status), marketID, string(direction))
	if err != nil {
		return fmt.Errorf("bet_repo.UpdateStatusBulk: %w", err)
	}
	return nil
}

// UpdateMMPositionStatus closes an MM position after market resolution and
// records its profit-or-loss.  pnl is positive when the platform profited.
func (r *BetRepository) UpdateMMPositionStatus(ctx context.Context, tx *sqlx.Tx, positionID uuid.UUID, status string, pnl decimal.Decimal) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE mm_positions
		SET status    = $1,
		    pnl       = $2,
		    closed_at = now()
		WHERE id = $3`,
		status, pnl, positionID)
	if err != nil {
		return fmt.Errorf("bet_repo.UpdateMMPositionStatus: %w", err)
	}
	return nil
}
