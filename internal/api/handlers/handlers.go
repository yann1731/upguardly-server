package handlers

import (
	"upguardly-backend/internal/database"
)

type Handlers struct {
	db *database.Client
}

func NewHandlers(db *database.Client) *Handlers {
	return &Handlers{
		db: db,
	}
}
