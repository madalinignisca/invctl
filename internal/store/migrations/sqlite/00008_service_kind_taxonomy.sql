-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Dialect-split despite being byte-identical in both directories, and that is
-- not an oversight. The seven lookup tables are created by the DIALECT
-- migration 00004, and Migrate applies every shared migration before any
-- dialect one -- so a shared migration touching these tables runs before they
-- exist. Measured, not assumed: as shared/00010 this failed with "no such
-- table: asset_kind". Placement follows the dependency, not the SQL.
--
-- Two service kinds that were missing, and the two they were being folded into.
--
-- Both came out of a systems administrator reading the vocabulary cold and
-- saying what the words meant to him. That is the only test this kind of thing
-- has, and it failed twice:
--
--   `proxy` had to mean BOTH a forward proxy -- Squid, an egress gateway,
--   traffic going out on behalf of clients -- AND a reverse proxy or L7 load
--   balancer taking traffic in. Opposite directions, different failure
--   semantics. And the distinction is load-bearing here rather than academic:
--   the reverse proxy is what owns `route` rows and backend pools, which is
--   the whole reason route-as-node exists in the impact engine. A Squid box
--   has none of that.
--
--   `auth` had to mean BOTH an identity provider -- Keycloak, LDAP, OIDC --
--   AND a secret manager like Vault. The schema already treats secrets as
--   their own concern: identity.secret_ref holds a path into exactly such a
--   system, under a hard rule that the value itself never enters this
--   database. Calling that "auth" flattened a distinction the rest of the
--   model already makes.
--
-- ADDITIVE ON PURPOSE. `proxy` and `auth` both survive, so every existing row
-- stays legal and nothing needs rewriting. That is not caution, it is honesty:
-- no data migration could tell whether somebody's existing `proxy` row is
-- Squid or HAProxy, so guessing would silently corrupt the very distinction
-- this migration exists to draw. An estate gets two new codes and reclassifies
-- what it chooses to, when it chooses to.
--
-- The demo fixture DOES reclassify, because we know what its services are:
-- haproxy-edge becomes `lb` and vault becomes `secrets` in internal/seed.

-- +goose Up
INSERT INTO service_kind (code, label, sort_order, description) VALUES
  ('lb', 'Load balancer', 55,
   'A REVERSE proxy or L7 load balancer taking traffic in and spreading it over a pool — HAProxy, nginx as a front end, an AWS ALB. This is the kind that owns routes and backend pools, so an outage is felt by whatever the pool was serving. For outbound traffic on behalf of clients use `proxy`.'),
  ('secrets', 'Secret manager', 75,
   'A secret store — Vault, a cloud KMS, a credential broker. Distinct from `auth`, which answers who someone is: this one holds material other systems need at startup, which is why it turns up as a startup dependency far more often than a runtime one. identity.secret_ref points into a system of this kind, and never carries the secret itself.');

-- +goose Down
DELETE FROM service_kind WHERE code IN ('lb', 'secrets');
