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

// formatLatency renders a check's latency for an alert body.
//
// A DOWN check never completed a round trip: its recorded latency is how long
// the checker waited before giving up, so a ping monitor whose host reboots
// reports the full 30s timeout. Printing "30000ms" next to a DOWN status reads
// as a service that is merely very slow. Alerts therefore show no figure at
// all for a failure — the same rule the stats path applies when aggregating
// (measuredLatency in internal/database/bun/store.go).
//
// The placeholder is plain ASCII: SMS bodies are GSM-7 encoded, and a single
// non-GSM character (an em dash, say) switches the whole message to UCS-2 and
// halves the per-segment length.
func formatLatency(result *models.CheckResult) string {
	if result.Status == models.StatusDOWN {
		return "n/a"
	}
	return fmt.Sprintf("%dms", result.Latency)
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
