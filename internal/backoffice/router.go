package backoffice

import (
	"net/http"
	"strings"

	"github.com/evetabi/prediction/internal/backoffice/handler"
	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/evetabi/prediction/internal/service"
	"github.com/evetabi/prediction/internal/ws"
	"github.com/gin-gonic/gin"
)

// BackofficeDeps bundles every dependency needed for the admin router.
type BackofficeDeps struct {
	AuthSvc    *service.AuthService
	MarketSvc  *service.MarketService
	MMSvc      *service.MMService
	UserRepo   *repository.UserRepository
	MarketRepo *repository.MarketRepository
	BetRepo    *repository.BetRepository
	WalletRepo *repository.WalletRepository
	Hub        *ws.Hub
	PriceSvc   *service.PriceService
	Cfg        *config.Config
}

// SetupBackofficeRouter creates the admin Gin engine on port 8081.
func SetupBackofficeRouter(deps BackofficeDeps) *gin.Engine {
	if deps.Cfg.IsProd() {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	r.Use(ipWhitelistMiddleware(deps.Cfg.Server.BackofficeAllowedIPs))

	dashH := handler.NewDashboardHandler(deps.MarketSvc, deps.MMSvc, deps.WalletRepo, deps.BetRepo, deps.Hub, deps.Cfg)
	marketH := handler.NewMarketAdminHandler(deps.MarketSvc, deps.BetRepo, deps.Cfg)
	userH := handler.NewUserAdminHandler(deps.UserRepo, deps.WalletRepo, deps.Cfg)
	riskH := handler.NewRiskHandler(deps.MMSvc, deps.PriceSvc, deps.MarketSvc, deps.Cfg)
	financeH := handler.NewFinanceHandler(deps.WalletRepo, deps.MarketRepo, deps.Cfg)

	jwtMW := adminJWTMiddleware(deps.AuthSvc)

	admin := r.Group("/admin")
	admin.Use(jwtMW)
	{
		admin.GET("/dashboard", dashH.Dashboard)

		// Markets
		m := admin.Group("/markets")
		{
			m.GET("", marketH.List)
			m.POST("", marketH.Create)
			m.GET("/:id", marketH.Detail)
			m.POST("/:id/suspend", marketH.Suspend)
			m.POST("/:id/cancel", marketH.Cancel)
			m.POST("/:id/resolve", marketH.Resolve)
		}

		// Users
		u := admin.Group("/users")
		{
			u.GET("", userH.List)
			u.GET("/:id", userH.Detail)
			u.POST("/:id/suspend", userH.Suspend)
			u.POST("/:id/activate", userH.Activate)
			u.POST("/:id/balance", userH.AdjustBalance)
			u.POST("/:id/role", userH.SetRole)
		}

		// Risk
		risk := admin.Group("/risk")
		{
			risk.GET("/live", riskH.Live)
			risk.GET("/mm-stats", riskH.MMStats)
			risk.POST("/mm-override", riskH.MMOverride)
			risk.GET("/alerts", riskH.Alerts)
			risk.GET("/exchange-status", riskH.ExchangeStatus)
		}

		// Finance
		fin := admin.Group("/finance")
		{
			fin.GET("/withdrawals", financeH.Withdrawals)
			fin.POST("/withdrawals/:id/approve", financeH.ApproveWithdrawal)
			fin.POST("/withdrawals/:id/reject", financeH.RejectWithdrawal)
			fin.GET("/report", financeH.Report)
			fin.GET("/transactions", financeH.Transactions)
		}
	}

	return r
}

// ── IP whitelist middleware ───────────────────────────────────────────────────

// ipWhitelistMiddleware blocks requests from IPs not in the allowlist.
// allowedIPs is a comma-separated string; empty means allow all.
func ipWhitelistMiddleware(allowedIPs string) gin.HandlerFunc {
	if allowedIPs == "" {
		return func(c *gin.Context) { c.Next() } // dev mode: no restriction
	}

	allowed := make(map[string]bool)
	for _, ip := range strings.Split(allowedIPs, ",") {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			allowed[ip] = true
		}
	}

	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		if !allowed[clientIP] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "access denied: your IP is not whitelisted",
			})
			return
		}
		c.Next()
	}
}

// ── Admin JWT middleware ──────────────────────────────────────────────────────

// adminJWTMiddleware validates a JWT and requires the caller to have a
// backoffice-capable role (admin, risk, finance, ops).
func adminJWTMiddleware(authSvc *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		claims, err := authSvc.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
		if err != nil || claims.TokenType != "access" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		// Require at least one backoffice role
		backofficeRoles := map[string]bool{
			"admin":    true,
			"risk":     true,
			"finance":  true,
			"ops":      true,
			"readonly": true,
		}
		if !backofficeRoles[claims.Role] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
			return
		}

		c.Set("userID", claims.Subject)
		c.Set("role", claims.Role)
		c.Next()
	}
}
