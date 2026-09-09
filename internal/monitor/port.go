package monitor

import (
	"context"
	"time"

	"upguardly-backend/internal/models"
)

type PortChecker struct{}

func (c *PortChecker) Check(ctx context.Context, target string, timeout time.Duration) models.CheckResult {
	start := time.Now()

	dialer := SafeDialer()
	dialer.Timeout = timeout

	conn, err := dialer.DialContext(ctx, "tcp", target)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		return models.CheckResult{
			Status:  models.StatusDOWN,
			Latency: latency,
			Message: "Connection failed: " + err.Error(),
		}
	}
	defer conn.Close()

	// Slow-but-open classification happens in the scheduler (runCheck), which
	// knows the monitor's effective degraded threshold.
	return models.CheckResult{
		Status:  models.StatusUP,
		Latency: latency,
		Message: "Port is open",
	}
}
