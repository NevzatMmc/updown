package handler

import (
	"net/http"

	"github.com/evetabi/prediction/internal/api/middleware"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/evetabi/prediction/internal/service"
	"github.com/gin-gonic/gin"
)

// UserHandler handles authentication and profile endpoints.
type UserHandler struct {
	authSvc    *service.AuthService
	walletRepo *repository.WalletRepository
}

// NewUserHandler creates a UserHandler.
func NewUserHandler(authSvc *service.AuthService, walletRepo *repository.WalletRepository) *UserHandler {
	return &UserHandler{authSvc: authSvc, walletRepo: walletRepo}
}

// Register godoc
// POST /api/auth/register
func (h *UserHandler) Register(c *gin.Context) {
	var req service.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}

	resp, err := h.authSvc.Register(c.Request.Context(), req)
	if err != nil {
		switch err {
		case domain.ErrEmailTaken:
			respondError(c, http.StatusConflict, "ERR_EMAIL_TAKEN", err.Error())
		case domain.ErrUsernameTaken:
			respondError(c, http.StatusConflict, "ERR_USERNAME_TAKEN", err.Error())
		default:
			respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "registration failed")
		}
		return
	}
	respondSuccess(c, http.StatusCreated, resp)
}

// Login godoc
// POST /api/auth/login
func (h *UserHandler) Login(c *gin.Context) {
	var body struct {
		Email    string `json:"email"    binding:"required,email"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}

	resp, err := h.authSvc.Login(c.Request.Context(), body.Email, body.Password)
	if err != nil {
		switch err {
		case domain.ErrInvalidCredentials:
			respondError(c, http.StatusUnauthorized, "ERR_INVALID_CREDENTIALS", err.Error())
		case domain.ErrUserInactive:
			respondError(c, http.StatusForbidden, "ERR_ACCOUNT_DISABLED", err.Error())
		default:
			respondError(c, http.StatusInternalServerError, "ERR_INTERNAL", "login failed")
		}
		return
	}
	respondSuccess(c, http.StatusOK, resp)
}

// Refresh godoc
// POST /api/auth/refresh
func (h *UserHandler) Refresh(c *gin.Context) {
	var body struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, "ERR_VALIDATION", err.Error())
		return
	}

	access, refresh, err := h.authSvc.RefreshToken(c.Request.Context(), body.RefreshToken)
	if err != nil {
		respondError(c, http.StatusUnauthorized, "ERR_INVALID_TOKEN", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{
		"access_token":  access,
		"refresh_token": refresh,
	})
}

// Me godoc
// GET /api/me [JWT required]
func (h *UserHandler) Me(c *gin.Context) {
	userID := middleware.GetUserID(c)
	wallet, err := h.walletRepo.GetByUserID(c.Request.Context(), userID)
	if err != nil {
		respondError(c, http.StatusNotFound, "ERR_WALLET_NOT_FOUND", err.Error())
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{
		"user_id":   userID,
		"balance":   wallet.Balance,
		"locked":    wallet.Locked,
		"available": wallet.Available(),
	})
}
