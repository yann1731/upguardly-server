package handlers

import (
	"errors"
	"fmt"
	"log"
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
		// Invited org members work inside their org only; their own (usually
		// FREE) subscription doesn't open a separate solo workspace. Solo
		// monitors from before they joined stay readable and editable.
		acct, err := h.accountContext(c.Request.Context(), userId)
		if err != nil {
			log.Printf("monitors: resolve account context for user %s: %v", userId, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check monitor quota"})
			return
		}
		if acct.Type == models.AccountTypeOrgMember {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Organization members create monitors in their organization",
				"code":  "org_member_solo_forbidden",
			})
			return
		}
		plan = h.planForUser(c.Request.Context(), userId)
		n, err := h.store.CountMonitorsByUser(c.Request.Context(), userId)
		if err != nil {
			log.Printf("monitors: count monitors for user %s: %v", userId, err)
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
			log.Printf("monitors: count monitors for org %s: %v", req.OrgID, err)
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

	// A custom slow-response threshold is plan-gated (PRO/ENTERPRISE).
	var degradedThresholdArg *int
	if req.DegradedThresholdMs != nil && *req.DegradedThresholdMs != 0 {
		if !limits.CustomDegradedThreshold {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "Custom response-time thresholds require the PRO plan or higher.",
			})
			return
		}
		v := *req.DegradedThresholdMs
		degradedThresholdArg = &v
	}

	// Repeat alerts are ENTERPRISE-only, with the plan capping the count.
	// Validate() already checked the pair is set together and in bounds.
	var repeatIntervalArg, repeatCountArg *int
	if req.RepeatAlertIntervalSecs != nil && *req.RepeatAlertIntervalSecs != 0 {
		if limits.MaxAlertRepeats == 0 {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "Repeat alerts require the ENTERPRISE plan.",
			})
			return
		}
		if *req.RepeatAlertMaxCount > limits.MaxAlertRepeats {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Repeat alert count is capped at %d on your plan.", limits.MaxAlertRepeats),
			})
			return
		}
		iv := *req.RepeatAlertIntervalSecs
		cv := *req.RepeatAlertMaxCount
		repeatIntervalArg, repeatCountArg = &iv, &cv
	}

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

	// Expiry monitoring (cert/domain) is ENTERPRISE-only and HTTP-only.
	// Applied via a follow-up UpdateMonitor call after creation so
	// CreateMonitor's store call doesn't need to know about it.
	var expiryUpdate *models.UpdateMonitorRequest
	if req.CertCheckEnabled != nil || req.DomainCheckEnabled != nil {
		if req.Type != models.MonitorTypeHTTP {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Expiry monitoring is only available for HTTP monitors"})
			return
		}
		wantsCert := req.CertCheckEnabled != nil && *req.CertCheckEnabled
		wantsDomain := req.DomainCheckEnabled != nil && *req.DomainCheckEnabled
		if (wantsCert && !limits.SSLMonitoring) || (wantsDomain && !limits.DomainMonitoring) {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "SSL/domain expiry monitoring requires the ENTERPRISE plan.",
			})
			return
		}
		expiryUpdate = &models.UpdateMonitorRequest{
			CertCheckEnabled:          req.CertCheckEnabled,
			CertExpiryThresholdDays:   req.CertExpiryThresholdDays,
			DomainCheckEnabled:        req.DomainCheckEnabled,
			DomainExpiryThresholdDays: req.DomainExpiryThresholdDays,
		}
	}

	m, err := h.store.CreateMonitor(c.Request.Context(), userId, req.OrgID, req.Name, string(req.Type), req.Target, intervalArg, req.Timeout, degradedThresholdArg, repeatIntervalArg, repeatCountArg, *req.Enabled, req.Regions)
	if err != nil {
		log.Printf("monitors: create monitor for user %s (org %q, type %s, regions %v): %v", userId, req.OrgID, req.Type, req.Regions, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create monitor"})
		return
	}

	if expiryUpdate != nil {
		updated, err := h.store.UpdateMonitor(c.Request.Context(), m.ID, userId, *expiryUpdate)
		if err != nil {
			log.Printf("monitors: apply expiry config to new monitor %s: %v", m.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Monitor created, but failed to apply expiry monitoring settings"})
			return
		}
		m = updated
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
		log.Printf("monitors: list monitors for user %s: %v", userId, err)
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

	if req.Name == nil && req.Type == nil && req.Target == nil && req.Interval == nil && req.Timeout == nil && req.Enabled == nil && req.Regions == nil && req.DegradedThresholdMs == nil && req.RepeatAlertIntervalSecs == nil && req.RepeatAlertMaxCount == nil && req.CertCheckEnabled == nil && req.CertExpiryThresholdDays == nil && req.DomainCheckEnabled == nil && req.DomainExpiryThresholdDays == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	// Validate updated field lengths and bounds.
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Interval, regions, the slow-response threshold, repeat alerts, and
	// expiry monitoring are all plan-gated, so changing any of them needs the
	// plan of the monitor's owning scope (org owner's plan for org monitors,
	// otherwise the user's own plan).
	if req.Interval != nil || req.Regions != nil || req.DegradedThresholdMs != nil || req.RepeatAlertIntervalSecs != nil || req.RepeatAlertMaxCount != nil || req.CertCheckEnabled != nil || req.DomainCheckEnabled != nil {
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
		// Setting an explicit threshold needs the capability; reverting to the
		// default (0) is always allowed.
		if req.DegradedThresholdMs != nil && *req.DegradedThresholdMs != 0 && !limits.CustomDegradedThreshold {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "Custom response-time thresholds require the PRO plan or higher.",
			})
			return
		}
		// Enabling repeat alerts needs the capability and respects the plan's
		// count cap; disabling (0 on either side) is always allowed. The pair
		// must arrive together when enabling (Validate checked bounds).
		if req.RepeatAlertIntervalSecs != nil || req.RepeatAlertMaxCount != nil {
			iv, cv := 0, 0
			if req.RepeatAlertIntervalSecs != nil {
				iv = *req.RepeatAlertIntervalSecs
			}
			if req.RepeatAlertMaxCount != nil {
				cv = *req.RepeatAlertMaxCount
			}
			if iv != 0 && cv != 0 {
				if limits.MaxAlertRepeats == 0 {
					c.JSON(http.StatusPaymentRequired, gin.H{
						"error": "Repeat alerts require the ENTERPRISE plan.",
					})
					return
				}
				if cv > limits.MaxAlertRepeats {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": fmt.Sprintf("Repeat alert count is capped at %d on your plan.", limits.MaxAlertRepeats),
					})
					return
				}
			} else if iv != 0 || cv != 0 {
				// One side set, the other missing/0 and not a clean disable.
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "repeatAlertIntervalSecs and repeatAlertMaxCount must be set together",
				})
				return
			}
		}
		// Expiry monitoring: enabling needs the capability and an HTTP
		// monitor; disabling (false) is always allowed.
		if req.CertCheckEnabled != nil || req.DomainCheckEnabled != nil {
			effectiveType := existing.Type
			if req.Type != nil {
				effectiveType = *req.Type
			}
			wantsCert := req.CertCheckEnabled != nil && *req.CertCheckEnabled
			wantsDomain := req.DomainCheckEnabled != nil && *req.DomainCheckEnabled
			if (wantsCert || wantsDomain) && effectiveType != models.MonitorTypeHTTP {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Expiry monitoring is only available for HTTP monitors"})
				return
			}
			if (wantsCert && !limits.SSLMonitoring) || (wantsDomain && !limits.DomainMonitoring) {
				c.JSON(http.StatusPaymentRequired, gin.H{
					"error": "SSL/domain expiry monitoring requires the ENTERPRISE plan.",
				})
				return
			}
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
		log.Printf("monitors: update monitor %s for user %s: %v", id, userId, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update monitor"})
		return
	}

	c.JSON(http.StatusOK, m)
}

// GetMonitorExpiry returns the monitor's CERT/DOMAIN expiry sub-check state
// (whichever have been checked at least once).
func (h *Handlers) GetMonitorExpiry(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")
	if _, err := h.store.GetMonitor(c.Request.Context(), id, userId); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	status, err := h.store.GetMonitorExpiryStatus(c.Request.Context(), id)
	if err != nil {
		log.Printf("monitors: get expiry status for monitor %s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get expiry status"})
		return
	}

	c.JSON(http.StatusOK, status)
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
		log.Printf("monitors: get results for monitor %s (user %s, region %q): %v", id, userId, region, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get results"})
		return
	}

	c.JSON(http.StatusOK, results)
}

// maxTZOffsetMinutes bounds the client's UTC offset; real zones span
// UTC-12:00 to UTC+14:00.
const maxTZOffsetMinutes = 14 * 60

func (h *Handlers) GetMonitorUptime(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	days := models.DefaultUptimeDays
	if d := c.Query("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= models.MaxUptimeDays {
			days = parsed
		}
	}

	// Minutes east of UTC, so the day buckets match the caller's calendar.
	// Absent = UTC. A bad value is a 400 rather than a silent shift to UTC,
	// which would look like off-by-one days instead of an error.
	tzOffsetMinutes := 0
	if o := c.Query("tzOffsetMinutes"); o != "" {
		parsed, err := strconv.Atoi(o)
		if err != nil || parsed < -maxTZOffsetMinutes || parsed > maxTZOffsetMinutes {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("tzOffsetMinutes must be an integer within +/-%d", maxTZOffsetMinutes)})
			return
		}
		tzOffsetMinutes = parsed
	}

	uptime, err := h.store.GetMonitorUptime(c.Request.Context(), id, userId, days, tzOffsetMinutes)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		log.Printf("monitors: get uptime for monitor %s (user %s): %v", id, userId, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get uptime"})
		return
	}

	c.JSON(http.StatusOK, uptime)
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
		log.Printf("monitors: list incidents for monitor %s (user %s): %v", id, userId, err)
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
		log.Printf("monitors: get stats for monitor %s (user %s): %v", id, userId, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get stats"})
		return
	}

	c.JSON(http.StatusOK, stats)
}
