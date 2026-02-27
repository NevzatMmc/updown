// Package scheduler manages the three background goroutines that run the
// BTC UP/DOWN market lifecycle:
//  1. marketCreationLoop – opens a new market every 5 minutes on the clock.
//  2. resolutionLoop     – resolves expired markets every 5 seconds.
//  3. priceBroadcastLoop – pushes live price + odds to WS clients every second.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/service"
	"github.com/evetabi/prediction/internal/ws"
	"github.com/shopspring/decimal"
)

// ──────────────────────────────────────────────────────────────────────────────
// WsHub interface — minimally required from the Hub (Step 12)
// ──────────────────────────────────────────────────────────────────────────────

// WsHub defines the broadcast operations the Scheduler needs from the WebSocket
// hub.  Declared here so the scheduler package does not import the ws/hub.go
// implementation and cause a circular dependency.
type WsHub interface {
	BroadcastPriceUpdate(msg ws.PriceUpdateMessage)
	BroadcastMarketResolved(msg ws.MarketResolvedMessage)
	BroadcastNewMarket(msg ws.NewMarketMessage)
}

// ──────────────────────────────────────────────────────────────────────────────
// Scheduler
// ──────────────────────────────────────────────────────────────────────────────

// Scheduler wires together the services and runs the three market lifecycle
// goroutines.  Call Start(ctx) once from main(); cancel the context to shut it
// down gracefully.
type Scheduler struct {
	marketSvc     *service.MarketService
	resolutionSvc *service.ResolutionService
	priceSvc      *service.PriceService
	hub           WsHub
	cfg           *config.Config
	logger        *slog.Logger
}

// NewScheduler creates a Scheduler.
func NewScheduler(
	marketSvc *service.MarketService,
	resolutionSvc *service.ResolutionService,
	priceSvc *service.PriceService,
	hub WsHub,
	cfg *config.Config,
	logger *slog.Logger,
) *Scheduler {
	return &Scheduler{
		marketSvc:     marketSvc,
		resolutionSvc: resolutionSvc,
		priceSvc:      priceSvc,
		hub:           hub,
		cfg:           cfg,
		logger:        logger,
	}
}

// Start launches the three background goroutines.  It returns immediately;
// all loops run until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	go s.marketCreationLoop(ctx)
	go s.resolutionLoop(ctx)
	go s.priceBroadcastLoop(ctx)
	s.logger.Info("scheduler started")
}

// ──────────────────────────────────────────────────────────────────────────────
// marketCreationLoop
// ──────────────────────────────────────────────────────────────────────────────

// marketCreationLoop opens a new market on each exact 5-minute boundary
// (e.g. 10:00, 10:05, 10:10 …).  On failure it retries up to 3 times with a
// 30-second pause before waiting for the next boundary.
func (s *Scheduler) marketCreationLoop(ctx context.Context) {
	defer s.recoverAndLog("marketCreationLoop")

	for {
		// Align to the next 5-minute mark.
		now := time.Now().UTC()
		next := now.Truncate(5 * time.Minute).Add(5 * time.Minute)
		wait := next.Sub(now)

		s.logger.Info("next market opens at", "time", next.Format(time.RFC3339), "wait", wait.Round(time.Second))

		select {
		case <-ctx.Done():
			s.logger.Info("marketCreationLoop: shutting down")
			return
		case <-time.After(wait):
		}

		end := next.Add(5 * time.Minute)
		if err := s.createMarketWithRetry(ctx, next, end); err != nil {
			s.logger.Error("marketCreationLoop: failed to create market after retries", "err", err)
		}
	}
}

// createMarketWithRetry attempts to create a market up to 3 times.
func (s *Scheduler) createMarketWithRetry(ctx context.Context, opens, closes time.Time) error {
	const maxAttempts = 3
	const retryDelay = 30 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		market, err := s.marketSvc.CreateMarket(ctx, opens, closes)
		if err == nil {
			// Broadcast the new market to all WS clients.
			if s.hub != nil {
				var openPrice decimal.Decimal
				if market.OpenPrice != nil {
					openPrice = *market.OpenPrice
				}
				s.hub.BroadcastNewMarket(ws.NewMarketMessage{
					Type:      ws.MsgTypeNewMarket,
					MarketID:  market.ID,
					OpensAt:   market.OpensAt,
					ClosesAt:  market.ClosesAt,
					OpenPrice: openPrice,
					Timestamp: time.Now().UTC(),
				})
			}
			s.logger.Info("market created", "id", market.ID, "opens", opens, "closes", closes)
			return nil
		}
		lastErr = err
		s.logger.Warn("market creation failed, retrying",
			"attempt", attempt, "max", maxAttempts, "err", err)

		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}
	}
	return lastErr
}

// ──────────────────────────────────────────────────────────────────────────────
// resolutionLoop
// ──────────────────────────────────────────────────────────────────────────────

// resolutionLoop checks for expired markets every 5 seconds and resolves them.
func (s *Scheduler) resolutionLoop(ctx context.Context) {
	defer s.recoverAndLog("resolutionLoop")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("resolutionLoop: shutting down")
			return
		case <-ticker.C:
			if err := s.resolutionSvc.ResolveExpiredMarkets(ctx); err != nil {
				s.logger.Error("resolutionLoop: ResolveExpiredMarkets", "err", err)
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// priceBroadcastLoop
// ──────────────────────────────────────────────────────────────────────────────

// priceBroadcastLoop fetches the weighted BTC price and active market odds
// every second and broadcasts a PriceUpdateMessage to all connected WS clients.
func (s *Scheduler) priceBroadcastLoop(ctx context.Context) {
	defer s.recoverAndLog("priceBroadcastLoop")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("priceBroadcastLoop: shutting down")
			return
		case <-ticker.C:
			s.broadcastPrice(ctx)
		}
	}
}

// broadcastPrice is the inner body of priceBroadcastLoop, extracted so that
// the defer/recover in the loop catches panics correctly.
func (s *Scheduler) broadcastPrice(ctx context.Context) {
	// Prefer the cheap cached read; fall back to a fresh fetch.
	price, ok := s.priceSvc.GetCachedPrice()
	if !ok {
		var err error
		price, _, err = s.priceSvc.GetWeightedPrice(ctx)
		if err != nil {
			s.logger.Warn("priceBroadcastLoop: price fetch failed", "err", err)
			return
		}
	}

	market, err := s.marketSvc.GetActiveMarket(ctx)
	if err != nil {
		// No open market — broadcast price-only update is fine; skip market fields.
		return
	}

	// Build price diff vs open price.
	var diff, diffPct decimal.Decimal
	if market.OpenPrice != nil && !market.OpenPrice.IsZero() {
		diff = price.Sub(*market.OpenPrice)
		diffPct = diff.Div(*market.OpenPrice).Mul(decimal.NewFromInt(100))
	}

	msg := ws.PriceUpdateMessage{
		Type:            ws.MsgTypePriceUpdate,
		MarketID:        market.ID,
		BTCPrice:        price,
		OpenPrice:       market.OpenPrice,
		Diff:            diff,
		DiffPct:         diffPct,
		UpOdds:          market.UpOdds(),
		DownOdds:        market.DownOdds(),
		UpPercent:       market.UpPercent(),
		DownPercent:     market.DownPercent(),
		PoolUp:          market.PoolUp,
		PoolDown:        market.PoolDown,
		TimeLeftSeconds: int64(market.TimeLeft().Seconds()),
		Timestamp:       time.Now().UTC(),
	}

	if s.hub != nil {
		s.hub.BroadcastPriceUpdate(msg)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BroadcastResolved — called by ResolutionService after settlement
// ──────────────────────────────────────────────────────────────────────────────

// BroadcastResolved sends a market-resolved notification to all WS clients.
// Intended to be called from ResolutionService after a successful commit.
func (s *Scheduler) BroadcastResolved(market *domain.Market) {
	if s.hub == nil {
		return
	}
	msg := ws.MarketResolvedMessage{
		Type:       ws.MsgTypeMarketResolved,
		MarketID:   market.ID,
		ClosePrice: *market.ClosePrice,
		OpenPrice:  market.OpenPrice,
		PoolUp:     market.PoolUp,
		PoolDown:   market.PoolDown,
		Timestamp:  time.Now().UTC(),
	}
	if market.Result != nil {
		msg.Result = *market.Result
	}
	s.hub.BroadcastMarketResolved(msg)
}

// ──────────────────────────────────────────────────────────────────────────────
// Panic recovery
// ──────────────────────────────────────────────────────────────────────────────

// recoverAndLog is deferred inside each goroutine to catch unexpected panics,
// log them, and allow the scheduler to continue running.
func (s *Scheduler) recoverAndLog(loop string) {
	if r := recover(); r != nil {
		s.logger.Error("PANIC recovered in scheduler loop",
			"loop", loop, "panic", r)
	}
}
