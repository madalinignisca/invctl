-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Audit trail and local accounts.

-- Every mutation writes one of these in the same transaction as the mutation
-- itself. `diff` is field-level JSON ({"field":{"old":...,"new":...}}) for
-- updates and a full snapshot under "new" for creates -- small enough to keep
-- forever, complete enough to reconstruct a row at a point in time.
CREATE TABLE change_log (
  id          TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  action      TEXT NOT NULL CHECK (action IN ('create','update','delete','retire')),
  actor       TEXT NOT NULL,
  actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user','agent','system')),
  diff        TEXT NOT NULL,
  ticket_ref  TEXT,
  at          TEXT NOT NULL
);
CREATE INDEX idx_changelog_entity ON change_log(entity_type, entity_id, at);
-- Supports the global "recent activity" feed on the dashboard.
CREATE INDEX idx_changelog_at ON change_log(at);

CREATE TABLE app_user (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  display_name  TEXT,
  email         TEXT,
  source        TEXT NOT NULL CHECK (source IN ('local','ldap')),
  -- argon2id only. NULL for LDAP users, whose credentials never touch us.
  password_hash TEXT,
  is_active     BOOLEAN NOT NULL DEFAULT TRUE,
  last_login_at TEXT,
  created_at    TEXT NOT NULL
);

-- +goose Down
DROP TABLE app_user;
DROP TABLE change_log;
