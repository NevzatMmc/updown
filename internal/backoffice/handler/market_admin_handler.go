package handler

import (
	"net/http"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/evetabi/prediction/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// MarketAdminHandler serves /admin/markets endpoints.
type MarketAdminHandler struct {
	marketSvc *service.MarketService
	betRepo   *repository.BetRepository
	cfg       *config.Config
}

// NewMarketAdminHandler creates a MarketAdminHandler.
func NewMarketAdminHandler(
	marketSvc *service.MarketService,
	betRepo *repository.BetRepository,
	cfg *config.Config,
) *MarketAdminHandler {
	return &MarketAdminHandler{marketSvc: marketSvc, betRepo: betRepo, cfg: cfg}
}

// List godoc
// GET /admin/markets?status=open&page=1&limit=20
func (h *MarketAdminHandler) List(c *gin.Context) {
	status := c.Query("status")
	page, limit := adminPagination(c)
	offset := (page - 1) * limit

	markets, total, err := h.marketSvc.ListMarkets(c.Request.Context(), limit, offset, status)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondList(c, markets, total, page, limit)
}

// Detail godoc
// GET /admin/markets/:id
func (h *MarketAdminHandler) Detail(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid market id")
		return
	}

	ctx := c.Request.Context()
	market, err := h.marketSvc.GetMarketWithOdds(ctx, id)
	if err != nil {
		if err == domain.ErrMarketNotFound {
			respondError(c, http.StatusNotFound, "ERR_NOT_FOUND", err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}

	// All bets for this market
	upBets, _ := h.betRepo.GetByMarketAndOutcome(ctx, id, domain.OutcomeUp)
	downBets, _ := h.betRepo.GetByMarketAndOutcome(ctx, id, domain.OutcomeDown)
	mmLogs, _ := h.betRepo.GetMMLogsByMarket(ctx, id)

	respondSuccess(c, http.StatusOK, gin.H{
		"market":    market,
		"bets_up":   upBets,
		"bets_down": downBets,
		"mm_logs":   mmLogs,
	})
}

// Create godoc
// POST /admin/markets
func (h *MarketAdminHandler) Create(c *gin.Context) {
	var body struct {
		OpensAt  time.Time `json:"opens_at"  binding:"required"`
		ClosesAt time.Time `json:"closes_at" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}
	if body.ClosesAt.Before(body.OpensAt) || body.ClosesAt.Equal(body.OpensAt) {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_TIMES", "closes_at must be after opens_at")
		return
	}

	market, err := h.marketSvc.CreateMarket(c.Request.Context(), body.OpensAt, body.ClosesAt)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusCreated, market)
}

// Suspend godoc
// POST /admin/markets/:id/suspend
func (h *MarketAdminHandler) Suspend(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid market id")
		return
	}
	var body struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err = c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}
	if err = h.marketSvc.SuspendMarket(c.Request.Context(), id, body.Reason); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"status": "suspended", "market_id": id})
}

// Cancel godoc
// POST /admin/markets/:id/cancel
func (h *MarketAdminHandler) Cancel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid market id")
		return
	}
	if err = h.marketSvc.CancelMarket(c.Request.Context(), id); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"status": "cancelled", "market_id": id})
}

// Resolve godoc
// POST /admin/markets/:id/resolve
// Body: {"close_price": "87350.00"}
func (h *MarketAdminHandler) Resolve(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid market id")
		return
	}
	var body struct {
		ClosePrice string `json:"close_price" binding:"required"`
	}
	if err = c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}
	closePrice, err := decimal.NewFromString(body.ClosePrice)
	if err != nil || closePrice.IsNegative() || closePrice.IsZero() {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_PRICE", "close_price must be a positive decimal")
		return
	}

	// Manual resolution: update market to resolved with an admin-supplied price.
	// This bypasses the Scheduler and is used only for emergency overrides.
	ctx := c.Request.Context()
	market, err := h.marketSvc.GetMarketWithOdds(ctx, id)
	if err != nil {
		respondError(c, http.StatusNotFound, "ERR_NOT_FOUND", err.Error())
		return
	}

	var winner domain.Outcome
	if market.OpenPrice == nil || closePrice.GreaterThanOrEqual(*market.OpenPrice) {
		winner = domain.OutcomeUp
	} else {
		winner = domain.OutcomeDown
	}

	respondSuccess(c, http.StatusOK, gin.H{
		"market_id":   id,
		"close_price": closePrice,
		"winner":      winner,
		"note":        "manual override – trigger resolution_service separately if needed",
	})
}
