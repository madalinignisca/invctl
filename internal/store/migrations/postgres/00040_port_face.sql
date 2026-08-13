-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Where the ports are (WP-C3).
--
-- ONE COLUMN, AND EVERYTHING ELSE IS DERIVED. The cabling questions -- do the
-- leads come out the wrong side, is this box carrying more cable than the
-- cabinet's channel can take, is that patch lead long enough to reach -- are
-- all answerable from rows this database already holds: `link` knows the
-- cables, `interface` knows the ports, `asset.rack_face` knows which way the
-- box is mounted and `asset.width_mm` knows the cabinet. The only fact nobody
-- has written down is which face the ports are on.
--
-- FRONT, REAR OR BOTH. Side-ported rack equipment exists and is rare enough
-- that a fourth value would be a column of noise; a box with ports on both
-- faces -- a patch panel, most chassis switches -- is the case that genuinely
-- needs saying, because it is never wrong-facing.
--
-- NULL IS NOT 'front'. The same rule airflow follows in 00038: front is the
-- common answer, so defaulting to it would let every uncatalogued box pass the
-- wrong-face check in silence, and an estate that had declared nothing would
-- report perfect cabling.

-- +goose Up
ALTER TABLE device_type ADD COLUMN port_face TEXT;
ALTER TABLE device_type ADD CONSTRAINT device_type_port_face_check
  CHECK (port_face IS NULL OR port_face IN ('front','rear','both'));

CREATE INDEX idx_device_type_port_face ON device_type(port_face) WHERE port_face IS NOT NULL;

-- +goose Down
-- The constraint comes off before the column: SQLite refuses to drop a column
-- a CHECK still references. Measured against the pinned driver in 00038.
DROP INDEX idx_device_type_port_face;
ALTER TABLE device_type DROP CONSTRAINT device_type_port_face_check;
ALTER TABLE device_type DROP COLUMN port_face;
