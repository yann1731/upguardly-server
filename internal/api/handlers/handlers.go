package handlers

import "upguardly-backend/internal/models"

type Handlers struct {
	store models.Store
}

func NewHandlers(store models.Store) *Handlers {
	return &Handlers{store: store}
}
