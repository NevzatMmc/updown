package middleware

import (
	"net/http"
	"strings"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/evetabi/prediction/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ContextKey constants for gin.Context values set by middleware.
const (
	CtxUserID = "userID"
	CtxRole   = "role"
)

// ──────────────────────────────────────────────────────────────────────────────
// JWTMiddleware
// ──────────────────────────────────────────────────────────────────────────────

// JWTMiddleware validates the Bearer token in the Authorization header.
// On success it stores userID (uuid.UUID) and role (string) in the gin context.
func JWTMiddleware(authSvc *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": domain.ErrUnauthorized.Error(),
			})
			return
		}

		tokenString := strings.TrimPrefix(header, "Bearer ")
		claims, err := authSvc.ParseAccessToken(tokenString)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": domain.ErrTokenInvalid.Error(),
			})
			return
		}

		if claims.TokenType != "access" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "token type must be access",
			})
			return
		}

		userID, err := uuid.Parse(claims.Subject)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": domain.ErrTokenInvalid.Error(),
			})
			return
		}

		c.Set(CtxUserID, userID)
		c.Set(CtxRole, claims.Role)
		c.Next()
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// RoleMiddleware
// ──────────────────────────────────────────────────────────────────────────────

// RoleMiddleware ensures the authenticated user has one of the allowed roles.
// Must be placed after JWTMiddleware in the chain.
func RoleMiddleware(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		role, _ := c.Get(CtxRole)
		roleStr, _ := role.(string)
		if !allowed[roleStr] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": domain.ErrForbidden.Error(),
			})
			return
		}
		c.Next()
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// AdminMiddleware
// ──────────────────────────────────────────────────────────────────────────────

// AdminMiddleware allows only admin-tier roles to access the route.
// Must be placed after JWTMiddleware in the chain.
func AdminMiddleware() gin.HandlerFunc {
	return RoleMiddleware(
		string(domain.RoleAdmin),
		string(domain.RoleRisk),
		string(domain.RoleFinance),
		string(domain.RoleOps),
	)
}

// ──────────────────────────────────────────────────────────────────────────────
// Helper — extract userID from context (for use in handlers)
// ──────────────────────────────────────────────────────────────────────────────

// GetUserID retrieves the authenticated user's UUID from the gin context.
// Returns uuid.Nil if the middleware was not applied or the value is missing.
func GetUserID(c *gin.Context) uuid.UUID {
	v, exists := c.Get(CtxUserID)
	if !exists {
		return uuid.Nil
	}
	id, _ := v.(uuid.UUID)
	return id
}

// GetRole retrieves the authenticated user's role string from the gin context.
func GetRole(c *gin.Context) string {
	v, _ := c.Get(CtxRole)
	r, _ := v.(string)
	return r
}
