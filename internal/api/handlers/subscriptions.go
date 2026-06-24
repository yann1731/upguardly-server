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
		// Return a default free subscription if none exists
		c.JSON(http.StatusOK, gin.H{"plan": "FREE", "status": "ACTIVE"})
		return
	}

	c.JSON(http.StatusOK, sub)
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
	// metadata is what the webhooks resolve the subscription back to.
	customerID, err := h.stripe.EnsureCustomer(userId, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create billing customer"})
		return
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

	userID, ok := sub.Customer.Metadata["user_id"]
	if !ok || userID == "" {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	status := mapStripeStatus(string(sub.Status))
	start := time.Unix(sub.CurrentPeriodStart, 0)
	end := time.Unix(sub.CurrentPeriodEnd, 0)
	customerID := sub.Customer.ID
	subID := sub.ID

	var priceID string
	if len(sub.Items.Data) > 0 {
		priceID = sub.Items.Data[0].Price.ID
	}

	plan, err := h.planFromPriceID(priceID)
	if err != nil {
		log.Printf("stripe webhook: unrecognised price ID %q for user %s — ignoring event", priceID, userID)
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	_, upsertErr := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
		UserID:               userID,
		Plan:                 plan,
		Status:               status,
		StripeCustomerID:     &customerID,
		StripeSubscriptionID: &subID,
		StripePriceID:        &priceID,
		CurrentPeriodStart:   &start,
		CurrentPeriodEnd:     &end,
	})
	if upsertErr != nil {
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

	userID, ok := sub.Customer.Metadata["user_id"]
	if !ok || userID == "" {
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

	userID, ok := inv.Customer.Metadata["user_id"]
	if !ok || userID == "" {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	// Fetch current plan to preserve it while marking payment as past-due.
	existingPlan := "PRO"
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

func mapStripeStatus(s string) string {
	switch s {
	case "active":
		return "ACTIVE"
	case "past_due":
		return "PAST_DUE"
	case "canceled":
		return "CANCELED"
	case "trialing":
		return "TRIALING"
	default:
		return "ACTIVE"
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
