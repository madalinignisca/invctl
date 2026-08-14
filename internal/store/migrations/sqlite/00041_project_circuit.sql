-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Which project a circuit belongs to (WP-I2).
--
-- FIXING A WRONG NUMBER, NOT ADDING A FEATURE. A project's cost rollup gathers
-- cost lines from assets and services and stops there. Circuits carry costs --
-- a monthly rate, an install fee, a contract end -- and there was no way to say
-- which project a circuit served, so every project that depends on connectivity
-- has been reporting less than it costs. The page was not wrong about what it
-- gathered; it was wrong about what it implied.
--
-- SHAPED EXACTLY LIKE project_asset, deliberately. Same composite key, same
-- owns/uses relation, same soft delete. That relation is not decoration: it is
-- what already drives the Own / Shared / Elsewhere breakdown on the project
-- page, and a circuit shared between two projects is the ordinary case -- one
-- transit link serving everything in a rack. Inventing a different shape here
-- would mean the rollup needed two ways to answer the same question.
--
-- OWNING IMPLIES USING, so a project cannot both own and use one circuit; the
-- composite primary key makes that unrepresentable rather than merely
-- discouraged, and reactivation goes through ON CONFLICT as it does for assets.

-- +goose Up
CREATE TABLE project_circuit (
  project_id TEXT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  circuit_id TEXT NOT NULL REFERENCES circuit(id) ON DELETE CASCADE,
  relation   TEXT NOT NULL CONSTRAINT project_circuit_relation_check
               CHECK (relation IN ('owns','uses')),
  note       TEXT,
  lifecycle  TEXT NOT NULL DEFAULT 'active'
               CONSTRAINT project_circuit_lifecycle_check
               CHECK (lifecycle IN ('active','retired')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (project_id, circuit_id)
);

-- ONE OWNER, ENFORCED BY THE DATABASE. A circuit may be USED by many projects
-- -- one transit link serving a whole rack is the ordinary case -- but only one
-- project can own it, or the same monthly rate lands in two rollups and the
-- estate appears to spend more than it does. The partial index is the
-- authority; store.checkOwnerFree exists only so the operator reads "already
-- owned by platform" instead of a bare uniqueness violation, since SQLite
-- reports one without naming a column.
CREATE UNIQUE INDEX idx_project_circuit_owner
  ON project_circuit(circuit_id) WHERE relation = 'owns' AND lifecycle = 'active';

-- The reverse lookup: "which projects does this circuit serve", which the
-- circuit page asks and the primary key cannot answer, since it leads with
-- project_id. Lifecycle second because every caller filters on it, which is the
-- shape 00016 and 00017 each had to correct after guessing.
CREATE INDEX idx_project_circuit_circuit ON project_circuit(circuit_id, lifecycle);

-- +goose Down
DROP INDEX idx_project_circuit_circuit;
DROP INDEX idx_project_circuit_owner;
DROP TABLE project_circuit;
