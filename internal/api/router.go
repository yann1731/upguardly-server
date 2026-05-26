package api

import (
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/supertokens/supertokens-golang/supertokens"

	"upguardly-backend/internal/api/handlers"
	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

func NewRouter(store models.Store, websiteDomain string) *gin.Engine {
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

	h := handlers.NewHandlers(store)

	v1 := router.Group("/v1")
	{
		v1.GET("/health", h.Health)

		protected := v1.Group("")
		protected.Use(middleware.AuthRequired())
		{
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

			alerts := protected.Group("/alerts")
			{
				alerts.PUT("/:id", h.UpdateAlert)
				alerts.DELETE("/:id", h.DeleteAlert)
			}
		}
	}

	return router
}
