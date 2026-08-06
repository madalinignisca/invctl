-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Projects: who owns what, and who merely uses it.
--
-- The business question this answers is not one the rest of the schema can.
-- `environment` says where a thing runs, `asset_closure` says what contains
-- it, and `owner_team` is free text on four tables with no referential
-- integrity at all. None of them answers "what does the orders project
-- actually consist of, and what is it standing on that somebody else owns".
--
-- TWO RELATIONS, AND THE ASYMMETRY IS THE WHOLE POINT.
--
--   owns  -- the thing exists FOR this project. At most ONE project may own a
--            given asset or service, enforced by a partial unique index below.
--   uses  -- the project depends on it and shares it. Any number of projects.
--
-- That asymmetry is what makes cost attribution possible later without a
-- weight on every edge: each thing has at most one direct-cost owner and any
-- number of shared consumers. No cost columns yet -- the hook is what is
-- expensive to retrofit, the numbers are not, and a nullable cost_share
-- nobody writes is a column that must be classified, tested and migrated for
-- no benefit.
--
-- `relation` is a named CHECK rather than a lookup table, on the rule
-- docs/DECISIONS.md sets out: a value that selects a CODE PATH stays an enum,
-- because a third value arriving as data would be storable and then silently
-- inert everywhere -- exactly the defect that made asset.kind behavioural. It
-- is explained at /help through internal/help instead, which is the half of
-- that panel for meanings the engine owns.
--
-- DIALECT-SPLIT, and byte-identical to the sqlite copy. Placement follows
-- the dependency, not the SQL: Migrate applies every shared migration before
-- any dialect one, and dialect 00004 REBUILDS `service`. A shared migration
-- creating a table that references service(id) would run first and then watch
-- its parent be dropped and recreated underneath it. 00010 must be dialect
-- for the same reason, so both halves of this feature live together.

-- +goose Up
CREATE TABLE project (
  id          TEXT NOT NULL PRIMARY KEY,
  code        TEXT NOT NULL CONSTRAINT project_code_check CHECK (code <> ''),
  name        TEXT NOT NULL CONSTRAINT project_name_check CHECK (name <> ''),
  description TEXT,
  owner_team  TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT project_lifecycle_check
                CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- A UNIQUE INDEX rather than an inline UNIQUE constraint: on SQLite a named
-- UNIQUE still cannot be dropped (`constraint may not be dropped`) but an
-- index can, so the next change to this table is a DROP INDEX rather than a
-- rebuild. Same lesson as migrations 00002 and 00003.
CREATE UNIQUE INDEX project_code_key ON project(code);

CREATE TABLE project_asset (
  project_id TEXT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  asset_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  relation   TEXT NOT NULL CONSTRAINT project_asset_relation_check
               CHECK (relation IN ('owns','uses')),
  note       TEXT,
  lifecycle  TEXT NOT NULL DEFAULT 'active'
               CONSTRAINT project_asset_lifecycle_check
               CHECK (lifecycle IN ('active','retired')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  -- Composite key, no surrogate id: this is a membership row, exactly like
  -- net_group_member, and it reactivates through ON CONFLICT rather than by
  -- inserting a second row. It also keeps the absorb migration legal, since
  -- INSERT ... SELECT cannot generate a UUIDv7 and IDs are never made in SQL.
  --
  -- One consequence worth stating: a project cannot both own AND use the same
  -- asset. Owning implies using, and two rows would double-count it.
  PRIMARY KEY (project_id, asset_id)
);

-- The at-most-one-owner rule. Partial, so a retired link frees the slot and
-- any number of `uses` links coexist with the one owner. Same technique and
-- the same reason as idx_net_group_member_one.
CREATE UNIQUE INDEX idx_project_asset_owner
  ON project_asset(asset_id) WHERE relation = 'owns' AND lifecycle = 'active';
CREATE INDEX idx_project_asset_asset ON project_asset(asset_id);

CREATE TABLE project_service (
  project_id TEXT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  service_id TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  relation   TEXT NOT NULL CONSTRAINT project_service_relation_check
               CHECK (relation IN ('owns','uses')),
  note       TEXT,
  lifecycle  TEXT NOT NULL DEFAULT 'active'
               CONSTRAINT project_service_lifecycle_check
               CHECK (lifecycle IN ('active','retired')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (project_id, service_id)
);

CREATE UNIQUE INDEX idx_project_service_owner
  ON project_service(service_id) WHERE relation = 'owns' AND lifecycle = 'active';
CREATE INDEX idx_project_service_service ON project_service(service_id);

-- +goose Down
DROP TABLE project_service;
DROP TABLE project_asset;
DROP TABLE project;
