package handlers_test

import (
	"strings"
	"time"

	"upguardly-backend/internal/models"
)

func jsonReader(s string) *strings.Reader {
	return strings.NewReader(s)
}

// fixture helpers

func aMonitor() *models.Monitor {
	return &models.Monitor{
		ID:        "mon-1",
		Name:      "My Monitor",
		Type:      models.MonitorTypeHTTP,
		Target:    "https://example.com",
		Interval:  60,
		Timeout:   30,
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func anAlert() *models.Alert {
	return &models.Alert{
		ID:        "alert-1",
		MonitorID: "mon-1",
		Channel:   models.AlertChannelEMAIL,
		Target:    "user@example.com",
		Enabled:   true,
		CreatedAt: time.Now(),
	}
}

func aMembership() *models.OrganizationMember {
	return &models.OrganizationMember{
		ID:        "member-1",
		OrgID:     "test-org-id",
		UserID:    "test-user-id",
		Role:      models.OrgRoleMember,
		CreatedAt: time.Now(),
	}
}

func aSubscription(plan string) *models.Subscription {
	return &models.Subscription{
		ID:     "sub-1",
		UserID: testUserID,
		Plan:   plan,
		Status: "ACTIVE",
	}
}
