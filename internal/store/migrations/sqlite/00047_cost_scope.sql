-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Not every cost divides across everything (WP-J4, COST-ATTRIBUTION.md §5.6).
--
-- THIS RULE EXISTS BECAUSE A WORKED EXAMPLE BROKE THE PREVIOUS DRAFT WITHIN
-- MINUTES OF MEETING REAL INFRASTRUCTURE. §5.1 said "cluster cost ÷ usable
-- capacity", and that is wrong for any cost only some consumers benefit from. A
-- per-core datacentre operating-system licence granting unlimited guests
-- benefits ONLY the guests running that operating system; divide it evenly and
-- every workload running something else silently subsidises the ones that do.
--
-- The failure is in the direction nobody checks: the workloads carrying no
-- expensive licence look more expensive than they are, and the ones carrying it
-- look cheaper. Nothing about the total is wrong, so nothing prompts a reader
-- to look.
--
-- ON asset_cost ONLY, of the four cost tables. A cluster's shared cost is the
-- lines on its member HOSTS -- that is what "the cost of the cluster" means and
-- it is the only pool this divides. A service, project or circuit cost attaches
-- to something that is already the unit of attribution, so there is nothing to
-- subdivide and a scope column on those would be a field with no reader.
--
-- THREE SHAPES, AND THE THIRD DIVIDES DIFFERENTLY FROM THE SECOND:
--
--   universal     every guest benefits. Divides across the whole capacity.
--   conditional   only the named guests benefit, in proportion to what they
--                 hold. A per-core OS or database licence.
--   per_consumer  only the named guests, EQUALLY, per head. A backup product
--                 licensed per virtual machine costs the same for a large VM
--                 as for a small one, so dividing it by capacity share would
--                 charge the big one several times over for one licence.
--
-- DEFAULTED TO universal ON EXISTING ROWS, deliberately and with the cost
-- stated: it is what the arithmetic implicitly assumed before this column
-- existed, and it is genuinely right for hardware, power and rack space, which
-- is most of what is recorded. It is NOT right for a licence somebody has
-- already entered, and nothing here can detect which lines those are -- a
-- backfill that guessed would be laundering a default into a declaration. The
-- honest position is that scope is unreviewed on every pre-existing line until
-- a person looks at it.

-- +goose Up

ALTER TABLE asset_cost ADD COLUMN applies_to TEXT NOT NULL DEFAULT 'universal';

ALTER TABLE asset_cost ADD CONSTRAINT asset_cost_applies_to_check
  CHECK (applies_to IN ('universal', 'conditional', 'per_consumer'));

-- Which consumers a non-universal line applies to.
--
-- A SET OWNED BY THE COST LINE, replaced wholesale inside the line's own
-- transaction and folded into its audited value -- the rule CLAUDE.md states
-- and that this codebase has broken often enough to have a test for it.
--
-- The consumer is an ASSET rather than a project: a licence covers machines,
-- and which project those machines belong to is a separate declaration that can
-- change without the licence changing. Attributing through the asset means the
-- answer follows ownership automatically instead of going stale.
CREATE TABLE asset_cost_consumer (
  cost_id    TEXT NOT NULL REFERENCES asset_cost(id),
  asset_id   TEXT NOT NULL REFERENCES asset(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (cost_id, asset_id)
);

-- Every consumer of one line, which is the divide-across query.
CREATE INDEX idx_cost_consumer_cost ON asset_cost_consumer(cost_id);

-- +goose Down
DROP INDEX idx_cost_consumer_cost;
DROP TABLE asset_cost_consumer;
ALTER TABLE asset_cost DROP CONSTRAINT asset_cost_applies_to_check;
ALTER TABLE asset_cost DROP COLUMN applies_to;
