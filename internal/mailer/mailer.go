package mailer

import (
	"fmt"
	"net/smtp"

	"upguardly-backend/internal/config"
)

type Mailer struct {
	cfg config.SMTPConfig
}

func NewMailer(cfg config.SMTPConfig) *Mailer {
	return &Mailer{cfg: cfg}
}

func (m *Mailer) SendInvitation(to, orgName, inviterName, acceptURL string) error {
	if m.cfg.Host == "" {
		return fmt.Errorf("SMTP not configured")
	}

	subject := fmt.Sprintf("You've been invited to join %s on Upguardly", orgName)
	body := fmt.Sprintf(`Hi,

%s has invited you to join the %s organisation on Upguardly.

Click the link below to accept the invitation (expires in 7 days):

%s

If you did not expect this invitation, you can safely ignore this email.

---
Upguardly
`, inviterName, orgName, acceptURL)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		m.cfg.From, to, subject, body)

	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)

	var auth smtp.Auth
	if m.cfg.User != "" {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Password, m.cfg.Host)
	}

	if err := smtp.SendMail(addr, auth, m.cfg.From, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("failed to send invitation email: %w", err)
	}
	return nil
}
