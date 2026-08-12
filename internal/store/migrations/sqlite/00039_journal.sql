-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- What a person knows that the schema has nowhere to put.
--
-- THE GAP THIS FILLS. Every fact in this database is a column somebody designed
-- a place for, and the things an operator most needs six months later are not
-- among them: why this box is still on an old firmware, which vendor case
-- covers the flapping port, that the replacement is ordered and due in March.
-- Without somewhere to write those, they live in a ticket system nobody reads
-- from here, or in somebody's head.
--
-- IT IS NOT THE AUDIT TRAIL AND MUST NOT BE MISTAKEN FOR IT. change_log records
-- what the software did, is written by the code in the same transaction as the
-- change, and is append-only forever. This is what a person chose to say. Both
-- appear on one timeline, and every row on it says which it is -- a note
-- claiming to be an audit entry would be the laundering rule 7 exists to stop,
-- in the other direction.
--
-- POLYMORPHIC, LIKE change_log, and for the same reason: notes belong wherever
-- somebody is standing when they need to write one. A circuit that keeps
-- dropping, a cluster with a quirk, a project with a decision behind it -- all
-- of those want a note, and a table per entity would be six tables and six
-- panels agreeing about everything except which one you are looking at. There
-- is deliberately NO foreign key: change_log has none either, because the trail
-- has to outlive what it describes.
--
-- THE AUTHOR IS AN OPAQUE app_user.id, exactly like change_log.actor. A CMDB
-- kept forever must carry nothing anybody could ask to have erased, so the
-- column holds an id the UI joins to a display name; scrubbing an app_user row
-- answers an erasure request while the notes keep their integrity and simply
-- stop resolving to a person. The BODY is free text written by a human and is
-- the one place personal data could arrive -- which is a matter for the
-- deployment's own policy, and is said out loud in the field hint rather than
-- pretended away.

-- +goose Up

CREATE TABLE journal_entry (
  id          TEXT NOT NULL PRIMARY KEY,
  entity_type TEXT NOT NULL
                CONSTRAINT journal_entry_entity_type_check CHECK (entity_type <> ''),
  entity_id   TEXT NOT NULL
                CONSTRAINT journal_entry_entity_id_check CHECK (entity_id <> ''),
  -- What kind of note. Four, chosen because they answer different questions and
  -- a reader scanning a timeline wants to tell them apart at a glance:
  --   note        context somebody wanted recorded
  --   incident    what happened, while it was happening
  --   maintenance planned work, before or after
  --   decision    a choice and its reason -- the one that rots worst untold
  kind        TEXT NOT NULL DEFAULT 'note'
                CONSTRAINT journal_entry_kind_check
                CHECK (kind IN ('note','incident','maintenance','decision')),
  body        TEXT NOT NULL
                CONSTRAINT journal_entry_body_check CHECK (body <> ''),
  -- app_user.id, never a name. See the header.
  author      TEXT NOT NULL
                CONSTRAINT journal_entry_author_check CHECK (author <> ''),
  -- Soft delete, like every other entity here. A note somebody withdrew is
  -- still a thing that was said, and the change_log entry for the withdrawal
  -- refers to a row that has to still exist.
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT journal_entry_lifecycle_check
                CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);

-- The panel's own query: this entity's notes, newest first.
CREATE INDEX idx_journal_entity ON journal_entry(entity_type, entity_id, created_at);
-- The timeline folds journal rows in by time across an entity and its
-- neighbours, and a global "recent notes" view wants the same ordering.
CREATE INDEX idx_journal_created ON journal_entry(created_at);

-- +goose Down
DROP INDEX idx_journal_created;
DROP INDEX idx_journal_entity;
DROP TABLE journal_entry;
