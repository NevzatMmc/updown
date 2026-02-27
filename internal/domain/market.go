// Package domain defines the core business entities and types for the
// BTC UP/DOWN prediction market system.
package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ──────────────────────────────────────────────────────────────────────────────
// Types & constants
// ──────────────────────────────────────────────────────────────────────────────

// MarketStatus represents the lifecycle state of a market.
type MarketStatus string

const (
	StatusPending   MarketStatus = "pending"   // created, not yet open for betting
	StatusOpen      MarketStatus = "open"      // accepting bets
	StatusClosed    MarketStatus = "closed"    // betting window over, awaiting resolution
	StatusResolved  MarketStatus = "resolved"  // winner determined, payouts sent
	StatusSuspended MarketStatus = "suspended" // temporarily halted by admin
	StatusCancelled MarketStatus = "cancelled" // voided; all bets refunded
)

// Outcome represents the direction a user bets on.
type Outcome string

const (
	OutcomeUp   Outcome = "UP"
	OutcomeDown Outcome = "DOWN"
)

// IsValid returns true if the outcome is a recognised direction.
func (o Outcome) IsValid() bool {
	return o == OutcomeUp || o == OutcomeDown
}

// CommissionRate is the pari-mutuel pool commission (3 %).
var CommissionRate = decimal.NewFromFloat(0.03)

// ──────────────────────────────────────────────────────────────────────────────
// PriceSource
// ──────────────────────────────────────────────────────────────────────────────

// PriceSource holds a single exchange price reading used for weighted averaging.
type PriceSource struct {
	Exchange  string          `json:"exchange"`
	Price     decimal.Decimal `json:"price"`
	Weight    decimal.Decimal `json:"weight"` // 0–100 integer stored as decimal
	FetchedAt time.Time       `json:"fetched_at"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Market
// ──────────────────────────────────────────────────────────────────────────────

// Market represents a single 5-minute BTC UP/DOWN prediction round.
type Market struct {
	ID              uuid.UUID        `json:"id"               db:"id"`
	Status          MarketStatus     `json:"status"           db:"status"`
	OpenPrice       *decimal.Decimal `json:"open_price"     db:"open_price"`
	ClosePrice      *decimal.Decimal `json:"close_price"    db:"close_price"`
	Result          *Outcome         `json:"result"           db:"result"`
	PoolUp          decimal.Decimal  `json:"pool_up"         db:"pool_up"`
	PoolDown        decimal.Decimal  `json:"pool_down"       db:"pool_down"`
	CommissionTaken decimal.Decimal  `json:"commission_taken" db:"commission_taken"`
	OpensAt         time.Time        `json:"opens_at"         db:"opens_at"`
	ClosesAt        time.Time        `json:"closes_at"        db:"closes_at"`
	ResolvedAt      *time.Time       `json:"resolved_at"      db:"resolved_at"`
	CreatedAt       time.Time        `json:"created_at"       db:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"       db:"updated_at"`
}

// TotalPool returns the sum of both pools.
func (m *Market) TotalPool() decimal.Decimal {
	return m.PoolUp.Add(m.PoolDown)
}

// effectivePool returns the pool after commission deduction.
func (m *Market) effectivePool() decimal.Decimal {
	one := decimal.NewFromInt(1)
	return m.TotalPool().Mul(one.Sub(CommissionRate))
}

// UpOdds returns the current payout multiplier for an UP bet.
//
//	UpOdds = (PoolUp + PoolDown) * (1 - commission%) / PoolUp
//
// Returns decimal.Zero when PoolUp is zero (no bets on UP side yet).
func (m *Market) UpOdds() decimal.Decimal {
	if m.PoolUp.IsZero() {
		return decimal.Zero
	}
	return m.effectivePool().Div(m.PoolUp)
}

// DownOdds returns the current payout multiplier for a DOWN bet.
// Returns decimal.Zero when PoolDown is zero.
func (m *Market) DownOdds() decimal.Decimal {
	if m.PoolDown.IsZero() {
		return decimal.Zero
	}
	return m.effectivePool().Div(m.PoolDown)
}

// OddsFor returns the odds for the given outcome.
func (m *Market) OddsFor(o Outcome) decimal.Decimal {
	if o == OutcomeUp {
		return m.UpOdds()
	}
	return m.DownOdds()
}

// UpPercent returns the percentage of the total pool bet on UP (0–100).
// Returns decimal.Zero when there are no bets.
func (m *Market) UpPercent() decimal.Decimal {
	total := m.TotalPool()
	if total.IsZero() {
		return decimal.Zero
	}
	return m.PoolUp.Div(total).Mul(decimal.NewFromInt(100))
}

// DownPercent returns the percentage of the total pool bet on DOWN (0–100).
func (m *Market) DownPercent() decimal.Decimal {
	total := m.TotalPool()
	if total.IsZero() {
		return decimal.Zero
	}
	return m.PoolDown.Div(total).Mul(decimal.NewFromInt(100))
}

// IsOpen returns true while the market is accepting bets.
func (m *Market) IsOpen() bool {
	return m.Status == StatusOpen
}

// IsResolved returns true after the market has been settled.
func (m *Market) IsResolved() bool {
	return m.Status == StatusResolved
}

// TimeLeft returns the duration remaining until the market closes.
// Returns 0 if the market's closing time has already passed.
func (m *Market) TimeLeft() time.Duration {
	remaining := time.Until(m.ClosesAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// WeightedPrice computes a weighted average BTC price from multiple sources.
// Sources with a zero weight or zero price are skipped.
// Returns decimal.Zero if no valid sources are provided.
func (m *Market) WeightedPrice(sources []PriceSource) decimal.Decimal {
	var sumWeighted, sumWeights decimal.Decimal
	for _, s := range sources {
		if s.Price.IsZero() || s.Weight.IsZero() {
			continue
		}
		sumWeighted = sumWeighted.Add(s.Price.Mul(s.Weight))
		sumWeights = sumWeights.Add(s.Weight)
	}
	if sumWeights.IsZero() {
		return decimal.Zero
	}
	return sumWeighted.Div(sumWeights)
}

// ──────────────────────────────────────────────────────────────────────────────
// MarketSummary — lightweight read model for WS broadcasts and list endpoints
// ──────────────────────────────────────────────────────────────────────────────

// MarketSummary is a derived, read-only view of a Market used for broadcasting.
type MarketSummary struct {
	ID           uuid.UUID        `json:"id"`
	Status       MarketStatus     `json:"status"`
	OpenPrice    *decimal.Decimal `json:"open_price"`
	CurrentPrice decimal.Decimal  `json:"current_price"`
	UpOdds       decimal.Decimal  `json:"up_odds"`
	DownOdds     decimal.Decimal  `json:"down_odds"`
	UpPercent    decimal.Decimal  `json:"up_percent"`
	DownPercent  decimal.Decimal  `json:"down_percent"`
	PoolUp       decimal.Decimal  `json:"pool_up"`
	PoolDown     decimal.Decimal  `json:"pool_down"`
	ClosesAt     time.Time        `json:"closes_at"`
	TimeLeftSec  int64            `json:"time_left_sec"`
}

// ToSummary builds a MarketSummary from the market and a live price.
func (m *Market) ToSummary(currentPrice decimal.Decimal) MarketSummary {
	return MarketSummary{
		ID:           m.ID,
		Status:       m.Status,
		OpenPrice:    m.OpenPrice,
		CurrentPrice: currentPrice,
		UpOdds:       m.UpOdds(),
		DownOdds:     m.DownOdds(),
		UpPercent:    m.UpPercent(),
		DownPercent:  m.DownPercent(),
		PoolUp:       m.PoolUp,
		PoolDown:     m.PoolDown,
		ClosesAt:     m.ClosesAt,
		TimeLeftSec:  int64(m.TimeLeft().Seconds()),
	}
}
