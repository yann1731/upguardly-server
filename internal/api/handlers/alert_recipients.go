package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// Org alert recipients are the Enterprise "alerting seats": notify-only
// EMAIL/SMS contacts on an organization that receive alerts for every org
// monitor. No user account is involved. The org owner keeps receiving through
// their own notification channels without consuming a seat; the alert fan-out
// (maintenance.evaluate_monitor_quorum) dedupes a recipient that duplicates an
// owner channel.

func (h *Handlers) ListOrgAlertRecipients(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	recipients, err := h.store.ListOrgAlertRecipients(c.Request.Context(), orgId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list alert recipients"})
		return
	}

	c.JSON(http.StatusOK, recipients)
}

func (h *Handlers) CreateOrgAlertRecipient(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	var req models.CreateOrgAlertRecipientRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := validateRecipientTarget(req.Channel, req.Target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	limits := models.LimitsForPlan(h.planForOrg(c.Request.Context(), orgId))
	if limits.MaxAlertRecipients == 0 {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Alert recipients require an Enterprise plan"})
		return
	}
	if limits.MaxAlertRecipients != models.Unlimited {
		count, err := h.store.CountOrgAlertRecipients(c.Request.Context(), orgId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check alert recipient quota"})
			return
		}
		if count >= limits.MaxAlertRecipients {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": fmt.Sprintf("Alert recipient limit reached for your plan (%d). Remove a recipient to add another.", limits.MaxAlertRecipients),
			})
			return
		}
	}

	recipient, err := h.store.CreateOrgAlertRecipient(c.Request.Context(), orgId, string(req.Channel), req.Target)
	if err != nil {
		if errors.Is(err, models.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "This recipient already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create alert recipient"})
		return
	}

	c.JSON(http.StatusCreated, recipient)
}

func (h *Handlers) DeleteOrgAlertRecipient(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	recipientId := c.Param("recipientId")

	if err := h.store.DeleteOrgAlertRecipient(c.Request.Context(), orgId, recipientId); err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Alert recipient not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete alert recipient"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
