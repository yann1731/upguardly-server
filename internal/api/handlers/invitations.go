package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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

	// Emails are compared case-insensitively everywhere (the accept check
	// included), so store them normalized.
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Prevent duplicate pending invitations for the same email in this org. A
	// pending invitation whose expiry has passed no longer blocks: it is marked
	// EXPIRED and superseded by the new one — this is how an invite is resent.
	existing, _ := h.store.ListInvitations(c.Request.Context(), orgId)
	for _, inv := range existing {
		if inv.Status != "PENDING" || !strings.EqualFold(inv.Email, email) {
			continue
		}
		if time.Now().Before(inv.ExpiresAt) {
			c.JSON(http.StatusConflict, gin.H{"error": "A pending invitation already exists for this email address"})
			return
		}
		if err := h.store.ExpireInvitation(c.Request.Context(), inv.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invitation"})
			return
		}
	}

	// Login seats: the owner is free; every other member and every pending
	// non-expired invitation holds a seat. This is the user-facing check —
	// AcceptInvitation re-checks transactionally in case of races.
	limits := models.LimitsForPlan(h.planForOrg(c.Request.Context(), orgId))
	if limits.MaxLoginSeats != models.Unlimited {
		members, err := h.store.CountNonOwnerMembers(c.Request.Context(), orgId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check seat quota"})
			return
		}
		pending, err := h.store.CountPendingInvitations(c.Request.Context(), orgId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check seat quota"})
			return
		}
		if members+pending >= limits.MaxLoginSeats {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": fmt.Sprintf("Login seat limit reached for your plan (%d). Revoke a pending invitation or remove a member to free a seat.", limits.MaxLoginSeats),
			})
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

	inv, err := h.store.CreateInvitation(c.Request.Context(), orgId, email, callerId, req.Role, tokenHash, expiresAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invitation"})
		return
	}

	// Send invitation email (non-blocking failure — log but don't fail the request).
	org, _ := h.store.GetOrganization(c.Request.Context(), orgId)
	if org != nil && h.mailer != nil {
		websiteDomain := os.Getenv("WEBSITE_DOMAIN")
		if websiteDomain == "" {
			// Don't send a broken link; the invitation still exists and can be
			// resent once the misconfiguration is fixed.
			log.Printf("[WARN] invitations: WEBSITE_DOMAIN is not set; email for invitation %s not sent", inv.ID)
		} else {
			acceptURL := fmt.Sprintf("%s/invitations/%s", websiteDomain, rawToken)
			if err := h.mailer.SendInvitation(email, org.Name, h.inviterName(callerId), acceptURL); err != nil {
				log.Printf("[WARN] invitations: sending email for invitation %s failed: %v", inv.ID, err)
			}
		}
	}

	// Return the raw token on creation so the caller can share it directly if
	// needed (e.g., in tests). Never exposed again after this response.
	inv.Token = rawToken
	c.JSON(http.StatusCreated, inv)
}

// inviterName is how the invitation email names the inviter. SuperTokens only
// knows the account email; fall back to a neutral phrase rather than putting
// the internal user ID in front of the invitee.
func (h *Handlers) inviterName(userID string) string {
	if email, err := h.UserEmailLookup(userID); err == nil && email != "" {
		return email
	}
	return "A teammate"
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

// GetInvitationPreview is public: the invite page renders it before the
// invitee has signed in, so they know which org, role and email the link is
// for — and which account to sign in or register with. The token (256 random
// bits, mailed to the invitee) is the only credential; only its hash is
// looked up, and the response carries no token or IDs.
func (h *Handlers) GetInvitationPreview(c *gin.Context) {
	inv, err := h.store.GetInvitationByToken(c.Request.Context(), hashToken(c.Param("token")))
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Invitation not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load invitation"})
		return
	}

	org, err := h.store.GetOrganization(c.Request.Context(), inv.OrgID)
	if err != nil || org == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invitation not found"})
		return
	}

	// Nothing expires invitations on a schedule, so report a lapsed PENDING
	// row the way the accept endpoint will treat it.
	status := inv.Status
	if status == "PENDING" && time.Now().After(inv.ExpiresAt) {
		status = "EXPIRED"
	}

	c.JSON(http.StatusOK, models.InvitationPreview{
		OrgName:   org.Name,
		Email:     inv.Email,
		Role:      inv.Role,
		Status:    status,
		ExpiresAt: inv.ExpiresAt,
	})
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

	// An invitation is addressed to one email; only the account holding that
	// email may accept it. Otherwise anyone the link reaches — a forwarded
	// email, a shared screen — could join the org.
	accountEmail, err := h.UserEmailLookup(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify your account email"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(accountEmail), strings.TrimSpace(inv.Email)) {
		c.JSON(http.StatusForbidden, gin.H{
			"error":        "This invitation was sent to a different email address",
			"code":         "email_mismatch",
			"invitedEmail": inv.Email,
		})
		return
	}

	// A user may belong to at most one organization.
	if existing, err := h.store.ListOrganizations(c.Request.Context(), userId); err == nil && len(existing) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "You already belong to an organization"})
		return
	}

	maxSeats := models.LimitsForPlan(h.planForOrg(c.Request.Context(), inv.OrgID)).MaxLoginSeats
	member, err := h.store.AcceptInvitation(c.Request.Context(), tokenHash, userId, maxSeats)
	if err != nil {
		if errors.Is(err, models.ErrSeatLimit) {
			c.JSON(http.StatusConflict, gin.H{"error": "This organization has no available seats"})
			return
		}
		if errors.Is(err, models.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "You already belong to an organization"})
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			// Revoked or expired between the check above and the transaction.
			c.JSON(http.StatusConflict, gin.H{"error": "Invitation is no longer valid"})
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
