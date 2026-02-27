-- Migration 003: Market Maker liquidity tracking

-- MM positions: records the platform's own liquidity injections
CREATE TABLE IF NOT EXISTS mm_positions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id   UUID NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    direction   VARCHAR(10)  NOT NULL,           -- 'up' | 'down' (which side MM funded)
    amount      DECIMAL(18,4) NOT NULL,          -- TRY amount injected
    status      VARCHAR(20)  NOT NULL DEFAULT 'open',  -- 'open' | 'closed' | 'won' | 'lost'
    pnl         DECIMAL(18,4),                   -- profit/loss after resolution (positive = profit)
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    closed_at   TIMESTAMPTZ,
    CONSTRAINT mm_positions_amount_positive CHECK (amount > 0)
);

CREATE INDEX IF NOT EXISTS idx_mm_positions_market_id ON mm_positions(market_id);
CREATE INDEX IF NOT EXISTS idx_mm_positions_status    ON mm_positions(status);

-- MM config: per-market override capability for threshold/exposure
CREATE TABLE IF NOT EXISTS mm_config (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id           UUID REFERENCES markets(id),   -- NULL = global default
    threshold_ratio     DECIMAL(5,4) NOT NULL,          -- e.g. 0.8000
    max_exposure_try    DECIMAL(18,4) NOT NULL,
    is_active           BOOLEAN NOT NULL DEFAULT TRUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- House treasury: tracks cumulative P&L from MM operations
CREATE TABLE IF NOT EXISTS house_treasury (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id       UUID NOT NULL REFERENCES markets(id),
    commission_earned   DECIMAL(18,4) NOT NULL DEFAULT 0,
    mm_pnl              DECIMAL(18,4) NOT NULL DEFAULT 0,  -- positive = won, negative = lost
    cashout_fees_earned DECIMAL(18,4) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_house_treasury_market_id ON house_treasury(market_id);
