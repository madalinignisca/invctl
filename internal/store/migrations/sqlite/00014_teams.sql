-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Who looks after this, and in what capacity.
--
-- Migration 00009's own header names the gap this closes: "`owner_team` is free
-- text on four tables with no referential integrity at all". Free text is how
-- `platform`, `Platform` and `platform-team` become three teams, and how a
-- report that groups by owner produces a list nobody can act on.
--
-- TWO AXES, DELIBERATELY SEPARATE. `team_id` says WHO looks after a thing;
-- `manager_role` says IN WHAT CAPACITY. A team without a role is "the platform
-- team's box, nobody has said who in it"; a role without a team is not
-- expressible, and that is correct -- a role is a capacity within a team and
-- means nothing floating free. Both are OPTIONAL, because a small estate where
-- everyone looks after everything gains nothing from filling them in, and a
-- field people are forced to complete is a field full of noise.
--
-- WHY TEAM IS A TABLE AND ROLE IS A LOOKUP. A team is an organisational fact
-- with a life: it forms, it owns things, it is disbanded, and its rows must
-- survive that as `retired` so the change_log keeps resolving. Every lookup in
-- this schema is a closed vocabulary whose rows are never retired, so a team is
-- not one. A role is the opposite -- descriptive, nothing branches on its value,
-- and an estate that wants `dpo` or `vendor-manager` should be able to add one
-- as data. That is exactly the line migration 00004 drew.
--
-- ROLE IS DOCUMENTATION, NOT AUTHORIZATION. "Who can manage it" here means who
-- to call, not who the application will let through. Nothing in this codebase
-- reads `manager_role` to decide anything, and `authz.CanWrite` does not consult
-- it. That separation is the whole remit -- this system presents state -- and
-- the day LDAP group roles land, this column is where they will look, which is
-- an argument for recording it accurately now and none at all for enforcing it
-- yet.
--
-- NO PERSONAL DATA, WHICH IS WHY THIS IS TEAMS AND NOT PEOPLE. `contact_ref`
-- holds a GROUP address, a ticket queue or a channel -- never an individual.
-- The application cannot tell the difference and does not try: the rule lives in
-- the field hint, the help text and docs/AUDIT.md, exactly as `identity.
-- secret_ref` is documented to hold a path and never a secret. The point is that
-- a CMDB kept forever should carry nothing that anybody could ever ask to have
-- erased.
--
-- BEFORE YOU RUN THIS ON ANYTHING REAL, check what it will discard:
--
--   SELECT 'asset' AS t, owner_team, COUNT(*) FROM asset    WHERE owner_team IS NOT NULL GROUP BY owner_team
--   UNION ALL SELECT 'service',      owner_team, COUNT(*) FROM service  WHERE owner_team IS NOT NULL GROUP BY owner_team
--   UNION ALL SELECT 'project',      owner_team, COUNT(*) FROM project  WHERE owner_team IS NOT NULL GROUP BY owner_team
--   UNION ALL SELECT 'identity',     owner_team, COUNT(*) FROM identity WHERE owner_team IS NOT NULL GROUP BY owner_team;
--
-- If that returns rows you care about, create the teams first and set `team_id`
-- before applying this. The migration cannot check for you -- neither engine can
-- abort portably on a row count -- so this is a runbook step and it is written
-- here rather than somewhere nobody would look. A review asked for the
-- trip-wire and was right to: "we believe there is no real data" and "we
-- checked" are different statements.
--
-- THE FOUR `owner_team` COLUMNS GO, AND ARE NOT BACKFILLED. That is a real
-- choice and not an oversight. A backfill has to invent an id per distinct
-- string, and this repository generates every id as a UUIDv7 in Go and never in
-- SQL -- a rule worth more than the contents of a fixture. Building the
-- two-phase runner that would let Go do it between two migrations is machinery
-- this POC does not need: the only estates in existence are the test fixture and
-- a demo database that is rebuilt on every deploy, and the seed creates the
-- teams properly. A deployment with real data would need that second phase, and
-- would be past the point where DECISIONS.md calls a destructive migration free.

-- +goose Up
CREATE TABLE team (
  id          TEXT NOT NULL PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT,
  -- A group address, a queue or a channel. NEVER a person; see the header.
  contact_ref TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  CONSTRAINT team_code_check      CHECK (code <> ''),
  CONSTRAINT team_name_check      CHECK (name <> ''),
  CONSTRAINT team_lifecycle_check CHECK (lifecycle IN ('planned','active','deprecated','retired'))
);

CREATE TABLE responsibility_role (
  code        TEXT NOT NULL PRIMARY KEY,
  label       TEXT NOT NULL,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  description TEXT
);
INSERT INTO responsibility_role (code, label, sort_order, description) VALUES
  ('owner',      'Owner',           10, 'Accountable for it existing at all: its budget, its lifecycle, whether it is replaced or retired. The person to convince, not the person to page.'),
  ('operator',   'Operator',        20, 'Runs it day to day -- patching, restarts, capacity, backups. The team that touches it most and the first to notice when it misbehaves.'),
  ('approver',   'Approver',        30, 'Signs off changes before they happen. Distinct from owner because the budget holder and the change gate are often different desks.'),
  ('oncall',     'On call',         40, 'First response out of hours. Recorded here so an incident does not begin by working out whose problem this is.'),
  ('custodian',  'Data custodian',  50, 'Answerable for the data on it rather than the box: retention, classification, an erasure request. Usually a different team from the one that runs it.'),
  ('vendor',     'Vendor managed',  60, 'Operated by a third party under contract. The internal team named alongside is who holds the contract, not who does the work.');

ALTER TABLE asset ADD COLUMN team_id TEXT REFERENCES team(id);
ALTER TABLE asset ADD COLUMN manager_role TEXT REFERENCES responsibility_role(code);
ALTER TABLE service ADD COLUMN team_id TEXT REFERENCES team(id);
ALTER TABLE service ADD COLUMN manager_role TEXT REFERENCES responsibility_role(code);

-- project and identity get the team but no role: a role says who manages a
-- thing, and both of these are managed through what they contain. A project's
-- assets carry their own roles; an identity is looked after by whoever runs the
-- service that uses it.
ALTER TABLE project ADD COLUMN team_id TEXT REFERENCES team(id);
ALTER TABLE identity ADD COLUMN team_id TEXT REFERENCES team(id);

CREATE INDEX idx_asset_team    ON asset(team_id)    WHERE team_id IS NOT NULL;
CREATE INDEX idx_service_team  ON service(team_id)  WHERE team_id IS NOT NULL;
CREATE INDEX idx_project_team  ON project(team_id)  WHERE team_id IS NOT NULL;
CREATE INDEX idx_identity_team ON identity(team_id) WHERE team_id IS NOT NULL;

-- The free text goes. No index covers any of these, so no DROP INDEX is needed
-- first -- unlike migration 00010, where one made the DROP COLUMN fail outright.
ALTER TABLE asset    DROP COLUMN owner_team;
ALTER TABLE service  DROP COLUMN owner_team;
ALTER TABLE project  DROP COLUMN owner_team;
ALTER TABLE identity DROP COLUMN owner_team;

-- +goose Down
ALTER TABLE identity ADD COLUMN owner_team TEXT;
ALTER TABLE project  ADD COLUMN owner_team TEXT;
ALTER TABLE service  ADD COLUMN owner_team TEXT;
ALTER TABLE asset    ADD COLUMN owner_team TEXT;

DROP INDEX idx_identity_team;
DROP INDEX idx_project_team;
DROP INDEX idx_service_team;
DROP INDEX idx_asset_team;

ALTER TABLE identity DROP COLUMN team_id;
ALTER TABLE project  DROP COLUMN team_id;
ALTER TABLE service  DROP COLUMN manager_role;
ALTER TABLE service  DROP COLUMN team_id;
ALTER TABLE asset    DROP COLUMN manager_role;
ALTER TABLE asset    DROP COLUMN team_id;

DROP TABLE responsibility_role;
DROP TABLE team;
