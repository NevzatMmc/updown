package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"
)

// ──────────────────────────────────────────────────────────────────────────────
// Request / Response types
// ──────────────────────────────────────────────────────────────────────────────

// RegisterRequest contains the fields required to create a new user account.
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

// RegisterResponse is returned on successful registration.
type RegisterResponse struct {
	User         *domain.User `json:"user"`
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
}

// LoginResponse is returned on successful login.
type LoginResponse struct {
	User         *domain.User `json:"user"`
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
}

// TokenPair holds both tokens returned by generateTokenPair.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

// ──────────────────────────────────────────────────────────────────────────────
// JWT claims
// ──────────────────────────────────────────────────────────────────────────────

// AppClaims extends jwt.RegisteredClaims with application-specific fields.
type AppClaims struct {
	jwt.RegisteredClaims
	Role      string `json:"role"`
	TokenType string `json:"type"` // "access" or "refresh"
}

// ──────────────────────────────────────────────────────────────────────────────
// AuthService
// ──────────────────────────────────────────────────────────────────────────────

// AuthService handles user registration, login, and JWT token operations.
type AuthService struct {
	db         *sqlx.DB
	userRepo   *repository.UserRepository
	walletRepo *repository.WalletRepository
	cfg        *config.Config
}

// NewAuthService creates an AuthService.
func NewAuthService(
	db *sqlx.DB,
	userRepo *repository.UserRepository,
	walletRepo *repository.WalletRepository,
	cfg *config.Config,
) *AuthService {
	return &AuthService{
		db:         db,
		userRepo:   userRepo,
		walletRepo: walletRepo,
		cfg:        cfg,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Register
// ──────────────────────────────────────────────────────────────────────────────

// Register creates a new user account with a wallet seeded with a 1 000 TRY
// registration bonus.  The user row, wallet row, and audit log are all written
// in a single atomic transaction.
func (s *AuthService) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("auth_service.Register: hash: %w", err)
	}

	now := time.Now().UTC()
	user := &domain.User{
		ID:           uuid.New(),
		Email:        req.Email,
		Username:     req.Username,
		PasswordHash: string(hash),
		Role:         domain.RoleUser,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	bonus := decimal.NewFromInt(1000)

	tx, txErr := s.db.BeginTxx(ctx, nil)
	if txErr != nil {
		return nil, fmt.Errorf("auth_service.Register: begin tx: %w", txErr)
	}
	defer func() {
		if txErr != nil {
			_ = tx.Rollback()
		}
	}()

	// Insert user (unique constraint errors are mapped to domain errors)
	if txErr = s.insertUserTx(ctx, tx, user); txErr != nil {
		return nil, txErr
	}

	// Create wallet with bonus balance
	walletID := uuid.New()
	if _, txErr = tx.ExecContext(ctx, `
		INSERT INTO wallets (id, user_id, balance, locked, created_at, updated_at)
		VALUES ($1, $2, $3, 0, $4, $4)`,
		walletID, user.ID, bonus, now); txErr != nil {
		return nil, fmt.Errorf("auth_service.Register: create wallet: %w", txErr)
	}

	// Bonus audit log
	txn := &domain.Transaction{
		ID:            uuid.New(),
		WalletID:      walletID,
		Type:          domain.TxBonus,
		Amount:        bonus,
		BalanceBefore: decimal.Zero,
		BalanceAfter:  bonus,
		Description:   "Kayıt bonusu",
		CreatedAt:     now,
	}
	if txErr = s.walletRepo.LogTransaction(ctx, tx, txn); txErr != nil {
		return nil, fmt.Errorf("auth_service.Register: log bonus: %w", txErr)
	}

	if txErr = tx.Commit(); txErr != nil {
		return nil, fmt.Errorf("auth_service.Register: commit: %w", txErr)
	}

	pair, err := s.generateTokenPair(user.ID, string(user.Role))
	if err != nil {
		return nil, fmt.Errorf("auth_service.Register: tokens: %w", err)
	}

	return &RegisterResponse{
		User:         user,
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
	}, nil
}

// insertUserTx inserts a user row within an existing transaction.
func (s *AuthService) insertUserTx(ctx context.Context, tx *sqlx.Tx, u *domain.User) error {
	query := `
		INSERT INTO users (id, email, username, password_hash, role, is_active, created_at, updated_at)
		VALUES (:id, :email, :username, :password_hash, :role, :is_active, :created_at, :updated_at)`
	if _, err := tx.NamedExecContext(ctx, query, u); err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "users_email_key") {
			return domain.ErrEmailTaken
		}
		if strings.Contains(errStr, "users_username_key") {
			return domain.ErrUsernameTaken
		}
		return fmt.Errorf("auth_service.insertUserTx: %w", err)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Login
// ──────────────────────────────────────────────────────────────────────────────

// Login validates credentials and returns a fresh token pair.
func (s *AuthService) Login(ctx context.Context, email, password string) (*LoginResponse, error) {
	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		// Map not-found to a generic credential error to prevent user enumeration.
		return nil, domain.ErrInvalidCredentials
	}

	if err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	if !user.IsActive {
		return nil, domain.ErrUserInactive
	}

	pair, err := s.generateTokenPair(user.ID, string(user.Role))
	if err != nil {
		return nil, fmt.Errorf("auth_service.Login: tokens: %w", err)
	}

	return &LoginResponse{
		User:         user,
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// RefreshToken
// ──────────────────────────────────────────────────────────────────────────────

// RefreshToken validates a refresh token and issues a new token pair.
func (s *AuthService) RefreshToken(ctx context.Context, refreshToken string) (string, string, error) {
	claims, err := s.parseToken(refreshToken)
	if err != nil {
		return "", "", domain.ErrTokenInvalid
	}
	if claims.TokenType != "refresh" {
		return "", "", domain.ErrTokenInvalid
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return "", "", domain.ErrTokenInvalid
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return "", "", domain.ErrUserNotFound
	}
	if !user.IsActive {
		return "", "", domain.ErrUserInactive
	}

	pair, err := s.generateTokenPair(user.ID, string(user.Role))
	if err != nil {
		return "", "", fmt.Errorf("auth_service.RefreshToken: %w", err)
	}
	return pair.AccessToken, pair.RefreshToken, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Token helpers
// ──────────────────────────────────────────────────────────────────────────────

// generateTokenPair creates a signed access token (AccessTTL) and a signed
// refresh token (RefreshTTL) for the given user.
func (s *AuthService) generateTokenPair(userID uuid.UUID, role string) (TokenPair, error) {
	now := time.Now().UTC()
	secret := []byte(s.cfg.JWT.AccessSecret) // same secret for both; type claim differentiates

	accessClaims := AppClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.JWT.AccessTTL)),
		},
		Role:      role,
		TokenType: "access",
	}
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}

	refreshClaims := AppClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.JWT.RefreshTTL)),
		},
		TokenType: "refresh",
	}
	refresh, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign refresh token: %w", err)
	}

	return TokenPair{AccessToken: access, RefreshToken: refresh}, nil
}

// parseToken validates the token signature, algorithm, and expiry.
func (s *AuthService) parseToken(tokenString string) (*AppClaims, error) {
	secret := []byte(s.cfg.JWT.AccessSecret)
	tok, err := jwt.ParseWithClaims(tokenString, &AppClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil || !tok.Valid {
		return nil, domain.ErrTokenInvalid
	}
	claims, ok := tok.Claims.(*AppClaims)
	if !ok {
		return nil, domain.ErrTokenInvalid
	}
	return claims, nil
}

// ParseAccessToken is exported for use by the JWT middleware.
func (s *AuthService) ParseAccessToken(tokenString string) (*AppClaims, error) {
	return s.parseToken(tokenString)
}
