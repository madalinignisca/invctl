-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A span of addresses somebody has set aside.
--
-- WHAT IT IS FOR. A prefix says "this network exists". An address says "this
-- interface holds this". Neither can say "10.20.30.100 to .199 belongs to DHCP,
-- do not hand any of it out" -- and that is the sentence that stops two people
-- allocating the same address a fortnight apart. A range is delegation: the
-- space is spoken for by something that is not this system.
--
-- NO FOREIGN KEY TO prefix, DELIBERATELY, and for the same reason ip_address
-- has none. Containment here is computed from the byte range, never stored: a
-- range belongs to whichever prefix contains it, and if somebody later declares
-- a narrower prefix around it the answer must change by itself. A stored
-- parent_id would be a second copy of a fact the addresses already carry, and
-- the two would disagree the first time anybody subnetted anything.
--
-- SCOPED BY VRF like a prefix, and for the identical reason: two tenants may
-- both set aside 10.0.0.0/8's hundredth address, and those are different
-- reservations of different space.
--
-- THE FOUR-COLUMN PATTERN AGAIN. Text for the label, bytes for the scan. The
-- start and end are stored rather than a start and a count, because every
-- question asked of this table is "does this address fall inside it" and that
-- is a comparison, not arithmetic.

-- +goose Up
CREATE TABLE ip_range (
  id           TEXT PRIMARY KEY NOT NULL,
  start_text   TEXT NOT NULL,
  end_text     TEXT NOT NULL,
  addr_family  INTEGER NOT NULL
                 CONSTRAINT ip_range_addr_family_check CHECK (addr_family IN (4,6)),
  addr_start   BYTEA NOT NULL,
  addr_end     BYTEA NOT NULL,
  vrf_id       TEXT REFERENCES vrf(id),
  -- What has the space: dhcp, a load balancer's VIP pool, an out-of-band
  -- allocation somebody made by hand. Free text, like prefix.role.
  role         TEXT,
  description  TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active'
                 CONSTRAINT ip_range_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  row_version  INTEGER NOT NULL DEFAULT 1
);

-- The containment scan, family first so a 4-byte range is never compared
-- against a 16-byte one.
CREATE INDEX idx_ip_range_scan ON ip_range(addr_family, addr_start, addr_end);
CREATE INDEX idx_ip_range_vrf  ON ip_range(vrf_id);

-- OVERLAPPING RANGES ARE ALLOWED; IDENTICAL ONES ARE NOT. Two reservations that
-- partly cover each other are a real, if untidy, estate -- a DHCP pool inside a
-- wider "do not use" band is ordinary. Two rows with exactly the same bounds
-- carry no information the first does not, and are always somebody submitting a
-- form twice.
--
-- Two partial indexes for the reason 00029 spells out: NULLs are distinct on
-- both engines, so a single composite including vrf_id would constrain nothing
-- at all for the global table, which is where every row starts.
CREATE UNIQUE INDEX ip_range_vrf_bounds_key ON ip_range(vrf_id, addr_start, addr_end)
  WHERE vrf_id IS NOT NULL AND lifecycle <> 'retired';
CREATE UNIQUE INDEX ip_range_global_bounds_key ON ip_range(addr_start, addr_end)
  WHERE vrf_id IS NULL AND lifecycle <> 'retired';

-- +goose Down
DROP INDEX ip_range_global_bounds_key;
DROP INDEX ip_range_vrf_bounds_key;
DROP INDEX idx_ip_range_vrf;
DROP INDEX idx_ip_range_scan;
DROP TABLE ip_range;
