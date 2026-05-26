package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/metrics"
)

func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		c.Next()

		duration := time.Since(start).Seconds()
		statusCode := strconv.Itoa(c.Writer.Status())
		method := c.Request.Method

		metrics.HTTPRequestsTotal.WithLabelValues(method, path, statusCode).Inc()
		metrics.HTTPRequestDurationSeconds.WithLabelValues(method, path, statusCode).Observe(duration)
	}
}
