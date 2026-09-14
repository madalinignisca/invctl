# Device-type component templates — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `device_type` carries the components every instance of that model has; creating an asset of that type brings them into existence, and an existing asset can have the missing ones added on request.

**Architecture:** One new table, `device_type_component`, with a `kind` discriminator. Instantiation hangs off `insertAsset` — the single chokepoint every creation path already routes through, including both importers. Range entry expands to individual rows in a pure domain function. Drift between an asset and its type is reported as an estate finding rather than silently corrected.

**Tech Stack:** Go 1.26, `jmoiron/sqlx` with hand-written SQL, `pressly/goose/v3` migrations (dialect-split), `html/template` + HTMX, SQLite and PostgreSQL.

**Spec:** `docs/device-type-templates-design.md`

## Global Constraints

Every task's requirements implicitly include these. They are copied from `CLAUDE.md` and the spec, not summarised.

- **Placeholders are `?`.** Call `sqlx.Rebind` before execution. Never write `$1`. `TestNoQueryIsDialectSpecific` reads all SQL out of the Go source and refuses dialect-specific constructs whether or not a test reaches them.
- **Every query runs unmodified on both engines.** `make test` is the gate and runs both. `go test ./...` alone is NOT the gate — with `INV_TEST_POSTGRES_DSN` unset the Postgres half is skipped.
- **IDs are UUIDv7 as `TEXT`**, generated in Go via `store.NewID()`, never by the database.
- **Timestamps are RFC3339 UTC as `TEXT`**, generated in Go. Never `NOW()`.
- **Enums are `TEXT` with a named `CHECK (col IN (...))`** plus a matching Go constant set.
- **Every mutation of declared state writes a `change_log` row in the same transaction.** No exceptions. A set write producing no audit entry is a failure this repo has made three times.
- **Soft delete only.** `lifecycle = 'retired'`. Never `DELETE` an entity row.
- **Constructors validate.** `domain.NewX` returns an error for an invalid value; the DB `CHECK` is the second line of defence.
- **A refused form returns 422 with the form re-rendered and the typed input intact** — `refusalStatus(err)` and `rejected(r, id, messages, fields...)`. `internal/web/handlers/refusal_status_test.go` AST-scans for this and fails a handler that flashes-and-redirects a refusal.
- **Every edit form carries its `row_version`** (`TestEveryEditFormCarriesItsVersion`).
- **`hx-confirm` only fires on an htmx-driven request** — it needs `hx-post` beside it or it is decorative.
- **New files carry the AGPL-3.0-only header**, blank line before `package`. **`git add` before running `./internal/license/`** — that scan reads `git ls-files` and silently skips untracked files.
- **No new dependency.**
- Postgres for local runs: `INV_TEST_POSTGRES_DSN="postgres://invctl:invctl@127.0.0.1:5433/invctl?sslmode=disable"`, container via `make compose-up`.
- `make lint` needs `/usr/local/go/bin` and `$HOME/go/bin` on PATH.
- Commit per task, message saying WHY, ending with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

## File Structure

**Create:**
- `internal/store/migrations/sqlite/00067_device_type_components.sql` — the table and its live-scoped unique index
- `internal/store/migrations/postgres/00067_device_type_components.sql` — same, byte-identical content
- `internal/domain/device_type_component.go` — the entity, its kind constants, its constructor
- `internal/domain/range_expand.go` — `ExpandRange`, pure, no imports beyond stdlib
- `internal/domain/range_expand_test.go`
- `internal/store/device_type_components.go` — CRUD, instantiation, apply-to-existing
- `internal/store/device_type_components_test.go`
- `internal/store/template_drift.go` — the drift finding family
- `internal/web/device_type_components_test.go`

**Modify:**
- `internal/domain/classification.go` — classify every new column as declared
- `internal/store/assets.go` — `insertAsset` gains the instantiation call
- `internal/store/findings.go` — register the drift family in `EstateFindings`
- `internal/web/handlers/catalogue.go` — template component handlers
- `internal/web/handlers/assets.go` — the apply-template action
- `internal/web/routes.go` — new routes
- `internal/web/routescan/testdata/write_routes.txt` — regenerate, do not hand-edit
- `internal/web/rbac_boundary_test.go` — the pinned route count moves; update with a note saying why
- `web/templates/pages/catalogue.html` — the component list and its forms
- `web/templates/pages/asset_detail.html` — the apply-template control
- `docs/ROADMAP.md` — WP-C1's status

---

### Task 1: `ExpandRange`, alone and first

It is a pure function with no dependencies, it is where the subtle bugs live, and every later task can use it once it exists.

**Files:**
- Create: `internal/domain/range_expand.go`
- Test: `internal/domain/range_expand_test.go`

**Interfaces:**
- Produces: `func ExpandRange(spec string) ([]string, error)` — `"Ethernet1/[1-4]"` → `["Ethernet1/1","Ethernet1/2","Ethernet1/3","Ethernet1/4"]`; a spec with no bracket returns `[]string{spec}` and no error.

- [ ] **Step 1: Write the failing test**

```go
func TestExpandRange(t *testing.T) {
	for _, tc := range []struct {
		name, spec string
		want       []string
		wantErr    bool
	}{
		{"no range is one name", "eth0", []string{"eth0"}, false},
		{"simple range", "Ethernet1/[1-4]", []string{"Ethernet1/1", "Ethernet1/2", "Ethernet1/3", "Ethernet1/4"}, false},
		{"single element range", "xe-[7-7]", []string{"xe-7"}, false},
		{"suffix after the range", "[1-2]/0", []string{"1/0", "2/0"}, false},
		{"reversed is refused", "eth[4-1]", nil, true},
		{"malformed is refused", "eth[1-]", nil, true},
		{"non-numeric is refused", "eth[a-c]", nil, true},
		{"two ranges are refused", "e[1-2]/[1-2]", nil, true},
		{"over the cap is refused", "eth[1-4097]", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandRange(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ExpandRange(%q) = %v, want an error", tc.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandRange(%q): %v", tc.spec, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ExpandRange(%q) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/domain/ -run TestExpandRange -count=1`
Expected: FAIL, `undefined: ExpandRange`.

- [ ] **Step 3: Implement**

The cap is not decoration: `Ethernet[1-100000]` is a denial of service against your own database, submitted through an ordinary form. 4096 is above any real chassis and far below a problem.

```go
// MaxRangeExpansion caps what one spec may produce. A 48-port switch needs 48
// and the largest chassis in the wild needs a few hundred; 4096 is comfortably
// above any real device and far below the point where a form submission
// becomes a denial of service against this database.
const MaxRangeExpansion = 4096

// ExpandRange turns "Ethernet1/[1-48]" into the names it stands for. A spec
// with no bracket is one name, so callers need no special case.
func ExpandRange(spec string) ([]string, error) {
	open := strings.Index(spec, "[")
	if open < 0 {
		if strings.Contains(spec, "]") {
			return nil, fmt.Errorf("%q closes a range it never opened", spec)
		}
		return []string{spec}, nil
	}
	end := strings.Index(spec[open:], "]")
	if end < 0 {
		return nil, fmt.Errorf("%q opens a range and never closes it", spec)
	}
	end += open
	prefix, body, suffix := spec[:open], spec[open+1:end], spec[end+1:]
	// One range at a time. Two would multiply, and a form that can produce
	// 48x48 names from one line is the denial of service the cap exists for.
	if strings.ContainsAny(prefix, "[]") || strings.ContainsAny(suffix, "[]") {
		return nil, fmt.Errorf("%q has more than one range; expand one at a time", spec)
	}
	lo, hi, ok := strings.Cut(body, "-")
	if !ok {
		return nil, fmt.Errorf("%q is not a range: want [low-high]", spec)
	}
	first, err := strconv.Atoi(lo)
	if err != nil {
		return nil, fmt.Errorf("%q: %q is not a number", spec, lo)
	}
	last, err := strconv.Atoi(hi)
	if err != nil {
		return nil, fmt.Errorf("%q: %q is not a number", spec, hi)
	}
	if last < first {
		return nil, fmt.Errorf("%q counts backwards", spec)
	}
	// The subtraction is safe and the addition is not, which is why the cap is
	// tested against the difference. first >= 0 and last >= first by here, so
	// last-first cannot overflow; last-first+1 CAN, and a wrapped negative
	// count is not > the cap, so it walks past the guard into make() and
	// panics. CORRECTED 2026-09-14 after review caught it in this plan's code.
	if last-first >= MaxRangeExpansion {
		return nil, fmt.Errorf("%q expands to more than %d names",
			spec, MaxRangeExpansion)
	}
	n := last - first + 1
	out := make([]string, 0, n)
	for i := first; i <= last; i++ {
		out = append(out, prefix+strconv.Itoa(i)+suffix)
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/domain/ -run TestExpandRange -count=1`
Expected: PASS.

- [ ] **Step 5: Prove the cap can fail**

Raise `MaxRangeExpansion` to `1 << 30`, re-run, watch `over the cap is refused` go red, restore it. A test never observed failing is a claim, not a check.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/range_expand.go internal/domain/range_expand_test.go
git commit   # why: 48 ports have to be enterable or the feature is unused
```

---

### Task 2: The table, the entity, the classification

**Files:**
- Create: `internal/store/migrations/sqlite/00067_device_type_components.sql`, `internal/store/migrations/postgres/00067_device_type_components.sql`, `internal/domain/device_type_component.go`
- Modify: `internal/domain/classification.go`

**Interfaces:**
- Produces: `domain.DeviceTypeComponent`, `domain.DeviceTypeComponentSpec`, `domain.NewDeviceTypeComponent(id string, spec DeviceTypeComponentSpec, now time.Time) (*DeviceTypeComponent, error)`, constants `domain.ComponentKindInterface = "interface"` and `domain.ComponentKindPowerInput = "power_input"`.

  **CORRECTED 2026-09-14, during execution.** This was a positional signature
  `(id, deviceTypeID, kind, name, position, now)`. It cannot build a valid
  interface component, because a form factor is REQUIRED for that kind (see
  below) and the signature has nowhere to pass one. A spec, like `NewNetGroup`
  and `NewPowerInput` already take.

  **A form factor is required when `kind` is `interface`**, enforced in the
  constructor AND as a table `CHECK`. `interface.form_factor` is `NOT NULL` with
  a foreign key into `interface_form_factor`, and `CreateInterface` calls
  `requireVocabulary` on it — so a component without one is accepted by the
  template and then refused at INSTANTIATION, failing every attempt to create an
  asset of that model, with an error naming a field on a form the operator is
  not looking at. The table constraint goes AFTER every column definition;
  placing it among them is a syntax error on both engines.

- [ ] **Step 1: Write the migration, both engines**

Header must say why the unique index is live-scoped, following `00064`'s reasoning. Content identical on both engines — no dialect-specific feature is needed here.

```sql
-- +goose Up
CREATE TABLE device_type_component (
  id              TEXT PRIMARY KEY,
  device_type_id  TEXT NOT NULL REFERENCES device_type(id),
  kind            TEXT NOT NULL
                    CONSTRAINT dtc_kind_check CHECK (kind IN ('interface','power_input')),
  name            TEXT NOT NULL,
  position        INTEGER NOT NULL,
  form_factor     TEXT REFERENCES interface_form_factor(code),
  speed_mbps      INTEGER,
  is_mgmt         BOOLEAN NOT NULL DEFAULT FALSE,
  draw_va         INTEGER,
  lifecycle       TEXT NOT NULL DEFAULT 'active'
                    CONSTRAINT dtc_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  row_version     INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX dtc_name_key ON device_type_component(device_type_id, kind, name)
  WHERE lifecycle = 'active';
CREATE INDEX dtc_type ON device_type_component(device_type_id);

-- +goose Down
DROP INDEX dtc_type;
DROP INDEX dtc_name_key;
DROP TABLE device_type_component;
```

`BOOLEAN` with `TRUE`/`FALSE` literals, per CLAUDE.md — both engines accept the keywords, Postgres rejects `0`/`1`.

- [ ] **Step 2: Write the entity and its constructor**

`NewDeviceTypeComponent` validates that the columns set match the kind: `form_factor`/`speed_mbps`/`is_mgmt` belong to `interface`, `draw_va` to `power_input`. A `power_input` carrying a `speed_mbps` is a programming error and the constructor says so.

- [ ] **Step 3: Classify every new column**

Declared, in `internal/domain/classification.go`. All of them: somebody asserts that this model has this port. `created_at`/`updated_at`/`row_version` are declared under the bookkeeping rule already stated in that file.

- [ ] **Step 4: Verify on BOTH engines**

Run: `git add -A && INV_TEST_POSTGRES_DSN="..." go test ./internal/store/ -run 'TestEveryColumnIsClassified|TestMigrat' -count=1`
Expected: PASS on sqlite AND postgres. `TestEveryColumnIsClassified` is the one that catches a missed column, and it only fails on the engine it ran against — a SQLite-only run will not show you a Postgres failure.

- [ ] **Step 5: Commit**

---

### Task 3: Store CRUD for a type's components

**Files:**
- Create: `internal/store/device_type_components.go`, `internal/store/device_type_components_test.go`

**Interfaces:**
- Consumes: `domain.NewDeviceTypeComponent`, `domain.ExpandRange`.
- Produces:
  - `ListDeviceTypeComponents(ctx, deviceTypeID string) ([]domain.DeviceTypeComponent, error)` — active only, ordered by `kind, position, name`
  - `CreateDeviceTypeComponents(ctx, p domain.Permit, deviceTypeID string, spec ComponentSpec) error` — expands the range and inserts all rows in ONE transaction
  - `UpdateDeviceTypeComponent(ctx, p domain.Permit, c *domain.DeviceTypeComponent) error`
  - `RetireDeviceTypeComponent(ctx, p domain.Permit, id string) error`

- [ ] **Step 1: Write the failing tests**

Both engines via `Engines(t)`. Cover: a range creating N rows in one call; the live-scoped index allowing a retired name to be redeclared; `position` ordering `Ethernet1/2` before `Ethernet1/10`.

- [ ] **Step 2: Run them and watch them fail**

- [ ] **Step 3: Implement**

`CreateDeviceTypeComponents` takes the whole expansion in one transaction: 48 ports are one operator action and one audit story, not 48.

- [ ] **Step 4: Run the tests, both engines**

- [ ] **Step 5: Mutation-prove the audit**

Delete the `change_log` write, watch the audit assertion go red, restore.

- [ ] **Step 6: Commit**

---

### Task 4: Instantiation at creation

**Files:**
- Modify: `internal/store/assets.go` (`insertAsset`), `internal/store/device_type_components.go`

**Interfaces:**
- Produces: `instantiateComponents(ctx context.Context, t *tx, a *domain.Asset) error`, called from `insertAsset`.

`insertAsset` is the single chokepoint — `CreateAsset`, `CreateAssetInProject`, `import.go` and `import_batched.go` all route through it. Hooking there means every creation path gets templates, including both importers, and there is exactly one place to get it right.

- [ ] **Step 1: Write the failing test**

Create a device type, give it two interface components and one power input, create an asset of that type, assert the asset has exactly those three components with the right attributes — and assert one `change_log` row per component.

Also: an asset whose type has NO components is created exactly as before. That is the regression test for every existing caller.

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Implement**

In the asset's own transaction. Each component is an ordinary `interface` or `power_input` row with its own `change_log` entry — they are declared state and the audit rule has no exception for rows a template suggested.

- [ ] **Step 4: Run the store and web suites**

Run: `INV_TEST_POSTGRES_DSN="..." go test ./internal/store/ ./internal/web/... -count=1`
Expected: PASS. The importers have large fixtures; a failure here means instantiation fired where it should not.

- [ ] **Step 5: Mutation-prove**

Skip the instantiation call, watch the test go red, restore.

- [ ] **Step 6: Commit**

---

### Task 5: Apply a template to an asset that already exists

Without this the feature helps only assets created from tomorrow, and the four `DCS-7050SX3-48YC8` switches with fourteen ports between them — the reason the feature exists — stay wrong.

**Files:**
- Modify: `internal/store/device_type_components.go`

**Interfaces:**
- Produces: `ApplyTemplate(ctx, p domain.Permit, assetID string) (added int, err error)`

- [ ] **Step 1: Write the failing test — the one that matters**

An asset with `eth0` already recorded, whose type's template also names `eth0` **with a different form factor**. After applying: `eth0` is untouched, the missing components are added, and `added` counts only what was created.

A port somebody already recorded is theirs, whatever the template says about it. Overwriting it destroys operator data silently, which is worse than the feature not existing.

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Implement** — add what is missing by name, never remove, never overwrite.

- [ ] **Step 4: Run the tests, both engines**

- [ ] **Step 5: Mutation-prove the skip**

Change the skip-by-name rule to overwrite, watch the "untouched" assertion go red, restore. This is the mutation that matters most in the plan.

- [ ] **Step 6: Commit**

---

### Task 6: Drift as a finding

The counterpart to not rewriting instances. Without it, "type edits do not rewrite existing instances — report drift as a finding instead" has an empty *instead*.

**Files:**
- Create: `internal/store/template_drift.go`
- Modify: `internal/store/findings.go`

**Interfaces:**
- Consumes: the `Finding` shape in `internal/store/findings.go`.
- Produces: `TemplateDriftFindings(ctx) ([]Finding, error)`, registered in `EstateFindings` beside `PowerFindings` and `CapacityFindings`.

- [ ] **Step 1: Write the failing test** — an asset missing a component its type declares produces one finding; an asset matching its type produces none.

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Implement**

Severity `FindingGap` — "not recorded, so not knowable" — because that is what drift here usually means: the model gained ports the asset never got, not that anything is broken. One row per KIND of finding, per that file's own rule: forty drifting assets is one decision.

- [ ] **Step 4: Run the tests, both engines**

- [ ] **Step 5: Commit**

---

### Task 7: The catalogue UI

**Files:**
- Modify: `internal/web/handlers/catalogue.go`, `internal/web/routes.go`, `web/templates/pages/catalogue.html`, `internal/web/routescan/testdata/write_routes.txt`, `internal/web/rbac_boundary_test.go`
- Test: `internal/web/device_type_components_test.go`

**Interfaces:**
- Consumes: the Task 3 store methods.
- Produces routes, alongside the existing `/catalogue/types` family:
  - `POST /catalogue/types/{id}/components`
  - `POST /catalogue/types/{id}/components/{componentID}`
  - `POST /catalogue/types/{id}/components/{componentID}/retire`

- [ ] **Step 1: Write the failing tests** — adding by range creates N rows and returns 303; a malformed range returns **422** with the typed spec still in the form; the edit form carries `row_version`.

- [ ] **Step 2: Run them and watch them fail**

- [ ] **Step 3: Implement**

A refusal re-renders through the catalogue page's own assembly with `refusalStatus(err)` and `rejected(...)`. Do NOT flash-and-redirect — `TestARefusalIsRenderedNotFlashed` AST-scans for exactly that and will fail you.

- [ ] **Step 4: Regenerate the route inventory**

Run: `go test ./internal/web/routescan/ -write-inventory` then update the pinned count in `internal/web/rbac_boundary_test.go` with a comment saying which routes moved it. Do not hand-edit the inventory file.

- [ ] **Step 5: Run the web suite**

- [ ] **Step 6: Commit**

---

### Task 8: The apply-template control, and closing the entry

**Files:**
- Modify: `internal/web/handlers/assets.go`, `internal/web/routes.go`, `web/templates/pages/asset_detail.html`, `docs/ROADMAP.md`

**Interfaces:**
- Consumes: `ApplyTemplate` from Task 5.
- Produces: `POST /assets/{id}/apply-template`.

- [ ] **Step 1: Write the failing test** — the control adds the missing components and reports how many; it is absent when the asset has no device type.

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Implement**

The control needs `hx-post` beside its `hx-confirm`, or the confirm is decorative — a bug this repo has shipped before. It says what it will do: adding N components is not obviously safe to an operator reading a button.

- [ ] **Step 4: Update WP-C1 in `docs/ROADMAP.md`**

Mark the template half DONE with the date. Say plainly that modules, module bays and inventory items remain unbuilt and are NOT covered — this entry was already found once marked DONE with its scope unbuilt, and the fix is not to repeat the pattern in the other direction.

- [ ] **Step 5: Run the full gate**

Run: `make lint && make test`
Expected: both clean. **Capture `make test`'s own exit code directly — not through a pipeline**, where the status you read is `tail`'s and a red gate reports green.

- [ ] **Step 6: Commit**

---

## Self-review notes

- **Spec coverage:** table (T2), range expansion (T1), instantiation (T4), apply-to-existing (T5), drift finding (T6), UI (T7, T8). Non-goals — modules, inventory items, outlets, WP-A3 extraction — appear in no task, deliberately.
- **`is_mgmt` type consistency:** declared `BOOLEAN` in the migration and written with `TRUE`/`FALSE` literals; the Go field is `bool`. Do not let it become an `INTEGER`.
- **Both engines everywhere:** every store task's verification step names both. The failure mode this prevents is a green SQLite run hiding a Postgres-only classification failure.
