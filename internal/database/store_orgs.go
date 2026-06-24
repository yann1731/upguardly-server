package database

import (
	"context"
	"time"

	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

// ── Organization ─────────────────────────────────────────────────────────────

func (s *PrismaStore) CreateOrganization(ctx context.Context, userId, name string) (*models.Organization, error) {
	org, err := s.client.Prisma.Organization.CreateOne(
		db.Organization.Name.Set(name),
		db.Organization.OwnerID.Set(userId),
	).Exec(ctx)
	if err != nil {
		if _, ok := db.IsErrUniqueConstraint(err); ok {
			return nil, models.ErrConflict
		}
		return nil, err
	}

	// Add creator as OWNER member
	_, err = s.client.Prisma.OrganizationMember.CreateOne(
		db.OrganizationMember.Organization.Link(db.Organization.ID.Equals(org.ID)),
		db.OrganizationMember.UserID.Set(userId),
		db.OrganizationMember.Role.Set(db.OrgRoleOwner),
	).Exec(ctx)
	if err != nil {
		// Best-effort rollback
		_, _ = s.client.Prisma.Organization.FindUnique(db.Organization.ID.Equals(org.ID)).Delete().Exec(ctx)
		// A unique violation here means the user already belongs to an org.
		if _, ok := db.IsErrUniqueConstraint(err); ok {
			return nil, models.ErrConflict
		}
		return nil, err
	}

	return orgToModel(org), nil
}

func (s *PrismaStore) GetOrganization(ctx context.Context, id string) (*models.Organization, error) {
	org, err := s.client.Prisma.Organization.FindUnique(
		db.Organization.ID.Equals(id),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return orgToModel(org), nil
}

func (s *PrismaStore) ListOrganizations(ctx context.Context, userId string) ([]models.Organization, error) {
	orgs, err := s.client.Prisma.Organization.FindMany(
		db.Organization.Members.Some(
			db.OrganizationMember.UserID.Equals(userId),
		),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.Organization, len(orgs))
	for i := range orgs {
		out[i] = *orgToModel(&orgs[i])
	}
	return out, nil
}

func (s *PrismaStore) UpdateOrganization(ctx context.Context, id string, req models.UpdateOrgRequest) (*models.Organization, error) {
	if req.Name == nil {
		return nil, models.ErrNotFound
	}
	org, err := s.client.Prisma.Organization.FindUnique(
		db.Organization.ID.Equals(id),
	).Update(
		db.Organization.Name.Set(*req.Name),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return orgToModel(org), nil
}

func (s *PrismaStore) DeleteOrganization(ctx context.Context, id string) error {
	_, err := s.client.Prisma.Organization.FindUnique(
		db.Organization.ID.Equals(id),
	).Delete().Exec(ctx)
	return err
}

// ── OrganizationMember ───────────────────────────────────────────────────────

func (s *PrismaStore) GetMembership(ctx context.Context, orgId, userId string) (*models.OrganizationMember, error) {
	m, err := s.client.Prisma.OrganizationMember.FindFirst(
		db.OrganizationMember.OrganizationID.Equals(orgId),
		db.OrganizationMember.UserID.Equals(userId),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return memberToModel(m), nil
}

func (s *PrismaStore) ListMembers(ctx context.Context, orgId string) ([]models.OrganizationMember, error) {
	ms, err := s.client.Prisma.OrganizationMember.FindMany(
		db.OrganizationMember.OrganizationID.Equals(orgId),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.OrganizationMember, len(ms))
	for i := range ms {
		out[i] = *memberToModel(&ms[i])
	}
	return out, nil
}

func (s *PrismaStore) UpdateMemberRole(ctx context.Context, orgId, userId string, role models.OrgRole) (*models.OrganizationMember, error) {
	existing, err := s.client.Prisma.OrganizationMember.FindFirst(
		db.OrganizationMember.OrganizationID.Equals(orgId),
		db.OrganizationMember.UserID.Equals(userId),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	m, err := s.client.Prisma.OrganizationMember.FindUnique(
		db.OrganizationMember.ID.Equals(existing.ID),
	).Update(
		db.OrganizationMember.Role.Set(db.OrgRole(role)),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return memberToModel(m), nil
}

func (s *PrismaStore) RemoveMember(ctx context.Context, orgId, userId string) error {
	m, err := s.client.Prisma.OrganizationMember.FindFirst(
		db.OrganizationMember.OrganizationID.Equals(orgId),
		db.OrganizationMember.UserID.Equals(userId),
	).Exec(ctx)
	if err != nil {
		return models.ErrNotFound
	}
	_, err = s.client.Prisma.OrganizationMember.FindUnique(
		db.OrganizationMember.ID.Equals(m.ID),
	).Delete().Exec(ctx)
	return err
}

// ── Invitation ───────────────────────────────────────────────────────────────

func (s *PrismaStore) CreateInvitation(ctx context.Context, orgId, email, invitedBy string, role models.OrgRole, token string, expiresAt time.Time) (*models.Invitation, error) {
	inv, err := s.client.Prisma.Invitation.CreateOne(
		db.Invitation.Organization.Link(db.Organization.ID.Equals(orgId)),
		db.Invitation.Email.Set(email),
		db.Invitation.Token.Set(token),
		db.Invitation.InvitedBy.Set(invitedBy),
		db.Invitation.ExpiresAt.Set(expiresAt),
		db.Invitation.Role.Set(db.OrgRole(role)),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return invitationToModel(inv), nil
}

func (s *PrismaStore) GetInvitationByToken(ctx context.Context, token string) (*models.Invitation, error) {
	inv, err := s.client.Prisma.Invitation.FindUnique(
		db.Invitation.Token.Equals(token),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return invitationToModel(inv), nil
}

func (s *PrismaStore) GetInvitationByID(ctx context.Context, id string) (*models.Invitation, error) {
	inv, err := s.client.Prisma.Invitation.FindUnique(
		db.Invitation.ID.Equals(id),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return invitationToModel(inv), nil
}

func (s *PrismaStore) ListInvitations(ctx context.Context, orgId string) ([]models.Invitation, error) {
	invs, err := s.client.Prisma.Invitation.FindMany(
		db.Invitation.OrganizationID.Equals(orgId),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.Invitation, len(invs))
	for i := range invs {
		out[i] = *invitationToModel(&invs[i])
	}
	return out, nil
}

func (s *PrismaStore) AcceptInvitation(ctx context.Context, token, userId string) (*models.OrganizationMember, error) {
	inv, err := s.client.Prisma.Invitation.FindUnique(
		db.Invitation.Token.Equals(token),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}

	// Create the membership first so a failure (e.g. the user already belongs to
	// an org) leaves the invitation PENDING rather than consuming it.
	m, err := s.client.Prisma.OrganizationMember.CreateOne(
		db.OrganizationMember.Organization.Link(db.Organization.ID.Equals(inv.OrganizationID)),
		db.OrganizationMember.UserID.Set(userId),
		db.OrganizationMember.Role.Set(inv.Role),
	).Exec(ctx)
	if err != nil {
		if _, ok := db.IsErrUniqueConstraint(err); ok {
			return nil, models.ErrConflict
		}
		return nil, err
	}

	_, err = s.client.Prisma.Invitation.FindUnique(
		db.Invitation.ID.Equals(inv.ID),
	).Update(
		db.Invitation.Status.Set(db.InvitationStatusAccepted),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}

	return memberToModel(m), nil
}

func (s *PrismaStore) RevokeInvitation(ctx context.Context, id string) error {
	_, err := s.client.Prisma.Invitation.FindUnique(
		db.Invitation.ID.Equals(id),
	).Update(
		db.Invitation.Status.Set(db.InvitationStatusRevoked),
	).Exec(ctx)
	return err
}

// ── Subscription ─────────────────────────────────────────────────────────────

func (s *PrismaStore) GetSubscriptionByUser(ctx context.Context, userId string) (*models.Subscription, error) {
	sub, err := s.client.Prisma.Subscription.FindUnique(
		db.Subscription.UserID.Equals(userId),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return subscriptionToModel(sub), nil
}

func (s *PrismaStore) UpsertSubscription(ctx context.Context, params models.UpsertSubscriptionParams) (*models.Subscription, error) {
	setParams := []db.SubscriptionSetParam{
		db.Subscription.Plan.Set(db.SubscriptionPlan(params.Plan)),
		db.Subscription.Status.Set(db.SubscriptionStatus(params.Status)),
	}
	if params.StripeCustomerID != nil {
		setParams = append(setParams, db.Subscription.StripeCustomerID.Set(*params.StripeCustomerID))
	}
	if params.StripeSubscriptionID != nil {
		setParams = append(setParams, db.Subscription.StripeSubscriptionID.Set(*params.StripeSubscriptionID))
	}
	if params.StripePriceID != nil {
		setParams = append(setParams, db.Subscription.StripePriceID.Set(*params.StripePriceID))
	}
	if params.CurrentPeriodStart != nil {
		setParams = append(setParams, db.Subscription.CurrentPeriodStart.Set(*params.CurrentPeriodStart))
	}
	if params.CurrentPeriodEnd != nil {
		setParams = append(setParams, db.Subscription.CurrentPeriodEnd.Set(*params.CurrentPeriodEnd))
	}

	existing, err := s.client.Prisma.Subscription.FindUnique(
		db.Subscription.UserID.Equals(params.UserID),
	).Exec(ctx)

	if err != nil {
		// Create
		createParams := append(
			[]db.SubscriptionSetParam{},
			db.Subscription.Plan.Set(db.SubscriptionPlan(params.Plan)),
			db.Subscription.Status.Set(db.SubscriptionStatus(params.Status)),
		)
		if params.StripeCustomerID != nil {
			createParams = append(createParams, db.Subscription.StripeCustomerID.Set(*params.StripeCustomerID))
		}
		if params.StripeSubscriptionID != nil {
			createParams = append(createParams, db.Subscription.StripeSubscriptionID.Set(*params.StripeSubscriptionID))
		}
		if params.StripePriceID != nil {
			createParams = append(createParams, db.Subscription.StripePriceID.Set(*params.StripePriceID))
		}
		if params.CurrentPeriodStart != nil {
			createParams = append(createParams, db.Subscription.CurrentPeriodStart.Set(*params.CurrentPeriodStart))
		}
		if params.CurrentPeriodEnd != nil {
			createParams = append(createParams, db.Subscription.CurrentPeriodEnd.Set(*params.CurrentPeriodEnd))
		}

		sub, createErr := s.client.Prisma.Subscription.CreateOne(
			db.Subscription.UserID.Set(params.UserID),
			createParams...,
		).Exec(ctx)
		if createErr != nil {
			return nil, createErr
		}
		return subscriptionToModel(sub), nil
	}

	// Update
	sub, err := s.client.Prisma.Subscription.FindUnique(
		db.Subscription.ID.Equals(existing.ID),
	).Update(setParams...).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return subscriptionToModel(sub), nil
}

// ── Conversion helpers ────────────────────────────────────────────────────────

func orgToModel(o *db.OrganizationModel) *models.Organization {
	return &models.Organization{
		ID:        o.ID,
		Name:      o.Name,
		OwnerID:   o.OwnerID,
		CreatedAt: o.CreatedAt,
		UpdatedAt: o.UpdatedAt,
	}
}

func memberToModel(m *db.OrganizationMemberModel) *models.OrganizationMember {
	return &models.OrganizationMember{
		ID:        m.ID,
		OrgID:     m.OrganizationID,
		UserID:    m.UserID,
		Role:      models.OrgRole(m.Role),
		CreatedAt: m.CreatedAt,
	}
}

func invitationToModel(i *db.InvitationModel) *models.Invitation {
	return &models.Invitation{
		ID:        i.ID,
		OrgID:     i.OrganizationID,
		Email:     i.Email,
		Role:      models.OrgRole(i.Role),
		Token:     i.Token,
		Status:    string(i.Status),
		InvitedBy: i.InvitedBy,
		ExpiresAt: i.ExpiresAt,
		CreatedAt: i.CreatedAt,
	}
}

func subscriptionToModel(s *db.SubscriptionModel) *models.Subscription {
	sub := &models.Subscription{
		ID:        s.ID,
		UserID:    s.UserID,
		Plan:      string(s.Plan),
		Status:    string(s.Status),
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
	if v, ok := s.StripeCustomerID(); ok {
		sub.StripeCustomerID = &v
	}
	if v, ok := s.StripeSubscriptionID(); ok {
		sub.StripeSubscriptionID = &v
	}
	if v, ok := s.StripePriceID(); ok {
		sub.StripePriceID = &v
	}
	if v, ok := s.CurrentPeriodStart(); ok {
		sub.CurrentPeriodStart = &v
	}
	if v, ok := s.CurrentPeriodEnd(); ok {
		sub.CurrentPeriodEnd = &v
	}
	return sub
}
