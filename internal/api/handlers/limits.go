package handlers

import "context"

// planForUser resolves the effective plan for a user (the billing subject),
// defaulting to FREE when no subscription record exists.
//
// Entitlement requires a live status: ACTIVE and TRIALING are entitled, and
// PAST_DUE keeps access as a grace period while Stripe retries the payment.
// CANCELED — which terminal Stripe statuses like unpaid and incomplete also
// map to — carries no entitlement, so the stored plan name is ignored.
func (h *Handlers) planForUser(ctx context.Context, userID string) string {
	sub, err := h.store.GetSubscriptionByUser(ctx, userID)
	if err != nil || sub == nil {
		return "FREE"
	}
	if sub.Status == "CANCELED" {
		return "FREE"
	}
	return sub.Plan
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
