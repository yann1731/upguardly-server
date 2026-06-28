package metrics

import (
	"upguardly-backend/internal/models"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	MonitorChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upguardly_monitor_checks_total",
		Help: "Total number of monitor checks performed.",
	}, []string{"monitor_id", "monitor_name", "monitor_type", "status"})

	MonitorCheckLatencyMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "upguardly_monitor_check_latency_ms",
		Help:    "Latency of monitor checks in milliseconds.",
		Buckets: []float64{10, 50, 100, 250, 500, 1000, 2500, 5000},
	}, []string{"monitor_id", "monitor_name", "monitor_type", "status"})

	MonitorStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upguardly_monitor_status",
		Help: "Current status of monitors (1=UP, 0=DEGRADED, -1=DOWN).",
	}, []string{"monitor_id", "monitor_name", "monitor_type"})

	AlertsSentTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upguardly_alerts_sent_total",
		Help: "Total number of alerts sent.",
	}, []string{"monitor_id", "monitor_name", "channel", "status"})

	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upguardly_http_requests_total",
		Help: "Total number of HTTP requests handled.",
	}, []string{"method", "path", "status_code"})

	HTTPRequestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "upguardly_http_request_duration_seconds",
		Help:    "Duration of HTTP requests in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status_code"})

	RateLimitBlockedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upguardly_ratelimit_blocked_total",
		Help: "Total number of requests rejected (429) by the rate limiter.",
	}, []string{"limiter"})

	RateLimitRedisErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "upguardly_ratelimit_redis_errors_total",
		Help: "Total number of Redis errors in the rate limiter that caused it to fail open (degraded to allowing the request). Non-zero means the global limit is not being reliably enforced.",
	})
)

func StatusToGaugeValue(status models.Status) float64 {
	switch status {
	case models.StatusUP:
		return 1
	case models.StatusDEGRADED:
		return 0
	case models.StatusDOWN:
		return -1
	default:
		return -1
	}
}
