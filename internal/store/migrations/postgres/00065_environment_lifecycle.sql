-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- An environment declared in error was permanent. The write-surface census
-- (internal/store/write_surface_test.go) carried it as a decision deferred,
-- not a decision made: "no withdrawal ... but the answer today is that nobody
-- can, which is not the same as having decided." That decision has now been
-- made.
--
-- A LABEL, NOT AN OCCUPANCY -- and that is what makes this migration additive
-- and nothing else. Six tables carry environment_id (asset_environment,
-- service, net_group, net_anchor, vlan, prefix); RetireEnvironment refuses
-- nothing and rewrites nothing there. Compare RetireInterface and RetirePrefix,
-- which refuse while something is attached: those entities are OCCUPIED, and
-- unpatching or re-homing first is the honest order of the physical act.
-- Retiring "staging" is not unplugging anything -- it is saying the label is no
-- longer in current use, while every asset, service and network still wearing
-- it keeps wearing it. Refusing here would mean forcing a person to relabel
-- every one of those rows by hand first, and rewriting them automatically
-- would write change_log entries attributing a relabelling to whoever clicked
-- withdraw -- exactly the misattribution RetireInterface's own doc comment
-- refuses to commit.
--
-- Additive with a default, so every existing row reads active and no
-- constraint can fail on data a visitor to the public demo created.

-- +goose Up
ALTER TABLE environment ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT environment_lifecycle_check CHECK (lifecycle IN ('active','retired'));
CREATE INDEX idx_environment_lifecycle ON environment(lifecycle);

-- +goose Down
DROP INDEX idx_environment_lifecycle;
ALTER TABLE environment DROP COLUMN lifecycle;
