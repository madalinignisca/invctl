-- Dialect-split despite being byte-identical in both directories, and that is
-- not an oversight. The seven lookup tables are created by the DIALECT
-- migration 00004, and Migrate applies every shared migration before any
-- dialect one -- so a shared migration touching these tables runs before they
-- exist. Measured, not assumed: as shared/00010 this failed with "no such
-- table: asset_kind". Placement follows the dependency, not the SQL.
--
-- +goose Up
-- Network reachability (docs/reachability-design.md, M2). asset_closure answers
-- "what is inside this"; these tables answer "what is behind this". Two graphs,
-- deliberately separate.
--
-- Portability, verified on modernc.org/sqlite and standard on PG:
--   * No ALTER TABLE: every enum keeps a real CHECK and there is no backfill.
--   * Partial UNIQUE indexes are safe on both engines.
--   * REAL, BOOLEAN with TRUE/FALSE, ON DELETE CASCADE: all already used here.
--
-- This is migration 00007 rather than 00006 in docs/reachability-design.md's
-- draft numbering: 00006 was taken by link.lifecycle before this milestone
-- started.

-- A set of network assets that jointly forward traffic. One row per lone device
-- too (availability='standalone', one member) so the graph has one vertex type
-- and hosts are never vertices -- an end host cannot become a transit node.
--
-- availability / min_healthy / failover_mode are deliberately the same columns,
-- the same values and the same evaluator as `service`: an active/passive
-- firewall pair and an active/passive database pair fail identically, and one
-- tested function for both is one set of bugs rather than two.
--
-- 'quorum' and 'sharded' are excluded from the CHECK on purpose. quorum on a
-- two-member pair requires two survivors and would report DOWN when one is
-- lost -- the exact inverse of MC-LAG -- and sharded is meaningless for a
-- forwarder. Reuse the evaluator, restrict the vocabulary.
CREATE TABLE net_group (
  id             TEXT PRIMARY KEY,
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  -- Descriptive only; shown in the UI, not read by the algorithm. The failure
  -- semantics live in availability/failover_mode, which is what the engine needs.
  kind           TEXT NOT NULL CHECK (kind IN
                   ('standalone','ha_pair','mclag','stack','vrrp','cluster')),
  -- Load-bearing: derivation orients an undirected cable between two forwarders
  -- by rank (edge > core > distribution > access). Direction is the one thing a
  -- cable genuinely cannot tell you, so it is declared, never guessed.
  role           TEXT NOT NULL CHECK (role IN ('edge','core','distribution','access')),
  availability   TEXT NOT NULL CHECK (availability IN
                   ('standalone','active_active','active_passive')),
  min_healthy    INTEGER,
  failover_mode  TEXT CHECK (failover_mode IN ('auto','manual','none')),
  environment_id TEXT REFERENCES environment(id),
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  -- HANDOVER 3.5: declared is authoritative and never silently overwritten.
  source         TEXT NOT NULL DEFAULT 'declared'
                   CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence     REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  attrs          TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);

CREATE TABLE net_group_member (
  group_id   TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  asset_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  -- Same vocabulary as service_instance.role, because the same function reads it.
  role       TEXT NOT NULL DEFAULT 'member'
               CHECK (role IN ('primary','standby','member')),
  lifecycle  TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','retired')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (group_id, asset_id)
);
-- A device in two forwarding groups is a modelling error, not a topology, and
-- it would make the vertex ambiguous. Partial so a retired membership can be
-- superseded rather than blocking re-entry. This index also covers every
-- lookup by asset_id on its own, so a separate plain index on asset_id would
-- be redundant.
CREATE UNIQUE INDEX idx_net_group_member_one
  ON net_group_member(asset_id) WHERE lifecycle = 'active';

-- Directed, unlike `link`. Several rows from one group are ALTERNATE paths
-- (best-of), never both-required. "Both required" is one uplink to a group
-- whose availability says active_active with min_healthy = 2.
--
-- A partial UNIQUE index -- exactly the technique used nine lines above for
-- net_group_member -- is what makes "no second active edge to the same
-- upstream group on the same plane, but a retired one can be re-created"
-- possible: WHERE lifecycle = 'active' means only the live edge is
-- constrained, so retiring one frees the (group, upstream, plane) triple back
-- up. An earlier version of this comment claimed no UNIQUE constraint was
-- possible here for that reason -- backwards; a partial index is the fix, not
-- the obstacle. Go additionally enforces this before the insert
-- (CreateNetUplink), so a duplicate gets a readable error; the index is the
-- backstop that holds under concurrent callers, the same role
-- idx_net_group_member_one plays above and CreateLink's writeSerializable
-- check plays for `link`.
CREATE TABLE net_uplink (
  id                TEXT PRIMARY KEY,
  group_id          TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  upstream_group_id TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  plane             TEXT NOT NULL DEFAULT 'data'
                      CHECK (plane IN ('data','mgmt','storage')),
  lifecycle         TEXT NOT NULL DEFAULT 'active'
                      CHECK (lifecycle IN ('active','retired')),
  source            TEXT NOT NULL DEFAULT 'declared'
                      CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence        REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK (group_id <> upstream_group_id)
);
CREATE INDEX idx_net_uplink_group ON net_uplink(group_id);
CREATE INDEX idx_net_uplink_up    ON net_uplink(upstream_group_id);
CREATE UNIQUE INDEX idx_net_uplink_one
  ON net_uplink(group_id, upstream_group_id, plane) WHERE lifecycle = 'active';

-- A host's attachment to a forwarding group, on one plane.
--
-- `plane` is what stops a management-switch failure reporting services down.
-- It sits here rather than replacing interface.is_mgmt because a retrofit of
-- is_mgmt is a backfill whose failure mode is manufacturing outages, and
-- because this is where the engine actually reads it. Derivation sets it from
-- interface.is_mgmt on the HOST side of the cable.
--
-- asset_id is restricted in Go to non-forwarder kinds (server, hypervisor,
-- cluster, vm, k8s_node, storage). A rack or a site is not network-attached,
-- and allowing one would let a single row fake full coverage for a subtree.
CREATE TABLE net_attachment (
  id         TEXT PRIMARY KEY,
  asset_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  group_id   TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  plane      TEXT NOT NULL DEFAULT 'data'
               CHECK (plane IN ('data','mgmt','storage')),
  lifecycle  TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','retired')),
  source     TEXT NOT NULL DEFAULT 'declared'
               CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX idx_net_attach_asset ON net_attachment(asset_id, plane);
CREATE INDEX idx_net_attach_group ON net_attachment(group_id);
-- Same partial-index technique, same reason: one active attachment per
-- (asset, group, plane), and a retired one frees the triple back up.
CREATE UNIQUE INDEX idx_net_attach_one
  ON net_attachment(asset_id, group_id, plane) WHERE lifecycle = 'active';

-- Which chassis of the group this attachment actually lands on.
--
-- EMPTY set = attached to the group as a whole; survives while the group does.
-- NON-EMPTY = survives only while at least one listed chassis survives.
--
-- A child table rather than a nullable column because a dual-homed host has TWO
-- chassis and a nullable column plus a UNIQUE key cannot express that -- the
-- derivation would silently emit a single-homed row for a genuinely dual-homed
-- host, which is a wrong answer in the pessimistic direction.
--
-- Two cables to the SAME chassis collapse to one row (asset_id is in the PK),
-- which is exactly why a LAG to a single switch is correctly not redundancy.
--
-- Membership in the group is validated in Go (CreateNetAttachment checks each
-- member is an active net_group_member of the target group before inserting),
-- not by a composite FK: the FK would need group_id denormalised here, and it
-- could not check lifecycle anyway, so it would buy a stale constraint at the
-- cost of a duplicated column.
--
-- No lifecycle column here on purpose: retiring the asset a member row names
-- must retire the parent net_attachment, not silently drop the pin. Deleting
-- or retiring only the pin would turn a single-homed host's *last* surviving
-- chassis into an empty member set -- which means "attached to the group as a
-- whole" -- the exact inverse of what happened. RetireAsset enforces this by
-- retiring the whole net_attachment row instead of touching this table.
CREATE TABLE net_attachment_member (
  attachment_id TEXT NOT NULL REFERENCES net_attachment(id) ON DELETE CASCADE,
  asset_id      TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  -- Provenance, and the hook for a future per-port simulation.
  interface_id  TEXT REFERENCES interface(id) ON DELETE SET NULL,
  PRIMARY KEY (attachment_id, asset_id)
);
CREATE INDEX idx_net_attach_member_asset ON net_attachment_member(asset_id);

-- Where a reachability scope enters the estate.
--
-- scope reuses endpoint.exposure's vocabulary exactly, minus 'internal' (which
-- needs no path), so an endpoint's declared exposure names the anchor it must
-- reach. No parallel taxonomy.
--
-- It hangs off a GROUP, not an interface: an active/passive pair is ONE point
-- of presence, and pinning to a port would need one duplicate row per chassis
-- whose omission silently changes every external verdict in the estate.
--
-- environment_id IS read: an 'environment'-scoped endpoint requires an anchor
-- whose environment_id is NULL or equals the service's environment, so a dev
-- host is not credited with reaching a prod anchor.
--
-- Carries the same provenance columns as every other net_* table
-- (source/confidence/first_seen/last_seen/verified_by/verified_at). A
-- misplaced anchor is the single highest-leverage wrong row in this model --
-- one row silently changes every external-reachability verdict in the estate
-- -- so knowing whether it was declared, derived or discovered, and whether a
-- person has verified it, matters at least as much here as anywhere else.
CREATE TABLE net_anchor (
  id             TEXT PRIMARY KEY,
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  scope          TEXT NOT NULL CHECK (scope IN ('environment','cross_env','external')),
  group_id       TEXT NOT NULL REFERENCES net_group(id) ON DELETE CASCADE,
  environment_id TEXT REFERENCES environment(id),
  plane          TEXT NOT NULL DEFAULT 'data'
                   CHECK (plane IN ('data','mgmt','storage')),
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CHECK (lifecycle IN ('active','retired')),
  source         TEXT NOT NULL DEFAULT 'declared'
                   CHECK (source IN ('declared','derived_link','discovered_lldp')),
  confidence     REAL,
  first_seen TEXT, last_seen TEXT, verified_by TEXT, verified_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX idx_net_anchor_group ON net_anchor(group_id);

-- +goose Down
DROP TABLE net_anchor;
DROP TABLE net_attachment_member;
DROP TABLE net_attachment;
DROP TABLE net_uplink;
DROP TABLE net_group_member;
DROP TABLE net_group;
