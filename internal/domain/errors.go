package domain

import (
	"errors"
)

// ──────────────────────────────────────────────────────────────────────────────
// Sentinel errors — compare with errors.Is()
// ──────────────────────────────────────────────────────────────────────────────

// Market errors
var (
	// ErrMarketNotFound is returned when no market matches the given criteria.
	ErrMarketNotFound = errors.New("market not found")

	// ErrMarketNotOpen is returned when a bet placement or cashout is attempted
	// on a market that is not in StatusOpen.
	ErrMarketNotOpen = errors.New("market is not open for betting")

	// ErrMarketAlreadyResolved is returned when trying to resolve an already-
	// resolved market.
	ErrMarketAlreadyResolved = errors.New("market is already resolved")

	// ErrNoOpenMarket is returned when there is no active market available.
	ErrNoOpenMarket = errors.New("no open market available")
)

// Bet errors
var (
	// ErrBetNotActive is returned when a cashout is attempted on a bet that is
	// not in BetStatusActive.
	ErrBetNotActive = errors.New("bet is not active")

	// ErrBetAlreadyResolved is returned when trying to re-resolve a bet.
	ErrBetAlreadyResolved = errors.New("bet is already resolved")

	// ErrBetTooSmall is returned when a bet amount is below the configured minimum.
	ErrBetTooSmall = errors.New("bet amount is below the minimum")

	// ErrInvalidOutcome is returned when the direction is not UP or DOWN.
	ErrInvalidOutcome = errors.New("invalid bet outcome: must be UP or DOWN")
)

// User / wallet errors
var (
	// ErrUserNotFound is returned when no user matches the given criteria.
	ErrUserNotFound = errors.New("user not found")

	// ErrEmailTaken is returned on registration when the email already exists.
	ErrEmailTaken = errors.New("email address is already registered")

	// ErrUsernameTaken is returned on registration when the username already exists.
	ErrUsernameTaken = errors.New("username is already taken")

	// ErrInvalidCredentials is returned when login credentials are wrong.
	ErrInvalidCredentials = errors.New("invalid email or password")

	// ErrUserInactive is returned when a suspended/banned user attempts an action.
	ErrUserInactive = errors.New("user account is inactive")

	// ErrInsufficientBalance is returned when a user's available balance is too
	// low to place a bet or make a withdrawal.
	ErrInsufficientBalance = errors.New("insufficient wallet balance")

	// ErrWithdrawLimitExceeded is returned when a withdrawal would breach the
	// user's daily or per-transaction limit.
	ErrWithdrawLimitExceeded = errors.New("withdrawal limit exceeded")

	// ErrBelowMinWithdraw is returned when the requested withdrawal amount is
	// below the configured minimum.
	ErrBelowMinWithdraw = errors.New("withdrawal amount is below the minimum")

	// ErrWalletNotFound is returned when no wallet exists for the requested user.
	ErrWalletNotFound = errors.New("wallet not found")
)

// Market Maker errors
var (
	// ErrMMReserveInsufficient is returned when the house reserve falls below the
	// configured minimum and MM injection is blocked.
	ErrMMReserveInsufficient = errors.New("market maker reserve is below minimum threshold")

	// ErrMMDailyLossExceeded is returned when the Daily Loss Limit is reached and
	// the MM will no longer inject liquidity for the day.
	ErrMMDailyLossExceeded = errors.New("market maker daily loss limit exceeded")
)

// Auth errors
var (
	// ErrUnauthorized is returned when a valid token is not present.
	ErrUnauthorized = errors.New("unauthorized")

	// ErrForbidden is returned when the authenticated user lacks the required role.
	ErrForbidden = errors.New("forbidden: insufficient permissions")

	// ErrTokenExpired is returned when a JWT or refresh token has passed its TTL.
	ErrTokenExpired = errors.New("token has expired")

	// ErrTokenInvalid is returned when a token cannot be parsed or its signature
	// does not match.
	ErrTokenInvalid = errors.New("token is invalid")
)

// ──────────────────────────────────────────────────────────────────────────────
// Helper predicates
// ──────────────────────────────────────────────────────────────────────────────

// notFoundErrors collects all "entity not found" sentinel errors so that
// IsNotFound can stay in sync automatically.
var notFoundErrors = []error{
	ErrMarketNotFound,
	ErrUserNotFound,
	ErrWalletNotFound,
	ErrNoOpenMarket,
}

// IsNotFound returns true when err (or any error in its chain) is one of the
// domain "not found" errors. Use this instead of comparing error values directly
// when you need to translate domain errors to HTTP 404 responses.
func IsNotFound(err error) bool {
	for _, target := range notFoundErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// IsConflict returns true for errors that represent a state conflict (e.g.
// duplicate registration or double-resolution).
func IsConflict(err error) bool {
	conflictErrors := []error{
		ErrEmailTaken,
		ErrUsernameTaken,
		ErrMarketAlreadyResolved,
		ErrBetAlreadyResolved,
		ErrMarketNotOpen,
	}
	for _, target := range conflictErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// IsAuthError returns true for authentication/authorisation errors.
func IsAuthError(err error) bool {
	authErrors := []error{
		ErrUnauthorized,
		ErrForbidden,
		ErrTokenExpired,
		ErrTokenInvalid,
		ErrInvalidCredentials,
	}
	for _, target := range authErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
