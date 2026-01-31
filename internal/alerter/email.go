package alerter

import (
	"context"
	"fmt"
	"net/smtp"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

type EmailAlerter struct {
	config config.SMTPConfig
	To     string
}

func NewEmailAlerter(cfg config.SMTPConfig) *EmailAlerter {
	return &EmailAlerter{
		config: cfg,
	}
}

func (a *EmailAlerter) Send(ctx context.Context, monitor *models.Monitor, result *models.CheckResult) error {
	if a.config.Host == "" {
		return fmt.Errorf("SMTP not configured")
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

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		a.config.From, a.To, subject, body)

	addr := fmt.Sprintf("%s:%d", a.config.Host, a.config.Port)

	var auth smtp.Auth
	if a.config.User != "" {
		auth = smtp.PlainAuth("", a.config.User, a.config.Password, a.config.Host)
	}

	err := smtp.SendMail(addr, auth, a.config.From, []string{a.To}, []byte(msg))
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}
