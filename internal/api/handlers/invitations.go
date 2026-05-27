package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

func (h *Handlers) CreateInvitation(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	callerId, _ := middleware.GetUserID(c)

	var req models.InviteMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, err := generateToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate invitation token"})
		return
	}

	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	inv, err := h.store.CreateInvitation(c.Request.Context(), orgId, req.Email, callerId, req.Role, token, expiresAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invitation"})
		return
	}

	// Send invitation email (non-blocking failure — log but don't fail the request)
	org, _ := h.store.GetOrganization(c.Request.Context(), orgId)
	if org != nil && h.mailer != nil {
		websiteDomain := os.Getenv("WEBSITE_DOMAIN")
		if websiteDomain == "" {
			websiteDomain = "http://localhost:3000"
		}
		acceptURL := fmt.Sprintf("%s/invitations/%s", websiteDomain, token)
		_ = h.mailer.SendInvitation(req.Email, org.Name, callerId, acceptURL)
	}

	// Never expose the token in list responses; only return it on creation
	c.JSON(http.StatusCreated, inv)
}

func (h *Handlers) ListInvitations(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	invs, err := h.store.ListInvitations(c.Request.Context(), orgId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list invitations"})
		return
	}

	// Strip token from list response
	for i := range invs {
		invs[i].Token = ""
	}

	c.JSON(http.StatusOK, invs)
}

func (h *Handlers) RevokeInvitation(c *gin.Context) {
	orgId, ok := middleware.GetOrgID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
		return
	}

	invId := c.Param("invId")

	inv, err := h.store.GetInvitationByID(c.Request.Context(), invId)
	if err != nil || inv.OrgID != orgId {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invitation not found"})
		return
	}

	if err := h.store.RevokeInvitation(c.Request.Context(), invId); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke invitation"})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}

// AcceptInvitation is a protected endpoint: the caller must be authenticated.
func (h *Handlers) AcceptInvitation(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	token := c.Param("token")

	inv, err := h.store.GetInvitationByToken(c.Request.Context(), token)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invitation not found"})
		return
	}

	if inv.Status != "PENDING" {
		c.JSON(http.StatusConflict, gin.H{"error": "Invitation is no longer valid"})
		return
	}

	if time.Now().After(inv.ExpiresAt) {
		c.JSON(http.StatusConflict, gin.H{"error": "Invitation has expired"})
		return
	}

	member, err := h.store.AcceptInvitation(c.Request.Context(), token, userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to accept invitation"})
		return
	}

	c.JSON(http.StatusOK, member)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
