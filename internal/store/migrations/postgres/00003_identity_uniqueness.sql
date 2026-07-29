-- identity: uniqueness that respects the lifecycle, a realm that cannot be
-- NULL, and a CHECK on the lifecycle column that never had one. PostgreSQL
-- half; see migrations/sqlite/00003 for the three defects and why they are
-- fixed together.
--
-- No rebuild needed: PostgreSQL names its constraints, so the UNIQUE can be
-- dropped by name, and ALTER COLUMN ... SET NOT NULL exists. The backfill must
-- run before the SET NOT NULL or it fails on the rows it is there to fix.

-- +goose Up
ALTER TABLE identity DROP CONSTRAINT identity_realm_name_key;

-- Before SET NOT NULL, and matching COALESCE(realm, '') in the SQLite half so
-- both engines land on identical data.
UPDATE identity SET realm = '' WHERE realm IS NULL;
ALTER TABLE identity ALTER COLUMN realm SET NOT NULL;
ALTER TABLE identity ALTER COLUMN realm SET DEFAULT '';

ALTER TABLE identity
  ADD CONSTRAINT identity_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  ADD CONSTRAINT identity_name_check CHECK (name <> ''),
  ADD CONSTRAINT identity_rotation_days_check
    CHECK (rotation_days IS NULL OR rotation_days > 0);

CREATE UNIQUE INDEX idx_identity_name_active
  ON identity(realm, name) WHERE lifecycle = 'active';

-- +goose Down
DROP INDEX IF EXISTS idx_identity_name_active;

ALTER TABLE identity
  DROP CONSTRAINT IF EXISTS identity_lifecycle_check,
  DROP CONSTRAINT IF EXISTS identity_name_check,
  DROP CONSTRAINT IF EXISTS identity_rotation_days_check;

ALTER TABLE identity ALTER COLUMN realm DROP DEFAULT;
ALTER TABLE identity ALTER COLUMN realm DROP NOT NULL;

-- Same trade as the SQLite half: a retired identity's name may since have been
-- reused, which the restored UNIQUE cannot represent.
DELETE FROM identity WHERE lifecycle = 'retired';

ALTER TABLE identity ADD CONSTRAINT identity_realm_name_key UNIQUE (realm, name);
