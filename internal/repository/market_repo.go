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
)

// MarketRepository handles all database operations for Markets.
type MarketRepository struct {
	db *sqlx.DB
}

// NewMarketRepository creates a new MarketRepository.
func NewMarketRepository(db *sqlx.DB) *MarketRepository {
	return &MarketRepository{db: db}
}

// Create inserts a new market row.
func (r *MarketRepository) Create(ctx context.Context, m *domain.Market) error {
	query := `
		INSERT INTO markets
			(id, status, open_price, pool_up, pool_down, commission_taken, opens_at, closes_at, created_at, updated_at)
		VALUES
			(:id, :status, :open_price, :pool_up, :pool_down, :commission_taken, :opens_at, :closes_at, :created_at, :updated_at)`
	_, err := r.db.NamedExecContext(ctx, query, m)
	if err != nil {
		return fmt.Errorf("market_repo.Create: %w", err)
	}
	return nil
}

// GetByID fetches a market by its primary key.
func (r *MarketRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Market, error) {
	var m domain.Market
	err := r.db.GetContext(ctx, &m, `SELECT * FROM markets WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrMarketNotFound
		}
		return nil, fmt.Errorf("market_repo.GetByID: %w", err)
	}
	return &m, nil
}

// GetActive returns the single market currently in StatusOpen.
// Returns ErrNoOpenMarket when none exists.
func (r *MarketRepository) GetActive(ctx context.Context) (*domain.Market, error) {
	var m domain.Market
	err := r.db.GetContext(ctx, &m,
		`SELECT * FROM markets WHERE status = 'open' ORDER BY opens_at DESC LIMIT 1`)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNoOpenMarket
		}
		return nil, fmt.Errorf("market_repo.GetActive: %w", err)
	}
	return &m, nil
}

// GetExpiredUnresolved returns all markets that are still StatusOpen but whose
// closing time has passed (i.e. due for resolution).
func (r *MarketRepository) GetExpiredUnresolved(ctx context.Context, now time.Time) ([]*domain.Market, error) {
	var markets []*domain.Market
	err := r.db.SelectContext(ctx, &markets,
		`SELECT * FROM markets WHERE status = 'open' AND closes_at <= $1 ORDER BY closes_at ASC`,
		now)
	if err != nil {
		return nil, fmt.Errorf("market_repo.GetExpiredUnresolved: %w", err)
	}
	return markets, nil
}

// UpdatePools increments pool_up or pool_down for market by amount within an
// existing transaction. Uses FOR UPDATE to prevent race conditions.
func (r *MarketRepository) UpdatePools(ctx context.Context, tx *sqlx.Tx, marketID uuid.UUID, outcome domain.Outcome, amount interface{}) error {
	// Lock the row first
	var mktID uuid.UUID
	err := tx.GetContext(ctx, &mktID,
		`SELECT id FROM markets WHERE id = $1 AND status = 'open' FOR UPDATE`,
		marketID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrMarketNotOpen
		}
		return fmt.Errorf("market_repo.UpdatePools lock: %w", err)
	}

	var query string
	if outcome == domain.OutcomeUp {
		query = `UPDATE markets SET pool_up   = pool_up   + $1, updated_at = now() WHERE id = $2`
	} else {
		query = `UPDATE markets SET pool_down = pool_down + $1, updated_at = now() WHERE id = $2`
	}
	if _, err = tx.ExecContext(ctx, query, amount, marketID); err != nil {
		return fmt.Errorf("market_repo.UpdatePools update: %w", err)
	}
	return nil
}

// Resolve sets close_price, result, status=resolved and resolved_at.
func (r *MarketRepository) Resolve(ctx context.Context, marketID uuid.UUID, closePrice interface{}, winner domain.Outcome) error {
	query := `
		UPDATE markets
		SET status      = 'resolved',
		    close_price = $1,
		    result      = $2,
		    resolved_at = now(),
		    updated_at  = now()
		WHERE id = $3 AND status IN ('open','closed')`
	res, err := r.db.ExecContext(ctx, query, closePrice, string(winner), marketID)
	if err != nil {
		return fmt.Errorf("market_repo.Resolve: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrMarketNotFound
	}
	return nil
}

// Suspend sets the market status to suspended.
func (r *MarketRepository) Suspend(ctx context.Context, marketID uuid.UUID, reason string) error {
	query := `
		UPDATE markets
		SET status = 'suspended', updated_at = now()
		WHERE id = $1 AND status = 'open'`
	res, err := r.db.ExecContext(ctx, query, marketID)
	if err != nil {
		return fmt.Errorf("market_repo.Suspend: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrMarketNotFound
	}
	return nil
}

// Cancel marks the market as cancelled (all bets should be refunded by caller).
func (r *MarketRepository) Cancel(ctx context.Context, marketID uuid.UUID) error {
	query := `
		UPDATE markets
		SET status = 'cancelled', updated_at = now()
		WHERE id = $1 AND status NOT IN ('resolved','cancelled')`
	res, err := r.db.ExecContext(ctx, query, marketID)
	if err != nil {
		return fmt.Errorf("market_repo.Cancel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrMarketNotFound
	}
	return nil
}

// List returns a paginated slice of markets filtered by optional status.
// status="" returns all statuses.
// Returns (markets, totalCount, error).
func (r *MarketRepository) List(ctx context.Context, limit, offset int, status string) ([]*domain.Market, int, error) {
	var markets []*domain.Market
	var total int

	if status != "" {
		if err := r.db.GetContext(ctx, &total,
			`SELECT COUNT(*) FROM markets WHERE status = $1`, status); err != nil {
			return nil, 0, fmt.Errorf("market_repo.List count: %w", err)
		}
		if err := r.db.SelectContext(ctx, &markets,
			`SELECT * FROM markets WHERE status = $1 ORDER BY opens_at DESC LIMIT $2 OFFSET $3`,
			status, limit, offset); err != nil {
			return nil, 0, fmt.Errorf("market_repo.List select: %w", err)
		}
	} else {
		if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM markets`); err != nil {
			return nil, 0, fmt.Errorf("market_repo.List count: %w", err)
		}
		if err := r.db.SelectContext(ctx, &markets,
			`SELECT * FROM markets ORDER BY opens_at DESC LIMIT $1 OFFSET $2`,
			limit, offset); err != nil {
			return nil, 0, fmt.Errorf("market_repo.List select: %w", err)
		}
	}
	return markets, total, nil
}

// GetHistory returns closed/resolved markets in descending time order.
func (r *MarketRepository) GetHistory(ctx context.Context, limit, offset int) ([]*domain.Market, error) {
	var markets []*domain.Market
	err := r.db.SelectContext(ctx, &markets,
		`SELECT * FROM markets
		 WHERE status IN ('resolved','cancelled')
		 ORDER BY closes_at DESC
		 LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("market_repo.GetHistory: %w", err)
	}
	return markets, nil
}

// FinanceReport holds aggregated financial data for a date range.
type FinanceReport struct {
	From             time.Time `json:"from"`
	To               time.Time `json:"to"`
	CommissionEarned string    `json:"commission_earned"`
	MMPnL            string    `json:"mm_pnl"`
	CashoutFees      string    `json:"cashout_fees"`
	NetProfit        string    `json:"net_profit"`
	TotalUpPool      string    `json:"total_up_pool"`
	TotalDownPool    string    `json:"total_down_pool"`
	TotalVolume      string    `json:"total_volume"`
	MarketCount      int       `json:"market_count"`
}

// GetFinanceReport aggregates house_treasury and market data for a date range.
func (r *MarketRepository) GetFinanceReport(ctx context.Context, from, to time.Time) (*FinanceReport, error) {
	type row struct {
		CommissionEarned string `db:"commission_earned"`
		MMPnL            string `db:"mm_pnl"`
		CashoutFees      string `db:"cashout_fees"`
	}
	var fin row
	err := r.db.GetContext(ctx, &fin, `
		SELECT
			COALESCE(SUM(commission_earned), 0)::text AS commission_earned,
			COALESCE(SUM(mm_pnl), 0)::text            AS mm_pnl,
			COALESCE(SUM(cashout_fees_earned), 0)::text AS cashout_fees
		FROM house_treasury
		WHERE created_at >= $1 AND created_at < $2`,
		from, to)
	if err != nil {
		return nil, fmt.Errorf("market_repo.GetFinanceReport treasury: %w", err)
	}

	type mrow struct {
		TotalUp   string `db:"total_up"`
		TotalDown string `db:"total_down"`
		Count     int    `db:"count"`
	}
	var mdata mrow
	err = r.db.GetContext(ctx, &mdata, `
		SELECT
			COALESCE(SUM(pool_up), 0)::text   AS total_up,
			COALESCE(SUM(pool_down), 0)::text AS total_down,
			COUNT(*)                           AS count
		FROM markets
		WHERE status = 'resolved'
		  AND closes_at >= $1 AND closes_at < $2`,
		from, to)
	if err != nil {
		return nil, fmt.Errorf("market_repo.GetFinanceReport markets: %w", err)
	}

	// Net profit = commission + cashout fees + mm_pnl
	// All values kept as strings to preserve decimal precision for JSON.
	return &FinanceReport{
		From:             from,
		To:               to,
		CommissionEarned: fin.CommissionEarned,
		MMPnL:            fin.MMPnL,
		CashoutFees:      fin.CashoutFees,
		TotalUpPool:      mdata.TotalUp,
		TotalDownPool:    mdata.TotalDown,
		MarketCount:      mdata.Count,
	}, nil
}
