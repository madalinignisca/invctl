-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- backend_pool and route catch up to every convention the rest of the schema
-- already has. PostgreSQL half; see sqlite/00070 for the full reasoning,
-- including why this landed in the dialect pair rather than shared/ even
-- though every ADD COLUMN statement below is portable SQL on its own --
-- SQLite's sqlite/00005 rebuild of `route` (with a hardcoded pre-lifecycle
-- column list) silently discarded these same columns on a fresh install when
-- they were first tried in shared/00070, because Migrate() runs the whole
-- shared/ directory before the first statement of the dialect directory.
-- PostgreSQL has no equivalent rebuild and was never at risk, but the two
-- engines get one file each so nothing here can drift from what SQLite
-- actually needs.
--
-- No rebuild needed for the uniqueness half either: PostgreSQL names every
-- constraint itself, so backend_pool's UNIQUE (service_id, name) from
-- shared/00004_dependencies.sql is backend_pool_service_id_name_key by the
-- standard <table>_<columns>_key convention -- the same rule that named
-- identity_realm_name_key in 00003 -- and drops by name in one statement.

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
CREATE INDEX idx_backend_pool_lifecycle ON backend_pool(lifecycle);

ALTER TABLE backend_pool DROP CONSTRAINT backend_pool_service_id_name_key;

CREATE UNIQUE INDEX idx_backend_pool_name_active
  ON backend_pool(service_id, name) WHERE lifecycle = 'active';

-- +goose Down
DROP INDEX IF EXISTS idx_backend_pool_name_active;

-- Same trade as the SQLite half: a retired pool's name may since have been
-- reused by a live one, which the restored UNIQUE cannot represent.
DELETE FROM backend_pool WHERE lifecycle = 'retired';

ALTER TABLE backend_pool ADD CONSTRAINT backend_pool_service_id_name_key
  UNIQUE (service_id, name);

DROP INDEX IF EXISTS idx_backend_pool_lifecycle;
ALTER TABLE backend_pool DROP COLUMN updated_at;
ALTER TABLE backend_pool DROP COLUMN created_at;
ALTER TABLE backend_pool DROP COLUMN row_version;
ALTER TABLE backend_pool DROP COLUMN lifecycle;

DROP INDEX IF EXISTS idx_route_lifecycle;
ALTER TABLE route DROP COLUMN updated_at;
ALTER TABLE route DROP COLUMN created_at;
ALTER TABLE route DROP COLUMN row_version;
ALTER TABLE route DROP COLUMN lifecycle;
