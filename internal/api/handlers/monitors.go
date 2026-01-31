package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

func (h *Handlers) CreateMonitor(c *gin.Context) {
	var req models.CreateMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.SetDefaults()

	monitor, err := h.db.Prisma.Monitor.CreateOne(
		db.Monitor.Name.Set(req.Name),
		db.Monitor.Type.Set(db.MonitorType(req.Type)),
		db.Monitor.Target.Set(req.Target),
		db.Monitor.Interval.Set(req.Interval),
		db.Monitor.Timeout.Set(req.Timeout),
		db.Monitor.Enabled.Set(*req.Enabled),
	).Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create monitor"})
		return
	}

	c.JSON(http.StatusCreated, monitorToResponse(monitor))
}

func (h *Handlers) ListMonitors(c *gin.Context) {
	monitors, err := h.db.Prisma.Monitor.FindMany().Exec(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list monitors"})
		return
	}

	response := make([]models.Monitor, len(monitors))
	for i, m := range monitors {
		response[i] = *monitorToResponse(&m)
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handlers) GetMonitor(c *gin.Context) {
	id := c.Param("id")

	monitor, err := h.db.Prisma.Monitor.FindUnique(
		db.Monitor.ID.Equals(id),
	).Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	c.JSON(http.StatusOK, monitorToResponse(monitor))
}

func (h *Handlers) UpdateMonitor(c *gin.Context) {
	id := c.Param("id")

	var req models.UpdateMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var updates []db.MonitorSetParam

	if req.Name != nil {
		updates = append(updates, db.Monitor.Name.Set(*req.Name))
	}
	if req.Type != nil {
		updates = append(updates, db.Monitor.Type.Set(db.MonitorType(*req.Type)))
	}
	if req.Target != nil {
		updates = append(updates, db.Monitor.Target.Set(*req.Target))
	}
	if req.Interval != nil {
		updates = append(updates, db.Monitor.Interval.Set(*req.Interval))
	}
	if req.Timeout != nil {
		updates = append(updates, db.Monitor.Timeout.Set(*req.Timeout))
	}
	if req.Enabled != nil {
		updates = append(updates, db.Monitor.Enabled.Set(*req.Enabled))
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	monitor, err := h.db.Prisma.Monitor.FindUnique(
		db.Monitor.ID.Equals(id),
	).Update(updates...).Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	c.JSON(http.StatusOK, monitorToResponse(monitor))
}

func (h *Handlers) DeleteMonitor(c *gin.Context) {
	id := c.Param("id")

	_, err := h.db.Prisma.Monitor.FindUnique(
		db.Monitor.ID.Equals(id),
	).Delete().Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}

func (h *Handlers) GetMonitorResults(c *gin.Context) {
	id := c.Param("id")

	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	results, err := h.db.Prisma.MonitorResult.FindMany(
		db.MonitorResult.MonitorID.Equals(id),
	).OrderBy(
		db.MonitorResult.CheckedAt.Order(db.DESC),
	).Take(limit).Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get results"})
		return
	}

	response := make([]models.MonitorResult, len(results))
	for i, r := range results {
		response[i] = *resultToResponse(&r)
	}

	c.JSON(http.StatusOK, response)
}

func monitorToResponse(m *db.MonitorModel) *models.Monitor {
	return &models.Monitor{
		ID:        m.ID,
		Name:      m.Name,
		Type:      models.MonitorType(m.Type),
		Target:    m.Target,
		Interval:  m.Interval,
		Timeout:   m.Timeout,
		Enabled:   m.Enabled,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func resultToResponse(r *db.MonitorResultModel) *models.MonitorResult {
	result := &models.MonitorResult{
		ID:        r.ID,
		MonitorID: r.MonitorID,
		Status:    models.Status(r.Status),
		Latency:   r.Latency,
		CheckedAt: r.CheckedAt,
	}

	if code, ok := r.StatusCode(); ok {
		result.StatusCode = &code
	}

	if msg, ok := r.Message(); ok {
		result.Message = &msg
	}

	return result
}
