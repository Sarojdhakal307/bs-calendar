-- Super admins may approve their own year-table drafts and UI configs (single-admin installations).
-- The four-eyes CHECKs stay, but accept a row the application explicitly marks as self-approved,
-- so any other code path that lets an author approve their own change is still rejected.

-- +goose Up
ALTER TABLE calendar_year_drafts ADD COLUMN self_approved boolean NOT NULL DEFAULT false;
ALTER TABLE calendar_year_drafts DROP CONSTRAINT calendar_year_drafts_check;
ALTER TABLE calendar_year_drafts ADD CONSTRAINT calendar_year_drafts_four_eyes
  CHECK (state <> 'approved' OR decided_by <> created_by OR self_approved);

ALTER TABLE ui_configs ADD COLUMN self_approved boolean NOT NULL DEFAULT false;
ALTER TABLE ui_configs DROP CONSTRAINT ui_configs_check;
ALTER TABLE ui_configs ADD CONSTRAINT ui_configs_four_eyes
  CHECK (origin = 'rollback' OR approved_by IS NULL OR approved_by <> created_by OR self_approved);

-- +goose Down
-- NOT VALID: rows self-approved while this migration was applied would fail the old rule.
ALTER TABLE ui_configs DROP CONSTRAINT ui_configs_four_eyes;
ALTER TABLE ui_configs ADD CONSTRAINT ui_configs_check
  CHECK (origin = 'rollback' OR approved_by IS NULL OR approved_by <> created_by) NOT VALID;
ALTER TABLE ui_configs DROP COLUMN self_approved;

ALTER TABLE calendar_year_drafts DROP CONSTRAINT calendar_year_drafts_four_eyes;
ALTER TABLE calendar_year_drafts ADD CONSTRAINT calendar_year_drafts_check
  CHECK (state <> 'approved' OR decided_by <> created_by) NOT VALID;
ALTER TABLE calendar_year_drafts DROP COLUMN self_approved;
