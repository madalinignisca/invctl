-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Storage as a dimension (WP-J3's outstanding half, prerequisite for WP-J4).
--
-- 00044 modelled compute and stopped. The worked example in
-- docs/COST-ATTRIBUTION.md §5.7 needs four rows and the estate could answer
-- two: block storage at 3.4% and bulk at 1.0% had nowhere to come from.
--
-- A POOL IS AN ASSET, and that was a decision rather than a default. A
-- first-class storage_pool entity models reality more closely -- one array
-- genuinely does hold several pools -- but cost lines, containment, the audit
-- trail, retirement, search and the impact engine all already work on assets,
-- and a fifth cost-attachment target would have meant touching the cost model,
-- the forms, the rollup and the classification table before a single number
-- could be divided. The asset kind `storage` already exists and was waiting.
--
-- The cost of the choice, stated plainly: one physical array holding three
-- pools is three sibling assets rather than one box with three children. That
-- is a fair description of what is being PRICED, which is what this is for.
--
-- ONE STORAGE FIGURE WOULD BE MEANINGLESS -- the spec's own words. Block and
-- bulk are different products at different prices per GB, and a project holding
-- 350 GB of one and 300 GB of the other has two different shares of two
-- different pools. That is why the claim is per POOL and not a column on the
-- workload: a single storage_gb column could never divide into two prices.
--
-- RAW, NOT USABLE, IS WHAT GETS RECORDED. An operator knows how many disks are
-- in the box; how much survives replication is arithmetic, and arithmetic
-- belongs in one place. Recording usable directly would mean every reader
-- trusting that whoever typed it had applied the right ratio -- and the ratio
-- is exactly the thing that differs between technologies nobody remembers the
-- factor for.

-- +goose Up

-- The raw-to-usable ratio, per technology.
--
-- HUNDREDTHS OF RAW PER ONE USABLE: Ceph at 3x replication is 300, a RAID6 set
-- of eight disks is 133, local disk is 100. Stored this way round because that
-- is how the loss is described -- "three times replication", not "a third
-- efficiency" -- and a ratio typed the way it is spoken is a ratio typed
-- correctly. Same unit as cluster.cpu_overcommit, so one renderer serves both.
--
-- A VOCABULARY TABLE RATHER THAN A GO ENUM, following asset_kind: a site with
-- an erasure-coded pool at 1.5x can INSERT one and use it immediately, without
-- a release. The seeded rows are the common cases, not the permitted set.
CREATE TABLE storage_kind (
  -- NOT NULL stated explicitly: a PRIMARY KEY does NOT imply it on SQLite,
  -- where several rows could then hold a NULL code, each invisible to every
  -- statement keyed on it while still counting towards every total. The other
  -- vocabularies say so for the same reason.
  code           TEXT PRIMARY KEY NOT NULL,
  label          TEXT NOT NULL,
  sort_order     INTEGER NOT NULL DEFAULT 0,
  description    TEXT,
  -- Raw capacity consumed per unit of usable capacity, in hundredths.
  raw_per_usable INTEGER NOT NULL,
  -- Named, because PostgreSQL invents a name for an anonymous CHECK and SQLite
  -- does not, and the two schemas then differ in a way only a cross-engine
  -- comparison notices.
  CONSTRAINT storage_kind_raw_per_usable_check CHECK (raw_per_usable >= 100)
);

INSERT INTO storage_kind (code, label, sort_order, raw_per_usable, description) VALUES
  ('local', 'Local disk', 10, 100,
   'One raw gigabyte per usable gigabyte. No redundancy, and the failure of the disk is the failure of the data.'),
  ('raid1', 'RAID 1 / mirror', 20, 200,
   'Mirrored. Two raw gigabytes per usable one.'),
  ('raid5', 'RAID 5', 30, 125,
   'Single parity. The ratio depends on the member count; 125 assumes five disks.'),
  ('raid6', 'RAID 6', 40, 133,
   'Double parity. The ratio depends on the member count; 133 assumes eight disks.'),
  ('ceph_2x', 'Ceph, 2x replication', 50, 200,
   'Two replicas.'),
  ('ceph_3x', 'Ceph, 3x replication', 60, 300,
   'Three replicas, which is the common default and the one people are surprised by.'),
  ('erasure', 'Erasure coded', 70, 150,
   'Parity across a wider set. The ratio depends on the profile; 150 is a common 4+2.'),
  ('external', 'Provider-managed', 80, 100,
   'Object storage or a managed volume, billed on what is stored. The provider''s own redundancy is already in the price.');

-- A pool's own size. Only meaningful on an asset that IS a pool.
--
-- NULL IS "NOT A POOL, OR NOBODY HAS MEASURED IT", exactly as with cpu_cores.
-- The two are told apart by the kind and by whether a claim points here, never
-- by treating a missing number as zero.
ALTER TABLE asset ADD COLUMN storage_kind TEXT REFERENCES storage_kind(code);
ALTER TABLE asset ADD COLUMN raw_capacity_gb INTEGER;

ALTER TABLE asset ADD CONSTRAINT asset_raw_capacity_check
  CHECK (raw_capacity_gb IS NULL OR raw_capacity_gb > 0);

-- What a workload holds in a pool.
--
-- A ROW PER (WORKLOAD, POOL) PAIR, because a machine routinely holds its system
-- disk on fast media and its backups on bulk, and those divide into two
-- different prices. The composite key says a workload states its claim on a
-- given pool once; correcting it is an UPDATE, which keeps the audit trail
-- linear rather than accumulating rows nobody can order.
--
-- ALLOCATED, NOT USED. df(1) is telemetry and belongs to the observed path with
-- its own obligations; this is what somebody agreed the workload gets, which is
-- the basis money is computed on. See docs/DECISIONS.md.
CREATE TABLE asset_storage_claim (
  asset_id     TEXT NOT NULL REFERENCES asset(id),
  pool_id      TEXT NOT NULL REFERENCES asset(id),
  allocated_gb INTEGER NOT NULL,
  note         TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  PRIMARY KEY (asset_id, pool_id),
  CONSTRAINT asset_storage_claim_allocated_gb_check CHECK (allocated_gb > 0),
  -- A pool cannot hold itself. Cheap to state, and the alternative is a share
  -- of a pool that includes its own capacity.
  CONSTRAINT asset_storage_claim_not_self_check CHECK (asset_id <> pool_id)
);

-- Every claim against one pool, which is the divide-by query.
CREATE INDEX idx_storage_claim_pool ON asset_storage_claim(pool_id);

-- +goose Down
DROP INDEX idx_storage_claim_pool;
DROP TABLE asset_storage_claim;
ALTER TABLE asset DROP CONSTRAINT asset_raw_capacity_check;
ALTER TABLE asset DROP COLUMN raw_capacity_gb;
ALTER TABLE asset DROP COLUMN storage_kind;
DROP TABLE storage_kind;
