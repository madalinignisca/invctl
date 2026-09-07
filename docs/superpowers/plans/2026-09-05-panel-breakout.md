# WP-B4 (first half) Panel breakout — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A twelve-fibre MPO trunk lands on a panel's rear port and breaks out to twelve LC front ports. Make `TracePath` follow *every* strand instead of one, and answer the question somebody asks in front of a rack: *this trunk arrives here — where does each strand actually go?*

**Spec:** `docs/panel-breakout-design.md` (DRAFT 2026-09-05, survived two challenge rounds). §3b is the result type and the successor rule, verbatim. D1–D5 are decided. Nothing here re-opens them; the three places this plan asks a question are collected under "Decisions this plan cannot take" below, and two of them block a task.

**Architecture:** No migration and no new table — `port_pass_through.position` already exists with the exact partial unique index breakout needs (verified below). Three changes in one file: `plant.through` widens to one-to-many, the walk becomes a recursive expansion producing a tree, and the walk is split from the I/O so the bounds can be tested on a plant built in memory. The template renders a flattened tree; the handler does not change at all.

**Tech stack:** Go (`go.mod` toolchain), `jmoiron/sqlx` hand-written SQL, `html/template` + HTMX. No new dependency. No migration.

---

## Migration check — confirmed, not assumed

Read before Task 1, so nobody re-derives it:

- `internal/store/migrations/sqlite/00028_pass_through.sql:39-69` and `internal/store/migrations/postgres/00028_pass_through.sql` are **byte-identical in DDL**: `position INTEGER NOT NULL DEFAULT 1 CHECK (position > 0)`, a partial unique index on `(front_interface_id) WHERE lifecycle <> 'retired'`, and a partial unique index on `(rear_interface_id, position) WHERE lifecycle <> 'retired'`.
- That second index *is* the breakout rule: one front port per (rear port, position), many positions per rear port. Nothing needs relaxing.
- `internal/domain/patch.go:36-58` already accepts and defaults `Position` (0 → 1).
- `internal/web/handlers/cabling.go:40` already reads `position` off the form.
- `internal/domain/classification.go:522-526` already lists `position` as a declared column of `port_pass_through`.
- The **only** thing missing is that `loadPlant`'s `SELECT` (`internal/store/cabling.go:155-157`) does not read the column, and `plant.through` (`cabling.go:83`) could not hold it if it did.

**So: no migration, no new column, no `docs/AUDIT.md` change, no classification-table change.** Two prose comments become false on delivery and are fixed in Task 9.

---

## Global Constraints

These are the ones this feature can get wrong quietly.

- **The result type is the recursive `TraceNode` from spec §3b, verbatim.** Not a flat list of branches, not a list of paths. Breakouts nest; a flat list repeats the shared prefix once per leaf and the repetition drifts.
- **`visited` is PER BRANCH — the interfaces on the path from the root to the node being expanded.** This is the single most important correctness property in this work package **and it fails silently**: with a global set, strand 1 consumes `panel-b/rear` and strand 7 reports a run ending one hop short, indistinguishable from a genuinely unpatched port. Spec §2.3 constructs that case and Task 4's first test is it. The plausible wrong turn is concluding no set is needed at all, because branches start from distinct front ports — they do, and then they *converge*.
- **`visited` is also what makes the walk directional**, which `cabling.go:183-194` says in its own words. Per-branch preserves that *within* a branch: the parent is still in the set when the child is expanded, so the walk still cannot bounce back down the cable it arrived on. That whole comment block must survive into the new code, extended rather than replaced.
- **The successor rule is UNCHANGED: cable first, then pass-through.** Only the pass-through step may now yield more than one successor. The walk gains no notion of direction. A trace starting at a rear port with a trunk plugged in follows that trunk first, exactly as today.
- **Two bounds, both per their own question.** `traceHopLimit = 64` stays, per branch, with its existing comment intact. `traceNodeBudget = 512` is new and bounds the whole tree. Exceeding either terminates *that branch* with a reason on the leaf — **never a silent truncation, and never a dropped successor**.
- **Only RECORDED positions appear (D4 as corrected 2026-09-05).** `port_pass_through` holds a row per *patched* position and **nothing anywhere records how many positions a rear port physically has**. "Three strands are patched here" is true; "nine are free" is a claim about a trunk nobody described. No code, comment, template or test may state or imply a total. See "Decisions this plan cannot take" §A — spec §4 still contains two stale bullets that say the opposite.
- **`Position` is `port_pass_through.position`, verbatim, never an index into `Children`, never renumbered, never inferred (D5).** Strand 7 stays strand 7 when strand 6 is unpatched.
- **Every query runs unmodified on SQLite AND PostgreSQL.** `?` placeholders only, through `s.read`, which rebinds. No `$1`. The one new SQL clause in this work package is an `ORDER BY` on two plain columns — no dialect feature is available to get wrong, and that is deliberate.
- **Nothing here mutates declared state.** The trace is a read. No `change_log` obligation, no `domain.Permit` parameter, no observed state. Task 7 (`PassThroughsFor` ordering) is also read-only. Task 8, if taken, changes a *form*, not a write path — `CreatePassThrough` already logs.
- **invctl never acts on the estate.** This shows a run. It does not verify one.
- Every new file opens with the AGPL-3.0-only notice; in Go, a blank line before `package` (`internal/license` fails otherwise). Three new files are planned: one Go test file, and nothing else unless Task 10 is taken.
- **Gates:** `make lint` and `make test`, foreground, one at a time, exit status read directly. Never pipe either through `tail`/`head`/`grep`. `go test ./...` on its own is NOT the gate — with `INV_TEST_POSTGRES_DSN` unset the Postgres half is silently skipped.
- **Suite time is a real constraint here.** `internal/store/engines_test.go:57-70` records a suite that crept to 586s and failed a release tag on Go's ten-minute timeout. The node-budget fixture needs >512 pass-throughs; inserting those through `CreatePassThrough` twice (once per engine) is ~1000 write transactions for a bound that is pure Go. Task 2 exists to make that test in-memory and instant. **Do not build the budget fixture in the database.**

---

## Decisions this plan cannot take

Three items. Two block a task; take them back to the main conversation before starting that task, not during it.

### A. Spec §4's bullets 3 and 5 contradict corrected D4 — non-blocking, but fix the document

`docs/panel-breakout-design.md:270` ("unpatched positions are leaves (D4)") and `:286` ("three patched of twelve yields twelve leaves, nine of them unpatched (D4)") are **survivors of the first draft**, which D4's correction on 2026-09-05 (`:176-200`) explicitly overturned: the database does not know the nine free strands exist. The task brief resolves it — corrected D4 wins — and this plan implements corrected D4 throughout. **Task 9 amends those two bullets in the design document** so the stale version cannot be re-implemented later by someone reading §4 as the build list. That is a documentation edit to a spec that was approved, so it wants a nod, but it does not block code.

### B. `PassThroughsFor` ordering: literal §4.6, or grouped by rear port? — blocks Task 7 only

§4.6 says "orders by position, then name". Taken literally that is `ORDER BY p.position, f.name`, which on a panel with several rear ports **interleaves the trunks**: strand 1 of every trunk, then strand 2 of every trunk. The reason §4.6 gives — "the panel's own editing view is where somebody reads them off against the physical trunk" — argues for grouping by rear port first: `ORDER BY r.name, p.position, f.name`. This plan writes the grouped version, flagged, because it serves the stated purpose; reverting to the literal one is a one-line change if that is the call. (Note either way that `r.name` sorts `rear-10` before `rear-2`. That is the existing defect one level up, panels have few rear ports, and fixing text-sorted port names is not this work package.)

### C. The patch form has no `position` field, so breakout is unreachable through the UI — blocks Task 8, and Task 10 depends on it

`web/templates/pages/asset_detail.html:886-906` posts only `front_interface_id` and `rear_interface_id`. The handler (`internal/web/handlers/cabling.go:40`) already reads `position` and defaults it to 1, so **every patch a user can create today is position 1**, and the rear-port unique index then refuses a second one. Ship Tasks 1–6 alone and the tracer can render a breakout that no user can record: reachable only via seed or SQL. That is the same shape as this project's "404 on a button" release — every layer correct, nothing able to reach it.

Spec §4 does not list a form change, so this plan does **not** assume it. Task 8 is written and marked DECISION REQUIRED. It is one `<input type="number">`, one label and one sentence of help text against a handler that already parses it.

---

## Task 1: `loadPlant` reads `position`, and `through` becomes one-to-many

The map widens. The walk does not branch yet, the result type does not change, and **the seven existing tests are not touched**.

**Files:**
- Modify: `internal/store/cabling.go` (`plant` 78-85, `loadPlant` patches block 151-163, walk 229-243 and 254-256)
- Modify: `internal/store/cabling_test.go` (add `mustPatchAt`, refactor `mustPatch` 53-65)
- Create: nothing

**Interfaces:**
- Produces: `type passThroughEnd struct { other string; position int; fromRear bool }` and `plant.through map[string][]passThroughEnd`, ordered by position. Tasks 2, 3 and 4 consume it.
- Produces: `func mustPatchAt(t *testing.T, s *SQLStore, ctx context.Context, front, rear string, position int) string` for Tasks 4 and 7.
- Consumes: `SQLStore.read`, `domain.LifecycleRetired` — unchanged.

- [ ] **Step 1: The far side of a pass-through gains a position and a side**

Add above `plant`:

```go
// passThroughEnd is the far side of one pass-through row, seen from whichever
// end the walk is standing on.
type passThroughEnd struct {
	// other is the interface on the panel's opposite side.
	other string
	// position is port_pass_through.position, declared by whoever recorded the
	// patch. Never derived, never renumbered, never an index into anything:
	// strand 7 stays strand 7 when strand 6 is unpatched, because it is a fact
	// about which hole the fibre is in (docs/panel-breakout-design.md D5).
	position int
	// fromRear is true when the interface this entry is filed under is the REAR
	// port, so `other` is one of possibly several front ports.
	//
	// IT IS NOT A DIRECTION PREFERENCE. The successor rule is unchanged --
	// cable first, then pass-through, from either end. This says which side of
	// the row the walk came in on, which is what decides whether `position`
	// describes the step just taken or the step somebody else would take.
	fromRear bool
}
```

- [ ] **Step 2: Widen the map, and say why in the place that will be read**

Replace `cabling.go:82-83`:

```go
	// through maps an interface to the ports on the panel's other side.
	//
	// ONE-TO-MANY, and that is the whole of this work package. A twelve-fibre
	// MPO trunk lands on one rear port and breaks out to twelve front ports;
	// map[string]string could hold exactly one of them, so eleven fibres were
	// invisible and the tracer silently reported whichever row loadPlant read
	// last. A FRONT port still has at most one entry -- the partial unique
	// index port_pass_through_front_key enforces that on live rows -- so only
	// the rear side is ever longer than one.
	//
	// Ordered by position, which comes from the query rather than a sort here:
	// the tree must render in the order the strands are physically numbered.
	through map[string][]passThroughEnd
```

and the initialiser at `cabling.go:108`:

```go
		through: map[string][]passThroughEnd{},
```

- [ ] **Step 3: Read the column, ordered**

Replace `cabling.go:151-163`:

```go
	var patches []struct {
		Front    string `db:"front_interface_id"`
		Rear     string `db:"rear_interface_id"`
		Position int    `db:"position"`
	}
	// ORDERED IN SQL, NOT IN GO. The rows are appended in the order they come
	// back, so a globally position-ordered result set gives every per-rear-port
	// slice its strands in position order for free. front_interface_id breaks
	// the tie so two engines cannot disagree about two strands recorded at the
	// same position on different rear ports -- both plain columns, no dialect
	// feature involved.
	if err := s.read(ctx, &patches, `
		SELECT front_interface_id, rear_interface_id, position
		FROM port_pass_through WHERE lifecycle <> ?
		ORDER BY position, front_interface_id`, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading pass-throughs: %w", err)
	}
	for _, t := range patches {
		p.through[t.Front] = append(p.through[t.Front],
			passThroughEnd{other: t.Rear, position: t.Position})
		p.through[t.Rear] = append(p.through[t.Rear],
			passThroughEnd{other: t.Front, position: t.Position, fromRear: true})
	}
```

- [ ] **Step 4: Keep the walk a path, on one entry, for one commit**

The walk still returns a flat `Trace`. Replace `cabling.go:229` with a lookup of the first *unvisited* entry:

```go
		// Then through the panel we just arrived at.
		if e, ok := firstUnvisited(p.through[current], visited); ok {
			info, known := p.iface[e.other]
```

(the body below it is unchanged — `other` becomes `e.other` in the three places it appears), and replace the loop-detection line at `cabling.go:254-256`:

```go
		for _, e := range p.through[current] {
			if e.other != previous && visited[e.other] {
				looped = true
			}
		}
```

with the helper, marked for its own deletion:

```go
// firstUnvisited is the pass-through continuation a PATH walk takes.
//
// TEMPORARY, AND DELETED IN THE TASK AFTER NEXT. It exists so the map can
// widen in one commit without the walk changing shape, which is what lets the
// seven tests that pin today's behaviour stay untouched while it does. Taking
// the first entry is now the LOWEST POSITION rather than whichever row came
// back last, which is a strict improvement on a plant that already had a
// breakout recorded and could not be traced through it.
func firstUnvisited(ends []passThroughEnd, visited map[string]bool) (passThroughEnd, bool) {
	for _, e := range ends {
		if !visited[e.other] {
			return e, true
		}
	}
	return passThroughEnd{}, false
}
```

- [ ] **Step 5: A fixture helper that can record a strand**

In `cabling_test.go`, add beside `mustPatch` and make `mustPatch` delegate — one construction path, so a change to how a patch is made cannot apply to only half the suite:

```go
// mustPatchAt records one strand: a front port at a declared position on a
// rear port. mustPatch is the 1:1 case and is exactly this at position 1.
func mustPatchAt(t *testing.T, s *SQLStore, ctx context.Context, front, rear string, position int) string {
	t.Helper()
	p, err := domain.NewPassThrough(NewID(), domain.PassThroughSpec{
		FrontInterfaceID: front, RearInterfaceID: rear, Position: position,
	}, s.Now())
	if err != nil {
		t.Fatalf("building pass-through at position %d: %v", position, err)
	}
	if err := s.CreatePassThrough(ctx, testPermit, p); err != nil {
		t.Fatalf("creating pass-through at position %d: %v", position, err)
	}
	return p.ID
}

func mustPatch(t *testing.T, s *SQLStore, ctx context.Context, front, rear string) string {
	t.Helper()
	// Position 1 explicitly rather than relying on NewPassThrough's 0 -> 1
	// default: the default is a domain rule with its own test, and a fixture
	// that leans on it tests two things at once.
	return mustPatchAt(t, s, ctx, front, rear, 1)
}
```

- [ ] **Step 6: Pin the plant, both engines**

New test in `cabling_test.go`. This is the only place the SQL ordering is asserted, and it is asserted against the two orderings it must beat — insertion and name:

```go
// TestThePlantHoldsEveryStrandOfABreakoutInPositionOrder pins what
// map[string]string could not hold.
func TestThePlantHoldsEveryStrandOfABreakoutInPositionOrder(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			rear := mustPort(t, s, ctx, pa, "rear-1")

			// Recorded out of order, with a gap, and named so that NAME order
			// and POSITION order disagree: f-10 sorts before f-2 as text.
			f10 := mustPort(t, s, ctx, pa, "f-10")
			f2 := mustPort(t, s, ctx, pa, "f-2")
			f7 := mustPort(t, s, ctx, pa, "f-7")
			mustPatchAt(t, s, ctx, f10, rear, 10)
			mustPatchAt(t, s, ctx, f2, rear, 2)
			mustPatchAt(t, s, ctx, f7, rear, 7)

			p, err := s.loadPlant(ctx)
			if err != nil {
				t.Fatalf("loading the plant: %v", err)
			}

			ends := p.through[rear]
			if len(ends) != 3 {
				t.Fatalf("the rear port holds %d strands, want 3. A map[string]string kept "+
					"whichever row came back last, so eleven fibres of a twelve-fibre trunk "+
					"were invisible to the tracer.", len(ends))
			}
			wantID := []string{f2, f7, f10}
			wantPos := []int{2, 7, 10}
			for i, got := range ends {
				if got.other != wantID[i] || got.position != wantPos[i] {
					t.Errorf("strand %d is %s at position %d, want %s at position %d -- "+
						"ordered by position, not by insertion and not by name",
						i, got.other, got.position, wantID[i], wantPos[i])
				}
				if !got.fromRear {
					t.Errorf("strand %d is filed under the REAR port and does not say so; "+
						"whether a hop is the far side of a breakout depends on it", i)
				}
			}
			// The front side is still one-to-one, and knows which side it is.
			for _, front := range wantID {
				got := p.through[front]
				if len(got) != 1 || got[0].other != rear || got[0].fromRear {
					t.Errorf("front port %s holds %+v, want exactly one entry pointing at "+
						"the rear and not flagged as the rear side", front, got)
				}
			}
		})
	}
}
```

- [ ] **Step 7: Gates.** `make lint`, then `make test`. The seven existing trace tests must be **green and unmodified** — that is the evidence this task changed no behaviour.

---

## Task 2: Split the walk from the I/O

One refactor, no behaviour change, and it is what makes Task 4's budget test cost microseconds instead of two container-minutes.

**Files:**
- Modify: `internal/store/cabling.go` (`TracePath` 167-268)
- Create: `internal/store/cabling_walk_test.go` (licence header, blank line before `package store`)

**Interfaces:**
- Produces: `func (p *plant) trace(startID string, start ifaceInfo) *Trace` — pure, no `ctx`, no DB. Task 3 rewrites its body; Task 4 calls it directly.
- Produces (test-only): `newTestPlant()`, `(*testPlant).port/cable/patch` for building a `plant` in memory.
- Consumes: `plant` from Task 1.
- `SQLStore.TracePath` keeps its exact signature. No caller changes.

- [ ] **Step 1: `TracePath` becomes load, look up, walk**

```go
// TracePath follows a cable from one interface to wherever it ends.
func (s *SQLStore) TracePath(ctx context.Context, interfaceID string) (*Trace, error) {
	p, err := s.loadPlant(ctx)
	if err != nil {
		return nil, err
	}
	start, ok := p.iface[interfaceID]
	if !ok {
		return nil, fmt.Errorf("tracing %s: %w", interfaceID, domain.ErrNotFound)
	}
	return p.trace(interfaceID, start), nil
}

// trace walks a plant that is already loaded.
//
// SPLIT FROM THE QUERY DELIBERATELY. The interesting part of this file is the
// two bounds, and the fixture that exercises the second one is more
// pass-throughs than the entire seed estate has. Inserting five hundred rows
// through CreatePassThrough, twice, once per engine, is minutes of suite time
// for a bound that never touches a database -- and this suite has already
// failed a release tag on Go's ten-minute timeout. The plant is the real
// structure the walk runs on, so a test that builds one is a fixture, not a
// mock: loadPlant's only job is to fill it, and that job has its own
// dual-engine test.
func (p *plant) trace(startID string, start ifaceInfo) *Trace {
	// ... the existing body, verbatim, with `interfaceID` renamed to `startID`,
	// `start` already resolved, and the error returns removed (there are none
	// left in it: every remaining `return t, nil` becomes `return t`).
}
```

- [ ] **Step 2: A plant a test can build**

New file `internal/store/cabling_walk_test.go`:

```go
// testPlant builds a cable plant in memory, the way loadPlant would have.
//
// Every helper writes BOTH directions, exactly as loadPlant does, because a
// one-directional fixture would make the walk look directional when the whole
// point is that it is not.
type testPlant struct{ p *plant }

func newTestPlant() *testPlant {
	return &testPlant{&plant{
		cable:   map[string]cableEnd{},
		through: map[string][]passThroughEnd{},
		iface:   map[string]ifaceInfo{},
	}}
}

// port returns the id it filed, which is "asset/name" so a failure message
// reads like the rack rather than like a UUID.
func (tp *testPlant) port(asset, kind, name string) string {
	id := asset + "/" + name
	tp.p.iface[id] = ifaceInfo{name: name, assetID: asset, assetName: asset, assetKind: kind}
	return id
}

func (tp *testPlant) cable(a, b string) {
	tp.p.cable[a] = cableEnd{other: b}
	tp.p.cable[b] = cableEnd{other: a}
}

// patch records one strand. CALL IN POSITION ORDER: loadPlant's ORDER BY is
// what guarantees that in production and Task 1's dual-engine test is what
// pins it, so a fixture that shuffled here would be testing its own builder.
func (tp *testPlant) patch(front, rear string, position int) {
	tp.p.through[front] = append(tp.p.through[front],
		passThroughEnd{other: rear, position: position})
	tp.p.through[rear] = append(tp.p.through[rear],
		passThroughEnd{other: front, position: position, fromRear: true})
}

func (tp *testPlant) trace(t *testing.T, start string) *Trace {
	t.Helper()
	info, ok := tp.p.iface[start]
	if !ok {
		t.Fatalf("no such port in the fixture: %s", start)
	}
	return tp.p.trace(start, info)
}
```

- [ ] **Step 3: One test that the seam itself works**

Small, and it earns its place by failing if `trace` and `loadPlant` ever disagree about how a plant is shaped:

```go
// TestTheWalkRunsOnAPlantBuiltInMemory is the smallest possible proof that the
// in-memory fixture and the loaded one are the same structure. Everything in
// this file rests on it.
func TestTheWalkRunsOnAPlantBuiltInMemory(t *testing.T) {
	tp := newTestPlant()
	sw := tp.port("sw-1", "switch", "eth1")
	aF := tp.port("panel-a", "patch_panel", "a-front-1")
	aR := tp.port("panel-a", "patch_panel", "a-rear-1")
	srv := tp.port("srv-1", "server", "eth0")
	tp.cable(sw, aF)
	tp.patch(aF, aR, 1)
	tp.cable(aR, srv)

	trace := tp.trace(t, sw)
	// Assertions are written against the flat Trace in this task and rewritten
	// against the tree in Task 3, alongside every other caller.
}
```

- [ ] **Step 4: Gates.** `make lint`, `make test`. Seven existing tests still green, still unmodified.

---

## Task 3: The tree

The result gains `Root`; `Hops`, `Why` and `Complete` are **kept and derived** for exactly two commits, so the seven tests that pin today's behaviour go on running — against the new walk — while the type changes underneath them. That is the whole answer to "how do you keep the tree green while `Complete` is being removed": the flat fields become a projection of the tree, the old tests validate the new walk without being edited, and only then (Task 6) are they rewritten with the compiler naming every site.

**Files:**
- Modify: `internal/store/cabling.go` (`Trace` 47-60, `End()` 63-68, `traceHopLimit` 70-76, `trace` from Task 2)
- Create: nothing

**Interfaces:**
- Produces: `TraceNode` (spec §3b verbatim), `TraceCounts`, `TraceRow`, the `Outcome*` constants, `traceNodeBudget`.
- Produces: `func (t *Trace) Leaves() []*TraceNode`, `func (t *Trace) Nodes() int`, `func (t *Trace) Counts() TraceCounts`, `func (t *Trace) Chain() ([]TraceHop, bool)`, `func (t *Trace) Rows() []TraceRow`.
- Keeps, temporarily: `Trace.Hops`, `Trace.Why`, `Trace.Complete`, `Trace.End()` — derived from `Root`, deleted in Task 6.
- Consumes: `plant`, `passThroughEnd`, `ifaceInfo`.

- [ ] **Step 1: The node, verbatim from spec §3b**

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

// leaf records why a branch ends here. Outcome and Why are set together, and
// only on a node with no children: an interior node has no verdict of its own,
// because with three strands patched and nine looping neither answer is true
// of the whole (docs/panel-breakout-design.md D3).
func (n *TraceNode) leaf(outcome, why string) { n.Outcome, n.Why = outcome, why }
```

- [ ] **Step 2: What a leaf can say**

```go
// What a leaf can be. The strings are stable: the page branches on them, the
// tests name them, and a leaf's Why is prose for a person while its Outcome is
// the thing code is allowed to compare.
const (
	// OutcomeComplete: this branch ran out of cable rather than out of patience.
	OutcomeComplete = "complete"
	// OutcomeUnpatched: the port the caller asked about has nothing in it.
	OutcomeUnpatched = "unpatched"
	// OutcomeLooped: this branch arrived somewhere already on its own path --
	// something is patched into its own run.
	OutcomeLooped = "looped"
	// OutcomeHopLimit: traceHopLimit, which bounds one branch.
	OutcomeHopLimit = "hop_limit"
	// OutcomeNodeBudget: traceNodeBudget, which bounds the whole tree.
	OutcomeNodeBudget = "node_budget"
	// OutcomeUnknown: the continuation names an interface the plant does not
	// hold -- a retired asset at the far end of a cable, or a pass-through onto
	// a port that no longer exists.
	OutcomeUnknown = "unknown"
)
```

- [ ] **Step 3: The second bound, and why it is not the first one**

Below `traceHopLimit` (whose comment stays exactly as it is):

```go
// traceNodeBudget bounds the whole TREE, beside the per-branch hop limit.
//
// Depth stopped being the only way a walk gets large the moment one rear port
// could have twelve continuations: a twelve-way break-out into panels that
// themselves break out is 144 leaves at depth two, and traceHopLimit does not
// see that at all. The two bounds answer different questions -- "this run is
// absurdly long" and "this plant fans out faster than anyone wants to read" --
// and neither covers the other.
//
// 512 is comfortably past the 144 the motivating case produces, and small
// enough that a malformed plant renders a bounded page instead of hanging a
// browser. It is a guard against a plant being edited halfway through, not a
// capacity limit anybody should meet.
const traceNodeBudget = 512

// budget is the tree-wide node allowance.
//
// SPENDING IS NEVER REFUSED. A successor the plant actually holds is never
// silently dropped -- dropping one reports a SHORTER trace with no error,
// which is the worst failure shape available here. What the budget refuses is
// EXPANSION: a node created after the allowance is gone becomes a leaf that
// says so. The tree is therefore bounded by the budget plus the fan-out of the
// single node that crossed zero -- one node can overshoot, because expansion
// is depth first and the check happens before each one -- and every strand
// that stopped early carries the reason it stopped.
type budget struct{ left int }

func (b *budget) spend(n int) { b.left -= n }
func (b *budget) spent() bool { return b.left <= 0 }
```

- [ ] **Step 4: The walk becomes an expansion, with `visited` per branch**

Replace the body of `plant.trace`:

```go
func (p *plant) trace(startID string, start ifaceInfo) *Trace {
	t := &Trace{
		StartAssetID: start.assetID, StartAsset: start.assetName,
		StartInterface: start.name,
		Root:           &TraceNode{},
	}

	// TWO BOUNDS, and both are load-bearing.
	//
	// `visited` does more than its name suggests, which mutation testing made
	// obvious: without it the walk does not merely permit loops, it bounces
	// straight back down the cable it arrived on, because that cable is still
	// there and nothing says it has been used. It is what makes the walk
	// directional at all, and only secondarily what stops two panels patched
	// into each other from running for ever.
	//
	// IT IS NOW PER BRANCH: the interfaces on the path from the ROOT to the
	// node being expanded, maintained by adding before a recursion and removing
	// after it. For a path, "already seen anywhere in this walk" and "already
	// seen on the way here" are the same set. FOR A TREE THEY ARE NOT. Two
	// strands of one trunk legitimately reach the same second panel; a global
	// set would let strand 1 consume it and strand 7 would stop one hop short
	// WITH NO ERROR, reporting a run that ends at a front port and looks
	// exactly like a genuinely unpatched one. Per-branch is also the correct
	// definition of a cycle, which the path case only got right by coincidence
	// of having one branch -- and it preserves the directional property above,
	// because the parent is still in the set while its child is expanded.
	//
	// The hop count is the second bound, for a chain that is merely absurd
	// rather than circular -- what a plant being edited looks like halfway
	// through -- and traceNodeBudget is the third, for a plant that is neither
	// long nor circular but simply fans out faster than anyone can read.
	visited := map[string]bool{startID: true}
	b := &budget{left: traceNodeBudget}
	b.spend(1) // the root is a node too
	p.expand(t.Root, startID, "", visited, 0, b)
	return t
}

// expand grows one node into its continuations.
//
// previous is the interface this node was reached from, kept apart from
// `visited` because the two answer different questions. At the far end of any
// run the only cable is the one we arrived on -- treating that as a cycle
// reported every complete path as a loop, which is what the first version did.
func (p *plant) expand(n *TraceNode, current, previous string, visited map[string]bool, depth int, b *budget) {
	if depth >= traceHopLimit {
		n.leaf(OutcomeHopLimit, fmt.Sprintf("stopped after %d hops. A path this long is almost "+
			"certainly a mis-patch rather than a real run.", traceHopLimit))
		return
	}
	if b.spent() {
		n.leaf(OutcomeNodeBudget, fmt.Sprintf("stopped here: the trace has already reached %d "+
			"steps in total. A plant that fans out this far is being edited or mis-patched "+
			"rather than read.", traceNodeBudget))
		return
	}

	// A cable first: leaving the box is the interesting move. UNCHANGED, and
	// deliberately so -- the successor rule did not move. A trace starting at a
	// rear port with a trunk plugged into it follows that trunk, exactly as it
	// did before breakout existed. The only step that branches is the next one.
	if end, ok := p.cable[current]; ok && !visited[end.other] {
		info, known := p.iface[end.other]
		if !known {
			n.leaf(OutcomeUnknown, "the far end of a cable is on an asset that has been retired")
			return
		}
		b.spend(1)
		child := &TraceNode{Hop: TraceHop{
			Kind: HopCable, AssetID: info.assetID, AssetName: info.assetName,
			AssetKind: info.assetKind, InterfaceID: end.other, Interface: info.name,
			Medium: end.medium, Length: end.length,
		}}
		n.Children = append(n.Children, child)
		visited[end.other] = true
		p.expand(child, end.other, current, visited, depth+1, b)
		delete(visited, end.other) // the branch is done with it; a sibling is not
		return
	}

	// Then through the panel we just arrived at -- EVERY strand of it. This is
	// the one line of the successor rule that changed.
	var strands []passThroughEnd
	for _, e := range p.through[current] {
		if !visited[e.other] {
			strands = append(strands, e)
		}
	}
	if len(strands) > 0 {
		b.spend(len(strands))
		for _, e := range strands {
			child := &TraceNode{}
			// Position labels the far side of a BREAKOUT: the parent was the
			// rear port and this node is one of the front ports behind it. Going
			// the other way the position describes a step somebody else would
			// take, not this one, so it stays 0. The value is whatever was
			// declared -- a lone strand recorded at position 7 reports 7 (D5).
			if e.fromRear {
				child.Position = e.position
			}
			n.Children = append(n.Children, child)

			info, known := p.iface[e.other]
			if !known {
				// The strand is real -- it has a row -- but its far side is on a
				// retired asset or a port that is gone. IT STILL GETS A NODE:
				// dropping it would leave a trunk short one strand with nothing
				// saying so, which is precisely the silent truncation this whole
				// design is arranged to prevent. The interface id is all there is
				// to name it by; the row that carried a name has gone.
				child.Hop = TraceHop{Kind: HopPanel, InterfaceID: e.other}
				child.leaf(OutcomeUnknown, "a pass-through lands on an interface that no longer exists")
				continue
			}
			child.Hop = TraceHop{
				Kind: HopPanel, AssetID: info.assetID, AssetName: info.assetName,
				AssetKind: info.assetKind, InterfaceID: e.other, Interface: info.name,
			}
			visited[e.other] = true
			p.expand(child, e.other, current, visited, depth+1, b)
			delete(visited, e.other)
		}
		return
	}

	// Nothing further. Which of the several ways that can happen is the answer,
	// so it is spelled out rather than left as an empty list. A continuation
	// that is somewhere this BRANCH has already been, and is not simply the way
	// it came, is a genuine loop: something is patched into its own run.
	looped := false
	if end, ok := p.cable[current]; ok && end.other != previous && visited[end.other] {
		looped = true
	}
	for _, e := range p.through[current] {
		if e.other != previous && visited[e.other] {
			looped = true
		}
	}
	switch {
	case depth == 0 && !looped:
		n.leaf(OutcomeUnpatched, "nothing is plugged into this port")
	case looped:
		n.leaf(OutcomeLooped, "the path loops back on itself — something is patched into its own run")
	default:
		n.leaf(OutcomeComplete, "the path ends here")
	}
}
```

**Note the one deliberate behaviour change**, and put it in the commit message: a pass-through onto a missing interface used to end the *whole* walk with no hop recorded. It now ends *that strand*, with a node, and its siblings continue. Nothing tested the old behaviour; the new one is the only one compatible with "never silently drop a strand".

- [ ] **Step 5: Reading a tree — the accessors the page and the tests need**

```go
// Leaves is every end of the run, left to right: position order within a
// breakout, and depth first through nested ones.
func (t *Trace) Leaves() []*TraceNode {
	var out []*TraceNode
	var walk func(*TraceNode)
	walk = func(n *TraceNode) {
		if len(n.Children) == 0 {
			out = append(out, n)
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	if t.Root != nil {
		walk(t.Root)
	}
	return out
}

// Nodes counts every step in the tree, the start port included. It is what the
// node budget bounds, and what a test asserts against it.
func (t *Trace) Nodes() int { ... }

// TraceCounts is what a whole trace reports. COUNTS, NEVER A VERDICT: with
// three strands patched and one looping, neither "complete" nor "incomplete"
// is true of the trace, and a summary bool over a tree is exactly the figure
// that looks more certain than it is (D3).
//
// It counts the strands that HAVE ROWS and nothing else. Nothing anywhere
// records how many positions a rear port physically has, so this must never
// grow a "free" or "total" field: "nine are free" is a claim about a trunk
// nobody described (D4 as corrected 2026-09-05).
type TraceCounts struct {
	Strands int // leaves in total -- ends of runs, not positions on a trunk
	Ends    int // leaves that reached a far end
	Loops   int
	Stopped int // hop limit or node budget
	Unknown int
}

func (t *Trace) Counts() TraceCounts { ... }

// Chain flattens a run with no breakout in it into the flat hop list this
// tracer returned before WP-B4, and reports false the moment any node has more
// than one continuation.
//
// TWO CALLERS AND NO OTHERS: the tests that pin the 1:1 case against the exact
// list it produced before the type changed, and any future reader who needs
// "the path" and must be TOLD when there isn't one. It is deliberately not the
// page's route -- a helper that quietly returns the first branch of a tree is
// the "one function, two shapes" API D1 rejected.
func (t *Trace) Chain() ([]TraceHop, bool) {
	if t.Root == nil {
		return nil, false
	}
	var hops []TraceHop
	for n := t.Root; len(n.Children) > 0; {
		if len(n.Children) > 1 {
			return nil, false
		}
		n = n.Children[0]
		hops = append(hops, n.Hop)
	}
	return hops, true
}
```

- [ ] **Step 6: `Rows`, so the template stays dumb**

```go
// TraceRow is one line of the rendered trace.
type TraceRow struct {
	// Step is the depth from the start port. 0 is the port itself, which
	// reproduces the numbering the page had before the result became a tree.
	Step int
	Node *TraceNode
	// Strand says whether to label this row with its position.
	//
	// THE DATA IS ALWAYS HONEST AND THE LABEL IS NOT ALWAYS USEFUL. Node.
	// Position carries whatever was declared, including the 1 on every
	// ordinary 1:1 panel, because that is what the row says (D5). Printing
	// "strand 1" on every panel hop in the estate would be noise that implies a
	// breakout where there is none. So: label it when the parent actually
	// branched, or when the position is not 1 -- a lone strand recorded at
	// position 7 is worth saying out loud even though nothing branched.
	Strand bool
}

// Rows flattens the tree for rendering, depth first and in position order.
//
// FLATTENED IN GO, NOT IN THE TEMPLATE. html/template can recurse, but the
// alternative is a template computing its own depth and branch labels, which
// is business logic in a template and is untestable without asserting on
// markup -- which this work package explicitly does not do. A run with no
// breakout in it flattens to exactly the numbered rows the page had before.
func (t *Trace) Rows() []TraceRow { ... }
```

- [ ] **Step 7: Derive the old fields, and mark them for removal**

On `Trace`, keeping the field comments as they are:

```go
	// Root is the port the caller asked about, and the run beneath it. Never nil.
	Root *TraceNode
	// Hops, Why and Complete are DERIVED FROM Root FOR TWO COMMITS and then
	// deleted (Task 6). They exist so the seven tests that pin this tracer's
	// behaviour can go on running -- against the new walk -- while the type
	// changes underneath them. A trace with a breakout in it cannot be
	// described by any of the three, which is why they go.
	Hops     []TraceHop
	Why      string
	Complete bool
```

Populate at the end of `trace()`:

```go
	// TEMPORARY (Task 6 deletes this block with the fields it fills).
	if hops, ok := t.Chain(); ok {
		t.Hops = hops
	}
	if leaves := t.Leaves(); len(leaves) == 1 {
		t.Why = leaves[0].Why
		t.Complete = leaves[0].Outcome == OutcomeComplete
	}
```

`End()` stays as-is for now; it reads `Hops`.

- [ ] **Step 8: Gates.** `make lint`, `make test`. **The seven existing tests must still be green and still unmodified.** That is the evidence that the tree walk reproduces the path walk on every fixture that existed before it.

---

## Task 4: The tests the tree exists for

New behaviour, new file. Written before the page changes, because none of it is about the page.

**Files:**
- Modify: `internal/store/cabling_walk_test.go` (from Task 2)
- Create: `internal/store/cabling_breakout_test.go` (licence header, blank line before `package store`)

**Interfaces:**
- Consumes: `mustPatchAt`, `cablePlant`, `newTestPlant`, `Trace.Leaves/Counts/Chain/Nodes`, the `Outcome*` constants.
- Produces: `breakoutPlant(t, s, ctx) (swPort, corePort string, ids map[string]string)` — the convergence fixture from spec §2.3, which does not exist today.

- [ ] **Step 1: The convergence fixture (spec §2.3) — the one this whole design turns on**

Two strands of one trunk reach one second panel, whose rear port is cabled onward:

```
                    ┌─ pos 1 → panel-b/f-1 ─┐
sw-1/eth1 ═ panel-a/rear-1                   ├─ panel-b/rear-1 ═ core-1/eth1
                    └─ pos 7 → panel-b/f-7 ─┘
```

```go
// breakoutPlant is spec §2.3: one trunk, two strands, both legitimately
// reaching the same second panel.
//
// IT IS THE FIXTURE A GLOBAL `visited` SET FAILS ON, and it fails quietly:
// strand 1 marks panel-b/rear-1, and strand 7 then stops at panel-b/f-7 with
// no error at all -- reporting a run that ends at a front port, which looks
// exactly like a strand nobody patched onward.
func breakoutPlant(t *testing.T, s *SQLStore, ctx context.Context) (swPort, corePort string, ids map[string]string) {
	t.Helper()
	site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
	sw := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", &site)
	pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
	pb := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-b", &site)
	core := mustAsset(t, s, ctx, domain.KindSwitch, "core-1", &site)

	swPort = mustPort(t, s, ctx, sw, "eth1")
	aRear := mustPort(t, s, ctx, pa, "a-rear-1")
	aF1 := mustPort(t, s, ctx, pa, "a-f-1")
	aF7 := mustPort(t, s, ctx, pa, "a-f-7")
	bF1 := mustPort(t, s, ctx, pb, "b-f-1")
	bF7 := mustPort(t, s, ctx, pb, "b-f-7")
	bRear := mustPort(t, s, ctx, pb, "b-rear-1")
	corePort = mustPort(t, s, ctx, core, "eth1")

	// The trunk arrives on panel-a's rear port and breaks out to two of its
	// front ports. Positions 1 and 7, not 1 and 2: a gap is the ordinary case
	// and it is what stops anybody reading Position as an index (D5).
	mustCable(t, s, ctx, swPort, aRear)
	mustPatchAt(t, s, ctx, aF1, aRear, 1)
	mustPatchAt(t, s, ctx, aF7, aRear, 7)

	// Each strand runs to its own front port on panel-b, and panel-b's rear
	// port -- SHARED BY BOTH -- is cabled onward to the core.
	mustCable(t, s, ctx, aF1, bF1)
	mustCable(t, s, ctx, aF7, bF7)
	mustPatchAt(t, s, ctx, bF1, bRear, 1)
	mustPatchAt(t, s, ctx, bF7, bRear, 7)
	mustCable(t, s, ctx, bRear, corePort)

	return swPort, corePort, map[string]string{
		"a-rear-1": aRear, "a-f-1": aF1, "a-f-7": aF7,
		"b-f-1": bF1, "b-f-7": bF7, "b-rear-1": bRear,
	}
}

func TestBothStrandsOfATrunkReachTheFarEnd(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			swPort, corePort, ids := breakoutPlant(t, s, ctx)

			trace, err := s.TracePath(ctx, swPort)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}

			leaves := trace.Leaves()
			if len(leaves) != 2 {
				t.Fatalf("the trunk produced %d ends, want 2 -- one per recorded strand. "+
					"A tracer that follows one continuation per interface answers for one "+
					"fibre of the twelve in the trunk.", len(leaves))
			}
			for i, leaf := range leaves {
				if leaf.Hop.InterfaceID != corePort {
					t.Errorf("strand %d ends at %s/%s, want core-1/eth1. BOTH strands "+
						"legitimately reach panel-b's rear port: a `visited` set shared "+
						"across branches lets the first one consume it and stops the second "+
						"ONE HOP SHORT WITH NO ERROR, which is indistinguishable from a "+
						"strand nobody patched onward.",
						i, leaf.Hop.AssetName, leaf.Hop.Interface)
				}
				if leaf.Outcome != OutcomeComplete {
					t.Errorf("strand %d ended %q (%s), want %q", i, leaf.Outcome, leaf.Why, OutcomeComplete)
				}
			}

			// The strands are labelled with what was DECLARED, in that order.
			// 1 and 7, never 1 and 2: Position is the hole the fibre is in, not
			// an index into Children (D5).
			var branch *TraceNode
			for n := trace.Root; ; n = n.Children[0] {
				if len(n.Children) > 1 {
					branch = n
					break
				}
				if len(n.Children) == 0 {
					t.Fatal("no node in the trace has more than one continuation, so the "+
						"trunk was never followed as a breakout at all")
				}
			}
			if branch.Hop.InterfaceID != ids["a-rear-1"] {
				t.Errorf("the branch is at %s, want panel-a's rear port", branch.Hop.Interface)
			}
			if got := []int{branch.Children[0].Position, branch.Children[1].Position}; got[0] != 1 || got[1] != 7 {
				t.Errorf("the strands are positions %v, want [1 7] in that order", got)
			}
			if c := trace.Counts(); c.Strands != 2 || c.Ends != 2 || c.Loops != 0 {
				t.Errorf("counts = %+v, want 2 strands both reaching an end", c)
			}
		})
	}
}
```

- [ ] **Step 2: PROVE THIS TEST CAN FAIL, and write what you did into the evidence gate**

Not optional, and this is the specific mutation:

1. In `plant.trace`, hoist `visited` so it is shared: delete the two `delete(visited, ...)` calls in `expand`.
2. Run `make test`. `TestBothStrandsOfATrunkReachTheFarEnd` must go **red**, reporting strand 7 ending at `panel-b/b-f-7`.
3. Restore.

A test that has never been observed failing is a claim, not a check — and this is the claim the entire design rests on.

- [ ] **Step 3: One leaf per recorded position, and no implied total**

```go
// TestABreakoutYieldsOneLeafPerRecordedPosition covers §4's four-position case
// and D4-as-corrected together: what appears is what has rows.
func TestABreakoutYieldsOneLeafPerRecordedPosition(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			rear := mustPort(t, s, ctx, pa, "rear-1")

			// THREE STRANDS OF A TWELVE-FIBRE TRUNK, recorded at 1, 5 and 12.
			// The first draft of the design wanted twelve leaves here, nine of
			// them saying "nothing patched". That is unbuildable and the
			// challenge round found it: port_pass_through holds a row per
			// PATCHED position and nothing anywhere records how many positions a
			// rear port physically has. So the nine free strands are not merely
			// unqueried -- the database does not know they exist, and reporting
			// them would be a claim about a trunk nobody described (D4).
			want := []int{1, 5, 12}
			for _, pos := range want {
				front := mustPort(t, s, ctx, pa, fmt.Sprintf("f-%02d", pos))
				mustPatchAt(t, s, ctx, front, rear, pos)
			}

			trace, err := s.TracePath(ctx, rear)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			leaves := trace.Leaves()
			if len(leaves) != len(want) {
				t.Fatalf("the trunk produced %d ends, want %d -- one per RECORDED position "+
					"and not one per hole in a trunk nobody described", len(leaves), len(want))
			}
			for i, leaf := range leaves {
				if leaf.Position != want[i] {
					t.Errorf("end %d is position %d, want %d -- in position order, and "+
						"position 12 is not renumbered to 3 because 2..4 have no rows (D5)",
						i, leaf.Position, want[i])
				}
			}
			if c := trace.Counts(); c.Strands != 3 {
				t.Errorf("counts = %+v, want 3 strands. The trace says how many it FOUND; "+
					"it must never imply how many the trunk has.", c)
			}
		})
	}
}
```

- [ ] **Step 4: A looping strand does not truncate its siblings**

Fixture: `panel-a/rear` breaks out to two strands. Strand 1 runs to a server. Strand 2 runs into two panels patched into each other and comes back to a port already on its own path.

```go
func TestALoopingStrandDoesNotTruncateItsSiblings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			pb := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-b", &site)
			pc := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-c", &site)
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)

			aRear := mustPort(t, s, ctx, pa, "a-rear-1")
			aF1 := mustPort(t, s, ctx, pa, "a-f-1")
			aF2 := mustPort(t, s, ctx, pa, "a-f-2")
			mustPatchAt(t, s, ctx, aF1, aRear, 1)
			mustPatchAt(t, s, ctx, aF2, aRear, 2)

			// Strand 1: an ordinary, complete run.
			srvPort := mustPort(t, s, ctx, srv, "eth0")
			mustCable(t, s, ctx, aF1, srvPort)

			// Strand 2 into a mis-patch: panel-b's rear port itself breaks out
			// two ways, and the second way comes back to it through panel-c.
			bRear := mustPort(t, s, ctx, pb, "b-rear-1")
			bF1 := mustPort(t, s, ctx, pb, "b-f-1")
			bF2 := mustPort(t, s, ctx, pb, "b-f-2")
			cRear := mustPort(t, s, ctx, pc, "c-rear-1")
			cF1 := mustPort(t, s, ctx, pc, "c-f-1")
			mustPatchAt(t, s, ctx, bF1, bRear, 1)
			mustPatchAt(t, s, ctx, bF2, bRear, 2)
			mustPatchAt(t, s, ctx, cF1, cRear, 1)
			mustCable(t, s, ctx, aF2, bF2)
			mustCable(t, s, ctx, bRear, cRear)
			mustCable(t, s, ctx, cF1, bF1) // closes it: back to panel-b's rear

			done := make(chan *Trace, 1)
			go func() {
				trace, err := s.TracePath(ctx, aRear)
				if err != nil {
					done <- nil
					return
				}
				done <- trace
			}()
			select {
			case trace := <-done:
				if trace == nil {
					t.Fatal("tracing a plant with one looping strand errored")
				}
				var reachedServer, looped int
				for _, leaf := range trace.Leaves() {
					if leaf.Hop.InterfaceID == srvPort && leaf.Outcome == OutcomeComplete {
						reachedServer++
					}
					if leaf.Outcome == OutcomeLooped {
						looped++
					}
				}
				if reachedServer != 1 {
					t.Errorf("the clean strand reached the server %d times, want once. A "+
						"branch that gives up must not take its siblings with it.", reachedServer)
				}
				if looped == 0 {
					t.Error("no branch reported a loop, so the mis-patched strand either ran " +
						"out of hops or was silently dropped -- both are the wrong answer here")
				}
				if trace.Nodes() > traceNodeBudget+len(trace.Root.Children) {
					t.Errorf("the tree has %d nodes; a loop is not supposed to grow one", trace.Nodes())
				}
			case <-timeoutAfter():
				t.Fatal("tracing a plant with one looping strand did not terminate")
			}
		})
	}
}
```

- [ ] **Step 5: The node budget, in memory, on a fan-out no database fixture should have to build**

In `cabling_walk_test.go`:

```go
// TestAFanOutPastTheNodeBudgetSaysSoOnEveryBranchItStopped is why Task 2 split
// the walk from the query: this fixture is 520 pass-throughs, and inserting
// them through CreatePassThrough once per engine would cost the suite minutes
// for a bound that never touches a database.
func TestAFanOutPastTheNodeBudgetSaysSoOnEveryBranchItStopped(t *testing.T) {
	tp := newTestPlant()
	rear := tp.port("panel-a", "patch_panel", "rear-1")
	width := traceNodeBudget + 8
	for i := 1; i <= width; i++ {
		front := tp.port("panel-a", "patch_panel", fmt.Sprintf("f-%03d", i))
		tp.patch(front, rear, i)
	}

	trace := tp.trace(t, rear)

	// NOT ONE STRAND FEWER. A budget that dropped successors would report a
	// SHORTER trunk with no error, which is the failure shape this design
	// exists to prevent; what the budget refuses is EXPANSION, not existence.
	if got := len(trace.Root.Children); got != width {
		t.Fatalf("the fan-out produced %d strands, want %d -- every strand with a row "+
			"appears, and the ones that could not be followed say so", got, width)
	}
	var stopped int
	for _, leaf := range trace.Leaves() {
		if leaf.Outcome == OutcomeNodeBudget {
			stopped++
			if leaf.Why == "" {
				t.Error("a branch stopped on the budget and said nothing. \"The path ends " +
					"here\" and \"we gave up\" are different answers.")
			}
		}
	}
	if stopped < 2 {
		t.Errorf("%d branches reported the budget, want it said PER BRANCH -- a single "+
			"summary would leave every other strand looking like a complete run", stopped)
	}
	// Bounded: the budget, plus at most the fan-out of the one node that
	// crossed zero. Depth-first expansion checks before each node, so only one
	// can overshoot.
	if max := traceNodeBudget + width; trace.Nodes() > max {
		t.Errorf("the tree has %d nodes, past the bound of %d", trace.Nodes(), max)
	}
}
```

- [ ] **Step 6: Tracing up from one front port is a single chain**

```go
// TestTracingUpFromOneFrontPortIsASingleChain is the asymmetry in §2.2: down
// from the trunk there are many answers, up from one strand there is one.
//
// It also pins the Position rule from the other side. Going front -> rear the
// position describes a step somebody ELSE would take, not this one, so the hop
// carries 0 -- while the row itself says 7.
func TestTracingUpFromOneFrontPortIsASingleChain(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			sw := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", &site)

			rear := mustPort(t, s, ctx, pa, "a-rear-1")
			front := mustPort(t, s, ctx, pa, "a-f-7")
			swPort := mustPort(t, s, ctx, sw, "eth1")
			mustPatchAt(t, s, ctx, front, rear, 7)
			mustCable(t, s, ctx, rear, swPort)

			trace, err := s.TracePath(ctx, front)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			hops, single := trace.Chain()
			if !single {
				t.Fatalf("tracing up from one strand branched; it has one answer")
			}
			if len(hops) != 2 || hops[0].InterfaceID != rear || hops[1].InterfaceID != swPort {
				t.Fatalf("the chain is %+v, want the rear port then the switch", hops)
			}
			for _, n := range []*TraceNode{trace.Root.Children[0]} {
				if n.Position != 0 {
					t.Errorf("the front -> rear hop is labelled strand %d. Position labels "+
						"the FAR SIDE of a breakout; going this way it names a step this run "+
						"did not take.", n.Position)
				}
			}
			if leaves := trace.Leaves(); len(leaves) != 1 || leaves[0].Outcome != OutcomeComplete {
				t.Errorf("leaves = %+v, want one complete end", leaves)
			}
		})
	}
}
```

- [ ] **Step 7: `Rows` labels a strand when a strand is worth labelling**

A small in-memory unit test in `cabling_walk_test.go`: a 1:1 chain produces no `Strand` row; a two-way breakout produces `Strand` on both; a lone strand recorded at position 7 produces `Strand` on it. Assert `Step` on the 1:1 chain is `0,1,2,...` — that is the numbering the page had before, and it is the only "renders as today" claim this work package makes at all.

- [ ] **Step 8: Gates.** `make lint`, `make test`. The seven existing tests **still unmodified and still green** at the end of this task.

---

## Task 5: The page renders the tree

The handler does not change. `tracePage.Trace` is still `*store.Trace`; only the template moves.

**Files:**
- Modify: `web/templates/pages/trace.html` (`trace_path` 29-76 — the hop loop 42-58 and the completeness block 62-72)
- Not modified, deliberately: `internal/web/handlers/cabling.go`
- Not modified, deliberately: `internal/web/power_test.go:342-360`

**Interfaces:**
- Consumes: `Trace.Rows`, `Trace.Counts`, `TraceRow.{Step,Node,Strand}`, `TraceNode.{Hop,Position,Outcome,Why}`, the `Outcome*` string values.
- Template funcs available and already registered: `add`, `derefInt` (`internal/web/render/funcs.go:51,89`).

- [ ] **Step 1: Row 0 keeps its shape, and the loop renders rows instead of hops**

`Rows()` includes the root, so the start row and every hop come from one loop; the root's own leaf reason (an unplugged port) can no longer be lost. Sketch — the plan does not pin markup, but **the `pill-ok">complete` span must survive verbatim**, see Step 3:

```html
          {{range .Trace.Rows}}
          {{$n := .Node}}
          <tr>
            <td class="mono muted">{{.Step}}</td>
            {{if eq .Step 0}}
            <td><a class="id" href="/assets/{{$.Trace.StartAssetID}}">{{$.Trace.StartAsset}}</a></td>
            <td class="mono">{{$.Trace.StartInterface}}</td>
            <td class="muted">start</td>
            {{else}}
            <td><a class="id" href="/assets/{{$n.Hop.AssetID}}">{{$n.Hop.AssetName}}</a>
              <div class="muted" style="font-size:11.5px">{{$n.Hop.AssetKind}}</div></td>
            <td class="mono">{{$n.Hop.Interface}}{{if .Strand}}
              <span class="pill pill-muted">strand {{$n.Position}}</span>{{end}}</td>
            <td>
              {{if eq $n.Hop.Kind "panel"}}
                <span class="pill pill-muted">through the panel</span>
              {{else}}
                <span class="pill pill-info">cable</span>
                {{if $n.Hop.Medium}}<span class="muted">{{$n.Hop.Medium}}</span>{{end}}
                {{if $n.Hop.Length}}<span class="muted">{{derefInt $n.Hop.Length}} m</span>{{end}}
              {{end}}
            </td>
            {{end}}
          </tr>
          {{end}}
```

- [ ] **Step 2: The verdict moves onto the leaf, and the trace reports counts**

Replace the `{{if .Trace.Complete}}` block. Each leaf row carries its own pill and reason; below the table, the counts — and the D4 sentence, which is a requirement and not copy:

```html
      {{/* Never blank, and never a single verdict. With three strands patched
           and one looping, neither "complete" nor "incomplete" is true of the
           whole run, so the answer sits on each end (D3). */}}
      <p class="hint" style="margin-top:12px">
        {{$c := .Trace.Counts}}
        {{$c.Strands}} end{{if ne $c.Strands 1}}s{{end}},
        {{$c.Ends}} reaching a far end{{if $c.Loops}}, {{$c.Loops}} looping{{end}}{{if $c.Stopped}}, {{$c.Stopped}} stopped short{{end}}.
        Only the strands that have been recorded are shown: nothing records how
        many positions a rear port physically has, so this is not a count of the
        trunk.
      </p>
```

- [ ] **Step 3: Do not change `internal/web/power_test.go`, and understand why**

`TestTracingARunThroughTwoPanelsOnScreen` (`power_test.go:262-361`) asserts `pill-ok">complete` is present and `pill-degraded">incomplete` is absent, with a comment recording that mutation testing caught the substring version of that assertion. Its fixture is a 1:1 run, which is one leaf, so **rendering the leaf pill with exactly that markup keeps the test passing and keeps it meaning what it meant.** If you find yourself editing that test to make it pass, stop: either the markup drifted for no reason, or a 1:1 run stopped reporting itself as complete, and the second one is a bug in Task 3.

Note this is the guard that catches a template referring to a field that no longer exists — `html/template` fails at execute time, not compile time, so `go build` will not tell you.

- [ ] **Step 4: Add one functional test for a breakout ON THE PAGE**

In `internal/web`, beside the existing trace test. It posts two patches at positions 1 and 2 to `/assets/{id}/patch` (the handler already accepts `position`), then asserts both strands' far ends are named on `/interfaces/{rear}/trace` and that the strand labels appear. **If Task 8 is taken, drive it through the form field instead** — a functional test that posts a field the UI cannot send proves the handler works and nothing else.

- [ ] **Step 5: Gates.** `make lint`, `make test`.

---

## Task 6: Remove `Complete`, `Hops`, `Why` and `End()`

Mechanical, compiler-driven, and the last task that can change meaning by accident — so it changes only shape, and each of the seven existing tests is accounted for below by name.

**Files:**
- Modify: `internal/store/cabling.go` (`Trace` fields, `End()` 63-68, the derivation block from Task 3 Step 7, `firstUnvisited` from Task 1 Step 4)
- Modify: `internal/store/cabling_test.go` (four of the seven tests)

**Interfaces:**
- Removes: `Trace.Hops`, `Trace.Why`, `Trace.Complete`, `Trace.End()`, `firstUnvisited`.
- Nothing outside `internal/store` consumes any of them once Task 5 has landed — verified: the only consumers are `web/templates/pages/trace.html` and this test file.

- [ ] **Step 1: Delete the fields, the derivation block, `End()` and `firstUnvisited`. Build. The compiler now lists every site left.**

- [ ] **Step 2: `TestATraceCrossesThePanelsInTheWay` — changes SHAPE, and gets stricter**

This is §4.4's structured-result requirement: *a 1:1 run yields a single chain whose hops, order, kinds and reasons equal today's flat list exactly.* The current test asserts `strings.Contains` on a joined list, which is order-blind; asserting the exact sequence is strictly stronger and is what "equals today's flat list" means. Keep every existing failure message verbatim — they carry the reasoning.

```go
			trace, err := s.TracePath(ctx, swPort)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			hops, single := trace.Chain()
			if !single {
				t.Fatalf("a 1:1 run branched. Every existing run must render as a single " +
					"chain: the tree is what CHANGED, not what a run through two ordinary " +
					"panels means.")
			}
			// EVERY HOP NAMED, IN ORDER. "It goes through panel-b, port
			// b-front-1" is actionable; "these two are connected" is not.
			want := []struct{ kind, where string }{
				{HopCable, "panel-a/a-front-1"},
				{HopPanel, "panel-a/a-rear-1"},
				{HopCable, "panel-b/b-rear-1"},
				{HopPanel, "panel-b/b-front-1"},
				{HopCable, "srv-1/eth0"},
			}
			if len(hops) != len(want) {
				t.Fatalf("the path has %d hops, want %d: %+v", len(hops), len(want), hops)
			}
			for i, w := range want {
				got := hops[i].AssetName + "/" + hops[i].Interface
				if got != w.where || hops[i].Kind != w.kind {
					t.Errorf("hop %d is %s (%s), want %s (%s)", i+1, got, hops[i].Kind, w.where, w.kind)
				}
			}
			leaves := trace.Leaves()
			if len(leaves) != 1 || leaves[0].Hop.InterfaceID != srvPort {
				t.Fatalf("the path ended at %+v, want srv-1 eth0. A tracer that stops at "+
					"the first cable answers \"a patch panel\", which is true and useless.", leaves)
			}
			if leaves[0].Outcome != OutcomeComplete {
				t.Fatalf("the trace did not complete: %s", leaves[0].Why)
			}
```

(The three-cables-two-panels count is now implied by the exact sequence; keep the explicit count loop as well if it reads better — it costs nothing and it is the assertion the original test was named for.)

- [ ] **Step 3: `TestATraceRunsBothWays` — SHAPE only**

`trace.End()` becomes the last element of `Chain()`; the assertion, the direction and the message are untouched.

- [ ] **Step 4: `TestAMisPatchedPanelTerminatesRatherThanLooping` — SHAPE only, and it must stay the test it is**

Spec §4 names this one specifically. `trace.Complete` becomes "no leaf reported `OutcomeComplete`"; `trace.Why == ""` becomes "some leaf has an empty `Why`"; `len(trace.Hops) > traceHopLimit` becomes the chain length, plus `trace.Nodes()` against the budget. The timeout goroutine, the fixture and every message stay exactly as they are.

- [ ] **Step 5: `TestAnUnpluggedPortSaysSoRatherThanReturningNothing` — SHAPE only**

`len(trace.Hops) != 0` becomes `len(trace.Root.Children) != 0`; the `Why` substring check moves to `trace.Root.Why`; `trace.Complete` becomes `trace.Root.Outcome == OutcomeComplete`. **This is the test that proves the root's own leaf reason survived the tree** — the one thing Task 5's Step 1 could have quietly dropped.

- [ ] **Step 6: The other three are unchanged**

- `TestAPassThroughMustStayInsideOnePanel` — untouched. It never traces.
- `TestUnpatchingAPanelBreaksTheRun` — untouched in meaning, and **strengthened by one line**: `trace.End()` asked whether *the* path reached the server; on a tree the honest question is whether **any** leaf does. Loop over `Leaves()`.
- `TestThePlantHoldsEveryStrandOfABreakoutInPositionOrder` (added in Task 1) — untouched.

- [ ] **Step 7: Gates.** `make lint`, `make test`. Also re-run the Task 4 Step 2 mutation once more here: the `delete(visited, ...)` calls are the thing most likely to be "cleaned up" during this task.

---

## Task 7: `PassThroughsFor` orders by position

Spec §4.6. Independent of everything above; land it any time after Task 1. **See Decision B before writing the `ORDER BY`.**

**Files:**
- Modify: `internal/store/cabling.go:290-300`
- Modify: `internal/store/cabling_test.go` (one new test)

**Interfaces:**
- `PassThroughsFor(ctx, assetID) ([]PassThroughRow, error)` — signature unchanged, order changed.
- Consumer: `internal/web/handlers/assets.go:714` → `web/templates/pages/asset_detail.html:861`. Neither changes.

- [ ] **Step 1: Order it**

```go
	err := s.read(ctx, &rows, panelPatchSelect+
		` WHERE f.asset_id = ? AND p.lifecycle <> ?
		  ORDER BY r.name, p.position, f.name`,
		assetID, domain.LifecycleRetired)
```

with the reasoning above the function:

```go
// PassThroughsFor lists the live patches inside one asset.
//
// GROUPED BY REAR PORT, THEN BY POSITION. It ordered by front-port name until
// breakout arrived, which puts strand 10 before strand 2 the moment positions
// mean anything -- and interleaves two trunks besides. This view is where
// somebody stands in front of the panel and reads the list off against the
// physical trunk, so the trunk has to be contiguous and its strands in order.
// (r.name is still text-sorted, so rear-10 precedes rear-2. A panel has a
// handful of rear ports and hundreds of strands; fixing text-sorted port names
// is a separate problem from this one.)
```

- [ ] **Step 2: A test whose fixture defeats the old ordering**

Two strands at positions 2 and 10 whose front-port names sort the other way (`f-10` before `f-2`), on both engines, asserting the returned positions ascend. Name it for what it protects: `TestPatchesAreListedInStrandOrderNotNameOrder`.

- [ ] **Step 3: Gates.** `make lint`, `make test`.

---

## Task 8: A position on the patch form — DECISION REQUIRED (Decision C)

**Do not start this without a yes.** It is not in spec §4.

**Files:**
- Modify: `web/templates/pages/asset_detail.html:886-906`
- Modify: `internal/web` — extend the breakout functional test from Task 5 Step 4 to drive the form

**Interfaces:**
- Consumes: `internal/web/handlers/cabling.go:40`'s existing `intValue(r, "position", 1)`. **No handler change and no store change** — the write path, its validation and its `change_log` entry already exist and already work.

- [ ] **Step 1: One number field beside the two selects**

```html
        <div class="field">
          <label for="pt-position">Strand</label>
          <input id="pt-position" name="position" type="number" min="1" value="1" style="width:6em">
          <p class="field-note">
            Which hole of the rear port this front port is behind. Leave it at 1
            for an ordinary panel. A twelve-fibre trunk that breaks out is one
            row per strand, numbered as the trunk is numbered — strand 7 stays
            strand 7 whether or not 6 is patched.
          </p>
        </div>
```

- [ ] **Step 2: The refusal is already correct, and should be seen once**

A second patch at the same (rear port, position) is refused by the partial unique index and surfaces through `translateWriteErr` as a flash. Check by hand what that message reads like — an index-name-shaped error on a form somebody just filled in is worth knowing about even if fixing it is separate work.

- [ ] **Step 3: Gates.** `make lint`, `make test`.

---

## Task 9: The two comments that become false, and the record

**Files:**
- Modify: `internal/domain/patch.go:19-21`
- Modify: `internal/domain/classification.go:519-521`
- Modify: `docs/panel-breakout-design.md` (§4 bullets at :270 and :286 — Decision A; and the Status line)
- Modify: `CHANGELOG.md`
- Modify: `docs/ROADMAP.md:374` (WP-B4's entry: first half delivered, breakout cables and bundles are not)

**Interfaces:** none. Prose only, and it is the prose that stops the next person re-deriving all of this.

- [ ] **Step 1: `patch.go`** — "1 for every 1:1 panel, which is all of them until breakout arrives in WP-B4" is false on delivery:

```go
	// Position is which slot of the rear port this front port takes. 1 for an
	// ordinary panel; a rear port that breaks out has one row per strand,
	// numbered as the trunk is numbered. The tracer reads it: internal/store/
	// cabling.go's walk continues into EVERY strand recorded on a rear port,
	// in position order. Declared, never derived and never renumbered -- strand
	// 7 stays strand 7 when strand 6 is unpatched.
	Position int `db:"position"`
```

- [ ] **Step 2: `classification.go`** — same sentence, same fix. The column's classification does not change: declared, somebody patched it.

- [ ] **Step 3: `docs/panel-breakout-design.md`** — strike the two stale §4 bullets (Decision A) with a one-line note saying corrected D4 supersedes them, and move the Status line off DRAFT. Do not rewrite the challenge-round history; §3's account of the first draft's error is the most useful thing in the document.

- [ ] **Step 4: `CHANGELOG.md`, under Changed.** An operator will notice: the trace page shows every recorded strand of a trunk instead of one, and reports per-end outcomes instead of a single complete/incomplete verdict. No action required, no migration.

- [ ] **Step 5: Gates.** `make lint`, `make test`.

---

## Task 10: E2E — DECISION REQUIRED, and it depends on Task 8

**Recommendation: state the skip and its reason, unless Task 8 lands.**

`tests/e2e` is read-only by default (`docs/E2E.md`) and runs against a seeded instance. **The seed contains no `port_pass_through` rows at all** — `internal/seed/seed_cabling.go` builds a 24-port panel and 24 leads and patches nothing through it — so today the demo estate cannot demonstrate a trace across a panel, let alone a breakout. A read-only spec therefore has nothing to open.

Two ways forward, both scope decisions:

1. **Skip, stated.** Task 5's functional test drives the real router and the real template. The E2E-only risks here — routing, assets, HTMX wiring — are unchanged by this work package: no new route, no new JS, no new asset. Write the skip and this reason into the PR body.
2. **Seed a breakout, then one spec.** Add a four-strand breakout to `seed_cabling.go` (the panel and its ports already exist; it is one rear port and four patches at positions 1–4) and one read-only spec that clicks *Trace* from the panel's own patching table and asserts four strands with their positions. That also makes the feature demonstrable to a client, which today it is not. It is a seed change with its own idempotency rule to respect (`seed_cabling.go:104-122`).

Whichever is chosen, **do not write a spec that skips itself when it finds no patch panel.** A runtime skip on "the thing under test appears to be missing" converts an outage into a pass.

---

## Evidence gate — write this in the PR body before any reviewer is invoked

*What would be true if this were broken, and what did you run to show it isn't?*

1. **A global `visited` would silently shorten a strand.** Ran `TestBothStrandsOfATrunkReachTheFarEnd` on both engines; mutated `expand` to share the set (removed both `delete(visited, ...)` calls), watched strand 7 report an end at `panel-b/b-f-7`, restored. Quote the failure output.
2. **A budget that dropped successors would report a shorter trunk.** `TestAFanOutPastTheNodeBudgetSaysSoOnEveryBranchItStopped` asserts the child count equals the number of rows, not the number expanded.
3. **The 1:1 run could have changed meaning while its test changed shape.** The seven pre-existing tests ran green and **unmodified** through Tasks 1–5, against the new walk (Task 3 Step 7 derives the flat fields from the tree). Only then were four of them rewritten, and `TestATraceCrossesThePanelsInTheWay` now asserts the exact hop sequence, which is stricter than the `strings.Contains` it replaced.
4. **The page could have started lying about completeness.** `internal/web/power_test.go`'s `pill-ok">complete` assertion is unmodified and still green.
5. `make test` (not `go test ./...`) on both engines, exit status quoted. `make lint` clean.

## Risks

- **The silent one is per-branch `visited`**, and no reviewer reading the diff will catch it — only the §2.3 fixture will. That is why the mutation in Task 4 Step 2 is a required step and not a suggestion.
- **Rewriting a test while its subject changes** is the risk spec §4.5 names. The mitigation is structural: the old tests are not touched until the new walk has been green under them for three tasks.
- **Template errors are runtime, not compile-time.** Removing `Trace.Complete` (Task 6) while `trace.html` still referenced it would produce a broken page and a green `go build`. Task 5 lands before Task 6 for exactly that reason, and `power_test.go` renders the page for real.
- **Suite time.** One new dual-engine fixture (`breakoutPlant`, ~10 assets) and one 520-strand in-memory fixture. Nothing else grows.
- **Reachability.** Without Task 8 the tracer can render something no user can create (Decision C). That is a product gap, not a defect, but it must be a stated one.
