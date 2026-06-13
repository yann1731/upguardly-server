package handlers

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// e164Regexp validates E.164 international phone numbers.
var e164Regexp = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// validateAlertTarget checks that the alert destination is valid and safe.
// For webhook channels (DISCORD, SLACK) it also prevents SSRF by rejecting
// URLs that point to private or reserved IP ranges.
func validateAlertTarget(channel models.AlertChannel, target string) error {
	switch channel {
	case models.AlertChannelEMAIL:
		// binding:"email" already validated format upstream; nothing extra needed.
		return nil

	case models.AlertChannelSMS:
		if !e164Regexp.MatchString(target) {
			return fmt.Errorf("invalid SMS target: must be an E.164 phone number (e.g. +12125551234)")
		}
		return nil

	case models.AlertChannelDISCORD, models.AlertChannelSLACK:
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid webhook URL: must be a valid absolute URL")
		}
		if u.Scheme != "https" {
			return fmt.Errorf("invalid webhook URL: only HTTPS webhooks are allowed")
		}
		// Extract the plain hostname (strip port if present).
		host := u.Hostname()
		// Block literal private IPs.
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
				return fmt.Errorf("invalid webhook URL: private IP addresses are not allowed")
			}
			return nil
		}
		// Resolve and validate every address the hostname maps to.
		addrs, err := net.LookupHost(host)
		if err != nil {
			return fmt.Errorf("invalid webhook URL: hostname could not be resolved")
		}
		for _, addr := range addrs {
			ip := net.ParseIP(addr)
			if ip == nil {
				continue
			}
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
				return fmt.Errorf("invalid webhook URL: hostname resolves to a private or reserved IP address")
			}
		}
		return nil
	}

	return nil
}

func (h *Handlers) CreateAlert(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")

	var req models.CreateAlertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.SetDefaults()

	if err := validateAlertTarget(req.Channel, req.Target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Enforce the owning org's plan limit on alerts per monitor.
	monitor, err := h.store.GetMonitor(c.Request.Context(), monitorID, userId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}
	plan := "FREE"
	if monitor.OrgID != nil {
		plan = h.planForOrg(c.Request.Context(), *monitor.OrgID)
	}
	limits := models.LimitsForPlan(plan)
	if limits.MaxAlertsPerMonitor != models.Unlimited {
		existing, err := h.store.ListAlerts(c.Request.Context(), monitorID, userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check alert quota"})
			return
		}
		if len(existing) >= limits.MaxAlertsPerMonitor {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": fmt.Sprintf("Alert limit reached for your plan (%d per monitor). Upgrade to add more.", limits.MaxAlertsPerMonitor),
			})
			return
		}
	}

	alert, err := h.store.CreateAlert(c.Request.Context(), monitorID, userId, string(req.Channel), req.Target, *req.Enabled)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create alert"})
		return
	}

	c.JSON(http.StatusCreated, alert)
}

func (h *Handlers) ListAlerts(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")

	alerts, err := h.store.ListAlerts(c.Request.Context(), monitorID, userId)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list alerts"})
		return
	}

	c.JSON(http.StatusOK, alerts)
}

func (h *Handlers) UpdateAlert(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	var req models.UpdateAlertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Channel == nil && req.Target == nil && req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	alert, err := h.store.GetAlert(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	if _, err := h.store.GetMonitor(c.Request.Context(), alert.MonitorID, userId); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	// Validate the new target if either channel or target is being changed.
	if req.Target != nil || req.Channel != nil {
		effectiveChannel := alert.Channel
		if req.Channel != nil {
			effectiveChannel = *req.Channel
		}
		effectiveTarget := alert.Target
		if req.Target != nil {
			effectiveTarget = *req.Target
		}
		if err := validateAlertTarget(effectiveChannel, effectiveTarget); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	updated, err := h.store.UpdateAlert(c.Request.Context(), id, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update alert"})
		return
	}

	c.JSON(http.StatusOK, updated)
}

func (h *Handlers) DeleteAlert(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	alert, err := h.store.GetAlert(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	if _, err := h.store.GetMonitor(c.Request.Context(), alert.MonitorID, userId); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	if err := h.store.DeleteAlert(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete alert"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}

func isNotFound(err error) bool {
	return err == models.ErrNotFound
}
