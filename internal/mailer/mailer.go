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

// disableClickTracking turns off SendGrid's link rewriting for a message. These
// are transactional emails whose links carry single-use tokens; routing them
// through SendGrid's click-tracking redirector mangles the tokens and breaks
// localhost links in dev, so we always send the links verbatim.
func disableClickTracking(message *mail.SGMailV3) {
	ct := mail.NewClickTrackingSetting()
	ct.SetEnable(false)
	ct.SetEnableText(false)

	ts := mail.NewTrackingSettings()
	ts.SetClickTracking(ct)

	message.SetTrackingSettings(ts)
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
	disableClickTracking(message)

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

func (m *Mailer) SendPasswordResetEmail(to, resetURL string) error {
	if m.cfg.APIKey == "" {
		return fmt.Errorf("SendGrid not configured")
	}

	subject := "Reset your Upguardly password"
	body := fmt.Sprintf(`Hi,

We received a request to reset the password for your Upguardly account.
Click the link below to choose a new password (this link expires shortly):

%s

If you did not request a password reset, you can safely ignore this email —
your password will not change.

---
Upguardly
`, resetURL)

	from := mail.NewEmail(m.cfg.FromName, m.cfg.From)
	recipient := mail.NewEmail("", to)
	message := mail.NewSingleEmail(from, subject, recipient, body, "")
	disableClickTracking(message)

	client := sendgrid.NewSendClient(m.cfg.APIKey)
	resp, err := client.Send(message)
	if err != nil {
		return fmt.Errorf("failed to send password reset email: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SendGrid returned status %d: %s", resp.StatusCode, resp.Body)
	}
	return nil
}

func (m *Mailer) SendVerificationEmail(to, verifyURL string) error {
	if m.cfg.APIKey == "" {
		return fmt.Errorf("SendGrid not configured")
	}

	subject := "Verify your Upguardly email address"
	body := fmt.Sprintf(`Hi,

Please confirm your email address to finish setting up your Upguardly account:

%s

If you did not create this account, you can safely ignore this email.

---
Upguardly
`, verifyURL)

	from := mail.NewEmail(m.cfg.FromName, m.cfg.From)
	recipient := mail.NewEmail("", to)
	message := mail.NewSingleEmail(from, subject, recipient, body, "")
	disableClickTracking(message)

	client := sendgrid.NewSendClient(m.cfg.APIKey)
	resp, err := client.Send(message)
	if err != nil {
		return fmt.Errorf("failed to send verification email: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SendGrid returned status %d: %s", resp.StatusCode, resp.Body)
	}
	return nil
}
