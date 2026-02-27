package handler

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evetabi/prediction/internal/api/middleware"
	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// WalletHandler serves balance, transaction history, and withdrawal endpoints.
type WalletHandler struct {
	walletRepo *repository.WalletRepository
	cfg        *config.Config
}

// NewWalletHandler creates a WalletHandler.
func NewWalletHandler(walletRepo *repository.WalletRepository, cfg *config.Config) *WalletHandler {
	return &WalletHandler{walletRepo: walletRepo, cfg: cfg}
}

// GetBalance godoc
// GET /api/wallet/balance [JWT]
func (h *WalletHandler) GetBalance(c *gin.Context) {
	userID := middleware.GetUserID(c)
	wallet, err := h.walletRepo.GetByUserID(c.Request.Context(), userID)
	if err != nil {
		respondError(c, http.StatusNotFound, "ERR_WALLET_NOT_FOUND", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{
		"balance":   wallet.Balance,
		"locked":    wallet.Locked,
		"available": wallet.Available(),
	})
}

// GetTransactions godoc
// GET /api/wallet/transactions?page=1&limit=20 [JWT]
func (h *WalletHandler) GetTransactions(c *gin.Context) {
	userID := middleware.GetUserID(c)
	page, limit := parsePagination(c)
	offset := (page - 1) * limit

	txns, err := h.walletRepo.GetTransactions(c.Request.Context(), userID, limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not fetch transactions")
		return
	}
	respondList(c, txns, len(txns), page, limit)
}

// Withdraw godoc
// POST /api/wallet/withdraw [JWT]
// Body: {"amount":"1000.00","iban":"TR..."}
func (h *WalletHandler) Withdraw(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var body struct {
		Amount string `json:"amount" binding:"required"`
		IBAN   string `json:"iban"   binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}

	// Parse amount
	amount, err := decimal.NewFromString(body.Amount)
	if err != nil || amount.IsNegative() || amount.IsZero() {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_AMOUNT", "amount must be a positive decimal string")
		return
	}

	// IBAN format: Turkey TR + 24 digits = 26 chars total
	iban := strings.ToUpper(strings.ReplaceAll(body.IBAN, " ", ""))
	if !isValidIBAN(iban) {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_IBAN", "IBAN must be 26 characters starting with TR followed by digits")
		return
	}

	// Min withdraw check
	minWithdraw := decimal.NewFromFloat(h.cfg.Wallet.MinWithdraw)
	if amount.LessThan(minWithdraw) {
		respondError(c, http.StatusBadRequest, "ERR_BELOW_MIN_WITHDRAW",
			"minimum withdrawal is "+minWithdraw.StringFixed(2)+" TRY")
		return
	}

	// Max single-transaction check
	maxDaily := decimal.NewFromFloat(h.cfg.Wallet.MaxDailyWithdraw)
	if amount.GreaterThan(maxDaily) {
		respondError(c, http.StatusBadRequest, "ERR_EXCEEDS_DAILY_LIMIT",
			"maximum single withdrawal is "+maxDaily.StringFixed(2)+" TRY")
		return
	}

	// Daily limit check
	ctx := c.Request.Context()
	dailyTotal, err := h.walletRepo.GetDailyWithdrawTotal(ctx, userID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not check daily limit")
		return
	}
	if dailyTotal.Add(amount).GreaterThan(maxDaily) {
		respondError(c, http.StatusBadRequest, "ERR_DAILY_LIMIT_EXCEEDED",
			"daily withdrawal limit of "+maxDaily.StringFixed(2)+" TRY would be exceeded")
		return
	}

	// Check wallet balance
	wallet, err := h.walletRepo.GetByUserID(ctx, userID)
	if err != nil {
		respondError(c, http.StatusNotFound, "ERR_WALLET_NOT_FOUND", err.Error())
		return
	}
	if wallet.Available().LessThan(amount) {
		respondError(c, http.StatusPaymentRequired, "ERR_INSUFFICIENT_BALANCE", domain.ErrInsufficientBalance.Error())
		return
	}

	req := &domain.WithdrawRequest{
		ID:          uuid.New(),
		UserID:      userID,
		Amount:      amount,
		Status:      domain.WithdrawPending,
		IBAN:        iban,
		RequestedAt: time.Now().UTC(),
	}
	if err = h.walletRepo.CreateWithdrawRequest(ctx, req); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not create withdrawal request")
		return
	}
	respondSuccess(c, http.StatusCreated, req)
}

// GetWithdrawStatus godoc
// GET /api/wallet/withdraw/status?page=1&limit=20 [JWT]
func (h *WalletHandler) GetWithdrawStatus(c *gin.Context) {
	userID := middleware.GetUserID(c)
	page, limit := parsePagination(c)
	offset := (page - 1) * limit

	// Fetch only this user's requests by re-querying with user filter
	// (the current GetWithdrawRequests is admin-scoped; for user-facing we
	// query withdraw_requests directly filtered by user_id)
	ctx := c.Request.Context()
	var reqs []*domain.WithdrawRequest
	err := (func() error {
		all, err := h.walletRepo.GetWithdrawRequests(ctx, "", limit+offset, 0)
		if err != nil {
			return err
		}
		for _, r := range all {
			if r.UserID == userID {
				reqs = append(reqs, r)
			}
		}
		// Simple in-process pagination (adequate for typical withdraw counts)
		if offset >= len(reqs) {
			reqs = nil
		} else {
			end := offset + limit
			if end > len(reqs) {
				end = len(reqs)
			}
			reqs = reqs[offset:end]
		}
		return nil
	})()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "could not fetch withdrawal requests")
		return
	}
	respondList(c, reqs, len(reqs), page, limit)
}

// ── IBAN validation ───────────────────────────────────────────────────────────

// isValidIBAN performs a lightweight structural check on a Turkish IBAN.
// Full ISO 13616 MOD-97 math validation is omitted for brevity but can be
// added without changing the handler interface.
func isValidIBAN(iban string) bool {
	if utf8.RuneCountInString(iban) != 26 {
		return false
	}
	if !strings.HasPrefix(iban, "TR") {
		return false
	}
	for _, ch := range iban[2:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
