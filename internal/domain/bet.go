package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ──────────────────────────────────────────────────────────────────────────────
// Types & constants
// ──────────────────────────────────────────────────────────────────────────────

// BetStatus represents the current state of a user's bet.
type BetStatus string

const (
	BetStatusActive   BetStatus = "open"       // in play
	BetStatusWon      BetStatus = "won"        // market resolved in user's favour
	BetStatusLost     BetStatus = "lost"       // market resolved against user
	BetStatusExited   BetStatus = "cashed_out" // user exited early
	BetStatusRefunded BetStatus = "cancelled"  // market cancelled; bet refunded
)

// CashoutFeeRate is the fee taken on early exit (5 %).
var CashoutFeeRate = decimal.NewFromFloat(0.05)

// ──────────────────────────────────────────────────────────────────────────────
// Bet
// ──────────────────────────────────────────────────────────────────────────────

// Bet represents a single user wager inside a Market.
type Bet struct {
	ID            uuid.UUID        `json:"id"             db:"id"`
	UserID        uuid.UUID        `json:"user_id"        db:"user_id"`
	MarketID      uuid.UUID        `json:"market_id"      db:"market_id"`
	Direction     Outcome          `json:"direction"      db:"direction"`
	Amount        decimal.Decimal  `json:"amount"         db:"amount"`
	OddsAtEntry   decimal.Decimal  `json:"odds_at_entry"  db:"odds_at_entry"`
	Status        BetStatus        `json:"status"         db:"status"`
	Payout        *decimal.Decimal `json:"payout"        db:"payout"`
	CashoutAmount *decimal.Decimal `json:"cashout_amount" db:"cashout_amount"`
	CashoutFee    *decimal.Decimal `json:"cashout_fee"   db:"cashout_fee"`
	PlacedAt      time.Time        `json:"placed_at"      db:"placed_at"`
	ResolvedAt    *time.Time       `json:"resolved_at"    db:"resolved_at"`
}

// IsActive returns true when the bet can still interact with the market.
func (b *Bet) IsActive() bool {
	return b.Status == BetStatusActive
}

// CalculateExitAmount computes how much TRY a user receives when cashing out early.
//
// Formula:
//
//	grossReturn = Amount × (currentOdds / oddsAtEntry)
//	fee         = grossReturn × CashoutFeeRate
//	netReturn   = grossReturn - fee
//
// The amount is always floored to 4 decimal places (matching DB DECIMAL(18,4)).
// Returns decimal.Zero if OddsAtEntry is zero (guard against division by zero).
func (b *Bet) CalculateExitAmount(currentOdds decimal.Decimal) decimal.Decimal {
	if b.OddsAtEntry.IsZero() || currentOdds.IsZero() {
		return decimal.Zero
	}
	gross := b.Amount.Mul(currentOdds).Div(b.OddsAtEntry)
	fee := gross.Mul(CashoutFeeRate)
	net := gross.Sub(fee)
	return net.RoundDown(4)
}

// CalculateExitFee returns only the fee portion for a given exit amount.
func (b *Bet) CalculateExitFee(currentOdds decimal.Decimal) decimal.Decimal {
	if b.OddsAtEntry.IsZero() || currentOdds.IsZero() {
		return decimal.Zero
	}
	gross := b.Amount.Mul(currentOdds).Div(b.OddsAtEntry)
	return gross.Mul(CashoutFeeRate).RoundDown(4)
}

// ──────────────────────────────────────────────────────────────────────────────
// PlaceBetRequest — value object used by BetService
// ──────────────────────────────────────────────────────────────────────────────

// PlaceBetRequest carries the validated inputs for placing a bet.
type PlaceBetRequest struct {
	UserID    uuid.UUID
	MarketID  uuid.UUID
	Direction Outcome
	Amount    decimal.Decimal
}

// BetResponse is the API-safe view of a bet (no internal IDs leaked beyond IDs).
type BetResponse struct {
	ID            uuid.UUID        `json:"id"`
	MarketID      uuid.UUID        `json:"market_id"`
	Direction     Outcome          `json:"direction"`
	Amount        decimal.Decimal  `json:"amount"`
	OddsAtEntry   decimal.Decimal  `json:"odds_at_entry"`
	Status        BetStatus        `json:"status"`
	Payout        *decimal.Decimal `json:"payout,omitempty"`
	CashoutAmount *decimal.Decimal `json:"cashout_amount,omitempty"`
	PlacedAt      time.Time        `json:"placed_at"`
	ResolvedAt    *time.Time       `json:"resolved_at,omitempty"`
}

// ToResponse converts a Bet to its API response form.
func (b *Bet) ToResponse() BetResponse {
	return BetResponse{
		ID:            b.ID,
		MarketID:      b.MarketID,
		Direction:     b.Direction,
		Amount:        b.Amount,
		OddsAtEntry:   b.OddsAtEntry,
		Status:        b.Status,
		Payout:        b.Payout,
		CashoutAmount: b.CashoutAmount,
		PlacedAt:      b.PlacedAt,
		ResolvedAt:    b.ResolvedAt,
	}
}
