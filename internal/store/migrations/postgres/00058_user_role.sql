-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Replaces "a comma-separated list of usernames in an environment variable
-- grants write to everything" (WP-G1) with a role a person is given, rather
-- than an environment variable a deploy carries. This migration is schema
-- only: nothing reads these columns yet. Task 2 adds the Go constants,
-- Task 4 teaches Authorizer.CanWrite/CanSeeCosts to consult them and
-- reconciles INV_ADMIN_USERS into the new column at startup.
--
-- THE DEFAULT IS `observer`, AND THAT IS THE SECURITY DECISION HERE. Every
-- existing app_user row -- and every row an unauthenticated LDAP first-login
-- creates from here forward, until Task 4 lands -- becomes a reader, not a
-- writer. A default of `administrator` would silently promote every account
-- that has ever logged in, including a first-time LDAP bind that upserts an
-- app_user row from a route nobody reviewed. Anyone who actually needs write
-- access today is already named in INV_ADMIN_USERS -- so choosing the safe
-- default here costs nothing an administrator cannot immediately restore,
-- while the unsafe default would need to be caught before it was ever
-- deployed.
--
-- CORRECTION, made after deploying this: an earlier version of this comment
-- said Task 4 "reconciles that list into `role` at startup". It does not, and
-- nothing does. INV_ADMIN_USERS is consulted live, on every check, and takes
-- precedence over this column (auth.Authorizer.isAdministrator tests the
-- env list FIRST, then the role) -- which is what makes it a break-glass
-- path that works even when the column says otherwise. The roster shows the
-- difference rather than hiding it, via Authorizer.EnvOverride and the
-- "Administrator (from INV_ADMIN_USERS)" marker.
--
-- The operational consequence, which the reconciliation story would have
-- hidden: on a deployment whose only writer is named in INV_ADMIN_USERS,
-- that row's `role` column still reads `observer`, and removing the
-- environment variable leaves the estate with no writer at all. Set the role
-- properly as well as naming the account; see docs/RECOVERY.md.
--
-- can_see_costs DEFAULTS TO FALSE and is consulted for BOTH observer and
-- project_owner (spec §3 as corrected) -- seeing what something costs is a
-- separate grant from being able to change it, and a project owner managing
-- their own estate does not thereby learn what a shared cluster costs.
-- Administrator sees costs implicitly and NEVER consults this column: that
-- branch belongs in Authorizer.CanSeeCosts (Task 4), not in the schema, so
-- there is exactly one column and exactly one place that decides the grant --
-- encoding a role-specific exception here would mean two places could
-- disagree about what an administrator is allowed to see.

-- +goose Up

ALTER TABLE app_user ADD COLUMN role TEXT NOT NULL DEFAULT 'observer';

ALTER TABLE app_user ADD CONSTRAINT app_user_role_check
  CHECK (role IN ('administrator', 'observer', 'project_owner'));

ALTER TABLE app_user ADD COLUMN can_see_costs BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE app_user DROP COLUMN can_see_costs;
ALTER TABLE app_user DROP CONSTRAINT app_user_role_check;
ALTER TABLE app_user DROP COLUMN role;
