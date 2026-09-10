package alerter

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

type Alerter interface {
	// Send delivers an alert to target. target is passed per call (recipient
	// email, phone number, or webhook URL) rather than stored on the alerter,
	// so a single shared alerter instance is safe for concurrent use.
	Send(ctx context.Context, target string, monitor *models.Monitor, result *models.CheckResult) error
}

type Manager struct {
	alerters map[models.AlertChannel]Alerter
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		alerters: map[models.AlertChannel]Alerter{
			models.AlertChannelEMAIL:    NewEmailAlerter(cfg.SendGrid),
			models.AlertChannelSMS:      NewSMSAlerter(cfg.Twilio),
			models.AlertChannelDISCORD:  NewDiscordAlerter(),
			models.AlertChannelSLACK:    NewSlackAlerter(),
			models.AlertChannelTELEGRAM: NewTelegramAlerter(cfg.Telegram),
		},
	}
}

func (m *Manager) GetAlerter(channel models.AlertChannel) Alerter {
	return m.alerters[channel]
}

func (m *Manager) Send(ctx context.Context, channel models.AlertChannel, target string, monitor *models.Monitor, result *models.CheckResult) error {
	alerter := m.alerters[channel]
	if alerter == nil {
		// Fail loudly: returning nil here would make the dispatcher record a
		// successful delivery for a channel nothing actually sent.
		return fmt.Errorf("no alerter registered for channel %q", channel)
	}

	return alerter.Send(ctx, target, monitor, result)
}

// sanitizeTransportError strips the request URL out of an http.Client failure.
//
// Client errors are *url.Error, and its Error() formats as
// `Post "<url>": <cause>` — so the full URL lands in the message. Every
// alerter's URL is a credential: the Telegram Bot API puts the bot token in
// the path, and a Discord or Slack webhook URL *is* the capability to post to
// that channel (anyone holding it can). These strings are logged on every
// delivery attempt and persisted to alert_history on the final one (see
// alertDispatcher in internal/scheduler), so the URL must never reach them.
//
// The underlying cause — the DNS/dial/TLS/timeout error operators actually
// need — is preserved and stays wrapped for errors.Is/As.
func sanitizeTransportError(op string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s: %w", op, urlErr.Err)
	}
	return fmt.Errorf("%s: %w", op, err)
}
