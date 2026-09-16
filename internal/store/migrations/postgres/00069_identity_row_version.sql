-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- identity acquires the optimistic-concurrency token, for the reason 00066 gave
-- link: it is growing a CORRECTION PATH (WP-J8), and two operators fixing the
-- same credential's realm at the same time must not silently overwrite each
-- other. link had likewise never carried one; DEFAULT 1 plus a migration is how
-- link answered the drift objection and is how this one does.
--
-- THIS SUPERSEDES docs/ownership-report-design.md §4 FOR A NEW CIRCUMSTANCE, and
-- does not retract it. That section refused a token for BULK REASSIGNMENT, where
-- `UPDATE ... WHERE team_id IS NULL` is itself the atomic eligibility check and a
-- version would add nothing -- which remains true, and the guards in
-- bulk_ownership.go and team_reassignment.go are UNCHANGED by this migration.
-- The condition §4 set was "do not add a token you are not going to maintain
-- everywhere", and nobody was offering to at the time. WP-J8 pays that condition:
-- there are exactly three statements that write identity in this codebase and all
-- three now maintain the column. A fourth must too, or the token stops being one.
-- SIGNED OFF by Gabriel, 2026-09-15.
--
-- NO created_at AND NO updated_at, and that is the decision rather than the
-- omission. 00066 is the precedent and it is exact: link also grew a correction
-- path and got row_version ONLY. A backfill would have to invent a creation date
-- for every existing row, and a fabricated date in a CMDB is worse than none.
-- change_log already answers both questions precisely and permanently -- the
-- create entry is when it was declared, the newest entry is when it last changed,
-- and both carry who. And this table's meaningful timestamp is last_rotated,
-- which is a FACT ABOUT THE CREDENTIAL; an updated_at beside it would be a
-- second, weaker date that invites the wrong reading at 03:00.
--
-- last_rotated gets a DATE SHAPE CHECK, exactly as 00011 gave eol_date. `length`
-- and `substr` are the two string functions both engines agree on; a real parse
-- happens in Go, where domain.ParseDate rejects 2027-02-31 and this cannot. Named,
-- per the 2026-07-29 rule: an unnamed inline constraint is one of the exactly
-- three shapes SQLite cannot alter later.
--
-- THE CHECK VALIDATES EXISTING ROWS AND THAT IS SAFE HERE BY CONSTRUCTION: no
-- code path has ever written last_rotated, so it is NULL in every deployment. A
-- hand-edited database with a malformed value fails the migration loudly, which
-- is the correct outcome.
--
-- Both statements are ordinary DDL here. The SQLite half is the one that had to
-- be measured, and it turned out to accept exactly the same three statements --
-- so the two files differ in their comments and nowhere else.
--
-- idx_identity_team COMES BACK, shaped for the query that now exists. 00016
-- dropped it and named this moment: "When the team page grows an identities
-- section it can come back, shaped for whatever that query turns out to be
-- rather than guessed at now." The list page (WP-J8) filters on team and
-- excludes retired rows, so the index carries lifecycle as idx_asset_team does.

-- +goose Up
ALTER TABLE identity ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;

ALTER TABLE identity ADD CONSTRAINT identity_last_rotated_check
  CHECK (last_rotated IS NULL OR (length(last_rotated) = 10
         AND substr(last_rotated, 5, 1) = '-' AND substr(last_rotated, 8, 1) = '-'));

CREATE INDEX idx_identity_team ON identity(team_id, lifecycle) WHERE team_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_identity_team;
ALTER TABLE identity DROP CONSTRAINT identity_last_rotated_check;
ALTER TABLE identity DROP COLUMN row_version;
