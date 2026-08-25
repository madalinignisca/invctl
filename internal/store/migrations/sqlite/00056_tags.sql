-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Tags, piece 1 of WP-G4a (docs/tags-design.md). The tag itself and its
-- registry only -- applying a tag to an asset or a service (`entity_tag`,
-- piece 2) and filtering by tag (piece 3) are separate migrations. This one
-- creates nothing a tag could be attached to yet.
--
-- SHAPE MIRRORS custom_field (00051), DELIBERATELY: both are an
-- estate-defined vocabulary an administrator names before it can be used,
-- and the same failure mode -- an unexplained field nobody remembers
-- defining -- is why description is required here too.
--
-- description IS NOT NULL AND NOT EMPTY, for the identical reason
-- custom_field.description is: an administrator who cannot say why a tag
-- exists is the origin of the support call this feature defends against,
-- and the cheapest moment to ask is while they are creating it.
--
-- THE UNIQUE INDEX ON code IS PARTIAL, WHERE retired_at IS NULL, so a
-- retired code is free again -- the identical reasoning as
-- custom_field_live_code_key (00051): a plain UNIQUE would mean a retired
-- tag's name could never be reused. CREATE UNIQUE INDEX ... WHERE runs
-- unmodified on both engines; the shared test suite applies this migration
-- on both to prove it.
--
-- code IS EDITABLE, EVEN AFTER a tag has been applied to something (piece
-- 2). docs/tags-design.md §4 folds an entity's tag SET as the tag's stable
-- id, never its code, precisely so a rename here never rewrites every
-- entity that carries this tag -- the hazard docs/custom-fields-design.md
-- §7 already documents for field codes. Nothing in this schema or in
-- internal/domain/tag.go treats the code as immutable once set.
--
-- No colour column. docs/tags-design.md §6 permits one for display only,
-- with nothing ever depending on it -- deliberately left out of piece 1
-- rather than added unused; adding it later is one migration and touches
-- no fold, no CHECK and no uniqueness rule above.
--
-- retired_by REFERENCES app_user(id), same as custom_field.retired_by: an
-- opaque id resolved to a display name at render time, so a GDPR scrub of
-- that account leaves this row readable without a name to show for it.

-- +goose Up

CREATE TABLE tag (
  id          TEXT PRIMARY KEY NOT NULL,
  -- Machine name. Lower case, no whitespace or control characters --
  -- internal/domain/tag.go normalises and checks this before a row is ever
  -- written; the CHECK below is the second line of defence, not the first.
  -- Uniqueness while live is the partial index below.
  code        TEXT NOT NULL
                CONSTRAINT tag_code_check CHECK (code <> ''),
  label       TEXT NOT NULL
                CONSTRAINT tag_label_check CHECK (label <> ''),
  -- Why this tag exists. Not optional -- see the header note above.
  description TEXT NOT NULL
                CONSTRAINT tag_description_check CHECK (description <> ''),
  created_by  TEXT NOT NULL REFERENCES app_user(id),
  created_at  TEXT NOT NULL,
  retired_at  TEXT,
  retired_by  TEXT REFERENCES app_user(id),
  row_version INTEGER NOT NULL DEFAULT 1
);

-- A retired code is free again: WHERE retired_at IS NULL is the whole point.
CREATE UNIQUE INDEX tag_live_code_key ON tag (code) WHERE retired_at IS NULL;

-- The registry lists every tag, live and retired together, alphabetically.
CREATE INDEX idx_tag_code ON tag(code);

-- +goose Down
DROP INDEX idx_tag_code;
DROP INDEX tag_live_code_key;
DROP TABLE tag;
