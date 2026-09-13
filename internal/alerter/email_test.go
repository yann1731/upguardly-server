package alerter

import (
	"context"
	"strings"
	"testing"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

// TestEmailAlerterDisabled verifies the EMAIL_ENABLED=false kill switch: the
// send must succeed as a no-op without touching SendGrid — even with an API
// key configured — so development and load tests cannot burn email quota.
func TestEmailAlerterDisabled(t *testing.T) {
	a := NewEmailAlerter(config.SendGridConfig{
		Enabled: false,
		APIKey:  "SG.fake-key-that-must-never-be-used",
		From:    "alerts@example.com",
	})

	err := a.Send(context.Background(), "user@example.com",
		&models.Monitor{ID: "m1", Name: "api", Type: models.MonitorTypeHTTP, Target: "https://x"},
		&models.CheckResult{Status: models.StatusDOWN, Message: "Server error"},
	)
	if err != nil {
		t.Fatalf("disabled email send must be a silent no-op, got error: %v", err)
	}
}

// TestEmailAlerterDisablesClickTracking guards against SendGrid rewriting the
// monitor target into a urlNNNN.upguardly.com click-tracking redirect.
func TestEmailAlerterDisablesClickTracking(t *testing.T) {
	a := NewEmailAlerter(config.SendGridConfig{Enabled: true, APIKey: "SG.fake", From: "alerts@example.com"})
	target := "https://status.example.com/health"

	msg := a.buildMessage("user@example.com",
		&models.Monitor{ID: "m1", Name: "api", Type: models.MonitorTypeHTTP, Target: target},
		&models.CheckResult{Status: models.StatusDOWN, Message: "Server error"},
	)

	if msg.TrackingSettings == nil || msg.TrackingSettings.ClickTracking == nil {
		t.Fatal("click tracking settings must be set on alert emails")
	}
	ct := msg.TrackingSettings.ClickTracking
	if ct.Enable == nil || *ct.Enable {
		t.Error("click tracking must be disabled")
	}
	if ct.EnableText == nil || *ct.EnableText {
		t.Error("plain-text click tracking must be disabled")
	}

	if len(msg.Content) == 0 || !strings.Contains(msg.Content[0].Value, "Target: "+target) {
		t.Errorf("email body must contain the raw target %q", target)
	}
}
