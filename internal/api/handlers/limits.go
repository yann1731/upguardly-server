package handlers

import "context"

// planForOrg resolves the subscription plan name for an organization,
// defaulting to FREE when no subscription record exists.
func (h *Handlers) planForOrg(ctx context.Context, orgID string) string {
	sub, err := h.store.GetSubscription(ctx, orgID)
	if err != nil || sub == nil {
		return "FREE"
	}
	return sub.Plan
}
