package database

import (
	db "upguardly-backend/internal/database/prisma"
)

type Client struct {
	Prisma *db.PrismaClient
}

func NewClient() *Client {
	client := db.NewClient()
	return &Client{
		Prisma: client,
	}
}

func (c *Client) Connect() error {
	return c.Prisma.Connect()
}

func (c *Client) Disconnect() error {
	return c.Prisma.Disconnect()
}
