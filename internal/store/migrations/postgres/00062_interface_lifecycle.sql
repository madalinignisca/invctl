-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A port can be removed. Until now it could only ever be added.
--
-- `interface` was the most-referenced table in this schema with no lifecycle
-- column at all -- 15 foreign-key declarations point at it and 58 queries read
-- it, and a port that was physically pulled out of a chassis stayed on the
-- asset for ever. UpdateInterface could rename it; nothing could withdraw it.
-- Found by the write-surface census (internal/store/write_surface_test.go),
-- which keys on creation precisely so an entity with no repair method at all
-- cannot pass in silence the way this one did.
--
-- SOFT DELETE, as CLAUDE.md requires of every entity: "Never delete an asset,
-- service, dependency, cable or topology row." A port is a topology row, and
-- the rule was always meant to cover it; there was simply no column to set.
--
-- NOT THE SAME THING AS `enabled`, AND THE DISTINCTION MATTERS OPERATIONALLY.
-- `enabled = FALSE` is a port that exists and is administratively shut -- it is
-- in the chassis, it can be brought up, and an operator asking "what is down"
-- wants to see it. `lifecycle = retired` is a port that is NOT THERE ANY MORE.
-- Conflating them would mean either that shutting a port erases it from the
-- inventory, or that pulling a NIC leaves a port somebody can try to enable.
--
-- THE UNIQUE CONSTRAINT ON (asset_id, name) IS DELIBERATELY LEFT ALONE. It is a
-- table constraint rather than an index, so making it partial would mean
-- rebuilding a table fifteen foreign keys point at, on both engines, to buy
-- something Go can do inside the transaction -- which is exactly what
-- power_feed and power_source already do (requireUniquePowerName), and they
-- carry no DB unique constraint at all for the same reason.
--
-- What the constraint means in practice is that a retired port keeps its name,
-- so re-adding `eth0` after a NIC swap collides. That is handled in
-- CreateInterface, which REACTIVATES the retired row instead: the port comes
-- back as itself, keeping its id, its history and its position, which is more
-- truthful than a second row claiming to be the same physical port. See its
-- doc comment.

-- +goose Up
ALTER TABLE interface ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT interface_lifecycle_check CHECK (lifecycle IN ('active','retired'));
CREATE INDEX idx_interface_lifecycle ON interface(lifecycle);

-- +goose Down
DROP INDEX idx_interface_lifecycle;
ALTER TABLE interface DROP COLUMN lifecycle;
