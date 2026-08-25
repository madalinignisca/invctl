-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Adds an owning team to a custom field, so "who do I ask about this" has an
-- answer that survives the person who defined the field leaving.
--
-- THE GAP THIS CLOSES. custom_field.created_by/CreatedByName already answers
-- "who defined this", but that is an INDIVIDUAL -- exactly the wrong answer
-- for staff turnover, which is the scenario this whole feature exists to
-- defend against (docs/custom-fields-design.md's opening paragraph). Worse,
-- customfields.go's own comment on the created_by/retired_by LEFT JOIN
-- documents that a scrubbed app_user row leaves the field "readable... just
-- without a name to show for it" -- one GDPR erasure request quietly blanks
-- the feature's only attribution surface. team.contact_ref (migration
-- 00014) already exists for exactly this: "a GROUP address, a ticket queue
-- or a channel... never an individual", already GDPR-argued as non-personal.
-- This reuses it rather than inventing a second contact concept.
--
-- NULLABLE HERE, REQUIRED ON THE CREATE FORM. The eleven fields that exist
-- before this migration have no owner and this migration cannot invent one --
-- nobody can guess who owns `cost_centre` from the schema alone. They become
-- visible orphans on the registry, deliberately: finding and assigning them
-- is a separate piece of work, not something a migration should fake with a
-- default team nobody chose. Every field created through the application
-- from this point on is required to name one; see domain.NewCustomField.
--
-- NO ON DELETE CLAUSE, matching every other `team_id` reference in this
-- schema (migration 00014): team is soft-deleted, never hard-deleted, so a
-- referencing row can never be orphaned by the team's own removal. A
-- RETIRED team is not offered as a choice for a NEW field but keeps
-- displaying on a field that already names it -- the identical rule
-- already applied to a retired `custom_field_option` (design.md §3): what
-- is STORED must keep displaying, what is RETIRED must not be newly
-- selectable. The symmetry is deliberate, not a coincidence.

-- +goose Up
ALTER TABLE custom_field ADD COLUMN owner_team_id TEXT REFERENCES team(id);

CREATE INDEX idx_custom_field_owner_team ON custom_field(owner_team_id) WHERE owner_team_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_custom_field_owner_team;
ALTER TABLE custom_field DROP COLUMN owner_team_id;
