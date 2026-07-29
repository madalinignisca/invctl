-- Seven domain vocabularies stop being frozen enums and become lookup tables.
--
-- WHY THESE SEVEN AND NOT THE OTHER FIFTY-ODD. There are two kinds of enum in
-- this schema and they want opposite things.
--
--   A BEHAVIOURAL enum is read by Go to select a code path. `nature` decides
--   how failure propagates; `availability` decides how the impact engine counts
--   capacity; `plane` decides which anchor an attachment folds into. If a new
--   value for one of those could arrive as an INSERT, every `switch` over it
--   would fall through to its default silently -- a wrong answer during an
--   incident, with nothing in the logs. Those keep TEXT + CHECK, because a new
--   value SHOULD require a release: the release is where the code that has to
--   understand it gets written.
--
--   A DOMAIN VOCABULARY only describes the estate. Nothing branches on it; code
--   reads it, renders it and passes it through. `sfp28`, `podman` and `qsfp+`
--   are facts about hardware and tooling that will keep arriving forever, and
--   there is no code to write when one does. Requiring a migration, a rebuild
--   and a deploy to record that a rack now has 400G optics is friction that
--   buys nothing.
--
-- The seven below are the second kind. (Two carry a caveat, recorded at the
-- bottom of this comment rather than buried in the schema.)
--
-- WHY KEYING ON `code` MEANS NO DATA MIGRATES. The lookup's primary key is the
-- string already stored in the column -- `'server'`, not a surrogate id. So
-- `asset.kind` keeps every byte it has, the FK is satisfied the instant the
-- seed rows exist, and every query in the codebase still reads
-- `WHERE a.kind = 'k8s_node'` and still means it. There is no backfill, no
-- UPDATE of an entity table, no join added to a hot path, and no Go change
-- required for the schema to be correct. A surrogate id would have bought
-- nothing here and cost all of that.
--
-- WHY IT HAS TO BE NOW. Measured against the pinned driver
-- (modernc.org/sqlite v1.54.0, SQLite 3.53.3): `ADD CONSTRAINT <name> CHECK`
-- works and validates existing rows, but `ADD CONSTRAINT ... FOREIGN KEY` is a
-- syntax error, and `DROP CONSTRAINT` on an unnamed inline constraint reports
-- `no such constraint`. Both halves of this change are therefore rebuild-only:
-- the CHECK being removed cannot be dropped, and the FK replacing it cannot be
-- added. After POC sign-off a destructive migration needs a recorded decision,
-- so this is the last cheap moment -- the same argument as 00002 and 00003, and
-- the last of the three shapes DECISIONS.md lists as unfixable later.
--
-- WHY THE REBUILT TABLES ALSO GAIN CONSTRAINT NAMES. `asset`, `service` and
-- `ip_address` carry behavioural CHECKs that are staying (lifecycle,
-- availability, failover_mode, tier, addr_family), and those are unnamed and
-- therefore equally permanent. Rebuilding a table twice to fix two properties
-- of it would be absurd, so they are named here. The names chosen are exactly
-- the ones PostgreSQL generates for itself -- `<table>_<column>_check`,
-- `<table>_<column>_key` -- so the two engines converge on one schema rather
-- than two that happen to agree, and the PostgreSQL half needs no matching
-- statements at all. `interface`, `environment`, `rt_container` and
-- `dependency_data_class` have no behavioural CHECK; they are rebuilt for the
-- vocabulary alone.
--
-- WHY NO TRANSACTION. Dropping `asset` with foreign keys enforced would fire
-- ON DELETE CASCADE into `asset_closure`, `asset_environment`, `interface`,
-- `net_attachment`, `net_group_member` and onward, and ON DELETE SET NULL would
-- silently blank every `ip_address.interface_id` -- data loss with no error.
-- The pragma that turns enforcement off is a no-op inside a transaction and
-- goose wraps each migration in one by default, hence the directive. The cost
-- is that a failure part-way leaves a half-migrated database; acceptable
-- against a fixture pre-sign-off, and the reason this is happening now.
--
-- TABLE NAMING. `<table>_<column>`, which is also what the FK reads as. Two
-- exceptions, because that rule degenerates: `dependency_data_class.data_class`
-- would give `dependency_data_class_data_class`, and `rt_` is this schema's
-- prefix for runtime-detail tables rather than part of the vocabulary's name.
-- Those two are named for the vocabulary itself: `data_class`, `container_engine`.
--
-- SORT ORDER. Physical and logical nesting, not the alphabet: a site contains
-- racks, a rack contains power and network and compute, a hypervisor hosts VMs.
-- That is already the order these values are declared in, and the order the Go
-- constant slices use, so the numbering preserves every dropdown exactly as it
-- renders today. Steps of ten, because the whole point of the change is that
-- the next value arrives as an INSERT and it should be able to land between two
-- existing ones without renumbering the table.
--
-- NO TIMESTAMPS, NO SURROGATE ID. A lookup row is (code, label, sort_order) and
-- nothing else, so the rules about UUIDv7 and RFC3339 generated in Go have
-- nothing to bite on. Seeding is done here rather than from `internal/seed`
-- because the FK cannot be added until the rows exist, and no migration in this
-- repository writes `change_log` -- the audit trail records what operators do
-- to the estate, and this is the schema arriving.
--
-- TWO CAVEATS, FLAGGED NOT RESOLVED. `asset.kind` is read by
-- `domain.CanHostInstances`, `domain.AttachableAssetKinds` and a `kind = ?`
-- bound to `KindK8sNode` in the impact graph; `environment.role` is read by
-- `IsTransit` and the spanning-assets query. A value inserted into either
-- lookup will therefore be non-hosting, non-attachable and non-transit by
-- default, with no diagnostic. That is a real gap and the fix -- carrying the
-- behaviour as a column on the lookup, or keeping a named CHECK for these two
-- -- is an architecture decision, not something to settle in a migration.
-- Recorded here so the next person finds it.

-- +goose NO TRANSACTION

-- +goose Up
PRAGMA foreign_keys = OFF;

-- ---------------------------------------------------------------------------
-- The vocabularies
-- ---------------------------------------------------------------------------

-- The behaviour columns are the point, not decoration.
--
-- A vocabulary is only extensible as data if the BEHAVIOUR attached to a value
-- travels with it. Without these, 'kind' was opened to data while
-- Asset.CanHostInstances and domain.IsAttachable kept switching over a
-- hand-listed subset, so a kind added by INSERT was storable and then silently
-- non-hosting and non-attachable -- a bridge that can carry nothing, which is
-- exactly the case this work exists to serve. Measured on both engines before
-- these columns existed: CanHostInstances('bridge') = false, IsAttachable = false.
--
-- Default FALSE on both, so a value added without thinking about it is inert
-- rather than quietly granted placement rights it was never considered for.
CREATE TABLE asset_kind (
  code               TEXT PRIMARY KEY,
  label              TEXT NOT NULL,
  sort_order         INTEGER NOT NULL DEFAULT 0,
  -- May a service_instance be placed on an asset of this kind? Placing a
  -- workload on a rack or a patch panel is a data-entry mistake.
  can_host_instances BOOLEAN NOT NULL DEFAULT FALSE,
  -- May an asset of this kind be the subject of a net_attachment? A rack or a
  -- site is not network-attached, and allowing one would let a single row fake
  -- full coverage for a whole subtree.
  is_attachable      BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO asset_kind (code, label, sort_order, can_host_instances, is_attachable) VALUES
  ('site', 'Site', 10, FALSE, FALSE),
  ('rack', 'Rack', 20, FALSE, FALSE),
  ('pdu', 'PDU', 30, FALSE, FALSE),
  ('firewall', 'Firewall', 40, TRUE, FALSE),
  ('switch', 'Switch', 50, TRUE, FALSE),
  ('patch_panel', 'Patch panel', 60, FALSE, FALSE),
  ('server', 'Server', 70, TRUE, TRUE),
  ('hypervisor', 'Hypervisor', 80, TRUE, TRUE),
  ('cluster', 'Cluster', 90, TRUE, TRUE),
  ('vm', 'Virtual machine', 100, TRUE, TRUE),
  ('k8s_node', 'Kubernetes node', 110, TRUE, TRUE),
  ('storage', 'Storage', 120, TRUE, TRUE);

CREATE TABLE service_kind (
  code       TEXT PRIMARY KEY,
  label      TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT INTO service_kind (code, label, sort_order) VALUES
  ('db',         'Database',        10),
  ('cache',      'Cache',           20),
  ('queue',      'Queue',           30),
  ('web',        'Web',             40),
  ('api',        'API',             50),
  ('proxy',      'Proxy',           60),
  ('auth',       'Authentication',  70),
  ('batch',      'Batch',           80),
  ('agent',      'Agent',           90),
  ('storage',    'Storage',        100),
  ('infra',      'Infrastructure', 110),
  ('monitoring', 'Monitoring',     120);

CREATE TABLE interface_form_factor (
  code       TEXT PRIMARY KEY,
  label      TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT INTO interface_form_factor (code, label, sort_order) VALUES
  ('rj45',     'RJ45',     10),
  ('sfp',      'SFP',      20),
  ('sfp+',     'SFP+',     30),
  ('sfp28',    'SFP28',    40),
  ('qsfp+',    'QSFP+',    50),
  ('qsfp28',   'QSFP28',   60),
  ('virtual',  'Virtual',  70),
  ('lag',      'LAG',      80),
  ('loopback', 'Loopback', 90);

-- is_transit carries the one behaviour attached to a role. Environment.IsTransit
-- and the segmentation-span report both switched on the literal 'transit', so a
-- brokering role added as data turned every asset spanning it into a false
-- positive on the report whose entire job is finding real segmentation
-- exceptions.
CREATE TABLE environment_role (
  code        TEXT PRIMARY KEY,
  label       TEXT NOT NULL,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  -- A transit zone exists to broker between segments, so an asset sitting in
  -- one is not a segmentation exception.
  is_transit  BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO environment_role (code, label, sort_order, is_transit) VALUES
  ('production', 'Production', 10, FALSE),
  ('staging', 'Staging', 20, FALSE),
  ('dev', 'Development', 30, FALSE),
  ('transit', 'Transit', 40, TRUE),
  ('shared', 'Shared', 50, FALSE),
  ('dr', 'Disaster recovery', 60, FALSE);

CREATE TABLE ip_address_role (
  code       TEXT PRIMARY KEY,
  label      TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT INTO ip_address_role (code, label, sort_order) VALUES
  ('primary',   'Primary',    10),
  ('secondary', 'Secondary',  20),
  ('vip',       'VIP',        30),
  ('mgmt',      'Management', 40),
  ('floating',  'Floating',   50);

-- Ordered by regulatory weight, not by name: an operator scanning the checkbox
-- group is looking for the two PCI classes first, and `none` is the answer that
-- should never be reached for by accident.
CREATE TABLE data_class (
  code       TEXT PRIMARY KEY,
  label      TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT INTO data_class (code, label, sort_order) VALUES
  ('chd',        'Cardholder data (CHD)',                10),
  ('sad',        'Sensitive authentication data (SAD)',  20),
  ('pii',        'Personal data (PII)',                  30),
  ('credential', 'Credential',                           40),
  ('telemetry',  'Telemetry',                            50),
  ('config',     'Configuration',                        60),
  ('none',       'None',                                 70);

CREATE TABLE container_engine (
  code       TEXT PRIMARY KEY,
  label      TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT INTO container_engine (code, label, sort_order) VALUES
  ('docker', 'Docker', 10),
  ('podman', 'Podman', 20);

-- ---------------------------------------------------------------------------
-- Repointing the columns. Parents before children, so a run interrupted by the
-- NO TRANSACTION directive never leaves a child pointing at a table that has
-- been dropped and not yet renamed back into place.
-- ---------------------------------------------------------------------------

CREATE TABLE environment_new (
  id          TEXT PRIMARY KEY,
  code        TEXT NOT NULL CONSTRAINT environment_code_key UNIQUE,
  name        TEXT NOT NULL,
  role        TEXT NOT NULL REFERENCES environment_role(code),
  in_scope    BOOLEAN NOT NULL DEFAULT FALSE,
  criticality INTEGER NOT NULL DEFAULT 3,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
INSERT INTO environment_new
  (id, code, name, role, in_scope, criticality, created_at, updated_at)
SELECT id, code, name, role, in_scope, criticality, created_at, updated_at
FROM environment;
DROP TABLE environment;
ALTER TABLE environment_new RENAME TO environment;

CREATE TABLE asset_new (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL REFERENCES asset_kind(code),
  name        TEXT NOT NULL,
  parent_id   TEXT REFERENCES asset(id),
  serial      TEXT,
  asset_tag   TEXT,
  vendor      TEXT,
  model       TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT asset_lifecycle_check
                CHECK (lifecycle IN ('planned','active','maintenance','deprecated','retired')),
  owner_team  TEXT,
  attrs       TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
INSERT INTO asset_new
  (id, kind, name, parent_id, serial, asset_tag, vendor, model, lifecycle,
   owner_team, attrs, created_at, updated_at)
SELECT id, kind, name, parent_id, serial, asset_tag, vendor, model, lifecycle,
       owner_team, attrs, created_at, updated_at
FROM asset;
DROP TABLE asset;
ALTER TABLE asset_new RENAME TO asset;
CREATE INDEX idx_asset_kind   ON asset(kind);
CREATE INDEX idx_asset_parent ON asset(parent_id);

CREATE TABLE interface_new (
  id            TEXT PRIMARY KEY,
  asset_id      TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  form_factor   TEXT NOT NULL REFERENCES interface_form_factor(code),
  speed_mbps    INTEGER,
  mac           TEXT,
  mtu           INTEGER,
  lag_parent_id TEXT REFERENCES interface(id),
  is_mgmt       BOOLEAN NOT NULL DEFAULT FALSE,
  enabled       BOOLEAN NOT NULL DEFAULT TRUE,
  CONSTRAINT interface_asset_id_name_key UNIQUE (asset_id, name)
);
INSERT INTO interface_new
  (id, asset_id, name, form_factor, speed_mbps, mac, mtu, lag_parent_id, is_mgmt, enabled)
SELECT id, asset_id, name, form_factor, speed_mbps, mac, mtu, lag_parent_id, is_mgmt, enabled
FROM interface;
DROP TABLE interface;
ALTER TABLE interface_new RENAME TO interface;
CREATE INDEX idx_interface_asset ON interface(asset_id);
-- Partial index: most interfaces have no MAC, and MAC lookup is a search path.
CREATE INDEX idx_interface_mac ON interface(mac) WHERE mac IS NOT NULL;

CREATE TABLE ip_address_new (
  id           TEXT PRIMARY KEY,
  addr_text    TEXT NOT NULL,
  addr_family  INTEGER NOT NULL
                 CONSTRAINT ip_address_addr_family_check CHECK (addr_family IN (4,6)),
  addr_start   BYTEA NOT NULL,
  interface_id TEXT REFERENCES interface(id) ON DELETE SET NULL,
  role         TEXT NOT NULL DEFAULT 'primary' REFERENCES ip_address_role(code),
  CONSTRAINT ip_address_addr_text_interface_id_key UNIQUE (addr_text, interface_id)
);
INSERT INTO ip_address_new
  (id, addr_text, addr_family, addr_start, interface_id, role)
SELECT id, addr_text, addr_family, addr_start, interface_id, role
FROM ip_address;
DROP TABLE ip_address;
ALTER TABLE ip_address_new RENAME TO ip_address;
CREATE INDEX idx_ip_start ON ip_address(addr_family, addr_start);
CREATE INDEX idx_ip_text  ON ip_address(addr_text);

CREATE TABLE service_new (
  id             TEXT PRIMARY KEY,
  application_id TEXT REFERENCES application(id),
  code           TEXT NOT NULL CONSTRAINT service_code_key UNIQUE,
  name           TEXT NOT NULL,
  kind           TEXT NOT NULL REFERENCES service_kind(code),
  environment_id TEXT NOT NULL REFERENCES environment(id),
  availability   TEXT NOT NULL CONSTRAINT service_availability_check
                   CHECK (availability IN
                     ('standalone','active_active','active_passive','quorum','sharded')),
  min_healthy    INTEGER,
  failover_mode  TEXT CONSTRAINT service_failover_mode_check
                   CHECK (failover_mode IN ('auto','manual','none')),
  tier           INTEGER NOT NULL DEFAULT 3
                   CONSTRAINT service_tier_check CHECK (tier BETWEEN 1 AND 4),
  rto_minutes    INTEGER,
  rpo_minutes    INTEGER,
  owner_team     TEXT,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CONSTRAINT service_lifecycle_check
                   CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  attrs          TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
INSERT INTO service_new
  (id, application_id, code, name, kind, environment_id, availability, min_healthy,
   failover_mode, tier, rto_minutes, rpo_minutes, owner_team, lifecycle, attrs,
   created_at, updated_at)
SELECT id, application_id, code, name, kind, environment_id, availability, min_healthy,
       failover_mode, tier, rto_minutes, rpo_minutes, owner_team, lifecycle, attrs,
       created_at, updated_at
FROM service;
DROP TABLE service;
ALTER TABLE service_new RENAME TO service;
CREATE INDEX idx_service_env  ON service(environment_id);
CREATE INDEX idx_service_app  ON service(application_id);

CREATE TABLE rt_container_new (
  instance_id     TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  -- Nullable, and a NULL child column is exempt from foreign key checking on
  -- both engines, so "engine not recorded" stays expressible.
  engine          TEXT REFERENCES container_engine(code),
  container_name  TEXT,
  compose_project TEXT,
  compose_service TEXT,
  image_repo      TEXT,
  image_tag       TEXT,
  image_digest    TEXT,
  restart_policy  TEXT,
  network_mode    TEXT,
  rootless        BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO rt_container_new
  (instance_id, engine, container_name, compose_project, compose_service,
   image_repo, image_tag, image_digest, restart_policy, network_mode, rootless)
SELECT instance_id, engine, container_name, compose_project, compose_service,
       image_repo, image_tag, image_digest, restart_policy, network_mode, rootless
FROM rt_container;
DROP TABLE rt_container;
ALTER TABLE rt_container_new RENAME TO rt_container;

CREATE TABLE dependency_data_class_new (
  dependency_id TEXT NOT NULL REFERENCES dependency(id) ON DELETE CASCADE,
  data_class    TEXT NOT NULL REFERENCES data_class(code),
  PRIMARY KEY (dependency_id, data_class)
);
INSERT INTO dependency_data_class_new (dependency_id, data_class)
SELECT dependency_id, data_class FROM dependency_data_class;
DROP TABLE dependency_data_class;
ALTER TABLE dependency_data_class_new RENAME TO dependency_data_class;

PRAGMA foreign_key_check;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

-- The down migration deliberately does NOT restore the original vocabulary
-- CHECKs, and that is not laziness.
--
-- The up migration exists so a value can be added by INSERT. The moment anyone
-- uses that -- adds 'bridge' to asset_kind and records a bridge, which is the
-- feature this work was requested for -- the original CHECK no longer holds
-- over the data. Restoring it made the down migration fail mid-rebuild with
-- "CHECK constraint failed", and because the file is NO TRANSACTION it left a
-- half-built table behind while goose still recorded the version as applied.
-- Measured end to end before this change.
--
-- The alternatives were worse: deleting every row using a value added after the
-- up migration destroys real inventory to satisfy a development convenience,
-- and coercing them to some default puts a false fact in the estate. So the
-- column comes back as unconstrained TEXT, the lookup tables are dropped, and
-- the down migration is honest about being a one-way door in one respect: the
-- vocabulary constraint is not restored, so re-applying the up migration is the
-- supported route back rather than a manual repair.

CREATE TABLE environment_old (
  id          TEXT PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  role        TEXT NOT NULL,
  in_scope    BOOLEAN NOT NULL DEFAULT FALSE,
  criticality INTEGER NOT NULL DEFAULT 3,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
INSERT INTO environment_old
  (id, code, name, role, in_scope, criticality, created_at, updated_at)
SELECT id, code, name, role, in_scope, criticality, created_at, updated_at
FROM environment;
DROP TABLE environment;
ALTER TABLE environment_old RENAME TO environment;

CREATE TABLE asset_old (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
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
INSERT INTO asset_old
  (id, kind, name, parent_id, serial, asset_tag, vendor, model, lifecycle,
   owner_team, attrs, created_at, updated_at)
SELECT id, kind, name, parent_id, serial, asset_tag, vendor, model, lifecycle,
       owner_team, attrs, created_at, updated_at
FROM asset;
DROP TABLE asset;
ALTER TABLE asset_old RENAME TO asset;
CREATE INDEX idx_asset_kind   ON asset(kind);
CREATE INDEX idx_asset_parent ON asset(parent_id);

CREATE TABLE interface_old (
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
INSERT INTO interface_old
  (id, asset_id, name, form_factor, speed_mbps, mac, mtu, lag_parent_id, is_mgmt, enabled)
SELECT id, asset_id, name, form_factor, speed_mbps, mac, mtu, lag_parent_id, is_mgmt, enabled
FROM interface;
DROP TABLE interface;
ALTER TABLE interface_old RENAME TO interface;
CREATE INDEX idx_interface_asset ON interface(asset_id);
CREATE INDEX idx_interface_mac ON interface(mac) WHERE mac IS NOT NULL;

CREATE TABLE ip_address_old (
  id           TEXT PRIMARY KEY,
  addr_text    TEXT NOT NULL,
  addr_family  INTEGER NOT NULL CHECK (addr_family IN (4,6)),
  addr_start   BYTEA NOT NULL,
  interface_id TEXT REFERENCES interface(id) ON DELETE SET NULL,
  role         TEXT NOT NULL DEFAULT 'primary',
  UNIQUE (addr_text, interface_id)
);
INSERT INTO ip_address_old
  (id, addr_text, addr_family, addr_start, interface_id, role)
SELECT id, addr_text, addr_family, addr_start, interface_id, role
FROM ip_address;
DROP TABLE ip_address;
ALTER TABLE ip_address_old RENAME TO ip_address;
CREATE INDEX idx_ip_start ON ip_address(addr_family, addr_start);
CREATE INDEX idx_ip_text  ON ip_address(addr_text);

CREATE TABLE service_old (
  id             TEXT PRIMARY KEY,
  application_id TEXT REFERENCES application(id),
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  kind           TEXT NOT NULL,
  environment_id TEXT NOT NULL REFERENCES environment(id),
  availability   TEXT NOT NULL CHECK (availability IN
                   ('standalone','active_active','active_passive','quorum','sharded')),
  min_healthy    INTEGER,
  failover_mode  TEXT CHECK (failover_mode IN ('auto','manual','none')),
  tier           INTEGER NOT NULL DEFAULT 3 CHECK (tier BETWEEN 1 AND 4),
  rto_minutes    INTEGER,
  rpo_minutes    INTEGER,
  owner_team     TEXT,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  attrs          TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
INSERT INTO service_old
  (id, application_id, code, name, kind, environment_id, availability, min_healthy,
   failover_mode, tier, rto_minutes, rpo_minutes, owner_team, lifecycle, attrs,
   created_at, updated_at)
SELECT id, application_id, code, name, kind, environment_id, availability, min_healthy,
       failover_mode, tier, rto_minutes, rpo_minutes, owner_team, lifecycle, attrs,
       created_at, updated_at
FROM service;
DROP TABLE service;
ALTER TABLE service_old RENAME TO service;
CREATE INDEX idx_service_env  ON service(environment_id);
CREATE INDEX idx_service_app  ON service(application_id);

CREATE TABLE rt_container_old (
  instance_id     TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  engine          TEXT,
  container_name  TEXT,
  compose_project TEXT,
  compose_service TEXT,
  image_repo      TEXT,
  image_tag       TEXT,
  image_digest    TEXT,
  restart_policy  TEXT,
  network_mode    TEXT,
  rootless        BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO rt_container_old
  (instance_id, engine, container_name, compose_project, compose_service,
   image_repo, image_tag, image_digest, restart_policy, network_mode, rootless)
SELECT instance_id, engine, container_name, compose_project, compose_service,
       image_repo, image_tag, image_digest, restart_policy, network_mode, rootless
FROM rt_container;
DROP TABLE rt_container;
ALTER TABLE rt_container_old RENAME TO rt_container;

CREATE TABLE dependency_data_class_old (
  dependency_id TEXT NOT NULL REFERENCES dependency(id) ON DELETE CASCADE,
  data_class    TEXT NOT NULL CHECK (data_class IN
                  ('chd','sad','pii','credential','telemetry','config','none')),
  PRIMARY KEY (dependency_id, data_class)
);
INSERT INTO dependency_data_class_old (dependency_id, data_class)
SELECT dependency_id, data_class FROM dependency_data_class;
DROP TABLE dependency_data_class;
ALTER TABLE dependency_data_class_old RENAME TO dependency_data_class;

-- Last, so nothing still references them. A value added to one of these after
-- the migration ran is lost here, which is the honest outcome: the column it
-- described no longer accepts it.
DROP TABLE container_engine;
DROP TABLE data_class;
DROP TABLE ip_address_role;
DROP TABLE environment_role;
DROP TABLE interface_form_factor;
DROP TABLE service_kind;
DROP TABLE asset_kind;

PRAGMA foreign_key_check;
PRAGMA foreign_keys = ON;
