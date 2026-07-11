package bun

import (
	"database/sql"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

type Client struct {
	DB *bun.DB
}

func NewClient(dsn string) *Client {
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))

	// Configure connection pooling
	sqldb.SetMaxOpenConns(20)
	sqldb.SetMaxIdleConns(10)
	sqldb.SetConnMaxLifetime(30 * time.Minute)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)

	db := bun.NewDB(sqldb, pgdialect.New())
	return &Client{DB: db}
}

func (c *Client) Connect() error {
	return c.DB.Ping()
}

func (c *Client) Disconnect() error {
	return c.DB.Close()
}

type BunStore struct {
	client *Client
}

func NewBunStore(client *Client) *BunStore {
	return &BunStore{client: client}
}
