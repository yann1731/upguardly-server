package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
	moncheck "upguardly-backend/internal/monitor"
)

func (h *Handlers) CreateMonitor(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req models.CreateMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.SetDefaults()

	// Validate field lengths and interval/timeout bounds.
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Prevent SSRF: validate target is not a private/internal network address.
	if err := moncheck.ValidateTarget(req.Target, req.Type); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Resolve the plan and current monitor count for the owning scope. A monitor
	// is either solo (no org, governed by the user's own plan) or org-owned
	// (governed by the org owner's plan; caller must be a member).
	var plan string
	var count int
	if req.OrgID == "" {
		plan = h.planForUser(c.Request.Context(), userId)
		n, err := h.store.CountMonitorsByUser(c.Request.Context(), userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check monitor quota"})
			return
		}
		count = n
	} else {
		if _, err := h.store.GetMembership(c.Request.Context(), req.OrgID, userId); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "You are not a member of this organization"})
			return
		}
		plan = h.planForOrg(c.Request.Context(), req.OrgID)
		n, err := h.store.CountMonitorsByOrg(c.Request.Context(), req.OrgID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check monitor quota"})
			return
		}
		count = n
	}

	// Enforce the resolved plan's limit on the number of monitors.
	limits := models.LimitsForPlan(plan)
	if limits.MaxMonitors != models.Unlimited && count >= limits.MaxMonitors {
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error": fmt.Sprintf("Monitor limit reached for your plan (%d). Upgrade to add more.", limits.MaxMonitors),
		})
		return
	}

	// Regions: default to the platform default, then validate against the
	// registry, the deployed set, and the plan's region cap.
	if len(req.Regions) == 0 {
		req.Regions = []string{models.DefaultRegion}
	}
	regions, err := h.validateRegions(req.Regions, limits)
	if err != nil {
		respondRegionError(c, err)
		return
	}
	req.Regions = regions

	// Interval: omitted (0) means follow-plan — store NULL and let the plan's
	// minimum resolve at read time. An explicit value must meet the plan floor.
	// Validate() skipped the interval checks for the "not provided" case, so
	// re-check timeout against the floor the monitor will resolve to.
	var intervalArg *int
	if req.Interval == 0 {
		if req.Timeout >= limits.MinInterval {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("timeout (%ds) must be less than interval (%ds)", req.Timeout, limits.MinInterval),
			})
			return
		}
	} else if req.Interval < limits.MinInterval {
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error": fmt.Sprintf("Check interval must be at least %d seconds on your plan. Upgrade for more frequent checks.", limits.MinInterval),
		})
		return
	} else {
		v := req.Interval
		intervalArg = &v
	}

	m, err := h.store.CreateMonitor(c.Request.Context(), userId, req.OrgID, req.Name, string(req.Type), req.Target, intervalArg, req.Timeout, *req.Enabled, req.Regions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create monitor"})
		return
	}

	c.JSON(http.StatusCreated, m)
}

func (h *Handlers) ListMonitors(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitors, err := h.store.ListMonitors(c.Request.Context(), userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list monitors"})
		return
	}

	c.JSON(http.StatusOK, monitors)
}

func (h *Handlers) GetMonitor(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")
	m, err := h.store.GetMonitor(c.Request.Context(), id, userId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	c.JSON(http.StatusOK, m)
}

func (h *Handlers) UpdateMonitor(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	var req models.UpdateMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Name == nil && req.Type == nil && req.Target == nil && req.Interval == nil && req.Timeout == nil && req.Enabled == nil && req.Regions == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	// Validate updated field lengths and bounds.
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Interval and regions are plan-gated, so changing either needs the plan
	// of the monitor's owning scope (org owner's plan for org monitors,
	// otherwise the user's own plan).
	if req.Interval != nil || req.Regions != nil {
		existing, err := h.store.GetMonitor(c.Request.Context(), id, userId)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		var plan string
		if existing.OrgID != nil && *existing.OrgID != "" {
			plan = h.planForOrg(c.Request.Context(), *existing.OrgID)
		} else {
			plan = h.planForUser(c.Request.Context(), userId)
		}
		limits := models.LimitsForPlan(plan)
		if req.Interval != nil {
			effTimeout := existing.Timeout
			if req.Timeout != nil {
				effTimeout = *req.Timeout
			}
			if *req.Interval == 0 {
				// Revert to follow-plan: no floor to enforce, but the timeout
				// must be under the floor it will resolve to.
				if effTimeout >= limits.MinInterval {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": fmt.Sprintf("timeout (%ds) must be less than interval (%ds)", effTimeout, limits.MinInterval),
					})
					return
				}
			} else {
				if *req.Interval < limits.MinInterval {
					c.JSON(http.StatusPaymentRequired, gin.H{
						"error": fmt.Sprintf("Check interval must be at least %d seconds on your plan. Upgrade for more frequent checks.", limits.MinInterval),
					})
					return
				}
				if effTimeout >= *req.Interval {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": fmt.Sprintf("timeout (%ds) must be less than interval (%ds)", effTimeout, *req.Interval),
					})
					return
				}
			}
		}
		if req.Regions != nil {
			regions, err := h.validateRegions(*req.Regions, limits)
			if err != nil {
				respondRegionError(c, err)
				return
			}
			*req.Regions = regions
		}
	}

	// If target or type is being updated, re-validate for SSRF.
	if req.Target != nil || req.Type != nil {
		// Need the effective type to validate.
		existing, err := h.store.GetMonitor(c.Request.Context(), id, userId)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		effectiveType := existing.Type
		if req.Type != nil {
			effectiveType = *req.Type
		}
		effectiveTarget := existing.Target
		if req.Target != nil {
			effectiveTarget = *req.Target
		}
		if err := moncheck.ValidateTarget(effectiveTarget, effectiveType); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	m, err := h.store.UpdateMonitor(c.Request.Context(), id, userId, req)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update monitor"})
		return
	}

	c.JSON(http.StatusOK, m)
}

func (h *Handlers) DeleteMonitor(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")
	if err := h.store.DeleteMonitor(c.Request.Context(), id, userId); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}

func (h *Handlers) GetMonitorResults(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	// Optional per-region filter; an unknown region is a plain 400 rather
	// than an empty 200 so typos are visible.
	region := c.Query("region")
	if region != "" && !models.ValidRegion(region) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown region %q", region)})
		return
	}

	results, err := h.store.GetMonitorResults(c.Request.Context(), id, userId, limit, region)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get results"})
		return
	}

	c.JSON(http.StatusOK, results)
}

func (h *Handlers) GetMonitorIncidents(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	incidents, err := h.store.ListIncidents(c.Request.Context(), id, userId, limit)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get incidents"})
		return
	}

	c.JSON(http.StatusOK, incidents)
}

// periodToDuration maps a stats period query value to a lookback window.
// Unknown values fall back to 24h.
func periodToDuration(period string) time.Duration {
	switch period {
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func (h *Handlers) GetMonitorStats(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")
	since := time.Now().Add(-periodToDuration(c.Query("period")))

	stats, err := h.store.GetMonitorStats(c.Request.Context(), id, userId, since)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get stats"})
		return
	}

	c.JSON(http.StatusOK, stats)
}
