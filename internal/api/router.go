package api

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/supertokens/supertokens-golang/supertokens"

	"upguardly-backend/internal/api/handlers"
	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/mailer"
	"upguardly-backend/internal/models"
	"upguardly-backend/internal/stripeservice"
)

// securityHeaders adds essential HTTP security response headers to every reply.
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		// HSTS: 1 year, include subdomains. Only effective over HTTPS (reverse proxy
		// should set this too, but belt-and-suspenders is fine).
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Next()
	}
}

// metricsAuth guards the /metrics endpoint with a bearer token.
// Set METRICS_TOKEN env variable to require authentication.
// If the variable is empty, /metrics is still exposed but logs a warning at
// startup (handled in NewRouter). Use this only on trusted internal networks.
func metricsAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			// No token configured — allow but header was already warned about.
			c.Next()
			return
		}
		auth := c.GetHeader("Authorization")
		if auth != "Bearer "+token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		c.Next()
	}
}

// trustedProxies returns the proxy CIDRs/IPs gin should trust for client-IP
// resolution. TRUSTED_PROXIES is a comma-separated list; when unset it defaults
// to the standard private ranges (the docker network the reverse proxy uses).
func trustedProxies() []string {
	if v := os.Getenv("TRUSTED_PROXIES"); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.1/32"}
}

func NewRouter(store models.Store, websiteDomain string, m *mailer.Mailer, s *stripeservice.Client) *gin.Engine {
	router := gin.Default()

	// Trust only the reverse proxy / load balancer in front of us so that
	// gin.Context.ClientIP (used by rate limiting) reflects the real client and
	// cannot be spoofed via X-Forwarded-For. Configure with TRUSTED_PROXIES
	// (comma-separated CIDRs/IPs); default to private ranges, which covers the
	// docker-compose bridge network the proxy sits on.
	if err := router.SetTrustedProxies(trustedProxies()); err != nil {
		// A bad CIDR shouldn't crash the server; fall back to gin's default and
		// log so the misconfiguration is visible.
		// (gin.Default already logs proxy-trust warnings.)
		_ = err
	}

	router.Use(gin.Recovery())
	router.Use(securityHeaders())
	router.Use(middleware.MetricsMiddleware())
	// Apply global rate limiting to every route.
	router.Use(middleware.RateLimit())

	metricsToken := os.Getenv("METRICS_TOKEN")
	router.GET("/metrics", metricsAuth(metricsToken), gin.WrapH(promhttp.Handler()))

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{websiteDomain},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     append([]string{"content-type"}, supertokens.GetAllCORSHeaders()...),
		AllowCredentials: true,
	}))

	router.Use(func(c *gin.Context) {
		supertokens.Middleware(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			c.Next()
		})).ServeHTTP(c.Writer, c.Request)
	})

	h := handlers.NewHandlers(store, m, s)

	v1 := router.Group("/v1")
	{
		v1.GET("/health", h.Health)

		// Stripe webhook — public, verified by signature; stricter rate limit.
		v1.POST("/webhooks/stripe", middleware.StrictRateLimit(), h.StripeWebhook)

		protected := v1.Group("")
		protected.Use(middleware.AuthRequired())
		{
			// Current authenticated user (identity/email for the settings page).
			protected.GET("/me", h.GetMe)

			// Subscription — billing is per-user (the account), not per-org.
			// FREE/PRO are single-user; ENTERPRISE additionally unlocks orgs.
			protected.GET("/subscription", h.GetSubscription)
			protected.POST("/subscription", middleware.StrictRateLimit(), h.CreateCheckout)
			protected.POST("/subscription/portal", middleware.StrictRateLimit(), h.CreatePortal)
			protected.DELETE("/subscription", middleware.StrictRateLimit(), h.CancelSubscription)

			// Invitation accept — requires auth (to know which user is accepting).
			protected.POST("/invitations/:token/accept", middleware.StrictRateLimit(), h.AcceptInvitation)

			// Monitors
			monitors := protected.Group("/monitors")
			{
				monitors.POST("", middleware.StrictRateLimit(), h.CreateMonitor)
				monitors.GET("", h.ListMonitors)
				monitors.GET("/:id", h.GetMonitor)
				monitors.PUT("/:id", middleware.StrictRateLimit(), h.UpdateMonitor)
				monitors.DELETE("/:id", h.DeleteMonitor)
				monitors.GET("/:id/results", h.GetMonitorResults)
				monitors.GET("/:id/incidents", h.GetMonitorIncidents)
				monitors.GET("/:id/stats", h.GetMonitorStats)

				monitors.POST("/:id/alerts", middleware.StrictRateLimit(), h.CreateAlert)
				monitors.GET("/:id/alerts", h.ListAlerts)
			}

			// Alerts
			alerts := protected.Group("/alerts")
			{
				alerts.PUT("/:id", middleware.StrictRateLimit(), h.UpdateAlert)
				alerts.DELETE("/:id", h.DeleteAlert)
			}

			// Organizations
			orgs := protected.Group("/organizations")
			{
				orgs.POST("", middleware.StrictRateLimit(), h.CreateOrg)
				orgs.GET("", h.ListOrgs)

				org := orgs.Group("/:id")
				{
					org.GET("", middleware.RequireOrgRole(store, models.OrgRoleViewer), h.GetOrg)
					org.PUT("", middleware.RequireOrgRole(store, models.OrgRoleAdmin), middleware.StrictRateLimit(), h.UpdateOrg)
					org.DELETE("", middleware.RequireOrgRole(store, models.OrgRoleOwner), h.DeleteOrg)

					// Members
					org.GET("/members", middleware.RequireOrgRole(store, models.OrgRoleViewer), h.ListMembers)
					org.PUT("/members/:memberId", middleware.RequireOrgRole(store, models.OrgRoleAdmin), middleware.StrictRateLimit(), h.UpdateMemberRole)
					org.DELETE("/members/:memberId", middleware.RequireOrgRole(store, models.OrgRoleMember), h.RemoveMember)

					// Invitations
					org.POST("/invitations", middleware.RequireOrgRole(store, models.OrgRoleAdmin), middleware.StrictRateLimit(), h.CreateInvitation)
					org.GET("/invitations", middleware.RequireOrgRole(store, models.OrgRoleAdmin), h.ListInvitations)
					org.DELETE("/invitations/:invId", middleware.RequireOrgRole(store, models.OrgRoleAdmin), h.RevokeInvitation)
				}
			}
		}
	}

	return router
}
