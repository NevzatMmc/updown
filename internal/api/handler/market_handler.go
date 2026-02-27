package handler

import (
	"net/http"
	"strconv"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// MarketHandler serves market query endpoints.
type MarketHandler struct {
	marketSvc *service.MarketService
}

// NewMarketHandler creates a MarketHandler.
func NewMarketHandler(marketSvc *service.MarketService) *MarketHandler {
	return &MarketHandler{marketSvc: marketSvc}
}

// GetActive godoc
// GET /api/markets/active
func (h *MarketHandler) GetActive(c *gin.Context) {
	market, err := h.marketSvc.GetActiveMarket(c.Request.Context())
	if err != nil {
		if err == domain.ErrNoOpenMarket {
			respondError(c, http.StatusNotFound, "ERR_NO_OPEN_MARKET", err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not fetch active market")
		return
	}
	respondSuccess(c, http.StatusOK, market)
}

// GetByID godoc
// GET /api/markets/:id
func (h *MarketHandler) GetByID(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid market id")
		return
	}

	market, err := h.marketSvc.GetMarketWithOdds(c.Request.Context(), id)
	if err != nil {
		if err == domain.ErrMarketNotFound {
			respondError(c, http.StatusNotFound, "ERR_NOT_FOUND", err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not fetch market")
		return
	}
	respondSuccess(c, http.StatusOK, market)
}

// GetHistory godoc
// GET /api/markets/history?page=1&limit=20
func (h *MarketHandler) GetHistory(c *gin.Context) {
	page, limit := parsePagination(c)
	offset := (page - 1) * limit

	markets, err := h.marketSvc.GetMarketHistory(c.Request.Context(), limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not fetch history")
		return
	}
	respondList(c, markets, len(markets), page, limit)
}

// ListMarkets godoc
// GET /api/markets?status=resolved&page=1&limit=20
func (h *MarketHandler) ListMarkets(c *gin.Context) {
	status := c.Query("status")
	page, limit := parsePagination(c)
	offset := (page - 1) * limit

	markets, total, err := h.marketSvc.ListMarkets(c.Request.Context(), limit, offset, status)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not list markets")
		return
	}
	respondList(c, markets, total, page, limit)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func parsePagination(c *gin.Context) (page, limit int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return
}
