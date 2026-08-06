# invctl — implementation plan: DCIM, IPAM, and the missing domains

**Purpose:** material for you and the agent to argue over and order. Every work
package here is intended to be built. Nothing is gated on a client asking for it.

**Baseline (measured 2026-08-06):** 56 tables, 20 migration versions per dialect
plus 9 shared, 30 read views, 65 write actions, 581 tests on SQLite and
PostgreSQL.

---

## 0. Ground rules

### Invariants — these are architecture, not caution

Break any of these and the product stops being coherent. They are the reason
invctl can answer questions NetBox cannot; they are not overhead.

Each carries the guard that enforces it. An invariant with no guard is a promise,
and this project has already been bitten by the difference.

1. **Declared vs observed stays separate.** New models are declared state unless
   designed as observations. A reporter never creates and never retires.
   → `TestObservedPathTouchesNoDeclaredTable`,
   `TestOnlyTheObservedPathWritesTheObservedTables`, `TestEveryColumnIsClassified`
2. **Audit row in the same transaction as every declared-state mutation.**
   → `TestEveryMutationWritesChangeLog`
3. **Append-only audit, no hard deletes.** Retire keeps the row and its history.
   → `TestNoAssembledWriteReachesChangeLog`, `TestPruneNeverRemovesDeclaredEntries`
4. **Optimistic concurrency on every edit** — second save refused, not merged.
   → `TestEveryEditorRefusesASecondSaveFromOneToken`
5. **No personal data.** Teams and roles.
   → `TestChangeLogActorIsAnOpaqueID`, `TestATeamsContactNeverReachesTheAuditTrail`
6. **SQLite and PostgreSQL, same build, same queries.** No `inet`/`cidr` — extend
   the existing normalised address representation that already powers range lookup.
   → `TestNoQueryIsDialectSpecific`, `TestTheSharedSchemaRunsOnBothEngines`,
   `TestEveryDialectMigrationHasBothHalves`
7. **One static binary. No new runtime dependencies. No outbound calls.**
   → `TestNothingReachesOutOfThisProcess`
8. **No private keys**, and no field that could become a place to paste one.
   → `TestAKeyReferenceNeverReachesTheAuditTrail`, `TestTheNamesFieldRefusesAPaste`
9. **No delivery to the estate.** Render, export, display — never push. This is
   the line that keeps the security claim true. Config *rendering* is fine
   (WP-H1); an SSH or API push is not, ever.
   → `TestNothingReachesOutOfThisProcess`

### Negotiable

Everything else: table count, model shape, screen layout, order of work, which
domains ship first, how far to take each one. Argue about all of it.

### Working method

- One work package per branch; spec first, then code. The spec outlives the code.
- Mirror existing module, query and template conventions. Read three neighbours
  before inventing anything.
- **Extend the fixture estate with every WP.** The impact engine is tested against
  a real fixture — a new edge type absent from the fixture is untested.
- Full suite on both engines before merge.
- Update vocabularies and help when adding an object type.

### Definition of done — every work package

- [ ] Migration applied and verified on both engines
- [ ] Audit in-transaction; retire not delete; optimistic concurrency
- [ ] Search integration where the entity has a resolvable identifier
- [ ] Expiry integration where it carries an end date
- [ ] Cost integration where it can carry a price
- [ ] Impact and/or reachability integration, with fixture coverage
- [ ] Both engines green, 581+ still passing
- [ ] Self-checks pass: audit coverage, no personal data, no hard deletes,
      portability — these are the named tests above, not a review checklist

---

## 1. Work packages

Each carries: size (S/M/L/XL), what it depends on, and what it unlocks. Sizes are
relative effort, not time.

### Group A — Prerequisites

**WP-A1 · Bulk import** — M — depends: none — unlocks: everything
CSV per object type. Dry-run reporting what it would create and what it cannot
resolve. References by natural key, not UUID. Whole-file refusal on partial
failure. Audited as declared-state writes, one row per object. An import that
collides with an entity a reporter already flagged as unrecognised should stop and
point at it — that is a reconciliation moment, not an error.

**WP-A2 · Read-only inventory API** — M — depends: none — unlocks: integrations
Read-only, scoped tokens, keyset pagination matching the change log. Shapes for
Ansible inventory and observability joins. No write routes.

**WP-A3 · Type-and-template mechanism** — S — depends: none — unlocks: C1, E1, F1
Generic "a type carries component templates; instantiating creates them". Later
type edits do **not** rewrite existing instances — report drift as a finding
instead, since nobody declared that change.

*See the appendix: there is a standing argument for building this concretely
inside WP-C1 and extracting it when WP-B1 arrives, rather than up front.*

**WP-A4 · Custom fields** — M — depends: none — unlocks: adoption
Otherwise every estate-specific attribute is a migration. Typed, per object type,
searchable, exportable, audited like any other field.

---

### Group B — Physical: power and cabling

**WP-B1 · Power chain** — L — depends: A3 (light) — unlocks: new impact source
Power panel → feed → outlet → asset input. Supply, phase, voltage, amperage, max
utilisation. Assets with redundant inputs from different feeds.

Engine: a feed is a simulatable failure target. Single-fed assets report **at
risk**. A+B inputs tracing to the same upstream panel report **false redundancy** —
a finding nothing else in the market produces. Over-rating utilisation is a
finding, not an error.

*See the appendix: the propagation half is smaller than L; the false-redundancy
analysis is the part worth sizing around.*

**WP-B2 · Front and rear ports, pass-through** — M — depends: none — unlocks: B3
Port position mapping on patch panels and similar passive gear.

**WP-B3 · Cables and path tracing** — L — depends: B2
Cable with two terminations; a tracer that walks pass-through hops end to end.
Explicit hop limit, cycle guard, and a test asserting termination on a
deliberately malformed patch field — this is where these systems get slow and
subtly wrong.

Engine: a cable or panel becomes a failure target; partitioned findings gain the
specific hop responsible.

**WP-B4 · Cable profiles and bundles** — L — depends: B3
Breakout and multi-strand lane mapping (4×10 GE ↔ 40 GE), and bundles representing
runs managed as a unit. This is parity with NetBox 4.5/4.6 and it is genuinely
hard — the profile changes what "connected" means in the tracer.

**WP-B5 · Rack elevations** — M — depends: none
Units, position, orientation, depth, blade and slot occupancy. Utilisation derived,
never stored. Reuses existing containment for failure semantics — this adds
position and capacity, not new impact behaviour.

---

### Group C — Physical: hardware catalogue

**WP-C1 · Manufacturers, device types, module types** — M — depends: A3
Model, height, full-depth, part number, EOL and end-of-support dates. Component
templates for interfaces, ports, outlets, inputs. Modules, module bays, inventory
items, serial tracking.

Engine: expiry resolves an asset's support date from its type when the asset has
none — **and says which source it used.** Provenance, not silent inference. Search
resolves part numbers and serials.

---

### Group D — Addressing

**WP-D1 · VRFs and route targets** — M — depends: none
Grouping, lookup scoping and prefix-level uniqueness for overlapping tenant space.

*See the appendix: the "address uniqueness is global" premise does not hold in
this codebase. Worth having on its merits, but it is not a correctness fix and
carries no do-it-early urgency.*

**WP-D2 · Prefix hierarchy, containment, utilisation** — M — depends: D1 ideally
Parent/child resolution, depth, utilisation derived from contained prefixes, ranges
and addresses. Search already resolves a CIDR; this makes the result a tree.

**WP-D3 · IP ranges and next-free allocation** — M — depends: D2
Ranges within prefixes, "next available address" query.

**WP-D4 · VLANs and VLAN groups** — M — depends: none
VLANs, groups scoped to site/rack group/cluster, assignment to prefixes and
interfaces. A VLAN is a reachability domain — give it an edge, not just a record.

**WP-D5 · FHRP groups** — S — depends: D4 light
Shared virtual IP across devices. Small model, direct payoff: this *is* redundancy,
and the reachability model already reports redundancy lost.

**WP-D6 · ASNs, RIRs, aggregates** — S — depends: D2
Completes the IPAM hierarchy above prefixes. Parity item.

**WP-D7 · L2VPN overlays** — M — depends: D4
Overlay modelling and terminations. Parity item; matters for service providers.

---

### Group E — Circuits and virtualization

**WP-E1 · Providers and circuits** — M — depends: none — unlocks: cost + reachability
Provider, provider network, circuit type, circuit (install date, commit rate,
contract end), terminations to site or interface. Virtual circuits for parity.

Integration: monthly run rate lands natively in the existing cost model with
validity windows and amendments. Contract end joins the expiry report. A circuit is
a reachability edge — losing it partitions the sites it joins; a single-circuit site
reports redundancy lost.

*See the appendix: this bundles two very different sizes and should be split.*

**WP-E2 · Clusters and cluster groups** — M — depends: A3
Cluster type, group, cluster, VM placement, VM type, virtual disks.

Engine: HA semantics. Losing one host in a three-node cluster consults cluster
policy before propagating — the same availability reasoning already applied to
quorum services, one layer down.

---

### Group F — Wireless

**WP-F1 · Wireless LANs and links** — M — depends: A3 light
Wireless LANs, groups, links between interfaces, authentication attributes. Links
are reachability edges like cables; a wireless bridge is a single point of failure
worth simulating.

---

### Group G — Operational surface

**WP-G1 · Object-level permissions** — L
Constraint-based RBAC ("this group may edit assets at these sites only"). The
single biggest gap against NetBox for multi-team and MSP use.

**WP-G2 · Webhooks and event rules** — M
Fire on declared-state change. Outbound HTTP to a *user-configured* endpoint is
consistent with invariant 9 — it delivers notification, not configuration. Keep it
that way: no templated payload that could carry a command.

*This is the one work package that requires editing `dialAllowlist` in
`internal/estate/guard_test.go`. That is deliberate — the guard makes the decision
explicit rather than blocking it. Read the appendix before starting.*

**WP-G3 · Journal entries** — S
Free-text operational notes on entities. Distinct from audit: audit is what
changed, journal is what a human observed. Cheap, and heavily used in practice.

**WP-G4 · Tags, saved filters, table configs** — M
Makes large estates usable. Pure quality of life, and users notice its absence
immediately.

**WP-G5 · Export templates** — S
User-defined output formats over any object list.

---

### Group H — Configuration data

**WP-H1 · Config contexts and config templates** — L — depends: A4 helps
Context data assembled by scope (site, role, platform, cluster, tenancy) with
declared precedence, and templates rendering that data into text.

**Render only. No delivery.** The output is displayed, downloaded, or fetched via
the read-only API by something else that does the pushing. Invariant 9 holds:
invctl still has no credentials to the estate and no code path that touches it.
NetBox does exactly this and remains a source of truth rather than a config
manager. Keep the boundary visible in the UI — call the button *Render*, never
*Deploy*.

---

### Group I — Consolidation

Run after any two groups, not at the end. This is where plans like this rot.

**WP-I1 · Engine consolidation** — M, recurring
Every new edge type must appear in impact simulation, reachability findings,
shutdown order, and the fixture. Audit that they do; add the missing ones.

**WP-I2 · Report consolidation** — M, recurring
Expiry (type EOL, circuit contract end, with provenance), cost (circuits, metered
power draw), search (serials, part numbers, circuit IDs, VLAN IDs), project cost
buckets for new cost-bearing entities.

**WP-I3 · Performance** — M
Prefix trees, path tracing and cluster-aware impact change the query profile. Build
a fixture large enough to be slow — a few thousand assets — and assert bounds on
impact simulation and prefix tree construction.

---

## 2. Dependency graph

```
A1 import ─┐
A3 type/template ──► C1 device types ──► B1 power
                 └─► E2 clusters          B5 racks
                 └─► F1 wireless
A4 custom fields ──► H1 config contexts
B2 ports ──► B3 cables ──► B4 profiles
D1 VRF ──► D2 prefixes ──► D3 ranges ──► D6 ASN/RIR
D4 VLANs ──► D5 FHRP ──► D7 L2VPN
E1 circuits            (no deps)
A2 API                 (no deps)
G1..G5                 (no deps)
every group ──► I1 / I2
```

Anything with no inbound arrow can start immediately and in parallel: **A1, A2,
A3, A4, B2, B5, D1, D4, E1, G1–G5.**

---

## 3. Decisions to argue with the agent

Not recommendations — the questions that actually determine the order.

1. **Breadth-first or depth-first?** Ship every domain shallow and iterate, or take
   DCIM to full parity before touching IPAM? Breadth makes the comparison table
   green sooner; depth makes each domain actually usable.
2. **Parity or differentiation first?** The NetBox-shaped work (C1, D1–D7, B4, F1)
   closes the gap. The engine work (B1 power, B3 paths, E1 circuits, E2 clusters)
   widens the lead. Both matter; the ratio is the decision.
3. **Does A1 (import) come before or after the first new domain?** Every domain you
   add before the importer is a domain nobody can populate without typing.
4. **How much does A4 (custom fields) reduce the rest?** Some parity items might be
   satisfiable as custom fields rather than first-class models. Which ones — and
   which must be first-class because the engine has to reason over them?
5. **Where does the fixture estate stop being representative?** It currently has 28
   assets. At what point does it need to be regenerated at scale, and should that
   be WP-I3 pulled forward?
6. **Migration strategy.** ~40 more tables roughly doubles the schema. One
   migration per WP, or squash per group? Affects rollback granularity.

---

## 4. Parity checklist against NetBox 4.6

Track it. It answers "what's left" in one glance.

| NetBox area | Covered by |
|---|---|
| Sites, locations, racks, rack groups | existing + B5 |
| Device types, module types, component templates | C1 |
| Modules, inventory items, serials | C1 |
| Cables, terminations, path tracing | B2, B3 |
| Cable profiles, bundles | B4 |
| Power panels, feeds, outlets | B1 |
| Prefixes, aggregates, RIRs, utilisation | D2, D6 |
| IP ranges, next-free allocation | D3 |
| VRFs, route targets | D1 |
| VLANs, VLAN groups, scoping | D4 |
| FHRP groups | D5 |
| L2VPN overlays | D7 |
| ASNs | D6 |
| Providers, circuits, terminations | E1 |
| Clusters, VM types, virtual disks | E2 |
| Wireless LANs and links | F1 |
| Config contexts and templates | H1 |
| Custom fields, tags, export templates | A4, G4, G5 |
| Object-level permissions | G1 |
| Webhooks and event rules | G2 |
| Journal entries | G3 |
| REST API | A2 (read-only by design — deliberate divergence) |
| Bulk import | A1 |
| **Impact simulation with startup semantics** | **invctl only** |
| **Reachability findings** | **invctl only** |
| **Capital / run rate / amortisation, three-bucket projects** | **invctl only** |
| **Declared vs observed with provenance** | **invctl only** |

---

# Appendix — verified against the code

Added by the agent that maintains this codebase, on picking the plan up. The plan
above is otherwise unchanged; this is what checking its load-bearing claims found,
re-verified against the tree on 2026-08-06. Where a claim is wrong the correction
changes sequencing, so read this before starting.

## The baseline was stale

Measured: **56 tables** (excluding SQLite table-rebuild temporaries), **20
migration versions** per dialect plus **9 shared**, **30 GET routes**, **65 POST
routes**, **581 test functions**. The plan opened with 60 / 29 / 26 / 62 / 576.
Nothing turns on this, but a plan that opens with numbers should open with the
right ones.

## Wrong, and it changes the order

**WP-D1 · "Without VRFs, address uniqueness is global, which is wrong for
overlapping tenant space."** Not true here. `migrations/shared/00002_network.sql`
declares:

```sql
UNIQUE (addr_text, interface_id)
```

Uniqueness is already scoped per interface, so overlapping tenant space already
works at the address level. VRFs would add correct grouping, lookup scoping and
prefix-level uniqueness — worth having, but this is **not a correctness fix**, and
the "this touches the existing address model, so earlier is cheaper" urgency does
not follow from it. Judge D1 against D2 and D4 on merit, not on a bug that is not
there.

**WP-E1 bundles two different sizes.** The cost and expiry half is small (below).
The reachability half is not: a circuit joins *sites*, and the reach model
(`internal/impact/reach.go`) works over network groups, members, uplinks,
attachments and anchors — there is no site concept in it at all, so there is no
site-to-site edge to extend. Split the work package. The cost and expiry half
alone delivers most of the visible value and can ship first.

## Confirmed, and cheaper than stated

**A new physical failure source does not touch the impact engine.**
`impact.Request` takes `DownAssetIDs`, an outage window and an observed-health
flag — nothing structural — and its own comment says why:

> DownAssetIDs are the assets being taken away. Everything contained by them goes
> too — resolving that is the caller's job, via the closure table, so that "reboot
> this VM" and "this rack loses power" arrive here in the same shape.

So **WP-B1 (power)** integrates by writing a resolver — "this feed is down" → the
assets it feeds → `DownAssetIDs` — not by extending the engine. The propagation
half is closer to S than L.

What is *not* free is the finding the work package is actually worth building for:
**A+B feeds tracing back to one panel** is new analysis over the power graph,
unrelated to propagation. Size the work package around that.

**Circuits fit the cost model natively.** A cost owner is one `costTable{name,
column, entity, parent}` value plus a migration, and the project rollup enumerates
owners explicitly in `project_costs.go`. **WP-E1's cost and expiry half is
genuinely small.**

**WP-A1's reconciliation idea has a real hook.** `unmatched_observation` already
exists and already records entities a reporter named that do not exist. Pointing an
import at it is a few lines, and it is the best detail in the plan.

## One process disagreement

**WP-A3 · build the type-and-template mechanism once, up front.** This codebase
consistently extracts a shared mechanism when the *second* caller appears, not
before — `Environment.Validate`, `Interface.Validate`, `requireVersion` and the
form partials were all extracted that way, and each came out different from what a
first-guess abstraction would have been. A generic templating mechanism designed
before WP-C1 and WP-B1 both exist will be wrong in a way that is expensive to
unpick, because instantiation semantics are exactly where the guesses go bad.

Build the component templates concretely inside WP-C1. Extract when WP-B1 needs
them.

## The invariants are now enforced, not merely stated

Invariants 1–5 and 8 already had structural guards. Two did not, and both are now
listed in section 0 against the tests that hold them:

- **Invariant 6 (portability).** `make test` runs both engines, which is the
  stronger check — but only for a query some test exercises, and coverage is
  exactly what a new domain lacks on the day its store lands. A `$1` or an `ILIKE`
  in an uncovered branch was green on both engines and would stay green until a
  deployment ran it. `TestNoQueryIsDialectSpecific` now reads all 363 SQL
  statements out of the Go source and refuses the construct whether or not a test
  reaches it; the shared migrations and the dialect-migration pairing are checked
  too. It is lexical and says so: it knows the shapes on the forbidden list, not
  SQL.

- **Invariants 7 and 9 (no outbound calls, no delivery to the estate).**
  `TestNothingReachesOutOfThisProcess` refuses the *capability* rather than the
  intent: no `os/exec`, no HTTP client symbols, no dialler anywhere in non-test
  code. invctl cannot push configuration because it cannot run a command or make a
  request — not because no code currently does.

  It claims only what is true. invctl opens sockets: to its database, and to a
  directory server when LDAP is configured. Those are its own infrastructure, not
  the estate, and pretending otherwise would make the guard a lie that gets
  switched off. The single allowlist entry is `ldap.DialURL` in
  `internal/auth/ldap.go`, and `TestTheDialAllowlistIsSpent` fails if that
  exemption stops matching, so an unused exemption cannot sit open.

**This is why WP-G2 (webhooks) is now a conversation rather than a diff.** Its
outbound HTTP is a real breach of the mechanism, whatever the payload contains.
The plan's argument — notification is not configuration — may well be right, but
the guard forces it to be made out loud, in an allowlist entry with a reason,
rather than arriving as three reasonable-looking lines in a handler. That is the
point of writing the guard before the work package rather than after.

## Two earlier gaps, now closed

Both were listed here as missing from the previous plan, and both shipped in
`ae19f4f`:

- **`/changes/{id}`** — a single audit entry can now be linked to and cited in an
  incident write-up, with actor, actor kind, ticket reference and field detail.
- **The silent-fallback shape** — seven instances of "a value arrives, cannot be
  used, and is silently replaced by something indistinguishable from a legitimate
  answer". `TestNoParseErrorIsDiscarded` now refuses the mechanism (a discarded
  parse error) rather than recognising symptoms, with a deliberately empty
  allowlist. Every work package above adds form fields and query parameters, so
  the guard matters more from here, not less.
