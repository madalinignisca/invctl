-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- What sits above the panel: utility, generator, transfer switch, UPS.
--
-- THE FALSE NEGATIVE THIS EXISTS TO CLOSE. Migration 00023 stopped at the
-- panel, so two feeds from two different boards were treated as genuine
-- redundancy. In the ordinary 2N build -- UPS group A feeding boards A1..An,
-- UPS group B feeding B1..Bn, a generator behind both -- a dual-fed server
-- plugged into A1 and A2 is on two panels and was reported as fine. Both boards
-- are behind UPS group A. That is the more dangerous kind of wrong answer: the
-- tool actively reassures.
--
-- A CHAIN, NOT FIXED LEVELS. parent_id is nullable and self-referencing, so
-- utility → generator → UPS → panel is one shape among many and an estate with
-- a transfer switch, or two levels of UPS, or none, describes itself without
-- the schema having opinions about how many rungs a ladder has.
--
-- NOT EVERY SHARED ANCESTOR IS A FAULT, which is why `kind` is here and is
-- behavioural rather than a vocabulary. Two inputs converging on a UPS die
-- together and that is a finding. Two inputs converging only at the generator
-- is the DESIGN -- the generator is what makes a utility failure survivable, and
-- reporting it as a single point of failure would be reporting the safety
-- measure as the hazard. The finding grades itself by what is shared.
--
-- A SOURCE MAY ALSO BE AN ASSET, optionally. A UPS is a box with a serial and
-- batteries that expire, and battery end-of-life is one of the few hardware
-- dates that genuinely bites. Linking it means the catalogue, the expiry report
-- and the per-asset support-contract override all cover it for the price of one
-- nullable column. Left empty, the source is purely a topology node.

-- +goose Up
CREATE TABLE power_source (
  id          TEXT NOT NULL PRIMARY KEY,
  -- The thing feeding this one. NULL is the top of a chain: usually the
  -- utility, sometimes a generator in an estate that models no further up.
  parent_id   TEXT REFERENCES power_source(id),
  -- Where it is. An asset, like a panel's site, because the containment tree
  -- already answers "where".
  site_id     TEXT NOT NULL REFERENCES asset(id),
  -- The same thing as an inventory item, when somebody has catalogued it.
  asset_id    TEXT REFERENCES asset(id),
  name        TEXT NOT NULL,
  kind        TEXT NOT NULL
                CONSTRAINT power_source_kind_check
                CHECK (kind IN ('utility','generator','transfer_switch','ups')),
  notes       TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT power_source_lifecycle_check
                CHECK (lifecycle IN ('planned','active','maintenance','deprecated','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT power_source_name_check CHECK (name <> ''),
  -- A source cannot feed itself. Deeper cycles are checked in Go, where the
  -- walk can be bounded and the message can name the loop.
  CONSTRAINT power_source_self_check CHECK (parent_id IS NULL OR parent_id <> id)
);

CREATE UNIQUE INDEX power_source_site_name_key
  ON power_source(site_id, name)
  WHERE lifecycle <> 'retired';

CREATE INDEX idx_power_source_site   ON power_source(site_id);
CREATE INDEX idx_power_source_parent ON power_source(parent_id);

-- NULLABLE, and it stays nullable. An estate that has recorded its boards but
-- not yet what is behind them is the normal starting point, and the findings
-- report says how many panels are in that state rather than guessing.
ALTER TABLE power_panel ADD COLUMN source_id TEXT REFERENCES power_source(id);

CREATE INDEX idx_power_panel_source ON power_panel(source_id);

-- +goose Down
DROP INDEX idx_power_panel_source;
ALTER TABLE power_panel DROP COLUMN source_id;
DROP INDEX idx_power_source_parent;
DROP INDEX idx_power_source_site;
DROP INDEX power_source_site_name_key;
DROP TABLE power_source;
