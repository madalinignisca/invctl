-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Overlays: one L2 domain stretched across boxes that share no cable.
--
-- A VLAN is a broadcast domain on a switch fabric. An L2VPN is a broadcast
-- domain carried ACROSS one -- VXLAN over an IP underlay, VPLS over MPLS, EVPN
-- over either. The distinction matters here for one reason: everything else in
-- this system answers "what can reach what" from cables, containment and VLAN
-- membership, and an overlay is precisely the case where two ports are in one
-- L2 domain with none of those three connecting them. Without this table that
-- adjacency is invisible, and the trace stops at the underlay saying the two
-- sites are unrelated.
--
-- A TERMINATION IS A VLAN OR A PORT, never both, and that is a CHECK rather
-- than two tables. VXLAN maps a VNI to a VLAN on each leaf; VPLS attaches a
-- physical port. Both are "this local thing is a member of that overlay", and
-- splitting them would mean every reader asking twice and merging.
--
-- THE IDENTIFIER IS NULLABLE AND NOT UNIQUE ACROSS TYPES. A VNI is unique
-- within a fabric and a VPLS VC-ID within a provider's network; there is no
-- estate-wide namespace for either, and inventing one would refuse a perfectly
-- ordinary pair of overlays from two suppliers.

-- +goose Up
CREATE TABLE l2vpn (
  id          TEXT PRIMARY KEY NOT NULL,
  name        TEXT NOT NULL,
  -- What carries it. The list is fixed by the technologies rather than by this
  -- estate, so it is a CHECK and not a lookup table.
  kind        TEXT NOT NULL
                CONSTRAINT l2vpn_kind_check
                CHECK (kind IN ('vxlan','vpls','evpn','mpls','l2tp','other')),
  -- The VNI, VC-ID or equivalent. Nullable: plenty of overlays are named and
  -- managed without anybody here recording the number.
  identifier  BIGINT
                CONSTRAINT l2vpn_identifier_check
                CHECK (identifier IS NULL OR (identifier >= 0 AND identifier <= 16777215)),
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT l2vpn_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX l2vpn_name_key ON l2vpn(name) WHERE lifecycle <> 'retired';

-- What is attached to the overlay at each site.
CREATE TABLE l2vpn_termination (
  id           TEXT PRIMARY KEY NOT NULL,
  l2vpn_id     TEXT NOT NULL REFERENCES l2vpn(id),
  vlan_id      TEXT REFERENCES vlan(id),
  interface_id TEXT REFERENCES interface(id) ON DELETE CASCADE,
  lifecycle    TEXT NOT NULL DEFAULT 'active'
                 CONSTRAINT l2vpn_termination_lifecycle_check
                 CHECK (lifecycle IN ('active','retired')),
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  row_version  INTEGER NOT NULL DEFAULT 1,
  -- EXACTLY ONE END. A termination naming both a VLAN and a port says the
  -- overlay attaches in two places at once, and a termination naming neither
  -- attaches nowhere -- both are rows that look like a connection and are not.
  CONSTRAINT l2vpn_termination_one_end_check
    CHECK ((vlan_id IS NOT NULL AND interface_id IS NULL)
        OR (vlan_id IS NULL AND interface_id IS NOT NULL))
);
CREATE INDEX idx_l2vpn_termination_vpn ON l2vpn_termination(l2vpn_id);
CREATE INDEX idx_l2vpn_termination_vlan ON l2vpn_termination(vlan_id);
CREATE INDEX idx_l2vpn_termination_interface ON l2vpn_termination(interface_id);

-- One VLAN, or one port, terminates into a given overlay once. A second row
-- says the same thing twice and would double every count derived from it.
CREATE UNIQUE INDEX l2vpn_termination_vlan_key ON l2vpn_termination(l2vpn_id, vlan_id)
  WHERE vlan_id IS NOT NULL AND lifecycle <> 'retired';
CREATE UNIQUE INDEX l2vpn_termination_interface_key ON l2vpn_termination(l2vpn_id, interface_id)
  WHERE interface_id IS NOT NULL AND lifecycle <> 'retired';

-- +goose Down
DROP INDEX l2vpn_termination_interface_key;
DROP INDEX l2vpn_termination_vlan_key;
DROP INDEX idx_l2vpn_termination_interface;
DROP INDEX idx_l2vpn_termination_vlan;
DROP INDEX idx_l2vpn_termination_vpn;
DROP TABLE l2vpn_termination;
DROP INDEX l2vpn_name_key;
DROP TABLE l2vpn;
