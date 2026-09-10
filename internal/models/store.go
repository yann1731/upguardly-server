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

// ErrSeatLimit is returned when accepting an invitation would push an
// organization past its plan's login-seat cap (checked transactionally in
// AcceptInvitation; CreateInvitation pre-checks the same rule in the handler).
var ErrSeatLimit = errors.New("seat limit reached")

type Store interface {
	// Monitors. interval is nil for a follow-plan monitor (resolved to the
	// plan minimum at read time), or an explicit override in seconds.
	// degradedThresholdMs is nil for the per-type default slow-response
	// threshold, or a plan-gated explicit override in milliseconds.
	// repeatIntervalSecs/repeatMaxCount enable repeat alerts (ENTERPRISE);
	// both nil = off, always set together.
	CreateMonitor(ctx context.Context, userId, orgId, name, monitorType, target string, interval *int, timeout int, degradedThresholdMs, repeatIntervalSecs, repeatMaxCount *int, enabled bool, regions []string) (*Monitor, error)
	CountMonitorsByOrg(ctx context.Context, orgId string) (int, error)
	CountMonitorsByUser(ctx context.Context, userId string) (int, error)
	ListMonitors(ctx context.Context, userId string) ([]Monitor, error)
	GetMonitor(ctx context.Context, id, userId string) (*Monitor, error)
	UpdateMonitor(ctx context.Context, id, userId string, req UpdateMonitorRequest) (*Monitor, error)
	DeleteMonitor(ctx context.Context, id, userId string) error
	// GetMonitorResults returns recent results, optionally filtered to one
	// region ("" = all regions).
	GetMonitorResults(ctx context.Context, monitorId, userId string, limit int, region string) ([]MonitorResult, error)
	// ListMonitorRegionStatus returns the latest per-region check outcome for
	// the monitor's currently configured regions.
	ListMonitorRegionStatus(ctx context.Context, monitorId, userId string) ([]MonitorRegionStatus, error)
	ListIncidents(ctx context.Context, monitorId, userId string, limit int) ([]Incident, error)
	GetMonitorStats(ctx context.Context, monitorId, userId string, since time.Time) (*MonitorStats, error)
	// GetMonitorUptime returns per-day availability for the last `days`
	// calendar days, bucketed against tzOffsetMinutes (minutes east of UTC) so
	// the days line up with the caller's calendar.
	GetMonitorUptime(ctx context.Context, monitorId, userId string, days, tzOffsetMinutes int) (*MonitorUptime, error)

	// Maintenance windows (per-monitor alert suppression; ENTERPRISE).
	// Ownership is resolved by the caller via GetMonitor.
	ListMaintenanceWindows(ctx context.Context, monitorId string) ([]MaintenanceWindow, error)
	CreateMaintenanceWindow(ctx context.Context, monitorId string, req CreateMaintenanceWindowRequest) (*MaintenanceWindow, error)
	DeleteMaintenanceWindow(ctx context.Context, monitorId, windowId string) error

	// GetMonitorExpiryStatus returns the monitor's CERT/DOMAIN sub-check
	// state (whichever rows exist — a sub-check not yet run has none).
	GetMonitorExpiryStatus(ctx context.Context, monitorId string) ([]MonitorExpiryStatus, error)

	// Notification channels (global, per-user) and per-monitor overrides
	CreateNotificationChannel(ctx context.Context, userId, channel, target string, enabled bool) (*NotificationChannel, error)
	ListNotificationChannels(ctx context.Context, userId string) ([]NotificationChannel, error)
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
	// CountNonOwnerMembers counts members holding a login seat (everyone but
	// the OWNER, who is free).
	CountNonOwnerMembers(ctx context.Context, orgId string) (int, error)
	UpdateMemberRole(ctx context.Context, orgId, userId string, role OrgRole) (*OrganizationMember, error)
	RemoveMember(ctx context.Context, orgId, userId string) error

	// Org alert recipients (notify-only alerting seats)
	CreateOrgAlertRecipient(ctx context.Context, orgId, channel, target string) (*OrgAlertRecipient, error)
	ListOrgAlertRecipients(ctx context.Context, orgId string) ([]OrgAlertRecipient, error)
	CountOrgAlertRecipients(ctx context.Context, orgId string) (int, error)
	DeleteOrgAlertRecipient(ctx context.Context, orgId, id string) error

	// Invitations
	CreateInvitation(ctx context.Context, orgId, email, invitedBy string, role OrgRole, token string, expiresAt time.Time) (*Invitation, error)
	GetInvitationByToken(ctx context.Context, token string) (*Invitation, error)
	GetInvitationByID(ctx context.Context, id string) (*Invitation, error)
	ListInvitations(ctx context.Context, orgId string) ([]Invitation, error)
	// CountPendingInvitations counts PENDING, non-expired invitations — each
	// one holds a login seat until accepted, revoked, or expired.
	CountPendingInvitations(ctx context.Context, orgId string) (int, error)
	// AcceptInvitation converts a pending invitation into a membership. When
	// maxLoginSeats != Unlimited it re-checks the seat cap inside the
	// transaction (excluding the invitation being accepted, which already
	// holds the seat it is converting) and returns ErrSeatLimit if exceeded.
	AcceptInvitation(ctx context.Context, token, userId string, maxLoginSeats int) (*OrganizationMember, error)
	RevokeInvitation(ctx context.Context, id string) error

	// Subscriptions (keyed on the user — the billing subject)
	GetSubscriptionByUser(ctx context.Context, userId string) (*Subscription, error)
	GetSubscriptionByCustomerID(ctx context.Context, customerID string) (*Subscription, error)
	UpsertSubscription(ctx context.Context, params UpsertSubscriptionParams) (*Subscription, error)
	// ReconcileMonitorsToPlan snaps the monitors governed by the user's plan
	// (solo + owned-org monitors) to the new plan's limits after an effective
	// plan change. Returns the number of monitors adjusted.
	ReconcileMonitorsToPlan(ctx context.Context, userId, oldPlan, newPlan string) (int, error)
}

type SchedulerStore interface {
	FetchActiveMonitors(ctx context.Context, region string) ([]Monitor, error)
	FetchOwnedMonitors(ctx context.Context, region string, ownedPartitions []int, partitionSQLExpr string) ([]Monitor, error)
	RecordRegionCheck(ctx context.Context, monitorID, region string, result *CheckResult, source CheckSource) (string, error)
	PersistMonitorResults(ctx context.Context, region string, results []PendingResult) error
	ClaimOutboxAlerts(ctx context.Context, limit int) ([]AlertOutboxRow, error)
	FinalizeOutboxAlert(ctx context.Context, id string, status Status, message string, alertID, notificationChannelID *string) error

	// Cross-region confirmation (see maintenance.record_region_check /
	// evaluate_monitor_quorum, migration 20260706120000).
	UpsertRegionHeartbeat(ctx context.Context, region string) error
	ClaimVerificationRequests(ctx context.Context, region string, limit int) ([]VerificationRequest, error)
	CompleteVerificationRequest(ctx context.Context, id string) error
	// ExpireVerificationRequests deletes verification requests past their
	// expiry and returns the distinct monitor ids that lost one, so the caller
	// can re-run quorum for them (a non-responding region must not block the
	// decision forever).
	ExpireVerificationRequests(ctx context.Context) ([]string, error)
	// EvaluateMonitorQuorum re-runs the quorum decision for a monitor without a
	// triggering check, used by the expiry sweep. Returns the incident
	// transition ("none"/"opened"/"escalated"/"resolved").
	EvaluateMonitorQuorum(ctx context.Context, monitorID string) (string, error)

	// EnqueueRepeatAlerts runs one pass of the repeat-alert sweep
	// (maintenance.enqueue_repeat_alerts): re-enqueues outbox alerts for open
	// incidents whose repeat interval elapsed, honouring maintenance windows.
	// Safe to run from any number of instances (SKIP LOCKED). Returns the
	// number of incidents swept.
	EnqueueRepeatAlerts(ctx context.Context) (int, error)

	// ClaimDueExpiryChecks hands back the next batch of cert/domain expiry
	// sub-checks due for (re)checking (at most once per 24h per monitor+kind).
	// Claiming advances checked_at; safe from any number of instances.
	ClaimDueExpiryChecks(ctx context.Context, limit int) ([]ExpiryCheckClaim, error)
	// RecordExpiryCheck stores one sub-check's outcome and, on a new alert
	// bucket crossing, enqueues the alert (see models.ExpiryAlertBucket).
	// checkErr non-empty records a failure (last_error) and alerts nothing.
	RecordExpiryCheck(ctx context.Context, claim ExpiryCheckClaim, expiresAt *time.Time, checkErr string) error
}
