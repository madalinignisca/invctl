-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up

CREATE TABLE saved_view (
  id          TEXT PRIMARY KEY NOT NULL,
  user_id     TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  entity      TEXT NOT NULL
                CONSTRAINT saved_view_entity_check
                CHECK (entity IN ('asset','service')),
  name        TEXT NOT NULL,
  params      TEXT NOT NULL,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT saved_view_lifecycle_check
                CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX saved_view_active_key
  ON saved_view(user_id, entity, name) WHERE lifecycle = 'active';

CREATE INDEX idx_saved_view_user ON saved_view(user_id, entity, lifecycle);

-- +goose Down
DROP INDEX idx_saved_view_user;
DROP INDEX saved_view_active_key;
DROP TABLE saved_view;
