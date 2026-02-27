// Package ws holds WebSocket message types and the Hub implementation.
// messages.go defines all message structs broadcast to connected clients.
package ws

import (
	"time"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// MsgType identifies the kind of WS message so clients can switch on it.
type MsgType string

const (
	MsgTypePriceUpdate    MsgType = "price_update"
	MsgTypeBetPlaced      MsgType = "bet_placed"
	MsgTypeMarketResolved MsgType = "market_resolved"
	MsgTypeNewMarket      MsgType = "new_market"
	MsgTypeError          MsgType = "error"
)

// ──────────────────────────────────────────────────────────────────────────────
// PriceUpdateMessage — sent every second to all clients.
// ──────────────────────────────────────────────────────────────────────────────

// PriceUpdateMessage carries live BTC price, market pool state, and countdown.
type PriceUpdateMessage struct {
	Type            MsgType          `json:"type"`
	MarketID        uuid.UUID        `json:"market_id"`
	BTCPrice        decimal.Decimal  `json:"btc_price"`
	OpenPrice       *decimal.Decimal `json:"open_price"`
	Diff            decimal.Decimal  `json:"diff"`     // closePrice − openPrice
	DiffPct         decimal.Decimal  `json:"diff_pct"` // diff/openPrice × 100
	UpOdds          decimal.Decimal  `json:"up_odds"`
	DownOdds        decimal.Decimal  `json:"down_odds"`
	UpPercent       decimal.Decimal  `json:"up_percent"` // % of pool on UP
	DownPercent     decimal.Decimal  `json:"down_percent"`
	PoolUp          decimal.Decimal  `json:"pool_up"`
	PoolDown        decimal.Decimal  `json:"pool_down"`
	TimeLeftSeconds int64            `json:"time_left_seconds"`
	Timestamp       time.Time        `json:"timestamp"`
}

// ──────────────────────────────────────────────────────────────────────────────
// BetPlacedMessage — broadcast after a bet is accepted so odds refresh for all.
// ──────────────────────────────────────────────────────────────────────────────

// BetPlacedMessage notifies all clients that the pool ratios have changed.
type BetPlacedMessage struct {
	Type        MsgType         `json:"type"`
	MarketID    uuid.UUID       `json:"market_id"`
	Direction   domain.Outcome  `json:"direction"`
	Amount      decimal.Decimal `json:"amount"`
	NewUpOdds   decimal.Decimal `json:"new_up_odds"`
	NewDownOdds decimal.Decimal `json:"new_down_odds"`
	PoolUp      decimal.Decimal `json:"pool_up"`
	PoolDown    decimal.Decimal `json:"pool_down"`
	Timestamp   time.Time       `json:"timestamp"`
}

// ──────────────────────────────────────────────────────────────────────────────
// MarketResolvedMessage — broadcast when a market is settled.
// ──────────────────────────────────────────────────────────────────────────────

// MarketResolvedMessage tells clients which side won and what the final price was.
type MarketResolvedMessage struct {
	Type       MsgType          `json:"type"`
	MarketID   uuid.UUID        `json:"market_id"`
	Result     domain.Outcome   `json:"result"`
	ClosePrice decimal.Decimal  `json:"close_price"`
	OpenPrice  *decimal.Decimal `json:"open_price"`
	PoolUp     decimal.Decimal  `json:"pool_up"`
	PoolDown   decimal.Decimal  `json:"pool_down"`
	Timestamp  time.Time        `json:"timestamp"`
}

// ──────────────────────────────────────────────────────────────────────────────
// NewMarketMessage — broadcast when a new 5-minute market opens.
// ──────────────────────────────────────────────────────────────────────────────

// NewMarketMessage carries the identity of the freshly opened market.
type NewMarketMessage struct {
	Type      MsgType         `json:"type"`
	MarketID  uuid.UUID       `json:"market_id"`
	OpensAt   time.Time       `json:"opens_at"`
	ClosesAt  time.Time       `json:"closes_at"`
	OpenPrice decimal.Decimal `json:"open_price"`
	Timestamp time.Time       `json:"timestamp"`
}

// ──────────────────────────────────────────────────────────────────────────────
// ErrorMessage — sent to a single client on a non-fatal error.
// ──────────────────────────────────────────────────────────────────────────────

// ErrorMessage is sent directly to one client (not broadcast).
type ErrorMessage struct {
	Type    MsgType `json:"type"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
}
