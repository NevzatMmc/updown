// Package config provides application configuration loaded from environment variables.
// Use the package-level Get() function to obtain the singleton Config instance.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Sub-config structs
// ──────────────────────────────────────────────────────────────────────────────

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port                 string        // e.g. "8080"
	BackofficePort       string        // e.g. "8081"
	Env                  string        // "development" | "production"
	ReadTimeout          time.Duration // default 10s
	WriteTimeout         time.Duration // default 10s
	BackofficeAllowedIPs string        // comma-separated IPs; "" = allow all
}

// DBConfig holds PostgreSQL connection settings.
type DBConfig struct {
	DSN             string        // full postgres DSN
	MaxOpenConns    int           // default 25
	MaxIdleConns    int           // default 10
	ConnMaxLifetime time.Duration // default 5m
}

// JWTConfig holds JWT signing settings.
type JWTConfig struct {
	AccessSecret  string        // must be set
	RefreshSecret string        // must be set
	AccessTTL     time.Duration // default 15m
	RefreshTTL    time.Duration // default 720h (30 days)
}

// PriceConfig holds exchange API settings.
type PriceConfig struct {
	BinanceURL   string        // default "https://api.binance.com"
	BybitURL     string        // default "https://api.bybit.com"
	OKXURL       string        // default "https://www.okx.com"
	FetchTimeout time.Duration // default 2s
	CacheTTL     time.Duration // default 1s
	// Weight percentages (must sum to 100)
	BinanceWeight int // default 50
	BybitWeight   int // default 30
	OKXWeight     int // default 20
}

// MMConfig holds Market Maker settings.
type MMConfig struct {
	MaxExposurePerMarket float64 // max TRY per market the house risks
	MaxDailyLoss         float64 // house stops injecting liquidity after this daily loss (TRY)
	MinReserve           float64 // house must keep at least this in reserve (TRY)
	TriggerThreshold     float64 // imbalance ratio triggering MM, e.g. 0.8 = 80/20
	MinMMBet             float64 // minimum TRY the MM injects per action
}

// WalletConfig holds wallet and fee settings.
type WalletConfig struct {
	MinWithdraw      float64 // minimum withdrawal amount (TRY)
	MaxDailyWithdraw float64 // max cumulative withdrawal per day per user (TRY)
	CommissionRate   float64 // pari-mutuel commission, e.g. 0.03 = 3%
	CashoutFeeRate   float64 // early cashout fee, e.g. 0.05 = 5%
}

// ──────────────────────────────────────────────────────────────────────────────
// Top-level Config
// ──────────────────────────────────────────────────────────────────────────────

// Config is the root configuration object for the entire application.
type Config struct {
	Server ServerConfig
	DB     DBConfig
	JWT    JWTConfig
	Price  PriceConfig
	MM     MMConfig
	Wallet WalletConfig
}

// IsProd returns true when running in the production environment.
func (c *Config) IsProd() bool {
	return c.Server.Env == "production"
}

// Validate checks that all required configuration values are present and valid.
// Returns the first validation error encountered.
func (c *Config) Validate() error {
	var errs []error

	// JWT secrets are mandatory
	if c.JWT.AccessSecret == "" {
		errs = append(errs, errors.New("JWT_ACCESS_SECRET must be set"))
	}
	if c.JWT.RefreshSecret == "" {
		errs = append(errs, errors.New("JWT_REFRESH_SECRET must be set"))
	}

	// In production, DB DSN must be explicit
	if c.IsProd() && c.DB.DSN == "" {
		errs = append(errs, errors.New("DATABASE_DSN must be set in production"))
	}

	// Price weights must sum to 100
	total := c.Price.BinanceWeight + c.Price.BybitWeight + c.Price.OKXWeight
	if total != 100 {
		errs = append(errs, fmt.Errorf(
			"price weights must sum to 100, got %d (Binance=%d Bybit=%d OKX=%d)",
			total, c.Price.BinanceWeight, c.Price.BybitWeight, c.Price.OKXWeight,
		))
	}

	// Commission sanity check
	if c.Wallet.CommissionRate <= 0 || c.Wallet.CommissionRate >= 1 {
		errs = append(errs, fmt.Errorf(
			"COMMISSION_RATE must be between 0 and 1 (exclusive), got %.4f",
			c.Wallet.CommissionRate,
		))
	}
	if c.Wallet.CashoutFeeRate <= 0 || c.Wallet.CashoutFeeRate >= 1 {
		errs = append(errs, fmt.Errorf(
			"CASHOUT_FEE_RATE must be between 0 and 1 (exclusive), got %.4f",
			c.Wallet.CashoutFeeRate,
		))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Singleton
// ──────────────────────────────────────────────────────────────────────────────

var (
	instance *Config
	once     sync.Once
	loadErr  error
)

// Get returns the singleton Config, loading it once from environment variables.
// Panics if loading fails — call this early in main() to catch misconfigurations
// at startup.
func Get() *Config {
	once.Do(func() {
		instance, loadErr = load()
	})
	if loadErr != nil {
		panic(fmt.Sprintf("config: failed to load: %v", loadErr))
	}
	return instance
}

// MustLoad loads and validates configuration. Intended for use in main().
// Panics on any error so misconfiguration is caught immediately at boot.
func MustLoad() *Config {
	cfg := Get()
	if err := cfg.Validate(); err != nil {
		panic(fmt.Sprintf("config: validation failed: %v", err))
	}
	return cfg
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal loader
// ──────────────────────────────────────────────────────────────────────────────

func load() (*Config, error) {
	cfg := &Config{}

	// ── Server ────────────────────────────────────────────────────────────────
	cfg.Server = ServerConfig{
		Port:                 getEnv("SERVER_PORT", "8080"),
		BackofficePort:       getEnv("BACKOFFICE_PORT", "8081"),
		Env:                  getEnv("ENVIRONMENT", "development"),
		ReadTimeout:          getDuration("SERVER_READ_TIMEOUT", 10*time.Second),
		WriteTimeout:         getDuration("SERVER_WRITE_TIMEOUT", 10*time.Second),
		BackofficeAllowedIPs: getEnv("BACKOFFICE_ALLOWED_IPS", ""),
	}

	// ── Database ──────────────────────────────────────────────────────────────
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		// Build DSN from individual components for convenience in dev
		dsn = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
			getEnv("DB_HOST", "localhost"),
			getEnv("DB_PORT", "5432"),
			getEnv("DB_USER", "postgres"),
			getEnv("DB_PASSWORD", ""),
			getEnv("DB_NAME", "evetabi_prediction"),
			getEnv("DB_SSLMODE", "disable"),
		)
	}

	maxOpen, err := getInt("DB_MAX_OPEN_CONNS", 25)
	if err != nil {
		return nil, fmt.Errorf("DB_MAX_OPEN_CONNS: %w", err)
	}
	maxIdle, err := getInt("DB_MAX_IDLE_CONNS", 10)
	if err != nil {
		return nil, fmt.Errorf("DB_MAX_IDLE_CONNS: %w", err)
	}

	cfg.DB = DBConfig{
		DSN:             dsn,
		MaxOpenConns:    maxOpen,
		MaxIdleConns:    maxIdle,
		ConnMaxLifetime: getDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute),
	}

	// ── JWT ───────────────────────────────────────────────────────────────────
	cfg.JWT = JWTConfig{
		AccessSecret:  getEnv("JWT_ACCESS_SECRET", ""),
		RefreshSecret: getEnv("JWT_REFRESH_SECRET", ""),
		AccessTTL:     getDuration("JWT_ACCESS_TTL", 15*time.Minute),
		RefreshTTL:    getDuration("JWT_REFRESH_TTL", 30*24*time.Hour),
	}

	// ── Price ─────────────────────────────────────────────────────────────────
	binW, err := getInt("PRICE_BINANCE_WEIGHT", 50)
	if err != nil {
		return nil, fmt.Errorf("PRICE_BINANCE_WEIGHT: %w", err)
	}
	byW, err := getInt("PRICE_BYBIT_WEIGHT", 30)
	if err != nil {
		return nil, fmt.Errorf("PRICE_BYBIT_WEIGHT: %w", err)
	}
	okxW, err := getInt("PRICE_OKX_WEIGHT", 20)
	if err != nil {
		return nil, fmt.Errorf("PRICE_OKX_WEIGHT: %w", err)
	}

	cfg.Price = PriceConfig{
		BinanceURL:    getEnv("PRICE_BINANCE_URL", "https://api.binance.com"),
		BybitURL:      getEnv("PRICE_BYBIT_URL", "https://api.bybit.com"),
		OKXURL:        getEnv("PRICE_OKX_URL", "https://www.okx.com"),
		FetchTimeout:  getDuration("PRICE_FETCH_TIMEOUT", 2*time.Second),
		CacheTTL:      getDuration("PRICE_CACHE_TTL", 1*time.Second),
		BinanceWeight: binW,
		BybitWeight:   byW,
		OKXWeight:     okxW,
	}

	// ── Market Maker ──────────────────────────────────────────────────────────
	mmExposure, err := getFloat("MM_MAX_EXPOSURE_PER_MARKET", 10000)
	if err != nil {
		return nil, fmt.Errorf("MM_MAX_EXPOSURE_PER_MARKET: %w", err)
	}
	mmDailyLoss, err := getFloat("MM_MAX_DAILY_LOSS", 50000)
	if err != nil {
		return nil, fmt.Errorf("MM_MAX_DAILY_LOSS: %w", err)
	}
	mmReserve, err := getFloat("MM_MIN_RESERVE", 100000)
	if err != nil {
		return nil, fmt.Errorf("MM_MIN_RESERVE: %w", err)
	}
	mmThreshold, err := getFloat("MM_TRIGGER_THRESHOLD", 0.8)
	if err != nil {
		return nil, fmt.Errorf("MM_TRIGGER_THRESHOLD: %w", err)
	}
	mmMinBet, err := getFloat("MM_MIN_BET", 10)
	if err != nil {
		return nil, fmt.Errorf("MM_MIN_BET: %w", err)
	}

	cfg.MM = MMConfig{
		MaxExposurePerMarket: mmExposure,
		MaxDailyLoss:         mmDailyLoss,
		MinReserve:           mmReserve,
		TriggerThreshold:     mmThreshold,
		MinMMBet:             mmMinBet,
	}

	// ── Wallet ────────────────────────────────────────────────────────────────
	minW, err := getFloat("WALLET_MIN_WITHDRAW", 10)
	if err != nil {
		return nil, fmt.Errorf("WALLET_MIN_WITHDRAW: %w", err)
	}
	maxDW, err := getFloat("WALLET_MAX_DAILY_WITHDRAW", 50000)
	if err != nil {
		return nil, fmt.Errorf("WALLET_MAX_DAILY_WITHDRAW: %w", err)
	}
	commission, err := getFloat("WALLET_COMMISSION_RATE", 0.03)
	if err != nil {
		return nil, fmt.Errorf("WALLET_COMMISSION_RATE: %w", err)
	}
	cashoutFee, err := getFloat("WALLET_CASHOUT_FEE_RATE", 0.05)
	if err != nil {
		return nil, fmt.Errorf("WALLET_CASHOUT_FEE_RATE: %w", err)
	}

	cfg.Wallet = WalletConfig{
		MinWithdraw:      minW,
		MaxDailyWithdraw: maxDW,
		CommissionRate:   commission,
		CashoutFeeRate:   cashoutFee,
	}

	return cfg, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Helper functions
// ──────────────────────────────────────────────────────────────────────────────

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getInt(key string, defaultVal int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", v)
	}
	return n, nil
}

func getFloat(key string, defaultVal float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float %q", v)
	}
	return f, nil
}

// getDuration parses an env var as a Go duration string (e.g. "15m", "2s").
// Falls back to defaultVal if the variable is unset or empty.
func getDuration(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		// Log warning and fall back to default; do not crash on parse error
		return defaultVal
	}
	return d
}
