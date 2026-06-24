package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// CreateOrg creates an organization. Organizations are an ENTERPRISE-only
// feature (a multi-seat team), so the caller's account must be on the
// ENTERPRISE plan; FREE/PRO are single-user. The new org's effective plan
// derives from its owner.
func (h *Handlers) CreateOrg(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req models.CreateOrgRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Creating an organization requires an Enterprise account.
	if h.planForUser(c.Request.Context(), userId) != "ENTERPRISE" {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Creating an organization requires an Enterprise plan"})
		return
	}

	// A user may belong to at most one organization.
	if existing, err := h.store.ListOrganizations(c.Request.Context(), userId); err == nil && len(existing) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "You already belong to an organization"})
		return
	}

	org, err := h.store.CreateOrganization(c.Request.Context(), userId, req.Name)
	if err != nil {
		if errors.Is(err, models.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "That organization name is already taken"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create organization"})
		return
	}

	c.JSON(http.StatusCreated, org)
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

	// The subscription belongs to the owner's account, not the org, so deleting
	// the org leaves their Enterprise plan intact (they can create another).
	if err := h.store.DeleteOrganization(c.Request.Context(), orgId); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete organization"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
