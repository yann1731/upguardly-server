package handlers

import (
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// CreateOrg initiates the checkout-first org-creation flow. Creating an
// organization is a paid action: rather than creating the org directly, it
// starts a Stripe Checkout session and returns its URL. The organization is
// created only after payment succeeds, by handleCheckoutCompleted.
func (h *Handlers) CreateOrg(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if h.stripe == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Billing not configured"})
		return
	}

	var req models.CreateOrgRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// A user may belong to at most one organization.
	if existing, err := h.store.ListOrganizations(c.Request.Context(), userId); err == nil && len(existing) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "You already belong to an organization"})
		return
	}

	priceID, err := h.stripe.PriceIDForPlan(req.Plan)
	if err != nil || priceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid plan or price not configured"})
		return
	}

	websiteDomain := os.Getenv("WEBSITE_DOMAIN")
	if websiteDomain == "" {
		websiteDomain = "http://localhost:3000"
	}
	successPath := req.SuccessPath
	if successPath == "" {
		successPath = "/organizations"
	}
	cancelPath := req.CancelPath
	if cancelPath == "" {
		cancelPath = "/organizations"
	}
	successURL, err := buildSingleRedirectURL(websiteDomain, successPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cancelURL, err := buildSingleRedirectURL(websiteDomain, cancelPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	customerID, err := h.stripe.EnsureCustomerForUser(userId, req.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create billing customer"})
		return
	}

	redirectURL, err := h.stripe.CreateOrgCheckoutSession(customerID, priceID, successURL, cancelURL, map[string]string{
		"user_id":  userId,
		"org_name": req.Name,
		"plan":     req.Plan,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create checkout session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": redirectURL})
}

func (h *Handlers) ListOrgs(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	orgs, err := h.store.ListOrganizations(c.Request.Context(), userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list organizations"})
		return
	}

	c.JSON(http.StatusOK, orgs)
}

func (h *Handlers) GetOrg(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	org, err := h.store.GetOrganization(c.Request.Context(), orgId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Organization not found"})
		return
	}

	c.JSON(http.StatusOK, org)
}

func (h *Handlers) UpdateOrg(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	var req models.UpdateOrgRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Name == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	org, err := h.store.UpdateOrganization(c.Request.Context(), orgId, req)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Organization not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update organization"})
		return
	}

	c.JSON(http.StatusOK, org)
}

func (h *Handlers) DeleteOrg(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	// Reject if an active subscription exists
	sub, err := h.store.GetSubscription(c.Request.Context(), orgId)
	if err == nil && sub.Status == "ACTIVE" {
		c.JSON(http.StatusConflict, gin.H{"error": "Cancel your subscription before deleting the organization"})
		return
	}

	if err := h.store.DeleteOrganization(c.Request.Context(), orgId); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete organization"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
