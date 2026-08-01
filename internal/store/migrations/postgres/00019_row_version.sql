-- Optimistic concurrency, and the four tables that could not say when they changed.
--
-- WHY: every update in this codebase reads a row on one connection and writes
-- it on another. Two people editing the same thing both read, both write, and
-- the second silently reverts the first -- with change_log recording the revert
-- as a deliberate act by the second person. That is the misattribution
-- docs/AUDIT.md objects to for observed state, arriving through the declared
-- door. A security review flagged it; this closes it.
--
-- ROW_VERSION, NOT A TIMESTAMP. The obvious token is updated_at, and it is not
-- good enough: domain.FormatTime truncates to the second, so two writes inside
-- the same second produce the same value and the guard passes for both. An
-- integer that increments always changes, and depends on no clock at all.
-- Every UPDATE reads `WHERE id = ? AND row_version = ?` and bumps it; zero rows
-- affected is a conflict, not a missing row.
--
-- WHY THE VERSION COMES FROM THE FORM: the token has to be the one the operator
-- was LOOKING AT. Reading it in the same request that writes it would compare
-- the row against itself and detect nothing, so it is rendered into the edit
-- form as a hidden field and travels back with the submission.
--
-- CREATED_AT/UPDATED_AT ARE RECOVERED, NOT INVENTED. Backfilling every existing
-- row with the migration date would assert that the whole estate was created
-- the day this ran, which is false and permanent. change_log already knows: it
-- has one row per mutation with a timestamp, so MIN(at) is genuinely when a
-- thing was created and MAX(at) when it last changed. Anything with no audit
-- history keeps NULL, which is the honest answer -- these columns are therefore
-- nullable here, unlike on the tables that had them from the start.

-- +goose Up
ALTER TABLE endpoint ADD COLUMN created_at TEXT;
ALTER TABLE endpoint ADD COLUMN updated_at TEXT;
ALTER TABLE interface ADD COLUMN created_at TEXT;
ALTER TABLE interface ADD COLUMN updated_at TEXT;
ALTER TABLE ip_address ADD COLUMN created_at TEXT;
ALTER TABLE ip_address ADD COLUMN updated_at TEXT;
ALTER TABLE prefix ADD COLUMN created_at TEXT;
ALTER TABLE prefix ADD COLUMN updated_at TEXT;

UPDATE endpoint SET
  created_at = (SELECT MIN(c.at) FROM change_log c
                 WHERE c.entity_type = 'endpoint' AND c.entity_id = endpoint.id),
  updated_at = (SELECT MAX(c.at) FROM change_log c
                 WHERE c.entity_type = 'endpoint' AND c.entity_id = endpoint.id);
UPDATE interface SET
  created_at = (SELECT MIN(c.at) FROM change_log c
                 WHERE c.entity_type = 'interface' AND c.entity_id = interface.id),
  updated_at = (SELECT MAX(c.at) FROM change_log c
                 WHERE c.entity_type = 'interface' AND c.entity_id = interface.id);
UPDATE ip_address SET
  created_at = (SELECT MIN(c.at) FROM change_log c
                 WHERE c.entity_type = 'ip_address' AND c.entity_id = ip_address.id),
  updated_at = (SELECT MAX(c.at) FROM change_log c
                 WHERE c.entity_type = 'ip_address' AND c.entity_id = ip_address.id);
UPDATE prefix SET
  created_at = (SELECT MIN(c.at) FROM change_log c
                 WHERE c.entity_type = 'prefix' AND c.entity_id = prefix.id),
  updated_at = (SELECT MAX(c.at) FROM change_log c
                 WHERE c.entity_type = 'prefix' AND c.entity_id = prefix.id);

ALTER TABLE asset ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE asset_cost ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE certificate ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE dependency ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE endpoint ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE environment ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE interface ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE ip_address ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE prefix ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE project ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE project_cost ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE service ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE service_cost ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE service_instance ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE team ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE team DROP COLUMN row_version;
ALTER TABLE service_instance DROP COLUMN row_version;
ALTER TABLE service_cost DROP COLUMN row_version;
ALTER TABLE service DROP COLUMN row_version;
ALTER TABLE project_cost DROP COLUMN row_version;
ALTER TABLE project DROP COLUMN row_version;
ALTER TABLE prefix DROP COLUMN row_version;
ALTER TABLE ip_address DROP COLUMN row_version;
ALTER TABLE interface DROP COLUMN row_version;
ALTER TABLE environment DROP COLUMN row_version;
ALTER TABLE endpoint DROP COLUMN row_version;
ALTER TABLE dependency DROP COLUMN row_version;
ALTER TABLE certificate DROP COLUMN row_version;
ALTER TABLE asset_cost DROP COLUMN row_version;
ALTER TABLE asset DROP COLUMN row_version;
ALTER TABLE prefix DROP COLUMN updated_at;
ALTER TABLE prefix DROP COLUMN created_at;
ALTER TABLE ip_address DROP COLUMN updated_at;
ALTER TABLE ip_address DROP COLUMN created_at;
ALTER TABLE interface DROP COLUMN updated_at;
ALTER TABLE interface DROP COLUMN created_at;
ALTER TABLE endpoint DROP COLUMN updated_at;
ALTER TABLE endpoint DROP COLUMN created_at;
