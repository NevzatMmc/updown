package handler

import (
	"net/http"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// UserAdminHandler serves /admin/users endpoints.
type UserAdminHandler struct {
	userRepo   *repository.UserRepository
	walletRepo *repository.WalletRepository
	cfg        *config.Config
}

// NewUserAdminHandler creates a UserAdminHandler.
func NewUserAdminHandler(
	userRepo *repository.UserRepository,
	walletRepo *repository.WalletRepository,
	cfg *config.Config,
) *UserAdminHandler {
	return &UserAdminHandler{userRepo: userRepo, walletRepo: walletRepo, cfg: cfg}
}

// List godoc
// GET /admin/users?page=1&limit=20&search=
func (h *UserAdminHandler) List(c *gin.Context) {
	page, limit := adminPagination(c)
	offset := (page - 1) * limit

	users, total, err := h.userRepo.List(c.Request.Context(), limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondList(c, users, total, page, limit)
}

// Detail godoc
// GET /admin/users/:id
func (h *UserAdminHandler) Detail(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid user id")
		return
	}

	ctx := c.Request.Context()
	user, err := h.userRepo.GetByID(ctx, id)
	if err != nil {
		if err == domain.ErrUserNotFound {
			respondError(c, http.StatusNotFound, "ERR_NOT_FOUND", err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}

	wallet, _ := h.walletRepo.GetByUserID(ctx, id)
	txns, _ := h.walletRepo.GetTransactions(ctx, id, 50, 0)

	respondSuccess(c, http.StatusOK, gin.H{
		"user":         user,
		"wallet":       wallet,
		"transactions": txns,
	})
}

// Suspend godoc
// POST /admin/users/:id/suspend
func (h *UserAdminHandler) Suspend(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid user id")
		return
	}
	if err = h.userRepo.SetActive(c.Request.Context(), id, false); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"user_id": id, "is_active": false})
}

// Activate godoc
// POST /admin/users/:id/activate
func (h *UserAdminHandler) Activate(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid user id")
		return
	}
	if err = h.userRepo.SetActive(c.Request.Context(), id, true); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"user_id": id, "is_active": true})
}

// AdjustBalance godoc
// POST /admin/users/:id/balance
// Body: {"amount": "500", "note": "bonus"}
func (h *UserAdminHandler) AdjustBalance(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid user id")
		return
	}
	var body struct {
		Amount string `json:"amount" binding:"required"`
		Note   string `json:"note"`
	}
	if err = c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}
	amount, err := decimal.NewFromString(body.Amount)
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_AMOUNT", "amount must be a decimal string")
		return
	}

	ctx := c.Request.Context()
	wallet, err := h.walletRepo.GetByUserID(ctx, id)
	if err != nil {
		respondError(c, http.StatusNotFound, "ERR_WALLET_NOT_FOUND", err.Error())
		return
	}

	// Use a direct DB exec (no separate tx needed — admin adjustment is single op)
	// We reuse AddBalance (positive) or DeductBalance logic via a signed amount.
	// For simplicity: exec raw update and write audit log.
	txType := domain.TxBonus
	if amount.IsNegative() {
		txType = domain.TxWithdraw
	}

	if err = h.walletRepo.AdminAdjustBalance(ctx, id, amount); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}

	note := body.Note
	if note == "" {
		note = "Admin balance adjustment"
	}
	txn := &domain.Transaction{
		ID:            uuid.New(),
		WalletID:      wallet.ID,
		Type:          txType,
		Amount:        amount.Abs(),
		BalanceBefore: wallet.Balance,
		BalanceAfter:  wallet.Balance.Add(amount),
		Description:   note,
	}
	_ = h.walletRepo.LogTransactionDirect(ctx, txn)

	respondSuccess(c, http.StatusOK, gin.H{
		"user_id":     id,
		"amount":      amount,
		"new_balance": wallet.Balance.Add(amount),
	})
}

// SetRole godoc
// POST /admin/users/:id/role
// Body: {"role": "finance"}
func (h *UserAdminHandler) SetRole(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ID", "invalid user id")
		return
	}
	var body struct {
		Role string `json:"role" binding:"required"`
	}
	if err = c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}
	role := domain.UserRole(body.Role)
	validRoles := map[domain.UserRole]bool{
		domain.RoleUser:     true,
		domain.RoleAdmin:    true,
		domain.RoleRisk:     true,
		domain.RoleFinance:  true,
		domain.RoleOps:      true,
		domain.RoleReadOnly: true,
	}
	if !validRoles[role] {
		respondError(c, http.StatusBadRequest, "ERR_INVALID_ROLE", "unknown role")
		return
	}
	if err = h.userRepo.UpdateRole(c.Request.Context(), id, role); err != nil {
		respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"user_id": id, "role": role})
}
