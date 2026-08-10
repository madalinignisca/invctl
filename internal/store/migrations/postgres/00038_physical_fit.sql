-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Will it fit, will the rack hold it, will it stay cool.
--
-- The vertical half already works: CheckPlacement refuses a box whose top
-- passes a recorded height, and refuses an overlap, per face. What it cannot
-- answer is the question people actually argue about standing in front of the
-- rack -- a 2U server fits the units and is 780mm long, and the cabinet is
-- 600mm deep. Depth today is a BOOLEAN about which faces a box occupies in the
-- elevation drawing, which is a different question wearing similar words, and a
-- rack has no depth at all.
--
-- SIX NULLABLE COLUMNS AND NOTHING ELSE. No table is rebuilt, no existing row
-- changes, and every check added on top of this reports "not recorded" until
-- somebody records something. That is the point: an estate that has not
-- measured its cabinets is the ordinary starting state, not an error.
--
-- WEIGHT IS IN GRAMS, for the reason money is in minor units. A switch is
-- 8.5 kg and twenty of them are 170 kg, so rounding to whole kilograms loses
-- ten kilograms across a rack -- and a REAL column would put a float across the
-- SQLite/Postgres boundary for a quantity that is never divided. Integers,
-- converted for display in Go.
--
-- MILLIMETRES, not centimetres and not inches. Every datasheet that states a
-- chassis depth states it in millimetres.

-- +goose Up

-- ---------------------------------------------------------------------------
-- The rack.
-- ---------------------------------------------------------------------------

-- USABLE depth: from the face of the mounting rails to whatever is behind the
-- equipment, usually the rear door. NOT the external dimension on the cabinet's
-- datasheet, and the distinction is the whole value of the column. An 800mm
-- cabinet does not offer 800mm to equipment -- the rails sit inboard, the door
-- takes space, and where they sit is an installation choice rather than a
-- property of the model. So this is MEASURED, and left NULL when nobody has.
--
-- Deriving it from an external figure would be enforcing a guess, which
-- domain/rack.go already refuses to do for height: "Refusing U30 in a rack of
-- unknown height, on the grounds that racks are usually 42, would be enforcing
-- a guess." Same rule, same reason.
ALTER TABLE asset ADD COLUMN usable_depth_mm INTEGER;
ALTER TABLE asset ADD CONSTRAINT asset_usable_depth_check
  CHECK (usable_depth_mm IS NULL OR usable_depth_mm > 0);

-- External width, and here derivation IS legitimate -- deliberately the
-- opposite verdict to the column above, for a reason worth stating.
--
-- 19" is a STANDARD: EIA-310 fixes the faceplate at 482.6mm. So the side
-- clearance in a cabinet follows from its width by arithmetic on a constant,
-- not by assuming where somebody mounted something. A 600mm cabinet leaves
-- roughly 55mm a side and an 800mm one roughly 155mm, which is why network
-- cabinets are wide: in the narrow case the vertical cable channel and a
-- side-breathing device's intake are competing for the same 55mm.
--
-- The test for whether a derivation is honest is whether a standard fixes the
-- missing term. For width it does. For usable depth nothing does.
ALTER TABLE asset ADD COLUMN width_mm INTEGER;
ALTER TABLE asset ADD CONSTRAINT asset_width_check
  CHECK (width_mm IS NULL OR width_mm > 0);

-- What the rack is rated to carry, static. NULL means nobody looked it up,
-- which is the common case and reports as a gap rather than as unlimited.
ALTER TABLE asset ADD COLUMN max_load_grams INTEGER;
ALTER TABLE asset ADD CONSTRAINT asset_max_load_check
  CHECK (max_load_grams IS NULL OR max_load_grams > 0);

-- ---------------------------------------------------------------------------
-- The catalogued model.
-- ---------------------------------------------------------------------------

-- Chassis depth as the manufacturer states it. The clearance a box actually
-- needs is larger -- power cords and a bend radius live behind it -- and that
-- allowance is applied in Go rather than baked in here, so the finding can say
-- what it added instead of hiding it in a stored number.
ALTER TABLE device_type ADD COLUMN depth_mm INTEGER;
ALTER TABLE device_type ADD CONSTRAINT device_type_depth_check
  CHECK (depth_mm IS NULL OR depth_mm > 0);

ALTER TABLE device_type ADD COLUMN weight_grams INTEGER;
ALTER TABLE device_type ADD CONSTRAINT device_type_weight_check
  CHECK (weight_grams IS NULL OR weight_grams > 0);

-- Which way the air goes.
--
-- NULL IS NOT 'front_to_rear', and defaulting it would be the mistake that
-- makes this whole feature worthless. Front-to-rear is overwhelmingly the
-- common case, so defaulting to it would let every uncatalogued box pass the
-- opposing-neighbours check silently -- an estate that has declared nothing
-- would report perfect airflow. NULL means nobody said, and it reports as a
-- gap.
--
-- 'passive' is a real answer and distinct from NULL: a patch panel, a blanking
-- plate and a cable manager move no air, and saying so is a declaration.
ALTER TABLE device_type ADD COLUMN airflow TEXT;
ALTER TABLE device_type ADD CONSTRAINT device_type_airflow_check
  CHECK (airflow IS NULL OR airflow IN
    ('front_to_rear','rear_to_front','side_to_rear','side_to_side','passive'));

-- Partial, like the EOL indexes and for the same reason: the columns are the
-- exception rather than the rule, and the reports scan for rows that have one.
CREATE INDEX idx_asset_usable_depth ON asset(usable_depth_mm) WHERE usable_depth_mm IS NOT NULL;
CREATE INDEX idx_device_type_depth  ON device_type(depth_mm)  WHERE depth_mm IS NOT NULL;
CREATE INDEX idx_device_type_airflow ON device_type(airflow)  WHERE airflow IS NOT NULL;

-- +goose Down
--
-- THE CONSTRAINT COMES OFF BEFORE THE COLUMN. SQLite refuses to drop a column a
-- CHECK still references, and the first draft of this migration did exactly
-- that -- the Up applied cleanly and the Down failed with "no such column:
-- airflow", which the reversibility test caught rather than a deploy.
--
-- Measured against the pinned driver rather than assumed, the same way 00011
-- measured ADD CONSTRAINT: on modernc.org/sqlite both DROP CONSTRAINT and the
-- subsequent DROP COLUMN succeed. Postgres has supported both since long
-- before any version this targets.
DROP INDEX idx_device_type_airflow;
DROP INDEX idx_device_type_depth;
DROP INDEX idx_asset_usable_depth;

ALTER TABLE device_type DROP CONSTRAINT device_type_airflow_check;
ALTER TABLE device_type DROP COLUMN airflow;
ALTER TABLE device_type DROP CONSTRAINT device_type_weight_check;
ALTER TABLE device_type DROP COLUMN weight_grams;
ALTER TABLE device_type DROP CONSTRAINT device_type_depth_check;
ALTER TABLE device_type DROP COLUMN depth_mm;

ALTER TABLE asset DROP CONSTRAINT asset_max_load_check;
ALTER TABLE asset DROP COLUMN max_load_grams;
ALTER TABLE asset DROP CONSTRAINT asset_width_check;
ALTER TABLE asset DROP COLUMN width_mm;
ALTER TABLE asset DROP CONSTRAINT asset_usable_depth_check;
ALTER TABLE asset DROP COLUMN usable_depth_mm;
