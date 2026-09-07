-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- What a patch panel does: front port in, rear port out.
--
-- ONE TABLE, AND NO NEW PORT MODEL. A panel's ports are ordinary `interface`
-- rows -- form_factor is already an editable vocabulary, so `lc` and `sc` are
-- data rather than a migration -- and `link` already joins two interfaces with a
-- medium and a length and retires properly. What was missing was never the
-- cable; it was the thing in the middle. Without it a link goes interface to
-- interface and a patch panel is invisible, so a trunk cut looks like nothing at
-- all.
--
-- The alternative was NetBox's shape: front_port and rear_port tables, and a
-- cable whose ends point at "some kind of port". That needs a polymorphic
-- reference -- a type column plus an id -- which is the one join shape this
-- codebase has avoided everywhere, and it would put a second cable model beside
-- `link` for the two to disagree over. One small table over two existing edge
-- types buys the same answers.
--
-- POSITION IS WHAT WP-B4'S TRACER READS. A twelve-fibre MPO trunk breaking out
-- to twelve LC front ports is ordinary, and without a position the rear port
-- could only ever carry one front port -- adding it after the fact would have
-- meant rewriting the uniqueness rule underneath live data. Default 1 is the
-- 1:1 case, which is still the common one; the column and the index below were
-- both already exactly what breakout needed, so WP-B4 added no migration.
--
-- BOTH ENDS MUST BE ON ONE ASSET. A pass-through is what happens INSIDE a panel;
-- two ports on different boxes are joined by a cable, which is what `link` is
-- for. Enforced in Go, because a CHECK cannot reach another table.

-- +goose Up
CREATE TABLE port_pass_through (
  id                 TEXT NOT NULL PRIMARY KEY,
  front_interface_id TEXT NOT NULL REFERENCES interface(id),
  rear_interface_id  TEXT NOT NULL REFERENCES interface(id),
  position           INTEGER NOT NULL DEFAULT 1
                       CONSTRAINT port_pass_through_position_check CHECK (position > 0),
  lifecycle          TEXT NOT NULL DEFAULT 'active'
                       CONSTRAINT port_pass_through_lifecycle_check
                       CHECK (lifecycle IN ('active','retired')),
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  row_version        INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT port_pass_through_distinct_check
    CHECK (front_interface_id <> rear_interface_id)
);

-- A front port passes through to exactly one rear port. Live rows only, the
-- same rule every natural key here follows: a retired patch must not hold a
-- port against the one that replaced it.
CREATE UNIQUE INDEX port_pass_through_front_key
  ON port_pass_through(front_interface_id)
  WHERE lifecycle <> 'retired';

-- And a given position on a rear port takes exactly one front port. With the
-- default position of 1 this is "one front port per rear port"; it is also
-- exactly the rule a breakout needs, one row per strand.
CREATE UNIQUE INDEX port_pass_through_rear_key
  ON port_pass_through(rear_interface_id, position)
  WHERE lifecycle <> 'retired';

CREATE INDEX idx_pass_through_rear ON port_pass_through(rear_interface_id);

-- +goose Down
DROP INDEX idx_pass_through_rear;
DROP INDEX port_pass_through_rear_key;
DROP INDEX port_pass_through_front_key;
DROP TABLE port_pass_through;
