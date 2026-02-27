package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ──────────────────────────────────────────────────────────────────────────────
// Standard response helpers
// ──────────────────────────────────────────────────────────────────────────────

// respondSuccess writes {"success": true, "data": data} with the given status.
func respondSuccess(c *gin.Context, status int, data interface{}) {
	c.JSON(status, gin.H{
		"success": true,
		"data":    data,
	})
}

// respondError writes {"success": false, "error": msg, "code": code}.
func respondError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{
		"success": false,
		"error":   msg,
		"code":    code,
	})
}

// respondList writes {"success": true, "data": items, "meta": {...}}.
func respondList(c *gin.Context, items interface{}, total, page, limit int) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
		"meta": gin.H{
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}
