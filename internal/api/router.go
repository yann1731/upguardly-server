package api

import (
	"net/http"

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

func NewRouter(store models.Store, websiteDomain string, m *mailer.Mailer, s *stripeservice.Client) *gin.Engine {
	router := gin.Default()

	router.Use(gin.Recovery())
	router.Use(middleware.MetricsMiddleware())

	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

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

		// Stripe webhook — public, verified by signature
		v1.POST("/webhooks/stripe", h.StripeWebhook)

		protected := v1.Group("")
		protected.Use(middleware.AuthRequired())
		{
			// Invitation accept — requires auth (to know which user is accepting)
			protected.POST("/invitations/:token/accept", h.AcceptInvitation)

			// Monitors
			monitors := protected.Group("/monitors")
			{
				monitors.POST("", h.CreateMonitor)
				monitors.GET("", h.ListMonitors)
				monitors.GET("/:id", h.GetMonitor)
				monitors.PUT("/:id", h.UpdateMonitor)
				monitors.DELETE("/:id", h.DeleteMonitor)
				monitors.GET("/:id/results", h.GetMonitorResults)

				monitors.POST("/:id/alerts", h.CreateAlert)
				monitors.GET("/:id/alerts", h.ListAlerts)
			}

			// Alerts
			alerts := protected.Group("/alerts")
			{
				alerts.PUT("/:id", h.UpdateAlert)
				alerts.DELETE("/:id", h.DeleteAlert)
			}

			// Organizations
			orgs := protected.Group("/organizations")
			{
				orgs.POST("", h.CreateOrg)
				orgs.GET("", h.ListOrgs)

				org := orgs.Group("/:id")
				{
					org.GET("", middleware.RequireOrgRole(store, models.OrgRoleViewer), h.GetOrg)
					org.PUT("", middleware.RequireOrgRole(store, models.OrgRoleAdmin), h.UpdateOrg)
					org.DELETE("", middleware.RequireOrgRole(store, models.OrgRoleOwner), h.DeleteOrg)

					// Members
					org.GET("/members", middleware.RequireOrgRole(store, models.OrgRoleViewer), h.ListMembers)
					org.PUT("/members/:memberId", middleware.RequireOrgRole(store, models.OrgRoleAdmin), h.UpdateMemberRole)
					org.DELETE("/members/:memberId", middleware.RequireOrgRole(store, models.OrgRoleMember), h.RemoveMember)

					// Invitations
					org.POST("/invitations", middleware.RequireOrgRole(store, models.OrgRoleAdmin), h.CreateInvitation)
					org.GET("/invitations", middleware.RequireOrgRole(store, models.OrgRoleAdmin), h.ListInvitations)
					org.DELETE("/invitations/:invId", middleware.RequireOrgRole(store, models.OrgRoleAdmin), h.RevokeInvitation)

					// Subscription
					org.GET("/subscription", middleware.RequireOrgRole(store, models.OrgRoleViewer), h.GetSubscription)
					org.POST("/subscription", middleware.RequireOrgRole(store, models.OrgRoleOwner), h.CreateCheckout)
					org.POST("/subscription/portal", middleware.RequireOrgRole(store, models.OrgRoleOwner), h.CreatePortal)
				}
			}
		}
	}

	return router
}
