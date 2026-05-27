package handlers

import (
	"upguardly-backend/internal/mailer"
	"upguardly-backend/internal/models"
	"upguardly-backend/internal/stripeservice"
)

type Handlers struct {
	store  models.Store
	mailer *mailer.Mailer
	stripe *stripeservice.Client
}

func NewHandlers(store models.Store, m *mailer.Mailer, s *stripeservice.Client) *Handlers {
	return &Handlers{store: store, mailer: m, stripe: s}
}
