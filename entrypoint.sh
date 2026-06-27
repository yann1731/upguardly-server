#!/bin/sh
set -e
# Apply committed migrations only. Unlike `db push`, `migrate deploy` never drops
# tables it doesn't manage — critical here because SuperTokens shares this
# database's public schema, and `db push` would try to drop all of its tables.
# Migrations must run against Postgres DIRECTLY, never through a transaction-mode
# pooler like PgBouncer (DDL + advisory locks misbehave under transaction
# pooling). When DIRECT_DATABASE_URL is set, use it for the migration only; the
# application process keeps DATABASE_URL (which may point at PgBouncer).
# RUN_MIGRATIONS gates this so migrations run from exactly ONE place. In
# production a dedicated one-shot `migrate` service sets RUN_MIGRATIONS=true while
# server/scheduler set it to false and wait for that service to finish — otherwise
# every API replica AND the scheduler would each run `migrate deploy` on boot and
# race on Prisma's advisory lock. Defaults to true so single-instance stacks
# (local prod-like, standalone) keep auto-migrating with no extra config.
if [ "${RUN_MIGRATIONS:-true}" = "true" ]; then
  MIGRATE_URL="${DIRECT_DATABASE_URL:-$DATABASE_URL}"
  DATABASE_URL="$MIGRATE_URL" ./prisma-cli migrate deploy --schema ./prisma/schema.prisma
fi
exec "$@"
