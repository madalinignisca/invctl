-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- 'bridge' as an asset kind, added the way 00004 argued a kind should be added:
-- as DATA. No table is rebuilt, no CHECK is widened, nothing is recompiled.
-- asset.kind became a FOREIGN KEY into asset_kind in 00004 precisely so this
-- file could be four lines, and 00004's own header names this exact case --
-- "a bridge that can carry nothing, which is exactly the case this work exists
-- to serve".
--
-- WHY IT SHIPS IN A MIGRATION AND NOT IN THE SEED. asset_kind is classified
-- DECLARED (internal/domain/classification.go), so a writer that is not a
-- migration owes it a change_log row. Migrations are the one exception, and the
-- comment there says why: "values arrive in the migration, and no migration in
-- this repository writes change_log". Seeding a vocabulary row from
-- internal/seed would either break that rule or silently exempt a declared
-- table, and a demo fixture is not a reason to do either. It also means an
-- operator running against an empty production database gets the kind in the
-- asset form without having to load the demo estate.
--
-- THE TWO FLAGS ARE THE WHOLE DECISION, not decoration.
--
--   can_host_instances FALSE -- a bridge forwards frames; it runs nothing. A
--   service_instance placed on one is a data-entry mistake, and
--   store.CreateInstance refuses it on this column.
--
--   is_attachable TRUE -- a bridge is a network element and can be the subject
--   of a net_attachment. It gets no attachment row in the demo fixture: every
--   bridge sits inside a hypervisor that already has one, and inheritance by
--   nearest ancestor is the whole point of resolving attachments through
--   asset_closure. TRUE is what makes declaring one POSSIBLE for an estate
--   where a bridge really does land somewhere its host does not.
--
-- sort_order 115 puts it between k8s_node (110) and storage (120): the ordering
-- is containment, and a bridge lives inside a hypervisor alongside the guests.
--
-- This file is byte-identical to its postgres twin and deliberately still
-- duplicated: asset_kind is CREATEd in the dialect sets (00004 in each), so a
-- shared migration referencing it would run before the table exists -- Migrate
-- applies all of shared, then all of the dialect set.

-- +goose Up
INSERT INTO asset_kind (code, label, sort_order, can_host_instances, is_attachable)
VALUES ('bridge', 'Bridge', 115, FALSE, TRUE);

-- +goose Down
-- Safe because asset.kind references it: if any asset is still a bridge, the
-- foreign key refuses the DELETE rather than orphaning the row. That is the
-- correct failure -- a down migration that silently invalidated live rows is
-- the thing verifyForeignKeys exists to catch after the fact.
DELETE FROM asset_kind WHERE code = 'bridge';
