-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Indexes for the ownership report (WP-G7 piece 1), and a gap
-- docs/ownership-report-design.md §9 names explicitly: every existing team
-- index is the WRONG WAY ROUND for this report's primary question.
--
-- Migration 00016 built (team_id, lifecycle) WHERE team_id IS NOT NULL for
-- asset, service and project; 00054 built (owner_team_id) WHERE
-- owner_team_id IS NOT NULL for custom_field. Both exist to make "what does
-- this team own" fast, which is the opposite of "what has NO owner" -- this
-- report's finding 1. A partial index whose predicate is the negation of the
-- one a query needs is not a slower version of the right index, it is a
-- different index the planner has no reason to even consider; both engines
-- fall back to a full table scan filtered row by row.
--
-- One partial index per owned entity, `WHERE <column> IS NULL`, carrying the
-- lifecycle/retired_at column the unowned query always filters by too, so a
-- match is answered from the index alone rather than a further heap fetch --
-- the same covering-index reasoning 00016 measured and documented.
--
-- identity is included for completeness though its lifecycle vocabulary is
-- only active/retired (migration 00003) and it is by far the smallest of the
-- five tables seeded today; kept in the same shape as the other four rather
-- than special-cased, since the report treats all five identically.

-- +goose Up
CREATE INDEX idx_asset_unowned        ON asset(lifecycle)        WHERE team_id IS NULL;
CREATE INDEX idx_service_unowned      ON service(lifecycle)      WHERE team_id IS NULL;
CREATE INDEX idx_project_unowned      ON project(lifecycle)      WHERE team_id IS NULL;
CREATE INDEX idx_identity_unowned     ON identity(lifecycle)     WHERE team_id IS NULL;
CREATE INDEX idx_custom_field_unowned ON custom_field(retired_at) WHERE owner_team_id IS NULL;

-- +goose Down
DROP INDEX idx_custom_field_unowned;
DROP INDEX idx_identity_unowned;
DROP INDEX idx_project_unowned;
DROP INDEX idx_service_unowned;
DROP INDEX idx_asset_unowned;
