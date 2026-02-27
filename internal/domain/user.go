package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ──────────────────────────────────────────────────────────────────────────────
// UserRole
// ──────────────────────────────────────────────────────────────────────────────

// UserRole controls access levels in the back-office.
type UserRole string

const (
	RoleUser     UserRole = "user"     // standard bettor
	RoleAdmin    UserRole = "admin"    // full back-office access
	RoleRisk     UserRole = "risk"     // risk management view
	RoleFinance  UserRole = "finance"  // financial reports, withdrawals
	RoleOps      UserRole = "ops"      // operations: market management
	RoleReadOnly UserRole = "readonly" // read-only back-office access
)

// CanAccessBackoffice returns true for all non-standard roles.
func (r UserRole) CanAccessBackoffice() bool {
	return r != RoleUser
}

// IsAdmin returns true only for the full admin role.
func (r UserRole) IsAdmin() bool {
	return r == RoleAdmin
}

// ──────────────────────────────────────────────────────────────────────────────
// User
// ──────────────────────────────────────────────────────────────────────────────

// User is the domain entity for registered accounts.
type User struct {
	ID           uuid.UUID `json:"id"         db:"id"`
	Email        string    `json:"email"      db:"email"`
	Username     string    `json:"username"   db:"username"`
	PasswordHash string    `json:"-"          db:"password_hash"` // never serialised
	Role         UserRole  `json:"role"       db:"role"`
	IsActive     bool      `json:"is_active"  db:"is_active"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

// PublicProfile returns a user view safe to expose via API (no password hash).
type PublicProfile struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	Role      UserRole  `json:"role"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

// ToPublicProfile converts a User to its public-safe representation.
func (u *User) ToPublicProfile() PublicProfile {
	return PublicProfile{
		ID:        u.ID,
		Email:     u.Email,
		Username:  u.Username,
		Role:      u.Role,
		IsActive:  u.IsActive,
		CreatedAt: u.CreatedAt,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Wallet
// ──────────────────────────────────────────────────────────────────────────────

// Wallet holds a user's TRY balance.
type Wallet struct {
	ID         uuid.UUID       `json:"id"          db:"id"`
	UserID     uuid.UUID       `json:"user_id"     db:"user_id"`
	WalletType *string         `json:"wallet_type" db:"wallet_type"` // NULL=user, 'platform_mm'=house
	Balance    decimal.Decimal `json:"balance"     db:"balance"`
	Locked     decimal.Decimal `json:"locked"      db:"locked"` // reserved for open bets
	CreatedAt  time.Time       `json:"created_at"  db:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"  db:"updated_at"`
}

// Available returns the balance that is free to use (not locked for bets).
func (w *Wallet) Available() decimal.Decimal {
	return w.Balance.Sub(w.Locked)
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction
// ──────────────────────────────────────────────────────────────────────────────

// TxType enumerates wallet transaction types for auditing.
type TxType string

const (
	TxDeposit    TxType = "deposit"
	TxWithdraw   TxType = "withdraw"
	TxBetLock    TxType = "bet_lock"
	TxBetUnlock  TxType = "bet_unlock"
	TxPayout     TxType = "payout"
	TxCashout    TxType = "cashout"
	TxCommission TxType = "commission"
	TxRefund     TxType = "refund"
	TxBonus      TxType = "bonus" // registration / promotional bonus
)

// Transaction is an immutable audit record for every wallet balance change.
type Transaction struct {
	ID            uuid.UUID       `json:"id"             db:"id"`
	WalletID      uuid.UUID       `json:"wallet_id"      db:"wallet_id"`
	Type          TxType          `json:"type"           db:"type"`
	Amount        decimal.Decimal `json:"amount"         db:"amount"`
	BalanceBefore decimal.Decimal `json:"balance_before" db:"balance_before"`
	BalanceAfter  decimal.Decimal `json:"balance_after"  db:"balance_after"`
	RefID         *uuid.UUID      `json:"ref_id"         db:"ref_id"` // bet or market ID
	Description   string          `json:"description"    db:"description"`
	CreatedAt     time.Time       `json:"created_at"     db:"created_at"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Withdraw
// ──────────────────────────────────────────────────────────────────────────────

// WithdrawStatus represents the lifecycle of a withdrawal request.
type WithdrawStatus string

const (
	WithdrawPending   WithdrawStatus = "pending"
	WithdrawApproved  WithdrawStatus = "approved"
	WithdrawRejected  WithdrawStatus = "rejected"
	WithdrawCompleted WithdrawStatus = "completed"
)

// WithdrawRequest is submitted by a user who wants to withdraw TRY.
type WithdrawRequest struct {
	ID          uuid.UUID       `json:"id"           db:"id"`
	UserID      uuid.UUID       `json:"user_id"      db:"user_id"`
	Amount      decimal.Decimal `json:"amount"       db:"amount"`
	Status      WithdrawStatus  `json:"status"       db:"status"`
	IBAN        string          `json:"iban"         db:"iban"`
	Note        string          `json:"note"         db:"note"`
	ReviewedBy  *uuid.UUID      `json:"reviewed_by"  db:"reviewed_by"`
	ReviewNote  string          `json:"review_note"  db:"review_note"`
	RequestedAt time.Time       `json:"requested_at" db:"requested_at"`
	ReviewedAt  *time.Time      `json:"reviewed_at"  db:"reviewed_at"`
}
