package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v76"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

func (h *Handlers) GetSubscription(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	sub, err := h.store.GetSubscription(c.Request.Context(), orgId)
	if err != nil {
		// Return a default free subscription if none exists
		c.JSON(http.StatusOK, gin.H{"plan": "FREE", "status": "ACTIVE"})
		return
	}

	c.JSON(http.StatusOK, sub)
}

func (h *Handlers) CreateCheckout(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	if h.stripe == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Billing not configured"})
		return
	}

	var req models.CreateCheckoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	priceID, err := h.stripe.PriceIDForPlan(req.Plan)
	if err != nil || priceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid plan or price not configured"})
		return
	}

	org, err := h.store.GetOrganization(c.Request.Context(), orgId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Organization not found"})
		return
	}

	customerID, err := h.stripe.EnsureCustomer(orgId, org.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create billing customer"})
		return
	}

	url, err := h.stripe.CreateCheckoutSession(customerID, priceID, req.SuccessURL, req.CancelURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create checkout session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": url})
}

func (h *Handlers) CreatePortal(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	if h.stripe == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Billing not configured"})
		return
	}

	type portalReq struct {
		ReturnURL string `json:"returnUrl" binding:"required,url"`
	}
	var req portalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sub, err := h.store.GetSubscription(c.Request.Context(), orgId)
	if err != nil || sub.StripeCustomerID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No billing account found"})
		return
	}

	url, err := h.stripe.CreatePortalSession(*sub.StripeCustomerID, req.ReturnURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create portal session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": url})
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

	orgID, ok := sub.Customer.Metadata["org_id"]
	if !ok || orgID == "" {
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

	plan := h.planFromPriceID(priceID)

	_, err := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
		OrgID:                orgID,
		Plan:                 plan,
		Status:               status,
		StripeCustomerID:     &customerID,
		StripeSubscriptionID: &subID,
		StripePriceID:        &priceID,
		CurrentPeriodStart:   &start,
		CurrentPeriodEnd:     &end,
	})
	if err != nil {
		log.Printf("stripe webhook: failed to upsert subscription for org %s: %v", orgID, err)
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

func (h *Handlers) handleSubscriptionDeleted(c *gin.Context, event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	orgID, ok := sub.Customer.Metadata["org_id"]
	if !ok || orgID == "" {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	_, err := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
		OrgID:  orgID,
		Plan:   "FREE",
		Status: "CANCELED",
	})
	if err != nil {
		log.Printf("stripe webhook: failed to cancel subscription for org %s: %v", orgID, err)
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

	orgID, ok := inv.Customer.Metadata["org_id"]
	if !ok || orgID == "" {
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	_, err := h.store.UpsertSubscription(c.Request.Context(), models.UpsertSubscriptionParams{
		OrgID:  orgID,
		Plan:   "PRO",
		Status: "PAST_DUE",
	})
	if err != nil {
		log.Printf("stripe webhook: failed to mark past_due for org %s: %v", orgID, err)
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

func (h *Handlers) planFromPriceID(priceID string) string {
	if h.stripe == nil || priceID == "" {
		return "FREE"
	}
	if id, _ := h.stripe.PriceIDForPlan("PRO"); id == priceID {
		return "PRO"
	}
	if id, _ := h.stripe.PriceIDForPlan("ENTERPRISE"); id == priceID {
		return "ENTERPRISE"
	}
	return "PRO"
}
