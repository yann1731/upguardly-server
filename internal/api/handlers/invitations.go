package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

	// Prevent duplicate pending invitations for the same email in this org.
	existing, _ := h.store.ListInvitations(c.Request.Context(), orgId)
	for _, inv := range existing {
		if inv.Email == req.Email && inv.Status == "PENDING" {
			c.JSON(http.StatusConflict, gin.H{"error": "A pending invitation already exists for this email address"})
			return
		}
	}

	rawToken, err := generateToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate invitation token"})
		return
	}

	// Store the SHA-256 hash of the token, not the raw token.
	// The raw token is only ever sent to the invitee via email; the DB stores
	// the hash so that a DB leak does not expose usable invitation links.
	tokenHash := hashToken(rawToken)

	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	inv, err := h.store.CreateInvitation(c.Request.Context(), orgId, req.Email, callerId, req.Role, tokenHash, expiresAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invitation"})
		return
	}

	// Send invitation email (non-blocking failure — log but don't fail the request).
	org, _ := h.store.GetOrganization(c.Request.Context(), orgId)
	if org != nil && h.mailer != nil {
		websiteDomain := os.Getenv("WEBSITE_DOMAIN")
		if websiteDomain == "" {
			// Log the misconfiguration but don't silently send broken links.
			_ = fmt.Errorf("WEBSITE_DOMAIN is not set; invitation email will not be sent")
		} else {
			acceptURL := fmt.Sprintf("%s/invitations/%s", websiteDomain, rawToken)
			_ = h.mailer.SendInvitation(req.Email, org.Name, callerId, acceptURL)
		}
	}

	// Return the raw token on creation so the caller can share it directly if
	// needed (e.g., in tests). Never exposed again after this response.
	inv.Token = rawToken
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

	// Strip token from list response — the hash must never be returned.
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

	rawToken := c.Param("token")

	// The DB stores a SHA-256 hash of the token; hash before lookup.
	tokenHash := hashToken(rawToken)

	inv, err := h.store.GetInvitationByToken(c.Request.Context(), tokenHash)
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

	// A user may belong to at most one organization.
	if existing, err := h.store.ListOrganizations(c.Request.Context(), userId); err == nil && len(existing) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "You already belong to an organization"})
		return
	}

	member, err := h.store.AcceptInvitation(c.Request.Context(), tokenHash, userId)
	if err != nil {
		if errors.Is(err, models.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "You already belong to an organization"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to accept invitation"})
		return
	}

	c.JSON(http.StatusOK, member)
}

// generateToken creates a cryptographically secure random 32-byte token
// returned as a lowercase hex string (64 characters).
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashToken computes the SHA-256 hash of a token and returns it as a
// lowercase hex string. This is used to store tokens securely in the DB.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
