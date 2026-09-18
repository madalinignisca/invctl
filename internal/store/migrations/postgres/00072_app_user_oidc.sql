-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Keycloak (OIDC) authentication (docs/superpowers/specs/2026-09-18-keycloak-oidc-design.md).
--
-- `subject` carries the OIDC `sub` claim: an immutable identifier issued by
-- the provider, present only for accounts created through the OIDC flow.
-- Matching an incoming sign-in on `subject` rather than on `username` is the
-- whole of D2 -- a Keycloak account renamed later still resolves to the same
-- app_user row, and a Keycloak account merely SHARING a username with an
-- existing local or LDAP account is not thereby the same person (D4).
--
-- THE PARTIAL INDEX, NOT A PLAIN ONE. `app_user.subject` is NULL for every
-- local and LDAP account -- there is no `sub` to record for either. A plain
-- UNIQUE index treats every one of those NULLs as a distinct index entry in
-- SQL's NULL-is-not-equal-to-NULL rule, so this would still work by
-- accident on both engines even as a plain index -- but relying on that
-- accident is exactly the shape of bug this file's own migration history
-- warns about (00005's header, on a different index, for the same reason).
-- The WHERE clause makes "two NULLs never collide" a stated property instead
-- of a coincidence of NULL semantics, and it is what
-- TestSubjectIsNullableForLocalAndLDAP pins down.
--
-- `source_check` was never given an explicit name on this engine (see
-- shared/00005_audit_auth.sql's CREATE TABLE), but PostgreSQL auto-names a
-- column-level CHECK `<table>_<column>_check` when none is given, which is
-- exactly `app_user_source_check` here -- confirmed against the pinned
-- PostgreSQL major version before relying on it in this DROP CONSTRAINT.
-- SQLite has no equivalent auto-naming, which is why sqlite/00005 had to name
-- it explicitly and why this migration's SQLite half is a full table rebuild
-- instead of one ALTER.

-- +goose Up
ALTER TABLE app_user ADD COLUMN subject TEXT;

ALTER TABLE app_user DROP CONSTRAINT app_user_source_check;
ALTER TABLE app_user ADD CONSTRAINT app_user_source_check
  CHECK (source IN ('local', 'ldap', 'oidc'));

CREATE UNIQUE INDEX app_user_subject_key ON app_user(subject) WHERE subject IS NOT NULL;

-- +goose Down
DROP INDEX app_user_subject_key;

ALTER TABLE app_user DROP CONSTRAINT app_user_source_check;
ALTER TABLE app_user ADD CONSTRAINT app_user_source_check
  CHECK (source IN ('local', 'ldap'));

ALTER TABLE app_user DROP COLUMN subject;
