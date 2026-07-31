-- The team indexes carry the lifecycle they are always filtered by.
--
-- Migration 00014 indexed `(team_id)` alone. Every count in `teamSelect` pairs
-- `team_id = t.id` with `lifecycle <> 'retired'`, so the planner found the rows
-- by index and then went to the heap for a column the index could have carried.
--
-- Measured on a 200,000-asset estate by a database review:
--
--   (team_id)            PostgreSQL 112 ms, 88,687 buffer hits   SQLite 165 ms
--   (team_id, lifecycle) PostgreSQL 19.8 ms, 311 buffers,        SQLite  11 ms
--                        Heap Fetches: 0
--
-- 88,687 buffer hits is roughly 693 MB of buffer traffic to render 27 rows.
-- `team_id` stays the leading column, so the equality filters in ListAssets,
-- ListServices and ListProjects are unaffected; both engines now get a covering
-- scan and SQLite reports SEARCH ... USING COVERING INDEX.
--
-- idx_identity_team goes. `identity.team_id` is written once by the seeder and
-- read by nothing at all -- no filter, no join, no template, no IdentityFilter
-- field. The index cost write amplification on every identity insert and served
-- no query. When the team page grows an identities section it can come back,
-- shaped for whatever that query turns out to be rather than guessed at now.

-- +goose Up
DROP INDEX idx_asset_team;
DROP INDEX idx_service_team;
DROP INDEX idx_project_team;
DROP INDEX idx_identity_team;

CREATE INDEX idx_asset_team   ON asset(team_id, lifecycle)   WHERE team_id IS NOT NULL;
CREATE INDEX idx_service_team ON service(team_id, lifecycle) WHERE team_id IS NOT NULL;
CREATE INDEX idx_project_team ON project(team_id, lifecycle) WHERE team_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_project_team;
DROP INDEX idx_service_team;
DROP INDEX idx_asset_team;

CREATE INDEX idx_asset_team    ON asset(team_id)    WHERE team_id IS NOT NULL;
CREATE INDEX idx_service_team  ON service(team_id)  WHERE team_id IS NOT NULL;
CREATE INDEX idx_project_team  ON project(team_id)  WHERE team_id IS NOT NULL;
CREATE INDEX idx_identity_team ON identity(team_id) WHERE team_id IS NOT NULL;
