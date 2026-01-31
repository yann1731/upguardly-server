package monitor

import (
	"context"
	"net"
	"time"

	"upguardly-backend/internal/models"
)

type PortChecker struct{}

func (c *PortChecker) Check(ctx context.Context, target string, timeout time.Duration) models.CheckResult {
	start := time.Now()

	dialer := &net.Dialer{
		Timeout: timeout,
	}

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

	status := models.StatusUP
	message := "Port is open"

	if latency > 1000 {
		status = models.StatusDEGRADED
		message = "Port is open but high latency"
	}

	return models.CheckResult{
		Status:  status,
		Latency: latency,
		Message: message,
	}
}
