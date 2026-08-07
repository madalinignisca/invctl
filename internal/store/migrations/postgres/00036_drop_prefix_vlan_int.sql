-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- The contract half of 00031: the loose VLAN integer goes.
--
-- The reasoning is in the sqlite file of the same number and is not repeated.
-- The only difference is mechanical: PostgreSQL drops a column, where SQLite
-- has to copy the table.
--
-- THIS RELEASE REQUIRES THE PREVIOUS ONE TO HAVE BEEN RUN -- the backfill that
-- turns each integer into a VLAN is Go, and a deployment skipping it loses its
-- VLAN assignments here.

-- +goose Up
ALTER TABLE prefix DROP COLUMN vlan_id;

-- +goose Down
ALTER TABLE prefix ADD COLUMN vlan_id INTEGER;
