package models

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a write violates a uniqueness rule, e.g. a
// duplicate organization name or a user joining a second organization.
var ErrConflict = errors.New("conflict")

type Store interface {
	// Monitors
	CreateMonitor(ctx context.Context, userId, orgId, name, monitorType, target string, interval, timeout int, enabled bool) (*Monitor, error)
	CountMonitorsByOrg(ctx context.Context, orgId string) (int, error)
	CountMonitorsByUser(ctx context.Context, userId string) (int, error)
	ListMonitors(ctx context.Context, userId string) ([]Monitor, error)
	GetMonitor(ctx context.Context, id, userId string) (*Monitor, error)
	UpdateMonitor(ctx context.Context, id, userId string, req UpdateMonitorRequest) (*Monitor, error)
	DeleteMonitor(ctx context.Context, id, userId string) error
	GetMonitorResults(ctx context.Context, monitorId, userId string, limit int) ([]MonitorResult, error)
	ListIncidents(ctx context.Context, monitorId, userId string, limit int) ([]Incident, error)
	GetMonitorStats(ctx context.Context, monitorId, userId string, since time.Time) (*MonitorStats, error)

	// Alerts
	CreateAlert(ctx context.Context, monitorId, userId, channel, target string, enabled bool) (*Alert, error)
	ListAlerts(ctx context.Context, monitorId, userId string) ([]Alert, error)
	GetAlert(ctx context.Context, id string) (*Alert, error)
	UpdateAlert(ctx context.Context, id string, req UpdateAlertRequest) (*Alert, error)
	DeleteAlert(ctx context.Context, id string) error

	// Notification channels (global, per-user) and per-monitor overrides
	CreateNotificationChannel(ctx context.Context, userId, channel, target string, enabled bool) (*NotificationChannel, error)
	ListNotificationChannels(ctx context.Context, userId string) ([]NotificationChannel, error)
	CountNotificationChannels(ctx context.Context, userId string) (int, error)
	GetNotificationChannel(ctx context.Context, id, userId string) (*NotificationChannel, error)
	UpdateNotificationChannel(ctx context.Context, id, userId string, req UpdateNotificationChannelRequest) (*NotificationChannel, error)
	DeleteNotificationChannel(ctx context.Context, id, userId string) error
	ListMonitorChannelSettings(ctx context.Context, monitorId string) ([]MonitorChannelSetting, error)
	UpsertMonitorChannelSetting(ctx context.Context, monitorId, channelId string, enabled bool) (*MonitorChannelSetting, error)
	DeleteMonitorChannelSetting(ctx context.Context, monitorId, channelId string) error

	// Organizations
	CreateOrganization(ctx context.Context, userId, name string) (*Organization, error)
	GetOrganization(ctx context.Context, id string) (*Organization, error)
	ListOrganizations(ctx context.Context, userId string) ([]Organization, error)
	UpdateOrganization(ctx context.Context, id string, req UpdateOrgRequest) (*Organization, error)
	DeleteOrganization(ctx context.Context, id string) error

	// Members
	GetMembership(ctx context.Context, orgId, userId string) (*OrganizationMember, error)
	ListMembers(ctx context.Context, orgId string) ([]OrganizationMember, error)
	UpdateMemberRole(ctx context.Context, orgId, userId string, role OrgRole) (*OrganizationMember, error)
	RemoveMember(ctx context.Context, orgId, userId string) error

	// Invitations
	CreateInvitation(ctx context.Context, orgId, email, invitedBy string, role OrgRole, token string, expiresAt time.Time) (*Invitation, error)
	GetInvitationByToken(ctx context.Context, token string) (*Invitation, error)
	GetInvitationByID(ctx context.Context, id string) (*Invitation, error)
	ListInvitations(ctx context.Context, orgId string) ([]Invitation, error)
	AcceptInvitation(ctx context.Context, token, userId string) (*OrganizationMember, error)
	RevokeInvitation(ctx context.Context, id string) error

	// Subscriptions (keyed on the user — the billing subject)
	GetSubscriptionByUser(ctx context.Context, userId string) (*Subscription, error)
	UpsertSubscription(ctx context.Context, params UpsertSubscriptionParams) (*Subscription, error)
}
