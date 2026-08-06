-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A hardware catalogue: manufacturers and the models they make.
--
-- WHAT THIS IS ACTUALLY FOR. Today `vendor` and `model` are free text on the
-- asset, so the same switch is spelled four ways across forty rows, and
-- `eol_date` is per asset -- which means it has to be typed forty times, which
-- means it is typed zero times, which is why the expiry report is quiet about
-- exactly the hardware most likely to bite. A model is entered once and every
-- asset of that model inherits from it.
--
-- ONE DATE, NOT TWO. NetBox distinguishes end-of-sale from end-of-support.
-- invctl's eol_date already means "when this stops being supportable", which is
-- the question this system exists to answer, and adding a second date would
-- make every report choose between them. When somebody genuinely needs the
-- purchasing question, that is a new column with its own report, not a
-- reinterpretation of this one.
--
-- INHERITANCE IS AN OVERRIDE, AND THE DIRECTION MATTERS. An asset's own
-- eol_date WINS over its type's. The type carries what the manufacturer
-- published; the asset carries what THIS box is actually supported to, which a
-- private support contract can extend well past the model's date or a damaged
-- unit can cut short. The general fact is the fallback; the specific assertion
-- is the answer.
--
-- Which is why the resolved date is never enough on its own: every view that
-- shows one has to say WHERE IT CAME FROM. "Out of support in March" and "its
-- model is out of support in March and nobody has checked this box against the
-- contract" send you to different people. Same provenance argument as
-- source/confidence in docs/AUDIT.md, applied to a date.

-- +goose Up
CREATE TABLE manufacturer (
  id          TEXT NOT NULL PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  -- A support portal, a partner login, a contract reference. NEVER a person and
  -- never a credential: the same rule team.contact_ref holds to.
  support_ref TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT manufacturer_code_check      CHECK (code <> ''),
  CONSTRAINT manufacturer_name_check      CHECK (name <> ''),
  CONSTRAINT manufacturer_lifecycle_check CHECK (lifecycle IN ('planned','active','deprecated','retired'))
);

CREATE TABLE device_type (
  id              TEXT NOT NULL PRIMARY KEY,
  manufacturer_id TEXT NOT NULL REFERENCES manufacturer(id),
  model           TEXT NOT NULL,
  part_number     TEXT,
  -- Rack units, and nullable because plenty of things in an inventory do not
  -- occupy any: a blade, a chassis module, a virtual appliance. Zero would be a
  -- claim; NULL is the absence of one. WP-B5 reads this for elevations.
  u_height        INTEGER,
  full_depth      BOOLEAN NOT NULL DEFAULT FALSE,
  -- The manufacturer's end of support for this model. Inherited by assets that
  -- do not state their own; see the header.
  eol_date        TEXT,
  notes           TEXT,
  lifecycle       TEXT NOT NULL DEFAULT 'active',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  row_version     INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT device_type_model_check     CHECK (model <> ''),
  CONSTRAINT device_type_u_height_check  CHECK (u_height IS NULL OR u_height > 0),
  CONSTRAINT device_type_lifecycle_check CHECK (lifecycle IN ('planned','active','deprecated','retired'))
);

-- A model is unique within its manufacturer, among LIVE rows only -- the same
-- shape as the asset natural key in 00021, and for the same reason: a retired
-- row must not hold a name nobody can reuse, because nothing here is ever
-- deleted. It is also what lets an import name a model as `dell/r650`.
CREATE UNIQUE INDEX device_type_manufacturer_model_key
  ON device_type(manufacturer_id, model)
  WHERE lifecycle <> 'retired';

CREATE INDEX idx_device_type_eol ON device_type(eol_date);

-- NULLABLE, and it stays nullable. Most estates will catalogue the hardware
-- that matters and leave the rest, and an inventory that refuses to hold a
-- server until somebody has modelled its type is an inventory that stays empty.
ALTER TABLE asset ADD COLUMN device_type_id TEXT REFERENCES device_type(id);

CREATE INDEX idx_asset_device_type ON asset(device_type_id);

-- +goose Down
DROP INDEX idx_asset_device_type;
ALTER TABLE asset DROP COLUMN device_type_id;
DROP INDEX idx_device_type_eol;
DROP INDEX device_type_manufacturer_model_key;
DROP TABLE device_type;
DROP TABLE manufacturer;
