-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- An estate-specific attribute without a migration (WP-A4, custom-fields-design.md §2).
--
-- THE CONSTRAINT THAT SHAPES THIS IS NOT THE FEATURE, IT IS WHO SEES IT NEXT.
-- A company defines a handful of custom fields, then hires somebody new. That
-- person opens an asset page, sees "Cost Centre", assumes it is part of
-- invctl, and when it behaves unexpectedly they call the vendor. Every
-- decision below that looks like extra work -- the description column, the
-- attribution columns, keeping values after retirement -- exists so the
-- product can answer "who added this, when, and why" without a phone call.
--
-- THREE TABLES. NO COLUMN IS ADDED TO asset OR service. A definition
-- (custom_field), the vocabulary a `select` field offers (custom_field_option),
-- and the value an entity holds (custom_field_value). One value column, not
-- four typed ones: typed columns would need a constraint saying exactly one is
-- non-null, and num_nonnulls() is forbidden here -- the alternative is a CASE
-- expression that must behave identically on both engines for something a
-- Go-side validator already guards. value_text stores everything, the same way
-- every id and every timestamp in this schema is stored as text.
--
-- description IS NOT NULL AND NOT EMPTY. An administrator who cannot say why a
-- field exists is the origin of the support call this whole feature is built
-- against, and the cheapest moment to ask is while they are creating it.
--
-- THE UNIQUE INDEX ON (entity_type, code) IS PARTIAL, WHERE retired_at IS
-- NULL, so a retired code can be created again later. A plain UNIQUE would
-- refuse that and an operator could never reuse a name they had retired.
-- CREATE UNIQUE INDEX ... WHERE is identical syntax on both engines -- SQLite
-- has supported partial indexes since 3.8.0 and PostgreSQL since 7.2, both far
-- below anything this repo targets -- and the shared test suite applies this
-- migration on both to prove it rather than assume it.
--
-- entity_id ON custom_field_value CARRIES NO FOREIGN KEY, because it points at
-- either an asset or a service. That is the established shape here --
-- change_log, asset_health and observed_transition all reference entities as
-- (entity_type, entity_id) with no FK -- and orphans cannot arise from
-- deletion because neither assets nor services are ever hard-deleted.
-- entity_type LIVES ON THE DEFINITION ONLY, never duplicated onto the value
-- row, so there is one source of truth rather than two that can drift apart.
--
-- WHAT THIS MIGRATION DOES NOT DO. It creates no index, trigger or constraint
-- that enforces `kind`-specific value formatting, an option belonging to a
-- `select` field, or a value's field and entity_type agreeing -- those are
-- exactly the checks a domain constructor performs before a row is ever
-- written (design.md §3), and the CHECK constraints here are the second line
-- of defence, never the first. This migration is schema only; Task 2 defines
-- the matching Go constant sets and Task 3 the store methods that write here.

-- +goose Up

CREATE TABLE custom_field (
  id          TEXT PRIMARY KEY NOT NULL,
  entity_type TEXT NOT NULL
                CONSTRAINT custom_field_entity_type_check CHECK (entity_type IN ('asset','service')),
  -- Machine name, lower case. Uniqueness while live is the partial index below.
  code        TEXT NOT NULL,
  label       TEXT NOT NULL,
  kind        TEXT NOT NULL
                CONSTRAINT custom_field_kind_check CHECK (kind IN ('text','number','date','boolean','select')),
  -- Why this field exists. Not optional -- see the header note above.
  description TEXT NOT NULL
                CONSTRAINT custom_field_description_check CHECK (description <> ''),
  created_by  TEXT NOT NULL REFERENCES app_user(id),
  created_at  TEXT NOT NULL,
  retired_at  TEXT,
  retired_by  TEXT REFERENCES app_user(id),
  row_version INTEGER NOT NULL DEFAULT 1
);

-- A retired code is free again: WHERE retired_at IS NULL is the whole point.
CREATE UNIQUE INDEX custom_field_live_code_key
  ON custom_field (entity_type, code) WHERE retired_at IS NULL;

-- The registry lists fields per entity type, live and retired together.
CREATE INDEX idx_custom_field_entity_type ON custom_field(entity_type);

-- The vocabulary a `select` field offers. Rows are never deleted, only
-- retired -- "Retiring a select option follows the same rule as retiring a
-- field: existing values keep displaying, no new value may select it."
CREATE TABLE custom_field_option (
  id         TEXT PRIMARY KEY NOT NULL,
  field_id   TEXT NOT NULL REFERENCES custom_field(id),
  -- The value a custom_field_value row stores when this option is chosen.
  value      TEXT NOT NULL,
  label      TEXT NOT NULL,
  position   INTEGER NOT NULL,
  retired_at TEXT
);

CREATE UNIQUE INDEX custom_field_option_field_id_value_key
  ON custom_field_option(field_id, value);
CREATE INDEX idx_custom_field_option_field ON custom_field_option(field_id);

-- The current value of one field on one entity. This is a set the field owns,
-- the same shape as asset_environment and dependency_data_class: delete then
-- insert inside the entity's own transaction is correct, and the entity's
-- change_log entry must record the change (design.md §6 -- folded into
-- assetAudit.CustomFields / serviceAudit.CustomFields, not left implicit).
CREATE TABLE custom_field_value (
  id          TEXT PRIMARY KEY NOT NULL,
  field_id    TEXT NOT NULL REFERENCES custom_field(id),
  -- No REFERENCES: entity_id points at asset.id or service.id, per the
  -- established (entity_type, entity_id) shape used with no FK elsewhere in
  -- this schema. entity_type lives on custom_field only.
  entity_id   TEXT NOT NULL,
  value_text  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX custom_field_value_field_id_entity_id_key
  ON custom_field_value(field_id, entity_id);
-- "What custom values does this entity hold" is the page render's query.
CREATE INDEX idx_custom_field_value_entity ON custom_field_value(entity_id);

-- +goose Down
DROP INDEX idx_custom_field_value_entity;
DROP INDEX custom_field_value_field_id_entity_id_key;
DROP TABLE custom_field_value;

DROP INDEX idx_custom_field_option_field;
DROP INDEX custom_field_option_field_id_value_key;
DROP TABLE custom_field_option;

DROP INDEX idx_custom_field_entity_type;
DROP INDEX custom_field_live_code_key;
DROP TABLE custom_field;
