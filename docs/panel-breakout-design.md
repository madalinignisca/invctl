<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Panel breakout — design

**Status: DELIVERED 2026-09-05**, survived two challenge rounds and built
per `docs/superpowers/plans/2026-09-05-panel-breakout.md` (Tasks 1-9; Task 10,
E2E, is a separate scope decision -- see that plan). This is the first half of
WP-B4; breakout cables and bundles (§5) are not built. The roadmap entry read:

> **WP-B4 · Cable profiles and bundles** — L — depends: B3
> Breakout and multi-strand lane mapping (4×10 GE ↔ 40 GE), and bundles
> representing runs managed as a unit. This is parity with NetBox 4.5/4.6 and
> it is genuinely hard — the profile changes what "connected" means in the
> tracer.

**That entry is two independent features and this document does one of them.**
§5 says why, and what the other two are.

## 1. What this is for

A twelve-fibre MPO trunk lands on a panel's rear port and breaks out to twelve
LC front ports. That is ordinary cabling, and today invctl cannot express it:
the tracer follows exactly one continuation per interface, so eleven of those
twelve fibres are invisible.

The question it must answer is the one a person asks in front of a rack: *this
trunk arrives here — where does each strand actually go?*

## 2. The hard parts

### 2.1 The tracer holds ONE successor per interface

`internal/store/cabling.go`'s `plant` is three maps, and two of them are the
problem:

```go
type plant struct {
	// cable maps an interface to the interface at the other end of its link.
	cable map[string]cableEnd
	// through maps an interface to its opposite side of a panel.
	through map[string]string
	iface   map[string]ifaceInfo
}
```

`map[string]string` is not a shape that can be widened by adding a field. One
rear port continues into *n* front ports, so `through` becomes one-to-many and
the walk — currently a single-successor loop — has to branch.

**`position` is already in the schema and the tracer already ignores it.**
Migration `00028_pass_through.sql` created it for exactly this work and said so:

> "POSITION IS HERE FOR A FEATURE THAT DOES NOT EXIST YET, which usually earns
> a column its removal. It stays because the shape it anticipates is not
> speculative: a twelve-fibre MPO trunk breaking out to twelve LC front ports
> is ordinary, and it is WP-B4."

and its unique index carries the rule already:

> "And a given position on a rear port takes exactly one front port. With the
> default position of 1 this is 'one front port per rear port'; when B4 arrives
> it is already the rule breakout needs."

`loadPlant`'s `SELECT` reads `front_interface_id, rear_interface_id` and **not
`position`**. Nothing in the codebase reads that column today. **So this work
needs no migration and no new table.** That is the whole reason to do this half
first.

### 2.2 The trace becomes a TREE, and it is asymmetric

Tracing *up* from one LC front port has a single answer: one strand, one far
end. Tracing *down* from the MPO rear port has twelve.

**Decision (D1): `TracePath` returns a tree, always.** A 1:1 run is a tree with
one branch, so every existing run renders unchanged in content. What changes is
the *type*, and therefore the trace page and the tests that assert on a flat
hop list.

The rejected alternative was returning a path or a tree depending on which end
you started from. It matches the physics exactly and it is the kind of API that
reads fine and gets misused: one function, two shapes, every caller obliged to
handle both. A tree with one branch costs a render loop and never surprises
anybody.

### 2.3 `visited` means something different in a tree, and getting this wrong is silent

The current walk is bounded twice, and the file explains why with unusual care:

> "`visited` does more than its name suggests, which mutation testing made
> obvious: without it the walk does not merely permit loops, it bounces straight
> back down the cable it arrived on, because that cable is still there and
> nothing says it has been used. It is what makes the walk directional at all,
> and only secondarily what stops two panels patched into each other from
> running for ever."

**For a path, "already seen anywhere in this walk" and "already seen on the way
here" are the same set. For a tree they are not.** A global `visited` shared
across branches would let strand 1 consume a node and silently truncate strand
7 — reporting a *shorter* trace with no error, which is the worst failure shape
available here.

**`visited` becomes per-branch: the set of interfaces on the path from the root
to the node being expanded.** That is also the correct definition of a cycle,
which the path case only got right by coincidence of having one branch.

**The concrete case, because asserting this is not enough.** Two strands of one
trunk land on two front ports of a second panel, and that panel's rear port is
cabled onward:

```
                    ┌─ pos 1 → panel-b/f1 ─┐
switch ═ panel-a/rear                       ├─ panel-b/rear ═ core
                    └─ pos 7 → panel-b/f7 ─┘
```

Both strands legitimately reach `panel-b/rear`. With a global `visited`, strand
1 marks it, and **strand 7 stops one hop short with no error** — it reports a
run ending at `panel-b/f7` rather than at `core`, and nothing distinguishes that
from a genuinely unpatched port. With a per-branch set each strand carries its
own ancestry, both reach `core`, and a strand that truly loops back onto its own
path still terminates.

**The plausible wrong turn is to conclude no set is needed at all**, on the
reasoning that branches start from distinct front ports and are therefore
naturally independent. They are not: they converge, as above. And within one
branch the set is still what makes the walk directional, which is the job the
file's own comment says it is really doing.

### 2.4 A fan-out needs a budget the path case never needed

`traceHopLimit = 64` bounds depth. Depth is no longer the only way a walk gets
large: a 12-way break-out into panels that themselves break out is 144 leaves at
depth two. The hop limit does not see that.

**A total-node budget bounds the tree, separately from the per-branch depth
limit.** Both are needed and they answer different questions — one is "this run
is absurdly long", the other is "this plant fans out faster than anyone wants to
read". Exceeding either terminates that branch with a reason, exactly as the
hop limit does now, rather than truncating silently.

**`traceNodeBudget = 512`**, and the reasoning matters as much as the number,
the way `traceHopLimit = 64`'s does. A twelve-fibre MPO trunk is the motivating
case; one nested inside another is 144 nodes; 512 is comfortably past anything
that exists in a rack and still small enough that a malformed plant renders a
bounded page rather than hanging a browser. It is a guard against a plant being
edited halfway through, not a capacity limit anyone should meet.

## 3. Decisions

### D1. The result is a tree, always — **decided**

See §2.2.

### D2. Scope is PANEL breakout only — **decided**

Not breakout *cables*. See §5.

### D3. `Complete` becomes a property of a LEAF, not of the trace

Today `TracePath` returns one `Complete` bool and one `Why`. With twelve
strands, three patched and nine free, neither a single `true` nor a single
`false` is true.

**Every leaf carries its own outcome and its own reason** — "the path ends
here", "nothing is plugged into this port", "the path loops back on itself".
The trace as a whole reports counts, not a verdict. A summary bool over a tree
is precisely the "figure that looks more certain than it is" this codebase
refuses elsewhere.

### D4. Only RECORDED positions appear — **corrected 2026-09-05**

The first draft said a twelve-position trunk with three strands patched must
render twelve leaves, nine of them saying nothing is patched there, because
"which of these strands is free" is what somebody in front of the rack is
asking.

**That is unbuildable, and the challenge round found it.** `port_pass_through`
holds a row per *patched* position. Nothing anywhere records how many positions
a rear port physically has — there is no strand count on `interface`, and
adding one is a migration, which D2 exists to avoid. So the nine free strands
are not merely unqueried; **the database does not know they exist.**

The draft reasoned correctly that free strands are what an operator wants, and
never asked whether anything records them. That is the same one-step-short
failure this project's last two specs made, and it is worth leaving in the
document rather than quietly correcting.

**The trace shows the positions that have rows, in position order, and says how
many it found.** It must not imply a total. "Three strands are patched here" is
true; "nine are free" is a claim about a trunk nobody described.

A declared strand count is a reasonable future feature — it would make capacity
answerable — and it is a migration plus a form field plus a coverage problem,
which is its own work package.

### D5. Position is not renumbered, reused, or inferred

`position` is declared by whoever recorded the patch. Nothing derives it, and
nothing renumbers it when a neighbour is retired: strand 7 stays strand 7 when
strand 6 is unpatched, because it is a physical fact about which hole the fibre
is in, not an ordinal in a list.

## 3b. The result type, and the successor rule

Both were missing from the first draft and both block implementation. The
challenge round could not tell whether the tree was a recursive node or a flat
list of branches, and could not tell what a walk starting at a rear port does
first.

### The type is a recursive node

```go
// TraceNode is one step of a run. Children are its continuations: none for a
// leaf, one for an ordinary hop, several where a rear port breaks out.
type TraceNode struct {
	// Hop is how the run arrived HERE. Zero on the root, which is the port
	// the caller asked about.
	Hop TraceHop
	// Position is which strand of the parent rear port this node came through,
	// and is 0 for every node that is not the far side of a breakout. It is
	// port_pass_through.position, never an index into Children -- see D5.
	Position int
	Children []*TraceNode
	// Outcome and Why are set on a LEAF and empty on an interior node (D3).
	Outcome string
	Why     string
}
```

**Recursive, not a flat list of branches**, because breakouts nest: a strand of
a twelve-way trunk can land on another panel that breaks out again. A flat list
would have to repeat the shared prefix once per leaf, and the repetition is
exactly the sort of thing that drifts.

A 1:1 run is a chain of single-child nodes — which is why the existing content
survives unchanged, and why the page's existing rendering is a loop over one
branch rather than new machinery.

### The successor rule is UNCHANGED: cable first, then pass-through

The walk already tries a cable before a panel, guarded by `previous` so it does
not bounce back down the cable it arrived on:

> "A cable first: leaving the box is the interesting move."

**That rule does not change. The only difference is that the pass-through step
may now yield more than one successor.** So a trace starting at a rear port with
a trunk plugged into it follows that trunk first, exactly as it does today —
starting mid-run and walking one way is existing behaviour, and
`TestATraceRunsBothWays` already covers the ends.

This is stated because "tracing down from the MPO port has twelve answers" reads
as though the walk acquires a notion of direction. It does not. It acquires
nothing but a branching pass-through step.

## 4. What gets built

1. **`loadPlant` reads `position`**, and `through` becomes one-to-many —
   `map[string][]passThroughEnd`, ordered by position so the tree renders in
   the order the strands are physically numbered rather than in map order.
2. **The walk branches**, with per-branch `visited` (§2.3) and a total-node
   budget beside the existing per-branch hop limit (§2.4).
3. **A tree result type**, replacing the flat hop list. Every leaf carries its
   own outcome and reason (D3); only recorded positions appear, and nothing
   implies a total (D4 as corrected).
4. **The trace page renders the tree**, with each branch labelled by its
   position. **The 1:1 requirement is about the structured result, not the
   rendered HTML**: a 1:1 run yields a single chain whose hops, order, kinds and
   reasons equal today's flat list exactly. That is what the tests assert. The
   page is then expected to look the same because it is rendering the same
   data, but no test pins pixels or markup — the first draft said "same hops,
   same wording", which reads as a rendering requirement and is not one.
5. **Tests.** The existing trace tests change shape rather than meaning, and
   that is the risk this work carries: a test rewritten while its subject
   changes can be rewritten into agreement. Each must keep asserting what it
   asserts today.
   - the 1:1 fixture (`switch ── panel-a ═ panel-b ── server`) still reports
     three cables and two panels, now as a single-branch tree — the direct
     descendant of `TestATraceCrossesThePanelsInTheWay`
   - a 4-position rear port yields **four** leaves, one per position
   - a rear port with three recorded positions yields **three** leaves and
     claims nothing about a fourth — the corrected D4. Nothing records how many
     positions a trunk has, so a test asserting "nine unpatched" could only pass
     by inventing a strand count
   - a branch that loops terminates **without** truncating its siblings — the
     per-branch `visited` guard, and the one a global set would silently break
   - a fan-out exceeding the node budget says so, and says it per branch
   - tracing up from one front port returns a single-branch tree reaching the
     rear port's far side
   - `TestAMisPatchedPanelTerminatesRatherThanLooping` still terminates, still
     under the hop limit
6. **`PassThroughsFor` orders by rear port, then position, then name.** It
   currently orders by front-port name (`cabling.go:294`), which puts strand 10
   before strand 2 the moment positions mean anything. **Rear port first**: a
   panel has several, and ordering by position alone interleaves two trunks into
   one unreadable list. The panel's editing view is where somebody reads these
   off against the physical trunk, one trunk at a time.
7. **The patch form gains a position field.** `asset_detail.html` has none, so
   every pass-through a user can create is position 1 and the rear-port unique
   index refuses the second. **Shipping the tracer without this renders a
   breakout nobody can record** — the same shape as the release that shipped a
   404 on a button with four green checks, and the reason E2E exists here.
8. **The seed grows a breakout.** There are no `port_pass_through` rows in the
   seed at all today, so the demo estate cannot show a trace through a panel,
   let alone through a breakout, and E2E has nothing to point at.
9. **`docs/AUDIT.md` and `internal/domain/classification.go`** need no change:
   no column is added. `classification.go:519-526`'s note that `position` "is 1
   for every 1:1 panel, which is all of them until then" becomes false on
   delivery and must be updated to say the tracer now reads it.

## 5. What this explicitly does not do

- **Breakout CABLES.** A QSFP-to-4×SFP+ DAC with no panel in the middle is one
  cable with one end on one side and four on the other. `link` has exactly two
  foreign-key columns and cannot express it. Fixing that means either a
  termination table (following `circuit_termination`'s parent-plus-sides
  precedent) or *n* `link` rows sharing a profile id — a schema change and a
  second cable model beside `link`, which migration 00028 considered and
  rejected once already for pass-throughs:

  > "The alternative was NetBox's shape: front_port and rear_port tables, and a
  > cable whose ends point at 'some kind of port'. That needs a polymorphic
  > reference — a type column plus an id — which is the one join shape this
  > codebase has avoided everywhere, and it would put a second cable model
  > beside `link` for the two to disagree over."

  That decision deserves its own document, not a paragraph in this one.
- **Bundles** — "runs managed as a unit". Independent of breakout, and well
  served by the existing parent-plus-member precedent (`cluster_member` and
  five siblings, with the wholesale-replace-and-fold-the-audit rule already
  established).

  **Why that independence is real rather than convenient**, since the challenge
  round rightly asked: a bundle groups `link` ROWS — the cables somebody pulled
  together and will replace together — and a trace is a *derived* walk over
  those rows. Bundling is a fact somebody declares about cables; a trace is a
  question asked of them. Whether a run through a bundled cable is a chain or a
  tree changes nothing about which cables are in the bundle, and a bundle
  membership table has nothing a tree could disagree with. It touches the tracer
  not at all.
- **Nothing about `Path` or `Neighbourhood`.** They are asset-level walkers with
  their own definitions of connected; `dataPlaneAdjacency` does not join
  `port_pass_through` at all, so a panel is already invisible to them and stays
  so. This work does not change what a *route* is.
- **Nothing about the impact engine.** `link` is deliberately not a reachability
  edge, recorded with its reason in `graph_coverage_test.go`, and this does not
  reopen that.
- **No lane-level speed or media semantics.** Nothing branches on `form_factor`
  or reads `speed_mbps` for a decision today, and this work does not start.
  A position is a hole in a panel, not a claim about what runs through it.

## 6. For the challenge round

Attack these specifically:

1. **Is a tree actually right**, or does an operator want twelve separate
   traces? D1 rejected the per-lane alternative; say if that was wrong.
2. **Per-branch `visited` (§2.3)** — is that the correct cycle definition here,
   and does it reintroduce anything the global set was quietly preventing? The
   file's own comment says `visited` is what makes the walk *directional*.
   Per-branch preserves that within a branch; confirm it does.
3. **The node budget (§2.4)** — is a second bound warranted, or does the hop
   limit already cover the realistic cases and this is a bound nobody hits?
4. **D3's removal of a single `Complete`** — does anything depend on that bool
   in a way this breaks?
5. **The 1:1 regression risk (§4.4)**: is "renders exactly as today" testable,
   or does it need a golden output?
6. Anything B3 got right that this would undo.

## 7. Noted, not addressed here

**B3's engine half was never delivered.** Its roadmap line promises "a cable or
panel becomes a failure target; partitioned findings gain the specific hop
responsible", and `impact.Request` carries only `DownAssetIDs` and
`CutCircuitIDs`. A cable failing concludes nothing today. That is a gap in B3,
not something this work introduces, and it should be recorded as such rather
than absorbed silently into B4.
