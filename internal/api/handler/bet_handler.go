package handler

import (
	"net/http"

	"github.com/evetabi/prediction/internal/api/middleware"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BetHandler serves bet placement and cashout endpoints.
type BetHandler struct {
	betSvc *service.BetService
}

// NewBetHandler creates a BetHandler.
func NewBetHandler(betSvc *service.BetService) *BetHandler {
	return &BetHandler{betSvc: betSvc}
}

// PlaceBet godoc
// POST /api/bets [JWT]
// Body: {"market_id":"uuid","direction":"UP","amount":"500.00"}
func (h *BetHandler) PlaceBet(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var body struct {
		MarketID  string `json:"market_id"  binding:"required"`
		Direction string `json:"direction"  binding:"required"`
		Amount    string `json:"amount"     binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}

	marketID, err := uuid.Parse(body.MarketID)
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_MARKET_ID", "invalid market_id format")
		return
	}

	amount, err := decimal.NewFromString(body.Amount)
	if err != nil || amount.IsNegative() || amount.IsZero() {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_AMOUNT", "amount must be a positive decimal string")
		return
	}

	direction := domain.Outcome(body.Direction)
	req := domain.PlaceBetRequest{
		UserID:    userID,
		MarketID:  marketID,
		Direction: direction,
		Amount:    amount,
	}

	bet, err := h.betSvc.PlaceBet(c.Request.Context(), req)
	if err != nil {
		switch err {
		case domain.ErrBetTooSmall:
			respondError(c, http.StatusBadRequest, "ERR_BET_TOO_SMALL", err.Error())
		case domain.ErrInvalidOutcome:
			respondError(c, http.StatusBadRequest, "ERR_INVALID_DIRECTION", err.Error())
		case domain.ErrInsufficientBalance:
			respondError(c, http.StatusPaymentRequired, "ERR_INSUFFICIENT_BALANCE", err.Error())
		case domain.ErrMarketNotOpen:
			respondError(c, http.StatusConflict, "ERR_MARKET_NOT_OPEN", err.Error())
		case domain.ErrMarketNotFound:
			respondError(c, http.StatusNotFound, "ERR_MARKET_NOT_FOUND", err.Error())
		default:
			respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not place bet")
		}
		return
	}
	respondSuccess(c, http.StatusCreated, bet)
}

// ExitBet godoc
// POST /api/bets/:id/exit [JWT]
func (h *BetHandler) ExitBet(c *gin.Context) {
	userID := middleware.GetUserID(c)

	betID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_BET_ID", "invalid bet id")
		return
	}

	bet, err := h.betSvc.ExitBet(c.Request.Context(), betID, userID)
	if err != nil {
		switch err {
		case domain.ErrBetNotActive:
			respondError(c, http.StatusConflict, "ERR_BET_NOT_ACTIVE", err.Error())
		case domain.ErrForbidden:
			respondError(c, http.StatusForbidden, "ERR_FORBIDDEN", "this bet does not belong to you")
		case domain.ErrMarketNotOpen:
			respondError(c, http.StatusConflict, "ERR_MARKET_NOT_OPEN", err.Error())
		default:
			respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not exit bet")
		}
		return
	}
	respondSuccess(c, http.StatusOK, bet)
}

// GetMyBets godoc
// GET /api/bets/my?page=1&limit=20 [JWT]
func (h *BetHandler) GetMyBets(c *gin.Context) {
	userID := middleware.GetUserID(c)
	page, limit := parsePagination(c)
	offset := (page - 1) * limit

	bets, err := h.betSvc.GetMyBets(c.Request.Context(), userID, limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not fetch bets")
		return
	}
	respondList(c, bets, len(bets), page, limit)
}

// GetBetByID godoc
// GET /api/bets/:id [JWT]
func (h *BetHandler) GetBetByID(c *gin.Context) {
	userID := middleware.GetUserID(c)

	betID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_BET_ID", "invalid bet id")
		return
	}

	bet, err := h.betSvc.GetBetByID(c.Request.Context(), betID, userID)
	if err != nil {
		switch err {
		case domain.ErrForbidden:
			respondError(c, http.StatusForbidden, "ERR_FORBIDDEN", "access denied")
		case domain.ErrBetNotActive:
			respondError(c, http.StatusNotFound, "ERR_NOT_FOUND", "bet not found")
		default:
			respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not fetch bet")
		}
		return
	}
	respondSuccess(c, http.StatusOK, bet)
}
