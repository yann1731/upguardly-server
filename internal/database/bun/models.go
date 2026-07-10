package bun

import (
	"time"

	"github.com/uptrace/bun"
)

type Organization struct {
	bun.BaseModel `bun:"table:organizations,alias:o"`

	ID        string    `bun:"id,pk,default:cuid()"`
	Name      string    `bun:"name,unique,notnull"`
	OwnerID   string    `bun:"owner_id,notnull"`
	CreatedAt time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:"updated_at,nullzero,notnull,default:current_timestamp"`
}

type OrganizationMember struct {
	bun.BaseModel `bun:"table:organization_members,alias:om"`

	ID             string    `bun:"id,pk,default:cuid()"`
	OrganizationID string    `bun:"organization_id,notnull"`
	UserID         string    `bun:"user_id,unique,notnull"`
	Role           string    `bun:"role,notnull,default:'MEMBER'"`
	CreatedAt      time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
}

type Invitation struct {
	bun.BaseModel `bun:"table:invitations,alias:i"`

	ID             string    `bun:"id,pk,default:cuid()"`
	OrganizationID string    `bun:"organization_id,notnull"`
	Email          string    `bun:"email,notnull"`
	Role           string    `bun:"role,notnull,default:'MEMBER'"`
	Token          string    `bun:"token,unique,notnull"`
	Status         string    `bun:"status,notnull,default:'PENDING'"`
	InvitedBy      string    `bun:"invited_by,notnull"`
	ExpiresAt      time.Time `bun:"expires_at,notnull"`
	CreatedAt      time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
}

type Subscription struct {
	bun.BaseModel `bun:"table:subscriptions,alias:s"`

	ID                   string     `bun:"id,pk,default:cuid()"`
	UserID               string     `bun:"user_id,unique,notnull"`
	Plan                 string     `bun:"plan,notnull,default:'FREE'"`
	Status               string     `bun:"status,notnull,default:'ACTIVE'"`
	StripeCustomerID     *string    `bun:"stripe_customer_id"`
	StripeSubscriptionID *string    `bun:"stripe_subscription_id"`
	StripePriceID        *string    `bun:"stripe_price_id"`
	CurrentPeriodStart   *time.Time `bun:"current_period_start"`
	CurrentPeriodEnd     *time.Time `bun:"current_period_end"`
	CreatedAt            time.Time  `bun:"created_at,nullzero,notnull,default:current_timestamp"`
	UpdatedAt            time.Time  `bun:"updated_at,nullzero,notnull,default:current_timestamp"`
}

type Monitor struct {
	bun.BaseModel `bun:"table:monitors,alias:m"`

	ID     string  `bun:"id,pk,default:cuid()"`
	UserID string  `bun:"user_id,notnull"`
	OrgID  *string `bun:"org_id"`
	Name   string  `bun:"name,notnull"`
	Type   string  `bun:"type,notnull"`
	Target string  `bun:"target,notnull"`
	// Interval is nullable: NULL = follow the owner's plan (resolved via
	// OwnerPlan at conversion time); a value is an explicit override.
	Interval  *int      `bun:"interval"`
	Timeout   int       `bun:"timeout,notnull,default:30"`
	Enabled   bool      `bun:"enabled,notnull,default:true"`
	Regions   []string  `bun:"regions,array,default:'{\"na-east\"}'"`
	CreatedAt time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:"updated_at,nullzero,notnull,default:current_timestamp"`

	// OwnerPlan is the monitor owner's effective plan, populated by a computed
	// column on monitor-returning queries (see ownerPlanExpr). Not a stored
	// column — scan-only — it feeds models.EffectiveInterval in toModel.
	OwnerPlan string `bun:"owner_plan,scanonly"`
}

type MonitorResultRollup struct {
	bun.BaseModel `bun:"table:monitor_result_rollups,alias:mrr"`

	MonitorID  string    `bun:"monitor_id,pk"`
	Region     string    `bun:"region,pk,default:'na-east'"`
	Bucket     time.Time `bun:"bucket,pk"`
	Checks     int       `bun:"checks,notnull"`
	SumLatency int       `bun:"sum_latency,notnull"`
	MinLatency int       `bun:"min_latency,notnull"`
	MaxLatency int       `bun:"max_latency,notnull"`
}

type MonitorResult struct {
	bun.BaseModel `bun:"table:monitor_results,alias:mr"`

	ID         string    `bun:"id,pk,default:cuid()"`
	MonitorID  string    `bun:"monitor_id,notnull"`
	Status     string    `bun:"status,notnull"`
	Latency    int       `bun:"latency,notnull"`
	StatusCode *int      `bun:"status_code"`
	Message    *string   `bun:"message"`
	Region     string    `bun:"region,notnull,default:'na-east'"`
	CheckedAt  time.Time `bun:"checked_at,pk,nullzero,notnull,default:current_timestamp"`
}

type MonitorRegionStatus struct {
	bun.BaseModel `bun:"table:monitor_region_status,alias:mrs"`

	MonitorID  string    `bun:"monitor_id,pk"`
	Region     string    `bun:"region,pk"`
	Status     string    `bun:"status,notnull"`
	Latency    int       `bun:"latency,notnull,default:0"`
	StatusCode *int      `bun:"status_code"`
	Message    *string   `bun:"message"`
	Source     string    `bun:"source,notnull,default:'SCHEDULED'"`
	CheckedAt  time.Time `bun:"checked_at,nullzero,notnull,default:current_timestamp"`
}

type Incident struct {
	bun.BaseModel `bun:"table:incidents,alias:inc"`

	ID         string     `bun:"id,pk,default:cuid()"`
	MonitorID  string     `bun:"monitor_id,notnull"`
	Status     string     `bun:"status,notnull"`
	StartedAt  time.Time  `bun:"started_at,nullzero,notnull,default:current_timestamp"`
	ResolvedAt *time.Time `bun:"resolved_at"`
	StatusCode *int       `bun:"status_code"`
	Message    *string    `bun:"message"`
	CreatedAt  time.Time  `bun:"created_at,nullzero,notnull,default:current_timestamp"`
}

type Alert struct {
	bun.BaseModel `bun:"table:alerts,alias:al"`

	ID        string    `bun:"id,pk,default:cuid()"`
	MonitorID string    `bun:"monitor_id,notnull"`
	Channel   string    `bun:"channel,notnull"`
	Target    string    `bun:"target,notnull"`
	Enabled   bool      `bun:"enabled,notnull,default:true"`
	CreatedAt time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
}

type NotificationChannel struct {
	bun.BaseModel `bun:"table:notification_channels,alias:nc"`

	ID        string    `bun:"id,pk,default:cuid()"`
	UserID    string    `bun:"user_id,notnull"`
	Channel   string    `bun:"channel,notnull"`
	Target    string    `bun:"target,notnull"`
	Enabled   bool      `bun:"enabled,notnull,default:true"`
	CreatedAt time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:"updated_at,nullzero,notnull,default:current_timestamp"`
}

type MonitorChannelSetting struct {
	bun.BaseModel `bun:"table:monitor_channel_settings,alias:mcs"`

	ID                    string    `bun:"id,pk,default:cuid()"`
	MonitorID             string    `bun:"monitor_id,notnull"`
	NotificationChannelID string    `bun:"notification_channel_id,notnull"`
	Enabled               bool      `bun:"enabled,notnull"`
	CreatedAt             time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
	UpdatedAt             time.Time `bun:"updated_at,nullzero,notnull,default:current_timestamp"`
}

type AlertOutbox struct {
	bun.BaseModel `bun:"table:alert_outbox,alias:ao"`

	ID                    string    `bun:"id,pk,default:cuid()"`
	AlertID               *string   `bun:"alert_id"`
	NotificationChannelID *string   `bun:"notification_channel_id"`
	MonitorID             string    `bun:"monitor_id,notnull"`
	Channel               string    `bun:"channel,notnull"`
	Target                string    `bun:"target,notnull"`
	Status                string    `bun:"status,notnull"`
	Message               string    `bun:"message,notnull"`
	StatusCode            *int      `bun:"status_code"`
	Latency               int       `bun:"latency,notnull,default:0"`
	MonitorName           string    `bun:"monitor_name,notnull"`
	MonitorType           string    `bun:"monitor_type,notnull"`
	MonitorTarget         string    `bun:"monitor_target,notnull"`
	Attempts              int       `bun:"attempts,notnull,default:0"`
	NextAttemptAt         time.Time `bun:"next_attempt_at,nullzero,notnull,default:current_timestamp"`
	CreatedAt             time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp"`
}

type AlertHistory struct {
	bun.BaseModel `bun:"table:alert_history,alias:ah"`

	ID                    string    `bun:"id,default:cuid()"`
	AlertID               *string   `bun:"alert_id"`
	NotificationChannelID *string   `bun:"notification_channel_id"`
	Status                string    `bun:"status,notnull"`
	Message               string    `bun:"message,notnull"`
	SentAt                time.Time `bun:"sent_at,pk,nullzero,notnull,default:current_timestamp"`
}
