package alerter

import (
	"context"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

type Alerter interface {
	Send(ctx context.Context, monitor *models.Monitor, result *models.CheckResult) error
}

type Manager struct {
	alerters map[models.AlertChannel]Alerter
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		alerters: map[models.AlertChannel]Alerter{
			models.AlertChannelEMAIL:   NewEmailAlerter(cfg.SendGrid),
			models.AlertChannelSMS:     NewSMSAlerter(cfg.Twilio),
			models.AlertChannelDISCORD: NewDiscordAlerter(),
			models.AlertChannelSLACK:   NewSlackAlerter(),
		},
	}
}

func (m *Manager) GetAlerter(channel models.AlertChannel) Alerter {
	return m.alerters[channel]
}

func (m *Manager) Send(ctx context.Context, channel models.AlertChannel, target string, monitor *models.Monitor, result *models.CheckResult) error {
	alerter := m.alerters[channel]
	if alerter == nil {
		return nil
	}

	switch a := alerter.(type) {
	case *EmailAlerter:
		a.To = target
	case *SMSAlerter:
		a.To = target
	case *DiscordAlerter:
		a.WebhookURL = target
	case *SlackAlerter:
		a.WebhookURL = target
	}

	return alerter.Send(ctx, monitor, result)
}
