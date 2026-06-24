package handlers

import "context"

// planForUser resolves the subscription plan name for a user (the billing
// subject), defaulting to FREE when no subscription record exists.
func (h *Handlers) planForUser(ctx context.Context, userID string) string {
	sub, err := h.store.GetSubscriptionByUser(ctx, userID)
	if err != nil || sub == nil {
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
