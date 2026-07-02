package alerter

import (
	"context"
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
