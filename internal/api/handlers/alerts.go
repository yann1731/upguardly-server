package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

func (h *Handlers) CreateAlert(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")

	_, err := h.db.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorID),
		db.Monitor.UserID.Equals(userId),
	).Exec(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	var req models.CreateAlertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.SetDefaults()

	alert, err := h.db.Prisma.Alert.CreateOne(
		db.Alert.Monitor.Link(db.Monitor.ID.Equals(monitorID)),
		db.Alert.Channel.Set(db.AlertChannel(req.Channel)),
		db.Alert.Target.Set(req.Target),
		db.Alert.Enabled.Set(*req.Enabled),
	).Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create alert"})
		return
	}

	c.JSON(http.StatusCreated, alertToResponse(alert))
}

func (h *Handlers) ListAlerts(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")

	_, err := h.db.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorID),
		db.Monitor.UserID.Equals(userId),
	).Exec(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	alerts, err := h.db.Prisma.Alert.FindMany(
		db.Alert.MonitorID.Equals(monitorID),
	).Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list alerts"})
		return
	}

	response := make([]models.Alert, len(alerts))
	for i, a := range alerts {
		response[i] = *alertToResponse(&a)
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handlers) UpdateAlert(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	alert, err := h.db.Prisma.Alert.FindUnique(
		db.Alert.ID.Equals(id),
	).Exec(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	_, err = h.db.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(alert.MonitorID),
		db.Monitor.UserID.Equals(userId),
	).Exec(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	var req models.UpdateAlertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var updates []db.AlertSetParam

	if req.Channel != nil {
		updates = append(updates, db.Alert.Channel.Set(db.AlertChannel(*req.Channel)))
	}
	if req.Target != nil {
		updates = append(updates, db.Alert.Target.Set(*req.Target))
	}
	if req.Enabled != nil {
		updates = append(updates, db.Alert.Enabled.Set(*req.Enabled))
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	updated, err := h.db.Prisma.Alert.FindUnique(
		db.Alert.ID.Equals(id),
	).Update(updates...).Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update alert"})
		return
	}

	c.JSON(http.StatusOK, alertToResponse(updated))
}

func (h *Handlers) DeleteAlert(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	alert, err := h.db.Prisma.Alert.FindUnique(
		db.Alert.ID.Equals(id),
	).Exec(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	_, err = h.db.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(alert.MonitorID),
		db.Monitor.UserID.Equals(userId),
	).Exec(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Alert not found"})
		return
	}

	_, err = h.db.Prisma.Alert.FindUnique(
		db.Alert.ID.Equals(id),
	).Delete().Exec(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete alert"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}

func alertToResponse(a *db.AlertModel) *models.Alert {
	return &models.Alert{
		ID:        a.ID,
		MonitorID: a.MonitorID,
		Channel:   models.AlertChannel(a.Channel),
		Target:    a.Target,
		Enabled:   a.Enabled,
		CreatedAt: a.CreatedAt,
	}
}
