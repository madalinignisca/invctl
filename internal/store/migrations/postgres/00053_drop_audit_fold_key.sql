-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Drops audit_fold_key (migration 00052). The keyed HMAC digest it supported
-- has been replaced with a plain change counter -- foldCustomValues
-- (internal/store/customvalues.go) now folds "code@<row_version>" instead of
-- "code=#<digest>", using custom_field_value.row_version, which already
-- advances on every real change and holds still on a no-op resubmission. See
-- docs/AUDIT.md's custom_field_value row and docs/custom-fields-design.md §5
-- for why: the digest was pseudonymisation, not anonymisation (its key sat
-- in this same database), it was invertible with no key at all for a
-- `select` or `boolean` field (identical values digest identically, and the
-- option list is public), and a 48-bit collision could write no change_log
-- row at all for a real change. A counter has none of those properties: it
-- carries no information about the value, two consecutive values of one
-- field can never collide, and there is no key to manage.
--
-- NOTHING IS LOST. A digest was one-way by design -- nothing ever read one
-- back to recover a value -- so there is no data this migration needs to
-- carry forward. The only deployment this project has ever run held ZERO
-- rows in this table: its key was supplied via INV_AUDIT_FOLD_KEY from the
-- environment before the process's first start, so ResolveAuditFoldKey
-- never had reason to persist a generated one here.
--
-- change_log ITSELF IS UNTOUCHED, and stays heterogeneous. It is
-- append-only, so entries written before this migration -- some holding the
-- pre-digest plaintext, some holding "code=#<digest>" from the last few
-- days -- keep exactly what they were written with; nothing here rewrites a
-- stored diff. Only entries written from this migration forward carry
-- "code@<n>".

-- +goose Up

DROP TABLE audit_fold_key;

-- +goose Down

CREATE TABLE audit_fold_key (
  id         TEXT PRIMARY KEY NOT NULL,
  key_b64    TEXT NOT NULL,
  created_at TEXT NOT NULL
);
