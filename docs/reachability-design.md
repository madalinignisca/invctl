# Reachability design (working reference for M2-M6)

> **M3 gates application behind `Request.ApplyReachability`, default off.** The
> algorithm below computes and reports in full, but the three seams only move a
> status when that flag is set. M4 is flipping the default. The flag exists so
> the "report-only" milestone is a real review checkpoint rather than a
> description of intent — `TestReachabilityIsGatedAndTheGateIsReal` asserts both
> directions against a topology that genuinely isolates.
>
> **`asset_health` in this document is superseded.** It shows a simple
> `asset_id PRIMARY KEY` shape; `docs/AUDIT.md` is authoritative and requires
> `(entity_type, entity_id, reporter)` plus `state_since` and a companion
> `observed_transition` table. Two monitors watching one asset must not overwrite
> each other, and "down since when" must survive a heartbeat. M6 builds the
> AUDIT.md version; M2 deliberately created no health table at all rather than
> ship one known to be wrong.

Produced by a design review that developed four independent approaches, critiqued
each adversarially against 14 network scenarios, and synthesised the result. This is
the working reference for milestones M2 through M6; it is not a finished document and
will be folded into DECISIONS.md as each milestone lands.

## Recommendation

**Forwarder groups + two-pass union-find, composed at three disjoint seams.**

A second graph whose **vertices are declared forwarder groups** (`net_group`: a lone switch is a group of one; an MC-LAG pair or an active/passive firewall pair is a group of two), whose **edges are directed `net_uplink` rows** (several from one group = alternate paths, OR semantics), and to which **hosts attach** via `net_attachment` plus a `net_attachment_member` **child table** naming which chassis the cable actually lands on.

Group health is decided by `AvailabilityPolicy.Evaluate` — the arms of `Service.EvaluateCapacity` extracted verbatim. Reachability between two assets is decided by **two union-find passes** over the group graph (optimistic = include degraded-forwarding groups, pessimistic = exclude them), giving three-valued pairwise reach with no traversal, no memo table, no cycle special-case and no map-iteration nondeterminism.

It enters the engine at **three disjoint seams**:

1. **Isolation → instance liveness** — `alive := !down && !(needsNetwork && isolated)` — so `EvaluateCapacity` runs unchanged on the surviving-AND-reachable set.
2. **Pairwise partition → per-dependency provider downgrade** inside `providerHealth` and `routeStatuses`, computed once as a pre-pass, so `Dependency.Propagate`'s hard/soft/startup/async/optional table does all the semantic work.
3. **Anchor loss → exposure findings**, reported and never propagated.

`link` is demoted from authority to **evidence**: a propose-only `POST /network/derive` turns cables into proposed attachments and uplinks, with `interface.is_mgmt` on the host side of the cable setting `plane='mgmt'`.

Default posture: **an asset with no attachment anywhere in its containment ancestry is `unmodelled`; unmodelled is never isolated, never partitioned, never counted against anything.** With zero rows in the new tables `Analyse` receives `Inputs.Net == nil` and every result is byte-identical to today.

## Data model

One migration, `internal/store/migrations/shared/00006_reachability.sql`. Seven tables, no `ALTER TABLE` at all (so no backfill risk, and every enum keeps a real CHECK). `?` placeholders everywhere at runtime, IDs UUIDv7 TEXT from Go, timestamps RFC3339 UTC TEXT from Go, enums TEXT+CHECK mirrored by constant sets in `internal/domain/reach.go`.

```sql
-- +goose Up
-- Network reachability. asset_closure answers "what is inside this";
-- these tables answer "what is behind this". Two graphs, deliberately separate.
--
-- Portability, verified on modernc.org/sqlite 1.54 and standard on PG:
--   * No ALTER TABLE: every enum keeps a real CHECK and there is no backfill.
--   * Partial UNIQUE indexes are safe on both engines.
--   * REAL, BOOLEAN with TRUE/FALSE, ON DELETE CASCADE: all already used here.

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
CREATE INDEX idx_net_group_role ON net_group(role);

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
CREATE INDEX idx_net_group_member_asset ON net_group_member(asset_id);
-- A device in two forwarding groups is a modelling error, not a topology, and
-- it would make the vertex ambiguous. Partial so a retired membership can be
-- superseded rather than blocking re-entry.
CREATE UNIQUE INDEX idx_net_group_member_one
  ON net_group_member(asset_id) WHERE lifecycle = 'active';

-- Directed, unlike `link`. Several rows from one group are ALTERNATE paths
-- (best-of), never both-required. "Both required" is one uplink to a group
-- whose availability says active_active with min_healthy = 2.
--
-- No UNIQUE on (group_id, upstream_group_id): soft delete plus a unique pair
-- means a retired edge could never be re-created. The "no second active edge"
-- rule lives in Go, exactly as CreateLink enforces one cable per port
-- (internal/store/network.go:117-127).
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
-- Membership in the group is validated in Go, not by a composite FK: the FK
-- would need group_id denormalised here, and it could not check lifecycle
-- anyway, so it would buy a stale constraint at the cost of a duplicated column.
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
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX idx_net_anchor_group ON net_anchor(group_id);
CREATE INDEX idx_net_anchor_scope ON net_anchor(scope);

-- Observed state, physically apart from intent (HANDOVER 3.5). A monitoring
-- webhook writes only here and never touches asset.lifecycle. Nothing reads it
-- unless Request.UseObservedHealth is set, so a stale row can never silently
-- change a simulation. Its own table, not columns on `asset`, so observed churn
-- stays out of asset's change_log diffs and out of SELECT * FROM asset.
CREATE TABLE asset_health (
  asset_id TEXT PRIMARY KEY REFERENCES asset(id) ON DELETE CASCADE,
  state    TEXT NOT NULL CHECK (state IN ('up','degraded','down','unknown')),
  at       TEXT NOT NULL,
  source   TEXT NOT NULL,
  message  TEXT
);
CREATE INDEX idx_asset_health_state ON asset_health(state);

-- +goose Down
DROP TABLE asset_health;
DROP TABLE net_anchor;
DROP TABLE net_attachment_member;
DROP TABLE net_attachment;
DROP TABLE net_uplink;
DROP TABLE net_group_member;
DROP TABLE net_group;
```

**Deliberately NOT added, with reasons:**

- **No column on `link`, no `is_uplink`, no `role`, no `link.lifecycle`.** Cabling stays a physical record. Adding direction there would put the same fact in two places and make the cable table lie the moment a port is re-patched.
- **No `interface.plane`, no `is_mgmt` retrofit.** See the `net_attachment.plane` comment.
- **No reachability closure table.** A set of (ancestor, descendant) pairs cannot express "reachable if ANY of several alternative paths survives", which is the entire HA case. It would be right when there is no redundancy and silently wrong exactly when the operator's exceptions apply.
- **No fourth `domain.Status`.** `unreachable` would break the total order that makes the fixed point provably terminate (`engine.go:11-15`) and ripple through `rank()`, `Worse()`, `Propagate`, every CHECK and every template — to encode something that changes no decision the engine makes. Expressed as `ServiceImpact.Cause` instead.
- **No conflict-queue table.** HANDOVER 3.5 is satisfied by propose-only derivation plus `source`/`confidence`/`last_seen`/`verified_*` on every row — the same set `dependency` already carries.

**Go side, `internal/domain/reach.go`** (zero external deps): `NetGroupKinds`, `NetGroupRoles`, `NetGroupAvailabilities = {standalone, active_active, active_passive}`, `Planes`, `AnchorScopes`, `HealthStates`, `ObservedInstanceStates = {running, stopped, failed, unknown}`, plus entities and validating constructors. `NewNetGroup` rejects `active_active` without `min_healthy` and `active_passive` without `failover_mode`, mirroring `Service.Validate`.

**One edit to existing domain code**, `internal/domain/service.go` — a pure refactor whose correctness proof is `service_test.go` passing unmodified:

```go
type AvailabilityPolicy struct {
    Availability string
    MinHealthy   *int
    FailoverMode *string
}
func (p AvailabilityPolicy) Evaluate(members []InstanceHealth) Status { /* the existing body of EvaluateCapacity, moved */ }
func (s *Service) EvaluateCapacity(instances []InstanceHealth) Status {
    return AvailabilityPolicy{s.Availability, s.MinHealthy, s.FailoverMode}.Evaluate(instances)
}

// Better is the mirror of the existing Worse. Alternative network paths combine
// with OR -- two paths, either working, the asset is reachable -- so the fold
// runs in the opposite direction from propagation's merge. Identity for a
// Better-fold is StatusDown; identity for a Worse-fold is StatusOK. Stating
// both is deliberate: getting the initialiser backwards is how a fold silently
// returns the absorbing element for every input.
func (s Status) Better(other Status) Status {
    if other.rank() < s.rank() { return other }
    return s
}
```

## Algorithm

New file `internal/impact/reach.go`. **Phase 0 is linear and runs once, before `phaseCapacity`.** No iteration, no recursion, no traversal — so it provably cannot affect the fixed point's termination argument.

## Loading — 6 queries added to `LoadGraph`, all `?`-placeholder, all index-served

```sql
SELECT * FROM net_group WHERE lifecycle <> ?                     -- 'retired'
SELECT m.*, a.lifecycle AS asset_lifecycle
  FROM net_group_member m JOIN asset a ON a.id = m.asset_id
  WHERE m.lifecycle = ?                                          -- 'active'
SELECT * FROM net_uplink   WHERE lifecycle = ?
SELECT * FROM net_anchor   WHERE lifecycle = ?
SELECT * FROM net_attachment WHERE lifecycle = ?
SELECT am.*, na.group_id, a.lifecycle AS asset_lifecycle
  FROM net_attachment_member am
  JOIN net_attachment na ON na.id = am.attachment_id AND na.lifecycle = ?
  JOIN asset a ON a.id = am.asset_id
-- Attachment inheritance, scoped to attached subtrees only. This is the
-- closure query CLAUDE.md demands instead of a parent_id walk, and it is
-- bounded by (attached hosts x their descendants) rather than the whole estate.
SELECT c.ancestor_id, c.descendant_id, c.depth
  FROM asset_closure c
  JOIN net_attachment na ON na.asset_id = c.ancestor_id AND na.lifecycle = ?
```

`impact.Endpoint` gains `BindScope`, `Exposure`, `ServiceEnvID` from the `endpoint`/`service` rows `LoadGraph` already reads — no extra query. `rt_k8s` is loaded (one more query) for scenario 9's cluster resolution.

If every one of these returns zero rows, `LoadGraph` sets `Inputs.Net = nil` and the entire feature is inert.

## R1 — group status. O(members)

```go
func groupStatus(g *NetGroup, down map[string]bool) domain.Status {
    installed := g.InstalledMembers()   // asset lifecycle in {active,maintenance,deprecated}
                                        // AND member lifecycle = 'active'
    // EvaluateCapacity returns StatusOK for total == 0 (service.go:249-253),
    // which would make an emptied group a HEALTHY ingress. Guarded here, next
    // to the call, rather than three files away.
    if len(installed) == 0 { return domain.StatusDown }
    health := make([]domain.InstanceHealth, 0, len(installed))
    for _, m := range installed {
        health = append(health, domain.InstanceHealth{ID: m.AssetID, Role: m.Role, Alive: !down[m.AssetID]})
    }
    return domain.AvailabilityPolicy{g.Availability, g.MinHealthy, g.FailoverMode}.Evaluate(health)
}
```

`lifecycle = 'planned'` is deliberately NOT installed: cabling for a build that is not yet racked must not be reported as redundancy that physically exists. That is the most dangerous possible false negative for this tool.

## R2 — two union-find passes per plane. O((V+E)·α(V))

```go
edges := uplinks filtered to plane, lifecycle='active', both endpoints not StatusDown
sort.Slice(edges, byID)          // stable component representatives across runs
compA := unionFind(vertices with status in {OK, Degraded}, edges)   // optimistic
compB := unionFind(vertices with status == OK,             edges)   // pessimistic
```

This is a bottleneck-path computation over a three-element chain, done as two reachability passes. It is *exact*: if two groups are connected in B there is a path with no degraded hop, so OK; if connected only in A then every path has at least one degraded hop, so degraded; if not connected in A there is no path at all. No path enumeration, no priority queue, no memo.

Both planes are computed every run — `data` drives status, `mgmt` and `storage` are report-only. There is no `Request.Plane` toggle, because with one no single run could show "services keep serving but the estate is unmanageable".

## R3 — attachment resolution and isolation. O(A + assets·1)

```go
// Nearest-ancestor-wins per (asset, plane): a VM's own attachment (depth 0)
// shadows its hypervisor's (depth 1). Two O(n) passes over the closure rows.
live(a) = groupStatus[a.GroupID] != StatusDown &&
          (len(members(a)) == 0 ||
           exists m in members(a): installed(m) && !down[m])

netOf(x, plane) = { a.GroupID : a effective for x on plane, live(a) }
modelled(x, plane) = x has at least one effective attachment on that plane
isolated(x, plane) = modelled(x, plane) && netOf(x, plane) is empty
```

**`modelled` is the whole graceful-degradation guarantee.** An asset with no attachment in its ancestry is *unknown*, never *isolated*. Absence of topology data is not evidence of disconnection. Without this rule `hv-03` — which has no cable at all in the seed today — would report five services down the first time anyone touched the core switch.

## R4 — pairwise reach, three-valued plus a known flag

```go
func reach(x, y string) (domain.Status, bool /*known*/) {
    if !modelled(x) || !modelled(y) { return "", false }
    if isolated(x) || isolated(y)   { return domain.StatusDown, true }
    gx, gy := netOf(x), netOf(y)
    for p := range gx { for q := range gy { if compB[p] == compB[q] { return domain.StatusOK, true } } }
    for p := range gx { for q := range gy { if compA[p] == compA[q] { return domain.StatusDegraded, true } } }
    return domain.StatusDown, true
}
```

`Unknown` is **not** a status value and has no position in the lattice — it is a separate boolean. Unknown pairs are excluded from every fold and counted in `Coverage`. That avoids the "where does Unknown sit in min/max" problem a partially-cabled estate would otherwise hit for years.

## Seam 1 — isolation into `phaseCapacity` (three lines)

```go
needsNet[svcID] = !( len(endpoints(svc)) > 0 &&
                     all endpoints have BindScope in {loopback, unix} &&
                     all active outbound deps of svc resolve to a provider
                         endpoint with BindScope in {loopback, unix} )
// The vacuous case matters: backup-agent has NO endpoints and only outbound
// dependencies. "every endpoint is local" is vacuously true for it, so the
// len() > 0 guard is what stops a pure consumer being declared immune to
// network loss. Default is needs-network.

alive := !downInstanceIDs[inst.ID] && !(needsNet[svcID] && isolated(inst.HostAssetID, "data"))
status := svc.EvaluateCapacity(health)   // UNCHANGED
```

`lostToPlacement` and `lostToIsolation` are counted separately (placement wins when both), so `capacityReason` can read *"primary vm-db-1 is running but network-isolated (sw-core-2 down)"* rather than the misleading *"primary lost"*.

## Seam 2 — pairwise into `providerHealth` and `routeStatuses` (pre-pass, ~6 lines in the loop)

Computed **once**, before the fixed point, keyed by dependency id:

```go
// Skip entirely when the provider endpoint is loopback/unix: that traffic is
// intra-host by definition and no network device failure can touch it.
if providerEndpoint.BindScope in {loopback, unix} { reachOfDep[dep.ID] = ReachOK; continue }

C := dedupe(compB reps of hosts of ALIVE instances of consumer service)
P := dedupe(reachHosts(provider))        // see below; alive instances only
if len(C) == 0 { reachOfDep[dep.ID] = Unknown; continue }  // already down by capacity

// One reachable provider is enough for a consumer -> Better-fold, init StatusDown.
// A consumer that cannot reach ANY provider is broken -> the outer test is
// "are they all Down", not a fold. Stating both directions explicitly because
// inverting either one makes the function return Down for every input.
for c in C {
    per[c], known[c] = Down, false
    for p in P { if s, ok := reach(c, p); ok { per[c] = per[c].Better(s); known[c] = true } }
}
switch {
case no c is known:                         Unknown
case every known per[c] == StatusDown:      ReachDown
case any known per[c] != StatusOK:          ReachDegraded    // partial
default:                                    ReachOK
}
```

Deduping to component representatives **before** the comparison bounds the cost at |components|² per edge instead of |instances|² — which matters because DECISIONS.md §1 commits to one `service_instance` per k8s pod.

`reachHosts(endpoint)` is where scenario 9 lives:
- `loopback`, `unix` → intra-host, edge exempt (above).
- `host`, `vip` → hosts of alive instances.
- `cluster_ip`, `node_port`, `ingress` → if `rt_k8s.cluster_asset_id` names an asset of `kind='cluster'`, its closure descendants of kind `k8s_node` that are not isolated; otherwise the union of alive instance hosts, and `Coverage.ClusterUnresolved++`. Additionally, for `cluster_ip`, if the **consumer's** host is itself a node of the same cluster, return `ReachOK` without consulting the physical graph — pod-to-pod is a CNI tunnel, and asserting a physical partition inside a cluster is a claim this model cannot support. A stated fail-open, not an oversight.

Then inside the existing loop, one changed expression:

```go
eff := statuses[providerServiceID]                       // or routeStatus[r.ID]
switch reachOfDep[dep.ID] {
case ReachDown:     eff = domain.StatusDown
case ReachDegraded: eff = eff.Worse(domain.StatusDegraded)
}
```

`dep.Propagate(eff, req.WindowSeconds)` then does everything: `hard` unreachable → consumer down; `soft` → degraded, still serving; `startup` → no status change, lands on `WontRestart` (a service that cannot reach Vault today will not come back tomorrow — exactly right); `async` → degraded only if `tolerance_seconds < WindowSeconds`; `optional` → silent. **No new propagation rules, no change to `domain.Dependency`.**

`routeStatuses` gains a matching pre-pass: each pool member's contribution is downgraded by `reach(frontendHosts, memberHosts)` before the alive/degraded counting. That preserves the documented "routes are nodes, not passthroughs" behaviour (`00004:49-51`) for network loss as well as host loss — the one place every proposal regressed.

## Seam 3 — anchors and exposure (post-fixed-point, reported, never propagated)

```go
for ep in endpoints where BindScope not in {loopback, unix} and Exposure != "internal" {
    anchors := active anchors with scope == ep.Exposure and
               (scope != "environment" || env_id in {NULL, svc.EnvironmentID})
    if len(anchors) == 0 { Coverage.NoAnchorForScope[ep.Exposure]++; continue }  // no data -> no claim
    hosts := hosts of alive instances of ep.Service
    if len(hosts) == 0 { continue }                       // already down by capacity
    r := Better-fold over (h, anchor.GroupID) pairs, init StatusDown, known pairs only
    if unknown or r == OK { continue }
    Result.Unreachable = append(..., EndpointReach{svc, ep, ep.Exposure, r, blockingGroup})
}
// Service level: if a service has >=1 non-local endpoint and EVERY one is in
// Unreachable, append a ServiceImpact{Status: worst r, Cause: exposure}.
// It is NEVER written into `statuses` and NEVER propagates: haproxy-edge is
// genuinely still serving its internal consumers, and cascading "down" from a
// lost internet anchor would push a falsehood through the route.
```

## Signature and result

```go
type Inputs struct {
    DownInstanceIDs map[string]bool
    DownAssetIDs    map[string]bool   // ALREADY closure-expanded by the caller
    Net             *NetGraph         // nil => every reachability step is a no-op
}
func Analyse(g *Graph, req Request, in Inputs) Result

type Request struct {
    DownAssetIDs      []string
    WindowSeconds     int
    UseObservedHealth bool   // NEW, default false
    SkipNetwork       bool   // NEW, escape hatch
}

type Cause string   // "capacity" | "reachability" | "exposure" | "dependency"

// ServiceImpact gains: Cause, LostToIsolation int
// Result gains:
//   Isolated       []AssetIsolation   // asset, plane, blocking group
//   Partitions     []EdgePartition    // dependency edges cut, both sides named
//   Unreachable    []EndpointReach    // running, but no path to its anchor
//   RedundancyLost []GroupFinding     // survived, but the next failure is total
//   Coverage       ReachCoverage      // Modelled / Inherited / Unmodelled,
//                                     // GroupsWithoutUplinkOrAnchor,
//                                     // NoAnchorForScope, ClusterUnresolved
```

`SubtreeIDs` (already in `store/assets.go:432`) is computed **once** in `Simulate` and feeds both `DownInstances` and the forwarder down set — which is what makes scenario 10 consistent.

## Complexity

R1 `O(M)` members; R2 `O((V+E)·α(V))` twice per plane, V = groups (tens to low hundreds), E = uplinks; R3 `O(A)` attachments plus `O(closure rows under attached assets)`; R4 `O(|C|·|P|)` per dependency with both sides deduped to component representatives, so 1×1 in practice. All strictly linear and all *outside* the fixed point, which is unchanged. Six extra indexed queries. On the demo estate this is sub-millisecond; at a few thousand devices the attachment query dominates at a few thousand rows.

## HA semantics

HA is not special-cased anywhere in the reachability code. It is one call to `domain.AvailabilityPolicy.Evaluate` — the function extracted verbatim from `Service.EvaluateCapacity`, already covered by `internal/domain/service_test.go`.

## Active/passive firewall pair

```
net_group(code='fw-edge', kind='ha_pair', role='edge',
          availability='active_passive', failover_mode=<auto|manual|none>)
net_group_member(fw-edge, fw-edge-1, role='primary')
net_group_member(fw-edge, fw-edge-2, role='standby')
net_anchor(code='internet',        scope='external',  group=fw-edge)
net_anchor(code='partner-transit', scope='cross_env', group=fw-edge)
```

| Simulated loss | `Evaluate` path (`service.go:291-323`) | Group status | Union-find | Verdict for a path through it |
|---|---|---|---|---|
| fw-edge-1 (primary), `manual` | primary dead, standby alive, not auto → `StatusDegraded` | **degraded** | in pass A, **not** in pass B | **DEGRADED** — "reaching the internet anchor now depends on promoting fw-edge-2, and promotion is manual" |
| fw-edge-1, `auto` | primary dead, standby alive, auto → `StatusOK` | **ok** | in both passes | **OK** — a blip. `RedundancyLost` note only |
| fw-edge-1, `none` | not auto → `StatusDegraded` | degraded | A only | DEGRADED (see note below) |
| fw-edge-2 (standby) | primary alive → `StatusOK` | **ok** | both | OK. `RedundancyLost` note |
| both | surviving 0 → `StatusDown` (`service.go:263`) | **down** | excluded from both | **DOWN** |
| lone firewall, `standalone` | 1 member, lost → `StatusDown` | **down** | excluded | **DOWN** |

Scenario 1 → degraded ✓. Scenario 2 → ok ✓. These are the operator's two stated exceptions, produced by code that already exists and is already tested.

Note on `failover_mode='none'`: `Evaluate` returns degraded rather than down, which is arguably wrong for a standby that cannot be promoted at all. I would leave the shared evaluator alone rather than fork it — the existing service semantics are what operators already understand — and surface it in the reason string. If it proves wrong in practice that is a change to one shared function, not to the reachability code.

## MC-LAG / stacked switch pair

```
net_group(code='sw-core', kind='mclag', role='core',
          availability='active_active', min_healthy=1)
net_group_member(sw-core, sw-core-1, role='member')
net_group_member(sw-core, sw-core-2, role='member')
net_uplink(sw-core -> fw-edge, plane='data')
```

Losing one member: `surviving 1 >= min_healthy 1` → **ok** → in both passes → traffic survives. Losing both → `surviving 0` → **down** → every host behind it loses its group. Set `min_healthy=2` instead if the pair is capacity-critical and half the fabric should read as degraded — a data decision, not a code branch.

Scenario 3 ✓.

## The trap a group-only model misses: which chassis your cable lands on

This is where `net_attachment_member` earns its table, and it is what makes scenarios 3, 4 and 5 all correct **simultaneously** — something no single-nullable-column design can do.

| Host | Attachment members | Lose sw-core-1 | Lose sw-core-2 |
|---|---|---|---|
| hv-01, dual-homed | {sw-core-1, sw-core-2} | sw-core-2 alive → **live** | sw-core-1 alive → **live** |
| hv-03, single-homed to sw-core-2 | {sw-core-2} | **live** | no surviving member → **attachment dead → hv-03 isolated** |
| a server LAGged with two cables to sw-core-1 only | {sw-core-1} — two cables collapse to one row, `asset_id` is in the PK | **isolated** | live |

Scenario 4 ✓ (the group is OK at 1-of-2, the *host* is still cut off). Scenario 5 ✓ (two cables to one chassis is one member, so it is correctly not redundancy).

## What is deliberately not modelled, and why

- **Member-level uplink diversity.** Uplinks are group-to-group. If sw-core-1 is cabled only to fw-edge-1 and sw-core-2 only to fw-edge-2, losing fw-edge-1 and sw-core-2 together really does disconnect sw-core-1, and this model reports it optimistically. Modelling it correctly needs the full bipartite chassis adjacency — i.e. the cable plant — at the layer where operators genuinely do build full meshes. Stated as a known limit, with a post-POC lint from `link` as the mitigation. The member-level gate is applied where the data is cheap and the failure is common: **host attachments**.
- **Split-brain.** A pair that loses its interconnect but keeps both members alive is not expressible. `quorum` is excluded from the CHECK precisely because a two-member "quorum" gives the inverse answer, and there is no partition-detection logic behind it.
- **Bandwidth.** An MC-LAG at half capacity reports OK. `min_healthy=2` is the manual workaround; there is no capacity arithmetic anywhere.
- **Per-VRF failover.** A pair that is active/passive for one VRF and passive/active for another is one group with one role assignment here. Wrong for that estate, no cheap fix, and the partial unique index on `net_group_member(asset_id)` makes it explicit rather than silently ambiguous.
- **Convergence time.** STP reconvergence and routing adjacency are treated as instant. `failover_seconds` compared against `WindowSeconds` was tempting (it would reuse the async-tolerance mechanism), but attaching a number to a *manual* promotion is false precision — a human takes five minutes or five hours depending on who is awake — and most convergence in a real estate belongs to no group at all. Left out.

## Composition with the existing engine

Three channels, **disjoint by construction**. No path writes twice into `statuses` for the same cause.

**Channel 1 — isolation → `phaseCapacity`, via instance liveness.**
`alive := !downInstanceIDs[inst.ID] && !(needsNet[svc] && isolated(host))`. A single `&&`, so an instance already lost to placement is not additionally "lost to isolation" — that is what makes scenario 10 idempotent. `svc.EvaluateCapacity(health)` is then called with its existing signature on the reduced set, so `standalone`, `active_active` + `min_healthy`, `active_passive` + `failover_mode`, `quorum` (⌊n/2⌋+1) and `sharded` (every shard ≥ 1 replica) are all applied to **surviving-and-reachable** instances. That is scenario 11, and it is the thing a per-edge-only design (Proposal 1) structurally cannot do. `LostInstances` remains the count of not-alive instances; `LostToIsolation` is a new sub-count so the reason string can distinguish "the host is off" from "the host is running but cut off", which is the operator's exact phrasing.

**Channel 2 — pairwise partition → `Dependency.Propagate`, via a per-edge provider downgrade.**
Computed once in a pre-pass, applied inside `providerHealth` as `eff = statuses[provider]` downgraded by `reachOfDep[dep.ID]`. Then `dep.Propagate(eff, req.WindowSeconds)` runs completely unchanged, so the nature table does all the semantic work: `hard` unreachable → consumer **down**; `soft` → **degraded**, still serving; `startup` → no status change but **`WontRestart`** (a service that cannot reach Vault today will not come back tomorrow — exactly the right answer, and it falls out for free); `async` → degraded **only if** `tolerance_seconds < WindowSeconds`; `optional` → silent. `routeStatuses` gets the same treatment at pool-member granularity, so "the proxy is reachable but every backend is on the far side of the break" is still caught — preserving the documented route-as-node behaviour that every proposal regressed.

**No double-counting with channel 1**, because the consumer and provider host sets are built from **alive** instances only. An instance killed by isolation is already excluded from both sides, and a consumer with zero alive instances short-circuits to `Unknown` rather than being reported as both down-by-capacity and unreachable.

**No double-counting *within* channel 2**, because reachability is per **edge**, never per **service**. The same provider stays perfectly reachable to a consumer on its own side of the break. That is the entire reason scenario 6 comes out right, and it is why the verdict is never merged into `statuses` directly.

**Channel 3 — anchor loss → reported, never propagated.**
Produces `Result.Unreachable` and, when *every* non-local endpoint of a service has lost its anchor, one `ServiceImpact{Cause: exposure}` appended after the fixed point. It never enters `statuses` and never propagates. Rationale: `haproxy-edge` with a dead internet anchor is genuinely still serving `partner-gateway` from inside; marking it globally down would cascade a falsehood through the route and turn the report into an alarm storm — the failure mode that gets impact reports ignored. Services already down by capacity are skipped, so the rack-a1 case produces no duplicate entry.

**The two existing columns are used for what each actually means**, a distinction all four proposals muddled:
- **`bind_scope`** gates the *local exemption*. `loopback`/`unix` traffic is intra-host by definition, so such a dependency never receives a reachability downgrade, and a service whose endpoints are all local and whose outbound dependencies are all local is immune to isolation. `orders-api/http` is `bind_scope=host, exposure=internal` — a host port, genuinely network-affected between hosts, and correctly *not* exempt. Using `exposure` here (as Proposal 4's `environment` floor does) fails scenario 8.
- **`exposure`** gates the *anchor requirement*, 1:1 with `net_anchor.scope`. No third overlapping column, and no reinterpretation of either.

**Monotonicity and termination are untouched.** `domain.Status` keeps exactly three values, so `rank()`/`Worse()` and the argument at `engine.go:11-15` still hold verbatim. Phase 0 is linear and runs once; the fixed point sees a static `reachOfDep` map. The 20-round guard is not approached — the fw-edge-1 case converges in 1 iteration, hv-01 in 2, exactly as today.

**`Inputs.Net == nil` makes the whole thing inert.** That is the compositional guarantee in one line: with no topology data, `Analyse` reduces to today's function.

## Worked examples

The seed as committed today has exactly **two cables**, and both hypervisor ends are `is_mgmt=true` (`seed.go:288-292`). So derivation against today's fixture proposes **two management-plane attachments and zero data-plane ones** — which is the design behaving correctly and refusing to invent a data path from management cabling. That is precisely the trap critique #4 caught in Proposal 4, and it means **the current fixture cannot demonstrate this feature**. The M5 extension below ships with it.

**Minimum fixture extension (M5):** add `sw-core-2` (rack-b1), `fw-edge-2` (rack-b1), `sw-oob-1` (rack-a1, management); data NICs `eno2`/`eno3` on each hypervisor (`is_mgmt=false`); cables hv-01→{sw-core-1, sw-core-2}, hv-02→{sw-core-1, sw-core-2}, **hv-03→sw-core-2 only**, a sw-core-1↔sw-core-2 peer link (a deliberate cycle), sw-core-1→fw-edge-1, sw-core-2→fw-edge-2, and the existing mgmt NICs re-cabled to sw-oob-1. Groups `sw-core` (mclag, active_active, min_healthy=1), `fw-edge` (ha_pair, active_passive, **manual**), `sw-oob` (standalone). Uplink sw-core→fw-edge (data). Anchors `internet`/external and `partner-transit`/cross_env on fw-edge; `prod-net`/environment (env=prod) and `dev-net`/environment (env=dev) on sw-core. Attachments: hv-01 {sw-core-1, sw-core-2}, hv-02 {sw-core-1, sw-core-2}, hv-03 {sw-core-2}, plus three mgmt attachments to sw-oob. **The 13 VMs get no rows at all** — they inherit through `asset_closure`, which is the point.

---

## `fw-edge-1` — today reports 0 services

- R1: `fw-edge` — primary dead, standby alive, `manual` → **degraded**. `sw-core` → ok. `sw-oob` → ok.
- R2: pass A joins {sw-core, fw-edge}; pass B has fw-edge excluded, so sw-core sits alone.
- R3: no attachment loses a member. **Nothing is isolated.**
- Capacity: unchanged — every service passes exactly as today.
- Pairwise: every consumer and every provider host resolves to `sw-core`, same `compB` → **ReachOK** on every internal dependency. `orders-api → pgsql-core`, `sso → pgsql-core`, `vault → sso` — all untouched.
- Anchors: `haproxy-edge/https` (vip, external) → hosts {vm-proxy-1 on hv-02} → netOf = {sw-core} → reach(sw-core, fw-edge) is same-`compA`, not same-`compB` → **Degraded**. `partner-gateway/https` (cluster_ip, cross_env) → same. `prod-net` is on sw-core, so vault/api, pgsql-core/sql, sso/https, rabbitmq/amqp and mimir-ingester/grpc all reach their anchor in `compB` → OK.

```
Services (2)
  degraded  haproxy-edge     T1  cause=exposure
      https requires an EXTERNAL anchor; the only surviving path runs through
      net group fw-edge, which is DEGRADED: fw-edge-2 can take over but
      promotion is MANUAL and has not happened.
  degraded  partner-gateway  T2  cause=exposure   (same, cross_env)

Unreachable endpoints (2)   haproxy-edge/https, partner-gateway/https
RedundancyLost (1)          fw-edge: 1 of 2 members surviving; no firewall
                            redundancy remains until fw-edge-2 is promoted.
Isolated (0)   WontRestart (0)   Cycles (0)   Iterations 1
```

**Scenario 1 ✓** — degraded, not ok and not down. **Scenario 6 ✓** — the 9 internal services keep serving each other, and `partner-gateway`'s `hard` dependency on the route through `haproxy-edge` does **not** cascade, because exposure loss is reported and never propagated. Flip `failover_mode` to `auto` and the same run reports **0 services** with one `RedundancyLost` note (**scenario 2 ✓**). Take fw-edge-1 **and** fw-edge-2 together (M5's multi-asset form) and the group is `down`, excluded from both passes, so both entries become **down** — internal traffic still untouched.

## `sw-core-1` — today reports 0 services

- R1: `sw-core` is active_active with `min_healthy=1`; 1 surviving ≥ 1 → **ok**.
- R3: hv-01 {sw-core-1, sw-core-2} → sw-core-2 alive → live. hv-02 likewise. hv-03 {sw-core-2} → live.
- Nothing isolated, one component, every anchor reachable in `compB`.

```
Services (0)
RedundancyLost (1)  sw-core: 1 of 2 members surviving (min_healthy 1).
                    No redundancy remains; losing sw-core-2 now cuts off hv-03.
```

**Scenario 3 ✓, and no MC-LAG-specific code produced that answer** — the second attachment member did all the work.

**The mirror case, `sw-core-2`, is the interesting one:** the group is still ok (1 of 2), hv-01 and hv-02 are fine, but **hv-03's member set is exhausted** → hv-03 isolated → vm-vault-3, vm-sso-1, vm-k8s-1, vm-k8s-2, vm-dev-1 all lose their instances. Then `EvaluateCapacity` runs on the reduced set: **vault (quorum, 3 instances) → 2 of 3 surviving ≥ 2 → ok**; sso (standalone, 1 instance) → **down, cause=reachability**, "vm-sso-1 is running but network-isolated: hv-03's only attachment is to sw-core-2"; mimir-ingester (sharded, both on hv-03) → down; orders-api-dev → down. Propagation then does the rest from the existing table — `orders-api --soft--> sso` degrades, `orders-web --startup--> sso` lands on `WontRestart`. **Scenario 4 ✓ and scenario 11 ✓ in one run:** the availability policy is applied to the surviving-and-reachable set, and vault's quorum arithmetic correctly reports ok.

## `hv-01` — the regression guard

`SubtreeIDs` → {hv-01, vm-vault-1, vm-db-1, vm-app-1}. No `net_group_member` is in the down set, so no group status moves. hv-01's own attachment dies but hv-01 and its VMs are already in `downInstanceIDs`, so `alive = !down && !isolated` is unchanged for them — no double count. Every surviving host is still one component, so no dependency's reach changes; and services with zero survivors are skipped by the `len(C) == 0` rule rather than double-reported.

**Result byte-identical to today: 6 services** — orders-api down, orders-web down, partner-gateway down via route, backup-agent down, pgsql-core degraded, sso degraded — 0 WontRestart, 0 cycles, 2 iterations, all `Cause=capacity`. This is the first assertion I would write, and the whole design is worthless if it perturbs it.

## `sw-oob-1` — management switch only

The engine evaluates the data plane for status and the mgmt plane for reporting, in the same run. `sw-oob` is down, but **no data-plane attachment, uplink or anchor references it**, so the data-plane graph is untouched. The mgmt-plane pass finds hv-01, hv-02 and hv-03's mgmt attachments dead.

```
Services (0)
Isolated (3)  hv-01, hv-02, hv-03 -- plane=mgmt, blocked by net group sw-oob.
              "3 assets lost management-plane reachability. No service impact."
```

**Scenario 7 ✓.** And note the mechanism: derivation set `plane='mgmt'` on these attachments from `interface.is_mgmt` on the host side of the cable, so the seed's own management cabling can never masquerade as a data path.

## `rack-a1` — scenario 10, containment and reachability together

`SubtreeIDs` expands to {rack-a1, pdu-a1, sw-core-1, fw-edge-1, hv-01, hv-02, their VMs}. The **same** expanded set feeds `DownInstances` and the forwarder down set — computed once in `Simulate`. So `sw-core` loses sw-core-1 (1 of 2 → ok) and `fw-edge` loses fw-edge-1 (manual → degraded) as a *consequence* of the rack, not as a separate input. hv-03's attachment to sw-core-2 survives, so it is not isolated. The exposure pass skips haproxy-edge and partner-gateway because vm-proxy-1 is on hv-02 and they already have zero alive instances. **Today's rack-a1 answer (9 down, 2 cycles) is unchanged, with `RedundancyLost` added.** No double count, no contradiction. ✓

## Monitoring seam

The seam is a **set union computed before anything else runs**, so nothing in the engine has to change when the webhook lands.

**Schema, shipped in M2's migration and unused until M6.** `asset_health(asset_id PK, state CHECK IN ('up','degraded','down','unknown'), at, source, message)` — its own table rather than columns on `asset`, for three reasons: observed state stays physically apart from declared intent (HANDOVER §3.5); a webhook can never touch `asset.lifecycle`, which is intent; and observed churn stays out of `asset`'s change_log diffs and out of every `SELECT * FROM asset` that already scans into `domain.Asset`. Upsert is `ON CONFLICT (asset_id) DO UPDATE`, on HANDOVER §4's safe-on-both list.

**Consumption, M6, ~15 lines in `Simulate`:**

```go
down := req.DownAssetIDs
if req.UseObservedHealth {
    unhealthy, err := s.UnhealthyAssets(ctx)          // SELECT asset_id FROM asset_health WHERE state = ?
    down = append(down, unhealthy...)
}
subtree, err := s.SubtreeIDs(ctx, down)               // existing, assets.go:432
inst, err := s.DownInstances(ctx, down)
if req.UseObservedHealth {
    failed, err := s.FailedInstances(ctx)             // observed_state IN ('failed','stopped')
    for _, id := range failed { inst[id] = true }
}
return impact.Analyse(graph, req, impact.Inputs{
    DownInstanceIDs: inst, DownAssetIDs: setOf(subtree), Net: netGraph}), nil
```

That is the entire integration. Every one of the three seams then fires unchanged, because they all read the same down-asset and down-instance sets a simulated outage produces. A firewall marked `down` by monitoring degrades its group through `AvailabilityPolicy.Evaluate`, partitions the union-find, strands its anchors and downgrades the affected dependency edges — with no engine code aware that the input came from a webhook rather than a checkbox.

**One observed-state mechanism, not two.** `service_instance.observed_state`/`observed_at` have existed since M2 of the original plan, the seed writes `"running"`, and nothing has ever read them. Every one of the four proposals added an asset-level health channel and left that one dead, which would ship two half-working mechanisms. `UseObservedHealth` wires **both**: `asset_health` for infrastructure, `service_instance.observed_state` for workloads, with `domain.ObservedInstanceStates = {running, stopped, failed, unknown}` finally giving that column a Go constant set. A future webhook can mark either level.

**Default false, deliberately.** A simulation must answer "what if I take this away", not "what if I take this away *and* whatever monitoring happened to think 20 minutes ago". A stale row can never silently change a hypothetical. The UI gets a checkbox — "include currently-unhealthy assets" — so the operator opts in and can see what the flag added.

**Alarm suppression is a rendering, never persisted state.** Once observed health is an input, "which of these 14 alarms is the root cause" is a question this engine already answers: an alarm on asset X is a *symptom* when `Result.Isolated` or `Result.Partitions` attributes X's loss to another down asset, and the blocking group is already named in the finding. Nothing is stored, so a late or out-of-order parent-down event — the classic Zabbix wrinkle where the router is polled slower than the hosts behind it — reclassifies correctly on the next render instead of leaving a stuck suppression. That is where naive suppression state machines break, and this design avoids it by not having one.

**Explicitly not built now, and not estimated:** the `POST /api/health/asset` handler, bearer-token auth for `actor_kind='agent'`, event ordering, flap damping, TTL/staleness for a monitoring system that stops posting, and the root-cause summary UI. Those are the expensive half and guessing at them would be dishonest. What lands now is the table, the flag, the constant set and the three-line union — so the retrofit is a handler, not a migration.

**One thing to settle before M6, not around it:** CLAUDE.md says every mutation writes a `change_log` row in the same transaction, no exceptions. A health endpoint polled every 30 seconds would make that table unbounded, and the rule was written for declared intent rather than observed telemetry. My proposal is to log only on state **transition**, which keeps the audit trail meaningful and bounded. CLAUDE.md is explicit that a rule which genuinely blocks a requirement is a conversation rather than a workaround, so this needs sign-off rather than a quiet decision in a commit.

## Known limits and risks

- **Data rot in `net_attachment_member`, and the rot direction is pessimistic.** It is the numerous, machine-generated, hand-corrected, unverifiable field: someone adds a second cable to make a host dual-homed and nobody adds the member row, so the tool reports that host cut off when it is fine. Pessimistic errors are the ones that get a report ignored. Mitigations are real but partial: derivation is re-runnable and produces a diff against current state, `last_seen` is carried, and a post-POC lint can compare `link` against attachment members. None of that forces anyone to run it.
- **This models forwarding paths, not permitted traffic.** A firewall that is up but whose rule was deleted, a VLAN not trunked, an MTU mismatch, a routing adjacency that did not come up — the model says the path exists and is silent about all of them. Those cause most real network outages. An operator burned by a trunk misconfiguration will find the report unhelpful and may start distrusting it wholesale.
- **Uplinks are group-to-group, so member-level uplink diversity is reported optimistically.** A core whose members are individually single-homed upward will be scored as if the mesh were full. This is the one place I consciously accept an optimistic error, and optimistic errors are the more dangerous kind. It is bounded to the forwarder-to-forwarder layer, which is where operators genuinely do build full meshes, and the host layer — where single-homing is common and cheap to record — gets the member-level gate.
- **`endpoint.exposure` becomes load-bearing having been decorative.** Every operator who typed `internal` because it sounded modest has now silently changed an impact result. The 'no anchor declared for that scope means no claim' rule makes it opt-in per scope, so the blast radius is bounded — but shipping M4 honestly needs a review pass over every endpoint's exposure, and that is data-entry arriving through the back door.
- **Reachability here is symmetric; real networks are not.** Asymmetric firewall policy, NAT and asymmetric routing are unrepresentable, and 'A can reach B but B cannot reach A' cannot be said. This collides directly with the post-POC firewall-reconciliation goal, which is inherently directional and port-scoped.
- **An isolated Patroni primary reads as 'primary lost'.** Feeding an unreachable instance into `EvaluateCapacity` as not-alive is right for the consumer's view and wrong for the split-brain view: the model will suggest a promotion that must not happen. The reason string says 'running but network-isolated' rather than 'lost', which is honest text over identical arithmetic — but the arithmetic is the same, and this model cannot detect a partition-of-a-cluster.
- **`asset_health` versus CLAUDE.md's 'every mutation writes a change_log row, no exceptions'.** A health endpoint polled every 30 seconds would make that table unbounded. My position is that the rule was written for declared intent, not observed telemetry, and the compromise is to log only on state *transition*. CLAUDE.md says to raise a conflict rather than work around it, so this needs explicit sign-off before M6 — it is the one place the design bends a stated rule.
- **Seven new tables of hand-maintained metadata that nothing discovers.** Every one is a chance for the model to drift from reality and then present a wrong answer through an authoritative-looking UI. The judgement-heavy rows are few (5-15 groups, one uplink each, a handful of anchors) and should stay true; a misplaced anchor is a single row that silently changes every exposure verdict in the estate.
- **M5 changes the seed the existing tests assert against.** Adding `sw-core-2`, `fw-edge-2` and `sw-oob-1` shifts rack membership counts and asset totals. That is exactly where a subtle regression hides, and it means the change is not additive-only in the test suite even though it is additive-only in the schema.
- **Effort is roughly 2,750 LOC across six milestones.** Each is small and shippable, but M1 (data entry) and M5 (fixture) are genuine product work that does not look like 'the reachability feature', and there will be pressure to skip them. Skipping M1 makes the engine seed-only; skipping M5 means the demo shows a coverage banner rather than an answer.