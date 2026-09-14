-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Cable bundles (docs/cable-bundles-design.md, WP-B4's bundle half). A bundle
-- groups link rows -- cables somebody pulled together and will replace
-- together: a duct, a tray, a trunk. It groups an existing fact (link) and is
-- itself a fact somebody declares; nothing instantiates or derives a bundle.
--
-- DECLARED THROUGHOUT. No source/confidence/first_seen/last_seen columns:
-- unlike net_group or dependency, nothing ever proposes a bundle from
-- observed data -- it exists only because a person said "these cables run
-- together", so there is no provenance question to answer.
--
-- id TEXT NOT NULL PRIMARY KEY: the NOT NULL is explicit rather than implied,
-- because SQLite's PRIMARY KEY does NOT imply NOT NULL the way PostgreSQL's
-- does -- found the hard way on migration 00067's device_type_component.
-- Without it here, several rows could hold a NULL id, each invisible to any
-- statement keyed on that column (SQLite treats NULLs as distinct in a unique
-- index) while still counting towards every total.
--
-- THE CODE INDEX IS LIVE-SCOPED, the shape migration 00064 established: a
-- withdrawn bundle must not reserve its name forever. A duct gets relabelled
-- or a bundle record was created in error, and the fix is to retire that row
-- -- the same soft-delete-only rule as everywhere else in this schema. If the
-- unique index bound every row regardless of lifecycle, a corrected bundle
-- could never re-declare its code under a fresh row.
--
-- ONE BUNDLE PER CABLE, Gabriel's decision (docs/cable-bundles-design.md):
-- cable_bundle_member's unique index is on link_id ALONE, not on
-- (bundle_id, link_id). A bundle models the physical run a cable was pulled
-- in, and a cable is in exactly one of those -- so "what else goes with this
-- cable" is a single unambiguous answer, and an accidental double-add is a
-- refusal rather than a quietly confusing result the impact page would have
-- to explain as a union.
--
-- MEMBERSHIP CARRIES NO LIFECYCLE OF ITS OWN and is never deleted piecemeal:
-- CLAUDE.md's rule is that a set table is replaced wholesale inside its
-- parent's transaction, folded into the bundle's audited value, so Task 2's
-- SetBundleMembers always writes the row that decides audit scope. A retired
-- link stays in its bundle -- membership records what was pulled together,
-- and a withdrawn cable is still part of that history; the impact view
-- filters live links itself, this table does not.
--
-- RETIRING A BUNDLE RETIRES NOTHING ELSE. "We stopped managing these
-- together" is not "somebody pulled the cables" -- cascading would write
-- change_log entries attributing cable removals to whoever withdrew the
-- grouping, the same misattribution RetireInterface and RetireEnvironment
-- both refuse.

-- +goose Up
CREATE TABLE cable_bundle (
  id          TEXT NOT NULL PRIMARY KEY,   -- NOT NULL explicitly: SQLite's
                                           -- PRIMARY KEY does not imply it
  code        TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT cable_bundle_lifecycle_check
                CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX cable_bundle_code_key ON cable_bundle(code)
  WHERE lifecycle = 'active';

CREATE TABLE cable_bundle_member (
  bundle_id TEXT NOT NULL REFERENCES cable_bundle(id),
  link_id   TEXT NOT NULL REFERENCES link(id),
  PRIMARY KEY (bundle_id, link_id)
);
-- ONE BUNDLE PER CABLE. A bundle models the run a cable was physically pulled
-- in, and a cable is in one of those. It makes "what else goes with this"
-- a single unambiguous answer rather than a union the impact page would have
-- to explain, and an accidental double-add a refusal rather than a quietly
-- confusing result.
CREATE UNIQUE INDEX cable_bundle_member_link_key ON cable_bundle_member(link_id);

-- +goose Down
DROP INDEX cable_bundle_member_link_key;
DROP TABLE cable_bundle_member;
DROP INDEX cable_bundle_code_key;
DROP TABLE cable_bundle;
