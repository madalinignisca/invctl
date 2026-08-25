-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A batch identifier on change_log, added before the first bulk mutation
-- this codebase ships (WP-G7 piece 2 -- reassigning what a retiring team
-- owned) rather than after it.
--
-- WHY NOW, NOT WHEN PIECE 3 ADDS BULK ASSIGNMENT PROPER.
-- docs/ownership-report-design.md §4: "This cannot be added retroactively:
-- rows written without it lose their operational context permanently, and
-- change_log admits no UPDATE." The first bulk write is the last moment this
-- column can be added for free. Piece 2's reassignment (one source team, one
-- target, everything it owns) is the SIMPLER bulk case than piece 3's
-- filtered/grouped one, so establishing the column and the one INSERT it
-- touches here is safer than doing it under piece 3's added complexity.
--
-- NULLABLE, NO CHECK. Every write that is not part of a bulk operation --
-- which is nearly all of them -- leaves it NULL, and a NULL batch_id is not
-- a malformed row the way an empty entity_type or actor would be (see
-- 00009's CHECK (col <> '') idiom): it simply means "this row is its own
-- unit," which is the overwhelmingly common case and not an error.
--
-- ONE ROW PER ENTITY, NEVER ONE ROW PER BATCH. This column does not replace
-- the per-entity change_log rows a bulk operation writes -- each entity's
-- ownership change is still its own declared-state mutation and gets its
-- own row (design §4, "a single row saying 'assigned 11 things' is not an
-- audit trail, it is a receipt"). batch_id only makes the set of rows one
-- operation produced reconstructable afterwards.
--
-- INDEXED PARTIALLY, matching idx_asset_team and friends (see
-- docs/ownership-report-design.md §9's discussion of the same idiom): most
-- rows carry no batch_id, and an index across a column that is NULL on
-- effectively every row would cost far more to maintain than it saves for
-- the rare batch lookup it exists to serve.

-- +goose Up
ALTER TABLE change_log ADD COLUMN batch_id TEXT;

CREATE INDEX idx_changelog_batch ON change_log(batch_id) WHERE batch_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_changelog_batch;
ALTER TABLE change_log DROP COLUMN batch_id;
