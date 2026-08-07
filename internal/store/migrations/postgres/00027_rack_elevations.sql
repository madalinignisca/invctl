-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Where a box physically sits.
--
-- THREE COLUMNS, and each is nullable for the same reason: an estate that has
-- recorded its racks and not yet where anything is in them is the ordinary
-- starting state, and a model that refuses it is a model nobody fills in.
-- Position stays optional; the elevation draws what is placed and lists the
-- rest as "in this rack, position not recorded", the same shape the expiry
-- report uses for undated assets.
--
-- U_HEIGHT HERE IS THE RACK'S OWN CAPACITY, not a mounted box's height. A
-- mounted box takes its height from its catalogued model, which already carries
-- one -- that column exists precisely for this. A rack is a thing you have one
-- of rather than a model you buy forty of, so cataloguing rack models would be
-- machinery for a number that is 42 in almost every rack ever built.
--
-- NUMBERED FROM THE FLOOR, and not configurable. U1 at the bottom is what every
-- rack rail is stamped with. A direction flag would be one more field on every
-- rack, gettable wrong, for the benefit of the few estates that label top-down
-- -- and those can be served later by a display setting rather than by making
-- the stored number ambiguous.
--
-- FACE MATTERS BECAUSE DEPTH DOES. A full-depth box occupies both faces at its
-- position; a half-depth one occupies only the face it is mounted on, which is
-- how back-to-back patch panels and PDUs actually live in a network rack. The
-- device type carries full_depth already. Overlap is checked in Go against that,
-- not by a constraint: it depends on a height that lives on another table and on
-- a rule about depth, neither of which a CHECK can reach.

-- +goose Up
ALTER TABLE asset ADD COLUMN u_height INTEGER;
ALTER TABLE asset ADD COLUMN rack_position INTEGER;
ALTER TABLE asset ADD COLUMN rack_face TEXT;

ALTER TABLE asset ADD CONSTRAINT asset_u_height_check
  CHECK (u_height IS NULL OR (u_height > 0 AND u_height <= 60));
ALTER TABLE asset ADD CONSTRAINT asset_rack_position_check
  CHECK (rack_position IS NULL OR (rack_position > 0 AND rack_position <= 60));
ALTER TABLE asset ADD CONSTRAINT asset_rack_face_check
  CHECK (rack_face IS NULL OR rack_face IN ('front','rear'));

CREATE INDEX idx_asset_rack_position ON asset(parent_id, rack_position);

-- +goose Down
DROP INDEX idx_asset_rack_position;
ALTER TABLE asset DROP CONSTRAINT asset_rack_face_check;
ALTER TABLE asset DROP CONSTRAINT asset_rack_position_check;
ALTER TABLE asset DROP CONSTRAINT asset_u_height_check;
ALTER TABLE asset DROP COLUMN rack_face;
ALTER TABLE asset DROP COLUMN rack_position;
ALTER TABLE asset DROP COLUMN u_height;
