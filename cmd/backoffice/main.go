// Package main is the entry point for the evetabi back-office admin server.
// Runs on port 8081 and exposes admin-only endpoints protected by RBAC.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/evetabi/prediction/internal/backoffice"
	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/evetabi/prediction/internal/service"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func main() {
	// ── Logger ────────────────────────────────────────────────────────────────
	cfg := config.MustLoad()

	var logHandler slog.Handler
	if cfg.IsProd() {
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	logger.Info("starting evetabi backoffice server",
		"env", cfg.Server.Env, "port", cfg.Server.BackofficePort)

	// ── Database ──────────────────────────────────────────────────────────────
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

	// ── Repositories ──────────────────────────────────────────────────────────
	userRepo := repository.NewUserRepository(db)
	walletRepo := repository.NewWalletRepository(db)
	marketRepo := repository.NewMarketRepository(db)
	betRepo := repository.NewBetRepository(db)

	// ── Services ──────────────────────────────────────────────────────────────
	priceSvc := service.NewPriceService(cfg)
	marketSvc := service.NewMarketService(marketRepo, priceSvc, cfg)
	authSvc := service.NewAuthService(db, userRepo, walletRepo, cfg)
	mmSvc := service.NewMMService(db, betRepo, marketRepo, walletRepo, cfg)

	// ResolutionService needed for CancelMarket refunds
	resolutionSvc := service.NewResolutionService(db, marketRepo, betRepo, walletRepo, priceSvc, cfg)
	marketSvc.SetRefunder(resolutionSvc)

	// ── Signal context ────────────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── Router ────────────────────────────────────────────────────────────────
	router := backoffice.SetupBackofficeRouter(backoffice.BackofficeDeps{
		AuthSvc:    authSvc,
		MarketSvc:  marketSvc,
		MMSvc:      mmSvc,
		UserRepo:   userRepo,
		MarketRepo: marketRepo,
		BetRepo:    betRepo,
		WalletRepo: walletRepo,
		Hub:        nil, // backoffice does not directly serve WS
		PriceSvc:   priceSvc,
		Cfg:        cfg,
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Server.BackofficePort,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// ── Start ─────────────────────────────────────────────────────────────────
	go func() {
		logger.Info("backoffice http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("backoffice server error", "err", err)
			stop()
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err = srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("backoffice shutdown error", "err", err)
	}

	db.Close()
	logger.Info("backoffice server stopped cleanly")
}
