package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// meResponse is the GET /me body: the user's identity plus their account
// context. SuperTokens owns the user record (there is no local user table), so
// the email is resolved through UserEmailLookup. The frontend uses the email to
// drive the password-reset flow, and the account context to tell invited org
// members (whose plan and billing belong to the org owner) apart from
// independent accounts.
type meResponse struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	models.AccountContext
}

func (h *Handlers) GetMe(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	email, err := h.UserEmailLookup(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}

	acct, err := h.accountContext(c.Request.Context(), userId)
	if err != nil {
		log.Printf("me: resolve account context for user %s: %v", userId, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load account"})
		return
	}

	c.JSON(http.StatusOK, meResponse{UserID: userId, Email: email, AccountContext: acct})
}
