package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v76"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

func (h *Handlers) GetSubscription(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sub, err := h.store.GetSubscriptionByUser(c.Request.Context(), userId)
	if err != nil {
		// No DB record. A webhook may have been missed, so try to reconcile
		// directly from Stripe before falling back to a default free plan.
		if reconciled := h.reconcileSubscription(c, userId, nil); reconciled != nil {
			c.JSON(http.StatusOK, reconciled)
			return
		}
		c.JSON(http.StatusOK, gin.H{"plan": "FREE", "status": "ACTIVE"})
		return
	}

	// Reconcile the stored record against Stripe's live state so the displayed
	// plan, status and cancellation flag are always accurate even if a webhook
	// was dropped or arrived in an unparseable API version.
	if reconciled := h.reconcileSubscription(c, userId, sub); reconciled != nil {
		c.JSON(http.StatusOK, reconciled)
		return
	}

	c.JSON(http.StatusOK, sub)
}

// reconcileSubscription refreshes the user's subscription from Stripe's live
// state and returns the up-to-date model, or nil when reconciliation is not
// possible (caller should fall back to the DB record or a default free plan).
func (h *Handlers) reconcileSubscription(c *gin.Context, userID string, dbSub *models.Subscription) *models.Subscription {
	if h.stripe == nil {
		return nil
	}

	// Resolve the Stripe customer: prefer the stored ID, otherwise look one up
	// by user_id metadata (without creating one) to heal records lost to failed
	// webhooks.
	customerID := ""
	if dbSub != nil && dbSub.StripeCustomerID != nil {
		customerID = *dbSub.StripeCustomerID
	} else {
		id, err := h.stripe.FindCustomerByUser(userID)
		if err != nil || id == "" {
			return nil
		}
		customerID = id
	}

	stripeSub, err := h.stripe.GetActiveSubscription(customerID)
	if err != nil {
		return nil // transient Stripe error — fall back to the DB record
	}

	if stripeSub == nil {
		// No subscription at Stripe: downgrade a stale paid record to FREE.
		if dbSub != nil && dbSub.Plan != "FREE" {
			updated, upErr := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
				UserID: userID,
				Plan:   "FREE",
				Status: "CANCELED",
			})
			if upErr != nil {
				return nil
			}
			return updated
		}
		return nil
	}

	params, err := h.upsertParamsFromStripe(userID, stripeSub)
	if err != nil {
		return nil // unrecognised price — leave the DB record untouched
	}
	updated, err := h.store.UpsertSubscription(c.Request.Context(), params)
	if err != nil {
		return nil
	}
	updated.CancelAtPeriodEnd = stripeSub.CancelAtPeriodEnd
	return updated
}

// upsertParamsFromStripe maps a Stripe subscription to upsert params. Period
// fields are only set when present (webhook payloads on newer API versions omit
// them at the top level), so we never overwrite good dates with the epoch.
func (h *Handlers) upsertParamsFromStripe(userID string, s *stripe.Subscription) (models.UpsertSubscriptionParams, error) {
	var priceID string
	if len(s.Items.Data) > 0 && s.Items.Data[0].Price != nil {
		priceID = s.Items.Data[0].Price.ID
	}

	plan, err := h.planFromPriceID(priceID)
	if err != nil {
		return models.UpsertSubscriptionParams{}, err
	}

	subID := s.ID
	params := models.UpsertSubscriptionParams{
		UserID:               userID,
		Plan:                 plan,
		Status:               mapStripeStatus(string(s.Status)),
		StripeSubscriptionID: &subID,
	}
	if s.Customer != nil && s.Customer.ID != "" {
		customerID := s.Customer.ID
		params.StripeCustomerID = &customerID
	}
	if priceID != "" {
		params.StripePriceID = &priceID
	}
	if s.CurrentPeriodStart > 0 {
		start := time.Unix(s.CurrentPeriodStart, 0)
		params.CurrentPeriodStart = &start
	}
	if s.CurrentPeriodEnd > 0 {
		end := time.Unix(s.CurrentPeriodEnd, 0)
		params.CurrentPeriodEnd = &end
	}
	return params, nil
}

func (h *Handlers) CreateCheckout(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if h.stripe == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Billing not configured"})
		return
	}

	// Only accept the plan name from the client; build redirect URLs server-side
	// from the trusted WEBSITE_DOMAIN to prevent open-redirect attacks.
	type checkoutReq struct {
		Plan string `json:"plan" binding:"required,oneof=PRO ENTERPRISE"`
		// SuccessPath and CancelPath must be relative paths (no host/scheme).
		SuccessPath string `json:"successPath"`
		CancelPath  string `json:"cancelPath"`
	}
	var req checkoutReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	websiteDomain := os.Getenv("WEBSITE_DOMAIN")
	if websiteDomain == "" {
		websiteDomain = "http://localhost:3000"
	}

	successURL, cancelURL, err := buildRedirectURLs(websiteDomain, req.SuccessPath, req.CancelPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	priceID, err := h.stripe.PriceIDForPlan(req.Plan)
	if err != nil || priceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid plan or price not configured"})
		return
	}

	// The Stripe customer is keyed on the user (the billing subject); user_id
	// metadata is what the webhooks resolve the subscription back to. Prefer
	// the customer ID already stored on the subscription record so repeat
	// checkouts never hit Stripe's customer search.
	var customerID string
	dbSub, subErr := h.store.GetSubscriptionByUser(c.Request.Context(), userId)
	if subErr == nil && dbSub.StripeCustomerID != nil && *dbSub.StripeCustomerID != "" {
		customerID = *dbSub.StripeCustomerID
	} else {
		customerID, err = h.stripe.EnsureCustomer(userId, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create billing customer"})
			return
		}

		// Persist the ID immediately (not just via webhook) so later lookups
		// and reconciles skip the search — Stripe search is eventually
		// consistent, so a re-search right after creation can miss.
		plan, status := "FREE", "ACTIVE"
		if subErr == nil {
			plan, status = dbSub.Plan, dbSub.Status
		}
		if _, upErr := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
			UserID:           userId,
			Plan:             plan,
			Status:           status,
			StripeCustomerID: &customerID,
		}); upErr != nil {
			log.Printf("checkout: failed to persist stripe customer id for user %s: %v", userId, upErr)
		}
	}

	redirectURL, err := h.stripe.CreateCheckoutSession(customerID, priceID, successURL, cancelURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create checkout session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": redirectURL})
}

func (h *Handlers) CreatePortal(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if h.stripe == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Billing not configured"})
		return
	}

	// Accept only a relative return path; build the absolute URL server-side.
	type portalReq struct {
		ReturnPath string `json:"returnPath"`
	}
	var req portalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	websiteDomain := os.Getenv("WEBSITE_DOMAIN")
	if websiteDomain == "" {
		websiteDomain = "http://localhost:3000"
	}

	returnURL, err := buildSingleRedirectURL(websiteDomain, req.ReturnPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sub, err := h.store.GetSubscriptionByUser(c.Request.Context(), userId)
	if err != nil || sub.StripeCustomerID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No billing account found"})
		return
	}

	redirectURL, err := h.stripe.CreatePortalSession(*sub.StripeCustomerID, returnURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create portal session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": redirectURL})
}

// buildRedirectURLs constructs absolute success and cancel URLs by combining the
// trusted websiteDomain with relative paths provided by the client. Relative paths
// must start with "/" and must not contain scheme or host components.
func buildRedirectURLs(websiteDomain, successPath, cancelPath string) (string, string, error) {
	// Default to the account billing page if the client didn't provide paths.
	if successPath == "" {
		successPath = "/billing"
	}
	if cancelPath == "" {
		cancelPath = "/billing"
	}

	successURL, err := buildSingleRedirectURL(websiteDomain, successPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid successPath: %w", err)
	}
	cancelURL, err := buildSingleRedirectURL(websiteDomain, cancelPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid cancelPath: %w", err)
	}
	return successURL, cancelURL, nil
}

func buildSingleRedirectURL(websiteDomain, path string) (string, error) {
	if path == "" {
		return websiteDomain, nil
	}
	// Path must be relative (start with "/" and not contain "://").
	if strings.Contains(path, "://") || !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("redirect path must be a relative path starting with /")
	}
	// Validate by parsing the combined URL.
	combined := strings.TrimRight(websiteDomain, "/") + path
	u, err := url.Parse(combined)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("could not build a valid redirect URL")
	}
	return combined, nil
}

// StripeWebhook handles incoming Stripe webhook events.
func (h *Handlers) StripeWebhook(c *gin.Context) {
	if h.stripe == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Billing not configured"})
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return
	}

	event, err := h.stripe.ParseWebhook(payload, c.GetHeader("Stripe-Signature"))
	if err != nil {
		// Log the underlying error: a generic 400 hides whether the cause is a
		// bad signing secret, an expired timestamp, or an API-version mismatch.
		log.Printf("stripe webhook: rejected event: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook signature"})
		return
	}

	switch event.Type {
	case "customer.subscription.created", "customer.subscription.updated":
		h.handleSubscriptionUpdated(c, event)
	case "customer.subscription.deleted":
		h.handleSubscriptionDeleted(c, event)
	case "invoice.payment_failed":
		h.handlePaymentFailed(c, event)
	default:
		c.JSON(http.StatusOK, gin.H{"received": true})
	}
}

func (h *Handlers) handleSubscriptionUpdated(c *gin.Context, event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		log.Printf("stripe webhook: failed to unmarshal subscription: %v", err)
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	userID, ok := h.getUserIDFromCustomer(sub.Customer)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	params, err := h.upsertParamsFromStripe(userID, &sub)
	if err != nil {
		log.Printf("stripe webhook: unrecognised price for user %s — ignoring event: %v", userID, err)
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	if _, upsertErr := h.store.UpsertSubscription(c.Request.Context(), params); upsertErr != nil {
		log.Printf("stripe webhook: failed to upsert subscription for user %s: %v", userID, upsertErr)
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

func (h *Handlers) handleSubscriptionDeleted(c *gin.Context, event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	userID, ok := h.getUserIDFromCustomer(sub.Customer)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	_, err := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
		UserID: userID,
		Plan:   "FREE",
		Status: "CANCELED",
	})
	if err != nil {
		log.Printf("stripe webhook: failed to cancel subscription for user %s: %v", userID, err)
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

func (h *Handlers) handlePaymentFailed(c *gin.Context, event stripe.Event) {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	if inv.Subscription == nil {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	userID, ok := h.getUserIDFromCustomer(inv.Customer)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	// Fetch the current plan to preserve it while marking payment as
	// past-due. A user with no subscription record has nothing to preserve —
	// default to FREE, never to a paid plan.
	existingPlan := "FREE"
	if sub, err := h.store.GetSubscriptionByUser(c.Request.Context(), userID); err == nil {
		existingPlan = sub.Plan
	}

	_, err := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
		UserID: userID,
		Plan:   existingPlan,
		Status: "PAST_DUE",
	})
	if err != nil {
		log.Printf("stripe webhook: failed to mark past_due for user %s: %v", userID, err)
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

func (h *Handlers) CancelSubscription(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if h.stripe == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Billing not configured"})
		return
	}

	sub, err := h.store.GetSubscriptionByUser(c.Request.Context(), userId)
	if err != nil || sub.StripeSubscriptionID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No active billing subscription"})
		return
	}

	// Schedule cancellation at the end of the current period so the user keeps
	// access until it lapses. Re-scheduling an already-scheduled cancel is a
	// no-op, so this is idempotent. The customer.subscription.deleted webhook
	// downgrades the record to FREE when the period actually ends.
	if err := h.stripe.SetCancelAtPeriodEnd(*sub.StripeSubscriptionID, true); err != nil {
		log.Printf("cancel subscription for user %s: %v", userId, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel subscription"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": sub.Status, "cancelAtPeriodEnd": true})
}

// mapStripeStatus maps a Stripe subscription status to the internal enum.
// Statuses that carry no entitlement all collapse to CANCELED: "unpaid" means
// payment retries are exhausted (Stripe never emits a deleted event for it),
// "incomplete"/"incomplete_expired" mean the initial payment never succeeded,
// and "paused" means a trial ended without a payment method. Unknown (future)
// statuses also map to CANCELED — defaulting to ACTIVE would grant paid
// access on any status this code doesn't recognise.
func mapStripeStatus(s string) string {
	switch s {
	case "active":
		return "ACTIVE"
	case "past_due":
		return "PAST_DUE"
	case "trialing":
		return "TRIALING"
	default:
		return "CANCELED"
	}
}

// planFromPriceID maps a Stripe price ID to the internal plan name.
// Returns an error for unrecognised price IDs instead of silently defaulting,
// to prevent accidental privilege grants from malformed webhooks.
func (h *Handlers) planFromPriceID(priceID string) (string, error) {
	if h.stripe == nil || priceID == "" {
		return "FREE", nil
	}
	if id, _ := h.stripe.PriceIDForPlan("PRO"); id == priceID {
		return "PRO", nil
	}
	if id, _ := h.stripe.PriceIDForPlan("ENTERPRISE"); id == priceID {
		return "ENTERPRISE", nil
	}
	return "", fmt.Errorf("unrecognised price ID: %s", priceID)
}

func (h *Handlers) getUserIDFromCustomer(customer *stripe.Customer) (string, bool) {
	if customer == nil {
		return "", false
	}
	if customer.Metadata != nil {
		if id, ok := customer.Metadata["user_id"]; ok && id != "" {
			return id, true
		}
	}
	// Webhook payloads typically don't expand the Customer object, so Metadata is nil.
	// We must fetch the Customer from Stripe to read its metadata.
	cust, err := h.stripe.GetCustomer(customer.ID)
	if err != nil || cust == nil {
		return "", false
	}
	id, ok := cust.Metadata["user_id"]
	return id, ok && id != ""
}
