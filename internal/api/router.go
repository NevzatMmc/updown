package api

import (
	"net/http"

	"github.com/evetabi/prediction/internal/api/handler"
	"github.com/evetabi/prediction/internal/api/middleware"
	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/repository"
	"github.com/evetabi/prediction/internal/service"
	"github.com/evetabi/prediction/internal/ws"
	"github.com/gin-gonic/gin"
)

// RouterDeps bundles every dependency needed to build the router.
// Populated once in main() and passed to SetupRouter.
type RouterDeps struct {
	AuthSvc    *service.AuthService
	MarketSvc  *service.MarketService
	BetSvc     *service.BetService
	WalletRepo *repository.WalletRepository
	Hub        *ws.Hub
	Cfg        *config.Config
}

// SetupRouter creates and configures the main Gin engine with all routes,
// middleware, CORS, and rate limiting rules.
func SetupRouter(deps RouterDeps) *gin.Engine {
	if deps.Cfg.IsProd() {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// ── CORS ─────────────────────────────────────────────────────────────────
	r.Use(corsMiddleware(deps.Cfg))

	// ── Health check ─────────────────────────────────────────────────────────
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// ── Handlers ─────────────────────────────────────────────────────────────
	userH := handler.NewUserHandler(deps.AuthSvc, deps.WalletRepo)
	marketH := handler.NewMarketHandler(deps.MarketSvc)
	betH := handler.NewBetHandler(deps.BetSvc)
	walletH := handler.NewWalletHandler(deps.WalletRepo, deps.Cfg)

	// ── JWT middleware (shared) ───────────────────────────────────────────────
	jwtMW := middleware.JWTMiddleware(deps.AuthSvc)

	// ── Rate limiters ─────────────────────────────────────────────────────────
	authRL := middleware.RateLimitMiddleware(10) // 10 req/s per IP for auth endpoints
	betRL := middleware.RateLimitMiddleware(30)  // 30 req/s per IP for bet endpoints

	api := r.Group("/api")
	{
		// ── Auth (public, strict rate limit) ─────────────────────────────────
		auth := api.Group("/auth")
		auth.Use(authRL)
		{
			auth.POST("/register", userH.Register)
			auth.POST("/login", userH.Login)
			auth.POST("/refresh", userH.Refresh)
		}

		// ── Markets (public) ─────────────────────────────────────────────────
		markets := api.Group("/markets")
		{
			markets.GET("/active", marketH.GetActive)
			markets.GET("/history", marketH.GetHistory)
			markets.GET("", marketH.ListMarkets)
			markets.GET("/:id", marketH.GetByID)
		}

		// ── Authenticated routes ──────────────────────────────────────────────
		authed := api.Group("")
		authed.Use(jwtMW)
		{
			// Profile
			authed.GET("/me", userH.Me)

			// Bets
			bets := authed.Group("/bets")
			bets.Use(betRL)
			{
				bets.POST("", betH.PlaceBet)
				bets.GET("/my", betH.GetMyBets)
				bets.GET("/:id", betH.GetBetByID)
				bets.POST("/:id/exit", betH.ExitBet)
			}

			// Wallet
			wallet := authed.Group("/wallet")
			{
				wallet.GET("/balance", walletH.GetBalance)
				wallet.GET("/transactions", walletH.GetTransactions)
				wallet.POST("/withdraw", walletH.Withdraw)
				wallet.GET("/withdraw/status", walletH.GetWithdrawStatus)
			}
		}
	}

	// ── WebSocket ─────────────────────────────────────────────────────────────
	if deps.Hub != nil {
		r.GET("/ws", func(c *gin.Context) {
			deps.Hub.ServeWs(c.Writer, c.Request)
		})
	}

	return r
}

// ── CORS helper ───────────────────────────────────────────────────────────────

// corsMiddleware returns a gin middleware that sets appropriate CORS headers.
// In DEBUG mode all origins are allowed; in production only configured origins.
func corsMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		if !cfg.IsProd() {
			// Development: allow any origin
			c.Header("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			// Production: allow only evetabi.com (and www.)
			allowed := map[string]bool{
				"https://evetabi.com":     true,
				"https://www.evetabi.com": true,
			}
			if allowed[origin] {
				c.Header("Access-Control-Allow-Origin", origin)
			}
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
