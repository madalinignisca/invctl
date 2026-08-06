-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- The power chain: panel → feed → input.
--
-- WHAT THIS IS FOR, and it is not the outage simulation. Losing a feed and
-- resolving that to the assets it carries is genuinely cheap -- impact.Request
-- already takes DownAssetIDs and the closure table already resolves containment,
-- so a feed becomes a failure target by writing a resolver, not by extending the
-- engine.
--
-- The finding worth building this for is the one nothing else produces: an asset
-- with an A input and a B input, believed redundant, whose feeds trace back to
-- the SAME PANEL. That is not redundancy, it is two cables to one point of
-- failure, and it is invisible in every system that records power as a text
-- field on a rack. Nobody discovers it during normal running. They discover it
-- during the panel's first and only failure.
--
-- THREE LEVELS, DELIBERATELY. Not four. NetBox models outlets as rows and cables
-- them to device ports, which answers "which socket is free" -- real, but
-- secondary, and the outlet-to-input relationship is a CABLE, which is WP-B3's
-- subject. Modelling it here would pre-empt a work package that has not been
-- designed. A PDU stays an ordinary asset with a power input like anything else.
--
-- THE PANEL IS THE TOP, FOR NOW. Two feeds from one panel is false redundancy;
-- two panels is treated as genuine. It is not the whole truth -- two panels
-- behind one UPS or one transfer switch is the subtler version of the same trap
-- -- but a supply layer above this is additive: a nullable parent on the panel,
-- and the trace walks one level further. Nothing entered now becomes wrong.

-- +goose Up

-- A distribution board. Its site is an asset -- a site, a room or a rack --
-- because that is where the containment tree already lives.
CREATE TABLE power_panel (
  id          TEXT NOT NULL PRIMARY KEY,
  site_id     TEXT NOT NULL REFERENCES asset(id),
  name        TEXT NOT NULL,
  -- The panel's own supply, as documentation. Capacity is computed per FEED,
  -- because a panel commonly carries feeds of different ratings and a single
  -- number on the panel would be a total nobody can act on.
  --
  -- All three are NULLABLE, and that is the honest shape: an estate frequently
  -- knows a panel exists long before anybody has read its rating off the door.
  -- "Not recorded" and "zero" must not be the same value -- a capacity silently
  -- read as zero would make every feed on it look over-allocated.
  voltage     INTEGER,
  amperage    INTEGER,
  phase       TEXT CONSTRAINT power_panel_phase_check
                CHECK (phase IS NULL OR phase IN ('single','three')),
  notes       TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT power_panel_lifecycle_check
                CHECK (lifecycle IN ('planned','active','maintenance','deprecated','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT power_panel_name_check     CHECK (name <> ''),
  CONSTRAINT power_panel_voltage_check  CHECK (voltage IS NULL OR voltage > 0),
  CONSTRAINT power_panel_amperage_check CHECK (amperage IS NULL OR amperage > 0)
);

-- Unique among LIVE siblings, the same rule and the same reason as the asset
-- natural key in 00021: nothing here is ever deleted, so a retired panel must
-- not hold its name against the one that replaced it.
CREATE UNIQUE INDEX power_panel_site_name_key
  ON power_panel(site_id, name)
  WHERE lifecycle <> 'retired';

CREATE INDEX idx_power_panel_site ON power_panel(site_id);

-- A circuit off a panel. This is the thing that fails.
CREATE TABLE power_feed (
  id          TEXT NOT NULL PRIMARY KEY,
  panel_id    TEXT NOT NULL REFERENCES power_panel(id),
  name        TEXT NOT NULL,
  voltage     INTEGER,
  amperage    INTEGER,
  phase       TEXT CONSTRAINT power_feed_phase_check
                CHECK (phase IS NULL OR phase IN ('single','three')),
  -- The fraction of the rating a continuous load may occupy, as a percent.
  -- 80 is the common derating and is the default; it is a column rather than a
  -- constant because the number is a local electrical decision, not ours.
  max_utilisation INTEGER NOT NULL DEFAULT 80
                    CONSTRAINT power_feed_max_utilisation_check
                    CHECK (max_utilisation > 0 AND max_utilisation <= 100),
  notes       TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT power_feed_lifecycle_check
                CHECK (lifecycle IN ('planned','active','maintenance','deprecated','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT power_feed_name_check     CHECK (name <> ''),
  CONSTRAINT power_feed_voltage_check  CHECK (voltage IS NULL OR voltage > 0),
  CONSTRAINT power_feed_amperage_check CHECK (amperage IS NULL OR amperage > 0)
);

CREATE UNIQUE INDEX power_feed_panel_name_key
  ON power_feed(panel_id, name)
  WHERE lifecycle <> 'retired';

CREATE INDEX idx_power_feed_panel ON power_feed(panel_id);

-- Where an asset takes power from. An asset with two of these on two feeds is
-- claiming redundancy; whether that claim holds is what the findings answer.
CREATE TABLE power_input (
  id          TEXT NOT NULL PRIMARY KEY,
  asset_id    TEXT NOT NULL REFERENCES asset(id),
  feed_id     TEXT NOT NULL REFERENCES power_feed(id),
  -- Almost always 'A' or 'B'. Free text because estates label them their own
  -- way, and a vocabulary here would be a lookup table with two rows in it.
  name        TEXT NOT NULL,
  -- Declared draw in volt-amps: a nameplate or allocated figure somebody typed.
  -- NOT observed -- nothing in the estate reports it here, and a measured draw
  -- arriving from a PDU would be observed state with a reporter and an age,
  -- which is a different contract entirely (docs/AUDIT.md). Nullable, because an
  -- unknown draw must stay distinguishable from a declared zero.
  draw_va     INTEGER,
  notes       TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT power_input_lifecycle_check
                CHECK (lifecycle IN ('planned','active','maintenance','deprecated','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT power_input_name_check CHECK (name <> ''),
  CONSTRAINT power_input_draw_check CHECK (draw_va IS NULL OR draw_va >= 0)
);

CREATE UNIQUE INDEX power_input_asset_name_key
  ON power_input(asset_id, name)
  WHERE lifecycle <> 'retired';

CREATE INDEX idx_power_input_asset ON power_input(asset_id);
CREATE INDEX idx_power_input_feed  ON power_input(feed_id);

-- +goose Down
DROP INDEX idx_power_input_feed;
DROP INDEX idx_power_input_asset;
DROP INDEX power_input_asset_name_key;
DROP TABLE power_input;
DROP INDEX idx_power_feed_panel;
DROP INDEX power_feed_panel_name_key;
DROP TABLE power_feed;
DROP INDEX idx_power_panel_site;
DROP INDEX power_panel_site_name_key;
DROP TABLE power_panel;
