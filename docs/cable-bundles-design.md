# Cable bundles — design and plan

**WP-B4's bundle half.** Decisions taken with Gabriel 2026-09-14.
Breakout cables are NOT in scope — see "Not this work" below.

## What a bundle is

Cables somebody pulled together and will replace together: a duct, a tray, a
trunk. A bundle groups `link` rows. It is a fact somebody declares about
cables, not a property derived from them.

`docs/panel-breakout-design.md` §5 deferred this deliberately and argued it is
**independent of breakout** and **well served by the existing parent-plus-member
precedent** — `cluster_member` and five siblings. It touches the tracer not at
all. That argument is why this half can be built while the other cannot.

## Why it earns its place

`GET /links/{id}/impact` already answers "if this cable is cut, what goes dark".
For a cable in a duct that answer is usually wrong by omission: a backhoe does
not pick one strand. A bundle is what makes the honest answer available — **and
without that consumer a bundle is a label nothing reads**, which is the shape of
a feature nobody uses.

## Schema

```sql
CREATE TABLE cable_bundle (
  id          TEXT NOT NULL PRIMARY KEY,   -- NOT NULL explicitly: SQLite's
                                           -- PRIMARY KEY does not imply it
  code        TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CONSTRAINT cable_bundle_lifecycle_check
                CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX cable_bundle_code_key ON cable_bundle(code)
  WHERE lifecycle = 'active';

CREATE TABLE cable_bundle_member (
  bundle_id TEXT NOT NULL REFERENCES cable_bundle(id),
  link_id   TEXT NOT NULL REFERENCES link(id),
  PRIMARY KEY (bundle_id, link_id)
);
-- ONE BUNDLE PER CABLE. A bundle models the run a cable was physically pulled
-- in, and a cable is in one of those. It makes "what else goes with this" a
-- single unambiguous answer rather than a union the impact page would have to
-- explain, and an accidental double-add a refusal rather than a quietly
-- confusing result.
CREATE UNIQUE INDEX cable_bundle_member_link_key ON cable_bundle_member(link_id);
```

The code index is scoped to live rows, the shape migration `00064` established:
a withdrawn bundle must not reserve its name for ever.

## Rules

**Membership is a SET, replaced wholesale, folded into the bundle's audited
value.** CLAUDE.md is explicit that a set replacement producing no diff on the
parent is a failure this repo has made **three times**, twice on rows deciding
audit scope. `assetAudit` and `dependencyAudit` are the shape. This gets its own
test rather than trusting the pattern.

**Retiring a bundle retires nothing else.** "We stopped managing these together"
is not "somebody pulled the cables". Cascading would write `change_log` entries
attributing cable removals to whoever withdrew the grouping — the misattribution
`RetireInterface` and `RetireEnvironment` both refuse.

**A retired link stays in its bundle.** Membership records what was pulled
together; a withdrawn cable is still part of that history, and removing it
silently would rewrite it. The impact view filters live links itself.

## The cut

`cutEffect` (`internal/store/graph.go:547`) already takes a predicate over
`impact.NetUplinkInfo`, and `NetUplinkInfo.LinkID` names the cable an edge was
derived from — WP-B3's engine half. `LinkCutEffect` passes `u.LinkID == linkID`.

So `BundleCutEffect` is the same call with a set predicate: `u.LinkID ∈ bundle`.
**One walker, two predicates** — not a second implementation. The same argument
`RetirePrefix` makes for reusing `ListPrefixTree`.

`GET /links/{id}/impact` gains: which bundle this cable is in, and what else
shares its fate.

## Not this work

**Breakout cables.** A QSFP-to-4×SFP+ DAC is one cable with one end on one side
and four on the other, and `link` has exactly two foreign-key columns.
`docs/panel-breakout-design.md` §5 says that decision "deserves its own
document", and migration `00028` already **considered and rejected** NetBox's
polymorphic `front_port`/`rear_port` shape — so the design has to argue against
a standing precedent, not merely pick a table. Its own piece of work.

**"Cable profiles"** appears in WP-B4's title and nowhere else. In §5 it is one
of the *implementation options* for breakout cables ("*n* `link` rows sharing a
profile id"), not a separate deliverable. Nothing is dropped by not building it.

---

# Plan

Four tasks. Global constraints are CLAUDE.md's, in full — `?` placeholders and
both engines, `change_log` on every declared mutation, soft delete only,
constructors validate, 422 with the form re-rendered on refusal, `hx-confirm`
needs `hx-post`, AGPL headers with `git add` before the licence scan, no new
dependency.

### Task 1 — migration `00068`, entity, classification
Both engines, byte-identical. `domain.CableBundle` + `NewCableBundle(id, spec,
now)` taking a spec (the positional-signature lesson from `00067`). Classify
every column in `internal/domain/classification.go` — declared — and add
`cable_bundle` to `entityScope` **and** `docs/AUDIT.md`'s write-authorization
table, which `TestTheWriteScopeTableMatchesEntityScope` cross-checks. Scope:
`ScopeTopology` — and **not** for the reason this plan first
gave. It said "like `link` itself"; `link` is actually `ScopeSubjectDerived`,
resolving a subject through both cabled assets. A bundle has no such subject:
it is many-to-many via `cable_bundle_member`, and "every member in scope" is
**vacuously true for an empty bundle**, which would make a new empty one
writable by every project owner. That is the reasoning `docs/AUDIT.md` already
records for `cluster` and `certificate`, both `ScopeTopology` for it. Corrected
2026-09-14 during execution, after Task 1 checked the analogy and found it
false.

### Task 2 — store CRUD and membership
`CreateBundle`, `UpdateBundle`, `RetireBundle`, `SetBundleMembers`,
`ListBundles`, `GetBundle`, `BundleForLink`. The set write folds into the
bundle's audited value; a test asserts a membership change produces a diff on
the parent. The one-bundle-per-cable index refuses a second add with a field
message, not a 500.

### Task 3 — `BundleCutEffect` and the impact page
Reuse `cutEffect` with a set predicate. `GET /links/{id}/impact` names the
bundle and what shares its fate; a bundle gets its own cut view. A test pins
that a bundle's cut and the cut of each of its cables agree about the edges
removed — one walker, so they cannot disagree, and the test says so.

### Task 4 — the bundle UI, and close WP-B4's bundle half
List and detail, create/correct/withdraw, membership editing. Roadmap entry
updated to say the bundle half is done and **breakout cables are not** — that
entry's neighbours have been mismarked three times and precision is the point.
