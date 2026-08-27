-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Which projects a person is assigned to (WP-G1 piece 3, task 11) -- the
-- table that will let a project_owner's scope be computed. Nothing consults
-- it yet: Authorizer.Permit is Task 12, the gate flip is Task 13. This
-- migration is schema only.
--
-- A NEW ROW PER ASSIGNMENT, NOT A SINGLE ROW TOGGLED, unlike project_asset
-- and its neighbours. Those link tables use a composite primary key and flip
-- `lifecycle` on the same row because ON CONFLICT DO UPDATE reactivating it
-- is exactly the semantics wanted -- one row is the whole history of "is
-- this asset linked to this project right now". An assignment's history is
-- worth keeping distinct: if the same person is assigned, released, and
-- re-assigned six months later, collapsing that into one row loses when the
-- first grant ended and hides that there were two separate decisions. So
-- this table gets its own id and its own row_version, shaped like
-- custom_field (00051) rather than project_circuit (00041), and the partial
-- unique index below is what makes a fresh row for a repeat assignment
-- possible.
--
-- THE PARTIAL UNIQUE INDEX IS THE POINT. (user_id, project_id) WHERE
-- lifecycle = 'active' allows at most one active assignment per pair at a
-- time, but a RETIRED row for the same pair does not block a new one -- a
-- total UNIQUE would mean an operator who releases somebody from a project
-- could never put them back. See TestAReleasedAssignmentCanBeMadeAgain.
--
-- SOFT DELETE, AS EVERYWHERE. ReleaseProject retires the row (lifecycle =
-- 'retired'); nothing in this codebase deletes it. See
-- TestReleasingAProjectRetiresTheRowAndDoesNotDeleteIt.
--
-- ON DELETE CASCADE on both foreign keys matches project_circuit and
-- project_asset: neither app_user nor project rows are ever hard-deleted, so
-- the clause never actually fires, but it states the intent this schema
-- already commits to elsewhere rather than leaving an orphan-handling
-- question open for a reader to wonder about.

-- +goose Up
CREATE TABLE user_project (
  id          TEXT PRIMARY KEY NOT NULL,
  user_id     TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  project_id  TEXT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT user_project_lifecycle_check
                CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);

-- At most one ACTIVE assignment per (user, project); a retired one does not
-- block a fresh row. See the header note above.
CREATE UNIQUE INDEX user_project_active_key
  ON user_project(user_id, project_id) WHERE lifecycle = 'active';

-- ProjectsForUser's query: "what is this person assigned to right now".
CREATE INDEX idx_user_project_user ON user_project(user_id, lifecycle);

-- +goose Down
DROP INDEX idx_user_project_user;
DROP INDEX user_project_active_key;
DROP TABLE user_project;
