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
	ID        string    `json:"id"`
	OrgID     string    `json:"orgId"`
	UserID    string    `json:"userId"`
	Role      OrgRole   `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
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
	// CancelAtPeriodEnd reflects Stripe's flag; it is derived from the live
	// Stripe subscription during reconciliation and not persisted in the DB.
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
}
