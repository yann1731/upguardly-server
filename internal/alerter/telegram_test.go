package alerter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

func testMonitorAndResult() (*models.Monitor, *models.CheckResult) {
	monitor := &models.Monitor{Name: "test <svc>", Type: models.MonitorTypeHTTP, Target: "https://example.com"}
	result := &models.CheckResult{Status: models.StatusDOWN, Latency: 42, Message: "connection refused"}
	return monitor, result
}

func TestTelegramSend(t *testing.T) {
	var gotPath string
	var gotPayload telegramSendMessagePayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewTelegramAlerter(config.TelegramConfig{BotToken: "test-token"})
	a.baseURL = srv.URL

	monitor, result := testMonitorAndResult()
	if err := a.Send(context.Background(), "123456789", monitor, result); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("path = %q, want /bottest-token/sendMessage", gotPath)
	}
	if gotPayload.ChatID != "123456789" {
		t.Errorf("chat_id = %q, want 123456789", gotPayload.ChatID)
	}
	if gotPayload.ParseMode != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", gotPayload.ParseMode)
	}
	if !strings.Contains(gotPayload.Text, "DOWN") || !strings.Contains(gotPayload.Text, "connection refused") {
		t.Errorf("text missing status/message: %q", gotPayload.Text)
	}
	// A DOWN check has no round trip to report — the recorded latency is the
	// checker's timeout (see formatLatency).
	if strings.Contains(gotPayload.Text, "42ms") {
		t.Errorf("text reports a latency for a failed check: %q", gotPayload.Text)
	}
	// User-controlled fields must be escaped so they can't inject HTML markup.
	if strings.Contains(gotPayload.Text, "<svc>") {
		t.Errorf("text contains unescaped monitor name: %q", gotPayload.Text)
	}
	if !strings.Contains(gotPayload.Text, "&lt;svc&gt;") {
		t.Errorf("text missing escaped monitor name: %q", gotPayload.Text)
	}
}

func TestTelegramSendMissingToken(t *testing.T) {
	a := NewTelegramAlerter(config.TelegramConfig{})
	monitor, result := testMonitorAndResult()
	if err := a.Send(context.Background(), "123456789", monitor, result); err == nil {
		t.Fatal("Send with no bot token: want error, got nil")
	}
}

func TestTelegramSendMissingTarget(t *testing.T) {
	a := NewTelegramAlerter(config.TelegramConfig{BotToken: "test-token"})
	monitor, result := testMonitorAndResult()
	if err := a.Send(context.Background(), "", monitor, result); err == nil {
		t.Fatal("Send with no target: want error, got nil")
	}
}

func TestTelegramSendAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	a := NewTelegramAlerter(config.TelegramConfig{BotToken: "test-token"})
	a.baseURL = srv.URL

	monitor, result := testMonitorAndResult()
	if err := a.Send(context.Background(), "123456789", monitor, result); err == nil {
		t.Fatal("Send with 400 response: want error, got nil")
	}
}

// TestTelegramSendAPIErrorDescription checks that the Bot API's description
// survives into the returned error. Every user-side setup mistake arrives as a
// bare 403/400, so the description is the only thing that tells them apart in
// the logs and in alert_history.
func TestTelegramSendAPIErrorDescription(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{
			name:   "never started the bot",
			status: http.StatusForbidden,
			body:   `{"ok":false,"error_code":403,"description":"Forbidden: bot can't initiate conversation with a user"}`,
			want:   []string{"403", "bot can't initiate conversation with a user"},
		},
		{
			name:   "blocked by user",
			status: http.StatusForbidden,
			body:   `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`,
			want:   []string{"403", "bot was blocked by the user"},
		},
		{
			name:   "wrong chat id",
			status: http.StatusBadRequest,
			body:   `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`,
			want:   []string{"400", "chat not found"},
		},
		{
			name:   "rate limited surfaces retry_after",
			status: http.StatusTooManyRequests,
			body:   `{"ok":false,"error_code":429,"description":"Too Many Requests: retry later","parameters":{"retry_after":17}}`,
			want:   []string{"429", "retry later", "retry after 17s"},
		},
		{
			name:   "non-envelope body is truncated but reported",
			status: http.StatusBadGateway,
			body:   "<html>upstream proxy failure</html>",
			want:   []string{"502", "upstream proxy failure"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			a := NewTelegramAlerter(config.TelegramConfig{BotToken: "test-token"})
			a.baseURL = srv.URL

			monitor, result := testMonitorAndResult()
			err := a.Send(context.Background(), "123456789", monitor, result)
			if err == nil {
				t.Fatalf("Send with %d response: want error, got nil", tt.status)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

// TestTelegramSendErrorBodyBounded keeps the non-envelope fallback from
// pushing an unbounded upstream body into alert_history.
func TestTelegramSendErrorBodyBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", 64<<10)))
	}))
	defer srv.Close()

	a := NewTelegramAlerter(config.TelegramConfig{BotToken: "test-token"})
	a.baseURL = srv.URL

	monitor, result := testMonitorAndResult()
	err := a.Send(context.Background(), "123456789", monitor, result)
	if err == nil {
		t.Fatal("Send with oversized body: want error, got nil")
	}
	if len(err.Error()) > 512 {
		t.Errorf("error is %d bytes, want it bounded: %.128q…", len(err.Error()), err)
	}
}

// TestTelegramSendTransportErrorHidesToken is the regression guard for the
// credential leak: http.Client returns *url.Error, whose Error() embeds the
// request URL, and the Bot API puts the token in the path. That string is
// logged every attempt and written to alert_history on the last one.
func TestTelegramSendTransportErrorHidesToken(t *testing.T) {
	const token = "123456:SUPER-SECRET-BOT-TOKEN"

	// A closed server guarantees a transport-level failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	a := NewTelegramAlerter(config.TelegramConfig{BotToken: token})
	a.baseURL = closedURL

	monitor, result := testMonitorAndResult()
	err := a.Send(context.Background(), "123456789", monitor, result)
	if err == nil {
		t.Fatal("Send to closed server: want error, got nil")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks the bot token: %q", err)
	}
	if strings.Contains(err.Error(), "/bot") {
		t.Errorf("error leaks the token-bearing URL path: %q", err)
	}
}

// TestManagerSendUnknownChannel verifies the Manager fails loudly for a
// channel with no registered alerter — a silent nil would make the dispatcher
// record a successful delivery that never happened.
func TestManagerSendUnknownChannel(t *testing.T) {
	m := &Manager{alerters: map[models.AlertChannel]Alerter{}}
	monitor, result := testMonitorAndResult()
	if err := m.Send(context.Background(), models.AlertChannel("BOGUS"), "target", monitor, result); err == nil {
		t.Fatal("Send on unregistered channel: want error, got nil")
	}
}
