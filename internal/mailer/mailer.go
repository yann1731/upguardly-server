package mailer

import (
	"fmt"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"

	"upguardly-backend/internal/config"
)

type Mailer struct {
	cfg config.SendGridConfig
}

func NewMailer(cfg config.SendGridConfig) *Mailer {
	return &Mailer{cfg: cfg}
}

func (m *Mailer) SendInvitation(to, orgName, inviterName, acceptURL string) error {
	if m.cfg.APIKey == "" {
		return fmt.Errorf("SendGrid not configured")
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

	from := mail.NewEmail(m.cfg.FromName, m.cfg.From)
	recipient := mail.NewEmail("", to)
	message := mail.NewSingleEmail(from, subject, recipient, body, "")

	client := sendgrid.NewSendClient(m.cfg.APIKey)
	resp, err := client.Send(message)
	if err != nil {
		return fmt.Errorf("failed to send invitation email: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SendGrid returned status %d: %s", resp.StatusCode, resp.Body)
	}
	return nil
}
