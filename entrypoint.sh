#!/bin/sh
set -e
# Apply committed migrations only. Unlike `db push`, `migrate deploy` never drops
# tables it doesn't manage — critical here because SuperTokens shares this
# database's public schema, and `db push` would try to drop all of its tables.
./prisma-cli migrate deploy --schema ./prisma/schema.prisma
exec "$@"
