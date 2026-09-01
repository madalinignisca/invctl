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
  -- ON DELETE CASCADE is a backstop for rows removed by other means. The
  -- erasure mechanism for a scrub is the explicit DELETE in ScrubUser, which
  -- runs in the scrub's own transaction so the erasure is atomic: a scrub
  -- either removes the person's views along with their details, or does not
  -- happen. The deletion itself writes no change_log row -- the scrub's audit
  -- entry is the app_user update. Do not remove either this cascade or that
  -- DELETE believing the other makes it redundant.
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
