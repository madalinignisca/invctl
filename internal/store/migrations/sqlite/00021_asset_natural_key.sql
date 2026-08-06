-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- An asset's name is unique among its live siblings.
--
-- Until now an asset had no natural key at all. `name` carried no constraint,
-- `asset_tag` and `serial` are nullable and unconstrained, and the observed
-- path resolves an entity by id -- so nothing in this system could name an
-- asset except by the UUID it was given. That is fine while a person is
-- clicking, and it is the blocker for bulk import: a spreadsheet does not know
-- our UUIDs, and an import keyed on one is an import nobody can write.
--
-- WHY (parent_id, name) AND NOT A GLOBAL NAME. It is already the pattern twice
-- in this schema -- interface(asset_id, name) and endpoint(service_id, name) --
-- and it is the one that survives contact with a real estate. Two racks called
-- R1 in different sites is normal. So is web-01 in two clusters. A global
-- unique name forbids both forever, and loosening a unique constraint later
-- means reconciling whatever collided in the meantime.
--
-- It also gives an import file a key it can actually express: a path, dc1 /
-- rack-a / esx-01, resolved top-down. Parents must be created before children,
-- which the dry run can check against the file before writing anything.
--
-- TWO INDEXES, AND BOTH ARE PARTIAL. Neither half is optional:
--
--   1. RETIRED ROWS ARE EXCLUDED. Entities here are soft-deleted, never
--      removed, so a plain unique index would let a retired rack-1 hold its own
--      name against every future rack-1 in that site -- a name permanently
--      spent on a row nobody can see and nobody can delete. Uniqueness is a
--      statement about what exists, and a retired asset does not.
--
--   2. TOP-LEVEL ASSETS NEED THEIR OWN. SQL treats NULLs as distinct, on both
--      engines, so a composite index on (parent_id, name) constrains nothing at
--      all when parent_id IS NULL -- which is exactly the sites and other roots.
--      Without the second index the rule would hold everywhere except the layer
--      everything else hangs off.
--
-- Partial unique indexes rather than a table constraint: both engines support
-- CREATE UNIQUE INDEX ... WHERE, so this needs no SQLite table rebuild and the
-- two dialect files stay byte-identical. A CHECK could not express either half.

-- +goose Up
CREATE UNIQUE INDEX asset_parent_name_key
  ON asset(parent_id, name)
  WHERE lifecycle <> 'retired';

CREATE UNIQUE INDEX asset_root_name_key
  ON asset(name)
  WHERE parent_id IS NULL AND lifecycle <> 'retired';

-- +goose Down
DROP INDEX asset_root_name_key;
DROP INDEX asset_parent_name_key;
