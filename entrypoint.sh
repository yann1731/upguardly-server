#!/bin/sh
set -e
# Apply committed migrations with the embedded bun migrator (cmd/migrate). Like
# the retired `prisma migrate deploy`, it is forward-only and never drops tables
# it doesn't manage — critical here because SuperTokens shares this database's
# public schema. Migrations must run against Postgres DIRECTLY, never through a
# transaction-mode pooler like PgBouncer (DDL + advisory locks misbehave under
# transaction pooling): cmd/migrate prefers DIRECT_DATABASE_URL over DATABASE_URL
# for exactly this reason, while the application process keeps DATABASE_URL
# (which may point at PgBouncer).
# RUN_MIGRATIONS gates this so migrations run from exactly ONE place. In
# production a dedicated one-shot `migrate` service sets RUN_MIGRATIONS=true while
# server/scheduler set it to false and wait for that service to finish — otherwise
# every API replica AND the scheduler would each migrate on boot and race on the
# migrator's advisory lock. Defaults to true so single-instance stacks (local
# prod-like, standalone) keep auto-migrating with no extra config.
if [ "${RUN_MIGRATIONS:-true}" = "true" ]; then
  ./migrate
fi
exec "$@"
