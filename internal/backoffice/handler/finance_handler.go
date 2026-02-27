package handler

import (
	"net/http"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// FinanceHandler serves /admin/finance endpoints.
type FinanceHandler struct {
	walletRepo *repository.WalletRepository
	marketRepo *repository.MarketRepository
	cfg        *config.Config
}

// NewFinanceHandler creates a FinanceHandler.
func NewFinanceHandler(
	walletRepo *repository.WalletRepository,
	marketRepo *repository.MarketRepository,
	cfg *config.Config,
) *FinanceHandler {
	return &FinanceHandler{walletRepo: walletRepo, marketRepo: marketRepo, cfg: cfg}
}

// Withdrawals godoc
// GET /admin/finance/withdrawals?status=pending&page=1&limit=20
func (h *FinanceHandler) Withdrawals(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	page, limit := adminPagination(c)
	offset := (page - 1) * limit

	reqs, err := h.walletRepo.GetWithdrawRequests(c.Request.Context(), status, limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondList(c, reqs, len(reqs), page, limit)
}

// ApproveWithdrawal godoc
// POST /admin/finance/withdrawals/:id/approve
func (h *FinanceHandler) ApproveWithdrawal(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid withdrawal id")
		return
	}
	adminID := adminUserID(c)
	if err = h.walletRepo.UpdateWithdrawStatus(
		c.Request.Context(), id, domain.WithdrawApproved, "Approved by admin", adminID,
	); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"id": id, "status": "approved"})
}

// RejectWithdrawal godoc
// POST /admin/finance/withdrawals/:id/reject
// Body: {"note": "reason"}
func (h *FinanceHandler) RejectWithdrawal(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid withdrawal id")
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&body) // note is optional
	adminID := adminUserID(c)
	if err = h.walletRepo.UpdateWithdrawStatus(
		c.Request.Context(), id, domain.WithdrawRejected, body.Note, adminID,
	); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"id": id, "status": "rejected"})
}

// Report godoc
// GET /admin/finance/report?from=2024-01-01&to=2024-01-31
func (h *FinanceHandler) Report(c *gin.Context) {
	ctx := c.Request.Context()

	fromStr := c.Query("from")
	toStr := c.Query("to")

	var from, to time.Time
	var err error
	if fromStr != "" {
		from, err = time.Parse("2006-01-02", fromStr)
		if err != nil {
			respondError(c, http.StatusBadRequest, "ERR_INVALID_DATE", "from must be YYYY-MM-DD")
			return
		}
	} else {
		from = time.Now().UTC().AddDate(0, -1, 0).Truncate(24 * time.Hour) // default: last 30 days
	}
	if toStr != "" {
		to, err = time.Parse("2006-01-02", toStr)
		if err != nil {
			respondError(c, http.StatusBadRequest, "ERR_INVALID_DATE", "to must be YYYY-MM-DD")
			return
		}
		to = to.Add(24 * time.Hour) // inclusive
	} else {
		to = time.Now().UTC()
	}

	report, err := h.marketRepo.GetFinanceReport(ctx, from, to)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, report)
}

// Transactions godoc
// GET /admin/finance/transactions?page=1&limit=50
func (h *FinanceHandler) Transactions(c *gin.Context) {
	page, limit := adminPagination(c)
	offset := (page - 1) * limit
	txns, err := h.walletRepo.GetPlatformTransactions(c.Request.Context(), limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondList(c, txns, len(txns), page, limit)
}

// ── helper ────────────────────────────────────────────────────────────────────

// adminUserID extracts the admin's UUID from the gin context (set by adminJWTMiddleware).
func adminUserID(c *gin.Context) uuid.UUID {
	v, _ := c.Get("userID")
	s, _ := v.(string)
	id, _ := uuid.Parse(s)
	return id
}

// ensure decimal is used (imported via domain/wallet types)
var _ = decimal.Zero
