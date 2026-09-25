-- Least-privilege roles for a production database. Run ONCE, as an administrator, on a new database,
-- BEFORE the first `calendar-api bootstrap`:
--
--   psql "postgres://admin@db-host:5432/calendar?sslmode=require" \
--        -v owner_password="$(openssl rand -base64 32)" \
--        -v app_password="$(openssl rand -base64 32)" \
--        -f deploy/postgres/roles.sql
--
-- Then configure:
--   MIGRATION_DATABASE_URL = postgres://calendar_owner:<owner_password>@db-host:5432/calendar?sslmode=require   (bootstrap job only)
--   DATABASE_URL           = postgres://calendar_app:<app_password>@db-host:5432/calendar?sslmode=require     (api and worker)
--
-- calendar_owner owns the schema and runs migrations. calendar_app can read and write rows but cannot
-- change the schema, and (via migration 00002) cannot UPDATE or DELETE the audit log.

\set ON_ERROR_STOP on

CREATE ROLE calendar_owner LOGIN PASSWORD :'owner_password';
CREATE ROLE calendar_app   LOGIN PASSWORD :'app_password' CONNECTION LIMIT 200;

-- Only these roles (and admins) may connect to this database. CREATE lets the owner run
-- CREATE EXTENSION pgcrypto (a trusted extension) in the first migration.
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', current_database());
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO calendar_owner, calendar_app', current_database());
  EXECUTE format('GRANT CREATE ON DATABASE %I TO calendar_owner', current_database());
END
$$;

-- The owner role owns the schema (so migrations can create and alter objects).
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO calendar_owner;
GRANT USAGE ON SCHEMA public TO calendar_app;

-- Everything the owner creates later is usable, not alterable, by the application.
ALTER DEFAULT PRIVILEGES FOR ROLE calendar_owner IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO calendar_app;
ALTER DEFAULT PRIVILEGES FOR ROLE calendar_owner IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO calendar_app;
ALTER DEFAULT PRIVILEGES FOR ROLE calendar_owner IN SCHEMA public
  GRANT EXECUTE ON FUNCTIONS TO calendar_app;

-- pgcrypto (gen_random_uuid) is created by the first migration; the owner needs this right.
-- On managed Postgres (RDS, Cloud SQL, Azure) pgcrypto is on the allow-list for non-superusers.
