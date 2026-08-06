-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A fourth outcome for an import: partial.
--
-- Added when the write stopped being one transaction. Batching removes a
-- half-minute global write lock and admits, in exchange, one outcome that could
-- not happen before: the file validated, some batches committed, and something
-- moved underneath the rest. Rare -- it needs a concurrent writer to take a name
-- in the seconds between checking and writing -- but real, and it is the one
-- state where the estate is half-changed.
--
-- It gets its own status rather than being folded into `refused` because the
-- action it asks for is different. `refused` means fix your file. `failed` means
-- tell us. `partial` means run it again: import creates and never updates, so
-- the rows that already landed are skipped.

-- +goose Up
ALTER TABLE import_job DROP CONSTRAINT import_job_status_check;
ALTER TABLE import_job ADD CONSTRAINT import_job_status_check
  CHECK (status IN ('queued','running','succeeded','refused','failed','partial'));

-- +goose Down
ALTER TABLE import_job DROP CONSTRAINT import_job_status_check;
ALTER TABLE import_job ADD CONSTRAINT import_job_status_check
  CHECK (status IN ('queued','running','succeeded','refused','failed'));
