-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- VLANs as a thing, rather than a number written on a prefix.
--
-- `prefix.vlan_id` is an unconstrained integer today. It can say "this network
-- is on 30" and nothing else: not what 30 is called, not that the v4 and v6
-- prefixes sharing it are one broadcast domain, and not which ports are in it.
-- A number is a label; a VLAN is a place things can reach each other.
--
-- A VLAN ID IS ONLY UNIQUE SOMEWHERE. 4094 of them exist and every site reuses
-- them -- VLAN 10 in Oslo and VLAN 10 in Frankfurt are different L2 domains
-- that have never met. So a VLAN belongs to a GROUP, and the group says where
-- the numbering applies.
--
-- THE SCOPE IS AN ASSET, WHICH IS THE WHOLE TRICK. NetBox needs a polymorphic
-- scope here -- site, site group, location, rack, cluster, each its own foreign
-- key and a type column to say which -- because those are five different tables
-- over there. In this schema a site IS an asset, a rack IS an asset and a
-- cluster IS an asset, so one nullable reference covers every case with no type
-- column, no polymorphism, and no query that has to branch on which kind of
-- parent it found. NULL means the numbering is estate-wide.
--
-- INTERFACES ARE WHERE THE EDGE LIVES. A VLAN with prefixes and no ports is
-- still just a record: it says an address range exists and nothing about what
-- can talk. interface_vlan is the adjacency -- two access ports in VLAN 30 are
-- in one broadcast domain whether or not anybody drew a cable between them, and
-- that is a fact no cable trace can produce.

-- +goose Up
CREATE TABLE vlan_group (
  id             TEXT PRIMARY KEY NOT NULL,
  name           TEXT NOT NULL,
  -- Where the numbering applies. A site, a rack, a cluster -- all assets here.
  -- NULL is the estate-wide pool.
  scope_asset_id TEXT REFERENCES asset(id),
  description    TEXT,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CONSTRAINT vlan_group_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  row_version    INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX vlan_group_name_key ON vlan_group(name) WHERE lifecycle <> 'retired';
CREATE INDEX idx_vlan_group_scope ON vlan_group(scope_asset_id);

CREATE TABLE vlan (
  id             TEXT PRIMARY KEY NOT NULL,
  -- The tag on the wire. 0 and 4095 are reserved by 802.1Q and 1 is the default
  -- VLAN, which is a real VLAN people really use, so it is allowed.
  vid            INTEGER NOT NULL
                   CONSTRAINT vlan_vid_check CHECK (vid >= 1 AND vid <= 4094),
  name           TEXT NOT NULL,
  group_id       TEXT REFERENCES vlan_group(id),
  role           TEXT,
  environment_id TEXT REFERENCES environment(id),
  description    TEXT,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CONSTRAINT vlan_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  row_version    INTEGER NOT NULL DEFAULT 1
);

-- A VID is unique within its group, and the ungrouped pool is a group too.
-- Two partial indexes for the reason 00029 and 00030 both give: NULLs are
-- distinct on both engines, so a single composite over (group_id, vid) would
-- constrain nothing at all for the estate-wide pool -- which is where every
-- VLAN starts when nobody has declared a group yet.
CREATE UNIQUE INDEX vlan_group_vid_key  ON vlan(group_id, vid) WHERE group_id IS NOT NULL AND lifecycle <> 'retired';
CREATE UNIQUE INDEX vlan_global_vid_key ON vlan(vid)           WHERE group_id IS NULL     AND lifecycle <> 'retired';
CREATE INDEX idx_vlan_group ON vlan(group_id);
CREATE INDEX idx_vlan_vid   ON vlan(vid);

-- Which ports are in which VLAN, and how the frames are presented.
--
-- A SET TABLE, replaced wholesale with its interface, like asset_environment:
-- the membership belongs to the port and has no life of its own, so it is
-- folded into the interface's audited value rather than carrying an id and a
-- lifecycle. The CLAUDE.md rule applies -- the parent's change_log entry must
-- record the change, or a port moving from VLAN 10 to VLAN 20 produces no diff
-- at all.
CREATE TABLE interface_vlan (
  interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  vlan_id      TEXT NOT NULL REFERENCES vlan(id),
  -- untagged: the access VLAN, at most one per port. tagged: a trunk member.
  mode         TEXT NOT NULL
                 CONSTRAINT interface_vlan_mode_check CHECK (mode IN ('tagged','untagged')),
  PRIMARY KEY (interface_id, vlan_id)
);
CREATE INDEX idx_interface_vlan_vlan ON interface_vlan(vlan_id);

-- A port has at most ONE untagged VLAN. Two would be a frame with no
-- unambiguous home, which is a configuration no switch accepts and therefore
-- one this inventory must not be able to describe.
CREATE UNIQUE INDEX interface_untagged_key ON interface_vlan(interface_id) WHERE mode = 'untagged';

-- The prefix's link to a real VLAN, beside the raw integer rather than
-- replacing it yet. EXPAND NOW, CONTRACT LATER: the backfill that turns each
-- distinct vlan_id into a vlan row has to generate UUIDv7 ids and RFC3339
-- timestamps, and this codebase generates both in Go and never in SQL. So the
-- migration widens the schema, application startup fills it, and a later
-- migration drops the integer once every deployment has run the backfill.
-- Dropping it here would destroy the data the backfill reads.
ALTER TABLE prefix ADD COLUMN vlan_ref_id TEXT REFERENCES vlan(id);
CREATE INDEX idx_prefix_vlan_ref ON prefix(vlan_ref_id);

-- +goose Down
DROP INDEX idx_prefix_vlan_ref;
ALTER TABLE prefix DROP COLUMN vlan_ref_id;
DROP INDEX interface_untagged_key;
DROP INDEX idx_interface_vlan_vlan;
DROP TABLE interface_vlan;
DROP INDEX idx_vlan_vid;
DROP INDEX idx_vlan_group;
DROP INDEX vlan_global_vid_key;
DROP INDEX vlan_group_vid_key;
DROP TABLE vlan;
DROP INDEX idx_vlan_group_scope;
DROP INDEX vlan_group_name_key;
DROP TABLE vlan_group;
