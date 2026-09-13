package models

import "time"

type OrgRole string

const (
	OrgRoleOwner  OrgRole = "OWNER"
	OrgRoleAdmin  OrgRole = "ADMIN"
	OrgRoleMember OrgRole = "MEMBER"
	OrgRoleViewer OrgRole = "VIEWER"
)

// roleWeight maps role to numeric weight for comparison (higher = more privileged).
var roleWeight = map[OrgRole]int{
	OrgRoleViewer: 0,
	OrgRoleMember: 1,
	OrgRoleAdmin:  2,
	OrgRoleOwner:  3,
}

// RoleAtLeast reports whether role meets or exceeds minRole.
func RoleAtLeast(role, minRole OrgRole) bool {
	return roleWeight[role] >= roleWeight[minRole]
}

type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"ownerId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type OrganizationMember struct {
	ID     string  `json:"id"`
	OrgID  string  `json:"orgId"`
	UserID string  `json:"userId"`
	Role   OrgRole `json:"role"`
	// Email is resolved from SuperTokens by the ListMembers handler for
	// display; it is not stored with the membership. Empty when the lookup
	// failed.
	Email     string    `json:"email,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// OrgAlertRecipient is a notify-only alerting seat: a bare EMAIL or SMS
// contact attached to an organization that receives alerts for every org
// monitor. No user account is involved; the org owner keeps receiving through
// their own notification channels without consuming a seat.
type OrgAlertRecipient struct {
	ID        string       `json:"id"`
	OrgID     string       `json:"orgId"`
	Channel   AlertChannel `json:"channel"`
	Target    string       `json:"target"`
	CreatedAt time.Time    `json:"createdAt"`
}

// OrgSeats reports seat usage against the org's plan limits so the client can
// render counters. Max values use the Unlimited (-1) sentinel.
type OrgSeats struct {
	LoginSeatsUsed      int `json:"loginSeatsUsed"`
	MaxLoginSeats       int `json:"maxLoginSeats"`
	AlertRecipientsUsed int `json:"alertRecipientsUsed"`
	MaxAlertRecipients  int `json:"maxAlertRecipients"`
}

// OrgWithSeats is the GET /organizations/:id response shape.
type OrgWithSeats struct {
	Organization
	Seats OrgSeats `json:"seats"`
}

// AccountType distinguishes how a user relates to billing. An INDIVIDUAL has
// no org and is billed on their own subscription. An ORG_OWNER created an org
// (on their own ENTERPRISE subscription). An ORG_MEMBER joined someone else's
// org by invitation: their entitlements come from the org owner's plan, not
// their own subscription, and they don't manage billing.
type AccountType string

const (
	AccountTypeIndividual AccountType = "INDIVIDUAL"
	AccountTypeOrgOwner   AccountType = "ORG_OWNER"
	AccountTypeOrgMember  AccountType = "ORG_MEMBER"
)

// AccountOrg is the caller's organization as seen from GET /me.
type AccountOrg struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Role OrgRole `json:"role"`
}

// WorkspaceType distinguishes the caller's own workspace from an org's.
type WorkspaceType string

const (
	WorkspaceTypePersonal WorkspaceType = "PERSONAL"
	WorkspaceTypeOrg      WorkspaceType = "ORG"
)

// Workspace is one context a user can switch into. The personal workspace
// holds their solo monitors and integrations on their own subscription; an org
// workspace holds the org's monitors on the org owner's plan. ID is
// "personal" or the org id — the value clients send as X-Workspace-Id.
type Workspace struct {
	ID   string        `json:"id"`
	Type WorkspaceType `json:"type"`
	// Name and Role are set for org workspaces only.
	Name string  `json:"name,omitempty"`
	Role OrgRole `json:"role,omitempty"`
	Plan string  `json:"plan"`
	// MonitorsUsed and MaxMonitors report the workspace's monitor quota so the
	// dashboard can render a counter. Both are figures for the *billing owner*
	// — the user themselves for a personal workspace, the org owner for an
	// org's — and the cap is one pool covering their personal monitors and
	// every monitor in an org they own. So an org owner sees the same numbers
	// in both of their workspaces. MaxMonitors uses the Unlimited (-1)
	// sentinel, like OrgSeats.
	MonitorsUsed int `json:"monitorsUsed"`
	MaxMonitors  int `json:"maxMonitors"`
}

// AccountContext is the caller's account type, org (nil for INDIVIDUAL) and
// the workspaces they can switch between.
//
// Type and EffectivePlan predate workspaces and are kept for older clients:
// EffectivePlan is the org owner's plan for ORG_MEMBER, the user's own
// otherwise. New clients read the per-workspace Plan instead.
type AccountContext struct {
	Type          AccountType `json:"accountType"`
	EffectivePlan string      `json:"effectivePlan"`
	Org           *AccountOrg `json:"org"`
	Workspaces    []Workspace `json:"workspaces"`
}

type Invitation struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"orgId"`
	Email     string    `json:"email"`
	Role      OrgRole   `json:"role"`
	Token     string    `json:"token,omitempty"`
	Status    string    `json:"status"`
	InvitedBy string    `json:"invitedBy"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// InvitationPreview is the public GET /invitations/:token response: enough
// for the invite page to say who is inviting whom before the invitee signs in.
// Holding the token (mailed to Email) is the only credential required.
type InvitationPreview struct {
	OrgName string  `json:"orgName"`
	Email   string  `json:"email"`
	Role    OrgRole `json:"role"`
	// Status is PENDING, ACCEPTED, REVOKED or EXPIRED. A PENDING row whose
	// expiry has passed is reported as EXPIRED.
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Subscription struct {
	ID                   string     `json:"id"`
	UserID               string     `json:"userId"`
	Plan                 string     `json:"plan"`
	Status               string     `json:"status"`
	StripeCustomerID     *string    `json:"stripeCustomerId,omitempty"`
	StripeSubscriptionID *string    `json:"stripeSubscriptionId,omitempty"`
	StripePriceID        *string    `json:"stripePriceId,omitempty"`
	CurrentPeriodStart   *time.Time `json:"currentPeriodStart,omitempty"`
	CurrentPeriodEnd     *time.Time `json:"currentPeriodEnd,omitempty"`
	// CancelAtPeriodEnd reflects Stripe's flag: the plan is scheduled to lapse
	// at CurrentPeriodEnd instead of renewing. Status stays ACTIVE until it
	// does — the entitlement is unchanged — so this is the only signal that no
	// renewal is coming. Persisted, so every read reports it and not just the
	// ones that happen to reconcile against live Stripe state.
	CancelAtPeriodEnd bool      `json:"cancelAtPeriodEnd"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// --- Request types ---

// CreateOrgRequest creates an organization. Org creation is gated to ENTERPRISE
// accounts (checked in the handler); the org's plan derives from its owner.
type CreateOrgRequest struct {
	Name string `json:"name" binding:"required,min=2,max=100"`
}

type UpdateOrgRequest struct {
	Name *string `json:"name" binding:"omitempty,min=2,max=100"`
}

type InviteMemberRequest struct {
	Email string  `json:"email" binding:"required,email"`
	Role  OrgRole `json:"role" binding:"required,oneof=ADMIN MEMBER VIEWER"`
}

type CreateOrgAlertRecipientRequest struct {
	Channel AlertChannel `json:"channel" binding:"required,oneof=EMAIL SMS"`
	Target  string       `json:"target" binding:"required,max=254"`
}

type UpdateMemberRoleRequest struct {
	Role OrgRole `json:"role" binding:"required,oneof=ADMIN MEMBER VIEWER"`
}

type CreateCheckoutRequest struct {
	Plan       string `json:"plan" binding:"required,oneof=PRO ENTERPRISE"`
	SuccessURL string `json:"successUrl" binding:"required,url"`
	CancelURL  string `json:"cancelUrl" binding:"required,url"`
}

type UpsertSubscriptionParams struct {
	UserID               string
	Plan                 string
	Status               string
	StripeCustomerID     *string
	StripeSubscriptionID *string
	StripePriceID        *string
	CurrentPeriodStart   *time.Time
	CurrentPeriodEnd     *time.Time
	CancelAtPeriodEnd    bool
}
