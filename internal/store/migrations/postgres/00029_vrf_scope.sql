-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A prefix is unique WITHIN ITS TABLE, not across the world.
--
-- The reasoning is in the sqlite file of the same number and is not repeated
-- here. The only difference is mechanical: PostgreSQL can drop a named
-- constraint, so this is three statements where SQLite has to copy the table.
--
-- The two partial indexes are NOT a Postgres nicety -- they are the constraint.
-- A composite UNIQUE (vrf_id, cidr_text) enforces nothing when vrf_id IS NULL,
-- because NULLs are distinct, and every row starts NULL.

-- +goose Up
CREATE TABLE vrf (
  id          TEXT PRIMARY KEY NOT NULL,
  name        TEXT NOT NULL,
  -- The route distinguisher, e.g. 65000:100. Optional: a VRF is a useful
  -- grouping long before anybody has decided how it is exported.
  rd          TEXT,
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT vrf_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
-- Live rows only, so a retired VRF does not hold its name for ever.
CREATE UNIQUE INDEX vrf_name_key ON vrf(name) WHERE lifecycle <> 'retired';
CREATE UNIQUE INDEX vrf_rd_key   ON vrf(rd)   WHERE rd IS NOT NULL AND lifecycle <> 'retired';

ALTER TABLE prefix ADD COLUMN vrf_id TEXT REFERENCES vrf(id);
ALTER TABLE prefix DROP CONSTRAINT prefix_cidr_text_key;

CREATE INDEX idx_prefix_vrf ON prefix(vrf_id);

CREATE UNIQUE INDEX prefix_vrf_cidr_key    ON prefix(vrf_id, cidr_text) WHERE vrf_id IS NOT NULL;
CREATE UNIQUE INDEX prefix_global_cidr_key ON prefix(cidr_text)         WHERE vrf_id IS NULL;

-- +goose Down
DROP INDEX prefix_global_cidr_key;
DROP INDEX prefix_vrf_cidr_key;
DROP INDEX idx_prefix_vrf;
ALTER TABLE prefix ADD CONSTRAINT prefix_cidr_text_key UNIQUE (cidr_text);
ALTER TABLE prefix DROP COLUMN vrf_id;
DROP TABLE vrf;
