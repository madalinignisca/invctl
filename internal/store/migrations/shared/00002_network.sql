-- +goose Up
-- Layer 1-3 network: ports, cables, prefixes, addresses.

CREATE TABLE interface (
  id            TEXT PRIMARY KEY,
  asset_id      TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  form_factor   TEXT NOT NULL CHECK (form_factor IN
                  ('rj45','sfp','sfp+','sfp28','qsfp+','qsfp28','virtual','lag','loopback')),
  speed_mbps    INTEGER,
  mac           TEXT,
  mtu           INTEGER,
  lag_parent_id TEXT REFERENCES interface(id),
  is_mgmt       BOOLEAN NOT NULL DEFAULT FALSE,
  enabled       BOOLEAN NOT NULL DEFAULT TRUE,
  UNIQUE (asset_id, name)
);
CREATE INDEX idx_interface_asset ON interface(asset_id);
-- Partial index: most interfaces have no MAC, and MAC lookup is a search path.
CREATE INDEX idx_interface_mac ON interface(mac) WHERE mac IS NOT NULL;

CREATE TABLE link (
  id             TEXT PRIMARY KEY,
  a_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  b_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  medium         TEXT,
  length_m       INTEGER,
  CHECK (a_interface_id <> b_interface_id)
);
CREATE INDEX idx_link_a ON link(a_interface_id);
CREATE INDEX idx_link_b ON link(b_interface_id);

CREATE TABLE prefix (
  id             TEXT PRIMARY KEY,
  cidr_text      TEXT NOT NULL,
  addr_family    INTEGER NOT NULL CHECK (addr_family IN (4,6)),
  addr_start     BYTEA NOT NULL,
  addr_end       BYTEA NOT NULL,
  vlan_id        INTEGER,
  environment_id TEXT REFERENCES environment(id),
  role           TEXT,
  UNIQUE (cidr_text)
);
-- Supports the containment scan: family first to keep 4-byte and 16-byte
-- ranges from ever being compared against each other.
CREATE INDEX idx_prefix_range ON prefix(addr_family, addr_start, addr_end);

CREATE TABLE ip_address (
  id           TEXT PRIMARY KEY,
  addr_text    TEXT NOT NULL,
  addr_family  INTEGER NOT NULL CHECK (addr_family IN (4,6)),
  addr_start   BYTEA NOT NULL,
  interface_id TEXT REFERENCES interface(id) ON DELETE SET NULL,
  role         TEXT NOT NULL DEFAULT 'primary'
                 CHECK (role IN ('primary','secondary','vip','mgmt','floating')),
  UNIQUE (addr_text, interface_id)
);
CREATE INDEX idx_ip_start ON ip_address(addr_family, addr_start);
CREATE INDEX idx_ip_text  ON ip_address(addr_text);

-- +goose Down
DROP TABLE ip_address;
DROP TABLE prefix;
DROP TABLE link;
DROP TABLE interface;
