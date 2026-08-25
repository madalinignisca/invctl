-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Applying tags to entities, piece 2 of WP-G4a (docs/tags-design.md §3, §4a).
-- The tag itself and its registry (migration 00056) create nothing this could
-- attach to; this is the attachment.
--
-- POLYMORPHIC, following journal_entry (00039), asset_health (00005) and
-- custom_field_value (00051): one entity_tag(tag_id, entity_type, entity_id)
-- rather than asset_tag/service_tag/project_tag. design.md §3 states the
-- trade-off explicitly and withdraws the first draft's defence of it -- soft
-- delete is a POLICY, not a schema constraint, so entity_id carries no
-- foreign key and nothing here stops a row pointing at nothing. That is
-- compensated, not ignored:
--
--   - entity_type carries a CHECK limiting it to the types that are actually
--     taggable today (asset, service, project). A typo cannot invent a new
--     entity kind, and adding a fourth taggable type later is a deliberate
--     migration against this CHECK, not an accident.
--   - A store-level integrity check (internal/store, tested on both engines)
--     finds entity_tag rows whose entity_id matches nothing, which is the
--     thing the missing foreign key would otherwise have done.
--
-- NO row_version AND NO retired_at ON THIS TABLE. It is not an entity in its
-- own right the way `tag` is -- it is one row of the SET an asset, service or
-- project carries, replaced wholesale the way asset_environment and
-- dependency_data_class already are. "Set and index tables are replaced
-- wholesale, and that is not deletion" (CLAUDE.md): removing a tag from an
-- entity deletes this row outright, and what MUST survive is the parent
-- entity's own change_log entry recording that its tag set moved -- see
-- internal/store/entitytags.go, which folds the set into assetAudit,
-- serviceAudit and the new projectAudit exactly the way custom field values
-- already fold into the first two.
--
-- created_by REFERENCES app_user(id): an opaque id, resolved to a display
-- name at render time like every other attribution column in this schema, so
-- a GDPR scrub of that account leaves the row readable without a name to
-- show for it.
--
-- PRIMARY KEY (entity_type, entity_id, tag_id), not (tag_id, entity_type,
-- entity_id): design.md §3 asks for two indexes, one on entity_tag(tag_id)
-- ("what is tagged dr") and one on entity_tag(entity_type, entity_id) ("what
-- tags does this entity carry"), and states the query plan is to be CHECKED
-- on both engines rather than assumed. Leading the primary key with
-- (entity_type, entity_id) serves the second query as a leftmost-prefix scan
-- of the primary key itself on both engines; the explicit index below serves
-- the first. Confirmed with EXPLAIN QUERY PLAN (SQLite) and EXPLAIN
-- (PostgreSQL) against a seeded table -- see internal/store/entitytags_test.go.

-- +goose Up

CREATE TABLE entity_tag (
  tag_id      TEXT NOT NULL REFERENCES tag(id),
  entity_type TEXT NOT NULL
                CONSTRAINT entity_tag_entity_type_check
                CHECK (entity_type IN ('asset', 'service', 'project')),
  -- No foreign key -- see the header note above. Compensated by a store-level
  -- integrity check, not by a constraint this schema cannot express without
  -- one physical table per taggable type.
  entity_id   TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  created_by  TEXT NOT NULL REFERENCES app_user(id),
  PRIMARY KEY (entity_type, entity_id, tag_id)
);

-- "What is tagged dr" -- the primary key above does not serve this as a
-- leftmost prefix, so it gets its own index.
CREATE INDEX idx_entity_tag_tag_id ON entity_tag(tag_id);

-- +goose Down
DROP INDEX idx_entity_tag_tag_id;
DROP TABLE entity_tag;
