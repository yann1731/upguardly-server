package models

// AlertChannel is a delivery mechanism for alerts (email, SMS, webhooks…).
// Alert destinations are configured as account-level NotificationChannels;
// see notification_channel.go.
type AlertChannel string

const (
	AlertChannelEMAIL    AlertChannel = "EMAIL"
	AlertChannelSMS      AlertChannel = "SMS"
	AlertChannelDISCORD  AlertChannel = "DISCORD"
	AlertChannelSLACK    AlertChannel = "SLACK"
	AlertChannelTELEGRAM AlertChannel = "TELEGRAM"
)

type AlertOutboxRow struct {
	ID                    string
	AlertID               *string
	NotificationChannelID *string
	MonitorID             string
	Channel               AlertChannel
	Target                string
	Status                Status
	Message               string
	StatusCode            *int
	Latency               int
	MonitorName           string
	MonitorType           MonitorType
	MonitorTarget         string
	Attempts              int
}
