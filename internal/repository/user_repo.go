package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// UserRepository handles all database operations for Users.
type UserRepository struct {
	db *sqlx.DB
}

// NewUserRepository creates a new UserRepository.
func NewUserRepository(db *sqlx.DB) *UserRepository {
	return &UserRepository{db: db}
}

// Create inserts a new user row.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	query := `
		INSERT INTO users (id, email, username, password_hash, role, is_active, created_at, updated_at)
		VALUES (:id, :email, :username, :password_hash, :role, :is_active, :created_at, :updated_at)`
	if _, err := r.db.NamedExecContext(ctx, query, u); err != nil {
		// Detect unique constraint violations and surface as domain errors
		if isPgUniqueViolation(err, "users_email_key") {
			return domain.ErrEmailTaken
		}
		if isPgUniqueViolation(err, "users_username_key") {
			return domain.ErrUsernameTaken
		}
		return fmt.Errorf("user_repo.Create: %w", err)
	}
	return nil
}

// GetByID fetches a user by primary key.
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	var u domain.User
	err := r.db.GetContext(ctx, &u, `SELECT * FROM users WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("user_repo.GetByID: %w", err)
	}
	return &u, nil
}

// GetByEmail fetches a user by email address (used for login).
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	var u domain.User
	err := r.db.GetContext(ctx, &u, `SELECT * FROM users WHERE email = $1`, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("user_repo.GetByEmail: %w", err)
	}
	return &u, nil
}

// GetByUsername fetches a user by username.
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	var u domain.User
	err := r.db.GetContext(ctx, &u, `SELECT * FROM users WHERE username = $1`, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("user_repo.GetByUsername: %w", err)
	}
	return &u, nil
}

// List returns a paginated list of all users.
// Returns (users, totalCount, error).
func (r *UserRepository) List(ctx context.Context, limit, offset int) ([]*domain.User, int, error) {
	var users []*domain.User
	var total int

	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM users`); err != nil {
		return nil, 0, fmt.Errorf("user_repo.List count: %w", err)
	}
	if err := r.db.SelectContext(ctx, &users,
		`SELECT * FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset); err != nil {
		return nil, 0, fmt.Errorf("user_repo.List select: %w", err)
	}
	return users, total, nil
}

// UpdateRole changes a user's role (back-office operation).
func (r *UserRepository) UpdateRole(ctx context.Context, userID uuid.UUID, role domain.UserRole) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET role = $1, updated_at = now() WHERE id = $2`,
		string(role), userID)
	if err != nil {
		return fmt.Errorf("user_repo.UpdateRole: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// SetActive activates or deactivates a user account.
func (r *UserRepository) SetActive(ctx context.Context, userID uuid.UUID, active bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET is_active = $1, updated_at = now() WHERE id = $2`,
		active, userID)
	if err != nil {
		return fmt.Errorf("user_repo.SetActive: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// isPgUniqueViolation checks whether err is a PostgreSQL unique constraint
// violation for the given constraint name.
func isPgUniqueViolation(err error, constraintName string) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "unique constraint") &&
		contains(err.Error(), constraintName)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
