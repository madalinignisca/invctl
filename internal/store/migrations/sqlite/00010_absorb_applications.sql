-- `application` becomes a project, and the table goes.
--
-- WHY AT ALL. `application` grouped services and only services, had no UI
-- anywhere in the app, and held exactly one row in the fixture. Projects group
-- assets AND services, carry the owns/uses distinction, and are the thing a
-- business actually asks about. Keeping both would leave two overlapping ways
-- to say "these things belong together", and the weaker one would rot.
--
-- THIS IS A DESTRUCTIVE MIGRATION. It drops a column and a table that hold
-- real data. docs/DECISIONS.md requires an explicit recorded decision for that
-- after POC sign-off; the entry dated 2026-07-31 records that sign-off has not
-- happened, that the estate is a fixture plus one throwaway demo database, and
-- that this is therefore free now and would not be in a month.
--
-- NO REBUILD IS NEEDED, on either engine. Measured against the pinned driver
-- (modernc.org/sqlite v1.54.0, SQLite 3.53.3):
--
--   ALTER TABLE service DROP COLUMN application_id   with idx_service_app present
--     -> "error in index idx_service_app after drop column: no such column"
--   DROP INDEX idx_service_app; then the same DROP COLUMN
--     -> works, and the resulting DDL carries no REFERENCES application(id)
--
-- That matters more than it sounds: rebuilding `service` would cascade through
-- six dependent tables, which is exactly the surgery migration 00002 had to do
-- and documented as the reason to avoid it where possible.
--
-- IDS ARE PRESERVED. An application becomes a project with the SAME id, so
-- historic change_log rows (entity_type='application') still point at a row
-- that resolves. The audit trail keeps its integrity and simply describes an
-- entity that has since been renamed.
--
-- THIS MIGRATION WRITES NO change_log ROW, following the standing rule stated
-- in 00004 and in domain/classification.go: no migration in this repository
-- writes to the audit trail, because that trail records what operators do to
-- the estate and this is the schema arriving. It is a closer call than usual
-- since data moves rather than only shape -- 00009 reserved the `import`
-- action for precisely this -- and DECISIONS.md records the choice rather than
-- leaving it to be inferred.

-- +goose Up
-- COALESCE(NULLIF(...)) because `project` carries project_code_check and
-- project_name_check (CHECK col <> '') and `application` never did -- it was
-- NOT NULL and nothing more. An empty code in the source would abort the whole
-- migration mid-flight with a constraint violation naming a table the operator
-- did not touch. Unlikely in a fixture; free to make impossible.
INSERT INTO project (id, code, name, description, owner_team, lifecycle, created_at, updated_at)
SELECT id,
       COALESCE(NULLIF(code, ''), 'app-' || id),
       COALESCE(NULLIF(name, ''), NULLIF(code, ''), 'app-' || id),
       NULL, owner_team, 'active', created_at, updated_at
FROM application;

-- A service belonging to an application is that project OWNING it: it existed
-- for that application, which is exactly what `owns` means.
-- A RETIRED service takes a retired link. ListProjectServices filters retired
-- services out, so an active link to one would be invisible in the UI while
-- still occupying the single owner slot in idx_project_service_owner -- nobody
-- could then own that service, and no screen would say why.
INSERT INTO project_service (project_id, service_id, relation, note, lifecycle, created_at, updated_at)
SELECT s.application_id, s.id, 'owns', NULL,
       CASE WHEN s.lifecycle = 'retired' THEN 'retired' ELSE 'active' END,
       s.created_at, s.updated_at
FROM service s WHERE s.application_id IS NOT NULL;

-- The search index follows the rename, or a search for the old name returns a
-- hit that leads nowhere.
DELETE FROM search_index WHERE entity_type = 'application';
INSERT INTO search_index (entity_type, entity_id, title, subtitle, body)
SELECT 'project', id, name, code, COALESCE(owner_team, '') FROM project;

DROP INDEX idx_service_app;
ALTER TABLE service DROP COLUMN application_id;
DROP TABLE application;

-- +goose Down
CREATE TABLE application (
  id         TEXT PRIMARY KEY,
  code       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  owner_team TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- Every project comes back as an application. Codes are unique on both sides,
-- so this is well defined -- but a project created AFTER the absorb reappears
-- as an application, which is a lie the down path tells knowingly. A down
-- migration is a development convenience here, not a supported production
-- path, and saying so is better than pretending the round trip is lossless.
INSERT INTO application (id, code, name, owner_team, created_at, updated_at)
SELECT id, code, name, owner_team, created_at, updated_at FROM project;

ALTER TABLE service ADD COLUMN application_id TEXT REFERENCES application(id);

UPDATE service SET application_id = (
  SELECT ps.project_id FROM project_service ps
  WHERE ps.service_id = service.id AND ps.relation = 'owns' AND ps.lifecycle = 'active'
);

CREATE INDEX idx_service_app ON service(application_id);

DELETE FROM search_index WHERE entity_type = 'project';
INSERT INTO search_index (entity_type, entity_id, title, subtitle, body)
SELECT 'application', id, name, code, COALESCE(owner_team, '') FROM application;
