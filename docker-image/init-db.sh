#!/bin/sh
# Runs once, on the database's first start. Creates two users:
#   calendar_owner  runs migrations (MIGRATION_DATABASE_URL)
#   calendar_app    used by the API; can read/write data but not change tables or the audit log
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v owner_password="$CALENDAR_OWNER_PASSWORD" -v app_password="$CALENDAR_APP_PASSWORD" <<'SQL'
CREATE ROLE calendar_owner LOGIN PASSWORD :'owner_password';
CREATE ROLE calendar_app LOGIN PASSWORD :'app_password' CONNECTION LIMIT 200;
REVOKE CONNECT ON DATABASE calendar FROM PUBLIC;
GRANT CONNECT ON DATABASE calendar TO calendar_owner, calendar_app;
GRANT CREATE ON DATABASE calendar TO calendar_owner;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO calendar_owner;
GRANT USAGE ON SCHEMA public TO calendar_app;
ALTER DEFAULT PRIVILEGES FOR ROLE calendar_owner IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO calendar_app;
ALTER DEFAULT PRIVILEGES FOR ROLE calendar_owner IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO calendar_app;
ALTER DEFAULT PRIVILEGES FOR ROLE calendar_owner IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO calendar_app;
SQL
