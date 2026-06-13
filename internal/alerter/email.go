package alerter

import (
	"context"
	"fmt"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

type EmailAlerter struct {
	config config.SendGridConfig
	To     string
}

func NewEmailAlerter(cfg config.SendGridConfig) *EmailAlerter {
	return &EmailAlerter{
		config: cfg,
	}
}

func (a *EmailAlerter) Send(ctx context.Context, monitor *models.Monitor, result *models.CheckResult) error {
	if a.config.APIKey == "" {
		return fmt.Errorf("SendGrid not configured")
	}

	if a.To == "" {
		return fmt.Errorf("recipient email not set")
	}

	subject := fmt.Sprintf("Upguardly Alert: %s is %s", monitor.Name, result.Status)
	body := fmt.Sprintf(`Monitor Alert

Monitor: %s
Status: %s
Type: %s
Target: %s
Latency: %dms
Message: %s

---
Sent by Upguardly Monitoring
`, monitor.Name, result.Status, monitor.Type, monitor.Target, result.Latency, result.Message)

	from := mail.NewEmail(a.config.FromName, a.config.From)
	to := mail.NewEmail("", a.To)
	message := mail.NewSingleEmail(from, subject, to, body, "")

	client := sendgrid.NewSendClient(a.config.APIKey)
	resp, err := client.SendWithContext(ctx, message)
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SendGrid returned status %d: %s", resp.StatusCode, resp.Body)
	}

	return nil
}
