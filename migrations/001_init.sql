-- Migration 001: Initial schema for BTC UP/DOWN Prediction Market
-- All monetary values stored as DECIMAL(18,4) for financial precision

-- Users table
CREATE TABLE IF NOT EXISTS users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email       VARCHAR(255) NOT NULL UNIQUE,
    username    VARCHAR(100) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role        VARCHAR(50)  NOT NULL DEFAULT 'user',  -- 'user' | 'admin' | 'superadmin'
    is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Wallets table (one per user)
CREATE TABLE IF NOT EXISTS wallets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID REFERENCES users(id) ON DELETE CASCADE, -- NULL for platform wallets
    wallet_type VARCHAR(30),                                  -- NULL=user | 'platform_mm'=house
    balance     DECIMAL(18,4) NOT NULL DEFAULT 0,
    locked      DECIMAL(18,4) NOT NULL DEFAULT 0,    -- locked for open bets
    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    CONSTRAINT wallets_user_unique   UNIQUE (user_id),
    CONSTRAINT wallets_type_unique   UNIQUE (wallet_type),
    CONSTRAINT wallets_balance_non_negative CHECK (balance >= 0),
    CONSTRAINT wallets_locked_non_negative  CHECK (locked >= 0)
);

-- Platform house wallet (seeded once; balance set by ops team)
INSERT INTO wallets (wallet_type, balance)
    VALUES ('platform_mm', 1000000)
    ON CONFLICT (wallet_type) DO NOTHING;

-- Wallet transactions (audit log)
CREATE TABLE IF NOT EXISTS wallet_transactions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id       UUID NOT NULL REFERENCES wallets(id),
    type            VARCHAR(50)  NOT NULL,  -- 'deposit' | 'withdraw' | 'bet_lock' | 'bet_unlock' | 'payout' | 'cashout' | 'commission'
    amount          DECIMAL(18,4) NOT NULL,
    balance_before  DECIMAL(18,4) NOT NULL,
    balance_after   DECIMAL(18,4) NOT NULL,
    ref_id          UUID,                   -- references bet_id or market_id depending on type
    description     TEXT,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Markets table
CREATE TABLE IF NOT EXISTS markets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    status          VARCHAR(30)  NOT NULL DEFAULT 'open',  -- 'open' | 'closed' | 'resolved' | 'cancelled'
    open_price      DECIMAL(18,4),                         -- BTC/USDT at market open
    close_price     DECIMAL(18,4),                         -- BTC/USDT at market close
    result          VARCHAR(10),                           -- 'up' | 'down' | NULL
    pool_up         DECIMAL(18,4) NOT NULL DEFAULT 0,      -- total TRY bet on UP
    pool_down       DECIMAL(18,4) NOT NULL DEFAULT 0,      -- total TRY bet on DOWN
    commission_taken DECIMAL(18,4) NOT NULL DEFAULT 0,
    opens_at        TIMESTAMPTZ  NOT NULL,
    closes_at       TIMESTAMPTZ  NOT NULL,
    resolved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Bets table
CREATE TABLE IF NOT EXISTS bets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id),
    market_id       UUID NOT NULL REFERENCES markets(id),
    direction       VARCHAR(10)  NOT NULL,               -- 'UP' | 'DOWN'
    amount          DECIMAL(18,4) NOT NULL,              -- original bet amount in TRY
    odds_at_entry   DECIMAL(18,4) NOT NULL DEFAULT 1,   -- pari-mutuel odds when bet was placed
    status          VARCHAR(30)  NOT NULL DEFAULT 'open', -- 'open' | 'won' | 'lost' | 'cashed_out' | 'cancelled'
    payout          DECIMAL(18,4),                       -- filled on resolution
    cashout_amount  DECIMAL(18,4),                       -- filled if user cashes out early
    cashout_fee     DECIMAL(18,4),
    placed_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    CONSTRAINT bets_amount_positive CHECK (amount > 0)
);

-- Refresh tokens table
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Withdraw requests table
CREATE TABLE IF NOT EXISTS withdraw_requests (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id),
    amount       DECIMAL(18,4) NOT NULL,
    status       VARCHAR(30)  NOT NULL DEFAULT 'pending',  -- pending|approved|rejected|completed
    iban         VARCHAR(34)  NOT NULL,
    note         TEXT,
    review_note  TEXT,
    reviewed_by  UUID REFERENCES users(id),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at  TIMESTAMPTZ,
    CONSTRAINT withdraw_amount_positive CHECK (amount > 0)
);
