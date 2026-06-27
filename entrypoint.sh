#!/bin/sh
set -e
# Apply committed migrations only. Unlike `db push`, `migrate deploy` never drops
# tables it doesn't manage — critical here because SuperTokens shares this
# database's public schema, and `db push` would try to drop all of its tables.
# Migrations must run against Postgres DIRECTLY, never through a transaction-mode
# pooler like PgBouncer (DDL + advisory locks misbehave under transaction
# pooling). When DIRECT_DATABASE_URL is set, use it for the migration only; the
# application process keeps DATABASE_URL (which may point at PgBouncer).
MIGRATE_URL="${DIRECT_DATABASE_URL:-$DATABASE_URL}"
DATABASE_URL="$MIGRATE_URL" ./prisma-cli migrate deploy --schema ./prisma/schema.prisma
exec "$@"
