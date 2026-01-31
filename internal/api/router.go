package api

import (
	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/handlers"
	"upguardly-backend/internal/database"
)

func NewRouter(db *database.Client) *gin.Engine {
	router := gin.Default()

	router.Use(gin.Recovery())

	h := handlers.NewHandlers(db)

	v1 := router.Group("/v1")
	{
		v1.GET("/health", h.Health)

		monitors := v1.Group("/monitors")
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
		{
			alerts.PUT("/:id", h.UpdateAlert)
			alerts.DELETE("/:id", h.DeleteAlert)
		}
	}

	return router
}
