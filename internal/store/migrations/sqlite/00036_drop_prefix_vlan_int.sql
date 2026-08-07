-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- The contract half of 00031: the loose VLAN integer goes.
--
-- WHY IT COULD NOT WAIT. 00031 added vlan_ref_id BESIDE prefix.vlan_id and the
-- startup backfill filled it, which was the expand step and was correct. What
-- was not done is anything to the code still writing the integer -- the prefix
-- form, the list column and the search annotation all carried on using it. So
-- two columns held the same fact and different code paths wrote each: editing a
-- prefix's VLAN in the UI moved the integer and left the reference behind, and
-- /prefixes then said 41 while /vlans still counted the network under 40.
-- Neither page was wrong about its own column. The estate said two things.
--
-- That is precisely the failure the four-column address pattern is written to
-- avoid -- text and bytes are rewritten TOGETHER by SetCIDR for the same reason
-- -- and leaving it in place was the mistake, not adding the reference.
--
-- THIS RELEASE REQUIRES THE PREVIOUS ONE TO HAVE BEEN RUN. The backfill that
-- turns each integer into a VLAN is Go, because every row it writes needs a
-- UUIDv7 and an RFC3339 timestamp and neither is generated in SQL here. A
-- deployment jumping straight from before 00031 to this migration would have
-- the integer dropped before any Go ran, and would lose its VLAN assignments.
-- Expand, verify, contract -- in three deployments, not one. There is no way to
-- express that requirement in the migration itself, so it is stated here and in
-- the release notes rather than pretended away.
--
-- SQLite rebuilds the table because it cannot drop a column that other rows in
-- the file were written against; nothing REFERENCES prefix, so this is a copy
-- and a rename.

-- +goose Up
CREATE TABLE prefix_rebuilt (
  -- Explicit NOT NULL: PRIMARY KEY does not imply it on SQLite, and 00029
  -- learned that the hard way when a rebuild quietly dropped it.
  id             TEXT PRIMARY KEY NOT NULL,
  cidr_text      TEXT NOT NULL,
  addr_family    INTEGER NOT NULL
                   CONSTRAINT prefix_addr_family_check CHECK (addr_family IN (4,6)),
  addr_start     BYTEA NOT NULL,
  addr_end       BYTEA NOT NULL,
  environment_id TEXT REFERENCES environment(id),
  role           TEXT,
  vrf_id         TEXT REFERENCES vrf(id),
  vlan_ref_id    TEXT REFERENCES vlan(id),
  created_at     TEXT,
  updated_at     TEXT,
  row_version    INTEGER NOT NULL DEFAULT 1
);

INSERT INTO prefix_rebuilt
  (id, cidr_text, addr_family, addr_start, addr_end, environment_id, role,
   vrf_id, vlan_ref_id, created_at, updated_at, row_version)
SELECT
   id, cidr_text, addr_family, addr_start, addr_end, environment_id, role,
   vrf_id, vlan_ref_id, created_at, updated_at, row_version
FROM prefix;

DROP TABLE prefix;
ALTER TABLE prefix_rebuilt RENAME TO prefix;

CREATE INDEX idx_prefix_range    ON prefix(addr_family, addr_start, addr_end);
CREATE INDEX idx_prefix_vrf      ON prefix(vrf_id);
CREATE INDEX idx_prefix_vlan_ref ON prefix(vlan_ref_id);
CREATE UNIQUE INDEX prefix_vrf_cidr_key    ON prefix(vrf_id, cidr_text) WHERE vrf_id IS NOT NULL;
CREATE UNIQUE INDEX prefix_global_cidr_key ON prefix(cidr_text)         WHERE vrf_id IS NULL;

-- +goose Down
-- The integer comes back empty. Its values were the VLAN references' to begin
-- with and rebuilding them from vid would invent a number for any prefix whose
-- VLAN has since been renumbered -- a down migration that fabricates data is
-- worse than one that admits the column is gone.
ALTER TABLE prefix ADD COLUMN vlan_id INTEGER;
