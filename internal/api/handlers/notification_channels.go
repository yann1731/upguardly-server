package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// Global (account-level) notification channels. Every monitor the user owns
// inherits them by default; per-monitor MonitorChannelSetting rows override
// the enabled flag (see ListMonitorChannels below).

func (h *Handlers) CreateNotificationChannel(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req models.CreateNotificationChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.SetDefaults()

	// Email alerting is pinned to the account email: whatever target the
	// caller sent is replaced with the channel owner's own address.
	if req.Channel == models.AlertChannelEMAIL {
		email, err := h.UserEmailLookup(userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve account email"})
			return
		}
		req.Target = email
	}

	if err := validateAlertTarget(req.Channel, req.Target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	limits := models.LimitsForPlan(h.planForUser(c.Request.Context(), userId))
	if !limits.ChannelAllowed(req.Channel) {
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error": fmt.Sprintf("The %s channel is not included in your plan. Upgrade to use it.", req.Channel),
		})
		return
	}

	existing, err := h.store.ListNotificationChannels(c.Request.Context(), userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check existing integrations"})
		return
	}
	if status, msg := checkChannelRoom(existing, limits, req.Channel, req.Target, ""); status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}

	channel, err := h.store.CreateNotificationChannel(c.Request.Context(), userId, string(req.Channel), req.Target, *req.Enabled)
	if err != nil {
		// The unique (user, channel, target) index is the backstop for two
		// adds racing past the check above.
		if errors.Is(err, models.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": duplicateChannelMessage(req.Channel)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create notification channel"})
		return
	}

	c.JSON(http.StatusCreated, channel)
}

// checkChannelRoom reports whether one more (channel, target) integration fits
// alongside existing: no exact duplicate of the same destination, and room
// under the plan's cap for that channel. skipID is the row being edited (empty
// when creating), which neither check counts against itself. It returns the
// HTTP status and message to send, or 0 when the integration is fine.
//
// Duplicates matter because the alert fan-out inserts one outbox row per
// channel row: two integrations pointing at the same destination deliver the
// same alert twice. EMAIL is the case users hit, since its target is pinned to
// the account email and every add after the first is necessarily a duplicate.
func checkChannelRoom(existing []models.NotificationChannel, limits models.PlanLimits, channel models.AlertChannel, target, skipID string) (int, string) {
	max, allowed := limits.MaxChannelsFor(channel)
	if !allowed {
		return http.StatusPaymentRequired, fmt.Sprintf("The %s channel is not included in your plan. Upgrade to use it.", channel)
	}

	count := 0
	for _, ch := range existing {
		if ch.ID == skipID || ch.Channel != channel {
			continue
		}
		if ch.Target == target {
			return http.StatusConflict, duplicateChannelMessage(channel)
		}
		count++
	}

	if max != models.Unlimited && count >= max {
		return channelCapStatus(max), channelCapMessage(channel, max)
	}
	return 0, ""
}

func duplicateChannelMessage(channel models.AlertChannel) string {
	if channel == models.AlertChannelEMAIL {
		// The target is pinned server-side, so naming it would only confuse.
		return "Email alerts to your account address are already set up."
	}
	return fmt.Sprintf("That %s destination is already one of your integrations.", channel)
}

// channelCapStatus separates "pay us and you can have more" from "this is one
// per account on every plan" — EMAIL and SMS address the account holder, so an
// upgrade would not raise their cap and 402 would be misleading.
func channelCapStatus(max int) int {
	if max == 1 {
		return http.StatusConflict
	}
	return http.StatusPaymentRequired
}

func channelCapMessage(channel models.AlertChannel, max int) string {
	if max == 1 {
		return fmt.Sprintf("An account can have one %s integration. Edit the existing one instead.", channel)
	}
	return fmt.Sprintf("Your plan includes %d %s integrations. Upgrade to add more.", max, channel)
}

func (h *Handlers) ListNotificationChannels(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	channels, err := h.store.ListNotificationChannels(c.Request.Context(), userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list notification channels"})
		return
	}

	c.JSON(http.StatusOK, channels)
}

func (h *Handlers) UpdateNotificationChannel(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	var req models.UpdateNotificationChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Channel == nil && req.Target == nil && req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	channel, err := h.store.GetNotificationChannel(c.Request.Context(), id, userId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Notification channel not found"})
		return
	}

	// Switching to a different channel counts as configuring it, so it is
	// plan-gated like create; editing the target or toggling enabled on the
	// existing channel is not — after a downgrade, users keep control of
	// channels they configured while entitled.
	if req.Channel != nil && *req.Channel != channel.Channel {
		limits := models.LimitsForPlan(h.planForUser(c.Request.Context(), userId))
		if !limits.ChannelAllowed(*req.Channel) {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": fmt.Sprintf("The %s channel is not included in your plan. Upgrade to use it.", *req.Channel),
			})
			return
		}
	}

	if req.Target != nil || req.Channel != nil {
		effectiveChannel := channel.Channel
		if req.Channel != nil {
			effectiveChannel = *req.Channel
		}
		// EMAIL channels stay pinned to the account email regardless of the
		// requested target (including when switching an existing channel to
		// EMAIL).
		if effectiveChannel == models.AlertChannelEMAIL {
			email, err := h.UserEmailLookup(userId)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve account email"})
				return
			}
			req.Target = &email
		}
		effectiveTarget := channel.Target
		if req.Target != nil {
			effectiveTarget = *req.Target
		}
		if err := validateAlertTarget(effectiveChannel, effectiveTarget); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// A retargeted (or retyped) channel clears the same duplicate and
		// per-type cap checks as a new one, ignoring the row being edited.
		existing, err := h.store.ListNotificationChannels(c.Request.Context(), userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check existing integrations"})
			return
		}
		limits := models.LimitsForPlan(h.planForUser(c.Request.Context(), userId))
		if status, msg := checkChannelRoom(existing, limits, effectiveChannel, effectiveTarget, id); status != 0 {
			c.JSON(status, gin.H{"error": msg})
			return
		}
	}

	updated, err := h.store.UpdateNotificationChannel(c.Request.Context(), id, userId, req)
	if err != nil {
		if errors.Is(err, models.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": duplicateChannelMessage(updatedChannel(channel, req))})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update notification channel"})
		return
	}

	c.JSON(http.StatusOK, updated)
}

// updatedChannel resolves which channel type an update leaves the row on.
func updatedChannel(current *models.NotificationChannel, req models.UpdateNotificationChannelRequest) models.AlertChannel {
	if req.Channel != nil {
		return *req.Channel
	}
	return current.Channel
}

func (h *Handlers) DeleteNotificationChannel(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id := c.Param("id")

	if err := h.store.DeleteNotificationChannel(c.Request.Context(), id, userId); err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Notification channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete notification channel"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}

// ── Per-monitor channel overrides ─────────────────────────────────────────────

// monitorChannelOwner resolves whose global channels apply to the monitor and
// verifies the requesting user can access it. Channels are per-user; an org
// monitor uses the org owner's channels, mirroring planForOrg.
func (h *Handlers) monitorChannelOwner(ctx context.Context, monitorID, userId string) (*models.Monitor, string, error) {
	monitor, err := h.store.GetMonitor(ctx, monitorID, userId)
	if err != nil {
		return nil, "", err
	}
	if monitor.OrgID != nil {
		org, err := h.store.GetOrganization(ctx, *monitor.OrgID)
		if err != nil {
			return nil, "", err
		}
		return monitor, org.OwnerID, nil
	}
	return monitor, userId, nil
}

// ListMonitorChannels returns the owner's global channels merged with this
// monitor's overrides: the effective per-monitor state of each channel.
func (h *Handlers) ListMonitorChannels(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")

	_, ownerID, err := h.monitorChannelOwner(c.Request.Context(), monitorID, userId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	channels, err := h.store.ListNotificationChannels(c.Request.Context(), ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list channels"})
		return
	}

	settings, err := h.store.ListMonitorChannelSettings(c.Request.Context(), monitorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list channel settings"})
		return
	}
	overrides := make(map[string]bool, len(settings))
	for _, s := range settings {
		overrides[s.NotificationChannelID] = s.Enabled
	}

	out := make([]models.MonitorChannelState, len(channels))
	for i, ch := range channels {
		state := models.MonitorChannelState{NotificationChannel: ch, EffectiveEnabled: ch.Enabled}
		if enabled, found := overrides[ch.ID]; found {
			state.Overridden = true
			state.EffectiveEnabled = enabled
		}
		out[i] = state
	}

	c.JSON(http.StatusOK, out)
}

// SetMonitorChannel opts the monitor in or out of one global channel,
// overriding the channel's global enabled flag for this monitor only.
func (h *Handlers) SetMonitorChannel(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")
	channelID := c.Param("channelId")

	var req models.SetMonitorChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_, ownerID, err := h.monitorChannelOwner(c.Request.Context(), monitorID, userId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	// The channel must belong to whoever owns the monitor's channels.
	if _, err := h.store.GetNotificationChannel(c.Request.Context(), channelID, ownerID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Notification channel not found"})
		return
	}

	setting, err := h.store.UpsertMonitorChannelSetting(c.Request.Context(), monitorID, channelID, *req.Enabled)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update channel setting"})
		return
	}

	c.JSON(http.StatusOK, setting)
}

// DeleteMonitorChannel removes the per-monitor override so the monitor
// reverts to inheriting the channel's global enabled flag.
func (h *Handlers) DeleteMonitorChannel(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	monitorID := c.Param("id")
	channelID := c.Param("channelId")

	if _, _, err := h.monitorChannelOwner(c.Request.Context(), monitorID, userId); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Monitor not found"})
		return
	}

	if err := h.store.DeleteMonitorChannelSetting(c.Request.Context(), monitorID, channelID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove channel setting"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
