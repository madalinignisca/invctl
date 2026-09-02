# Group A (deferred-to-1.1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the decided-but-unbuilt "Deferred to 1.1" items in `docs/ROADMAP.md`: widen project-owner write scope to `dependency`, `link` and `asset_cost`; add the saved-view rename control; correct the stale roadmap and template claims.

**Architecture:** Every widening rides the two existing seams and adds no third. Row-level permission stays at `tx.log` via `domain.Permit.Covers`, reached through a narrow per-row minter in `internal/store` that follows `authorizeInstanceSubjects` (`internal/store/services.go:452`). Session-level permission stays in middleware. No handler learns a new authorization rule; no new `Permit` method is added.

**Tech Stack:** Go 1.22+, `jmoiron/sqlx`, `net/http.ServeMux`, `html/template`, HTMX 2.x, Alpine.js 3.x (CSP build), Playwright for E2E.

**Spec:** `docs/ROADMAP.md` "Deferred to 1.1, deliberately"; `docs/rbac-design.md` §4; `docs/AUDIT.md`. Two rulings were taken from the user on 2026-09-02 and are binding on this plan:

1. **`asset_cost` is gated across BOTH seams.** The store checks the row
   (`p.Covers("asset", assetID)`); new middleware checks the session
   (`auth.CanSeeCosts`). `domain.Permit` is NOT widened — its three-method
   width lock (`TestThePermitInterfaceCannotBeWidenedWithoutSayingSo`) stands.
2. **`certificate` and `cluster` stay Administrator-only.** Neither has a
   single owning subject (both are many-to-many with assets), and "every
   member in scope" is vacuously true for an empty cluster or an undeployed
   certificate — which would make them writable by every project owner in
   the estate. The roadmap's "no argument against" is wrong and Task 7
   corrects it. **No code change for these two.**

## Global Constraints

- Every query runs unmodified on SQLite AND PostgreSQL. Placeholders are `?`, rebound via `sqlx.Rebind`. Never `$1`.
- No forbidden Postgres-only feature (`SERIAL`, `ENUM`, native arrays, `jsonb` in `WHERE`, `NOW()` as a default, multi-row `RETURNING`, `generate_series`).
- IDs are UUIDv7 `TEXT` generated in Go. Timestamps are RFC3339 UTC `TEXT` generated in Go. Never from a DB clock.
- Every mutation of declared state writes a `change_log` row in the same transaction.
- Soft delete only. Never hard-delete an entity row.
- `internal/domain` has zero external dependencies.
- Wrap errors with `fmt.Errorf("doing x: %w", err)`. Sentinel errors are `domain.ErrNotFound` / `domain.ErrConflict` / `domain.ErrForbidden`.
- Validation failure returns **HTTP 422** with the form partial re-rendered. Mutating handlers branch on `HX-Request` through `render.Respond`.
- Every source file opens with the AGPL-3.0-only notice; in Go a **blank line** follows it before the package clause.
- `gofmt`, `go vet`, `staticcheck` clean. `make test` (both engines) green — `go test ./...` alone is NOT the gate.
- Comment for the junior who inherits this: why it is this way, what breaks otherwise, what was rejected.

### The trap every task in this plan must avoid

`tx.log` authorizes **the id of the one row being written**. That id carries no
information about the row's endpoints. Checking one end only is how the
`ReparentAsset` escalation (9d01318, 82ea6c5) happened. For an **update** that
can move an edge, BOTH the stored (old) endpoints and the submitted (new)
endpoints must be covered — otherwise a project owner submits their own id and
drags somebody else's row into their scope. Subjects for update/retire come from
the **STORED** row, never from a submitted field.

---

### Task 1: `dependency` becomes project-owner writable, two-ended

**Files:**
- Modify: `internal/domain/role.go` (the `entityScope` map)
- Modify: `internal/store/deps.go`
- Modify: `internal/store/permit_source_test.go` (`storePermitMinters`)
- Test: `internal/store/deps_scope_test.go` (create)

**Interfaces:**
- Consumes: `domain.Permit`, `domain.ScopedPermit`, `domain.ErrForbidden`, the `tx` type and its `get`/`exec`/`log*` methods.
- Produces: `authorizeDependencySubjects(ctx context.Context, q dbGetter, p domain.Permit, consumerServiceID string, providerEndpointID, providerRouteID *string, dependencyID string) (domain.Permit, error)` — used by no later task, but Task 7 documents it.

**Background the implementer needs:** a `dependency` connects a *consumer
service* to a *provider*, and the provider is either an `endpoint` or a `route`,
each of which belongs to a service. So both ends resolve to services. Read
`authorizeInstanceSubjects` in `internal/store/services.go` first — it is the
template, including its comment style.

- [ ] **Step 1: Write the failing test**

Create `internal/store/deps_scope_test.go`. Table-driven, and it must exercise
each end **independently** — a test that only ever varies both ends together
passes with a half-present fix. Cover at minimum:

```go
// A project owner scoped to service S1 only.
// case "covers neither end":        consumer S2, provider on S3 -> ErrForbidden
// case "covers consumer only":      consumer S1, provider on S3 -> ErrForbidden
// case "covers provider only":      consumer S2, provider on S1 -> ErrForbidden
// case "covers both ends":          consumer S1, provider on S1 -> nil
// case "update moves the consumer": stored consumer S1, submitted S3 -> ErrForbidden
// case "update moves the provider": stored provider on S1, submitted on S3 -> ErrForbidden
// case "administrator":             AdministratorPermit -> nil for every row
```

Assert on `errors.Is(err, domain.ErrForbidden)`, and assert the refusal wrote
**no** `change_log` row (count before and after).

- [ ] **Step 2: Run the test and watch it fail**

Run: `go test ./internal/store/ -run TestDependencyScope -v`
Expected: FAIL — `authorizeDependencySubjects` undefined.

- [ ] **Step 3: Reclassify the entity type**

In `internal/domain/role.go`, move `"dependency"` out of the `ScopeTopology`
block into the subject-derived group, with a comment on the model of
`saved_view`'s: say that a dependency's subjects are the two services it
connects, that an ordinary project-owner permit has no `dependency` bucket
(`auth.Authorizer.Permit` only ever fills asset/service/circuit), and that only
the permit minted after `authorizeDependencySubjects` can cover a row.

- [ ] **Step 4: Add the minter and wire it in**

In `internal/store/deps.go`, add `authorizeDependencySubjects`. It resolves the
provider's owning service with a single `?`-placeholder query — `SELECT
service_id FROM endpoint WHERE id = ?` or, for a route, `SELECT e.service_id FROM
route r JOIN endpoint e ON e.id = r.frontend_endpoint_id WHERE r.id = ?` (CORRECTED 2026-09-02: `route` has no `service_id` column of its own; this join matches `dependencySelect`'s existing one) depending on which pointer is non-nil — then:

```go
if !p.Covers("service", consumerServiceID) { return nil, fmt.Errorf(...ErrForbidden) }
if !p.Covers("service", providerServiceID) { return nil, fmt.Errorf(...ErrForbidden) }
return domain.ScopedPermit(p.Actor(), nil, domain.ScopedEntities{
    "dependency": {dependencyID: true},
}), nil
```

Wire it into `CreateDependency`, `UpdateDependency`, `RetireDependency` and
`VerifyDependency`. For update/retire/verify the subjects come from the
**stored** row; for update, check the stored endpoints **and** the submitted
endpoints (see "The trap" above).

**Before you finish, verify one thing and say so in your report:** the minted
permit is narrow, so any *other* entity logged inside the same transaction
would now be refused. Confirm each of those four store methods logs exactly
`dependency` and nothing else (`setDataClasses` folds into the parent's audit
value per `dependencyAudit`, so it writes no log of its own — confirm that).

- [ ] **Step 5: Register the minter**

Add `"authorizeDependencySubjects"` to `storePermitMinters` in
`internal/store/permit_source_test.go`, with a reason string in the same voice
as its neighbours. `TestOnlyTheNamedFunctionsMintAPermit` fails until you do.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/store/ ./internal/domain/ -run 'Dependency|Permit|Scope' -v`
Expected: PASS.

- [ ] **Step 7: Prove the test can fail**

Delete the `p.Covers("service", providerServiceID)` check, re-run, watch the
"covers consumer only" case go red, restore. Report the exact failure text.

- [ ] **Step 8: Commit**

One commit. The *why*: a dependency's two ends are two services, and checking
one lets a project owner point their service at anybody's.

---

### Task 2: `link` becomes project-owner writable, two-ended through interfaces

**Files:**
- Modify: `internal/domain/role.go`
- Modify: `internal/store/network.go`
- Modify: `internal/store/permit_source_test.go`
- Test: `internal/store/link_scope_test.go` (create)

**Interfaces:**
- Consumes: same as Task 1.
- Produces: `authorizeLinkSubjects(ctx context.Context, q dbGetter, p domain.Permit, aInterfaceID, bInterfaceID, linkID string) (domain.Permit, error)`.

**Background:** a `link` cables two interfaces. An interface has no project of
its own — its scope is entirely its owning asset's, exactly as
`authorizeInterfaceSubject` (`internal/store/network.go:168`) already
establishes. So a link is two assets, two hops away. `CreateLink` uses
`writeSerializable` and `RetireLink` uses `write`; both take the permit up
front, so the interface→asset resolution happens **before** the transaction
opens, the same way `RetireLink` already calls `s.GetLink` outside it. Comment
that accepted TOCTOU rather than leaving it silent.

- [ ] **Step 1: Write the failing test**

Create `internal/store/link_scope_test.go`, table-driven, each end varied
independently:

```go
// A project owner scoped to asset A1 only.
// case "covers neither asset":  A-if on A2, B-if on A3 -> ErrForbidden
// case "covers the A end only": A-if on A1, B-if on A3 -> ErrForbidden
// case "covers the B end only": A-if on A2, B-if on A1 -> ErrForbidden
// case "covers both":           A-if on A1, B-if on A1 -> nil
// case "retire uses the stored ends, not submitted": -> ErrForbidden
// case "administrator": -> nil
```

Assert `errors.Is(err, domain.ErrForbidden)` and that no `change_log` row and no
`link` row were written on refusal.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/store/ -run TestLinkScope -v`
Expected: FAIL — `authorizeLinkSubjects` undefined.

- [ ] **Step 3: Reclassify**

Move `"link"` from `ScopeTopology` to the subject-derived group in
`internal/domain/role.go`, commented as in Task 1.

- [ ] **Step 4: Add the minter and wire it in**

```go
func authorizeLinkSubjects(ctx context.Context, q dbGetter, p domain.Permit,
    aInterfaceID, bInterfaceID, linkID string) (domain.Permit, error) {
    // resolve each interface's asset with: SELECT asset_id FROM interface WHERE id = ?
    if !p.Covers("asset", aAssetID) { return nil, fmt.Errorf(...ErrForbidden) }
    if !p.Covers("asset", bAssetID) { return nil, fmt.Errorf(...ErrForbidden) }
    return domain.ScopedPermit(p.Actor(), nil, domain.ScopedEntities{
        "link": {linkID: true},
    }), nil
}
```

Wire into `CreateLink` and `RetireLink`. `RetireLink` reads the stored link
first (it already does) and takes its endpoints from that row.

- [ ] **Step 5: Register the minter** in `storePermitMinters`.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/store/ ./internal/domain/ -run 'Link|Permit|Scope' -v`
Expected: PASS.

- [ ] **Step 7: Prove the test can fail** — drop the B-end check, watch
"covers the A end only" go red, restore, report the text.

- [ ] **Step 8: Commit.** The *why*: a cable has two ends and two owners.

---

### Task 3: `asset_cost` becomes project-owner writable — the row half

**Files:**
- Modify: `internal/domain/role.go`
- Modify: `internal/store/costs.go`
- Modify: `internal/store/permit_source_test.go`
- Test: `internal/store/costs_scope_test.go` (create)

**Interfaces:**
- Produces: `authorizeCostSubject(p domain.Permit, t costTable, ownerID, costID string) (domain.Permit, error)`.

**Background and the two traps.** `costTable` (`internal/store/costs.go:47`)
drives four surfaces from one implementation: `asset_cost`, `service_cost`,
`project_cost`, `circuit_cost`. **Only `asset_cost` widens in this task.** The
other three stay `ScopeTopology` and stay Administrator-only.

Second trap: `costOnAsset` has `scoped: true` — an asset cost may declare a
consumer set in `asset_cost_consumer`, and that set names **arbitrary asset
ids**. That makes the cost line two-ended too. Every consumer asset must be
covered, or a project owner divides their own invoice across somebody else's
hardware and it lands in that project's totals.

- [ ] **Step 1: Write the failing test**

Create `internal/store/costs_scope_test.go`:

```go
// A project owner scoped to asset A1 only.
// case "cost on a foreign asset":            owner A2 -> ErrForbidden
// case "cost on their own asset":            owner A1 -> nil
// case "service_cost stays administrator-only": -> ErrForbidden
// case "project_cost stays administrator-only": -> ErrForbidden
// case "circuit_cost stays administrator-only": -> ErrForbidden
// case "consumer set names a foreign asset": owner A1, consumers [A1, A2] -> ErrForbidden
// case "consumer set is all in scope":       owner A1, consumers [A1]     -> nil
// case "administrator": -> nil everywhere
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/store/ -run TestCostScope -v`
Expected: FAIL — `authorizeCostSubject` undefined.

- [ ] **Step 3: Reclassify only `asset_cost`**

In `internal/domain/role.go` move `"asset_cost"` to the subject-derived group.
Leave `"service_cost"`, `"project_cost"` and `"circuit_cost"` exactly where they
are, and add a comment saying so explicitly and why: they attach to something
that is already the unit of attribution, and nothing has asked for them.

- [ ] **Step 4: Add the minter and wire it in**

Keep it explicit rather than clever — do **not** generalise over `t.parent`:

```go
// authorizeCostSubject narrows a permit to one asset cost line. ONLY
// asset_cost widens ... (explain: the other three cost tables remain
// ScopeTopology, so even if a permit were minted for them Covers would
// refuse it downstream -- but relying on that would be a trapdoor, so
// this function refuses them here, where a reader can see it.)
func authorizeCostSubject(p domain.Permit, t costTable, ownerID, costID string) (domain.Permit, error) {
    if t.entity != costOnAsset.entity {
        return nil, fmt.Errorf("writing a %s line: %w", t.entity, domain.ErrForbidden)
    }
    if !p.Covers("asset", ownerID) { ... ErrForbidden }
    return domain.ScopedPermit(p.Actor(), nil, domain.ScopedEntities{
        costOnAsset.entity: {costID: true},
    }), nil
}
```

**Careful:** that early refusal must not break Administrators writing service,
project or circuit costs. So `addCost`/`updateCost`/`retireCost` must call
`authorizeCostSubject` only when `t.entity == costOnAsset.entity`, and pass the
caller's own permit through untouched otherwise. Write that branch once, at the
top of each method, and comment why an Administrator is unaffected.

Then extend `setCostConsumers` (`internal/store/costs.go:452` area) to check
`p.Covers("asset", consumerAssetID)` for every id in the submitted set, before
the `DELETE`/`INSERT` pair. This runs against the caller's ORIGINAL permit, not
the narrowed one — state that in a comment, because it is the only reason the
check can work at all.

- [ ] **Step 5: Register the minter** in `storePermitMinters`.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/store/ ./internal/domain/ -run 'Cost|Permit|Scope' -v`
Expected: PASS.

- [ ] **Step 7: Prove the test can fail** — remove the consumer-set check,
watch "consumer set names a foreign asset" go red, restore, report the text.

- [ ] **Step 8: Commit.** The *why*: a cost line's subject is the asset it is
attached to, and its consumer set is a second set of subjects.

---

### Task 4: `asset_cost` — the session half, and the new route registrar

**Files:**
- Modify: `internal/web/middleware/` (the file holding `RequireWrite`)
- Modify: `internal/web/routes.go`
- Modify: `internal/web/routescan/routescan.go:163`
- Modify: `internal/web/rbac_boundary_test.go` (the pinned gate counts)
- Test: `internal/web/middleware/` cost-visibility test (create or extend)

**Interfaces:**
- Consumes: `auth.Authorizer.CanSeeCosts(user)` (`internal/auth/auth.go:309`).
- Produces: `middleware.RequireCostVisibility(next http.Handler) http.Handler`; route registrar `writeCost` in `routes.go`.

**Background.** `CanSeeCosts` is a grant **separate from role**: an
Administrator always passes; an Observer or a project owner passes only if
`app_user.can_see_costs` is set. It is a question about a *session*, so it
belongs in middleware beside `RequireWrite` — `tx.log` cannot answer it, because
`domain.Permit` has no cost dimension and its width is locked.

**ADDED 2026-09-02, from the Task 3 authorization review — this task is not
done without it.** `CanSeeCosts` is already bypassed by the audit trail. Probe,
confirmed on both engines: log in as an Observer with **no** cost grant, request
`GET /changes?entity_type=asset_cost`, and the diff table renders
`amount_minor: 840000` in plain sight. `GET /changes` is a `read(...)` route and
neither `ChangeLog`/`ChangeEntry` (`internal/web/handlers/misc.go:271`) nor the
templates consult `CanSeeCosts`.

So cost figures — and every cost id — are universally readable today. Adding
`RequireCostVisibility` to the write routes while that stands would be theatre:
it would stop a person writing a number they can already read. Close the read
path in this task. The change-log view must redact, for a viewer without the
grant, the amount fields of any entry whose `entity_type` is one of
`asset_cost`, `service_cost`, `project_cost`, `circuit_cost`.

Redact rather than hide the entry: the audit trail's integrity is that every
change is present and countable. A viewer without the grant should see that a
cost line changed, when, and by whom — not what the number was. Hiding the rows
entirely would make the same log show different histories to different people,
which is a worse property than withholding a figure.

There are 16 cost write routes (`internal/web/routes.go:382-394` and `462-465`).
**Put all 16 behind the new registrar, not just the five asset ones.** Writing a
number you are not allowed to read is a blind write on every surface; for
Administrators this changes nothing, so there is no regression to weigh.

- [ ] **Step 1: Write the failing test**

Assert, for a project owner with `can_see_costs = false`, that `POST
/assets/{id}/costs` is refused; and that the same person with `can_see_costs =
true` and the asset in scope is not refused by *this* middleware. Assert an
Administrator is never refused by it.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/web/... -run CostVisibility -v`
Expected: FAIL — `RequireCostVisibility` undefined.

- [ ] **Step 3: Add the middleware**

Model it exactly on `RequireWrite` — same signature shape, same refusal status
and body text convention. **Order matters:** the registrar composes
`RequireWrite` FIRST, then `RequireCostVisibility`, so an Observer still gets
RequireWrite's refusal text. `rbac_boundary_test.go` reads that text to decide
which layer refused, and will report the wrong layer if you invert them.

- [ ] **Step 4: Add the registrar and move the routes**

```go
// writeCost gates the money surfaces on BOTH seams: RequireWrite for "may
// this session write at all", RequireCostVisibility for "may it see money".
// ... explain why this is not RequireWrite alone ...
writeCost := func(pattern string, h http.HandlerFunc) { ... }
```

Move all 16 cost routes onto it. Then add `"writeCost"` to the disjunction at
`internal/web/routescan/routescan.go:163` and extend that file's `Gate` doc
comment, which currently enumerates the three registrars by name.

- [ ] **Step 5: Fix the pinned counts**

`internal/web/rbac_boundary_test.go` pins gate counts (`administratorGate != 14`,
`permitGate != 9`, `adminGate != 0` around lines 900-923). Moving 16 routes will
move these. **Do not just re-pin to whatever the run prints** — work out what
each number *should* be, update the explanatory comment above it to match, and
say in your report what moved and why.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/web/... -v`
Expected: PASS.

- [ ] **Step 7: Prove the test can fail** — make `RequireCostVisibility` return
`next.ServeHTTP` unconditionally, watch the project-owner case go red, restore.

- [ ] **Step 8: Commit.** The *why*: writing a number you may not read is a
blind write, and the session is the only seam that can see that grant.

---

### Task 5: the saved-view rename control

**Files:**
- Modify: `internal/web/handlers/savedviews.go`
- Modify: `internal/web/routes.go`
- Modify: `web/templates/partials/saved_views.html`
- Modify: `web/static/app.js`
- Test: `internal/web/handlers/savedviews_test.go` (extend)

**Interfaces:**
- Consumes: `store.UpdateSavedView(ctx, p, v)` — already implemented and tested (`internal/store/savedviews_test.go`); it authorizes through `authorizeSavedViewOwner`, which reads the row's **stored** owner and has no Administrator exception.
- Produces: handler `SavedViewRename`; route `POST /views/{id}/rename` on the `self` registrar.

**Background.** WP-G4b Wave B removed `POST /views/{id}` and its
`SavedViewUpdate` handler because nothing posted to them, and a mutating route
with no caller is unreviewed surface. This task re-adds the surface **with** its
caller. Rename changes the name only — `params` and `entity` are not
submittable, because `savedViewParamsFrom` reading a form-supplied `entity`
selected the allowlist that gates `params`, which is exactly the bug WP-G4b
Task 4 fixed. Take the entity from the **stored** row.

Saved views are private, including from Administrators, so this goes on the
`self` registrar (`RequireAuth` only) exactly as `POST /views` and
`POST /views/{id}/retire` do. Alpine here is the CSP build: `x-data` names a
**registered component** and is never evaluated, so no arguments in the
attribute — values arrive as `data-*` read via `this.$el.dataset.*`, and all JS
lives in `web/static/app.js` under `alpine:init`.

- [ ] **Step 1: Write the failing tests**

In `internal/web/handlers/savedviews_test.go`:

```go
// case "renames own view":              200/HX-Redirect, name changed, change_log row written
// case "blank name":                    422, form partial re-rendered, name unchanged
// case "name too long":                 422
// case "another person's view":         403 (ErrForbidden from authorizeSavedViewOwner)
// case "administrator renaming another person's view": 403 -- no exception
// case "params and entity are not submittable": post entity=service on an
//      asset view, assert the stored entity is still asset
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/web/handlers/ -run SavedViewRename -v`
Expected: FAIL — `SavedViewRename` undefined.

- [ ] **Step 3: Add the handler**

Read the name from the form, load the stored view, set only `Name` and the
Go-generated `UpdatedAt`, call `store.UpdateSavedView`. Validation failure →
`renderSavedViewsInvalid` (the existing 422 path). Map `domain.ErrForbidden` →
403 and `domain.ErrNotFound` → 404; never return a raw driver error.

- [ ] **Step 4: Register the route**

`self("POST /views/{id}/rename", app.SavedViewRename)` in `routes.go`, beside
the other two `self` routes.

- [ ] **Step 5: Add the UI control**

A rename affordance in `web/templates/partials/saved_views.html` inside the
existing `savedViews` Alpine component, and whatever `close()`/latch
participation it needs in `web/static/app.js` so it cooperates with the
disclosure latch added by 5737b2d. Keep the partial standalone-renderable. No
inline `<script>`; nothing longer than an `x-data` attribute.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/web/... -v`
Expected: PASS.

- [ ] **Step 7: Prove the test can fail** — make the handler take `entity` from
the form, watch the "not submittable" case go red, restore.

- [ ] **Step 8: Commit.** The *why*: the store method existed and was tested;
this gives it the caller whose absence got the old route deleted.

---

### Task 6: the `.CanWrite` template sweep

**Files:**
- Modify: `web/templates/**` (whichever files the census finds)

**Background.** Templates gate controls on `.CanWrite`, which since WP-G1
admits project owners. Where the server would still refuse a project owner, the
template offers a control that 403s — cosmetic, but it teaches an operator that
the app is broken. **This task runs after Tasks 1-4 deliberately:** those widen
what a project owner may actually write, so some current `.CanWrite` uses become
correct and must NOT be changed. The roadmap's figure of "132 occurrences" is
stale — `web/templates/pages/asset_list.html` now has zero.

- [ ] **Step 1: Take the census**

Run: `grep -rn '\.CanWrite' web/templates/ | wc -l` and record the real number.
Then, for each file, decide against the post-Task-4 entity classification in
`internal/domain/role.go` whether a project owner can in fact write that thing.

- [ ] **Step 2: Report before changing anything**

Write the census to your report file as a table — file, line, entity type,
scope class, verdict (correct / should be `.IsAdmin`). **Do not edit yet.**
If the census finds more than ~20 files needing change, say so and stop: that
is a bigger piece of work than this task was sized for and the controller will
re-scope it.

- [ ] **Step 3: Make the changes**

Change only the occurrences the census marked wrong.

- [ ] **Step 4: Verify**

Run: `make lint && go test ./internal/web/... -v`
Expected: PASS. Templates that fail to parse fail loudly here.

- [ ] **Step 5: Commit.** The *why*: a control that always 403s is a lie told
to an operator.

---

### Task 7: documentation, and the corrections this plan discovered

**Files:**
- Modify: `docs/ROADMAP.md`
- Modify: `docs/ROLES.md`
- Modify: `docs/rbac-design.md`
- Modify: `docs/AUDIT.md` (the column/entity classification table)
- Modify: `CHANGELOG.md`

**Background.** This repo has now found five stale rules, each a document
accurate everywhere except the sentence some change falsified. Three more are
known and this task fixes them.

- [ ] **Step 1: `docs/ROADMAP.md`**

  - Strike **"No test drives a write route unauthenticated"** — done in `f232a63`.
  - Strike **"Tighten the fact-deleting allowlist"** — done in `79e14f2`.
  - Rewrite the **project-owner write scope** entry: `dependency`, `link` and
    `asset_cost` are done; record the two-seam split for `asset_cost` and why
    `Permit` was not widened.
  - **Correct the `certificate`/`cluster` line.** "No argument against, just not
    needed for 1.0" is wrong. There IS an argument: neither has a single owning
    subject, and "every member in scope" is vacuously true for an empty cluster
    or an undeployed certificate, which would make them writable by every
    project owner. Record that, so 1.2 does not rediscover it.
  - Fix the stale `.CanWrite` figure to whatever Task 6's census found.
  - Mark the saved-view rename done.

- [ ] **Step 2: `docs/ROLES.md` and `docs/rbac-design.md`**

Add `dependency`, `link` and `asset_cost` to what a project owner may write,
including the two-ended rule for each and the `can_see_costs` gate for costs.
State plainly that a project owner who cannot see costs cannot write them.

- [ ] **Step 3: `docs/AUDIT.md`**

Update the classification table for the four reclassified entity types
(`dependency`, `link`, `asset_cost`) so it matches `entityScope`.

- [ ] **Step 4: `CHANGELOG.md`**

The cost middleware narrows behaviour for a deployment where a project owner has
write but not `can_see_costs`. That is a documented behaviour change, the same
way `CanSeeCosts`'s own narrowing was.

- [ ] **Step 5: Commit.**

---

### Task 8: end-to-end browser tests

**Files:**
- Modify: `tests/e2e/specs/rbac-project-owner-edit-boundary.spec.js`
- Create: `tests/e2e/specs/saved-view-rename.spec.js`

**Background.** Read `tests/e2e/`'s own test doc before running anything — these
suites have prerequisites that fail in ways which look nothing like the cause
(unbuilt vendor assets, a required canonical host in the baseURL, rate limiters
that break consecutive runs). The existing project-owner specs show the fixture
and login pattern; follow them.

**A runtime skip is a bug here.** A test that skips because the thing under test
appears missing cannot fail when the feature breaks. Only an explicitly declared
precondition (an env-var opt-in) may skip at runtime. "The page didn't load so I
skipped" is not acceptable.

- [ ] **Step 1: Extend the project-owner boundary spec**

Add, as real browser flows:
  - a project owner cables two interfaces on **their own** assets → succeeds
  - the same person attempts a cable with one end on a **foreign** asset → refused
  - with `can_see_costs = true`, adds a cost line to their own asset → succeeds
  - with `can_see_costs = false`, the cost control is absent, and posting anyway
    is refused

- [ ] **Step 2: Create the rename spec**

`tests/e2e/specs/saved-view-rename.spec.js`: save a view, rename it, reload,
assert the new name persists and the old one is gone.

- [ ] **Step 3: Run the suite**

Run the full Playwright suite, not only the new specs. Report the pass/fail
count and every failure with what it means, not raw logs.

- [ ] **Step 4: Prove a new test can fail**

Break one thing the new specs guard (e.g. revert the rename route registration),
watch the spec go red, restore. Report the failure text.

- [ ] **Step 5: Commit.**

---

## Final gate

- [ ] `make lint` — zero findings
- [ ] `make test` — green on **both** engines. Run it BACKGROUNDED; it needs
      ~750s and the foreground tool cap is 600s. Never run two concurrently:
      they share one Postgres container and both results become untrustworthy.
- [ ] Full Playwright suite green
- [ ] Evidence gate: for each task, what would be true if this were broken, and
      what was run to show it isn't. "CI is green" is not an answer.
