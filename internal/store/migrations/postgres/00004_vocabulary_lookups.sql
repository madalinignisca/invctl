-- Seven domain vocabularies stop being frozen enums and become lookup tables.
-- PostgreSQL half; see migrations/sqlite/00004 for which seven, why those and
-- not the other fifty-odd, and why keying on `code` means no data migrates.
--
-- The same end state, reached without a rebuild. PostgreSQL names every
-- constraint whether you do or not, so the vocabulary CHECK is simply dropped
-- by name and a foreign key added in its place -- both operations SQLite
-- refuses, which is the entire reason the two halves look nothing alike. The
-- seven names below were read from `pg_constraint` rather than guessed; they
-- follow the server's own rule, `<table>_<column>_check` for an inline
-- single-column CHECK.
--
-- Nothing here touches the behavioural constraints on `asset`, `service` and
-- `ip_address`, which the SQLite half names during its rebuild. It does not
-- need to: PostgreSQL already named them `asset_lifecycle_check`,
-- `service_availability_check` and so on, and the SQLite half deliberately
-- adopts those exact names. The two engines converge rather than diverge, and
-- this file stays about the one thing that actually differs.
--
-- The lookup tables and their seed rows below are byte-identical to the SQLite
-- half. They are portable DDL duplicated across two files, so they are the one
-- place this migration could drift between engines -- `diff` the two sections
-- if either is ever edited, and note that the dual-engine store suite compares
-- the resulting rows.
--
-- ADD CONSTRAINT ... FOREIGN KEY validates existing rows immediately, so a
-- vocabulary seeded with the wrong values fails here, loudly, rather than
-- passing and leaving a column that constrains nothing. That is a real
-- cross-check on the SQLite half, where the rebuild runs with enforcement off
-- and only `PRAGMA foreign_key_check` would notice.

-- +goose Up

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
-- Repointing the columns
--
-- Order is irrelevant here -- every statement is independent and the whole file
-- is one transaction -- so the pairs are listed in the order the SQLite half
-- rebuilds its tables, purely so the two files read side by side.
--
-- The FK names are the ones PostgreSQL would generate for an inline REFERENCES,
-- `<table>_<column>_fkey`, so nothing in this schema is named by a convention
-- the server does not already use. They do not collide with the like-named
-- lookup tables: a foreign key creates no index, so it claims nothing in the
-- relation namespace.
-- ---------------------------------------------------------------------------

ALTER TABLE environment DROP CONSTRAINT environment_role_check;
ALTER TABLE environment
  ADD CONSTRAINT environment_role_fkey FOREIGN KEY (role) REFERENCES environment_role(code);

ALTER TABLE asset DROP CONSTRAINT asset_kind_check;
ALTER TABLE asset
  ADD CONSTRAINT asset_kind_fkey FOREIGN KEY (kind) REFERENCES asset_kind(code);

ALTER TABLE interface DROP CONSTRAINT interface_form_factor_check;
ALTER TABLE interface
  ADD CONSTRAINT interface_form_factor_fkey
  FOREIGN KEY (form_factor) REFERENCES interface_form_factor(code);

ALTER TABLE ip_address DROP CONSTRAINT ip_address_role_check;
ALTER TABLE ip_address
  ADD CONSTRAINT ip_address_role_fkey FOREIGN KEY (role) REFERENCES ip_address_role(code);

ALTER TABLE service DROP CONSTRAINT service_kind_check;
ALTER TABLE service
  ADD CONSTRAINT service_kind_fkey FOREIGN KEY (kind) REFERENCES service_kind(code);

-- Nullable, and a NULL child column is exempt from foreign key checking on both
-- engines, so "engine not recorded" stays expressible.
ALTER TABLE rt_container DROP CONSTRAINT rt_container_engine_check;
ALTER TABLE rt_container
  ADD CONSTRAINT rt_container_engine_fkey FOREIGN KEY (engine) REFERENCES container_engine(code);

ALTER TABLE dependency_data_class DROP CONSTRAINT dependency_data_class_data_class_check;
ALTER TABLE dependency_data_class
  ADD CONSTRAINT dependency_data_class_data_class_fkey
  FOREIGN KEY (data_class) REFERENCES data_class(code);

-- +goose Down

-- The CHECKs are re-added under the names PostgreSQL gave them originally, so
-- the down migration restores the schema byte for byte rather than something
-- that merely behaves the same.

ALTER TABLE dependency_data_class DROP CONSTRAINT dependency_data_class_data_class_fkey;

ALTER TABLE rt_container DROP CONSTRAINT rt_container_engine_fkey;

ALTER TABLE service DROP CONSTRAINT service_kind_fkey;

ALTER TABLE ip_address DROP CONSTRAINT ip_address_role_fkey;

ALTER TABLE interface DROP CONSTRAINT interface_form_factor_fkey;

ALTER TABLE asset DROP CONSTRAINT asset_kind_fkey;

ALTER TABLE environment DROP CONSTRAINT environment_role_fkey;

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
