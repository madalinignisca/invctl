-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- backend_pool and route catch up to every convention the rest of the schema
-- already has. They are the two oldest tables in this set
-- (shared/00004_dependencies.sql) and predate lifecycle, row_version,
-- created_at and updated_at all existing at once -- today a load-balancer pool
-- or an L7 route can be declared but never corrected and never withdrawn
-- (docs/superpowers/plans/2026-09-17-write-surface-gaps.md, Task 1).
--
-- NOT IN shared/, EVEN THOUGH THE ADD COLUMN STATEMENTS ARE PORTABLE SQL --
-- MEASURED, AND IT MATTERS. The first draft of this migration put the four
-- ADD COLUMN statements in shared/00070 on the reasoning that they run
-- identically on both engines. That is true and still broke a fresh install:
-- Migrate() runs the ENTIRE shared/ directory before the FIRST statement of
-- the dialect directory, so on a brand-new database shared/00070 lands its
-- columns on `route`, and only afterwards does the sqlite/00005 rebuild of
-- `route` run -- copying the seven columns ITS hardcoded column list knew
-- about in 00005's `INSERT INTO route_new (id, frontend_endpoint_id,
-- match_type, match_value, backend_pool_id, tls_termination, priority) SELECT
-- ... FROM route`, then dropping the original table. The four new columns
-- were silently gone, and every existing store test for backend_pool passed
-- (that table is untouched by 00005) while a structural column check on
-- `route` failed -- exactly the asymmetry that made it easy to miss.
-- `TestBackendPoolAndRouteCarryTheLifecycleColumns` (backend_pool_lifecycle_
-- test.go) is what caught it, on SQLite only; PostgreSQL has no equivalent
-- rebuild and passed both ways. This is why every ADD COLUMN this repo has
-- landed since 00010 (see 00019, 00063, 00065, 00069) lives in the dialect
-- pair rather than shared/, byte-identical between them: not a style choice,
-- a guard against exactly this ordering trap for any table a later dialect
-- migration ever rebuilds. Filed here for both reasons at once.
--
-- backend_pool was never rebuilt by 00005 (it isn't in that migration's list
-- of sixteen tables) so a plain ALTER would have been safe for it alone, but
-- putting the two tables through the same file, in the same directory, is the
-- one way this migration cannot fall into the same trap a second time by
-- accident.
--
-- CREATED_AT/UPDATED_AT ARE A STAMPED LITERAL, NOT A RECOVERY, AND NOT NOW().
-- CLAUDE.md forbids NOW()/CURRENT_TIMESTAMP as a column default -- the
-- database must never mint a timestamp, only store one Go generated. These
-- rows genuinely predate the column existing at all, so there is no true
-- creation or modification instant to recover; 00019 recovered created_at/
-- updated_at for endpoint/interface/ip_address/prefix from change_log's
-- MIN/MAX(at) because those tables already HAD audited history to recover,
-- and left the columns nullable precisely because not every row had one.
-- backend_pool and route have never had an UpdateX or RetireX, so there is no
-- change_log row to recover from either -- there is nothing truer available
-- than "this is the day the column arrived", so that is what the NOT NULL
-- DEFAULT stamps, honestly, as the migration's own date. Any write in this
-- codebase overwrites it with a real Go-generated value the next time the row
-- is created or corrected.
--
-- THE UNIQUE CONSTRAINT is the harder half. backend_pool carries table-level
-- UNIQUE (service_id, name) from shared/00004, unnamed, so SQLite can never
-- drop it (00005's whole reason to exist). Once withdrawal exists that
-- constraint is wrong in the way 00003 documents for identity(realm, name): a
-- retired pool would keep its name reserved forever, so a service could never
-- re-declare a pool under the name it just withdrew. It becomes a partial
-- unique index scoped to live rows, via the same create-copy-drop-rename
-- rebuild 00003 used. PostgreSQL names every constraint itself and drops it
-- by name in one ALTER -- see postgres/00070.
--
-- backend_member.pool_id and route.backend_pool_id both reference backend_pool,
-- so the rebuild runs with foreign key enforcement off, exactly as 00003 did
-- for identity's two referencing tables.
--
-- Signed off with the plan, 2026-09-17: correction and withdrawal for all
-- seven write-surface gaps, backend_pool and route among them.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE route ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT route_lifecycle_check CHECK (lifecycle IN ('active','retired'));
ALTER TABLE route ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE route ADD COLUMN created_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';
ALTER TABLE route ADD COLUMN updated_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';
CREATE INDEX idx_route_lifecycle ON route(lifecycle);

ALTER TABLE backend_pool ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT backend_pool_lifecycle_check CHECK (lifecycle IN ('active','retired'));
ALTER TABLE backend_pool ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE backend_pool ADD COLUMN created_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';
ALTER TABLE backend_pool ADD COLUMN updated_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';

PRAGMA foreign_keys = OFF;

CREATE TABLE backend_pool_new (
  id           TEXT NOT NULL PRIMARY KEY,
  service_id   TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  lb_algorithm TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active'
                 CONSTRAINT backend_pool_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  row_version  INTEGER NOT NULL DEFAULT 1,
  created_at   TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z',
  updated_at   TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z'
  -- No inline UNIQUE. See the partial index below.
);

INSERT INTO backend_pool_new
  (id, service_id, name, lb_algorithm, lifecycle, row_version, created_at, updated_at)
SELECT id, service_id, name, lb_algorithm, lifecycle, row_version, created_at, updated_at
FROM backend_pool;

DROP TABLE backend_pool;
ALTER TABLE backend_pool_new RENAME TO backend_pool;

-- Uniqueness over LIVE pools only -- a retired pool's name is free to reuse.
CREATE UNIQUE INDEX idx_backend_pool_name_active
  ON backend_pool(service_id, name) WHERE lifecycle = 'active';
CREATE INDEX idx_backend_pool_lifecycle ON backend_pool(lifecycle);

-- No PRAGMA foreign_key_check: goose runs migration statements with Exec,
-- which discards the result rows the pragma reports violations as, so it
-- would find a problem and silently throw it away. store.verifyForeignKeys
-- runs the equivalent check with Query after every migration instead, where a
-- violation can actually fail the run. Same reasoning as 00003.
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

CREATE TABLE backend_pool_old (
  id           TEXT PRIMARY KEY,
  service_id   TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  lb_algorithm TEXT,
  UNIQUE (service_id, name)
);

-- Retired pools go: their (service_id, name) may since have been reused by a
-- live pool, which the restored table-wide UNIQUE cannot represent. Same
-- trade 00003 made for identity on the way down.
INSERT INTO backend_pool_old (id, service_id, name, lb_algorithm)
SELECT id, service_id, name, lb_algorithm
FROM backend_pool
WHERE lifecycle = 'active';

DROP TABLE backend_pool;
ALTER TABLE backend_pool_old RENAME TO backend_pool;

PRAGMA foreign_keys = ON;

DROP INDEX idx_route_lifecycle;
ALTER TABLE route DROP COLUMN updated_at;
ALTER TABLE route DROP COLUMN created_at;
ALTER TABLE route DROP COLUMN row_version;
ALTER TABLE route DROP COLUMN lifecycle;
