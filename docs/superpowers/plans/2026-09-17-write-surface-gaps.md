# Write-surface gaps — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Every entity a person can create can also be corrected and withdrawn, closing `writeSurfaceGaps` to empty.

**Architecture:** Seven entities, three shapes. Three need only a correction; two need correction and withdrawal against columns that already exist; two are legacy tables that need a migration before either is possible.

**Tech Stack:** Go 1.26, sqlx, goose, `net/http.ServeMux`, `html/template`, HTMX.

**Spec:** `docs/ROADMAP.md` WP-A3 / the `writeSurfaceGaps` map in `internal/store/write_surface_test.go`, whose own comment says *"empty is the goal"*. Scoping decision 2026-09-17: correction **and** withdrawal for all seven.

## Global Constraints

Copied verbatim from `CLAUDE.md`; every task's requirements include these.

- Placeholders are `?`; call `sqlx.Rebind`. Never write `$1`.
- Every query runs unmodified on SQLite **and** PostgreSQL. `make test` is the gate, not `go test ./...`.
- IDs are UUIDv7 `TEXT` generated in Go. Timestamps are RFC3339 UTC `TEXT` generated in Go. **Never** generate either in SQL.
- Booleans are `TRUE`/`FALSE` literals, never `0`/`1`.
- Enums are `TEXT` + `CHECK (col IN (...))` plus a matching Go constant set.
- Every mutation of declared state writes a `change_log` row **in the same transaction**.
- Soft delete only: `lifecycle = 'retired'`. Never `DELETE` an entity row.
- Constructors validate, and `Validate()` must be reachable **without** the constructor — `UpdateX` is handed an existing value and the DB `CHECK` must not be the first line of defence. See `Environment.Validate`'s doc comment.
- Validation failure returns **422** with the form partial re-rendered. Handler branches on `HX-Request` via `render.Respond`.
- Non-GET routes behind CSRF and `RequireWrite`.
- Optimistic concurrency: every editable entity carries `row_version`; `UpdateX` pins it and returns 409 on mismatch (`requireVersion`).
- Every element that posts declares its swap target (`TestEveryPostingElementDeclaresItsSwapTarget`).
- Licence header + blank line before `package` on every new file.
- `gofmt`, `go vet`, `staticcheck` clean.

---

## The seven, and why they are not one job

| Entity | Table | Has today | Needs | Migration |
|---|---|---|---|---|
| Aggregate | `aggregate` | Create, Retire | **Update** | no |
| ASN | `asn` | Create, Retire | **Update** | no |
| L2VPN | `l2vpn` | Create, Retire | **Update** | no |
| RIR | `rir` | Create | **Update, Retire** | no — `lifecycle` exists |
| VLANGroup | `vlan_group` | Create | **Update, Retire** | no — `lifecycle` exists |
| BackendPool | `backend_pool` | Create | **Update, Retire** | **yes** |
| Route | `route` | Create | **Update, Retire** | **yes** |

`backend_pool` and `route` are the two oldest tables in this set (`shared/00004_dependencies.sql`) and predate every convention this repo now has. Neither carries `lifecycle`, `row_version`, `created_at` or `updated_at`:

```sql
CREATE TABLE backend_pool (
  id           TEXT PRIMARY KEY,
  service_id   TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  lb_algorithm TEXT,
  UNIQUE (service_id, name)
);

CREATE TABLE route (
  id                   TEXT PRIMARY KEY,
  frontend_endpoint_id TEXT NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  match_type           TEXT NOT NULL CHECK (match_type IN ('sni','host_header','path_prefix','default')),
  match_value          TEXT,
  backend_pool_id      TEXT NOT NULL REFERENCES backend_pool(id),
  tls_termination      TEXT CHECK (tls_termination IN ('passthrough','terminate','reencrypt')),
  priority             INTEGER NOT NULL DEFAULT 100
);
```

---

### Task 1: Migration 00070 — bring `backend_pool` and `route` up to convention

**Files:**
- Create: `internal/store/migrations/shared/00070_backend_pool_route_lifecycle.sql`
- Test: `internal/store/migration_test.go` (existing portability tests must still pass)

**Interfaces:**
- Produces: `lifecycle`, `row_version`, `created_at`, `updated_at` on both tables; a live-scoped unique index on `backend_pool(service_id, name)`.

**Shared, not per-dialect**, because every statement here is portable: `ALTER TABLE … ADD COLUMN` with a constant default, and a partial unique index, both of which SQLite and PostgreSQL accept in the same spelling. Put it in `shared/` and `TestEveryDialectMigrationHasBothHalves` stays satisfied without two copies to drift apart.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
ALTER TABLE backend_pool ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT backend_pool_lifecycle_check CHECK (lifecycle IN ('active','retired'));
ALTER TABLE backend_pool ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
-- A LITERAL, not a DB clock. CLAUDE.md forbids NOW()/CURRENT_TIMESTAMP as a
-- column default, and these rows genuinely predate the column: stamping them
-- with the migration's own date is the honest answer, and it is the same one
-- 00019 used when row_version arrived.
ALTER TABLE backend_pool ADD COLUMN created_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';
ALTER TABLE backend_pool ADD COLUMN updated_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';

ALTER TABLE route ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT route_lifecycle_check CHECK (lifecycle IN ('active','retired'));
ALTER TABLE route ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE route ADD COLUMN created_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';
ALTER TABLE route ADD COLUMN updated_at TEXT NOT NULL DEFAULT '2026-09-17T00:00:00Z';

CREATE INDEX idx_backend_pool_lifecycle ON backend_pool(lifecycle);
CREATE INDEX idx_route_lifecycle        ON route(lifecycle);
```

**THE UNIQUE CONSTRAINT IS THE HARD PART, AND IT IS WHY THIS TASK IS ITS OWN TASK.**
`backend_pool` carries `UNIQUE (service_id, name)` as a table constraint. Once
withdrawal exists that constraint is wrong in the way migration `00003`
documents for `identity`: a retired pool keeps its name reserved forever, so a
service can never re-declare a pool under the name it just withdrew. It must
become a **partial unique index scoped to live rows**.

SQLite cannot drop a table constraint. So on SQLite this is the
create-copy-drop-rename dance (`00005` has the pattern), and on PostgreSQL it is
`ALTER TABLE … DROP CONSTRAINT`. **That single difference means the unique-index
change is NOT portable and must be split per dialect**, even though the column
additions above are. Put the column additions in `shared/` and the constraint
swap in `sqlite/` + `postgres/` under the same number.

- [ ] **Step 2: Verify both engines migrate from empty and from populated**

Run: `make test` — `TestEveryDialectMigrationHasBothHalves` and the migration
portability suite.
Expected: PASS on both engines.

- [ ] **Step 3: Commit**

---

### Task 2: Domain validators for all seven

**Files:**
- Modify: the `domain` file owning each type (`registry.go` for RIR/ASN/Aggregate, `vlans.go`, `l2vpn.go`, `dependency.go` for BackendPool/Route — confirm by grep before editing)

**Interfaces:**
- Produces: `func (x *T) Validate() error` returning `*ValidationError` for each of the seven, plus each `NewT` delegating to it.

**The lesson this repeats, and it must be read before writing the code:**
`Environment.Validate`'s doc comment records that the checks used to live inside
`NewEnvironment`, so `UpdateEnvironment` wrote whatever it was handed and the
table `CHECK` was the only thing between a form and a blank name. Every
validator here must be reachable **without** the constructor, and each gets the
test `TestValidateIsReachableWithoutTheConstructor` already written for
`Identity` (`internal/domain/identity_test.go`) as its model: build an existing
value, corrupt one field, assert `Validate()` names that field.

- [ ] Steps: failing table-driven test per entity → run → implement → run → commit.

---

### Task 3: Store corrections — `UpdateX` for all seven

**Files:**
- Modify: `internal/store/registry.go`, `internal/store/vlans.go`, `internal/store/l2vpn.go`, `internal/store/dependencies.go` (confirm owners by grep)

**Interfaces:**
- Consumes: Task 2's `Validate()`.
- Produces: `UpdateAggregate`, `UpdateASN`, `UpdateL2VPN`, `UpdateRIR`, `UpdateVLANGroup`, `UpdateBackendPool`, `UpdateRoute`, each `(ctx, p domain.Permit, x *domain.T) error`.

**Pin what correction may not change.** `UpdateLink` and `UpdateNetGroup` are the
models: read the stored row first, pin `lifecycle` and every identity-bearing
foreign key from it, and write only the descriptive attributes. Specifically —

- `UpdateRoute` pins `frontend_endpoint_id` and `backend_pool_id`. Re-pointing a
  route at a different pool is a different act from correcting its match value,
  and doing it through a correction form would be a seizure surface.
- `UpdateBackendPool` pins `service_id`.
- `UpdateASN` — the AS number itself: **correctable**, because the roadmap entry
  says "a mistyped AS number is withdraw-and-redeclare" and that is the gap being
  closed. Uniqueness still applies.

Each pins `row_version` and calls `requireVersion`. Each ends with
`t.logUpdate(...)` in the same transaction.

- [ ] Steps: failing test → run → implement → run → commit, per entity.

---

### Task 4: Store withdrawals — `RetireX` for the four that lack one

**Files:**
- Modify: same files as Task 3.

**Interfaces:**
- Produces: `RetireRIR`, `RetireVLANGroup`, `RetireBackendPool`, `RetireRoute`.

**Refuse rather than cascade, and say what is in the way.** `RetirePrefix` and
`RetireIPAddress` (migration `00064`) are the model: they refuse while a child
prefix, an address or a reservation still exists, naming the blocker, rather
than retiring it underneath somebody. Apply the same:

- `RetireRIR` refuses while a live `aggregate` references it.
- `RetireVLANGroup` refuses while a live `vlan` references it.
- `RetireBackendPool` refuses while a live `route` points at it, **and** the pool
  has live `backend_member` rows — a pool whose members are still declared is
  still in service.
- `RetireRoute` refuses while a live `dependency` names it via
  `provider_route_id`.

**A second withdrawal is not a second audit entry.** Follow `RetireIdentity`:
if it is already retired, return without writing a `change_log` row that would
claim a withdrawal that did not happen.

- [ ] Steps: failing test per refusal path → run → implement → run → commit.

---

### Task 5: The web surface

**Files:**
- Modify: `internal/web/routes.go`, the relevant handler files, `web/templates/partials/*.html`

**Interfaces:**
- Consumes: Tasks 3 and 4.
- Produces: `POST /{resource}/{id}` (correct) and `POST /{resource}/{id}/retire` (withdraw) for each of the seven, behind `write(...)`.

Row controls for Edit and Withdraw, rendered only when the caller's permit
covers the row. Every posting element declares `hx-target`/`hx-swap` — class 1
(`#<partial id>` + `outerHTML`) where the handler re-renders a fragment, class 2
(`this`/`none`) where it only redirects. Read the handler; do not guess.
`docs/htmx-swap-targets-design.md` §2b is the rule.

Validation failure re-renders the form partial with **422**.

- [ ] Steps per entity: route → handler → template → E2E-less functional test → commit.

---

### Task 6: Close the census

**Files:**
- Modify: `internal/store/write_surface_test.go`

- [ ] Delete all seven entries from `writeSurfaceGaps` and from
  `writeSurfaceUnbuilt`. The map's comment says *"empty is the goal"* — it is
  now reachable. **Do not delete the maps themselves**: they are two-directional
  and an empty map still fails when a new entity arrives without a surface.
- [ ] Update the comment to record that the list emptied on 2026-09-17 and what
  it cost, so the next person adding an entity knows the bar.
- [ ] Run `make test`. Expected: green on both engines, and
  `TestEveryCreatedEntityCanBeCorrectedOrWithdrawn` passing with an empty gap map.

---

## Evidence gate

Before review, state: **what would be true if this were broken, and what was run to show it isn't.**

- A correction that silently changed a pinned foreign key → test that `UpdateRoute` handed a different `backend_pool_id` writes the stored one.
- A withdrawal that cascaded → test that each refusal path refuses and names the blocker.
- A retired pool whose name stays reserved → test that a service can re-declare a pool under a withdrawn name.
- A 409 that never fires → test a stale `row_version` on each of the seven.
- Mutation-test at least the census change: reinstate one gap entry and watch it fail.
