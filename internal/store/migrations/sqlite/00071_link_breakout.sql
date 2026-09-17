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
-- THE TRAP THIS MIGRATION DOES NOT REPEAT (docs/superpowers/plans/
-- 2026-09-17-breakout-cables.md, and confirmed by reading sqlite/00005
-- directly): Migrate() runs every migrations/shared/*.sql file before ANY
-- dialect file runs, and sqlite/00005_named_constraints.sql already rebuilt
-- `link` once with a create-copy-drop-rename that names its OWN, original
-- column list (`ALTER TABLE link_new RENAME TO link`). A column added to
-- `link` from migrations/shared/ would be silently absent on a fresh SQLite
-- install the moment 00005 replayed, while every already-migrated database
-- kept the column and looked fine -- exactly what cost migration 00070 (on
-- the write-surface-gaps branch) a full rewrite for `backend_pool` and
-- `route`. Both columns go here, in the dialect pair, never in shared/.
--
-- THE PARTIAL UNIQUE INDEX ON POSITION IS SCOPED TO LIVE ROWS, the identical
-- idiom port_pass_through.position and backend_pool(service_id, name) already
-- use and for the identical reason: a retired strand must not reserve its
-- position forever. Pull position 3 of a four-way breakout, and a new strand
-- must be able to take position 3 again without the old, retired row's index
-- entry blocking it.
--
-- NO CHECK TIES breakout_id TO breakout_position HERE -- SQLite and
-- PostgreSQL can each express "both null or both set" as a CHECK
-- (comparing IS NULL on both columns), but the richer invariants around a
-- breakout (all live members share medium and length_m, one a-end, distinct
-- b-ends) need to see SIBLING rows, which no single-row CHECK or partial
-- index can do on either engine -- the identical limit migration 00068's
-- header already records for one-bundle-per-cable. Enforced in Go, in
-- CreateBreakout and UpdateLink (internal/store/network.go), inside their
-- own transactions -- one chokepoint, not a constraint. The both-null-or-
-- both-set half doesn't need a sibling row at all, so it IS checked at the
-- domain layer (domain.Link.Validate) as the first line of defence.

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
