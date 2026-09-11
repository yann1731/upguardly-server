package handlers

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// monitorScopePlan resolves the plan governing a monitor: the org owner's
// plan for org monitors, otherwise the requesting user's own plan.
func (h *Handlers) monitorScopePlan(c *gin.Context, m *models.Monitor, userId string) string {
	if m.OrgID != nil && *m.OrgID != "" {
		return h.planForOrg(c.Request.Context(), *m.OrgID)
	}
	return h.planForUser(c.Request.Context(), userId)
}

// ListMaintenanceWindows returns the monitor's alert-suppression windows.
func (h *Handlers) ListMaintenanceWindows(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")
	if _, err := h.store.GetMonitor(c.Request.Context(), monitorID, userId); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	windows, err := h.store.ListMaintenanceWindows(c.Request.Context(), monitorID)
	if err != nil {
		log.Printf("maintenance: list windows for monitor %s: %v", monitorID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list maintenance windows"})
		return
	}

	c.JSON(http.StatusOK, windows)
}

// CreateMaintenanceWindow adds a window. ENTERPRISE-only
// (PlanLimits.MaintenanceWindows); existing windows keep working after a
// downgrade (grace, like alert channels), the user just can't add more.
func (h *Handlers) CreateMaintenanceWindow(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")
	m, err := h.store.GetMonitor(c.Request.Context(), monitorID, userId)
	if err != nil || m == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}
	if !h.requireMonitorWrite(c, m, userId) {
		return
	}

	var req models.CreateMaintenanceWindowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// The window shape is valid; the timezone must also exist on this host.
	if req.Timezone != nil {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown timezone"})
			return
		}
	}

	plan := h.monitorScopePlan(c, m, userId)
	if !models.LimitsForPlan(plan).MaintenanceWindows {
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error": "Maintenance windows require the ENTERPRISE plan.",
		})
		return
	}

	w, err := h.store.CreateMaintenanceWindow(c.Request.Context(), monitorID, req)
	if err != nil {
		log.Printf("maintenance: create window for monitor %s: %v", monitorID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create maintenance window"})
		return
	}

	c.JSON(http.StatusCreated, w)
}

// DeleteMaintenanceWindow removes a window. Always allowed for anyone who can
// edit the monitor, regardless of plan (removing suppression must never be
// plan-gated); org VIEWERs are read-only.
func (h *Handlers) DeleteMaintenanceWindow(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")
	m, err := h.store.GetMonitor(c.Request.Context(), monitorID, userId)
	if err != nil || m == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}
	if !h.requireMonitorWrite(c, m, userId) {
		return
	}

	if err := h.store.DeleteMaintenanceWindow(c.Request.Context(), monitorID, c.Param("windowId")); err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Maintenance window not found"})
			return
		}
		log.Printf("maintenance: delete window %s for monitor %s: %v", c.Param("windowId"), monitorID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete maintenance window"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
