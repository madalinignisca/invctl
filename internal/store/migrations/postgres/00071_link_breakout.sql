-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Breakout cables (docs/breakout-cables-design.md, decided 2026-09-17). A
-- QSFP-to-4xSFP+ DAC is one cable with one end on one side and four on the
-- other -- five terminations -- and `link` had no arrangement of its two
-- foreign-key columns that could hold that. The ruling was Option B: n `link`
-- rows sharing a breakout id, not a parallel `cable`/`cable_termination`
-- model (Option A, the shape migration 00028 already refused for the same
-- reason -- see the design doc D1). Four strands, four pairs of ends, one
-- assembly; the store enforces that all four agree with each other.
--
-- BOTH COLUMNS NULLABLE, AND ONLY EVER BOTH NULL OR BOTH SET. An ordinary
-- two-ended cable carries neither -- domain.Link.Validate refuses a position
-- with no group or a group with no position, because either half alone is a
-- half-written breakout, not a smaller kind of cable.
--
-- THIS HALF NEEDS NO REBUILD-AVOIDANCE ARGUMENT -- PostgreSQL has no
-- create-copy-drop-rename step anywhere in this schema's history, so nothing
-- here can be silently dropped the way sqlite/00005 would drop a `link`
-- column added from migrations/shared/. Kept in the dialect pair anyway
-- (never shared/), matching the SQLite half exactly, because the two must
-- stay one file per version or TestEveryDialectMigrationHasBothHalves fails.
--
-- THE PARTIAL UNIQUE INDEX ON POSITION IS SCOPED TO LIVE ROWS, the identical
-- idiom port_pass_through.position and backend_pool(service_id, name) already
-- use and for the identical reason: a retired strand must not reserve its
-- position forever.
--
-- NO CHECK TIES breakout_id TO breakout_position HERE, and the richer
-- invariants around a breakout (all live members share medium and length_m,
-- one a-end, distinct b-ends) are enforced in Go inside CreateBreakout and
-- UpdateLink -- see the SQLite half's header for the full reasoning.

-- +goose Up
ALTER TABLE link ADD COLUMN breakout_id TEXT;
ALTER TABLE link ADD COLUMN breakout_position INTEGER
  CONSTRAINT link_breakout_position_check CHECK (breakout_position IS NULL OR breakout_position > 0);
CREATE INDEX idx_link_breakout ON link(breakout_id) WHERE breakout_id IS NOT NULL;
CREATE UNIQUE INDEX link_breakout_position_key
  ON link(breakout_id, breakout_position)
  WHERE breakout_id IS NOT NULL AND lifecycle <> 'retired';

-- +goose Down
DROP INDEX link_breakout_position_key;
DROP INDEX idx_link_breakout;
ALTER TABLE link DROP COLUMN breakout_position;
ALTER TABLE link DROP COLUMN breakout_id;
