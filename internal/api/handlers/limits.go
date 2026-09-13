package handlers

import (
	"context"

	"upguardly-backend/internal/models"
)

// effectivePlan maps a subscription record to the plan the user is actually
// entitled to. ACTIVE and TRIALING are entitled, and PAST_DUE keeps access as
// a grace period while Stripe retries the payment. CANCELED — which terminal
// Stripe statuses like unpaid and incomplete also map to — carries no
// entitlement, so the stored plan name is ignored. No record means FREE.
func effectivePlan(sub *models.Subscription) string {
	if sub == nil || sub.Status == "CANCELED" {
		return "FREE"
	}
	return sub.Plan
}

// planForUser resolves the effective plan for a user (the billing subject),
// defaulting to FREE when no subscription record exists.
func (h *Handlers) planForUser(ctx context.Context, userID string) string {
	sub, err := h.store.GetSubscriptionByUser(ctx, userID)
	if err != nil {
		return "FREE"
	}
	return effectivePlan(sub)
}

// orgOwner resolves the user an organization bills to. Every entitlement an org
// has is really its owner's, so this is the one place that mapping lives.
func (h *Handlers) orgOwner(ctx context.Context, orgID string) (string, error) {
	org, err := h.store.GetOrganization(ctx, orgID)
	if err != nil {
		return "", err
	}
	if org == nil {
		return "", models.ErrNotFound
	}
	return org.OwnerID, nil
}

// billingOwner resolves the user whose subscription governs a workspace, and
// therefore whose monitor pool its monitors draw on: the caller themselves in
// their personal workspace (orgID empty), the org owner in an org's. An org
// that can't be loaded is an error rather than a silent FREE — membership is
// proven before this runs, so a missing org means the database is unwell and
// quietly applying free-tier limits would be worse than failing.
func (h *Handlers) billingOwner(ctx context.Context, userID, orgID string) (string, error) {
	if orgID == "" {
		return userID, nil
	}
	return h.orgOwner(ctx, orgID)
}

// planForOrg resolves an organization's effective plan, which is its owner's
// plan. Only ENTERPRISE accounts can create orgs, so a healthy org resolves to
// ENTERPRISE; it falls back to FREE if the org or owner can't be resolved.
func (h *Handlers) planForOrg(ctx context.Context, orgID string) string {
	owner, err := h.orgOwner(ctx, orgID)
	if err != nil {
		return "FREE"
	}
	return h.planForUser(ctx, owner)
}

// accountContext classifies the user as an independent account, an org owner,
// or an invited org member, and lists the workspaces they can switch between:
// always their personal workspace (on their own plan), plus their org's (on
// the org owner's plan). A user belongs to at most one org (enforced on
// invitation accept); the owner holds an OWNER membership row like any other
// member.
func (h *Handlers) accountContext(ctx context.Context, userID string) (models.AccountContext, error) {
	orgs, err := h.store.ListOrganizations(ctx, userID)
	if err != nil {
		return models.AccountContext{}, err
	}
	personalPlan := h.planForUser(ctx, userID)
	personal := models.Workspace{ID: "personal", Type: models.WorkspaceTypePersonal, Plan: personalPlan}
	personalUsed, err := h.store.CountMonitorsForBillingOwner(ctx, userID)
	if err != nil {
		return models.AccountContext{}, err
	}
	personal.MonitorsUsed = personalUsed
	personal.MaxMonitors = models.LimitsForPlan(personalPlan).MaxMonitors
	if len(orgs) == 0 {
		return models.AccountContext{
			Type:          models.AccountTypeIndividual,
			EffectivePlan: personalPlan,
			Workspaces:    []models.Workspace{personal},
		}, nil
	}

	org := orgs[0]
	membership, err := h.store.GetMembership(ctx, org.ID, userID)
	if err != nil {
		return models.AccountContext{}, err
	}
	acct := models.AccountContext{
		Org: &models.AccountOrg{ID: org.ID, Name: org.Name, Role: membership.Role},
	}
	if membership.Role == models.OrgRoleOwner {
		acct.Type = models.AccountTypeOrgOwner
		acct.EffectivePlan = personalPlan
	} else {
		acct.Type = models.AccountTypeOrgMember
		acct.EffectivePlan = h.planForUser(ctx, org.OwnerID)
	}

	orgWs := models.Workspace{ID: org.ID, Type: models.WorkspaceTypeOrg, Name: org.Name, Role: membership.Role, Plan: acct.EffectivePlan}
	orgWs.MaxMonitors = models.LimitsForPlan(acct.EffectivePlan).MaxMonitors
	// The org's monitors are billed to its owner, whose pool already includes
	// their personal monitors. Keyed on owner_id — the field billingOwner
	// reads — rather than the OWNER membership role, so the two can't diverge:
	// when the caller is the owner, both workspaces draw on the one count
	// already taken above.
	if org.OwnerID == userID {
		orgWs.MonitorsUsed = personalUsed
	} else {
		orgUsed, err := h.store.CountMonitorsForBillingOwner(ctx, org.OwnerID)
		if err != nil {
			return models.AccountContext{}, err
		}
		orgWs.MonitorsUsed = orgUsed
	}

	acct.Workspaces = []models.Workspace{personal, orgWs}
	return acct, nil
}
