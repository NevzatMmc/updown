package handler

import (
	"net/http"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/evetabi/prediction/internal/service"
	"github.com/evetabi/prediction/internal/ws"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// DashboardHandler serves the /admin/dashboard endpoint.
type DashboardHandler struct {
	marketSvc  *service.MarketService
	mmSvc      *service.MMService
	walletRepo *repository.WalletRepository
	betRepo    *repository.BetRepository
	hub        *ws.Hub
	cfg        *config.Config
}

// NewDashboardHandler creates a DashboardHandler.
func NewDashboardHandler(
	marketSvc *service.MarketService,
	mmSvc *service.MMService,
	walletRepo *repository.WalletRepository,
	betRepo *repository.BetRepository,
	hub *ws.Hub,
	cfg *config.Config,
) *DashboardHandler {
	return &DashboardHandler{
		marketSvc:  marketSvc,
		mmSvc:      mmSvc,
		walletRepo: walletRepo,
		betRepo:    betRepo,
		hub:        hub,
		cfg:        cfg,
	}
}

// Dashboard godoc
// GET /admin/dashboard
func (h *DashboardHandler) Dashboard(c *gin.Context) {
	ctx := c.Request.Context()

	// ── Active market ────────────────────────────────────────────────────────
	var marketData gin.H
	market, err := h.marketSvc.GetActiveMarket(ctx)
	if err == nil {
		total := market.TotalPool()
		var upPct, downPct decimal.Decimal
		if !total.IsZero() {
			upPct = market.PoolUp.Div(total).Mul(decimal.NewFromInt(100)).RoundDown(2)
			downPct = decimal.NewFromInt(100).Sub(upPct)
		}
		marketData = gin.H{
			"id":             market.ID,
			"status":         market.Status,
			"opens_at":       market.OpensAt,
			"closes_at":      market.ClosesAt,
			"pool_up":        market.PoolUp,
			"pool_down":      market.PoolDown,
			"total_pool":     total,
			"up_pct":         upPct,
			"down_pct":       downPct,
			"time_left_sec":  int64(market.TimeLeft().Seconds()),
			"risk_indicator": riskIndicator(upPct, downPct),
		}
	}

	// ── Platform wallet ──────────────────────────────────────────────────────
	var platformBalance decimal.Decimal
	wallet, err := h.walletRepo.GetPlatformWallet(ctx)
	if err == nil {
		platformBalance = wallet.Balance
	}

	// ── MM stats ─────────────────────────────────────────────────────────────
	var mmStats *service.MMStats
	mmStats, _ = h.mmSvc.GetMMStats(ctx)

	// ── Daily P&L (commission - MM losses) ──────────────────────────────────
	var dailyPnL decimal.Decimal
	if mmStats != nil {
		dailyPnL = mmStats.DailyPnL
	}

	// ── Pending withdrawals ───────────────────────────────────────────────────
	pending, _ := h.walletRepo.GetWithdrawRequests(ctx, "pending", 1000, 0)
	var pendingTotal decimal.Decimal
	for _, p := range pending {
		pendingTotal = pendingTotal.Add(p.Amount)
	}

	// ── WS connections ────────────────────────────────────────────────────────
	var wsConnections int
	if h.hub != nil {
		wsConnections = h.hub.ConnectedCount()
	}

	respondSuccess(c, http.StatusOK, gin.H{
		"timestamp":        time.Now().UTC(),
		"active_market":    marketData,
		"platform_balance": platformBalance,
		"daily_pnl":        dailyPnL,
		"mm_daily_spend":   mmDataField(mmStats, "spend"),
		"mm_daily_limit":   h.cfg.MM.MaxDailyLoss,
		"mm_interventions": mmDataField(mmStats, "interventions"),
		"pending_withdrawals": gin.H{
			"count": len(pending),
			"total": pendingTotal,
		},
		"ws_connections": wsConnections,
	})
}

// riskIndicator returns GREEN/YELLOW/RED based on pool imbalance.
func riskIndicator(upPct, downPct decimal.Decimal) string {
	dominant := upPct
	if downPct.GreaterThan(upPct) {
		dominant = downPct
	}
	switch {
	case dominant.GreaterThan(decimal.NewFromInt(85)):
		return "RED"
	case dominant.GreaterThan(decimal.NewFromInt(70)):
		return "YELLOW"
	default:
		return "GREEN"
	}
}

func mmDataField(stats *service.MMStats, field string) interface{} {
	if stats == nil {
		return decimal.Zero
	}
	switch field {
	case "spend":
		return stats.DailySpend
	case "interventions":
		return stats.TotalInterventions
	default:
		return decimal.Zero
	}
}
