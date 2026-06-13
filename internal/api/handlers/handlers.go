package handlers

import (
	"upguardly-backend/internal/mailer"
	"upguardly-backend/internal/models"
)

type Handlers struct {
	store  models.Store
	mailer *mailer.Mailer
	stripe StripeService
}

func NewHandlers(store models.Store, m *mailer.Mailer, s StripeService) *Handlers {
	return &Handlers{store: store, mailer: m, stripe: s}
}
