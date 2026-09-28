#!/bin/sh
# First-start hook for the bundled database (profile bundled-db): creates the least-privilege
# roles from deploy/postgres/roles.sql with passwords from the environment.
set -eu
: "${CALENDAR_OWNER_PASSWORD:?set CALENDAR_OWNER_PASSWORD}"
: "${CALENDAR_APP_PASSWORD:?set CALENDAR_APP_PASSWORD}"
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v owner_password="$CALENDAR_OWNER_PASSWORD" -v app_password="$CALENDAR_APP_PASSWORD" \
  -f /docker-entrypoint-initdb.d/roles.sql.in
