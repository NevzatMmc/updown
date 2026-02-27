package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// MMLog records every liquidity injection made by the Market Maker service.
// It maps to the mm_positions table.
type MMLog struct {
	ID        uuid.UUID        `json:"id"         db:"id"`
	MarketID  uuid.UUID        `json:"market_id"  db:"market_id"`
	Direction Outcome          `json:"direction"  db:"direction"` // which side was funded
	Amount    decimal.Decimal  `json:"amount"     db:"amount"`
	Status    string           `json:"status"     db:"status"` // open | closed | won | lost
	PnL       *decimal.Decimal `json:"pnl"       db:"pnl"`     // set after market resolution
	Reason    string           `json:"reason"     db:"reason"` // logged for audit
	CreatedAt time.Time        `json:"created_at" db:"created_at"`
	ClosedAt  *time.Time       `json:"closed_at"  db:"closed_at"`
}
