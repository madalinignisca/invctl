-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Layer 4: applications, logical services, running instances, runtime detail.
--
-- identity is created here, before rt_windows, because a Windows service's
-- run-as account is a real foreign key into it.

CREATE TABLE application (
  id         TEXT PRIMARY KEY,
  code       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  owner_team TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE identity (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN
                  ('service_account','machine_account','api_token','cert_subject','human')),
  name          TEXT NOT NULL,
  realm         TEXT,
  -- A Vault path or similar. NEVER the secret itself. If a code path would
  -- put an actual secret here, that is a bug to raise, not to work around.
  secret_ref    TEXT,
  rotation_days INTEGER,
  last_rotated  TEXT,
  owner_team    TEXT,
  lifecycle     TEXT NOT NULL DEFAULT 'active',
  UNIQUE (realm, name)
);

-- The logical service: one row regardless of replica count. Dependencies and
-- ownership attach here so an edge is written once, not once per replica.
CREATE TABLE service (
  id             TEXT PRIMARY KEY,
  application_id TEXT REFERENCES application(id),
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  kind           TEXT NOT NULL CHECK (kind IN
                   ('db','cache','queue','web','api','proxy','auth','batch',
                    'agent','storage','infra','monitoring')),
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
CREATE INDEX idx_service_env  ON service(environment_id);
CREATE INDEX idx_service_app  ON service(application_id);

-- One running copy on one host. `shard` is an addition to the handover schema:
-- the sharded availability policy needs a shard key to evaluate "every shard
-- has at least one replica", and there was nowhere to put it. See
-- docs/DECISIONS.md.
CREATE TABLE service_instance (
  id             TEXT PRIMARY KEY,
  service_id     TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  host_asset_id  TEXT NOT NULL REFERENCES asset(id),
  runtime_type   TEXT NOT NULL CHECK (runtime_type IN
                   ('systemd','windows_service','container','k8s_workload','appliance')),
  role           TEXT,
  shard          TEXT,
  ordinal        INTEGER NOT NULL DEFAULT 0,
  desired_state  TEXT NOT NULL DEFAULT 'running'
                   CHECK (desired_state IN ('running','stopped','disabled')),
  observed_state TEXT,
  observed_at    TEXT,
  source         TEXT NOT NULL DEFAULT 'declared',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  UNIQUE (service_id, host_asset_id, ordinal)
);
CREATE INDEX idx_instance_host    ON service_instance(host_asset_id);
CREATE INDEX idx_instance_service ON service_instance(service_id);

CREATE TABLE rt_systemd (
  instance_id   TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  unit_name     TEXT NOT NULL,
  unit_type     TEXT,
  exec_start    TEXT,
  run_as_user   TEXT,
  run_as_group  TEXT,
  restart       TEXT,
  -- JSON arrays, opaque to SQL. Parsed in Go, never queried with json_extract.
  unit_after    TEXT NOT NULL DEFAULT '[]',
  unit_requires TEXT NOT NULL DEFAULT '[]',
  drop_ins      TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE rt_windows (
  instance_id       TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  service_name      TEXT NOT NULL,
  display_name      TEXT,
  binary_path       TEXT,
  start_type        TEXT CHECK (start_type IN ('auto','auto_delayed','manual','disabled')),
  logon_identity_id TEXT REFERENCES identity(id),
  depends_on_svc    TEXT NOT NULL DEFAULT '[]',
  recovery_action   TEXT
);

CREATE TABLE rt_container (
  instance_id     TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  engine          TEXT CHECK (engine IN ('docker','podman')),
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

CREATE TABLE rt_k8s (
  instance_id      TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  cluster_asset_id TEXT REFERENCES asset(id),
  namespace        TEXT,
  workload_kind    TEXT,
  workload_name    TEXT,
  replicas_desired INTEGER,
  service_account  TEXT,
  image_digest     TEXT
);

-- +goose Down
DROP TABLE rt_k8s;
DROP TABLE rt_container;
DROP TABLE rt_windows;
DROP TABLE rt_systemd;
DROP TABLE service_instance;
DROP TABLE service;
DROP TABLE identity;
DROP TABLE application;
