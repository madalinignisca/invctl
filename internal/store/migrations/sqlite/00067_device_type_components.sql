-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A device type's component template: the ports and power inputs every
-- instance of a model has. DECLARED -- somebody read a datasheet and asserted
-- that this model carries this port, the same class as device_type's own
-- physical columns (migration 00038). Nothing observes a template and nothing
-- derives one; Task 4 reads it to seed the real interface/power_input rows an
-- asset gets when it is created from this device type, but the template row
-- itself is intent, not a report about any physical box.
--
-- THE UNIQUE INDEX IS LIVE-SCOPED, same reasoning as 00064's prefix/ip_address
-- pair: a name withdrawn from a template must not keep its slot reserved
-- forever. A datasheet gets corrected -- "eth3" turns out not to exist on the
-- 1U variant -- and the fix is to retire that row, the same soft-delete-only
-- rule as everywhere else in this schema. If the unique index bound every row
-- regardless of lifecycle, the corrected template could never re-declare
-- "eth3" under a fresh row: CreateDeviceTypeComponent would answer "that name
-- is already declared on this model" pointing at a row the operator withdrew
-- and cannot see the point of undoing. Scoping to `lifecycle = 'active'` is
-- what keeps a withdrawal reversible in the sense that matters -- the name
-- becomes available again -- without ever deleting the retired row's history.
--
-- device_type_id is not itself soft-deletable in the sense that matters here:
-- a component belongs to the type row, not to any instantiated asset, so
-- retiring one never touches anything already brought into existence by
-- ExpandRange or a prior instantiation (Task 4's job, not this migration's).

-- +goose Up
CREATE TABLE device_type_component (
  -- NOT NULL IS EXPLICIT, not merely implied by PRIMARY KEY: SQLite does not
  -- infer it the way PostgreSQL does, and without it several rows could hold
  -- a NULL id -- each invisible to every statement keyed on that column, since
  -- SQLite treats NULLs as distinct in a unique index, while still counting
  -- towards every total. TestColumnShapesMatchAcrossEngines is what catches a
  -- column definition that would otherwise pass on both engines and diverge
  -- in what it actually permits.
  id              TEXT PRIMARY KEY NOT NULL,
  device_type_id  TEXT NOT NULL REFERENCES device_type(id),
  kind            TEXT NOT NULL
                    CONSTRAINT dtc_kind_check CHECK (kind IN ('interface','power_input')),
  name            TEXT NOT NULL,
  position        INTEGER NOT NULL,
  form_factor     TEXT REFERENCES interface_form_factor(code),
  speed_mbps      INTEGER,
  is_mgmt         BOOLEAN NOT NULL DEFAULT FALSE,
  draw_va         INTEGER,
  lifecycle       TEXT NOT NULL DEFAULT 'active'
                    CONSTRAINT dtc_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  row_version     INTEGER NOT NULL DEFAULT 1,

  -- An interface component MUST name a form factor: interface.form_factor is
  -- NOT NULL with a foreign key into interface_form_factor, so a component
  -- without one is accepted by the template and then refused at INSTANTIATION,
  -- which fails every attempt to create an asset of that model. The constructor
  -- is the first line of defence; this is the second.
  --
  -- A TABLE constraint, so it sits after every column definition rather than
  -- among them -- placing it mid-list is a syntax error on both engines.
  CONSTRAINT dtc_interface_form_factor_check
    CHECK (kind <> 'interface' OR form_factor IS NOT NULL)
);
CREATE UNIQUE INDEX dtc_name_key ON device_type_component(device_type_id, kind, name)
  WHERE lifecycle = 'active';
CREATE INDEX dtc_type ON device_type_component(device_type_id);

-- +goose Down
DROP INDEX dtc_type;
DROP INDEX dtc_name_key;
DROP TABLE device_type_component;
