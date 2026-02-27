package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// ──────────────────────────────────────────────────────────────────────────────
// Standard admin response helpers (mirrors internal/api/handler/response.go)
// ──────────────────────────────────────────────────────────────────────────────

func respondSuccess(c *gin.Context, status int, data interface{}) {
	c.JSON(status, gin.H{"success": true, "data": data})
}

func respondError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{
		"success": false,
		"error":   msg,
		"code":    code,
	})
}

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

// adminPagination reads page/limit query params with sane defaults for admin views.
func adminPagination(c *gin.Context) (page, limit int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", "50"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 500 {
		limit = 50
	}
	return
}
