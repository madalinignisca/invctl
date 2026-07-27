-- +goose Up
-- Core schema: environments, assets, the containment closure, and membership.
--
-- Portability notes for anyone editing this file:
--   * IDs are UUIDv7 TEXT generated in Go. No SERIAL, no IDENTITY.
--   * Timestamps are RFC3339 UTC TEXT generated in Go. No NOW() defaults --
--     they would make the value depend on the database's clock and timezone.
--   * Byte-range columns are declared BYTEA: PostgreSQL requires it, and
--     SQLite ignores unrecognised type names (the value keeps its own storage
--     class, and blob-to-blob comparison is bytewise in both engines).
--   * Enums are TEXT + CHECK, mirrored by a Go constant set in internal/domain.

CREATE TABLE environment (
  id          TEXT PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  role        TEXT NOT NULL CHECK (role IN ('production','staging','dev','transit','shared','dr')),
  in_scope    BOOLEAN NOT NULL DEFAULT FALSE,
  criticality INTEGER NOT NULL DEFAULT 3,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE asset (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL CHECK (kind IN (
                'site','rack','pdu','firewall','switch','patch_panel',
                'server','hypervisor','cluster','vm','k8s_node','storage')),
  name        TEXT NOT NULL,
  parent_id   TEXT REFERENCES asset(id),
  serial      TEXT,
  asset_tag   TEXT,
  vendor      TEXT,
  model       TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CHECK (lifecycle IN ('planned','active','maintenance','deprecated','retired')),
  owner_team  TEXT,
  attrs       TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_asset_kind   ON asset(kind);
CREATE INDEX idx_asset_parent ON asset(parent_id);

-- The containment tree flattened. Every asset has a self-row at depth 0, so
-- "everything at or below X" is a single indexed lookup and the impact engine
-- does not have to distinguish "reboot this VM" from "this rack loses power".
CREATE TABLE asset_closure (
  ancestor_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  descendant_id TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  depth         INTEGER NOT NULL,
  PRIMARY KEY (ancestor_id, descendant_id)
);
CREATE INDEX idx_closure_desc ON asset_closure(descendant_id);

-- Environment membership is many-to-many on purpose: a shared switch really
-- does belong to two environments, and "which assets span environments" is a
-- query that gets run constantly.
CREATE TABLE asset_environment (
  asset_id       TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  environment_id TEXT NOT NULL REFERENCES environment(id) ON DELETE CASCADE,
  note           TEXT,
  PRIMARY KEY (asset_id, environment_id)
);
CREATE INDEX idx_asset_env_env ON asset_environment(environment_id);

-- +goose Down
DROP TABLE asset_environment;
DROP TABLE asset_closure;
DROP TABLE asset;
DROP TABLE environment;
