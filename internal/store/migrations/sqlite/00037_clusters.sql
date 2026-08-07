-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Clusters: hosts that can carry each other's guests.
--
-- WHAT THE ENGINE DOES TODAY, AND WHY IT IS WRONG FOR HALF THE ESTATE. A VM is
-- a child of its hypervisor, so DownInstances walks asset_closure and takes
-- every guest with the host. That is exactly right for a standalone box and
-- exactly wrong for a three-node Proxmox cluster, where losing one host means
-- the guests restart on the other two and are serving again in minutes. The
-- engine has been answering "everything on hv-01 is gone" for an estate where
-- the truthful answer is "they moved".
--
-- THIS IS THE FIRST THING HERE THAT CHANGES A PROPAGATION rather than adding a
-- report beside one. Every domain shipped in D and E reports; a cluster makes
-- the engine conclude something different about the same outage, which is worth
-- saying out loud because it is the higher-risk kind of change.
--
-- THE PLACEMENT STAYS ON THE HOST. A VM's parent remains the hypervisor it is
-- actually running on -- that is a fact, and the rack, the power chain and the
-- containment view all need it. The cluster says that placement is MOBILE, not
-- that it is unknown. Reparenting guests to a cluster would lose which box to
-- walk to, which is the one thing somebody at 03:00 has.
--
-- min_hosts IS CAPACITY, NOT QUORUM. A three-node cluster whose guests need two
-- nodes' worth of memory does not survive losing two, and "how many are left"
-- is the only question the engine can answer honestly without a memory model.
-- Nullable: an estate that has not worked it out gets relocation on any
-- survivor, which is optimistic and is at least clearly stated.

-- +goose Up
CREATE TABLE cluster (
  id          TEXT PRIMARY KEY NOT NULL,
  name        TEXT NOT NULL,
  -- What runs it. Fixed by the products rather than by this estate.
  kind        TEXT NOT NULL
                CONSTRAINT cluster_kind_check
                CHECK (kind IN ('proxmox','vmware','hyperv','xen','nutanix','other')),
  -- none: guests stay down with their host, which is what the engine did for
  -- every cluster before this table existed.
  -- restart: guests come back on a surviving member, after a restart.
  ha_policy   TEXT NOT NULL DEFAULT 'none'
                CONSTRAINT cluster_ha_policy_check CHECK (ha_policy IN ('none','restart')),
  min_hosts   INTEGER
                CONSTRAINT cluster_min_hosts_check
                CHECK (min_hosts IS NULL OR min_hosts >= 1),
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT cluster_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX cluster_name_key ON cluster(name) WHERE lifecycle <> 'retired';

-- Which hosts are in it.
--
-- A SET TABLE owned by the cluster, replaced wholesale and folded into the
-- cluster's audited value -- the sixth in this schema, and the rule has not
-- changed: a host leaving a cluster that produces no diff on the parent is a
-- change nobody can find afterwards, and this one moves guests.
--
-- A host belongs to at most one cluster. Two would make "where do its guests
-- go" ambiguous, and the engine would have to pick.
CREATE TABLE cluster_member (
  cluster_id TEXT NOT NULL REFERENCES cluster(id),
  asset_id   TEXT NOT NULL REFERENCES asset(id),
  PRIMARY KEY (cluster_id, asset_id)
);
CREATE UNIQUE INDEX cluster_member_asset_key ON cluster_member(asset_id);
CREATE INDEX idx_cluster_member_cluster ON cluster_member(cluster_id);

-- +goose Down
DROP INDEX idx_cluster_member_cluster;
DROP INDEX cluster_member_asset_key;
DROP TABLE cluster_member;
DROP INDEX cluster_name_key;
DROP TABLE cluster;
