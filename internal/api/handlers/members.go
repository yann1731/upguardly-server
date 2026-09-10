package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

func (h *Handlers) ListMembers(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	members, err := h.store.ListMembers(c.Request.Context(), orgId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list members"})
		return
	}

	// SuperTokens owns user records, so emails are resolved per member. Org
	// size is capped by the plan's login seats, which keeps this N small. A
	// failed lookup leaves the email blank rather than failing the list.
	for i := range members {
		email, err := h.UserEmailLookup(members[i].UserID)
		if err != nil {
			log.Printf("[WARN] members: email lookup failed for user %s: %v", members[i].UserID, err)
			continue
		}
		members[i].Email = email
	}

	c.JSON(http.StatusOK, members)
}

func (h *Handlers) UpdateMemberRole(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	targetUserId := c.Param("memberId")

	var req models.UpdateMemberRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Prevent demoting the owner
	existing, err := h.store.GetMembership(c.Request.Context(), orgId, targetUserId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if existing.Role == models.OrgRoleOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot change the owner's role"})
		return
	}

	member, err := h.store.UpdateMemberRole(c.Request.Context(), orgId, targetUserId, req.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update member role"})
		return
	}

	c.JSON(http.StatusOK, member)
}

func (h *Handlers) RemoveMember(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	targetUserId := c.Param("memberId")
	callerId, _ := middleware.GetUserID(c)

	// Prevent removing the owner (unless it's a self-leave by non-owner)
	existing, err := h.store.GetMembership(c.Request.Context(), orgId, targetUserId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if existing.Role == models.OrgRoleOwner && callerId != targetUserId {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot remove the organization owner"})
		return
	}
	if existing.Role == models.OrgRoleOwner && callerId == targetUserId {
		c.JSON(http.StatusForbidden, gin.H{"error": "Transfer ownership before leaving"})
		return
	}

	if err := h.store.RemoveMember(c.Request.Context(), orgId, targetUserId); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove member"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
