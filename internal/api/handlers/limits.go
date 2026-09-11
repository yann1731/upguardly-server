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

// planForOrg resolves an organization's effective plan, which is its owner's
// plan. Only ENTERPRISE accounts can create orgs, so a healthy org resolves to
// ENTERPRISE; it falls back to FREE if the org or owner can't be resolved.
func (h *Handlers) planForOrg(ctx context.Context, orgID string) string {
	org, err := h.store.GetOrganization(ctx, orgID)
	if err != nil || org == nil {
		return "FREE"
	}
	return h.planForUser(ctx, org.OwnerID)
}

// accountContext classifies the user as an independent account, an org owner,
// or an invited org member, and resolves the plan their workspace runs on. A
// user belongs to at most one org (enforced on invitation accept); the owner
// holds an OWNER membership row like any other member.
func (h *Handlers) accountContext(ctx context.Context, userID string) (models.AccountContext, error) {
	orgs, err := h.store.ListOrganizations(ctx, userID)
	if err != nil {
		return models.AccountContext{}, err
	}
	if len(orgs) == 0 {
		return models.AccountContext{
			Type:          models.AccountTypeIndividual,
			EffectivePlan: h.planForUser(ctx, userID),
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
		acct.EffectivePlan = h.planForUser(ctx, userID)
	} else {
		acct.Type = models.AccountTypeOrgMember
		acct.EffectivePlan = h.planForUser(ctx, org.OwnerID)
	}
	return acct, nil
}
