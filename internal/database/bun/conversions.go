package bun

import (
	"upguardly-backend/internal/models"
)

func (m *Monitor) toModel() models.Monitor {
	return models.Monitor{
		ID:                        m.ID,
		OrgID:                     m.OrgID,
		Plan:                      m.OwnerPlan,
		Name:                      m.Name,
		Type:                      models.MonitorType(m.Type),
		Target:                    m.Target,
		Interval:                  models.EffectiveInterval(m.Interval, m.OwnerPlan, m.Timeout),
		IntervalIsCustom:          m.Interval != nil,
		Timeout:                   m.Timeout,
		DegradedThresholdMs:       models.EffectiveDegradedThreshold(m.DegradedThresholdMs, models.MonitorType(m.Type)),
		DegradedThresholdIsCustom: m.DegradedThresholdMs != nil,
		RepeatAlertIntervalSecs:   m.RepeatAlertIntervalSecs,
		RepeatAlertMaxCount:       m.RepeatAlertMaxCount,
		CertCheckEnabled:          m.CertCheckEnabled,
		CertExpiryThresholdDays:   m.CertExpiryThresholdDays,
		DomainCheckEnabled:        m.DomainCheckEnabled,
		DomainExpiryThresholdDays: m.DomainExpiryThresholdDays,
		Enabled:                   m.Enabled,
		Regions:                   m.Regions,
		CreatedAt:                 m.CreatedAt,
		UpdatedAt:                 m.UpdatedAt,
	}
}

func (w *MaintenanceWindow) toModel() models.MaintenanceWindow {
	return models.MaintenanceWindow{
		ID:              w.ID,
		MonitorID:       w.MonitorID,
		Kind:            models.MaintenanceWindowKind(w.Kind),
		StartsAt:        w.StartsAt,
		EndsAt:          w.EndsAt,
		Weekday:         w.Weekday,
		StartTime:       w.StartTime,
		DurationMinutes: w.DurationMinutes,
		Timezone:        w.Timezone,
		CreatedAt:       w.CreatedAt,
	}
}

func (nc *NotificationChannel) toModel() models.NotificationChannel {
	return models.NotificationChannel{
		ID:        nc.ID,
		Channel:   models.AlertChannel(nc.Channel),
		Target:    nc.Target,
		Enabled:   nc.Enabled,
		CreatedAt: nc.CreatedAt,
	}
}

func (o *Organization) toModel() models.Organization {
	return models.Organization{
		ID:        o.ID,
		Name:      o.Name,
		OwnerID:   o.OwnerID,
		CreatedAt: o.CreatedAt,
		UpdatedAt: o.UpdatedAt,
	}
}

func (om *OrganizationMember) toModel() models.OrganizationMember {
	return models.OrganizationMember{
		ID:        om.ID,
		OrgID:     om.OrganizationID,
		UserID:    om.UserID,
		Role:      models.OrgRole(om.Role),
		CreatedAt: om.CreatedAt,
	}
}

func (r *OrgAlertRecipient) toModel() models.OrgAlertRecipient {
	return models.OrgAlertRecipient{
		ID:        r.ID,
		OrgID:     r.OrganizationID,
		Channel:   models.AlertChannel(r.Channel),
		Target:    r.Target,
		CreatedAt: r.CreatedAt,
	}
}

func (i *Invitation) toModel() models.Invitation {
	return models.Invitation{
		ID:        i.ID,
		OrgID:     i.OrganizationID,
		Email:     i.Email,
		Role:      models.OrgRole(i.Role),
		Token:     i.Token,
		Status:    i.Status,
		InvitedBy: i.InvitedBy,
		ExpiresAt: i.ExpiresAt,
		CreatedAt: i.CreatedAt,
	}
}

func (s *Subscription) toModel() models.Subscription {
	return models.Subscription{
		ID:                   s.ID,
		UserID:               s.UserID,
		Plan:                 s.Plan,
		Status:               s.Status,
		StripeCustomerID:     s.StripeCustomerID,
		StripeSubscriptionID: s.StripeSubscriptionID,
		StripePriceID:        s.StripePriceID,
		CurrentPeriodStart:   s.CurrentPeriodStart,
		CurrentPeriodEnd:     s.CurrentPeriodEnd,
		CancelAtPeriodEnd:    s.CancelAtPeriodEnd,
		CreatedAt:            s.CreatedAt,
		UpdatedAt:            s.UpdatedAt,
	}
}
