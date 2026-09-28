-- Tighten the application role created by deploy/postgres/roles.sql (no-op when it does not exist,
-- for example in development where one role does everything).

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'calendar_app') THEN
    -- The audit trail is append-only for the application.
    REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM calendar_app;
    -- The application only reads the migration table (store.CheckSchema) and never migrates.
    REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON goose_db_version FROM calendar_app;
    GRANT SELECT ON goose_db_version TO calendar_app;
    -- Immutable published snapshots: insert only.
    REVOKE UPDATE, DELETE, TRUNCATE ON calendar_data_versions FROM calendar_app;
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'calendar_app') THEN
    GRANT UPDATE, DELETE ON audit_log, calendar_data_versions TO calendar_app;
  END IF;
END
$$;
-- +goose StatementEnd
