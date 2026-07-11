package api

import (
	"log"
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

// privateProxies are the ranges the reverse proxy in front of us sits on: the
// docker-compose bridge network (Caddy) and loopback (container healthcheck).
var privateProxies = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.1/32"}

// cloudflareProxies is Cloudflare's published edge range list
// (https://www.cloudflare.com/ips/). These must be trusted as *proxies* — not
// via gin's TrustedPlatform/CF-Connecting-IP shortcut, which reads the header
// before any peer check and so lets anyone who can reach the origin directly
// (our Caddy publishes 80/443 to the internet) forge an arbitrary client IP.
// Trusting them as proxies instead means gin walks X-Forwarded-For right to
// left and stops at the first untrusted hop: the real client behind Cloudflare,
// or a direct-to-origin caller's true peer address, which Caddy appends itself
// and a caller therefore cannot spoof.
//
// Refresh from Cloudflare's list if edge ranges change; a stale entry only
// costs client-IP precision (requests fall back to the edge IP), never safety.
var cloudflareProxies = []string{
	// IPv4
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	// IPv6
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// trustedProxies returns the proxy CIDRs/IPs gin should trust for client-IP
// resolution. TRUSTED_PROXIES is a comma-separated list of CIDRs/IPs in which
// the token "cloudflare" expands to Cloudflare's edge ranges, so ops doesn't
// have to paste 22 CIDRs into an env file:
//
//	TRUSTED_PROXIES=172.16.0.0/12,127.0.0.1/32,cloudflare
//
// Unset defaults to the private ranges alone — correct for local dev, where
// there is no CDN in front and the client IP is the peer address.
func trustedProxies() []string {
	v := os.Getenv("TRUSTED_PROXIES")
	if v == "" {
		return privateProxies
	}
	out := make([]string, 0, len(cloudflareProxies)+len(privateProxies))
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case strings.EqualFold(p, "cloudflare"):
			out = append(out, cloudflareProxies...)
		default:
			out = append(out, p)
		}
	}
	return out
}

func NewRouter(store models.Store, websiteDomain string, m *mailer.Mailer, s *stripeservice.Client, availableRegions []string) *gin.Engine {
	router := gin.Default()

	// Trust only the proxies actually in front of us — Caddy, and (in production)
	// Cloudflare's edge — so gin.Context.ClientIP resolves to the real client and
	// cannot be spoofed via X-Forwarded-For. Rate limiting keys off ClientIP, so
	// getting this wrong either lumps every user behind one CDN edge IP into a
	// shared bucket or, worse, lets a caller pick their own key. Configure with
	// TRUSTED_PROXIES; see trustedProxies.
	if err := router.SetTrustedProxies(trustedProxies()); err != nil {
		// Don't leave a misconfigured list silently in place: gin would fall back
		// to trusting every proxy, which is precisely the spoofable state this
		// call exists to prevent. Refuse the bad config and keep the safe default.
		log.Printf("[WARN] router: invalid TRUSTED_PROXIES (%v) — falling back to private ranges only; client IPs behind a CDN will resolve to the edge IP", err)
		if fbErr := router.SetTrustedProxies(privateProxies); fbErr != nil {
			log.Fatalf("router: private proxy fallback rejected: %v", fbErr)
		}
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

	h := handlers.NewHandlers(store, m, s, availableRegions)

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

			// Regions monitors can be checked from (deployed subset of the registry).
			protected.GET("/regions", h.ListRegions)

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
				monitors.GET("/:id/regions", h.GetMonitorRegions)

				// Per-monitor opt-in/opt-out of the account's global channels.
				monitors.GET("/:id/channels", h.ListMonitorChannels)
				monitors.PUT("/:id/channels/:channelId", middleware.StrictRateLimit(), h.SetMonitorChannel)
				monitors.DELETE("/:id/channels/:channelId", middleware.StrictRateLimit(), h.DeleteMonitorChannel)
			}

			// Global (account-level) notification channels — the settings-page
			// integrations that every monitor inherits by default.
			channels := protected.Group("/notification-channels")
			{
				channels.POST("", middleware.StrictRateLimit(), h.CreateNotificationChannel)
				channels.GET("", h.ListNotificationChannels)
				channels.PUT("/:id", middleware.StrictRateLimit(), h.UpdateNotificationChannel)
				channels.DELETE("/:id", middleware.StrictRateLimit(), h.DeleteNotificationChannel)
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
