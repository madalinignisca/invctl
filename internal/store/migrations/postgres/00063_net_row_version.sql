-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A forwarder group and an anchor can be corrected, so they need the token.
--
-- WHY THESE TWO TABLES HAVE NEVER HAD ONE. They were created in migration
-- 00005, before this schema adopted `row_version` as its optimistic-concurrency
-- token -- the interface table's own comment dates that convention to 00019,
-- and net_* simply predates it. Nothing recorded a decision to leave them out;
-- there was no correction path to need one, so nobody noticed.
--
-- WHY IT MATTERS NOW. WP-1.2 adds UpdateNetGroup and UpdateNetAnchor, and
-- net_group.availability, min_healthy and failover_mode are the semantics
-- HANDOVER §3.3 says make impact analysis mean anything. Two people correcting
-- min_healthy at the same time would silently overwrite each other, and the
-- losing edit takes an impact verdict with it.
--
-- IT ALSO BUYS COVERAGE, not merely correctness. TestEveryEditFormCarriesItsVersion
-- (internal/web) derives its population from the handlers that reach
-- submittedVersion. A correction path built without a token is not flagged by
-- it -- it is simply ABSENT from the population, which is the silent-exclusion
-- shape that census exists to prevent one layer down. Carrying the token puts
-- these two routes inside it.
--
-- NOT net_uplink OR net_attachment. Both are edges with no attributes of their
-- own beyond their endpoints and a plane, so neither gets a correction path at
-- all -- moving one is a different edge, the same reasoning
-- writeSurfaceByDesign already records for CircuitTermination. A column they
-- would never read would be noise.

-- +goose Up
ALTER TABLE net_group  ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE net_anchor ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE net_anchor DROP COLUMN row_version;
ALTER TABLE net_group  DROP COLUMN row_version;
