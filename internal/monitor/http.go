package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"upguardly-backend/internal/models"
)

type HTTPChecker struct{}

// classifyHTTPError maps a transport-level request error to a concise,
// human-readable issue label (e.g. "timeout", "connection refused") so the
// stored message describes the problem rather than dumping a raw Go error.
func classifyHTTPError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), os.IsTimeout(err):
		return "Timeout"
	case errors.Is(err, context.Canceled):
		return "Check canceled"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS lookup failed"
	}

	var tlsErr *tls.CertificateVerificationError
	if errors.As(err, &tlsErr) {
		return "TLS certificate error"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "Timeout"
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "Connection refused"
	case strings.Contains(msg, "no such host"):
		return "DNS lookup failed"
	case strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:"):
		return "TLS error"
	default:
		return "Connection error"
	}
}

func (c *HTTPChecker) Check(ctx context.Context, target string, timeout time.Duration) models.CheckResult {
	safeDialer := SafeDialer()
	safeDialer.Timeout = timeout

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = safeDialer.DialContext

	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return models.CheckResult{
			Status:  models.StatusDOWN,
			Latency: int(time.Since(start).Milliseconds()),
			Message: "Invalid request URL",
		}
	}

	req.Header.Set("User-Agent", "Upguardly-Monitor/1.0")

	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		return models.CheckResult{
			Status:  models.StatusDOWN,
			Latency: latency,
			Message: classifyHTTPError(err),
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
