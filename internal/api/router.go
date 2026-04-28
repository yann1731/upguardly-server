package api

import (
	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/handlers"
	"upguardly-backend/internal/auth"
	"upguardly-backend/internal/database"
)

func NewRouter(db *database.Client) *gin.Engine {
	router := gin.Default()

	router.Use(gin.Recovery())
	router.Use(CORSMiddleware())
	router.Use(auth.SuperTokensMiddleware())

	h := handlers.NewHandlers(db)

	v1 := router.Group("/v1")
	{
		v1.GET("/health", h.Health)

		monitors := v1.Group("/monitors")
		monitors.Use(auth.VerifySession(nil))
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

		alerts := v1.Group("/alerts")
		alerts.Use(auth.VerifySession(nil))
		{
			alerts.PUT("/:id", h.UpdateAlert)
			alerts.DELETE("/:id", h.DeleteAlert)
		}
	}

	return router
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}

		c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, Content-Length, Accept-Encoding, Authorization, "+
				"anti-csrf, rid, fdi-version, st-auth-mode")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
