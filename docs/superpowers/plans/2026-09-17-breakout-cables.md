# Breakout cables — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** A QSFP-to-4×SFP+ DAC is recordable as one cable with one end on one side and four on the other.

**Architecture:** *n* `link` rows sharing a `breakout_id`, two nullable columns, no new table. Decided 2026-09-17.

**Tech Stack:** Go 1.26, sqlx, goose, `net/http.ServeMux`, `html/template`, HTMX.

**Spec:** `docs/breakout-cables-design.md`. Read it before writing a line — the ruling, its argument against migration `00028`, and the two guards it mandates are all there.

## Global Constraints

- Placeholders are `?`; call `sqlx.Rebind`. Never `$1`.
- Every query runs unmodified on SQLite **and** PostgreSQL. `make test` is the gate.
- IDs UUIDv7 in Go; timestamps RFC3339 UTC in Go. Never in SQL.
- Every mutation of declared state writes a `change_log` row in the same transaction.
- Soft delete only.
- **A portable `ADD COLUMN` goes in BOTH dialect directories, never `migrations/shared/`.** `Migrate()` runs all of `shared/` before any dialect file, and `sqlite/00005` rebuilds `link` create-copy-drop-rename with its original column list — a shared column addition on `link` is silently dropped on fresh installs while existing databases stay fine. This cost migration `00070` a rewrite. **Verified 2026-09-17: `sqlite/00005` DOES rebuild `link`** (`ALTER TABLE link_new RENAME TO link`), exactly as it rebuilds `route`. So the trap is live for this migration, not hypothetical. Both dialect directories, no `shared/`.
- Licence header + blank line before `package`.
- `gofmt`, `go vet`, `staticcheck` clean.

---

### Task 1: Migration 00071 — the two columns and the position index

**Files:**
- Create: `internal/store/migrations/sqlite/00071_link_breakout.sql`
- Create: `internal/store/migrations/postgres/00071_link_breakout.sql`

```sql
ALTER TABLE link ADD COLUMN breakout_id       TEXT;
ALTER TABLE link ADD COLUMN breakout_position INTEGER
  CONSTRAINT link_breakout_position_check CHECK (breakout_position IS NULL OR breakout_position > 0);
CREATE INDEX idx_link_breakout ON link(breakout_id) WHERE breakout_id IS NOT NULL;
CREATE UNIQUE INDEX link_breakout_position_key
  ON link(breakout_id, breakout_position)
  WHERE breakout_id IS NOT NULL AND lifecycle <> 'retired';
```

The partial unique index is scoped to live rows for the reason `port_pass_through.position` and `backend_pool(service_id, name)` both are: a retired strand must not reserve its position forever.

- [ ] Steps: write both halves → `make test` → verify `TestEveryDialectMigrationHasBothHalves` and `TestEveryEnumConstraintIsNamed` → commit.

### Task 2: Domain — the invariants

**Files:** `internal/domain/` (whichever file owns `Link`)

- `BreakoutID *string`, `BreakoutPosition *int` on `domain.Link`.
- `Link.Validate()`: both set or both nil — a position with no group, or a group with no position, is a half-written breakout. Position > 0.
- Classify both columns in `domain.DeclaredColumns`. **`SELECT * FROM link` exists — a column with no struct field breaks `StructScan` outright.** Migration `00070` cost ~25 web tests to this exact omission.

- [ ] Steps: failing test → run → implement → run → commit.

### Task 3: Store — declaring a breakout, and the drift guard

**Files:** `internal/store/network.go`

- `CreateBreakout(ctx, p, spec)` writing *n* `link` rows in one transaction sharing a generated `breakout_id`, positions 1..n, all with the same a-end interface.
- **Relax one-patch-per-port for the breakout a-end only.** The rule is a Go predicate at `network.go:464` ("one of those ports is already patched"), NOT a unique index — verified. The b-ends stay exclusive.
- **THE DRIFT GUARD, and it ships in this commit, not a later one.** Every live `link` sharing a `breakout_id` must agree on `medium` and `length_m`. Enforce on write; prove with a test that constructs a deliberately-drifted pair and asserts the refusal. `docs/breakout-cables-design.md` D4 is explicit that the guard is proven against the specific bug, not the general shape — the WP-J9 rule, earned four times on one branch and twice more since.
- `UpdateLink` must keep a breakout's members in agreement, or refuse.

- [ ] Steps per behaviour: failing test → run → implement → run → commit.

### Task 4: The walkers

**Files:** `internal/store/` (impact), wherever `BundleCutEffect` and `LinkCutEffect` live.

Cutting one strand of a breakout cuts all of them — one connector, one assembly. Confirm against the existing walker whether that falls out for free or needs saying; if it falls out for free, write a test that would fail if it stopped.

- [ ] Steps: failing test → run → implement → run → commit.

### Task 5: Display grouping (design doc D5)

**Files:** the bundle and impact templates + their view models.

`cable_bundle_member` keys on `link_id`, so one physical breakout in a duct is four member rows and the cut page would say "4 cables". **Group by `breakout_id`** and render "1 breakout cable (4 strands)". Store keeps four rows.

**Its own guard.** A page that silently stopped grouping would overstate every breakout in it and nothing else would notice.

- [ ] Steps: failing test → run → implement → run → commit.

### Task 6: The patch form

Recording a breakout from the UI. Every posting element declares its swap target; read the handler for the class, do not guess.

---

## Evidence gate

- A drifted `medium` across one breakout's rows → refused, proven by a test that builds the drift.
- One strand cut → all strands cut.
- A duct holding a breakout reports 1 cable, not 4.
- The b-end of a breakout strand is still exclusive — only the a-end is shared.
- Mutation-test the drift guard and the display grouping.
