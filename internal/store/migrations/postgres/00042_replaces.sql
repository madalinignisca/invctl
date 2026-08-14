-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- What this replaced (WP-J1).
--
-- ONE NULLABLE COLUMN, AND THE REPORT IS A JOIN. Both boxes already carry their
-- own cost lines, their own vendor and their own dates; the only fact nobody
-- had written down is that this one took over from that one. Once it is
-- recorded, "the box we bought in 2021 cost 4,200 and its successor cost 6,850"
-- is arithmetic over rows that already exist.
--
-- THIS IS WHY SOFT DELETE EARNS ITS KEEP. A predecessor is retired, never
-- deleted, so it is still there to compare against years later. Every argument
-- for hard deletion in this codebase would have destroyed exactly the history
-- this column exists to read.
--
-- NULLABLE AND UNCONSTRAINED IN THE OBVIOUS DIRECTION. Most assets replace
-- nothing, and an estate that has not recorded a lineage is not in error -- it
-- is the ordinary state, reported as a gap where it matters and never as a
-- fault.
--
-- ON DELETE SET NULL rather than CASCADE: if a predecessor ever were removed,
-- losing the pointer is right and taking its successor with it is catastrophic.
-- Nothing in this codebase hard-deletes an asset, so this is a guard against a
-- future mistake rather than a live path.
--
-- NO UNIQUE CONSTRAINT on the column, deliberately. Two assets replacing one
-- predecessor is real: a single large server retired in favour of two smaller
-- ones is a refresh, not a data error, and the comparison still works -- it
-- simply has two successors to weigh against one old price.

-- +goose Up
ALTER TABLE asset ADD COLUMN replaces_asset_id TEXT
  REFERENCES asset(id) ON DELETE SET NULL;

-- The reverse lookup: "what replaced this", which the retired box's own page
-- asks. Partial, because the overwhelming majority of rows are NULL and an
-- index over them would be mostly empty pages.
CREATE INDEX idx_asset_replaces ON asset(replaces_asset_id)
  WHERE replaces_asset_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_asset_replaces;
ALTER TABLE asset DROP COLUMN replaces_asset_id;
