-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A prefix is unique WITHIN ITS TABLE, not across the world.
--
-- Until now `prefix` carried a global `UNIQUE (cidr_text)`, so 10.0.0.0/8 could
-- exist exactly once in the entire system. That is correct for one organisation
-- describing its own estate and wrong the moment two tenants are described at
-- once -- and overlapping RFC1918 between customers is not an edge case, it is
-- the normal state of anybody managing more than one.
--
-- ROADMAP §D1's APPENDIX SAYS THIS IS NOT A CORRECTNESS FIX. It is right about
-- what it checked and it stopped one table too early. Address-level uniqueness
-- really is already scoped -- ip_address is UNIQUE (addr_text, interface_id),
-- so overlapping addresses have always worked. Nobody looked at the prefix
-- table, where uniqueness is global and always has been.
--
-- WHY THE COLUMN NOW AND THE SCREENS LATER. Adding a nullable column and
-- reindexing costs nothing today. Doing it after a client's data is loaded means
-- rewriting a unique constraint on live rows, and reconciling whatever collided
-- while it was missing. The management surface -- route targets, import/export
-- policies, per-VRF lookup -- is real work and buys nothing until somebody has a
-- second tenant, so it waits. NULL means the global table, which is exactly what
-- every existing row already means, so nothing is rewritten.
--
-- TWO PARTIAL INDEXES, FOR THE REASON 00021 GIVES AT LENGTH. A plain composite
-- UNIQUE (vrf_id, cidr_text) would enforce NOTHING for the global table: SQL
-- treats NULLs as distinct on both engines, so every row with vrf_id IS NULL
-- would be mutually unique and 10.20.0.0/16 could be inserted a hundred times.
-- The migration would have read like it was adding a constraint while quietly
-- removing the only one there was.
--
-- SQLITE REBUILDS THE TABLE because it cannot drop an inline constraint. Nothing
-- in the schema REFERENCES prefix, so this is a copy and a rename with no
-- foreign keys to disable -- which is why it is safe inside the transaction and
-- why the postgres file is three statements instead.

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

CREATE TABLE prefix_rebuilt (
  -- NOT NULL is explicit: PRIMARY KEY does not imply it on SQLite, and a
  -- rebuild that drops it lets several rows hold a NULL id, each invisible to
  -- every lookup while still counting towards every total.
  id             TEXT PRIMARY KEY NOT NULL,
  cidr_text      TEXT NOT NULL,
  -- Named, so the family can be widened later without another table rebuild.
  addr_family    INTEGER NOT NULL
                   CONSTRAINT prefix_addr_family_check CHECK (addr_family IN (4,6)),
  addr_start     BYTEA NOT NULL,
  addr_end       BYTEA NOT NULL,
  vlan_id        INTEGER,
  environment_id TEXT REFERENCES environment(id),
  role           TEXT,
  vrf_id         TEXT REFERENCES vrf(id),
  created_at     TEXT,
  updated_at     TEXT,
  row_version    INTEGER NOT NULL DEFAULT 1
);

INSERT INTO prefix_rebuilt
  (id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id,
   role, vrf_id, created_at, updated_at, row_version)
SELECT
   id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id,
   role, NULL, created_at, updated_at, row_version
FROM prefix;

DROP TABLE prefix;
ALTER TABLE prefix_rebuilt RENAME TO prefix;

CREATE INDEX idx_prefix_range ON prefix(addr_family, addr_start, addr_end);
CREATE INDEX idx_prefix_vrf   ON prefix(vrf_id);

CREATE UNIQUE INDEX prefix_vrf_cidr_key    ON prefix(vrf_id, cidr_text) WHERE vrf_id IS NOT NULL;
CREATE UNIQUE INDEX prefix_global_cidr_key ON prefix(cidr_text)         WHERE vrf_id IS NULL;

-- +goose Down
DROP INDEX prefix_global_cidr_key;
DROP INDEX prefix_vrf_cidr_key;
DROP INDEX idx_prefix_vrf;
DROP INDEX idx_prefix_range;

CREATE TABLE prefix_rebuilt (
  -- NOT NULL is explicit: PRIMARY KEY does not imply it on SQLite, and a
  -- rebuild that drops it lets several rows hold a NULL id, each invisible to
  -- every lookup while still counting towards every total.
  id             TEXT PRIMARY KEY NOT NULL,
  cidr_text      TEXT NOT NULL,
  -- Named, so the family can be widened later without another table rebuild.
  addr_family    INTEGER NOT NULL
                   CONSTRAINT prefix_addr_family_check CHECK (addr_family IN (4,6)),
  addr_start     BYTEA NOT NULL,
  addr_end       BYTEA NOT NULL,
  vlan_id        INTEGER,
  environment_id TEXT REFERENCES environment(id),
  role           TEXT,
  created_at     TEXT,
  updated_at     TEXT,
  row_version    INTEGER NOT NULL DEFAULT 1,
  UNIQUE (cidr_text)
);
INSERT INTO prefix_rebuilt
  (id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id,
   role, created_at, updated_at, row_version)
SELECT
   id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id,
   role, created_at, updated_at, row_version
FROM prefix;
DROP TABLE prefix;
ALTER TABLE prefix_rebuilt RENAME TO prefix;
CREATE INDEX idx_prefix_range ON prefix(addr_family, addr_start, addr_end);

DROP TABLE vrf;
