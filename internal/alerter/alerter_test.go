package alerter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

// TestManagerSendConcurrentTargets verifies that a single shared Manager can
// deliver alerts to different targets concurrently without crossing wires.
// Before the fix, the Manager mutated a shared alerter's destination field per
// call, so concurrent sends could race and deliver to the wrong target.
//
// Run with: go test -race ./internal/alerter/...
func TestManagerSendConcurrentTargets(t *testing.T) {
	// Two webhook servers standing in for two distinct Slack destinations.
	var hitsA, hitsB int64
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitsA, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitsB, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	m := &Manager{
		alerters: map[models.AlertChannel]Alerter{
			models.AlertChannelSLACK: NewSlackAlerter(),
		},
	}

	monitor := &models.Monitor{Name: "test", Type: models.MonitorTypeHTTP, Target: "https://example.com"}
	result := &models.CheckResult{Status: models.StatusDOWN, Latency: 1, Message: "down"}

	const perTarget = 100
	var wg sync.WaitGroup
	for i := 0; i < perTarget; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Send(context.Background(), models.AlertChannelSLACK, srvA.URL, monitor, result)
		}()
		go func() {
			defer wg.Done()
			_ = m.Send(context.Background(), models.AlertChannelSLACK, srvB.URL, monitor, result)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&hitsA); got != perTarget {
		t.Errorf("target A: got %d deliveries, want %d", got, perTarget)
	}
	if got := atomic.LoadInt64(&hitsB); got != perTarget {
		t.Errorf("target B: got %d deliveries, want %d", got, perTarget)
	}
}

// TestWebhookTransportErrorHidesTarget is the regression guard for the webhook
// credential leak. For Discord and Slack the alert target IS the webhook URL —
// possession of it is the capability to post to that channel — and
// http.Client failures are *url.Error, whose Error() embeds the request URL.
// That string is logged on every delivery attempt and written to
// alert_history on the last one.
func TestWebhookTransportErrorHidesTarget(t *testing.T) {
	// A closed server guarantees a transport-level failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close()

	const secret = "SECRET-WEBHOOK-TOKEN"
	webhook := base + "/webhooks/123456/" + secret

	for _, tc := range []struct {
		name    string
		alerter Alerter
	}{
		{"discord", NewDiscordAlerter()},
		{"slack", NewSlackAlerter()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			monitor, result := testMonitorAndResult()
			err := tc.alerter.Send(context.Background(), webhook, monitor, result)
			if err == nil {
				t.Fatal("Send to closed server: want error, got nil")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaks the webhook credential: %q", err)
			}
			if strings.Contains(err.Error(), base) {
				t.Errorf("error leaks the webhook URL: %q", err)
			}
		})
	}
}

// TestSMSTransportErrorHidesURL keeps the SMS alerter on the same sanitized
// path as the others. Twilio authenticates with a Basic-auth header, so its
// URL carries only the Account SID rather than a credential — but the error
// still reaches the logs and alert_history, and holding every alerter to one
// rule is what stops the next one from reintroducing the leak.
func TestSMSTransportErrorHidesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close()

	const accountSID = "AC-ACCOUNT-SID-VALUE"
	a := NewSMSAlerter(config.TwilioConfig{
		AccountSID:   accountSID,
		APIKeySID:    "SK-key-sid",
		APIKeySecret: "key-secret",
		FromNumber:   "+12125550000",
	})
	a.baseURL = base

	monitor, result := testMonitorAndResult()
	err := a.Send(context.Background(), "+12125551234", monitor, result)
	if err == nil {
		t.Fatal("Send to closed server: want error, got nil")
	}
	if strings.Contains(err.Error(), accountSID) {
		t.Errorf("error leaks the Account SID: %q", err)
	}
	if strings.Contains(err.Error(), base) {
		t.Errorf("error leaks the request URL: %q", err)
	}
	// The API key secret is header-borne and must never appear either.
	if strings.Contains(err.Error(), "key-secret") {
		t.Errorf("error leaks the API key secret: %q", err)
	}
}

// TestSanitizeTransportErrorPreservesCause checks the sanitizer keeps the
// underlying error wrapped — operators still need the dial/DNS/TLS cause, and
// callers still need errors.Is/As to work through it.
func TestSanitizeTransportErrorPreservesCause(t *testing.T) {
	cause := errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
	wrapped := &url.Error{Op: "Post", URL: "https://hooks.example.com/SECRET", Err: cause}

	got := sanitizeTransportError("failed to send slack webhook", wrapped)
	if !errors.Is(got, cause) {
		t.Errorf("errors.Is(got, cause) = false, want true (got %q)", got)
	}
	if !strings.Contains(got.Error(), "connection refused") {
		t.Errorf("error dropped the cause: %q", got)
	}
	if strings.Contains(got.Error(), "SECRET") {
		t.Errorf("error leaks the URL: %q", got)
	}
}

// TestSanitizeTransportErrorNonURLError checks a non-*url.Error passes through
// wrapped rather than being swallowed.
func TestSanitizeTransportErrorNonURLError(t *testing.T) {
	cause := errors.New("context deadline exceeded")
	got := sanitizeTransportError("failed to send", fmt.Errorf("outer: %w", cause))
	if !errors.Is(got, cause) {
		t.Errorf("errors.Is(got, cause) = false, want true (got %q)", got)
	}
}

// A failed check's latency is the checker's timeout, not a response time — a
// ping monitor whose host reboots records 30000ms. Every channel must keep that
// figure out of the alert body; an UP or DEGRADED check still reports its real
// round trip.
func TestFormatLatency(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result models.CheckResult
		want   string
	}{
		{"up", models.CheckResult{Status: models.StatusUP, Latency: 42}, "42ms"},
		{"degraded", models.CheckResult{Status: models.StatusDEGRADED, Latency: 1500}, "1500ms"},
		{"down after a timeout", models.CheckResult{Status: models.StatusDOWN, Latency: 30000}, "n/a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLatency(&tc.result); got != tc.want {
				t.Errorf("formatLatency = %q, want %q", got, tc.want)
			}
		})
	}
}

// The end-to-end guard: whatever each channel's body looks like, a DOWN alert
// must not print the timeout as a millisecond figure. Only the two webhook
// channels can be driven against a local server here; the SMS and Telegram
// bodies are covered by their own tests, and email builds no request.
func TestDownAlertBodyOmitsTimeoutLatency(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	monitor := &models.Monitor{Name: "test", Type: models.MonitorTypePING, Target: "example.com"}
	result := &models.CheckResult{Status: models.StatusDOWN, Latency: 30000, Message: "Ping failed"}

	for _, tc := range []struct {
		name    string
		alerter Alerter
	}{
		{"discord", NewDiscordAlerter()},
		{"slack", NewSlackAlerter()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body = ""
			if err := tc.alerter.Send(context.Background(), srv.URL, monitor, result); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if strings.Contains(body, "30000ms") {
				t.Errorf("body reports the timeout as a latency: %s", body)
			}
			if !strings.Contains(body, "n/a") {
				t.Errorf("body missing the no-latency placeholder: %s", body)
			}
		})
	}
}
