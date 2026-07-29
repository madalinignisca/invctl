-- identity: uniqueness that respects the lifecycle, a realm that cannot be
-- NULL, and a CHECK on the lifecycle column that never had one.
--
-- Three defects, all of which need the same rebuild, so they are fixed together
-- rather than leaving a second undroppable constraint behind.
--
-- 1. UNIQUE (realm, name) is inline and unnamed, so on SQLite it can never be
--    dropped. identity carries a lifecycle, so retiring an identity leaves its
--    (realm, name) reserved forever -- the same defect as service_instance in
--    00002, in a table where the natural response to a compromised credential
--    is to retire it and create its replacement under the same name.
--
-- 2. realm is NULLABLE, and in SQL NULL is not equal to NULL, so the UNIQUE
--    does not fire at all for the rows most likely to collide. Two identities
--    named 'svc-orders' with no realm are accepted today by both engines. The
--    constraint reads as protection and provides none for exactly the local
--    accounts that have no realm to distinguish them.
--
--    NOT NULL DEFAULT '' rather than a sentinel word: '' is not a realm anyone
--    would type, sorts predictably, and needs no explanation in a WHERE clause.
--
-- 3. lifecycle has no CHECK -- the only lifecycle column in the schema without
--    one, so 'retierd' is storable and silently never matches a filter.
--
-- Two tables reference identity (rt_windows.logon_identity_id and
-- dependency.identity_id), both ON DELETE NO ACTION, so the rebuild must run
-- with enforcement off to avoid tripping on rows that legitimately point at it.

-- +goose NO TRANSACTION

-- +goose Up
PRAGMA foreign_keys = OFF;

CREATE TABLE identity_new (
  id            TEXT NOT NULL PRIMARY KEY,
  kind          TEXT NOT NULL CONSTRAINT identity_kind_check
                  CHECK (kind IN
                    ('service_account','machine_account','api_token','cert_subject','human')),
  name          TEXT NOT NULL CONSTRAINT identity_name_check CHECK (name <> ''),
  realm         TEXT NOT NULL DEFAULT '',
  -- A Vault path or similar. NEVER the secret itself. If a code path would
  -- put an actual secret here, that is a bug to raise, not to work around.
  secret_ref    TEXT,
  rotation_days INTEGER CONSTRAINT identity_rotation_days_check
                  CHECK (rotation_days IS NULL OR rotation_days > 0),
  last_rotated  TEXT,
  owner_team    TEXT,
  lifecycle     TEXT NOT NULL DEFAULT 'active'
                  CONSTRAINT identity_lifecycle_check CHECK (lifecycle IN ('active','retired'))
  -- No inline UNIQUE. See the partial index below.
);

INSERT INTO identity_new
  (id, kind, name, realm, secret_ref, rotation_days, last_rotated, owner_team, lifecycle)
SELECT id, kind, name, COALESCE(realm, ''), secret_ref, rotation_days, last_rotated, owner_team,
       lifecycle
FROM identity;

DROP TABLE identity;
ALTER TABLE identity_new RENAME TO identity;

-- Uniqueness over LIVE identities only, and now it actually fires for the
-- realm-less ones.
CREATE UNIQUE INDEX idx_identity_name_active
  ON identity(realm, name) WHERE lifecycle = 'active';

-- No PRAGMA foreign_key_check here. It reads like a safety net and is not one:
-- goose runs migration statements with Exec, the pragma reports violations as
-- result rows, and Exec discards them -- so it finds the problem, throws it
-- away and reports success. store.verifyForeignKeys runs the same check with
-- Query after every migration instead, where a violation can actually fail the
-- run.
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

CREATE TABLE identity_old (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN
                  ('service_account','machine_account','api_token','cert_subject','human')),
  name          TEXT NOT NULL,
  realm         TEXT,
  secret_ref    TEXT,
  rotation_days INTEGER,
  last_rotated  TEXT,
  owner_team    TEXT,
  lifecycle     TEXT NOT NULL DEFAULT 'active',
  UNIQUE (realm, name)
);

-- Retired identities go: their (realm, name) may since have been reused, which
-- the restored UNIQUE cannot represent. Announced here rather than discovered
-- as a constraint violation mid-migration.
INSERT INTO identity_old
  (id, kind, name, realm, secret_ref, rotation_days, last_rotated, owner_team, lifecycle)
SELECT id, kind, name, NULLIF(realm, ''), secret_ref, rotation_days, last_rotated, owner_team,
       lifecycle
FROM identity
WHERE lifecycle = 'active';

DROP TABLE identity;
ALTER TABLE identity_old RENAME TO identity;

PRAGMA foreign_keys = ON;
