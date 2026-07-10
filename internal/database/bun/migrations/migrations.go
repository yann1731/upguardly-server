// Package migrations holds the database schema migrations, applied with bun's
// migrate framework (cmd/migrate). Each migration is a transactional SQL file
// named <timestamp>_<name>.tx.up.sql and embedded into the binary, so the
// migrate command is self-contained — no external CLI or schema file.
//
// These files were ported verbatim from the retired prisma migration history
// (they are plain SQL). BaselineCutoff marks the last migration that predates
// the prisma→bun switch: on a database prisma already migrated, cmd/migrate
// records every migration up to and including the cutoff as applied without
// re-running it (see cmd/migrate/main.go).
package migrations

import (
	"embed"

	"github.com/uptrace/bun/migrate"
)

//go:embed *.up.sql
var sqlMigrations embed.FS

// Migrations is the discovered set of schema migrations, in filename order.
var Migrations = migrate.NewMigrations()

// BaselineCutoff is the timestamp of the final migration applied under prisma
// before the switch to bun migrate (20260703120000_add_regions). bun stores a
// migration's 14-digit timestamp as its Name, so this is just that prefix.
// Migrations at or before it already ran on existing databases and must be
// baselined (marked applied, not re-run) there.
const BaselineCutoff = "20260703120000"

func init() {
	if err := Migrations.Discover(sqlMigrations); err != nil {
		panic(err)
	}
}
