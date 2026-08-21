-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- The persisted fallback for INV_AUDIT_FOLD_KEY (GDPR mitigation for WP-A4's
-- custom fields, docs/AUDIT.md's custom_field_value row and
-- docs/custom-fields-design.md §5).
--
-- A custom value's text no longer reaches change_log; foldCustomValues
-- (internal/store/customvalues.go) folds a keyed HMAC-SHA256 digest of it
-- instead. THE KEY MUST NEVER CHANGE UNDER A RUNNING DEPLOYMENT'S DATA: a
-- session key regenerated at startup only logs people out, but a fold key
-- regenerated at startup changes every digest, so every entity holding a
-- custom value shows a spurious diff on its very next save, forever. When an
-- operator has not set INV_AUDIT_FOLD_KEY, this table is where the key that
-- was generated on the FIRST start is kept, so every later start reads the
-- same key back instead of generating a new one.
--
-- ONE ROW, id FIXED AT 'default'. Not a general settings table -- this
-- schema has never had one and does not gain one here; a second setting
-- needing the same shape gets its own migration and its own table, the same
-- way every other single-purpose table in this schema is named for what it
-- holds rather than for being a place to put things.
--
-- key_b64 rather than a raw BLOB: every other secret-shaped value in this
-- schema (id, timestamp) is TEXT, and a 32-byte key base64-encodes to 44
-- ASCII characters -- portable across engines with no BLOB comparison
-- semantics to keep aligned.
--
-- NO change_log ENTRY IS WRITTEN FOR THIS ROW, ever. It is not declared
-- state -- nobody asserts it, no person decided its value, and it names no
-- fact about the estate -- it is an internal secret this process needs to
-- keep behaving the way it behaved yesterday, the same category as a TLS
-- private key. Auditing it would also defeat its own purpose: change_log is
-- exactly the record this key exists to stop from carrying the key material
-- it protects.

-- +goose Up

CREATE TABLE audit_fold_key (
  id         TEXT PRIMARY KEY NOT NULL,
  key_b64    TEXT NOT NULL,
  created_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE audit_fold_key;
