-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Keycloak (OIDC) authentication (docs/superpowers/specs/2026-09-18-keycloak-oidc-design.md).
-- See postgres/00072_app_user_oidc.sql for the shared rationale (`subject`,
-- the partial unique index, matching on `sub` rather than username).
--
-- SQLite cannot ALTER or DROP a CHECK constraint even when it is named
-- (sqlite/00005's header measured this against the pinned driver), so
-- widening `source` here is the same create-copy-drop-rename shape 00005
-- used, not two ALTERs. THIS MUST NOT MOVE TO migrations/shared/: `Migrate()`
-- runs every shared migration before any dialect one, and 00005 already
-- rebuilds this exact table on SQLite -- a shared column addition landing
-- before that rebuild would be silently dropped on every fresh install while
-- every already-migrated database stayed green (confirmed 2026-09-18; cost
-- migration 00070 a rewrite the day before this one).
--
-- EVERY EXISTING COLUMN IS DECLARED BELOW, READ OFF THE CURRENT TABLE, NOT
-- GUESSED: sqlite/00005 (id, username, display_name, email, source,
-- password_hash, is_active, last_login_at, created_at) plus sqlite/00058
-- (role, can_see_costs). A column missing from the CREATE TABLE below or from
-- the INSERT...SELECT would silently drop every existing user's data on
-- upgrade.
--
-- `id` IS `TEXT NOT NULL PRIMARY KEY`, NOT JUST `TEXT PRIMARY KEY`, even
-- though 00005's original declaration (copied above) omits the NOT NULL.
-- 00013_not_null_primary_keys.sql later closed that gap with `ALTER TABLE
-- app_user ALTER COLUMN id SET NOT NULL` -- a constraint SQLite enforces
-- without it ever appearing in sqlite_master's stored DDL text, so a rebuild
-- that copies 00005's CREATE TABLE verbatim silently drops it again. Found by
-- TestColumnShapesMatchAcrossEngines failing against this exact migration
-- (`app_user.id is NOT NULL on sqlite=false postgres=true`) before the fix
-- was in place -- 00070's header describes the same rebuild-silently-drops-a-
-- later-ALTER shape for a different pair of columns.

-- +goose NO TRANSACTION

-- +goose Up
PRAGMA foreign_keys = OFF;

CREATE TABLE app_user_new (
  id            TEXT NOT NULL PRIMARY KEY,
  username      TEXT NOT NULL CONSTRAINT app_user_username_key UNIQUE,
  display_name  TEXT,
  email         TEXT,
  source        TEXT NOT NULL CONSTRAINT app_user_source_check CHECK (source IN ('local','ldap','oidc')),
  -- argon2id only. NULL for LDAP and OIDC users, whose credentials never
  -- touch us.
  password_hash TEXT,
  is_active     BOOLEAN NOT NULL DEFAULT TRUE,
  last_login_at TEXT,
  created_at    TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'observer' CONSTRAINT app_user_role_check
                  CHECK (role IN ('administrator', 'observer', 'project_owner')),
  can_see_costs BOOLEAN NOT NULL DEFAULT FALSE,
  -- The OIDC `sub` claim. NULL for local and LDAP accounts -- see the header.
  subject       TEXT
);
INSERT INTO app_user_new (id, username, display_name, email, source, password_hash,
                           is_active, last_login_at, created_at, role, can_see_costs, subject)
SELECT id, username, display_name, email, source, password_hash,
       is_active, last_login_at, created_at, role, can_see_costs, NULL
FROM app_user;
DROP TABLE app_user;
ALTER TABLE app_user_new RENAME TO app_user;

-- Partial: only OIDC accounts carry a subject, and two NULLs (every local and
-- LDAP account) must not collide -- see the header on the postgres half.
CREATE UNIQUE INDEX app_user_subject_key ON app_user(subject) WHERE subject IS NOT NULL;

PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

DROP INDEX app_user_subject_key;

CREATE TABLE app_user_old (
  id            TEXT NOT NULL PRIMARY KEY,
  username      TEXT NOT NULL CONSTRAINT app_user_username_key UNIQUE,
  display_name  TEXT,
  email         TEXT,
  source        TEXT NOT NULL CONSTRAINT app_user_source_check CHECK (source IN ('local','ldap')),
  password_hash TEXT,
  is_active     BOOLEAN NOT NULL DEFAULT TRUE,
  last_login_at TEXT,
  created_at    TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'observer' CONSTRAINT app_user_role_check
                  CHECK (role IN ('administrator', 'observer', 'project_owner')),
  can_see_costs BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO app_user_old (id, username, display_name, email, source, password_hash,
                           is_active, last_login_at, created_at, role, can_see_costs)
SELECT id, username, display_name, email, source, password_hash,
       is_active, last_login_at, created_at, role, can_see_costs
FROM app_user;
DROP TABLE app_user;
ALTER TABLE app_user_old RENAME TO app_user;

PRAGMA foreign_keys = ON;
