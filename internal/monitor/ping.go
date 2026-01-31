package monitor

import (
	"context"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"upguardly-backend/internal/models"
)

type PingChecker struct{}

func (c *PingChecker) Check(ctx context.Context, target string, timeout time.Duration) models.CheckResult {
	start := time.Now()

	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}

	var cmd *exec.Cmd
	timeoutSec := int(timeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}

	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", strconv.Itoa(timeoutSec*1000), host)
	case "darwin":
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", strconv.Itoa(timeoutSec*1000), host)
	default:
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", strconv.Itoa(timeoutSec), host)
	}

	output, err := cmd.CombinedOutput()
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		return models.CheckResult{
			Status:  models.StatusDOWN,
			Latency: latency,
			Message: "Ping failed: " + err.Error(),
		}
	}

	parsedLatency := parseLatency(string(output))
	if parsedLatency > 0 {
		latency = parsedLatency
	}

	status := models.StatusUP
	message := "Host is reachable"

	if latency > 500 {
		status = models.StatusDEGRADED
		message = "Host is reachable but high latency"
	}

	return models.CheckResult{
		Status:  status,
		Latency: latency,
		Message: message,
	}
}

func parseLatency(output string) int {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "time=") {
			parts := strings.Split(line, "time=")
			if len(parts) > 1 {
				timePart := strings.Fields(parts[1])[0]
				timePart = strings.TrimSuffix(timePart, "ms")
				if ms, err := strconv.ParseFloat(timePart, 64); err == nil {
					return int(ms)
				}
			}
		}
	}
	return 0
}
