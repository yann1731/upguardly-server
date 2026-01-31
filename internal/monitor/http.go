package monitor

import (
	"context"
	"net/http"
	"time"

	"upguardly-backend/internal/models"
)

type HTTPChecker struct{}

func (c *HTTPChecker) Check(ctx context.Context, target string, timeout time.Duration) models.CheckResult {
	client := &http.Client{
		Timeout: timeout,
	}

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return models.CheckResult{
			Status:  models.StatusDOWN,
			Latency: int(time.Since(start).Milliseconds()),
			Message: "Failed to create request: " + err.Error(),
		}
	}

	req.Header.Set("User-Agent", "Upguardly-Monitor/1.0")

	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		return models.CheckResult{
			Status:  models.StatusDOWN,
			Latency: latency,
			Message: "Request failed: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	statusCode := resp.StatusCode

	var status models.Status
	var message string

	switch {
	case statusCode >= 200 && statusCode < 300:
		status = models.StatusUP
		message = "OK"
	case statusCode >= 300 && statusCode < 400:
		status = models.StatusUP
		message = "Redirect"
	case statusCode >= 400 && statusCode < 500:
		status = models.StatusDOWN
		message = "Client error"
	case statusCode >= 500:
		status = models.StatusDOWN
		message = "Server error"
	default:
		status = models.StatusDEGRADED
		message = "Unexpected status"
	}

	if latency > 2000 && status == models.StatusUP {
		status = models.StatusDEGRADED
		message = "High latency"
	}

	return models.CheckResult{
		Status:     status,
		Latency:    latency,
		StatusCode: &statusCode,
		Message:    message,
	}
}
