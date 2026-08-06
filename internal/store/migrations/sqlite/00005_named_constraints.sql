-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Every remaining behavioural constraint gets a name, so widening one later is
-- an ALTER instead of a table rebuild.
--
-- WHAT IS LEFT AFTER 00004. 00004 turned seven domain vocabularies into lookup
-- tables and, because it was already rebuilding `asset`, `service` and
-- `ip_address`, named the behavioural constraints on those three on the way
-- past. That leaves sixteen tables whose constraints are still anonymous:
-- app_user, asset_health, dependency, endpoint, health_override, link,
-- net_anchor, net_attachment, net_group, net_group_member, net_uplink,
-- observed_transition, prefix, route, rt_windows, unmatched_observation.
-- Sixty-four constraints -- 58 CHECK and 6 UNIQUE. No table is rebuilt twice.
--
-- WHY A REBUILD FOR SOMETHING THIS SMALL. Measured against the pinned driver
-- (modernc.org/sqlite v1.54.0, SQLite 3.53.3): `ALTER TABLE ... ADD CONSTRAINT
-- <name> CHECK (...)` works and validates existing rows, and `DROP CONSTRAINT
-- <name>` works -- but only for a constraint that has a name. `DROP CONSTRAINT`
-- against an unnamed inline one answers `no such constraint`, and that is true
-- of a table-level `CHECK (...)` too, not just an inline column one. So an
-- anonymous constraint can never be removed, narrowed or widened; the only way
-- to change it is to rebuild the table, which after POC sign-off means a
-- recorded destructive-migration decision. Naming them now converts every
-- future vocabulary change on these columns into two lines of ALTER on both
-- engines. This is the last cheap moment, the same argument as 00002, 00003
-- and 00004.
--
-- WHY THESE STAY CHECKS RATHER THAN BECOMING LOOKUP TABLES. Every value here
-- selects a code path. `nature` decides how failure propagates
-- (domain.Propagate); `availability` decides how the impact engine counts
-- capacity; `bind_scope` decides whether an endpoint resolves to cluster nodes;
-- `plane` decides which anchor an attachment folds into; `role` on
-- net_group is ranked to orient an undirected cable. A new value arriving as an
-- INSERT would fall through every one of those switches to its default,
-- silently, and produce a wrong answer during an incident with nothing in the
-- logs. For these, a new value SHOULD require a release -- the release is where
-- the code that has to understand it gets written. Naming the constraint makes
-- that release cheap; it does not make the value addable as data. See 00004 for
-- the other half of the argument.
--
-- THE NAMES ARE POSTGRESQL'S OWN. `<table>_<column>_check` and
-- `<table>_<column>_key` are exactly what PostgreSQL generates for an inline
-- constraint, so the two engines converge on one schema rather than two that
-- happen to agree, and the PostgreSQL half of this migration needs no
-- statements for any of them. Each name in this file was generated from the
-- live SQLite schema and then asserted to exist in `pg_constraint` before being
-- emitted, so the convention is checked rather than assumed.
--
-- THE SEVEN EXCEPTIONS. A table-level, multi-column CHECK is auto-named
-- `<table>_check` by PostgreSQL -- and `<table>_check1`, `_check2` for any
-- later ones on the same table. That is a positional name: it says nothing
-- about what the constraint means, and it moves if another table-level CHECK is
-- ever added. Adopting it would bake in a name that a future migration
-- silently invalidates. So the seven table-level constraints get a descriptive
-- name on both engines instead -- `dependency_provider_exclusive_check`,
-- `endpoint_port_or_unix_path_check` and so on -- and the PostgreSQL file
-- carries the seven DROP/ADD pairs that produces. Those seven are the only
-- place the two halves each need a statement.
--
-- WHY NO TRANSACTION. Identical to 00004 and for the same reason: `DROP TABLE`
-- with foreign keys enforced fires ON DELETE CASCADE and ON DELETE SET NULL
-- into children -- dropping `endpoint` would take `backend_member`, `route` and
-- every `dependency` row hanging off it, and dropping `net_group` would take
-- its anchors, attachments, members and uplinks. `PRAGMA foreign_keys` is a
-- no-op inside a transaction and goose wraps each migration in one by default,
-- hence the directive. The pragma is the protection here; the table order below
-- is a safety net, not the mechanism. The cost is that a failure part-way
-- leaves a half-migrated database, which is acceptable against a fixture
-- pre-sign-off and is the reason this is happening now.
--
-- ORDER. Parents before children per the foreign-key graph, so a run
-- interrupted under NO TRANSACTION is resumable by hand and every
-- `ALTER TABLE ... RENAME TO` -- which reparses the whole schema -- sees a
-- coherent one. The five tables with no foreign key in either direction go
-- first because they cannot fail for graph reasons.
--
-- THE DDL WAS GENERATED, NOT RETYPED. Each `CREATE TABLE` below is the text
-- SQLite itself stores, with `CONSTRAINT <name> ` spliced in immediately before
-- each anonymous CHECK and UNIQUE keyword and nothing else altered. Column
-- order, types, nullability, defaults, REFERENCES clauses with their ON DELETE
-- actions and the original comments are carried through byte for byte, because
-- a hand-copied rebuild that drops one column loses that column's data with no
-- error. The one deliberate edit is cosmetic: `link.lifecycle` was added by
-- `ALTER TABLE ADD COLUMN` in 00006, so SQLite had appended it into the stored
-- text mid-line; it is put on its own line here.
--
-- INDEXES. `DROP TABLE` takes its indexes with it, so all 27 are recreated
-- verbatim -- including the five partial ones (`idx_endpoint_port` plus the
-- four partial UNIQUE indexes `idx_health_override_one`, `idx_net_attach_one`,
-- `idx_net_group_member_one`, `idx_net_uplink_one`), where losing the WHERE
-- clause on a partial UNIQUE index stays silent until two rows collide months
-- later.

-- +goose NO TRANSACTION

-- +goose Up
PRAGMA foreign_keys = OFF;

CREATE TABLE app_user_new (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL CONSTRAINT app_user_username_key UNIQUE,
  display_name  TEXT,
  email         TEXT,
  source        TEXT NOT NULL CONSTRAINT app_user_source_check CHECK (source IN ('local','ldap')),
  -- argon2id only. NULL for LDAP users, whose credentials never touch us.
  password_hash TEXT,
  is_active     BOOLEAN NOT NULL DEFAULT TRUE,
  last_login_at TEXT,
  created_at    TEXT NOT NULL
);
INSERT INTO app_user_new (id, username, display_name, email, source, password_hash, is_active, last_login_at, created_at)
SELECT id, username, display_name, email, source, password_hash, is_active, last_login_at, created_at FROM app_user;
DROP TABLE app_user;
ALTER TABLE app_user_new RENAME TO app_user;

CREATE TABLE asset_health_new (
  entity_type      TEXT NOT NULL CONSTRAINT asset_health_entity_type_check CHECK (entity_type IN ('asset','service_instance')),
  entity_id        TEXT NOT NULL,
  reporter         TEXT NOT NULL,
  state            TEXT NOT NULL CONSTRAINT asset_health_state_check CHECK (state IN ('up','degraded','down','unknown')),
  state_since      TEXT NOT NULL,
  reported_at      TEXT NOT NULL,
  last_report_at   TEXT NOT NULL,
  interval_seconds INTEGER CONSTRAINT asset_health_interval_seconds_check CHECK (interval_seconds IS NULL OR interval_seconds > 0),
  observation_id   TEXT,
  -- Rule 9's open episode. All four are NULL together or set together.
  flap_since       TEXT,
  flap_count       INTEGER CONSTRAINT asset_health_flap_count_check CHECK (flap_count IS NULL OR flap_count > 0),
  flap_first_state TEXT CONSTRAINT asset_health_flap_first_state_check CHECK (flap_first_state IS NULL OR
                     flap_first_state IN ('up','degraded','down','unknown')),
  flap_seen        TEXT,
  PRIMARY KEY (entity_type, entity_id, reporter),
  CONSTRAINT asset_health_flap_episode_check CHECK ((flap_since IS NULL AND flap_count IS NULL AND flap_first_state IS NULL
          AND flap_seen IS NULL)
      OR (flap_since IS NOT NULL AND flap_count IS NOT NULL AND flap_first_state IS NOT NULL
          AND flap_seen IS NOT NULL))
);
INSERT INTO asset_health_new (entity_type, entity_id, reporter, state, state_since, reported_at, last_report_at, interval_seconds, observation_id, flap_since, flap_count, flap_first_state, flap_seen)
SELECT entity_type, entity_id, reporter, state, state_since, reported_at, last_report_at, interval_seconds, observation_id, flap_since, flap_count, flap_first_state, flap_seen FROM asset_health;
DROP TABLE asset_health;
ALTER TABLE asset_health_new RENAME TO asset_health;
CREATE INDEX idx_asset_health_entity ON asset_health(entity_type, entity_id);
CREATE INDEX idx_asset_health_reporter ON asset_health(reporter, last_report_at);

CREATE TABLE health_override_new (
  id             TEXT PRIMARY KEY,
  entity_type    TEXT NOT NULL CONSTRAINT health_override_entity_type_check CHECK (entity_type IN ('asset','service_instance')),
  entity_id      TEXT NOT NULL,
  asserted_state TEXT NOT NULL CONSTRAINT health_override_asserted_state_check CHECK (asserted_state IN ('up','degraded','down','unknown')),
  reason         TEXT NOT NULL CONSTRAINT health_override_reason_check CHECK (reason <> ''),
  actor          TEXT NOT NULL,
  lifecycle      TEXT NOT NULL DEFAULT 'active' CONSTRAINT health_override_lifecycle_check CHECK (lifecycle IN ('active','cleared')),
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  expires_at     TEXT NOT NULL,
  CONSTRAINT health_override_expiry_check CHECK (expires_at > created_at)
);
INSERT INTO health_override_new (id, entity_type, entity_id, asserted_state, reason, actor, lifecycle, created_at, updated_at, expires_at)
SELECT id, entity_type, entity_id, asserted_state, reason, actor, lifecycle, created_at, updated_at, expires_at FROM health_override;
DROP TABLE health_override;
ALTER TABLE health_override_new RENAME TO health_override;
CREATE INDEX idx_health_override_expiry ON health_override(expires_at);
CREATE UNIQUE INDEX idx_health_override_one
  ON health_override(entity_type, entity_id) WHERE lifecycle = 'active';

CREATE TABLE observed_transition_new (
  id            TEXT PRIMARY KEY,
  entity_type   TEXT NOT NULL CONSTRAINT observed_transition_entity_type_check CHECK (entity_type IN ('asset','service_instance')),
  entity_id     TEXT NOT NULL,
  reporter      TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'transition'
                  CONSTRAINT observed_transition_kind_check CHECK (kind IN ('transition','flap_open','flap_close')),
  from_state    TEXT CONSTRAINT observed_transition_from_state_check CHECK (from_state IS NULL OR
                  from_state IN ('up','degraded','down','unknown')),
  to_state      TEXT NOT NULL CONSTRAINT observed_transition_to_state_check CHECK (to_state IN ('up','degraded','down','unknown')),
  at            TEXT NOT NULL,
  -- flap_close detail. Null on every other kind.
  flap_count    INTEGER CONSTRAINT observed_transition_flap_count_check CHECK (flap_count IS NULL OR flap_count > 0),
  window_start  TEXT,
  window_end    TEXT,
  first_state   TEXT CONSTRAINT observed_transition_first_state_check CHECK (first_state IS NULL OR
                  first_state IN ('up','degraded','down','unknown')),
  last_state    TEXT CONSTRAINT observed_transition_last_state_check CHECK (last_state IS NULL OR
                  last_state IN ('up','degraded','down','unknown')),
  settled_state TEXT CONSTRAINT observed_transition_settled_state_check CHECK (settled_state IS NULL OR
                  settled_state IN ('up','degraded','down','unknown')),
  -- A flap_close that cannot say how much it hid is worse than no compression.
  CONSTRAINT observed_transition_flap_close_detail_check CHECK (kind <> 'flap_close' OR (flap_count IS NOT NULL AND window_start IS NOT NULL
         AND window_end IS NOT NULL AND first_state IS NOT NULL
         AND last_state IS NOT NULL AND settled_state IS NOT NULL))
);
INSERT INTO observed_transition_new (id, entity_type, entity_id, reporter, kind, from_state, to_state, at, flap_count, window_start, window_end, first_state, last_state, settled_state)
SELECT id, entity_type, entity_id, reporter, kind, from_state, to_state, at, flap_count, window_start, window_end, first_state, last_state, settled_state FROM observed_transition;
DROP TABLE observed_transition;
ALTER TABLE observed_transition_new RENAME TO observed_transition;
CREATE INDEX idx_observed_transition_at ON observed_transition(at);
CREATE INDEX idx_observed_transition_entity ON observed_transition(entity_type, entity_id, at);
CREATE INDEX idx_observed_transition_window
  ON observed_transition(entity_type, entity_id, reporter, at);

CREATE TABLE unmatched_observation_new (
  id            TEXT PRIMARY KEY,
  entity_type   TEXT NOT NULL CONSTRAINT unmatched_observation_entity_type_check CHECK (entity_type IN ('asset','service_instance')),
  entity_ref    TEXT NOT NULL,
  reporter      TEXT NOT NULL,
  state         TEXT NOT NULL CONSTRAINT unmatched_observation_state_check CHECK (state IN ('up','degraded','down','unknown')),
  reported_at   TEXT NOT NULL,
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  report_count  INTEGER NOT NULL DEFAULT 1 CONSTRAINT unmatched_observation_report_count_check CHECK (report_count > 0),
  CONSTRAINT unmatched_observation_entity_type_entity_ref_reporter_key UNIQUE (entity_type, entity_ref, reporter)
);
INSERT INTO unmatched_observation_new (id, entity_type, entity_ref, reporter, state, reported_at, first_seen_at, last_seen_at, report_count)
SELECT id, entity_type, entity_ref, reporter, state, reported_at, first_seen_at, last_seen_at, report_count FROM unmatched_observation;
DROP TABLE unmatched_observation;
ALTER TABLE unmatched_observation_new RENAME TO unmatched_observation;
CREATE INDEX idx_unmatched_observation_last ON unmatched_observation(last_seen_at);

CREATE TABLE prefix_new (
  id             TEXT PRIMARY KEY,
  cidr_text      TEXT NOT NULL,
  addr_family    INTEGER NOT NULL CONSTRAINT prefix_addr_family_check CHECK (addr_family IN (4,6)),
  addr_start     BYTEA NOT NULL,
  addr_end       BYTEA NOT NULL,
  vlan_id        INTEGER,
  environment_id TEXT REFERENCES environment(id),
  role           TEXT,
  CONSTRAINT prefix_cidr_text_key UNIQUE (cidr_text)
);
INSERT INTO prefix_new (id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id, role)
SELECT id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id, role FROM prefix;
DROP TABLE prefix;
ALTER TABLE prefix_new RENAME TO prefix;
CREATE INDEX idx_prefix_range ON prefix(addr_family, addr_start, addr_end);

CREATE TABLE rt_windows_new (
  instance_id       TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  service_name      TEXT NOT NULL,
  display_name      TEXT,
  binary_path       TEXT,
  start_type        TEXT CONSTRAINT rt_windows_start_type_check CHECK (start_type IN ('auto','auto_delayed','manual','disabled')),
  logon_identity_id TEXT REFERENCES identity(id),
  depends_on_svc    TEXT NOT NULL DEFAULT '[]',
  recovery_action   TEXT
);
INSERT INTO rt_windows_new (instance_id, service_name, display_name, binary_path, start_type, logon_identity_id, depends_on_svc, recovery_action)
SELECT instance_id, service_name, display_name, binary_path, start_type, logon_identity_id, depends_on_svc, recovery_action FROM rt_windows;
DROP TABLE rt_windows;
ALTER TABLE rt_windows_new RENAME TO rt_windows;

CREATE TABLE link_new (
  id             TEXT PRIMARY KEY,
  a_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  b_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  medium         TEXT,
  length_m       INTEGER,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CONSTRAINT link_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  CONSTRAINT link_distinct_interfaces_check CHECK (a_interface_id <> b_interface_id)
);
INSERT INTO link_new (id, a_interface_id, b_interface_id, medium, length_m, lifecycle)
SELECT id, a_interface_id, b_interface_id, medium, length_m, lifecycle FROM link;
DROP TABLE link;
ALTER TABLE link_new RENAME TO link;
CREATE INDEX idx_link_a ON link(a_interface_id);
CREATE INDEX idx_link_b ON link(b_interface_id);
CREATE INDEX idx_link_lifecycle ON link(lifecycle);

CREATE TABLE endpoint_new (
  id             TEXT PRIMARY KEY,
  service_id     TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  l4_proto       TEXT NOT NULL CONSTRAINT endpoint_l4_proto_check CHECK (l4_proto IN ('tcp','udp','sctp','unix')),
  port           INTEGER,
  unix_path      TEXT,
  bind_scope     TEXT NOT NULL CONSTRAINT endpoint_bind_scope_check CHECK (bind_scope IN
                   ('loopback','host','vip','cluster_ip','node_port','ingress','unix')),
  ip_address_id  TEXT REFERENCES ip_address(id),
  l7_proto       TEXT,
  tls_mode       TEXT NOT NULL DEFAULT 'none'
                   CONSTRAINT endpoint_tls_mode_check CHECK (tls_mode IN ('none','tls','mtls','starttls')),
  -- An opaque reference to whatever issues certificates. Certificates are not
  -- modelled as an entity in the POC (docs/DECISIONS.md Q5).
  certificate_id TEXT,
  exposure       TEXT NOT NULL DEFAULT 'internal'
                   CONSTRAINT endpoint_exposure_check CHECK (exposure IN ('internal','environment','cross_env','external')),
  CONSTRAINT endpoint_service_id_name_key UNIQUE (service_id, name),
  -- A unix socket has a path and no port; everything else is the other way
  -- round. Written as an explicit boolean expression rather than
  -- num_nonnulls(), which SQLite does not have.
  CONSTRAINT endpoint_port_or_unix_path_check CHECK ((l4_proto = 'unix' AND unix_path IS NOT NULL AND port IS NULL)
      OR (l4_proto <> 'unix' AND port IS NOT NULL AND unix_path IS NULL))
);
INSERT INTO endpoint_new (id, service_id, name, l4_proto, port, unix_path, bind_scope, ip_address_id, l7_proto, tls_mode, certificate_id, exposure)
SELECT id, service_id, name, l4_proto, port, unix_path, bind_scope, ip_address_id, l7_proto, tls_mode, certificate_id, exposure FROM endpoint;
DROP TABLE endpoint;
ALTER TABLE endpoint_new RENAME TO endpoint;
CREATE INDEX idx_endpoint_port    ON endpoint(port) WHERE port IS NOT NULL;
CREATE INDEX idx_endpoint_service ON endpoint(service_id);

CREATE TABLE route_new (
  id                   TEXT PRIMARY KEY,
  frontend_endpoint_id TEXT NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  match_type           TEXT NOT NULL CONSTRAINT route_match_type_check CHECK (match_type IN
                         ('sni','host_header','path_prefix','default')),
  match_value          TEXT,
  backend_pool_id      TEXT NOT NULL REFERENCES backend_pool(id),
  tls_termination      TEXT CONSTRAINT route_tls_termination_check CHECK (tls_termination IN
                         ('passthrough','terminate','reencrypt')),
  priority             INTEGER NOT NULL DEFAULT 100
);
INSERT INTO route_new (id, frontend_endpoint_id, match_type, match_value, backend_pool_id, tls_termination, priority)
SELECT id, frontend_endpoint_id, match_type, match_value, backend_pool_id, tls_termination, priority FROM route;
DROP TABLE route;
ALTER TABLE route_new RENAME TO route;
CREATE INDEX idx_route_frontend ON route(frontend_endpoint_id);
CREATE INDEX idx_route_pool     ON route(backend_pool_id);

CREATE TABLE dependency_new (
  id                   TEXT PRIMARY KEY,
  consumer_service_id  TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  consumer_instance_id TEXT REFERENCES service_instance(id) ON DELETE CASCADE,
  provider_endpoint_id TEXT REFERENCES endpoint(id) ON DELETE CASCADE,
  provider_route_id    TEXT REFERENCES route(id) ON DELETE CASCADE,
  nature               TEXT NOT NULL CONSTRAINT dependency_nature_check CHECK (nature IN
                         ('hard','soft','startup','async','optional')),
  tolerance_seconds    INTEGER,
  failure_mode         TEXT NOT NULL,
  identity_id          TEXT REFERENCES identity(id),
  auth_method          TEXT,
  firewall_rule_ref    TEXT,
  -- Declared data is authoritative; a reconciler never silently overwrites it.
  source               TEXT NOT NULL DEFAULT 'declared' CONSTRAINT dependency_source_check CHECK (source IN
                         ('declared','discovered_netstat','discovered_systemd',
                          'discovered_k8s','discovered_config')),
  confidence           REAL,
  first_seen           TEXT,
  last_seen            TEXT,
  verified_by          TEXT,
  verified_at          TEXT,
  -- Addition to the handover schema. "Soft delete only" is a hard rule, and a
  -- dependency edge is the thing operators most often need to withdraw after
  -- entering it wrongly -- without a lifecycle column the only options were a
  -- hard DELETE or leaving the wrong edge in place. See docs/DECISIONS.md.
  lifecycle            TEXT NOT NULL DEFAULT 'active'
                         CONSTRAINT dependency_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  CONSTRAINT dependency_provider_exclusive_check CHECK ((provider_endpoint_id IS NOT NULL AND provider_route_id IS NULL)
      OR (provider_endpoint_id IS NULL AND provider_route_id IS NOT NULL))
);
INSERT INTO dependency_new (id, consumer_service_id, consumer_instance_id, provider_endpoint_id, provider_route_id, nature, tolerance_seconds, failure_mode, identity_id, auth_method, firewall_rule_ref, source, confidence, first_seen, last_seen, verified_by, verified_at, lifecycle, created_at, updated_at)
SELECT id, consumer_service_id, consumer_instance_id, provider_endpoint_id, provider_route_id, nature, tolerance_seconds, failure_mode, identity_id, auth_method, firewall_rule_ref, source, confidence, first_seen, last_seen, verified_by, verified_at, lifecycle, created_at, updated_at FROM dependency;
DROP TABLE dependency;
ALTER TABLE dependency_new RENAME TO dependency;
CREATE INDEX idx_dep_consumer ON dependency(consumer_service_id);
CREATE INDEX idx_dep_endpoint ON dependency(provider_endpoint_id);
CREATE INDEX idx_dep_route    ON dependency(provider_route_id);

CREATE TABLE net_group_new (
  id             TEXT PRIMARY KEY,
  code           TEXT NOT NULL CONSTRAINT net_group_code_key UNIQUE,
  name           TEXT NOT NULL,
  -- Descriptive only; shown in the UI, not read by the algorithm. The failure
  -- semantics live in availability/failover_mode, which is what the engine needs.
  kind           TEXT NOT NULL CONSTRAINT net_group_kind_check CHECK (kind IN
                   ('standalone','ha_pair','mclag','stack','vrrp','cluster')),
  -- Load-bearing: derivation orients an undirected cable between two forwarders
  -- by rank (edge > core > distribution > access). Direction is the one thing a
  -- cable genuinely cannot tell you, so it is declared, never guessed.
  role           TEXT NOT NULL CONSTRAINT net_group_role_check CHECK (role IN ('edge','core','distribution','access')),
  availability   TEXT NOT NULL CONSTRAINT net_group_availability_check CHECK (availability IN
                   ('standalone','active_active','active_passive')),
  min_healthy    INTEGER,
  failover_mode  TEXT CONSTRAINT net_group_failover_mode_check CHECK (failover_mode IN ('auto','manual','none')),
  environment_id TEXT REFERENCES environment(id),
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CONSTRAINT net_group_lifecycle_check CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  -- HANDOVER 3.5: declared is authoritative and never silently overwritten.
  source         TEXT NOT NULL DEFAULT 'declared'
                   CONSTRAINT net_group_source_check CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence     REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  attrs          TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
INSERT INTO net_group_new (id, code, name, kind, role, availability, min_healthy, failover_mode, environment_id, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, attrs, created_at, updated_at)
SELECT id, code, name, kind, role, availability, min_healthy, failover_mode, environment_id, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, attrs, created_at, updated_at FROM net_group;
DROP TABLE net_group;
ALTER TABLE net_group_new RENAME TO net_group;

CREATE TABLE net_anchor_new (
  id             TEXT PRIMARY KEY,
  code           TEXT NOT NULL CONSTRAINT net_anchor_code_key UNIQUE,
  name           TEXT NOT NULL,
  scope          TEXT NOT NULL CONSTRAINT net_anchor_scope_check CHECK (scope IN ('environment','cross_env','external')),
  group_id       TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  environment_id TEXT REFERENCES environment(id),
  plane          TEXT NOT NULL DEFAULT 'data'
                   CONSTRAINT net_anchor_plane_check CHECK (plane IN ('data','mgmt','storage')),
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CONSTRAINT net_anchor_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  source         TEXT NOT NULL DEFAULT 'declared'
                   CONSTRAINT net_anchor_source_check CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence     REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT INTO net_anchor_new (id, code, name, scope, group_id, environment_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at)
SELECT id, code, name, scope, group_id, environment_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at FROM net_anchor;
DROP TABLE net_anchor;
ALTER TABLE net_anchor_new RENAME TO net_anchor;
CREATE INDEX idx_net_anchor_group ON net_anchor(group_id);

CREATE TABLE net_attachment_new (
  id         TEXT PRIMARY KEY,
  asset_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  group_id   TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  plane      TEXT NOT NULL DEFAULT 'data'
               CONSTRAINT net_attachment_plane_check CHECK (plane IN ('data','mgmt','storage')),
  lifecycle  TEXT NOT NULL DEFAULT 'active' CONSTRAINT net_attachment_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  source     TEXT NOT NULL DEFAULT 'declared'
               CONSTRAINT net_attachment_source_check CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT INTO net_attachment_new (id, asset_id, group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at)
SELECT id, asset_id, group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at FROM net_attachment;
DROP TABLE net_attachment;
ALTER TABLE net_attachment_new RENAME TO net_attachment;
CREATE INDEX idx_net_attach_asset ON net_attachment(asset_id, plane);
CREATE INDEX idx_net_attach_group ON net_attachment(group_id);
CREATE UNIQUE INDEX idx_net_attach_one
  ON net_attachment(asset_id, group_id, plane) WHERE lifecycle = 'active';

CREATE TABLE net_group_member_new (
  group_id   TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  asset_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  -- Same vocabulary as service_instance.role, because the same function reads it.
  role       TEXT NOT NULL DEFAULT 'member'
               CONSTRAINT net_group_member_role_check CHECK (role IN ('primary','standby','member')),
  lifecycle  TEXT NOT NULL DEFAULT 'active' CONSTRAINT net_group_member_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (group_id, asset_id)
);
INSERT INTO net_group_member_new (group_id, asset_id, role, lifecycle, created_at, updated_at)
SELECT group_id, asset_id, role, lifecycle, created_at, updated_at FROM net_group_member;
DROP TABLE net_group_member;
ALTER TABLE net_group_member_new RENAME TO net_group_member;
CREATE UNIQUE INDEX idx_net_group_member_one
  ON net_group_member(asset_id) WHERE lifecycle = 'active';

CREATE TABLE net_uplink_new (
  id                TEXT PRIMARY KEY,
  group_id          TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  upstream_group_id TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  plane             TEXT NOT NULL DEFAULT 'data'
                      CONSTRAINT net_uplink_plane_check CHECK (plane IN ('data','mgmt','storage')),
  lifecycle         TEXT NOT NULL DEFAULT 'active'
                      CONSTRAINT net_uplink_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  source            TEXT NOT NULL DEFAULT 'declared'
                      CONSTRAINT net_uplink_source_check CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence        REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CONSTRAINT net_uplink_distinct_groups_check CHECK (group_id <> upstream_group_id)
);
INSERT INTO net_uplink_new (id, group_id, upstream_group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at)
SELECT id, group_id, upstream_group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at FROM net_uplink;
DROP TABLE net_uplink;
ALTER TABLE net_uplink_new RENAME TO net_uplink;
CREATE INDEX idx_net_uplink_group ON net_uplink(group_id);
CREATE UNIQUE INDEX idx_net_uplink_one
  ON net_uplink(group_id, upstream_group_id, plane) WHERE lifecycle = 'active';
CREATE INDEX idx_net_uplink_up    ON net_uplink(upstream_group_id);

-- No PRAGMA foreign_key_check here. It reads like a safety net and is not one:
-- goose runs migration statements with Exec, the pragma reports violations as
-- result rows, and Exec discards them -- so it finds the problem, throws it
-- away and reports success. store.verifyForeignKeys runs the same check with
-- Query after every migration instead, where a violation can actually fail the
-- run.
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

-- Back to anonymous constraints, in the reverse order: a down migration
-- restores the schema it found rather than an improved one. Children before
-- parents here, mirroring the up.

CREATE TABLE net_uplink_old (
  id                TEXT PRIMARY KEY,
  group_id          TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  upstream_group_id TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  plane             TEXT NOT NULL DEFAULT 'data'
                      CHECK (plane IN ('data','mgmt','storage')),
  lifecycle         TEXT NOT NULL DEFAULT 'active'
                      CHECK (lifecycle IN ('active','retired')),
  source            TEXT NOT NULL DEFAULT 'declared'
                      CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence        REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK (group_id <> upstream_group_id)
);
INSERT INTO net_uplink_old (id, group_id, upstream_group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at)
SELECT id, group_id, upstream_group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at FROM net_uplink;
DROP TABLE net_uplink;
ALTER TABLE net_uplink_old RENAME TO net_uplink;
CREATE INDEX idx_net_uplink_group ON net_uplink(group_id);
CREATE UNIQUE INDEX idx_net_uplink_one
  ON net_uplink(group_id, upstream_group_id, plane) WHERE lifecycle = 'active';
CREATE INDEX idx_net_uplink_up    ON net_uplink(upstream_group_id);

CREATE TABLE net_group_member_old (
  group_id   TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  asset_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  -- Same vocabulary as service_instance.role, because the same function reads it.
  role       TEXT NOT NULL DEFAULT 'member'
               CHECK (role IN ('primary','standby','member')),
  lifecycle  TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','retired')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (group_id, asset_id)
);
INSERT INTO net_group_member_old (group_id, asset_id, role, lifecycle, created_at, updated_at)
SELECT group_id, asset_id, role, lifecycle, created_at, updated_at FROM net_group_member;
DROP TABLE net_group_member;
ALTER TABLE net_group_member_old RENAME TO net_group_member;
CREATE UNIQUE INDEX idx_net_group_member_one
  ON net_group_member(asset_id) WHERE lifecycle = 'active';

CREATE TABLE net_attachment_old (
  id         TEXT PRIMARY KEY,
  asset_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  group_id   TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  plane      TEXT NOT NULL DEFAULT 'data'
               CHECK (plane IN ('data','mgmt','storage')),
  lifecycle  TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','retired')),
  source     TEXT NOT NULL DEFAULT 'declared'
               CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT INTO net_attachment_old (id, asset_id, group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at)
SELECT id, asset_id, group_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at FROM net_attachment;
DROP TABLE net_attachment;
ALTER TABLE net_attachment_old RENAME TO net_attachment;
CREATE INDEX idx_net_attach_asset ON net_attachment(asset_id, plane);
CREATE INDEX idx_net_attach_group ON net_attachment(group_id);
CREATE UNIQUE INDEX idx_net_attach_one
  ON net_attachment(asset_id, group_id, plane) WHERE lifecycle = 'active';

CREATE TABLE net_anchor_old (
  id             TEXT PRIMARY KEY,
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  scope          TEXT NOT NULL CHECK (scope IN ('environment','cross_env','external')),
  group_id       TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  environment_id TEXT REFERENCES environment(id),
  plane          TEXT NOT NULL DEFAULT 'data'
                   CHECK (plane IN ('data','mgmt','storage')),
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CHECK (lifecycle IN ('active','retired')),
  source         TEXT NOT NULL DEFAULT 'declared'
                   CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence     REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT INTO net_anchor_old (id, code, name, scope, group_id, environment_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at)
SELECT id, code, name, scope, group_id, environment_id, plane, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, created_at, updated_at FROM net_anchor;
DROP TABLE net_anchor;
ALTER TABLE net_anchor_old RENAME TO net_anchor;
CREATE INDEX idx_net_anchor_group ON net_anchor(group_id);

CREATE TABLE net_group_old (
  id             TEXT PRIMARY KEY,
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  -- Descriptive only; shown in the UI, not read by the algorithm. The failure
  -- semantics live in availability/failover_mode, which is what the engine needs.
  kind           TEXT NOT NULL CHECK (kind IN
                   ('standalone','ha_pair','mclag','stack','vrrp','cluster')),
  -- Load-bearing: derivation orients an undirected cable between two forwarders
  -- by rank (edge > core > distribution > access). Direction is the one thing a
  -- cable genuinely cannot tell you, so it is declared, never guessed.
  role           TEXT NOT NULL CHECK (role IN ('edge','core','distribution','access')),
  availability   TEXT NOT NULL CHECK (availability IN
                   ('standalone','active_active','active_passive')),
  min_healthy    INTEGER,
  failover_mode  TEXT CHECK (failover_mode IN ('auto','manual','none')),
  environment_id TEXT REFERENCES environment(id),
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  -- HANDOVER 3.5: declared is authoritative and never silently overwritten.
  source         TEXT NOT NULL DEFAULT 'declared'
                   CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence     REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  attrs          TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
INSERT INTO net_group_old (id, code, name, kind, role, availability, min_healthy, failover_mode, environment_id, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, attrs, created_at, updated_at)
SELECT id, code, name, kind, role, availability, min_healthy, failover_mode, environment_id, lifecycle, source, confidence, first_seen, last_seen, verified_by, verified_at, attrs, created_at, updated_at FROM net_group;
DROP TABLE net_group;
ALTER TABLE net_group_old RENAME TO net_group;

CREATE TABLE dependency_old (
  id                   TEXT PRIMARY KEY,
  consumer_service_id  TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  consumer_instance_id TEXT REFERENCES service_instance(id) ON DELETE CASCADE,
  provider_endpoint_id TEXT REFERENCES endpoint(id) ON DELETE CASCADE,
  provider_route_id    TEXT REFERENCES route(id) ON DELETE CASCADE,
  nature               TEXT NOT NULL CHECK (nature IN
                         ('hard','soft','startup','async','optional')),
  tolerance_seconds    INTEGER,
  failure_mode         TEXT NOT NULL,
  identity_id          TEXT REFERENCES identity(id),
  auth_method          TEXT,
  firewall_rule_ref    TEXT,
  -- Declared data is authoritative; a reconciler never silently overwrites it.
  source               TEXT NOT NULL DEFAULT 'declared' CHECK (source IN
                         ('declared','discovered_netstat','discovered_systemd',
                          'discovered_k8s','discovered_config')),
  confidence           REAL,
  first_seen           TEXT,
  last_seen            TEXT,
  verified_by          TEXT,
  verified_at          TEXT,
  -- Addition to the handover schema. "Soft delete only" is a hard rule, and a
  -- dependency edge is the thing operators most often need to withdraw after
  -- entering it wrongly -- without a lifecycle column the only options were a
  -- hard DELETE or leaving the wrong edge in place. See docs/DECISIONS.md.
  lifecycle            TEXT NOT NULL DEFAULT 'active'
                         CHECK (lifecycle IN ('active','retired')),
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  CHECK ((provider_endpoint_id IS NOT NULL AND provider_route_id IS NULL)
      OR (provider_endpoint_id IS NULL AND provider_route_id IS NOT NULL))
);
INSERT INTO dependency_old (id, consumer_service_id, consumer_instance_id, provider_endpoint_id, provider_route_id, nature, tolerance_seconds, failure_mode, identity_id, auth_method, firewall_rule_ref, source, confidence, first_seen, last_seen, verified_by, verified_at, lifecycle, created_at, updated_at)
SELECT id, consumer_service_id, consumer_instance_id, provider_endpoint_id, provider_route_id, nature, tolerance_seconds, failure_mode, identity_id, auth_method, firewall_rule_ref, source, confidence, first_seen, last_seen, verified_by, verified_at, lifecycle, created_at, updated_at FROM dependency;
DROP TABLE dependency;
ALTER TABLE dependency_old RENAME TO dependency;
CREATE INDEX idx_dep_consumer ON dependency(consumer_service_id);
CREATE INDEX idx_dep_endpoint ON dependency(provider_endpoint_id);
CREATE INDEX idx_dep_route    ON dependency(provider_route_id);

CREATE TABLE route_old (
  id                   TEXT PRIMARY KEY,
  frontend_endpoint_id TEXT NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  match_type           TEXT NOT NULL CHECK (match_type IN
                         ('sni','host_header','path_prefix','default')),
  match_value          TEXT,
  backend_pool_id      TEXT NOT NULL REFERENCES backend_pool(id),
  tls_termination      TEXT CHECK (tls_termination IN
                         ('passthrough','terminate','reencrypt')),
  priority             INTEGER NOT NULL DEFAULT 100
);
INSERT INTO route_old (id, frontend_endpoint_id, match_type, match_value, backend_pool_id, tls_termination, priority)
SELECT id, frontend_endpoint_id, match_type, match_value, backend_pool_id, tls_termination, priority FROM route;
DROP TABLE route;
ALTER TABLE route_old RENAME TO route;
CREATE INDEX idx_route_frontend ON route(frontend_endpoint_id);
CREATE INDEX idx_route_pool     ON route(backend_pool_id);

CREATE TABLE endpoint_old (
  id             TEXT PRIMARY KEY,
  service_id     TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  l4_proto       TEXT NOT NULL CHECK (l4_proto IN ('tcp','udp','sctp','unix')),
  port           INTEGER,
  unix_path      TEXT,
  bind_scope     TEXT NOT NULL CHECK (bind_scope IN
                   ('loopback','host','vip','cluster_ip','node_port','ingress','unix')),
  ip_address_id  TEXT REFERENCES ip_address(id),
  l7_proto       TEXT,
  tls_mode       TEXT NOT NULL DEFAULT 'none'
                   CHECK (tls_mode IN ('none','tls','mtls','starttls')),
  -- An opaque reference to whatever issues certificates. Certificates are not
  -- modelled as an entity in the POC (docs/DECISIONS.md Q5).
  certificate_id TEXT,
  exposure       TEXT NOT NULL DEFAULT 'internal'
                   CHECK (exposure IN ('internal','environment','cross_env','external')),
  UNIQUE (service_id, name),
  -- A unix socket has a path and no port; everything else is the other way
  -- round. Written as an explicit boolean expression rather than
  -- num_nonnulls(), which SQLite does not have.
  CHECK ((l4_proto = 'unix' AND unix_path IS NOT NULL AND port IS NULL)
      OR (l4_proto <> 'unix' AND port IS NOT NULL AND unix_path IS NULL))
);
INSERT INTO endpoint_old (id, service_id, name, l4_proto, port, unix_path, bind_scope, ip_address_id, l7_proto, tls_mode, certificate_id, exposure)
SELECT id, service_id, name, l4_proto, port, unix_path, bind_scope, ip_address_id, l7_proto, tls_mode, certificate_id, exposure FROM endpoint;
DROP TABLE endpoint;
ALTER TABLE endpoint_old RENAME TO endpoint;
CREATE INDEX idx_endpoint_port    ON endpoint(port) WHERE port IS NOT NULL;
CREATE INDEX idx_endpoint_service ON endpoint(service_id);

CREATE TABLE link_old (
  id             TEXT PRIMARY KEY,
  a_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  b_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  medium         TEXT,
  length_m       INTEGER,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CHECK (lifecycle IN ('active','retired')),
  CHECK (a_interface_id <> b_interface_id)
);
INSERT INTO link_old (id, a_interface_id, b_interface_id, medium, length_m, lifecycle)
SELECT id, a_interface_id, b_interface_id, medium, length_m, lifecycle FROM link;
DROP TABLE link;
ALTER TABLE link_old RENAME TO link;
CREATE INDEX idx_link_a ON link(a_interface_id);
CREATE INDEX idx_link_b ON link(b_interface_id);
CREATE INDEX idx_link_lifecycle ON link(lifecycle);

CREATE TABLE rt_windows_old (
  instance_id       TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  service_name      TEXT NOT NULL,
  display_name      TEXT,
  binary_path       TEXT,
  start_type        TEXT CHECK (start_type IN ('auto','auto_delayed','manual','disabled')),
  logon_identity_id TEXT REFERENCES identity(id),
  depends_on_svc    TEXT NOT NULL DEFAULT '[]',
  recovery_action   TEXT
);
INSERT INTO rt_windows_old (instance_id, service_name, display_name, binary_path, start_type, logon_identity_id, depends_on_svc, recovery_action)
SELECT instance_id, service_name, display_name, binary_path, start_type, logon_identity_id, depends_on_svc, recovery_action FROM rt_windows;
DROP TABLE rt_windows;
ALTER TABLE rt_windows_old RENAME TO rt_windows;

CREATE TABLE prefix_old (
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
INSERT INTO prefix_old (id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id, role)
SELECT id, cidr_text, addr_family, addr_start, addr_end, vlan_id, environment_id, role FROM prefix;
DROP TABLE prefix;
ALTER TABLE prefix_old RENAME TO prefix;
CREATE INDEX idx_prefix_range ON prefix(addr_family, addr_start, addr_end);

CREATE TABLE unmatched_observation_old (
  id            TEXT PRIMARY KEY,
  entity_type   TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_ref    TEXT NOT NULL,
  reporter      TEXT NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('up','degraded','down','unknown')),
  reported_at   TEXT NOT NULL,
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  report_count  INTEGER NOT NULL DEFAULT 1 CHECK (report_count > 0),
  UNIQUE (entity_type, entity_ref, reporter)
);
INSERT INTO unmatched_observation_old (id, entity_type, entity_ref, reporter, state, reported_at, first_seen_at, last_seen_at, report_count)
SELECT id, entity_type, entity_ref, reporter, state, reported_at, first_seen_at, last_seen_at, report_count FROM unmatched_observation;
DROP TABLE unmatched_observation;
ALTER TABLE unmatched_observation_old RENAME TO unmatched_observation;
CREATE INDEX idx_unmatched_observation_last ON unmatched_observation(last_seen_at);

CREATE TABLE observed_transition_old (
  id            TEXT PRIMARY KEY,
  entity_type   TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_id     TEXT NOT NULL,
  reporter      TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'transition'
                  CHECK (kind IN ('transition','flap_open','flap_close')),
  from_state    TEXT CHECK (from_state IS NULL OR
                  from_state IN ('up','degraded','down','unknown')),
  to_state      TEXT NOT NULL CHECK (to_state IN ('up','degraded','down','unknown')),
  at            TEXT NOT NULL,
  -- flap_close detail. Null on every other kind.
  flap_count    INTEGER CHECK (flap_count IS NULL OR flap_count > 0),
  window_start  TEXT,
  window_end    TEXT,
  first_state   TEXT CHECK (first_state IS NULL OR
                  first_state IN ('up','degraded','down','unknown')),
  last_state    TEXT CHECK (last_state IS NULL OR
                  last_state IN ('up','degraded','down','unknown')),
  settled_state TEXT CHECK (settled_state IS NULL OR
                  settled_state IN ('up','degraded','down','unknown')),
  -- A flap_close that cannot say how much it hid is worse than no compression.
  CHECK (kind <> 'flap_close' OR (flap_count IS NOT NULL AND window_start IS NOT NULL
         AND window_end IS NOT NULL AND first_state IS NOT NULL
         AND last_state IS NOT NULL AND settled_state IS NOT NULL))
);
INSERT INTO observed_transition_old (id, entity_type, entity_id, reporter, kind, from_state, to_state, at, flap_count, window_start, window_end, first_state, last_state, settled_state)
SELECT id, entity_type, entity_id, reporter, kind, from_state, to_state, at, flap_count, window_start, window_end, first_state, last_state, settled_state FROM observed_transition;
DROP TABLE observed_transition;
ALTER TABLE observed_transition_old RENAME TO observed_transition;
CREATE INDEX idx_observed_transition_at ON observed_transition(at);
CREATE INDEX idx_observed_transition_entity ON observed_transition(entity_type, entity_id, at);
CREATE INDEX idx_observed_transition_window
  ON observed_transition(entity_type, entity_id, reporter, at);

CREATE TABLE health_override_old (
  id             TEXT PRIMARY KEY,
  entity_type    TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_id      TEXT NOT NULL,
  asserted_state TEXT NOT NULL CHECK (asserted_state IN ('up','degraded','down','unknown')),
  reason         TEXT NOT NULL CHECK (reason <> ''),
  actor          TEXT NOT NULL,
  lifecycle      TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','cleared')),
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  expires_at     TEXT NOT NULL,
  CHECK (expires_at > created_at)
);
INSERT INTO health_override_old (id, entity_type, entity_id, asserted_state, reason, actor, lifecycle, created_at, updated_at, expires_at)
SELECT id, entity_type, entity_id, asserted_state, reason, actor, lifecycle, created_at, updated_at, expires_at FROM health_override;
DROP TABLE health_override;
ALTER TABLE health_override_old RENAME TO health_override;
CREATE INDEX idx_health_override_expiry ON health_override(expires_at);
CREATE UNIQUE INDEX idx_health_override_one
  ON health_override(entity_type, entity_id) WHERE lifecycle = 'active';

CREATE TABLE asset_health_old (
  entity_type      TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_id        TEXT NOT NULL,
  reporter         TEXT NOT NULL,
  state            TEXT NOT NULL CHECK (state IN ('up','degraded','down','unknown')),
  state_since      TEXT NOT NULL,
  reported_at      TEXT NOT NULL,
  last_report_at   TEXT NOT NULL,
  interval_seconds INTEGER CHECK (interval_seconds IS NULL OR interval_seconds > 0),
  observation_id   TEXT,
  -- Rule 9's open episode. All four are NULL together or set together.
  flap_since       TEXT,
  flap_count       INTEGER CHECK (flap_count IS NULL OR flap_count > 0),
  flap_first_state TEXT CHECK (flap_first_state IS NULL OR
                     flap_first_state IN ('up','degraded','down','unknown')),
  flap_seen        TEXT,
  PRIMARY KEY (entity_type, entity_id, reporter),
  CHECK ((flap_since IS NULL AND flap_count IS NULL AND flap_first_state IS NULL
          AND flap_seen IS NULL)
      OR (flap_since IS NOT NULL AND flap_count IS NOT NULL AND flap_first_state IS NOT NULL
          AND flap_seen IS NOT NULL))
);
INSERT INTO asset_health_old (entity_type, entity_id, reporter, state, state_since, reported_at, last_report_at, interval_seconds, observation_id, flap_since, flap_count, flap_first_state, flap_seen)
SELECT entity_type, entity_id, reporter, state, state_since, reported_at, last_report_at, interval_seconds, observation_id, flap_since, flap_count, flap_first_state, flap_seen FROM asset_health;
DROP TABLE asset_health;
ALTER TABLE asset_health_old RENAME TO asset_health;
CREATE INDEX idx_asset_health_entity ON asset_health(entity_type, entity_id);
CREATE INDEX idx_asset_health_reporter ON asset_health(reporter, last_report_at);

CREATE TABLE app_user_old (
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
INSERT INTO app_user_old (id, username, display_name, email, source, password_hash, is_active, last_login_at, created_at)
SELECT id, username, display_name, email, source, password_hash, is_active, last_login_at, created_at FROM app_user;
DROP TABLE app_user;
ALTER TABLE app_user_old RENAME TO app_user;

PRAGMA foreign_keys = ON;
