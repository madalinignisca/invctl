-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- First-hop redundancy: the address that survives losing the router.
--
-- VRRP, HSRP and CARP all do the same thing -- several routers share one
-- virtual address, one of them answers for it, and if that one dies another
-- takes over. Every host on the segment points at the VIP and never learns
-- which box is actually forwarding.
--
-- WHY THIS IS NOT A PARITY ITEM. The rest of the addressing group records
-- facts; this one records REDUNDANCY, and the impact engine already reports
-- redundancy lost. A VIP with two members is survivable and a VIP with one is a
-- single point of failure wearing the costume of a redundant one -- which is
-- exactly the finding somebody wants at 03:00, and it is invisible while the
-- members are just two unrelated interfaces that happen to share a subnet.
--
-- THE VIP IS AN ip_address ROW, not a text column here. Three things follow for
-- free and all of them matter: it lands inside its prefix by the ordinary range
-- scan, the utilisation figure counts it, and the allocator will not offer it
-- to somebody else -- which a VIP recorded as text on this table would not have
-- achieved, and handing out an in-use gateway address is a memorable outage.
-- ip_address.interface_id was already nullable, so an address belonging to a
-- group rather than a port needs no new nullability, only somewhere to point.

-- +goose Up
CREATE TABLE fhrp_group (
  id          TEXT PRIMARY KEY NOT NULL,
  -- The protocol decides what the group id means and how many members are
  -- normal, so it is a constrained vocabulary rather than free text.
  protocol    TEXT NOT NULL
                CONSTRAINT fhrp_group_protocol_check
                CHECK (protocol IN ('vrrp2','vrrp3','hsrp','glbp','carp')),
  -- VRRP calls it a VRID and HSRP a group number; both are 0-255, and both are
  -- unique only on the segment they run on, never globally.
  group_number INTEGER NOT NULL
                CONSTRAINT fhrp_group_number_check
                CHECK (group_number >= 0 AND group_number <= 255),
  name        TEXT NOT NULL,
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT fhrp_group_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX fhrp_group_name_key ON fhrp_group(name) WHERE lifecycle <> 'retired';

-- Which routers are in the group, and how keen each is to be the one answering.
--
-- A SET TABLE owned by the group, replaced wholesale and folded into the
-- group's audited value -- the fifth of these in this schema, and the rule is
-- the same every time: a membership change that produces no diff on the parent
-- is a change nobody can find afterwards.
CREATE TABLE fhrp_member (
  group_id     TEXT NOT NULL REFERENCES fhrp_group(id),
  interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  -- Higher wins. Nullable because plenty of estates never set one and let the
  -- protocol's default decide, and recording 100 for those would be inventing
  -- a fact.
  priority     INTEGER
                 CONSTRAINT fhrp_member_priority_check
                 CHECK (priority IS NULL OR (priority >= 0 AND priority <= 255)),
  PRIMARY KEY (group_id, interface_id)
);
CREATE INDEX idx_fhrp_member_interface ON fhrp_member(interface_id);

-- The virtual address belongs to the group, not to a port.
ALTER TABLE ip_address ADD COLUMN fhrp_group_id TEXT REFERENCES fhrp_group(id);
CREATE INDEX idx_ip_address_fhrp ON ip_address(fhrp_group_id);

-- +goose Down
DROP INDEX idx_ip_address_fhrp;
ALTER TABLE ip_address DROP COLUMN fhrp_group_id;
DROP INDEX idx_fhrp_member_interface;
DROP TABLE fhrp_member;
DROP INDEX fhrp_group_name_key;
DROP TABLE fhrp_group;
