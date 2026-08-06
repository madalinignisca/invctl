-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Endpoints, L7 routing, and the dependency edge that the whole tool exists for.

CREATE TABLE endpoint (
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
CREATE INDEX idx_endpoint_service ON endpoint(service_id);
CREATE INDEX idx_endpoint_port    ON endpoint(port) WHERE port IS NOT NULL;

CREATE TABLE backend_pool (
  id           TEXT PRIMARY KEY,
  service_id   TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  lb_algorithm TEXT,
  UNIQUE (service_id, name)
);

CREATE TABLE backend_member (
  pool_id     TEXT NOT NULL REFERENCES backend_pool(id) ON DELETE CASCADE,
  endpoint_id TEXT NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  weight      INTEGER NOT NULL DEFAULT 1,
  is_backup   BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (pool_id, endpoint_id)
);
CREATE INDEX idx_member_endpoint ON backend_member(endpoint_id);

-- A route is a node in the dependency graph, not a passthrough: its health is
-- derived from its pool's members. That is what surfaces "the proxy is up but
-- every backend is on the node you are about to reboot".
CREATE TABLE route (
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
CREATE INDEX idx_route_frontend ON route(frontend_endpoint_id);
CREATE INDEX idx_route_pool     ON route(backend_pool_id);

CREATE TABLE dependency (
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
CREATE INDEX idx_dep_consumer ON dependency(consumer_service_id);
CREATE INDEX idx_dep_endpoint ON dependency(provider_endpoint_id);
CREATE INDEX idx_dep_route    ON dependency(provider_route_id);

CREATE TABLE dependency_data_class (
  dependency_id TEXT NOT NULL REFERENCES dependency(id) ON DELETE CASCADE,
  data_class    TEXT NOT NULL CHECK (data_class IN
                  ('chd','sad','pii','credential','telemetry','config','none')),
  PRIMARY KEY (dependency_id, data_class)
);

-- +goose Down
DROP TABLE dependency_data_class;
DROP TABLE dependency;
DROP TABLE route;
DROP TABLE backend_member;
DROP TABLE backend_pool;
DROP TABLE endpoint;
