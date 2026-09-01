# invctl — implementation plan: DCIM, IPAM, and the missing domains

**Purpose:** material for you and the agent to argue over and order. Every work
package here is intended to be built. Nothing is gated on a client asking for it.

**Baseline (measured 2026-08-06):** 56 tables, 20 migration versions per dialect
plus 9 shared, 30 read views, 65 write actions, 581 tests on SQLite and
PostgreSQL.

**Since then (2026-08-31):** 30 work packages complete, 1,428 test functions,
184 write routes behind a generated inventory. See "The 1.0 line" below for
what ships and what is deliberately held back.

---

## The 1.0 line

**1.0 means a company can deploy invctl on their own estate and run it.**
Not feature parity with NetBox, not every work package below — those keep
their own order, and the release does not wait for them.

From 1.0 the **database schema, the URLs and the `INV_*` environment
variables are stable**. `/api/v1` is covered too: fields may be added, never
removed or renamed, and a consumer must tolerate fields it does not
recognise. Anything breaking becomes 2.0, or `/api/v2` alongside v1. The
wire shape is enforced by `TestTheV1WireShapeIsUnchanged`, not just promised
in `docs/API.md`.

### Deferred to 1.1, deliberately

These are decided, not forgotten. Each is recorded here rather than in a
scratch file, because the scratch directory is git-ignored and a plan nobody
else can read is not a plan.

**Project-owner write scope, the rest of it.** WP-G1 extended a project
owner to their projects' assets, services and circuits, plus the types
scoped by one owning entity: `interface`, `ip_address`, `service_instance`
and `journal_entry` (`domain.ScopeSubjectDerived`). Still Administrator-only:

- `dependency` and `link` — **two-ended writes.** Each connects two entities
  and logs one `change_log` row, so `tx.log` authorizes one end and the
  other needs its own check. This is the shape that produced the
  `ReparentAsset` escalation; do both ends, and test each end independently,
  or the bug passes half-present.
- `asset_cost` — crosses the cost-visibility axis. Writing a cost line while
  `can_see_costs` may be false needs thinking about before building.
- `certificate`, `cluster` — no argument against, just not needed for 1.0.

Widening scope is never a breaking change, so 1.1 can take these freely.

**Saved-view rename.** `internal/store.UpdateSavedView` is real, tested
store-level work (`internal/store/savedviews_test.go`), but WP-G4b Wave B
removed the `POST /views/{id}` route and its `SavedViewUpdate` handler:
nothing posted to it — checked against `web/templates`, `web/static/app.js`
and the E2E suite — and a mutating route with no caller is unreviewed
surface. A rename control in 1.1 wires a new handler to the existing store
method; the store side needs no further work.

**Tighten the fact-deleting allowlist to the table, not the file.**
`TestTheOnlyFactDeletingStatementIsThePrune` (`internal/store/prune_test.go`)
maps a **file path** to a reason, so allowlisting a file exempts every
`DELETE FROM` in it. WP-G4b added `internal/store/users_admin.go` for the
scrub's erasure of saved views — and that is precisely the file where a
future `DELETE FROM app_user` would plausibly be written, hard-deleting a
person instead of scrubbing them. It would pass silently. The map's values
already name the table each entry is for, so the check can compare the
deleted table against the reason string; eleven existing entries share this
coarseness, which is why this is its own small piece of work rather than a
line in someone else's.

**No test drives a write route unauthenticated.** The boundary sweep proves
object-level permission holds for an authenticated caller, and
`route_registration_test.go` proves nothing registers around the registrars.
Neither proves `middleware.RequireAuth` is actually in the chain — that is
verified by reading. It became worth writing down in WP-G4b, which added a
fourth registrar (`self`) whose *entire* gate is `RequireAuth`: for those two
routes, an unnoticed break in that middleware is the whole authorization
story, not one layer of it.

**WP-A4 follow-ups**, filed at that work package's merge and re-verified
2026-08-31:

- `custom_fields_show.html` renders a select's raw stored value (`high`)
  rather than its option label (`High`), though the design gives options a
  label as "what a reader sees".
- `internal/csvsafe` ships with no test file — a package holding the one
  security control on the CSV boundary, extracted from a package that had
  coverage. It is exercised indirectly; the idempotency property its doc
  comment leans on is asserted nowhere.
- `SetCustomFieldOptions` folds `value=Label` joined on `,` for its audit
  entry. An option value containing `=` or `,` can make a label rename fold
  identically, and `checkVocabulary` permits any printable non-space rune,
  so `a=b,c` is a legal code.
- `SetCustomFieldOptions` rewrites the caller's `opts` slice in place. No
  caller is harmed today and the behaviour is now commented, so this is
  documented rather than surprising.
- A duplicate-option message rendered to the browser carries the field UUID.
  Not a leak; store phrasing reaching a user.
- A refused `+42` echoes back into a `type="number"` widget that blanks it,
  so the operator's rejected text vanishes — contradicting this repo's own
  "your text is still here" principle. Refusal path only.
- `loadCustomFieldsPanel` issues one extra `GetCustomField` per select field
  per detail-page render, on the hottest page.
- `docs/custom-fields-design.md` §9's test-name list has drifted from what
  shipped: the coverage exists under different names, so the list is a false
  index.

One further entry on that list — number and date fields defended only at the
input gate while `select` was defended at render — **is closed**, in
`adce20e`. It was still listed as open when 1.0 scope was set and became a
release item before anyone checked. A list like this is a claim about the
past.

**The other quality-of-life item:** the 132 `.CanWrite` template occurrences
on estate-configuration pages, which show a project owner controls the server
refuses. (WP-G4b, saved filters, was listed here and has since shipped.) Confirmed cosmetic — every one
gates a control, none gates data — and the asset, service and circuit pages
a project owner actually uses were swept for 1.0.

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

**On the DONE markers.** Seventeen of them were added on 2026-08-17 in one pass,
because they had been applied inconsistently since the beginning and this file
had stopped being usable as a status document — asked "what is next", it named
bulk import, VLANs, clusters and the whole of group D, all of which had shipped
months earlier. Nothing was wrong with the code; the markers had simply never
been kept.

Each one was verified against the running estate rather than from memory: the
tables the package would have created, the routes it would have registered, and
the navigation entry it would have added. Where those disagreed with a guess,
the estate won — `A2` looked built because `routes.go` mentions `/api/inventory`
in a comment reserving it, and is not built at all.

**`I1` and `I2` are marked "recurring" and will never be DONE.** They are
standing audits — every new edge type must reach the impact engine, every new
cost-bearing entity must reach the reports — so an absent marker there means the
obligation stands, not that work is pending. `I2` carries the date it was last
audited and the two gaps that audit found.

### Group A — Prerequisites

**WP-A1 · Bulk import** — M — **DONE**
CSV per object type. Dry-run reporting what it would create and what it cannot
resolve. References by natural key, not UUID. Whole-file refusal on partial
failure. Audited as declared-state writes, one row per object. An import that
collides with an entity a reporter already flagged as unrecognised should stop and
point at it — that is a reconciliation moment, not an error.

**WP-A2 · Read-only inventory API** — M — **DONE**
Read-only, scoped tokens, keyset pagination matching the change log. Shapes for
Ansible inventory and observability joins. No write routes.

**WP-A3 · Type-and-template mechanism** — S — **DONE**
Generic "a type carries component templates; instantiating creates them". Later
type edits do **not** rewrite existing instances — report drift as a finding
instead, since nobody declared that change.

*See the appendix: there is a standing argument for building this concretely
inside WP-C1 and extracting it when WP-B1 arrives, rather than up front.*

**WP-A4 · Custom fields** — M — **DONE**
Otherwise every estate-specific attribute is a migration. Typed, per object type,
searchable, exportable, audited like any other field.

---

### Group B — Physical: power and cabling

**WP-B1 · Power chain** — L — **DONE**
Power panel → feed → outlet → asset input. Supply, phase, voltage, amperage, max
utilisation. Assets with redundant inputs from different feeds.

Engine: a feed is a simulatable failure target. Single-fed assets report **at
risk**. A+B inputs tracing to the same upstream panel report **false redundancy** —
a finding nothing else in the market produces. Over-rating utilisation is a
finding, not an error.

*See the appendix: the propagation half is smaller than L; the false-redundancy
analysis is the part worth sizing around.*

**WP-B2 · Front and rear ports, pass-through** — M — **DONE**
Port position mapping on patch panels and similar passive gear.

**WP-B3 · Cables and path tracing** — L — **DONE**
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

**WP-B5 · Rack elevations** — M — **DONE**
Units, position, orientation, depth, blade and slot occupancy. Utilisation derived,
never stored. Reuses existing containment for failure semantics — this adds
position and capacity, not new impact behaviour.

---

### Group C — Physical: hardware catalogue

**WP-C1 · Manufacturers, device types, module types** — M — **DONE**
Model, height, full-depth, part number, EOL and end-of-support dates. Component
templates for interfaces, ports, outlets, inputs. Modules, module bays, inventory
items, serial tracking.

Engine: expiry resolves an asset's support date from its type when the asset has
none — **and says which source it used.** Provenance, not silent inference. Search
resolves part numbers and serials.

**WP-C2 · Physical fit — will it actually go in that rack** — S — **DONE**
*Decided 2026-08-10: warn, never refuse. Depth and weight in the first pass.*

The vertical half already works. `CheckPlacement` refuses a box whose top passes
the rack's recorded height, and refuses an overlap, per face. What it cannot
answer is the question people actually argue about in front of the rack: a 2U
server fits the units and is 780mm long, and the cabinet is 600mm deep.

Four nullable numbers, two each side:

| On the rack (`asset`) | On the model (`device_type`) |
|---|---|
| `usable_depth_mm` — rail face to door | `depth_mm` — chassis |
| `max_load_kg` | `weight_kg` |

`usable_depth_mm` is **measured, not derived from the external dimension on a
datasheet**. Deriving it would be enforcing a guess, which the placement check
already refuses to do for height and should not start doing for depth.

**IT WARNS, IT DOES NOT REFUSE, AND THAT IS THE WHOLE DESIGN DECISION.** Two
boxes in one unit is impossible, so the record is false and 422 is right. Too
deep is entirely possible: it is in there, the rear door does not close, and
somebody did it anyway. Refusing that placement does not stop it happening, it
stops it being *recorded* — the operator either lies to the form or leaves the
box out, and the inventory gets worse to make the validator tidier. So this
lands in the fault/risk/gap vocabulary in `store/findings.go`, beside the rest.

**Unknown is a third answer, and it is the reason to build this here.** A rack
with no measured depth does not report "fits" — it reports a **gap**. Most tools
can only say yes or no, so an unmeasured rack silently reads as fine. This one
already has somewhere to put "I do not know".

Two details decide whether anybody trusts the output:

- **A clearance allowance, not a bare comparison.** `depth_mm <= usable_depth_mm`
  passes a 772mm server into an 800mm cabinet, which does not fit once power
  cords and a bend radius are behind it. A named default in `domain` (~75mm),
  applied and stated in the finding text. Without it the check passes the exact
  case it was built for and gets ignored inside a month.
- **Weight is a rollup and must say what it could not see.** Depth is per box;
  load is the sum of everything in the rack against its rating. Summing over
  partial data and printing a total is the dishonest version. The finding reads
  *"at least 412 kg of a 600 kg rating, with 4 of 11 boxes unweighed"* — a lower
  bound labelled as one.

Not in this pass: mounting type (2-post/4-post, rail kits) and width. Both are
real and both are an enum with a CHECK constraint and a matching Go constant
set, which is a different size of change.

**WP-C3 · Is the cabling workable** — M — **DONE**
Split from C2 deliberately, on the E1 precedent: bundling a measurement with a
graph question produced one work package that was two.

"Will it fit" is arithmetic on four numbers. "Can it be cabled" is about port
face against mounted face, airflow direction against the aisle, and how many
leads land on one box in a cabinet with no side channel.

Built as forecast: **one declared column** (`device_type.port_face`, migration
00040) and everything else derived from rows already held. Three findings —
leads crossing the cabinet, a heavily cabled box in a narrow one, and a cable
too short for the span it is declared across.

*The length check is SAME-CABINET ONLY, and that is a limit rather than an
omission: two racks have no recorded distance between them. There is no floor
plan here, so the span between cabinets is unknown and a check that guessed it
would be inventing the number the answer turns on.*

**The first version of the crossing check was wrong in the instructive way.** It
compared a box's port face against the face it is mounted on and reported every
server in the estate — correct arithmetic, useless finding, because a server is
universally racked from the front with its ports at the back. The real cost is a
lead between two boxes whose ports face OPPOSITE ways, which is what the
declared faces can actually prove. Found by reading the output, not by a test.

**WP-C4 · Airflow and thermal adjacency** — S — **DONE**
*Raised 2026-08-10 from a real case: a side-intake, rear-exhaust, short-depth
firewall in a densely cabled cabinet.*

Two declared facts: `airflow` on the device type (`front-to-rear`,
`rear-to-front`, `side-to-rear`, `side-to-side`, `passive`) and `width_mm` on
the rack. Whichever of C3 and C4 lands first brings `width_mm` with it.

From those, three findings — all derived, because the rack already knows what
sits at which unit on which face:

- **A side-breather in a standard-width cabinet.** 19" is fixed at 482.6mm, so a
  600mm cabinet leaves roughly 55mm a side and an 800mm one roughly 155mm. In
  the narrow case the vertical cable channel and the device's intake are the
  same 55mm, which is why network cabinets are wide.
- **Neighbours breathing against each other.** A rear-to-front switch directly
  above a rear-exhaust firewall eats its exhaust. The predicate is *opposing
  airflow between vertical neighbours*, NOT position in the rack — "it is in
  the middle" was the first instinct and it is the wrong axis, since a
  side-breather does not care what is above it.
- **Short depth in a deep cabinet.** Hot rear air recirculates around the box
  unless it is blanked. Needs C2's `depth_mm` to be worth computing.

**Risk, not fault.** A side-breather in a 600mm cabinet with clear channels is
fine; it is one tidy-up away from a thermal incident, which is what risk means.

**IT MUST NOT INFER THAT THE CHANNELS ARE FULL.** Cable routing is not modelled
— B4 is deferred — so "48 leads terminate here, therefore the intake is
blocked" is a confident claim about something nobody recorded. The finding names
the risk and sends a person to look.

*A derivation asymmetry worth keeping straight, because it contradicts C2 on
purpose: C2 forbids deriving usable depth from an external datasheet figure,
since nothing pins where the door sits. Side clearance MAY be derived from
cabinet width, because 19" is a standard and equipment width is therefore a
constant rather than an unknown. The test is whether a standard fixes the
missing term, not whether the arithmetic looks similar.*

---

### Group D — Addressing

**WP-D1 · VRFs and route targets** — M — **DONE**
Grouping, lookup scoping and prefix-level uniqueness for overlapping tenant space.

*See the appendix: the "address uniqueness is global" premise does not hold in
this codebase. Worth having on its merits, but it is not a correctness fix and
carries no do-it-early urgency.*

**WP-D2 · Prefix hierarchy, containment, utilisation** — M — **DONE**
Parent/child resolution, depth, utilisation derived from contained prefixes, ranges
and addresses. Search already resolves a CIDR; this makes the result a tree.

**WP-D3 · IP ranges and next-free allocation** — M — **DONE**
Ranges within prefixes, "next available address" query.

**WP-D4 · VLANs and VLAN groups** — M — **DONE**
VLANs, groups scoped to site/rack group/cluster, assignment to prefixes and
interfaces. A VLAN is a reachability domain — give it an edge, not just a record.

**WP-D5 · FHRP groups** — S — **DONE**
Shared virtual IP across devices. Small model, direct payoff: this *is* redundancy,
and the reachability model already reports redundancy lost.

**WP-D6 · ASNs, RIRs, aggregates** — S — **DONE**
Completes the IPAM hierarchy above prefixes. Parity item.

**WP-D7 · L2VPN overlays** — M — **DONE**
Overlay modelling and terminations. Parity item; matters for service providers.

**WP-D8 · L2 domains as reachability edges** — **CLOSED, will not build**
*Decided 2026-08-07. Recorded so it is not reopened.*

The proposal was to make VLAN membership a reachability edge, so the engine could
say two hosts in one broadcast domain are adjacent. It is redundant at best and
misleading at worst:

- The reach model asks whether an asset reaches its declared **anchor**, which is
  a routed question. Hosts in different VLANs routing through a firewall reach
  each other normally — VLANs sit below the resolution the model works at.
- Two hosts in one VLAN are **already** connected in that model, through their
  attachments to the same forwarder group. Adding VLAN edges computes the same
  partitions a second way.
- The one thing VLAN membership uniquely decides is whether the VLAN is actually
  **configured on the trunk it must cross** — and a declared inventory cannot
  know that. Somebody typed the membership; the switch either has it or does not.

That is the boundary of declared data, not a gap in the model. The useful
declared half already ships as WP-I1's structure findings: losing a switch
reports the VLAN it empties and the ones it halves.

*If it ever returns, it returns as an agent reporting real per-port membership,
and the finding is DRIFT against observed state — closer to WP-G6 than to this
group.*

---

### Group E — Circuits and virtualization

**WP-E1a · Providers and circuits — cost and expiry** — S — **DONE**
Provider, circuit (install date, commit rate, contract end), terminations to site
or interface, and a fourth cost surface. Monthly run rate lands natively in the
existing cost model with validity windows; a one-off amortises to the **contract
end**, because a circuit has no end-of-support. Contract end joins the expiry
report — not because anything stops working on the day, but because somebody is
either renegotiating or being auto-renewed at a rate nobody checked.

**WP-E1b · Circuits as a reachability edge** — S — **DONE**
A circuit joining two forwarder groups is a connectivity edge; losing it
partitions the sites it joins, and a single-circuit site reports redundancy lost.

Built as forecast: the edge is DERIVED from the terminations rather than
declared, so nobody has to say the same thing twice and keep two records in
agreement. `/circuits/{id}/impact` answers "the fibre is cut, what goes dark" —
a separate entry point from the asset simulator, because a circuit is not in the
containment tree and cutting it removes an edge rather than a vertex.

*Three outcomes, and conflating any two would be a lie the page tells: it joins
nothing (most circuits end at the provider), it joins and another path survives,
or it joins and the far side is cut off. The impact Result cannot tell the first
from the third when nothing on the far side depends on anything — a partition
with no consumers produces no findings — so the connectivity answer is computed
from the graph and shown beside the service consequences.*

*The estate gained a DR forwarder group to make this demonstrable, which closed
a real hole: dr-bergen had a switch, a firewall and no group at all, so the
reach model did not cover the one site whose entire purpose is to still be
reachable.*

**WP-E2 · Clusters and cluster groups** — M — **DONE**
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

**WP-G1 · Object-level permissions** — L — **DONE**
Three roles — Administrator, Observer, project owner — with write scope
resolved per object rather than per route. Delivered narrower than the
original "this group may edit assets at these sites only" sketch and
deliberately so: scope follows **project membership**, which is a fact the
estate already records, instead of a constraint language nobody has to learn.
Design in `docs/rbac-design.md`, plan in
`docs/superpowers/plans/2026-08-26-rbac.md`.

Enforcement lives in `internal/store/store.go`'s `tx.log`, alongside the
`change_log` insert — see `docs/AUDIT.md`, which is now an authorization
document as well as an audit one. If every account ever loses write access,
the documented way back in is still `docs/RECOVERY.md` (`INV_ADMIN_USERS`
overrides the role column).

Known follow-up, not security-affecting: 132 `.CanWrite` occurrences across
38 templates still render controls to a project owner that the server
correctly refuses. (The misnamed `middleware.RequireAdmin` was renamed to
`RequireWrite`.)

**WP-G2 · Webhooks and event rules** — M
Fire on declared-state change. Outbound HTTP to a *user-configured* endpoint is
consistent with invariant 9 — it delivers notification, not configuration. Keep it
that way: no templated payload that could carry a command.

*This is the one work package that requires editing `dialAllowlist` in
`internal/estate/guard_test.go`. That is deliberate — the guard makes the decision
explicit rather than blocking it. Read the appendix before starting.*

**WP-G3 · Journal entries** — S — **DONE**
Free-text operational notes on entities. Distinct from audit: audit is what
changed, journal is what a human observed. Cheap, and heavily used in practice.

**WP-G4 · Tags, saved filters, table configs** — M — **DONE**
Makes large estates usable. Pure quality of life, and users notice its absence
immediately.

The roadmap bundled three features as one M and they are not one feature —
see `docs/tags-design.md` §0.

- **G4a · Tags — DONE.** Estate-level declared state, applied from an
  entity's own page, filterable, with bulk apply from a filtered view.
- **G4b · Saved filters — DONE.** Per-user, in a table — deliberately unlike
  G4c, and the contrast is the point: a column preference is about one browser,
  but a saved filter is a thing you name and come back to from anywhere, so it
  is account state and the database holds it. Nobody else can read or edit
  another person's views, Administrators included, and scrubbing an account
  deletes them. Design in `docs/saved-views-design.md`.
- **G4c · Table configs — DONE.** Column visibility per table, remembered in
  the browser (`localStorage`), not in the database — the whole design decision
  was that a column preference is a display preference about one browser, so
  `localStorage` holds it and the database never learns it exists. Design in
  `docs/table-configs-design.md`.

**WP-G5 · CSV export** — S — **DONE**
*Renamed 2026-08-12. It was "export templates", meaning the NetBox feature:
user-authored templates stored in the database and rendered per object type.
That is an L and a code-execution surface, and it is not what people reach for
— which is getting a list into a spreadsheet.*

A download on every list that has one, carrying the page's current query so the
file is what was on screen. `?format=csv` on the existing route rather than a
route of its own, because a second route parses the filters a second time and
the day the two readings diverge is the day somebody exports a filtered list and
silently gets everything.

**The asset export uses the importer's own columns**, so a file can be exported,
edited in a spreadsheet and loaded back — which is bulk editing for free. That
holds only while the two agree, so the parent is a PATH rather than a uuid and
`device_type` is `manufacturer_code/model` rather than the display label the row
carries.

*Cells beginning with `=`, `+`, `-` or `@` are prefixed with an apostrophe.
Spreadsheets evaluate those, so an asset named `=cmd|'/c calc'!A1` is code the
moment a colleague opens the file; the database is right to store such a name
and the defusing belongs at the boundary where text becomes a spreadsheet.
Nothing is removed — an export that silently altered a name would be worse than
the problem.*

**WP-G6 · Cloud resource discovery** — L — depends: the agent surface
Inventory of resources at a cloud or hosting provider: instances, volumes,
managed databases, object storage, load balancers, and what they cost.

**ON THE OBSERVED SIDE, AND THAT IS THE WHOLE DESIGN DECISION.** A rented
*server* already models perfectly as a declared asset with a monthly cost and no
acquisition — the demo estate runs development on two Hetzner boxes, staging on
Scaleway and monitoring on OVHcloud, and needed no new model to do it. True cloud
resources are a different shape and declaring them would be wrong:

- They are **discovered, not asserted**. The provider's API is authoritative and
  always current; anything typed here is a stale copy of it, and the copy is
  wrong within a day.
- They are **ephemeral**. An autoscaling group's instances outlive nobody's data
  entry, and an inventory that lists yesterday's instance ids is worse than one
  that lists none.

It also collides with invariant 9 as tested: `TestNothingReachesOutOfThisProcess`
refuses any outbound HTTP capability in this codebase, with a single allowlisted
exception for the LDAP bind authentication needs. A cloud API client inside the
application would break that guard, and the guard is right.

So the shape is the one monitoring already uses: an **external collector** reads
the provider API and posts to the agent endpoint; invctl records the result as
observed state with a reporter and an age, and never reaches out itself. The
valuable output is then **drift**, which this architecture already knows how to
express:

- resources running that nobody declared — shadow IT, or a forgotten test stack
  still being billed;
- declared things the provider no longer has;
- spend that appears in no cost line.

**Do not start this before the agent surface and `unmatched_observation` are
carrying real traffic.** Every part of the value depends on the observed side
working, and building the collector first would produce a second inventory with
nowhere to put its disagreements.

**WP-G7 · Ownership report** — S — **DONE**
`RetireTeam` states the philosophy already: a retired team still owning things
is "how the estate says *this used to be theirs and nobody has picked it up*,
which is a finding; silently nulling the column would erase the question along
with the answer." The gap used to be visible one entity at a time and nothing
answered "what has no owner?" across the estate. `GET /reports/ownership` now
does, with a "no contact" finding and bulk reassignment from the report
itself (`POST /reports/ownership/assign`).

Two distinct findings, deliberately not collapsed: **unowned** (`team_id IS
NULL` — nobody ever said) and **owner retired** (somebody said and the answer
expired). The second is the more actionable, because there is a name to start
from.

Covers every team-owned entity — `asset`, `service`, `project`, `identity`,
`custom_field.owner_team_id` — not one feature's orphans. A custom-field-only
view would be the third bespoke attribution mechanism in a product that
already has two.

Includes the smallest and highest-value piece: **retiring a team warns instead
of blocking.** `RetireTeam` counts nothing today. Showing "this team looks
after 12 assets, 3 services and 2 custom fields" before confirming preserves
the finding while removing the silence. Not forced reassignment — the person
disbanding a team rarely knows who should inherit twelve services, and forcing
a choice there converts a fact about the estate into a guess made under time
pressure.

*Lint-shaped but not the lint engine: the five existing reports set the
precedent that a targeted query surface is a report, not a rule engine.
Open question: whether assignment needs to be bulk, which is a different
transaction and a different audit story — twelve `change_log` rows, not one.*

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
Expiry (type EOL, circuit contract end, with provenance), cost (circuits, power
draw), search (serials, part numbers, circuit IDs, VLAN IDs), project cost
buckets for new cost-bearing entities.

*Audited 2026-08-14, and most of it was already done. Expiry covers device-type
EOL with provenance and circuit contract ends. Search resolves serials, part
numbers, VLAN IDs **and circuit IDs** — a circuit is indexed with its CID as
both title and body. Two real gaps remain: circuits were absent from the project
cost rollup (a wrong number, not a missing feature — fixed by migration 00041),
and there is no `/reports/cost` page at all.*

*The entry used to say "metered power draw". **Nothing meters anything** — this
system never touches the estate, and the form says so: "Draw (VA) — nameplate or
allocated. Nothing measures this." Decided 2026-08-14: power cost is an
**estimate**, declared nameplate draw times a tariff, and it must be labelled as
one everywhere it appears. It is useful for the comparison the estate actually
makes — keep this platform or move to another — and it must never be mistaken
for a reading. A figure that looks measured and is not is worse than no figure.*

**WP-I3 · Performance** — M — **DONE (first pass)**
Built the fixture the entry asked for — 4,000 assets, 10,000 prefixes, 50,000
addresses — and measured before changing anything. Two real findings, one of
them a bug.

**Measured, on a 12th-gen i5, SQLite:**

| | before | after |
|---|---|---|
| Prefix tree (the IPAM page) | 711ms | **181ms** |
| Estate findings (the overview) | 16ms | 16ms |
| Estate fit sweep | 13ms | 13ms |
| Simulate losing a rack | 4.9ms | 4.9ms |
| Search by address | 1.8ms | 1.8ms |
| Asset list | 1.5ms | 1.5ms |

**The bug: `ListPrefixTree` was quadratic.** It rescanned every node for every
node to find its children — a hundred million UUID comparisons at ten thousand
prefixes — directly beneath a comment congratulating the line above it for not
being quadratic. Indexing the children in one pass took the page from 711ms to
181ms, and `TestPrefixTreeDoesNotGoQuadratic` now guards it by comparing the
growth ratio at two sizes rather than by asserting a wall-clock number.

**The test suite was replaying 40 migrations, hundreds of times.** Opening and
migrating one SQLite database costs 295ms and this package has 306 test
functions; the SQLite half of the store suite was 98s of almost pure migration.
It now copies a template file that is migrated once per process — identical
isolation, 98s → **11s**, and the whole suite 303s → 218s locally.

*What is left, and deliberately not done: the remaining 181ms splits 74ms
correlated subquery / 60ms allocation spans / ~51ms assembly, with no single
culprit — further gains need pagination or an in-memory address index, both of
which are design changes rather than fixes. And the Postgres half of the suite
is now ~95% of its runtime: PostgreSQL has no `CREATE SCHEMA ... TEMPLATE`, so
the same trick needs per-test databases instead of per-test schemas, which is a
harness rewrite rather than an optimisation.*

*One UI question this surfaced rather than answered: the prefix page computes
next-free for every prefix in the estate and renders all of them. At ten
thousand that is a large answer to a question nobody asked — pagination is a
UX decision, not a performance one.*

---

### Group J — Money

*Added 2026-08-14, from the first adopting company. The full specification is
**`docs/COST-ATTRIBUTION.md`**; these are the packages, not the reasoning.*

**WP-J1 · Replacement lineage** — S — **DONE**
One nullable self-reference on `asset` — *this replaces that* — and the page that
compares them. Both boxes already carry their own cost history, so the comparison
is a join. Soft delete is why the predecessor is still there to compare against.

**WP-J2 · Price movement** — S — **DONE**
A view over cost lines already stored: what it cost, what it costs, when it
changed, by how much. **No schema change** — the validity windows on `cost` have
been recording this since the first release. Against a hand-maintained inflation
series it answers "did this rise faster than money fell".

**WP-J3 · Capacity model** — L — **DONE**
Hosts get cores, memory and storage; storage kinds get a raw-to-usable ratio
(Ceph 3×, RAID6, local 1:1); clusters get a declared safe overcommit ratio. A
workload carries **provisioned** (the hard limit) and **soft-allocated** (what
money is computed on), and a project carries **priced-for** (the resource
assumption its quote was built on). Deliberately not "contracted": these
contracts specify no resources at all, and a field named for a promise nobody
made will one day be quoted as though it were one. See
`docs/COST-ATTRIBUTION.md` §5.5. The estate models rack
units and weight but nothing about compute, and this is the prerequisite for
every figure in J4. Useful before any money is involved: it answers *"is this
cluster oversubscribed?"*.

**WP-J7 · Capacity findings** — M — **DONE**
Three findings, three audiences. A project allocated **above what it was priced
for** is the CEO's alert: nobody is in breach, the engagement has simply grown
past its own quote and the margin is eroding quietly. Provisioned above the safe
overcommit ratio is operational. Priced-for totals above physical capacity is
planning -- more work sold than the estate can host. The last is invisible on
every utilisation dashboard: a cluster at 35% CPU and 65% memory looks healthy
and says nothing about what could be claimed at once. Needs no money and can
ship before J4.

Closing it also closed a gap J3 had left open: the columns, the arithmetic and
the capacity panel all shipped, and **no form anywhere set a single one of the
numbers**. Every capacity figure could only come from the seed. Sizing a host,
recording a workload's allocation, declaring an overcommit ratio and stating
what an engagement was priced on are now all editable, each with the audit
entry that declared state owes. Two of the three seeded findings appear on the
demo estate; the two that need an estate genuinely in trouble are asserted in
`internal/store/capacity_findings_test.go` rather than forced into the fixture,
following the precedent `seed_engine.go` set for cluster relocation.

**WP-J4 · Cost attribution** — L — **DONE**
Cluster cost divided by *usable* capacity gives a price per unit; the redundancy
premium falls out of `cluster.min_hosts` rather than a hand-kept multiplier.
**Not every cost divides across everything**: a per-core OS licence granting
unlimited guests benefits only the guests running it, and spreading it evenly
makes every other workload subsidise them. A cost line therefore declares which
consumers it applies to (`COST-ATTRIBUTION.md` §5.6). There is also no single
"project share" — a worked example against a real estate produced 5.2% of CPU,
6.25% of memory, 3.4% of block storage and 1.0% of bulk storage for one project,
and a blended figure would have been invented (§5.7).
Slices per project, **summing to 100%**, with idle capacity shown as its own
slice rather than dropped. Allocation is the basis, not usage — see
`DECISIONS.md`.

**What shipped: the shares.** Every dimension divides on its own — the demo
estate puts `platform` at 12.5% of CPU, 15.63% of memory, 8.79% of the block
pool and 5.63% of bulk, which is §5.7's four-different-percentages lesson on
real data, memory binding hardest. Basis points by largest remainder so the
slices sum to exactly 100%, idle capacity as its own slice, workloads no
project owns gathered under one subject rather than dropped, and every division
stamped with its basis. Ownership resolves upwards through containment to the
NEAREST owning ancestor, never through `uses`.

**And the money.** A cost line declares who benefits (§5.6, migration `00047`):
**universal** divides across the whole capacity, **conditional** across the
named guests in proportion to what they hold, **per-consumer** across them
equally per head — because a per-VM backup licence costs the same for a 64 GB
machine as for a 2 GB one, and dividing it by capacity would charge the large
one many times over while reconciling perfectly. A scoped line naming nobody is
reported, never spread: the fallback would be a default wearing a declaration's
clothes.

**One invoice buys cores and memory, and the specification did not say how to
separate them.** §5.1 asserts a per-core and a per-GB price both fall out of
"cluster cost ÷ usable capacity", which is true of storage — a pool is its own
asset with its own invoice — and silently under-specified for compute. The
answer taken is a **declared split per cluster** (migration `00048`): one
number, audited like every other decision here. An undeclared split divides no
money at all and says so, because unlike the overcommit ratio there is no
conservative direction — half and half is not cautious, it is arbitrary.

Run rate and amortised capital stay apart the whole way through, per
`cost.go`'s rule that folding a one-off into a monthly figure is a lie.

**WP-J5 · Shared occupancy** — M — **DONE**
For estates that pack several tenants into one VM to save on licensing: occupants
with a declared percentage each, never inferred. Percentages that do not total
100 are a finding.

**The case ownership cannot describe.** At most one project owns an asset, so
without this the whole of a shared box's capacity and cost lands on its owner —
not an approximation but a wrong answer given confidently, for exactly the
estates that pack tenants together to save on licensing. Occupancy changes only
how a machine DIVIDES; it does not replace ownership, because a shared box still
has somebody answerable for it.

Percentages are whole numbers, because nobody defends a tenant's share to two
decimal places and offering the precision would invite an argument the figure
cannot support. A total that is not 100 is reported and the remainder attributed
to nobody: normalising 90 up to 100 would inflate every declared share by a ninth
and leave nothing on any page to notice. The demo estate declares one machine at
90% on purpose, so the discipline problem is visible and not only the arithmetic.

**WP-J6 · Supplier as a dimension** — M — **DONE**
Answers "which suppliers raise prices beyond inflation" across the estate rather
than one item at a time.

**The wording above was wrong and the code does not follow it.** It said to
promote `asset.vendor` to a real reference, which attributes a support contract
to whoever MADE the box. One server routinely carries hardware from a reseller,
support from the manufacturer and a licence from a third party — three suppliers,
three price histories, one asset. So the reference went on the **cost line**
(migration `00050`), where the invoice actually is.

`provider` was reused rather than a third organisation table added. It already
carried `account_ref` and `portal_url` — precisely what you need to ring a
supplier about a rise — and its meaning is widened from "telco" to "anybody who
invoices us". A telco IS a supplier, and modelling it twice would mean "which
supplier" had to union two tables to be answered honestly.

Movement is **weighted by what each line costs**: a €40 line up 50% beside a
€4,000 line up 2% is a 2% rise, not 26%. And a series whose supplier changed
mid-history is counted separately and excluded, because a price that moved when
the reseller changed is a switch rather than a supplier raising its price.

**`asset.vendor` is untouched and still free text.** It answers a different
question — who made or sold this box — and it still holds a mix of
manufacturers, hosting companies and noise. Tidying it is its own job and is not
pretended to here.

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
J1 lineage ──► J2 price movement ──► J6 supplier dimension
J3 capacity ──┬──► J7 capacity findings   (no money needed)
              └──┐
I2 reports ──────┴──► J4 attribution ──► J5 shared occupancy
```

Anything with no inbound arrow can start immediately and in parallel: **A1, A2,
A3, A4, B2, B5, D1, D4, E1, G1–G5, J1, J3.**

**J4 does not depend on G1**, though an earlier draft claimed it did. That draft
assumed clients sign in. They do not: one company owns the estate, every user is
its employee, and a project is an internal bucket — the work done for a client,
or one of the company's own products. Attribution rearranges figures its readers
are already entitled to. See `docs/COST-ATTRIBUTION.md` §9.

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
| Custom fields, tags, export templates | A4 — **DONE**, G4, G5 |
| Object-level permissions | G1 |
| Webhooks and event rules | G2 |
| Journal entries | G3 |
| REST API | A2 — **DONE** (read-only by design — deliberate divergence) |
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

**WP-E1 bundles two different sizes.** The cost and expiry half is small and
**shipped** (WP-E1a). The reachability half has not.

*Corrected 2026-08-07, and the original note sent two people down the wrong
path.* It said a circuit joins *sites*, the reach model has no site concept, and
therefore there is no edge to extend. The first two clauses are true and the
conclusion does not follow.

`computePartitions` resolves connectivity with **union-find over uplink edges**
(`uf.union(e.GroupID, e.UpstreamGroupID)`). So the reach model is not a
hierarchy — for partitioning it is an undirected graph of forwarder groups. A
circuit whose two terminations land on interfaces of assets in **different
groups** already *is* such an edge:

```
circuit → terminations → interfaces → assets → the groups they attach to
        → one connectivity edge, exactly like an uplink
```

No site model, no new hierarchy, no change to the walk. **WP-E1b is deriving
that edge**, and the existing machinery does the rest.

Three cases to decide rather than discover, which is why it is a day and not an
hour:

- **Most circuits do not join two of your sites.** A DIA circuit ends at the
  provider — one end inside the estate, one outside — and correctly produces no
  internal edge. The feature fires on site-to-site circuits only, a smaller set
  than "all circuits".
- **A termination may land on a site rather than an interface.** The schema
  allows both, and a site-terminated circuit has no interface and therefore no
  group. Skip them and say so, or resolve the site's assets' groups — which is
  more work and fuzzier. Recommendation: skip.
- **A circuit with one end recorded** produces nothing, which the overview
  already reports as a gap. Consistent; no extra work.

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
