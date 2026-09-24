-- Initial schema for the BS/AD calendar service. See docs/architecture.md §8.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug        text UNIQUE NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{1,40}$'),
  name        text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE admin_users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  email         text NOT NULL UNIQUE CHECK (email = lower(email) AND position('@' in email) > 1),
  password_hash text,
  oidc_sub      text UNIQUE,
  role          text NOT NULL CHECK (role IN ('viewer','editor','designer','calendar_admin','super_admin')),
  disabled_at   timestamptz,
  last_login_at timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- One row per login. The refresh token rotates on every use; the previous hash is kept
-- to detect reuse of a stolen token (which revokes the whole session).
CREATE TABLE admin_sessions (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id           uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  refresh_hash      bytea NOT NULL UNIQUE,
  prev_refresh_hash bytea,
  expires_at        timestamptz NOT NULL,
  revoked_at        timestamptz,
  ip                text,
  user_agent        text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  last_used_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_sessions_prev ON admin_sessions (prev_refresh_hash) WHERE prev_refresh_hash IS NOT NULL;

-- Current BS year table (global, shared by all tenants).
CREATE TABLE calendar_years (
  bs_year     smallint PRIMARY KEY,
  month_days  smallint[] NOT NULL,
  ad_start    date NOT NULL,
  status      text NOT NULL CHECK (status IN ('verified','projected')),
  source      text NOT NULL DEFAULT '',
  updated_by  uuid REFERENCES admin_users(id),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  -- Deferred so a draft can shift several years' starts in one transaction.
  CONSTRAINT calendar_years_ad_start_key UNIQUE (ad_start) DEFERRABLE INITIALLY DEFERRED,
  CHECK (array_length(month_days, 1) = 12),
  CHECK (29 <= ALL (month_days) AND 32 >= ALL (month_days))
);

-- Immutable published snapshots, served at /v1/calendar/data/{version}.
CREATE TABLE calendar_data_versions (
  version     bigint PRIMARY KEY,
  snapshot    jsonb NOT NULL,
  sha256      text NOT NULL,
  draft_id    uuid,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE calendar_year_drafts (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  base_version bigint NOT NULL,
  changes      jsonb NOT NULL,
  warnings     jsonb NOT NULL DEFAULT '[]',
  impact       jsonb NOT NULL DEFAULT '{}',
  state        text NOT NULL CHECK (state IN ('pending','approved','rejected')),
  reason       text,
  created_by   uuid NOT NULL REFERENCES admin_users(id),
  decided_by   uuid REFERENCES admin_users(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  decided_at   timestamptz,
  -- Four-eyes rule: nobody approves their own change.
  CHECK (state <> 'approved' OR decided_by <> created_by)
);

-- Version counters behind GET /v1/manifest.
-- Scopes: 'data', 'categories:<tenant>', 'events:<tenant>:recurring', 'events:<tenant>:y:<bsYear>'.
CREATE TABLE resource_versions (
  scope       text PRIMARY KEY,
  version     bigint NOT NULL,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE categories (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  key         text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{1,40}$'),
  name        jsonb NOT NULL,
  color_light text NOT NULL CHECK (color_light ~ '^#[0-9A-Fa-f]{6}$'),
  color_dark  text NOT NULL CHECK (color_dark ~ '^#[0-9A-Fa-f]{6}$'),
  is_holiday  boolean NOT NULL DEFAULT false,
  sort_order  int NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, key)
);

CREATE TABLE events (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  category_id   uuid NOT NULL REFERENCES categories(id),
  title         jsonb NOT NULL,
  description   jsonb,
  date_basis    text NOT NULL CHECK (date_basis IN ('AD','BS')),
  start_local   text NOT NULL CHECK (start_local ~ '^\d{4}-\d{2}-\d{2}$'),
  end_local     text NOT NULL CHECK (end_local ~ '^\d{4}-\d{2}-\d{2}$'),
  ad_start      date NOT NULL,
  ad_end        date NOT NULL,
  bs_year_start smallint NOT NULL,
  bs_year_end   smallint NOT NULL,
  all_day       boolean NOT NULL DEFAULT true,
  start_time    text CHECK (start_time ~ '^([01]\d|2[0-3]):[0-5]\d$'),
  end_time      text CHECK (end_time ~ '^([01]\d|2[0-3]):[0-5]\d$'),
  tz            text NOT NULL DEFAULT 'Asia/Kathmandu',
  recurrence    text NOT NULL DEFAULT 'none' CHECK (recurrence IN ('none','yearly_bs','yearly_ad','rrule')),
  rrule         text,
  recur_until   date,
  is_holiday    boolean,
  status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
  version       int NOT NULL DEFAULT 1,
  created_by    uuid NOT NULL REFERENCES admin_users(id),
  updated_by    uuid NOT NULL REFERENCES admin_users(id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz,
  CHECK (ad_end >= ad_start),
  CHECK (recurrence <> 'rrule' OR rrule IS NOT NULL),
  CHECK (all_day OR start_time IS NOT NULL)
);
CREATE INDEX events_admin_list ON events (tenant_id, ad_start, id) WHERE deleted_at IS NULL;
CREATE INDEX events_pub_range ON events (tenant_id, ad_start, ad_end)
  WHERE status = 'published' AND deleted_at IS NULL;
CREATE INDEX events_recurring ON events (tenant_id)
  WHERE recurrence <> 'none' AND deleted_at IS NULL;

CREATE TABLE ui_configs (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id),
  app_key            text NOT NULL CHECK (app_key ~ '^[a-z][a-z0-9-]{1,30}$'),
  version            int NOT NULL CHECK (version > 0),
  status             text NOT NULL CHECK (status IN ('draft','in_review','published','superseded','rolled_back','rejected')),
  origin             text NOT NULL DEFAULT 'draft' CHECK (origin IN ('draft','rollback')),
  schema_version     int NOT NULL,
  config             jsonb NOT NULL,
  min_client_version text NOT NULL DEFAULT '0.0.0',
  note               text,
  reject_reason      text,
  created_by         uuid NOT NULL REFERENCES admin_users(id),
  approved_by        uuid REFERENCES admin_users(id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  published_at       timestamptz,
  UNIQUE (tenant_id, app_key, version),
  -- Four-eyes rule, except for emergency rollbacks (which are audited).
  CHECK (origin = 'rollback' OR approved_by IS NULL OR approved_by <> created_by)
);

-- Which version each app receives: a stable version and an optional staged-rollout candidate.
CREATE TABLE ui_config_channels (
  tenant_id         uuid NOT NULL REFERENCES tenants(id),
  app_key           text NOT NULL,
  stable_version    int,
  candidate_version int,
  candidate_percent smallint NOT NULL DEFAULT 0 CHECK (candidate_percent BETWEEN 0 AND 100),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, app_key)
);

CREATE TABLE api_clients (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id),
  name            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('public','server')),
  key_prefix      text NOT NULL,
  key_hash        bytea NOT NULL UNIQUE,
  allowed_origins text[] NOT NULL DEFAULT '{}',
  rate_per_min    int NOT NULL DEFAULT 600 CHECK (rate_per_min BETWEEN 1 AND 1000000),
  revoked_at      timestamptz,
  last_used_at    timestamptz,
  created_by      uuid REFERENCES admin_users(id),
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhooks (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id),
  url         text NOT NULL CHECK (url ~ '^https?://'),
  secret_enc  bytea NOT NULL,
  topics      text[] NOT NULL CHECK (topics <@ ARRAY['events','categories','config','data']::text[] AND cardinality(topics) > 0),
  active      boolean NOT NULL DEFAULT true,
  created_by  uuid REFERENCES admin_users(id),
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- Transactional outbox: rows are written in the same transaction as the change they announce.
CREATE TABLE outbox (
  id          bigserial PRIMARY KEY,
  kind        text NOT NULL CHECK (kind IN ('webhook','cdn.purge')),
  payload     jsonb NOT NULL,
  attempts    int NOT NULL DEFAULT 0,
  next_run_at timestamptz NOT NULL DEFAULT now(),
  done_at     timestamptz,
  dead_at     timestamptz,
  last_error  text,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbox_due ON outbox (next_run_at) WHERE done_at IS NULL AND dead_at IS NULL;

-- Append-only audit trail. In production, grant the application role INSERT and SELECT only.
CREATE TABLE audit_log (
  id          bigserial PRIMARY KEY,
  tenant_id   uuid REFERENCES tenants(id),
  at          timestamptz NOT NULL DEFAULT now(),
  actor_id    uuid REFERENCES admin_users(id),
  actor_ip    text,
  action      text NOT NULL,
  entity      text NOT NULL,
  entity_id   text NOT NULL,
  before      jsonb,
  after       jsonb,
  request_id  text
);
CREATE INDEX audit_log_entity ON audit_log (entity, entity_id, id DESC);
CREATE INDEX audit_log_tenant ON audit_log (tenant_id, id DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS webhooks;
DROP TABLE IF EXISTS api_clients;
DROP TABLE IF EXISTS ui_config_channels;
DROP TABLE IF EXISTS ui_configs;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS resource_versions;
DROP TABLE IF EXISTS calendar_year_drafts;
DROP TABLE IF EXISTS calendar_data_versions;
DROP TABLE IF EXISTS calendar_years;
DROP TABLE IF EXISTS admin_sessions;
DROP TABLE IF EXISTS admin_users;
DROP TABLE IF EXISTS tenants;
