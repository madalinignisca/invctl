-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- When does this stop being supportable.
--
-- `eol_date` is the date after which a thing is expected to stop being fit for
-- service: vendor support ends, the warranty runs out, the licence is not
-- renewable, the box is past the age we replace at. One date, deliberately, and
-- what it means for a given row is prose in `description` -- not three columns
-- (warranty, support, planned replacement) that nobody fills in consistently
-- and that no report can then combine.
--
-- DECLARED, NOT OBSERVED. Naming does not decide the class and this one reads
-- like a fact about the world, but it is not: somebody read a contract and
-- typed it. Nothing reports it, nothing derives it, and a monitoring credential
-- must never write it. It belongs beside `lifecycle` -- see docs/AUDIT.md, and
-- the classification census in domain/classification.go, which fails if a
-- column is added here without a decision recorded there.
--
-- A DATE, NOT A TIMESTAMP. `YYYY-MM-DD`, ten characters, which sorts
-- lexicographically for the same reason the RFC3339 timestamps do. Support
-- ends on a day, not at a second, and storing 00:00:00Z would invite a reader
-- to believe a precision that is not there.
--
-- The CHECK is a shape test, not a calendar. `length` and `substr` are the two
-- string functions that behave identically on both engines; a real date parse
-- happens in Go, where the constructor rejects 2027-02-31 and this cannot.
-- Named, per the rule the 2026-07-29 entry set: an unnamed inline constraint is
-- one of the exactly three shapes SQLite cannot alter later.
--
-- Measured against the pinned driver (modernc.org/sqlite v1.54.0, SQLite
-- 3.53.3): ADD COLUMN then ADD CONSTRAINT ... CHECK both succeed, the
-- constraint validates existing rows, a malformed date is rejected, and the
-- resulting DDL carries the constraint with no table rebuild.

-- +goose Up
ALTER TABLE asset ADD COLUMN eol_date TEXT;
ALTER TABLE asset ADD CONSTRAINT asset_eol_date_check
  CHECK (eol_date IS NULL OR (length(eol_date) = 10
         AND substr(eol_date, 5, 1) = '-' AND substr(eol_date, 8, 1) = '-'));

ALTER TABLE service ADD COLUMN eol_date TEXT;
ALTER TABLE service ADD CONSTRAINT service_eol_date_check
  CHECK (eol_date IS NULL OR (length(eol_date) = 10
         AND substr(eol_date, 5, 1) = '-' AND substr(eol_date, 8, 1) = '-'));

-- The expiry report scans for rows with a date at all, and there are far more
-- rows without one than with. A partial index is the whole point here: it
-- indexes the handful of assets somebody has actually dated.
CREATE INDEX idx_asset_eol   ON asset(eol_date)   WHERE eol_date IS NOT NULL;
CREATE INDEX idx_service_eol ON service(eol_date) WHERE eol_date IS NOT NULL;

-- +goose Down
DROP INDEX idx_service_eol;
DROP INDEX idx_asset_eol;
ALTER TABLE service DROP CONSTRAINT service_eol_date_check;
ALTER TABLE service DROP COLUMN eol_date;
ALTER TABLE asset DROP CONSTRAINT asset_eol_date_check;
ALTER TABLE asset DROP COLUMN eol_date;
