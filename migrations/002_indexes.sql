-- Migration 002: Performance indexes

-- Users
CREATE INDEX IF NOT EXISTS idx_users_email    ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_role     ON users(role);

-- Wallets
CREATE INDEX IF NOT EXISTS idx_wallets_user_id ON wallets(user_id);

-- Wallet transactions
CREATE INDEX IF NOT EXISTS idx_wallet_txns_wallet_id  ON wallet_transactions(wallet_id);
CREATE INDEX IF NOT EXISTS idx_wallet_txns_ref_id     ON wallet_transactions(ref_id);
CREATE INDEX IF NOT EXISTS idx_wallet_txns_created_at ON wallet_transactions(created_at DESC);

-- Markets
CREATE INDEX IF NOT EXISTS idx_markets_status     ON markets(status);
CREATE INDEX IF NOT EXISTS idx_markets_opens_at   ON markets(opens_at DESC);
CREATE INDEX IF NOT EXISTS idx_markets_closes_at  ON markets(closes_at);

-- Bets
CREATE INDEX IF NOT EXISTS idx_bets_user_id    ON bets(user_id);
CREATE INDEX IF NOT EXISTS idx_bets_market_id  ON bets(market_id);
CREATE INDEX IF NOT EXISTS idx_bets_status     ON bets(status);
CREATE INDEX IF NOT EXISTS idx_bets_direction  ON bets(direction);
CREATE INDEX IF NOT EXISTS idx_bets_placed_at  ON bets(placed_at DESC);

-- Composite: all open bets for a given market (used during resolution)
CREATE INDEX IF NOT EXISTS idx_bets_market_status ON bets(market_id, status);

-- Refresh tokens
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash    ON refresh_tokens(token_hash);
