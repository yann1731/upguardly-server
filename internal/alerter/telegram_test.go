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
