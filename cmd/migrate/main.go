// Command migrate applies the embedded bun schema migrations
// (internal/database/bun/migrations) and exits. It replaces the retired
// `prisma migrate deploy` step: entrypoint.sh runs it before starting the
// server/scheduler, gated by RUN_MIGRATIONS so exactly one process migrates.
//
// Migrations must run against Postgres directly, never through a
// transaction-mode pooler like PgBouncer (DDL and advisory locks misbehave
// under transaction pooling), so DIRECT_DATABASE_URL takes precedence over
// DATABASE_URL when set.
//
// Baseline: on a database that prisma already migrated (its schema exists but
// bun's bookkeeping table is empty), every migration up to migrations.BaselineCutoff
// is recorded as applied without re-running it, so only the post-switch
// migrations execute. A fresh database runs the whole history from zero.
package main

import (
	"context"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/uptrace/bun/migrate"

	"upguardly-backend/internal/config"
	bundb "upguardly-backend/internal/database/bun"
	"upguardly-backend/internal/database/bun/migrations"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("[INFO] no .env file loaded (%v); relying on process environment", err)
	}

	dsn := os.Getenv("DIRECT_DATABASE_URL")
	if dsn == "" {
		dsn = config.Load().DatabaseURL
	}

	db := bundb.NewClient(dsn)
	if err := db.Connect(); err != nil {
		log.Fatalf("migrate: failed to connect: %v", err)
	}
	defer db.Disconnect()

	ctx := context.Background()
	migrator := migrate.NewMigrator(db.DB, migrations.Migrations)

	if err := migrator.Init(ctx); err != nil {
		log.Fatalf("migrate: init: %v", err)
	}

	if err := migrator.Lock(ctx); err != nil {
		log.Fatalf("migrate: lock: %v", err)
	}
	defer migrator.Unlock(ctx) //nolint:errcheck

	if err := baselineIfNeeded(ctx, migrator); err != nil {
		log.Fatalf("migrate: baseline: %v", err)
	}

	group, err := migrator.Migrate(ctx)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if group.IsZero() {
		log.Printf("migrate: no new migrations to run")
		return
	}
	log.Printf("migrate: applied %d migration(s) in group #%d", len(group.Migrations), group.ID)
}

// baselineIfNeeded records migrations up to the prisma→bun cutoff as applied,
// without running them, when the database already carries the pre-switch schema
// but has no bun migration history yet. It is a no-op on a fresh database (no
// schema) and on any database bun has already migrated (history present).
func baselineIfNeeded(ctx context.Context, migrator *migrate.Migrator) error {
	applied, err := migrator.AppliedMigrations(ctx)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		return nil // bun already owns this database's history
	}

	// "Schema already exists" invariant: the monitors table is present. If it
	// is not, this is a fresh database and the full history should run.
	var exists bool
	if err := migrator.DB().NewRaw(
		`SELECT to_regclass('public.monitors') IS NOT NULL`,
	).Scan(ctx, &exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}

	log.Printf("migrate: existing pre-bun schema detected; baselining migrations through %s", migrations.BaselineCutoff)
	for i := range migrations.Migrations.Sorted() {
		m := migrations.Migrations.Sorted()[i]
		if m.Name > migrations.BaselineCutoff {
			continue
		}
		m.GroupID = 1
		if err := migrator.MarkApplied(ctx, &m); err != nil {
			return err
		}
	}
	return nil
}
