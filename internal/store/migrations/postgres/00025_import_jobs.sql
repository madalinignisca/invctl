-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- An import that runs after the request that started it.
--
-- WHY. Measured: about 1.4ms per row inside one transaction, so five thousand
-- assets take seven and a half seconds and a full 1 MiB file would take most of
-- a minute -- past every proxy timeout between a browser and this process. The
-- upload was already bounded by a body limit that disagreed with how long the
-- work actually takes.
--
-- PROGRESS IS ROWS PROCESSED, NOT ROWS IMPORTED, and the distinction is the
-- whole reason this table is shaped the way it is. The import remains ONE
-- transaction -- whole file or nothing -- so a job that is 43% of the way
-- through has written nothing anybody can see, and will have written nothing at
-- all if row five thousand is bad. Reporting "43% imported" would be reporting
-- a number that can still become zero. `rows_done` counts what has been
-- examined and staged; `created` is only ever set once the commit succeeds.
--
-- THE ACTOR IS CAPTURED AT SUBMIT TIME and carried into the job. It is not
-- re-derived when the work runs, and it is never the system actor: the audit
-- trail has to name the person who uploaded the file, not the process that
-- happened to write the rows.
--
-- No file is kept. The parsed rows live in memory for the life of the job --
-- a few megabytes at the body limit -- because a temp file would need cleaning
-- up, would litter on a crash, and would buy nothing: the transaction rolls
-- back if this process dies, so there is nothing to resume.

-- +goose Up
CREATE TABLE import_job (
  id          TEXT NOT NULL PRIMARY KEY,
  -- What kind of file this was: assets, device_types. A TEXT with a CHECK and a
  -- matching Go constant set, like every other enum here.
  kind        TEXT NOT NULL
                CONSTRAINT import_job_kind_check
                CHECK (kind IN ('assets','device_types')),
  filename    TEXT NOT NULL,
  -- The app_user id, opaque, exactly as change_log.actor is -- so this table
  -- carries no personal data either and can be kept as long as the rest.
  actor       TEXT NOT NULL,
  actor_kind  TEXT NOT NULL,
  status      TEXT NOT NULL
                CONSTRAINT import_job_status_check
                CHECK (status IN ('queued','running','succeeded','refused','failed')),
  rows_total  INTEGER NOT NULL DEFAULT 0,
  -- Rows EXAMINED, not rows you have. See the header.
  rows_done   INTEGER NOT NULL DEFAULT 0,
  -- Set only after a successful commit.
  created     INTEGER NOT NULL DEFAULT 0,
  -- Why it was refused or how it broke. Opaque prose for a person; nothing
  -- parses it.
  message     TEXT,
  -- The problem list as JSON, rendered and never queried -- the same rule
  -- attrs columns hold to.
  problems    TEXT,
  created_at  TEXT NOT NULL,
  started_at  TEXT,
  finished_at TEXT,
  CONSTRAINT import_job_filename_check CHECK (filename <> ''),
  CONSTRAINT import_job_rows_check     CHECK (rows_done >= 0 AND rows_total >= 0)
);

CREATE INDEX idx_import_job_created ON import_job(created_at);
CREATE INDEX idx_import_job_status  ON import_job(status);

-- +goose Down
DROP INDEX idx_import_job_status;
DROP INDEX idx_import_job_created;
DROP TABLE import_job;
