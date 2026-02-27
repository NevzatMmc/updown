// Package main is the entry point for the evetabi BTC UP/DOWN prediction
// market API server.  It wires together all services and starts the HTTP
// server alongside the WebSocket hub and background scheduler.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/evetabi/prediction/internal/api"
	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/evetabi/prediction/internal/scheduler"
	"github.com/evetabi/prediction/internal/service"
	"github.com/evetabi/prediction/internal/ws"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq" // postgres driver
)

func main() {
	// ── 1. Logger ─────────────────────────────────────────────────────────────
	cfg := config.MustLoad()

	var logHandler slog.Handler
	if cfg.IsProd() {
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	logger.Info("starting evetabi prediction server", "env", cfg.Server.Env, "port", cfg.Server.Port)

	// ── 2. Database ───────────────────────────────────────────────────────────
	db, err := sqlx.Connect("postgres", cfg.DB.DSN)
	if err != nil {
		logger.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(cfg.DB.MaxOpenConns)
	db.SetMaxIdleConns(cfg.DB.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.DB.ConnMaxLifetime)

	if err = db.Ping(); err != nil {
		logger.Error("database ping failed", "err", err)
		os.Exit(1)
	}
	logger.Info("database connected")

	// ── 3. Migrations ─────────────────────────────────────────────────────────
	if err = runMigrations(db, "migrations"); err != nil {
		logger.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	logger.Info("migrations applied")

	// ── 4. Repositories ───────────────────────────────────────────────────────
	userRepo := repository.NewUserRepository(db)
	walletRepo := repository.NewWalletRepository(db)
	marketRepo := repository.NewMarketRepository(db)
	betRepo := repository.NewBetRepository(db)

	// ── 5. Services (order matters for injection) ─────────────────────────────
	priceSvc := service.NewPriceService(cfg)

	marketSvc := service.NewMarketService(marketRepo, priceSvc, cfg)

	betSvc := service.NewBetService(db, betRepo, marketRepo, walletRepo, cfg)

	authSvc := service.NewAuthService(db, userRepo, walletRepo, cfg)

	resolutionSvc := service.NewResolutionService(db, marketRepo, betRepo, walletRepo, priceSvc, cfg)

	mmSvc := service.NewMMService(db, betRepo, marketRepo, walletRepo, cfg)

	// Wire circular dependencies via interfaces
	marketSvc.SetRefunder(resolutionSvc)
	betSvc.SetRebalancer(mmSvc)

	// ── 6. WebSocket Hub ──────────────────────────────────────────────────────
	jwtSecret := []byte(cfg.JWT.AccessSecret)
	var allowedOrigins []string
	if ori := os.Getenv("WS_ALLOWED_ORIGINS"); ori != "" {
		for _, o := range strings.Split(ori, ",") {
			allowedOrigins = append(allowedOrigins, strings.TrimSpace(o))
		}
	}
	hub := ws.NewHub(jwtSecret, allowedOrigins)

	// Wire WS broadcaster into bet service
	betSvc.SetBroadcaster(hub)

	// ── 7. Root context + signal handling ─────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── 8. Start WS Hub ───────────────────────────────────────────────────────
	go hub.Run()
	logger.Info("websocket hub started")

	// ── 9. Scheduler ──────────────────────────────────────────────────────────
	sched := scheduler.NewScheduler(marketSvc, resolutionSvc, priceSvc, hub, cfg, logger)
	sched.Start(ctx)

	// ── 10. HTTP Router ───────────────────────────────────────────────────────
	router := api.SetupRouter(api.RouterDeps{
		AuthSvc:    authSvc,
		MarketSvc:  marketSvc,
		BetSvc:     betSvc,
		WalletRepo: walletRepo,
		Hub:        hub,
		Cfg:        cfg,
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// ── 11. Start server ──────────────────────────────────────────────────────
	go func() {
		logger.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server error", "err", err)
			stop() // trigger graceful shutdown
		}
	}()

	// ── 12. Graceful shutdown ─────────────────────────────────────────────────
	<-ctx.Done()
	logger.Info("shutdown signal received, draining connections…")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err = srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}

	db.Close()
	logger.Info("server stopped cleanly")
}

// runMigrations reads all *.sql files from dir, sorted by name, and executes
// them sequentially.  Idempotent: SQL files should use IF NOT EXISTS / ON CONFLICT.
func runMigrations(db *sqlx.DB, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("runMigrations: read dir %q: %w", dir, err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("runMigrations: read %q: %w", f, err)
		}
		if _, err = db.Exec(string(data)); err != nil {
			return fmt.Errorf("runMigrations: exec %q: %w", f, err)
		}
		slog.Info("migration applied", "file", filepath.Base(f))
	}
	return nil
}
