package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// WalletRepository handles all database operations for Wallets and Transactions.
type WalletRepository struct {
	db *sqlx.DB
}

// NewWalletRepository creates a new WalletRepository.
func NewWalletRepository(db *sqlx.DB) *WalletRepository {
	return &WalletRepository{db: db}
}

// GetByUserID fetches the wallet belonging to a specific user.
func (r *WalletRepository) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.Wallet, error) {
	var w domain.Wallet
	err := r.db.GetContext(ctx, &w, `SELECT * FROM wallets WHERE user_id = $1`, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrWalletNotFound
		}
		return nil, fmt.Errorf("wallet_repo.GetByUserID: %w", err)
	}
	return &w, nil
}

// GetPlatformWallet fetches the house Market Maker wallet (wallet_type='platform_mm').
func (r *WalletRepository) GetPlatformWallet(ctx context.Context) (*domain.Wallet, error) {
	var w domain.Wallet
	err := r.db.GetContext(ctx, &w, `SELECT * FROM wallets WHERE wallet_type = 'platform_mm'`)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrWalletNotFound
		}
		return nil, fmt.Errorf("wallet_repo.GetPlatformWallet: %w", err)
	}
	return &w, nil
}

// DeductBalance subtracts amount from a user's balance inside a transaction.
// Uses FOR UPDATE to prevent races; returns ErrInsufficientBalance when the
// available balance (balance - locked) would go negative.
// `amount` must be a shopspring/decimal.Decimal (passed as interface{} for
// sqlx compatibility with the PostgreSQL driver's decimal handling).
func (r *WalletRepository) DeductBalance(ctx context.Context, tx *sqlx.Tx, userID uuid.UUID, amount decimal.Decimal) error {
	// Lock the row and check available balance atomically
	var available decimal.Decimal
	err := tx.GetContext(ctx, &available,
		`SELECT (balance - locked) FROM wallets WHERE user_id = $1 FOR UPDATE`,
		userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrWalletNotFound
		}
		return fmt.Errorf("wallet_repo.DeductBalance lock: %w", err)
	}

	if available.LessThan(amount) {
		return domain.ErrInsufficientBalance
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE wallets SET balance = balance - $1, updated_at = now() WHERE user_id = $2`,
		amount, userID)
	if err != nil {
		return fmt.Errorf("wallet_repo.DeductBalance update: %w", err)
	}
	return nil
}

// AddBalance credits amount to a user's wallet inside a transaction.
func (r *WalletRepository) AddBalance(ctx context.Context, tx *sqlx.Tx, userID uuid.UUID, amount decimal.Decimal) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE wallets SET balance = balance + $1, updated_at = now() WHERE user_id = $2`,
		amount, userID)
	if err != nil {
		return fmt.Errorf("wallet_repo.AddBalance: %w", err)
	}
	return nil
}

// LockBalance increments the locked field (funds reserved for an open bet).
func (r *WalletRepository) LockBalance(ctx context.Context, tx *sqlx.Tx, userID uuid.UUID, amount decimal.Decimal) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE wallets SET locked = locked + $1, updated_at = now() WHERE user_id = $2`,
		amount, userID)
	if err != nil {
		return fmt.Errorf("wallet_repo.LockBalance: %w", err)
	}
	return nil
}

// UnlockBalance decrements the locked field (when a bet is settled or cancelled).
func (r *WalletRepository) UnlockBalance(ctx context.Context, tx *sqlx.Tx, userID uuid.UUID, amount decimal.Decimal) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE wallets SET locked = GREATEST(locked - $1, 0), updated_at = now() WHERE user_id = $2`,
		amount, userID)
	if err != nil {
		return fmt.Errorf("wallet_repo.UnlockBalance: %w", err)
	}
	return nil
}

// LogTransaction inserts an audit record into wallet_transactions inside a transaction.
func (r *WalletRepository) LogTransaction(ctx context.Context, tx *sqlx.Tx, txn *domain.Transaction) error {
	query := `
		INSERT INTO wallet_transactions
			(id, wallet_id, type, amount, balance_before, balance_after, ref_id, description, created_at)
		VALUES
			(:id, :wallet_id, :type, :amount, :balance_before, :balance_after, :ref_id, :description, :created_at)`
	if _, err := tx.NamedExecContext(ctx, query, txn); err != nil {
		return fmt.Errorf("wallet_repo.LogTransaction: %w", err)
	}
	return nil
}

// GetTransactions returns paginated transaction history for a user's wallet.
func (r *WalletRepository) GetTransactions(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Transaction, error) {
	var txns []*domain.Transaction
	err := r.db.SelectContext(ctx, &txns, `
		SELECT wt.*
		FROM wallet_transactions wt
		JOIN wallets w ON w.id = wt.wallet_id
		WHERE w.user_id = $1
		ORDER BY wt.created_at DESC
		LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("wallet_repo.GetTransactions: %w", err)
	}
	return txns, nil
}

// GetDailyWithdrawTotal sums approved/completed withdrawals for a user today.
func (r *WalletRepository) GetDailyWithdrawTotal(ctx context.Context, userID uuid.UUID) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.db.GetContext(ctx, &total, `
		SELECT COALESCE(SUM(amount), 0)
		FROM wallet_transactions
		WHERE wallet_id = (SELECT id FROM wallets WHERE user_id = $1)
		  AND type = 'withdraw'
		  AND created_at >= date_trunc('day', now())`,
		userID)
	if err != nil {
		return decimal.Zero, fmt.Errorf("wallet_repo.GetDailyWithdrawTotal: %w", err)
	}
	return total, nil
}

// CreateWithdrawRequest inserts a new withdrawal request.
func (r *WalletRepository) CreateWithdrawRequest(ctx context.Context, req *domain.WithdrawRequest) error {
	query := `
		INSERT INTO withdraw_requests
			(id, user_id, amount, status, iban, note, requested_at)
		VALUES
			(:id, :user_id, :amount, :status, :iban, :note, :requested_at)`
	if _, err := r.db.NamedExecContext(ctx, query, req); err != nil {
		return fmt.Errorf("wallet_repo.CreateWithdrawRequest: %w", err)
	}
	return nil
}

// GetWithdrawRequests returns paginated withdraw requests filtered by status.
// status="" means all statuses.
func (r *WalletRepository) GetWithdrawRequests(ctx context.Context, status string, limit, offset int) ([]*domain.WithdrawRequest, error) {
	var reqs []*domain.WithdrawRequest
	var err error
	if status != "" {
		err = r.db.SelectContext(ctx, &reqs, `
			SELECT * FROM withdraw_requests
			WHERE status = $1
			ORDER BY requested_at DESC
			LIMIT $2 OFFSET $3`,
			status, limit, offset)
	} else {
		err = r.db.SelectContext(ctx, &reqs, `
			SELECT * FROM withdraw_requests
			ORDER BY requested_at DESC
			LIMIT $1 OFFSET $2`,
			limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("wallet_repo.GetWithdrawRequests: %w", err)
	}
	return reqs, nil
}

// UpdateWithdrawStatus changes the status of a withdrawal request (admin action).
func (r *WalletRepository) UpdateWithdrawStatus(ctx context.Context, id uuid.UUID, status domain.WithdrawStatus, adminNote string, adminID uuid.UUID) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE withdraw_requests
		SET status      = $1,
		    review_note = $2,
		    reviewed_by = $3,
		    reviewed_at = now()
		WHERE id = $4`,
		string(status), adminNote, adminID, id)
	if err != nil {
		return fmt.Errorf("wallet_repo.UpdateWithdrawStatus: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrWalletNotFound
	}
	return nil
}

// ── Platform wallet helpers ──────────────────────────────────────────────────
// The platform MM wallet has user_id = NULL; all operations use wallet_type.

// DeductPlatformBalance subtracts amount from the platform MM wallet inside a
// transaction.  Uses FOR UPDATE to prevent concurrent over-draws.
func (r *WalletRepository) DeductPlatformBalance(ctx context.Context, tx *sqlx.Tx, amount decimal.Decimal) error {
	var available decimal.Decimal
	err := tx.GetContext(ctx, &available,
		`SELECT balance FROM wallets WHERE wallet_type = 'platform_mm' FOR UPDATE`)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrWalletNotFound
		}
		return fmt.Errorf("wallet_repo.DeductPlatformBalance lock: %w", err)
	}
	if available.LessThan(amount) {
		return domain.ErrInsufficientBalance
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE wallets SET balance = balance - $1, updated_at = now() WHERE wallet_type = 'platform_mm'`,
		amount)
	if err != nil {
		return fmt.Errorf("wallet_repo.DeductPlatformBalance update: %w", err)
	}
	return nil
}

// AddPlatformBalance credits amount to the platform MM wallet inside a transaction.
func (r *WalletRepository) AddPlatformBalance(ctx context.Context, tx *sqlx.Tx, amount decimal.Decimal) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE wallets SET balance = balance + $1, updated_at = now() WHERE wallet_type = 'platform_mm'`,
		amount)
	if err != nil {
		return fmt.Errorf("wallet_repo.AddPlatformBalance: %w", err)
	}
	return nil
}

// GetMarketMMExposure returns the total open MM position amount for a market
// (sum of mm_positions.amount where status='open').
func (r *WalletRepository) GetMarketMMExposure(ctx context.Context, marketID uuid.UUID) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.db.GetContext(ctx, &total, `
		SELECT COALESCE(SUM(amount), 0)
		FROM mm_positions
		WHERE market_id = $1 AND status = 'open'`,
		marketID)
	if err != nil {
		return decimal.Zero, fmt.Errorf("wallet_repo.GetMarketMMExposure: %w", err)
	}
	return total, nil
}

// ── Admin helpers ─────────────────────────────────────────────────────────────

// AdminAdjustBalance applies a signed decimal adjustment to a user's balance
// directly (positive = credit, negative = debit).  Used only by back-office.
func (r *WalletRepository) AdminAdjustBalance(ctx context.Context, userID uuid.UUID, amount decimal.Decimal) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE wallets SET balance = balance + $1, updated_at = now() WHERE user_id = $2`,
		amount, userID)
	if err != nil {
		return fmt.Errorf("wallet_repo.AdminAdjustBalance: %w", err)
	}
	return nil
}

// LogTransactionDirect writes an audit record outside of a transaction (e.g.
// admin adjustments that run without an explicit tx).
func (r *WalletRepository) LogTransactionDirect(ctx context.Context, txn *domain.Transaction) error {
	query := `
		INSERT INTO wallet_transactions
			(id, wallet_id, type, amount, balance_before, balance_after, ref_id, description, created_at)
		VALUES
			(:id, :wallet_id, :type, :amount, :balance_before, :balance_after, :ref_id, :description, :created_at)`
	if _, err := r.db.NamedExecContext(ctx, query, txn); err != nil {
		return fmt.Errorf("wallet_repo.LogTransactionDirect: %w", err)
	}
	return nil
}

// GetPlatformTransactions returns recent wallet_transactions for the platform
// MM wallet, ordered by descending time.
func (r *WalletRepository) GetPlatformTransactions(ctx context.Context, limit, offset int) ([]*domain.Transaction, error) {
	var txns []*domain.Transaction
	err := r.db.SelectContext(ctx, &txns, `
		SELECT wt.*
		FROM wallet_transactions wt
		JOIN wallets w ON w.id = wt.wallet_id
		WHERE w.wallet_type = 'platform_mm'
		ORDER BY wt.created_at DESC
		LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("wallet_repo.GetPlatformTransactions: %w", err)
	}
	return txns, nil
}
