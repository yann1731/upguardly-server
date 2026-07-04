package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// ListRegions returns the regions a monitor can be checked from — the
// deployed subset of the registry (AVAILABLE_REGIONS), not the full registry,
// so the client picker can never offer a region with no scheduler pool.
func (h *Handlers) ListRegions(c *gin.Context) {
	regions := make([]models.Region, 0, len(h.availableRegions))
	for _, id := range h.availableRegions {
		if r, ok := models.RegionByID(id); ok {
			regions = append(regions, r)
		}
	}
	c.JSON(http.StatusOK, regions)
}

// GetMonitorRegions returns the latest per-region check outcome for a
// monitor (the quorum inputs), including whether each report has gone stale.
func (h *Handlers) GetMonitorRegions(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	statuses, err := h.store.ListMonitorRegionStatus(c.Request.Context(), c.Param("id"), userId)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get region status"})
		return
	}

	c.JSON(http.StatusOK, statuses)
}

// regionPlanError marks a region-count violation, which is an upgrade
// prompt (402) rather than a bad request.
type regionPlanError struct {
	max int
}

func (e regionPlanError) Error() string {
	return fmt.Sprintf("Your plan allows up to %d region(s) per monitor. Upgrade to check from more regions.", e.max)
}

// validateRegions normalizes a requested region list and enforces both
// deployment availability (AVAILABLE_REGIONS) and the plan's region cap.
func (h *Handlers) validateRegions(regions []string, limits models.PlanLimits) ([]string, error) {
	normalized, err := models.NormalizeRegions(regions)
	if err != nil {
		return nil, err
	}
	for _, r := range normalized {
		available := false
		for _, a := range h.availableRegions {
			if r == a {
				available = true
				break
			}
		}
		if !available {
			return nil, fmt.Errorf("region %q is not available", r)
		}
	}
	if limits.MaxRegions != models.Unlimited && len(normalized) > limits.MaxRegions {
		return nil, regionPlanError{max: limits.MaxRegions}
	}
	return normalized, nil
}

// respondRegionError maps a validateRegions failure to 402 for plan-cap
// violations and 400 for everything else.
func respondRegionError(c *gin.Context, err error) {
	var planErr regionPlanError
	if errors.As(err, &planErr) {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}
