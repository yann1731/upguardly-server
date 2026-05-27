package handlers

import (
	"errors"
	"net/http"
	"strconv"

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

	m, err := h.store.CreateMonitor(c.Request.Context(), userId, req.Name, string(req.Type), req.Target, req.Interval, req.Timeout, *req.Enabled)
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

	if req.Name == nil && req.Type == nil && req.Target == nil && req.Interval == nil && req.Timeout == nil && req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	// Validate updated field lengths and bounds.
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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

	results, err := h.store.GetMonitorResults(c.Request.Context(), id, userId, limit)
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
