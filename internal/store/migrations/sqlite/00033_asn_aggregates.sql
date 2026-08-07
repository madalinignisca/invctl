-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- The layer above prefixes: what was delegated, and by whom.
--
-- A prefix says "this network exists here". It cannot say where the address
-- space CAME FROM -- whether 185.x is a /22 RIPE delegated to this company, a
-- slice of a provider's space that goes away with the contract, or RFC1918 that
-- was never anybody's to delegate. Those are three different facts with three
-- different consequences when somebody asks "can we renumber out of this".
--
-- AN AGGREGATE IS NOT A PREFIX, which is why it is its own table rather than a
-- flag. A prefix is something you route and address hosts from; an aggregate is
-- a registry allocation, and the useful question about it is "how much of this
-- have we actually used", answered by the prefixes falling inside it. Making
-- aggregates prefixes would put registry paperwork into the tree the allocator
-- walks and offer somebody the first address of a /22 nobody has subnetted yet.
--
-- CONTAINMENT IS THE SAME FOUR-COLUMN RANGE SCAN, so an aggregate needs no link
-- to its prefixes and no prefix needs a link to its aggregate: the bytes say
-- which contains which, and a stored parent would be a second copy of a fact
-- that changes by itself when somebody declares a narrower allocation.

-- +goose Up
-- A registry. Five exist globally, plus RFC1918 and friends, which are not a
-- registry at all -- but they ARE where a range came from, and modelling them
-- as one is what lets the question be asked uniformly.
CREATE TABLE rir (
  id          TEXT PRIMARY KEY NOT NULL,
  name        TEXT NOT NULL,
  -- is_private marks the ranges nobody delegated: RFC1918, RFC4193, CGNAT.
  -- Kept as a flag rather than a magic name, because code branches on it --
  -- an unused private aggregate is a tidiness note and an unused RIPE /22 is
  -- money.
  is_private  BOOLEAN NOT NULL DEFAULT FALSE,
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT rir_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX rir_name_key ON rir(name) WHERE lifecycle <> 'retired';

-- A block a registry delegated, in the same four columns everything else uses.
CREATE TABLE aggregate (
  id          TEXT PRIMARY KEY NOT NULL,
  cidr_text   TEXT NOT NULL,
  addr_family INTEGER NOT NULL
                CONSTRAINT aggregate_addr_family_check CHECK (addr_family IN (4,6)),
  addr_start  BYTEA NOT NULL,
  addr_end    BYTEA NOT NULL,
  rir_id      TEXT REFERENCES rir(id),
  -- When the delegation was made. A date, not a timestamp: registries allocate
  -- on days.
  allocated_on TEXT
                CONSTRAINT aggregate_allocated_on_check
                CHECK (allocated_on IS NULL OR (length(allocated_on) = 10
                  AND substr(allocated_on, 5, 1) = '-' AND substr(allocated_on, 8, 1) = '-')),
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT aggregate_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX idx_aggregate_range ON aggregate(addr_family, addr_start, addr_end);
CREATE UNIQUE INDEX aggregate_cidr_key ON aggregate(cidr_text) WHERE lifecycle <> 'retired';

-- An autonomous system number.
CREATE TABLE asn (
  id          TEXT PRIMARY KEY NOT NULL,
  -- 32-bit, so this exceeds a signed INTEGER on neither engine but is stored
  -- as BIGINT to say so plainly. 0 and 4294967295 are reserved.
  number      BIGINT NOT NULL
                CONSTRAINT asn_number_check CHECK (number >= 1 AND number <= 4294967294),
  name        TEXT,
  rir_id      TEXT REFERENCES rir(id),
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT asn_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX asn_number_key ON asn(number) WHERE lifecycle <> 'retired';

-- +goose Down
DROP INDEX asn_number_key;
DROP TABLE asn;
DROP INDEX aggregate_cidr_key;
DROP INDEX idx_aggregate_range;
DROP TABLE aggregate;
DROP INDEX rir_name_key;
DROP TABLE rir;
