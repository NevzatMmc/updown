package handler

import (
	"net/http"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// mmOverrideEnabled is an in-process MM kill-switch toggle.
// In production this should live in Redis / the DB so it survives restarts.
var mmOverrideEnabled = true

// RiskHandler serves /admin/risk endpoints.
type RiskHandler struct {
	mmSvc     *service.MMService
	priceSvc  *service.PriceService
	marketSvc *service.MarketService
	cfg       *config.Config
}

// NewRiskHandler creates a RiskHandler.
func NewRiskHandler(
	mmSvc *service.MMService,
	priceSvc *service.PriceService,
	marketSvc *service.MarketService,
	cfg *config.Config,
) *RiskHandler {
	return &RiskHandler{mmSvc: mmSvc, priceSvc: priceSvc, marketSvc: marketSvc, cfg: cfg}
}

// Live godoc
// GET /admin/risk/live
func (h *RiskHandler) Live(c *gin.Context) {
	ctx := c.Request.Context()

	market, err := h.marketSvc.GetActiveMarket(ctx)
	if err != nil {
		respondSuccess(c, http.StatusOK, gin.H{"market": nil, "mm_enabled": mmOverrideEnabled})
		return
	}

	upPct := market.UpPercent()
	downPct := market.DownPercent()
	mmStats, _ := h.mmSvc.GetMMStats(ctx)

	respondSuccess(c, http.StatusOK, gin.H{
		"market_id":      market.ID,
		"pool_up":        market.PoolUp,
		"pool_down":      market.PoolDown,
		"total_pool":     market.TotalPool(),
		"up_pct":         upPct,
		"down_pct":       downPct,
		"risk_indicator": riskIndicator(upPct, downPct),
		"mm_enabled":     mmOverrideEnabled,
		"mm_stats":       mmStats,
	})
}

// MMStats godoc
// GET /admin/risk/mm-stats
func (h *RiskHandler) MMStats(c *gin.Context) {
	stats, err := h.mmSvc.GetMMStats(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, stats)
}

// MMOverride godoc
// POST /admin/risk/mm-override
// Body: {"enabled": false}
func (h *RiskHandler) MMOverride(c *gin.Context) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}
	mmOverrideEnabled = body.Enabled
	respondSuccess(c, http.StatusOK, gin.H{"mm_enabled": mmOverrideEnabled})
}

// Alerts godoc
// GET /admin/risk/alerts
func (h *RiskHandler) Alerts(c *gin.Context) {
	stats, err := h.mmSvc.GetMMStats(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}

	type Alert struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	var alerts []Alert

	maxDailyLoss := decimal.NewFromFloat(h.cfg.MM.MaxDailyLoss)
	minReserve := decimal.NewFromFloat(h.cfg.MM.MinReserve)
	pct90 := decimal.NewFromFloat(0.90)
	pct110 := decimal.NewFromFloat(1.10)

	if stats.DailySpend.GreaterThanOrEqual(maxDailyLoss.Mul(pct90)) {
		alerts = append(alerts, Alert{"RED", "MM daily loss limit at 90%+"})
	}
	if stats.PlatformReserve.LessThan(minReserve.Mul(pct110)) {
		alerts = append(alerts, Alert{"YELLOW", "Platform reserve approaching minimum threshold"})
	}
	if alerts == nil {
		alerts = []Alert{}
	}
	respondSuccess(c, http.StatusOK, gin.H{"alerts": alerts, "mm_stats": stats})
}

// ExchangeStatus godoc
// GET /admin/risk/exchange-status
func (h *RiskHandler) ExchangeStatus(c *gin.Context) {
	price, sources, err := h.priceSvc.GetWeightedPrice(c.Request.Context())
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	respondSuccess(c, http.StatusOK, gin.H{
		"weighted_price": price,
		"sources":        sources,
		"error":          errMsg,
	})
}
