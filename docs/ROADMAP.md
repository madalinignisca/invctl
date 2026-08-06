# invctl — working plan: DCIM depth, IPAM depth, and missing domains

**Audience:** a coding agent picking this up, plus the human who owns it.
**Status:** plan only. Nothing here has been specced in detail or built.
**Baseline:** invctl at 60 tables, 29 migrations, 26 read views, 62 write actions,
576 tests on SQLite and PostgreSQL.

---

## 0. Read this before starting

### What invctl is, and what it must remain

invctl answers one question: *something is broken — what else does this break, and
who do I call?* It is an inventory **that computes over itself**: impact,
reachability, expiry, ownership and cost. It is not a DCIM, not a monitoring
system, and not a configuration manager.

Every work package below must earn its place by feeding one of those answers. A
model that is only ever displayed, never computed over, is a NetBox feature we are
copying for its own sake — and NetBox does it better. **Reject scope on that test.**

### Two explicit recommendations against scope

**Do not build config contexts or config templates.** Rendering device
configuration makes invctl a configuration manager. The product says in writing
that it is read-only against the estate and never acts on it, and that claim is
what gets it approved by security reviewers. Config rendering forfeits it. If a
client needs this, the answer is Ansible or NetBox, not invctl.

**Wireless is near the bottom for a reason.** Wireless LANs and links do not
change any impact, reachability or cost answer for the target estates. Build it
only if a paying client asks by name.

### Sequencing principle

The order below is **by value to the through-line**, not by NetBox's menu order.
Power and circuits come before prefix hierarchies because losing a PDU or a
carrier link is an outage; a missing RIR record is not.

### Hard invariants — do not break these

These are enforced by the codebase's own rules and by existing tests. Any work
package that cannot be built without violating one is the wrong work package.

1. **Declared vs observed stays separate.** New models are declared state unless
   explicitly designed as observations. A reporter can never create or retire.
2. **Every write to declared state writes an audit row in the same transaction.**
3. **Append-only audit. No hard deletes.** Retirement keeps the row and history.
4. **Optimistic concurrency on every edit form** — the second save is refused, not
   silently merged.
5. **No personal data.** Teams and roles, never people. No names, no emails.
6. **Portability: SQLite and PostgreSQL, same build, same queries.** No
   PostgreSQL-only types. In particular **do not reach for `inet`/`cidr`** — reuse
   whatever normalised representation the existing address and prefix code uses
   for range lookups, and extend it.
7. **One static binary, no new runtime dependencies, no outbound network calls.**
8. **No private keys, ever**, and no field that could become a place to paste one.
9. Every migration runs and is verified on **both** engines.

### How to work

- One work package per branch. Do not batch.
- **Spec before code.** Write the model and the screens as a short document, get
  it reviewed, then implement. The spec is the deliverable that outlives the code.
- Mirror the existing module layout, naming, query style and template patterns
  rather than introducing new ones. Read three neighbouring modules first.
- **Extend the fixture estate** with every work package. The impact engine is
  tested against a real fixture, not mocks; a new edge type that is not in the
  fixture is not tested.
- Run the full suite on both engines before opening a PR.
- Update the vocabularies screen and the help page when adding a new object type.

### Definition of done — applies to every work package below

- [ ] Migration written, applied and rolled forward cleanly on SQLite and PostgreSQL
- [ ] Audit rows written in the same transaction as every mutation
- [ ] Retire, not delete, on every new entity
- [ ] Optimistic concurrency on every edit form
- [ ] Search integration where the entity carries a resolvable identifier
- [ ] Expiry report integration where the entity carries an end date
- [ ] Cost model integration where the entity can carry a price
- [ ] Impact and/or reachability engine integration, with fixture coverage
- [ ] Tests on both engines; existing 576 still green
- [ ] Screens follow the existing panel conventions; no new UI idioms
- [ ] Codebase self-checks still pass (audit coverage, no personal data, no hard
      deletes, engine portability)

---

## Phase A — Prerequisites

These unblock everything. Nothing below Phase A is usable without them.

### WP-0.1 · Bulk import

**Size:** M · **Why:** every evaluation begins with "can I load my spreadsheet",
and every work package below multiplies the amount of typing a user faces. Adding
five new object types without an importer makes the product *worse* to adopt.

- CSV in, per object type, with a dry-run that reports what it would create and
  what it cannot resolve
- Resolve references by natural key (site name, asset name, prefix) not by UUID
- Import is a declared-state write: audited, one audit row per created object,
  attributed to the importing user
- Partial failure is a refusal of the whole file, not a half-loaded estate
- Reject an import that would create an entity a reporter has already flagged as
  unrecognised, and point at it instead — that is a real reconciliation moment

### WP-0.2 · Read-only inventory API

**Size:** M · **Why:** already named as post-POC in the roadmap, most-asked-for
missing piece, and the precondition for the NetBox-as-upstream strategy.

- Read-only. No write routes. This is not negotiable — a write API reopens every
  security question the product currently closes
- Scoped tokens, same narrow model as the monitoring token
- Keyset pagination, matching the change log
- Shapes suitable for Ansible inventory and for a Grafana/observability join

### WP-0.3 · The type-and-template pattern

**Size:** S · **Why:** it recurs in WP-A1, WP-C1 and WP-F1. Build it once.

A generic "a type carries a template of components; instantiating the type creates
those components on the instance" mechanism, with the rule that later edits to a
type do **not** retroactively rewrite instances (that would be a declared-state
change nobody made). Instead, report drift as a finding.

---

## Phase B — Highest value: things that cause outages

### WP-B1 · Power chain

**Size:** L · **Why: this is the strongest single addition in the whole plan.**
Losing a PDU or a feed is a real, common, unmodelled outage. It plugs straight into
the existing impact engine as a new failure source, and "simulate losing this
power feed" is a demo moment as good as "simulate losing this rack".

Model: power panel → power feed → outlet → asset draw. Feeds carry supply,
phase, voltage, amperage and max utilisation. An asset can have redundant inputs
from different feeds — which is exactly the availability-policy case the impact
engine already handles for redundant pairs.

Engine integration:
- A feed becomes a simulatable failure target
- Assets fed by a single path report as **at risk**; assets with A+B feeds from the
  same upstream panel report as **false redundancy** — a genuinely valuable finding
- Utilisation over the feed's rating is a finding, not an error

Screens: panel and feed detail with connected outlets and draw; asset detail gains
a power panel; a "power" findings view.

### WP-B2 · Circuits and providers

**Size:** M · **Why:** double value. Circuits are both a reachability edge between
sites and a recurring monthly cost — so this feeds the impact engine *and* the
part of the product the CEO asked for.

Model: provider, provider network, circuit type, circuit (with install date,
commit rate, contract end), circuit termination to site or to an interface.

Integration:
- **Cost:** a circuit carries a monthly run rate natively — the cleanest possible
  fit for the existing cost model, including validity windows and amendments
- **Expiry:** contract end date joins the expiry report alongside hardware support
  and TLS
- **Reachability:** a circuit is an edge; losing it partitions the sites it joins,
  and a site with one circuit reports redundancy lost

### WP-B3 · Cable paths, front and rear ports

**Size:** L · **Why:** the existing reachability model reasons about network groups
and attachments. Real cuts happen in patch panels. Pass-through modelling turns
"these two are attached" into "here is the physical path, and here is the panel
that breaks it".

Model: front port and rear port on an asset, with position mapping; cable with two
terminations; a path tracer that walks pass-through hops end to end.

Integration: a cable or a patch panel becomes a simulatable failure target;
partitioned findings gain the specific hop that caused them.

**Risk:** path tracing is where this kind of system usually gets slow and subtly
wrong. Build the tracer with an explicit hop limit, a cycle guard, and a test that
asserts termination on a deliberately malformed patch field. Do not attempt cable
profiles (breakout and multi-strand lane mapping) in this work package — see WP-G2.

---

## Phase C — Data entry and lifecycle leverage

### WP-C1 · Manufacturers, device types, module types

**Size:** M · **Why:** two payoffs. Component templates remove most of the typing
that WP-B1 and WP-B3 create. And **end-of-support at the type level** feeds the
expiry report — today an estate must record a date per asset, which is exactly why
the demo shows 22 assets with no date.

Model: manufacturer; device type (model, height in units, full-depth, part number,
EOL and end-of-support dates); module type; component templates for interfaces,
ports, outlets and inputs; inventory items and serial tracking.

Integration:
- Expiry report resolves an asset's support date from its type when the asset has
  no explicit date, and **says which it used** — provenance, not silent inference
- Search resolves part numbers and serials

### WP-C2 · Rack elevations

**Size:** M · **Why:** completes the containment model already on screen, and makes
capacity a question the tool can answer.

Model: rack units, position, orientation, depth, blade or slot occupancy; rack
utilisation derived, never stored.

Integration: reuse the existing containment graph — a rack already takes everything
inside it away. This work package adds position, not new failure semantics. Keep it
honest about that; it is a display and capacity feature.

---

## Phase D — Addressing depth

Order within IPAM matters less; these are largely independent. **Portability is the
main risk in this phase** — see invariant 6.

### WP-D1 · Prefix hierarchy, containment and utilisation

**Size:** M

Parent/child prefix resolution, depth, and utilisation derived from contained
prefixes, ranges and addresses. Utilisation is computed, never stored.

Integration: search already resolves a CIDR; this makes the result a tree rather
than a row.

### WP-D2 · IP ranges and next-free allocation

**Size:** S–M · Ranges within prefixes, with a "next available address" query.
This is the one IPAM feature users ask for by name.

### WP-D3 · VRFs and route targets

**Size:** M · **Why it matters here:** without VRFs, address uniqueness is global,
which is wrong for any estate with overlapping tenant space. This is a correctness
fix as much as a feature, and it touches the existing address model — do it before
the address table grows further.

### WP-D4 · VLANs and VLAN groups

**Size:** M · VLANs, groups with a scope (site, rack group, cluster), and
assignment to prefixes and interfaces.

Integration: a VLAN is a reachability domain — worth an edge in the reachability
model, not just a record.

### WP-D5 · FHRP groups

**Size:** S · **Why above ASNs and L2VPN:** a shared virtual IP across two devices
is *redundancy*, and the reachability model already reports redundancy lost. This
is a small model with a direct engine payoff.

### WP-D6 · ASNs, RIRs, aggregates

**Size:** S · Record-keeping completeness. No engine integration. Build when a
client asks.

---

## Phase E · Virtualization

### WP-E1 · Clusters and cluster groups

**Size:** M · **Why:** invctl already models host → VM containment. A cluster adds
**HA semantics**: losing one host in a three-node cluster should report what the
cluster policy says, not "everything on that host is down". This is the same
availability-policy reasoning the impact engine already applies to quorum services,
applied one layer down — so it fits the existing engine rather than extending it.

Model: cluster type, cluster group, cluster, VM-to-cluster placement, VM type,
virtual disks.

Integration: cluster becomes a simulatable failure target; host loss consults
cluster policy before propagating.

---

## Phase F · Optional, on client demand only

### WP-F1 · Wireless LANs and links
**Size:** M · No engine value for the target estates. Build on request.

### WP-F2 · Cable profiles and bundles
**Size:** L · Breakout and multi-strand lane mapping. Only meaningful for estates
with dense passive optical infrastructure. Significant complexity in the path
tracer for a narrow audience.

### WP-F3 · L2VPN overlays
**Size:** M · Build when a service-provider client asks.

### WP-F4 — NOT PLANNED · Config contexts and templates
Rejected. See section 0. Rendering device configuration contradicts the product's
central security claim.

---

## Phase G · Consolidation

Run this after any two phases above, not at the end.

### WP-G1 · Engine consolidation
Every new edge type added above must appear in: the impact simulation, the
reachability findings, the suggested shutdown order, and the fixture estate. Audit
that they all do, and add the missing ones. This is where a plan like this
normally rots.

### WP-G2 · Report consolidation
- Expiry: device type EOL, circuit contract end, and their provenance
- Cost: circuits, power draw as an input to running cost if the client meters it
- Search: serials, part numbers, circuit IDs, VLAN IDs
- Project cost buckets: confirm new cost-bearing entities land in the right bucket

### WP-G3 · Performance
Prefix trees, cable path tracing and cluster-aware impact all change the query
profile. Establish a fixture large enough to be slow (a few thousand assets), and
assert bounds on the impact simulation and the prefix tree.

---

## Suggested order

```
WP-0.1 import  →  WP-0.3 type/template  →  WP-C1 device types
                                            │
WP-B2 circuits  ──────────────────────────► WP-G2 reports
WP-B1 power     ──┐
WP-B3 cabling   ──┴───────────────────────► WP-G1 engine
WP-D3 VRF → WP-D1 prefixes → WP-D2 ranges → WP-D4 VLANs → WP-D5 FHRP
WP-E1 clusters
WP-0.2 API  (any time after WP-C1; before any client integration)
```

**If you only do three:** WP-0.1 (import), WP-B2 (circuits — cost *and*
reachability), WP-B1 (power — the best new outage source). Those three change what
the product can answer. Most of the rest changes what it can display.

---

# Appendix — verified against the code

Added by the agent that maintains this codebase, on picking the plan up. The
plan above is unchanged; this is what checking its load-bearing claims found.
Where a claim is wrong, the correction changes sequencing, so read this before
starting.

## Confirmed, and cheaper than stated

**A new physical failure source does not touch the impact engine.**
`impact.Request` takes `DownAssetIDs` and nothing else, and says so:

> DownAssetIDs are the assets being taken away. Everything contained by them
> goes too — resolving that is the caller's job, via the closure table, so that
> "reboot this VM" and "this rack loses power" arrive here in the same shape.

So **WP-B1 (power)** integrates by writing a resolver — "this feed is down" →
the assets it feeds → `DownAssetIDs` — not by extending the engine. The
propagation half is closer to S than L.

What is *not* free is the finding the work package is actually worth building
for: **A+B feeds tracing back to one panel** is new analysis over the power
graph, unrelated to propagation. Size the work package around that, not around
the outage simulation.

**Circuits fit the cost model natively.** A cost owner is one `costTable{name,
column, entity, parent}` value plus a migration; the project rollup enumerates
owners explicitly in `project_costs.go` and needs one line. **WP-B2's cost and
expiry half is genuinely small.**

**WP-0.1's reconciliation idea has a real hook.** `unmatched_observation`
already exists and already records entities a reporter named that do not exist.
Pointing an import at it is a few lines, and it is the best detail in the plan.

## Wrong, and it changes the order

**WP-D3 · "without VRFs, address uniqueness is global, which is wrong for any
estate with overlapping tenant space."** Not true here. The constraint is:

```sql
CONSTRAINT ip_address_addr_text_interface_id_key UNIQUE (addr_text, interface_id)
```

Uniqueness is already scoped per interface, so overlapping tenant space already
works at the address level. VRFs would add correct grouping, lookup scoping and
prefix-level uniqueness — worth having, but this is **not a correctness fix**,
and the "do it before the address table grows further" urgency does not hold.
Demote it to sit with the rest of Phase D on its merits.

**WP-B2 bundles two different sizes.** The cost and expiry half is small (above).
The reachability half is not: a circuit joins *sites*, and the reach model
(`internal/impact/reach.go`) works over network groups, attachments and planes —
there is no site-to-site edge to extend. Split the work package. The cost and
expiry half alone delivers most of the client-visible value and can ship first.

## One process disagreement

**WP-0.3 · build the type-and-template mechanism once, up front.** This codebase
consistently extracts a shared mechanism when the *second* caller appears, not
before — `Environment.Validate`, `Interface.Validate`, `requireVersion` and the
form partials were all extracted that way, and each came out different from what
a first-guess abstraction would have been. A generic templating mechanism
designed before WP-C1 and WP-B1 both exist will be wrong in a way that is
expensive to unpick, because instantiation semantics are exactly where the
guesses go bad.

Build the component templates concretely inside WP-C1. Extract when WP-B1 needs
them.

## Two things the plan does not mention

- **No `/changes/{id}`.** A single audit entry cannot be linked to or cited in an
  incident write-up. Small, and it fits the through-line better than most of
  Phase D.
- **A recurring defect shape.** Five instances in three days of: a value arrives,
  cannot be used, and is silently replaced by something indistinguishable from a
  legitimate answer (`intValue`, `optionalInt`, a page cursor, a report horizon,
  and an error with no rendering site). Every work package below adds form fields
  and query parameters. Worth a sweep for that shape, and worth knowing about
  while writing new ones.

## Baseline correction

577 tests, not 576, at the time of reading.
