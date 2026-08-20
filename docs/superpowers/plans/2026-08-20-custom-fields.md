# Custom Fields (WP-A4) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an administrator define typed, described, attributable fields on assets and services without a migration, and edit, audit and export their values like anything else this system stores.

**Architecture:** Three tables — a definition, its options, and its values — with values held as text and typed by the definition. Values fold into the parent entity's audited shape as a sorted joined string, matching what `assetAudit` already does for environments, so a value change is audited against the asset rather than against the field. Nothing is searchable, filterable, or published to the read-only API.

**Tech Stack:** Go 1.22+ stdlib, `net/http.ServeMux`, `html/template`, `jmoiron/sqlx` with hand-written SQL, `pressly/goose/v3`, HTMX 2.x. **No new dependencies.**

**Spec:** `docs/custom-fields-design.md`

## Global Constraints

From `CLAUDE.md` and the spec. Violating one is a rejected task.

- **Placeholders are `?`.** Call `sqlx.Rebind`. Never `$1`.
- **Every query runs unmodified on SQLite and PostgreSQL.** No `inet`, `cidr`, native arrays, `ENUM`, `jsonb` in `WHERE`, `SERIAL`, `generate_series()`, `NOW()` as a default, `num_nonnulls()`, or `RETURNING` on multi-row statements.
- **Booleans are `TRUE`/`FALSE` literals**, never `0`/`1`.
- **IDs are UUIDv7 TEXT generated in Go. Timestamps are RFC3339 UTC TEXT generated in Go.** Never by the database.
- **Every mutation of declared state writes a `change_log` row in the same transaction.**
- **Soft delete only.** Retiring a field deletes no value. Clearing one value IS a set replacement and is correct — see the spec §6 — but the parent's `change_log` entry must record it.
- **Optimistic concurrency on every edit**, 409 on a second save. `TestEveryEditorRefusesASecondSaveFromOneToken` (`internal/web/edits_test.go:596`) walks an `editors` table; new editors get entries there.
- **Licence header on every new `.go`, `.sql`, `.html` file**, followed by a **blank line** before the package clause:
  ```go
  // invctl — infrastructure inventory
  // Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
  //
  // Licensed under the GNU Affero General Public License, version 3 only —
  // no later version applies. See LICENSE for the full text.
  //
  // SPDX-License-Identifier: AGPL-3.0-only
  ```
- **`internal/domain` keeps zero external dependencies.**
- Wrap errors `fmt.Errorf("doing x: %w", err)`. Never panic outside `main`.
- **`make test` is the gate, not `go test ./...`** — the latter silently skips Postgres.
- **`make lint` is the lint gate, not `gofmt && go vet && staticcheck`** — it runs golangci-lint with gosec, perfsprint and usestdlibvars on top, and WP-A2 drifted for ten tasks by running the wrong three.
- Test names are full behaviour sentences. Assert **exact values**, never lengths or `contains`.

---

## File Structure

**Created**

| File | Responsibility |
|---|---|
| `internal/store/migrations/{sqlite,postgres}/00051_custom_fields.sql` | the three tables |
| `internal/domain/customfield.go` | entities, the kind constant set, validation |
| `internal/domain/customfield_test.go` | validation table tests |
| `internal/store/customfields.go` | definition and option CRUD, audited |
| `internal/store/customfields_test.go` | store tests, both engines |
| `internal/store/customvalues.go` | value read/replace + the audit fold |
| `internal/store/customvalues_test.go` | value tests, both engines |
| `internal/web/handlers/customfields.go` | registry handlers + value editors |
| `internal/web/customfields_test.go` | HTTP-level tests |
| `web/templates/pages/custom_field_list.html` | the registry |
| `web/templates/partials/custom_field_form.html` | define / edit a field |
| `web/templates/partials/custom_fields_show.html` | the grouped section |
| `web/templates/partials/custom_fields_form.html` | the value inputs |

**Modified**

| File | Change |
|---|---|
| `internal/store/assets.go` | `assetAudit` gains `CustomFields`; `auditedAsset` gains a parameter |
| `internal/store/services.go` | new `serviceAudit` type + `auditedService`; `UpdateService`/`CreateService` use it |
| `internal/store/export.go` | `ExportAssets` gains columns; `ExportServices` gains `ctx` and `error` |
| `internal/web/routes.go` | five routes |
| `internal/web/handlers/nav.go` | a Settings entry |
| `web/templates/pages/asset_detail.html`, `service_detail.html` | include the partials |
| `internal/web/edits_test.go` | two `editors` entries |
| `internal/seed/` | fixture fields, options and values |
| `docs/ROADMAP.md`, `CHANGELOG.md`, `docs/API.md` | marker, entry, and a note that custom fields are absent from the API |

**Shared test fixture, built in Task 3 and used by Tasks 3, 4 and 7.** Model it
on `newProjectFixture` in `internal/store/supplier_movement_test.go`. It returns
a struct carrying `s *SQLStore`, `ctx context.Context`, `actor domain.Actor`,
`assetID`, `secondAssetID`, `serviceID` and `username`, plus these helpers, each of which
`t.Fatal`s rather than returning an error:

| Helper | Does |
|---|---|
| `newCustomFieldFixture(t, e)` | migrated store, seeded estate, one asset and one service to hang values on |
| `mustField(t, f, entityType, code, kind) string` | creates a field, returns its id |
| `mustOption(t, f, fieldID, value)` | adds a select option |
| `mustValue(t, f, fieldID, entityID, raw)` | sets one value through the normal path |
| `mustSetValues(t, f, entityID, map[fieldID]raw)` | replaces this entity's values wholesale |
| `mustClearValues(t, f, entityID)` | replaces this entity's values with none |
| `changeCount(t, f, entityType, entityID) int64` | rows in `change_log` for that entity |
| `lastChangeDiff(t, f, entityType, entityID) string` | the newest entry's diff column |

**Import direction:** `internal/domain` imports nothing of ours. `internal/store` imports `domain`. `internal/web/handlers` imports both. Nothing imports `internal/api`, and `internal/api` gains nothing.

---

### Task 1: Migration 00051

**Files:**
- Create: `internal/store/migrations/sqlite/00051_custom_fields.sql`
- Create: `internal/store/migrations/postgres/00051_custom_fields.sql`

**Interfaces:**
- Produces: tables `custom_field`, `custom_field_option`, `custom_field_value` exactly as spec §2.

- [ ] **Step 1: Write both halves**

Copy the DDL from `docs/custom-fields-design.md` §2 verbatim. Both files carry the licence header as a SQL comment and goose's `-- +goose Up` / `-- +goose Down`. Name every constraint, as `00005_named_constraints.sql` established.

The dialect split exists only where the two engines genuinely differ; the tables above use `TEXT`, `INTEGER` and `BOOLEAN`, which both accept. `BOOLEAN NOT NULL DEFAULT TRUE` is already used by `app_user.is_active`, so follow that precedent rather than inventing an integer flag.

- [ ] **Step 2: Verify both halves exist and match**

Run: `go test ./internal/store/ -run TestEveryDialectMigrationHasBothHalves -v`
Expected: PASS. This guard already exists and will fail if you write only one.

- [ ] **Step 3: Apply on both engines**

Run: `make test`
Expected: PASS. `TestTheSharedSchemaRunsOnBothEngines` applies every migration to both.

- [ ] **Step 4: Verify the partial index actually works on both**

Add to `internal/store/customfields_test.go`:

```go
func TestARetiredCodeCanBeUsedAgain(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			first := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			if err := f.s.RetireCustomField(f.ctx, f.actor, first); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			// The partial unique index is WHERE retired_at IS NULL, so the
			// code is free again. A plain UNIQUE would refuse this and an
			// operator could never reuse a name they had retired.
			if _, err := f.s.CreateCustomField(f.ctx, f.actor, newFieldFixture("asset", "cost_centre")); err != nil {
				t.Fatalf("recreating a retired code must be allowed: %v", err)
			}
		})
	}
}

func TestTwoLiveFieldsCannotShareACode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			if _, err := f.s.CreateCustomField(f.ctx, f.actor, newFieldFixture("asset", "cost_centre")); err == nil {
				t.Fatal("two live fields sharing a code must be refused")
			}
		})
	}
}
```

These will not compile until Task 3. Write them now, comment them out with a note, and uncomment in Task 3 — or write them in Task 3 and here only verify the schema applies. Either is fine; say which you did.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/
git commit -m "Three tables so an estate-specific attribute is not a migration"
```

---

### Task 2: Domain types and validation

**Files:**
- Create: `internal/domain/customfield.go`
- Create: `internal/domain/customfield_test.go`

**Interfaces:**
- Consumes: `ValidationError`, `checkRequired(ve, field, val) string`, `checkEnum(ve, field, val, set)`, `ve.OrNil()` — all in `internal/domain`.
- Produces:
  ```go
  const (
      CustomFieldText    = "text"
      CustomFieldNumber  = "number"
      CustomFieldDate    = "date"
      CustomFieldBoolean = "boolean"
      CustomFieldSelect  = "select"
  )
  var CustomFieldKinds = []string{CustomFieldText, CustomFieldNumber,
      CustomFieldDate, CustomFieldBoolean, CustomFieldSelect}

  const (
      CustomFieldEntityAsset   = "asset"
      CustomFieldEntityService = "service"
  )
  var CustomFieldEntityTypes = []string{CustomFieldEntityAsset, CustomFieldEntityService}

  type CustomField struct {
      ID, EntityType, Code, Label, Kind, Description string
      CreatedBy string; CreatedAt string
      RetiredAt *string; RetiredBy *string
      RowVersion int
  }
  type CustomFieldOption struct {
      ID, FieldID, Value, Label string
      Position int; RetiredAt *string
  }
  type CustomFieldValue struct {
      ID, FieldID, EntityID, ValueText string
      CreatedAt, UpdatedAt string; RowVersion int
  }

  // now is a time.Time, matching NewAsset/NewEnvironment/NewCertificate:
  // every domain constructor in this package takes the clock as its last
  // parameter and formats it itself. Never a preformatted string.
  func NewCustomField(id, entityType, code, label, kind, description, createdBy string, now time.Time) (*CustomField, error)
  // CanonicalCustomValue validates raw against kind and returns the stored form.
  // options is the field's LIVE option values, used only when kind is select.
  func CanonicalCustomValue(kind, raw string, options []string) (string, error)
  func (f *CustomField) IsRetired() bool
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestACustomFieldRefusesAnEmptyDescription(t *testing.T) {
	// The description is what a new hire reads instead of telephoning the
	// vendor. An administrator who cannot say why a field exists is the
	// origin of that call, and creation is the cheapest moment to ask.
	_, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "   ", "user-1", testClock)
	if err == nil {
		t.Fatal("a field with no description must be refused")
	}
}

func TestACustomFieldCodeIsLowerCased(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "Cost_Centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", testClock)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if f.Code != "cost_centre" {
		t.Fatalf("got code %q, want cost_centre", f.Code)
	}
}

func TestCanonicalCustomValue(t *testing.T) {
	cases := []struct {
		name, kind, raw string
		options         []string
		want            string
		wantErr         bool
	}{
		{"text is trimmed", CustomFieldText, "  ABC-1234 ", nil, "ABC-1234", false},
		{"empty text is refused", CustomFieldText, "   ", nil, "", true},
		{"a whole number", CustomFieldNumber, "42", nil, "42", false},
		{"a decimal", CustomFieldNumber, "42.50", nil, "42.50", false},
		{"a negative", CustomFieldNumber, "-7", nil, "-7", false},
		{"a grouped number is refused", CustomFieldNumber, "1,234", nil, "", true},
		{"words are not a number", CustomFieldNumber, "many", nil, "", true},
		{"an ISO date", CustomFieldDate, "2027-03-01", nil, "2027-03-01", false},
		{"a non-date is refused", CustomFieldDate, "march next year", nil, "", true},
		{"an impossible date is refused", CustomFieldDate, "2027-02-30", nil, "", true},
		{"true normalises", CustomFieldBoolean, "TRUE", nil, "true", false},
		{"yes is not a boolean", CustomFieldBoolean, "yes", nil, "", true},
		{"a live option", CustomFieldSelect, "it-42", []string{"it-42", "it-99"}, "it-42", false},
		{"an unlisted option is refused", CustomFieldSelect, "it-01", []string{"it-42"}, "", true},
		{"select with no options is refused", CustomFieldSelect, "it-42", nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalCustomValue(c.kind, c.raw, c.options)
			if c.wantErr {
				if err == nil {
					t.Fatalf("%q was accepted for kind %s; want refused", c.raw, c.kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q was refused for kind %s: %v", c.raw, c.kind, err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestAnUnknownKindIsRefused(t *testing.T) {
	if _, err := CanonicalCustomValue("colour", "blue", nil); err == nil {
		t.Fatal("an unknown kind must be refused, not stored as text")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/domain/ -run TestCanonical -v`
Expected: FAIL — `undefined: CanonicalCustomValue`

- [ ] **Step 3: Implement**

Create `internal/domain/customfield.go` with the licence header and a doc comment saying what this file is for: fields whose meaning the ESTATE defines, the far end of the axis `internal/help` describes.

`NewCustomField` uses `checkRequired` for code, label and description, `checkEnum` for kind and entity type, lower-cases and trims the code, and returns `ve.OrNil()`.

`CanonicalCustomValue` switches on kind. Use `strconv.ParseFloat` for number but return the **trimmed original text** rather than a reformatted float, so `42.50` does not become `42.5` — an operator who typed two decimal places meant them. Use `time.Parse("2006-01-02", raw)` for date, which rejects `2027-02-30`. Boolean accepts only `true`/`false` case-insensitively. Select does a linear scan of `options`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/domain/ -v && gofmt -l internal/domain && go vet ./internal/domain/`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/customfield.go internal/domain/customfield_test.go
git commit -m "A custom field validates its own values, and refuses a field with no reason to exist"
```

---

### Task 3: Store — definitions and options

**Files:**
- Create: `internal/store/customfields.go`
- Create: `internal/store/customfields_test.go`

**Interfaces:**
- Consumes: `t.logCreate(ctx, entityType, entityID, entity)`, `t.logUpdate(ctx, entityType, entityID, before, after)`, `s.read`/`s.readOne`, `placeholders(n)`, `NewID()`, `Now()` — confirm the exact names of the last two by reading `internal/store/store.go` before writing.
- Produces:
  ```go
  type CustomFieldRow struct {
      domain.CustomField
      CreatedByName string `db:"created_by_name"`
      RetiredByName *string `db:"retired_by_name"`
      UsageCount    int    `db:"usage_count"`
      Options       []domain.CustomFieldOption
  }
  func (s *SQLStore) CreateCustomField(ctx context.Context, actor domain.Actor, f *domain.CustomField) error
  func (s *SQLStore) UpdateCustomField(ctx context.Context, actor domain.Actor, f *domain.CustomField) error
  func (s *SQLStore) RetireCustomField(ctx context.Context, actor domain.Actor, id string) error
  func (s *SQLStore) RestoreCustomField(ctx context.Context, actor domain.Actor, id string) error
  func (s *SQLStore) ListCustomFields(ctx context.Context, entityType string, includeRetired bool) ([]CustomFieldRow, error)
  func (s *SQLStore) GetCustomField(ctx context.Context, id string) (*CustomFieldRow, error)
  func (s *SQLStore) SetCustomFieldOptions(ctx context.Context, actor domain.Actor, fieldID string, opts []domain.CustomFieldOption) error
  ```

- [ ] **Step 1: Write the failing tests**

Model the fixture on `newProjectFixture` in `internal/store/supplier_movement_test.go`. Tests to write:

```go
func TestAFieldsKindCannotChangeWhileValuesExist(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			got, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			got.Kind = domain.CustomFieldNumber
			err = f.s.UpdateCustomField(f.ctx, f.actor, &got.CustomField)
			if err == nil {
				t.Fatal("retyping a field that holds values must be refused: " +
					"it is a data migration wearing a form control")
			}
			if !strings.Contains(err.Error(), "1") {
				t.Errorf("the refusal should name how many values block it; got %v", err)
			}
		})
	}
}

func TestTheLabelAndDescriptionStayEditableWithValues(t *testing.T) {
	// The two things an administrator actually needs to correct stay editable
	// forever. Only the kind is frozen once values exist.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Label = "SAP Cost Centre"
			row.Description = "the code finance rebills against"
			if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
				t.Fatalf("renaming a field that holds values must be allowed: %v", err)
			}
			after, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if after.Label != "SAP Cost Centre" {
				t.Fatalf("got label %q, want SAP Cost Centre", after.Label)
			}
			if after.Description != "the code finance rebills against" {
				t.Fatalf("got description %q", after.Description)
			}
		})
	}
}

func TestRetiringAFieldKeepsEveryValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			if err := f.s.RetireCustomField(f.ctx, f.actor, id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			// countOne(ctx, query, args...) (int64, error) -- it returns the
			// count, it does not take a destination pointer.
			n, err := f.s.countOne(f.ctx,
				`SELECT COUNT(*) FROM custom_field_value WHERE field_id = ?`, id)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 1 {
				t.Fatalf("got %d values after retiring, want 1: retiring a field "+
					"deletes no value, or somebody rings up asking where their data went", n)
			}
		})
	}
}

func TestRestoreIsRefusedWhenALiveFieldHoldsTheCode(t *testing.T) {
	// The partial unique index frees a retired code for reuse, so restoring
	// the original would produce two live fields with one code. Refuse, and
	// say which field is in the way.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			first := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			if err := f.s.RetireCustomField(f.ctx, f.actor, first); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)

			err := f.s.RestoreCustomField(f.ctx, f.actor, first)
			if err == nil {
				t.Fatal("restoring must be refused while a live field holds the code")
			}
			if !strings.Contains(err.Error(), "cost_centre") {
				t.Errorf("the refusal must name the code that is in the way; got %v", err)
			}
		})
	}
}

func TestTheRegistryResolvesTheCreatorWithoutStoringAUsername(t *testing.T) {
	// created_by holds an opaque app_user.id, exactly as change_log.actor does,
	// so scrubbing a user for an erasure request leaves the registry readable
	// and simply stops resolving a name.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if row.CreatedBy == f.username {
				t.Fatal("created_by holds the username; it must hold the opaque app_user.id")
			}
			if row.CreatedByName != f.username {
				t.Fatalf("got display name %q, want %q resolved by join", row.CreatedByName, f.username)
			}
		})
	}
}

func TestUsageCountsCountEntitiesNotRows(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			mustValue(t, f, id, f.secondAssetID, "IT-99")
			// A third asset deliberately holds no value: a count that returned
			// every asset would pass a test where every asset had one.

			rows, err := f.s.ListCustomFields(f.ctx, "asset", false)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var got int
			for _, r := range rows {
				if r.ID == id {
					got = r.UsageCount
				}
			}
			if got != 2 {
				t.Fatalf("got usage count %d, want 2", got)
			}
		})
	}
}
func TestEveryDefinitionMutationWritesChangeLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			steps := []struct {
				name string
				do   func() error
			}{
				{"update", func() error {
					row, err := f.s.GetCustomField(f.ctx, id)
					if err != nil {
						return err
					}
					row.Label = "Renamed"
					return f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField)
				}},
				{"retire", func() error { return f.s.RetireCustomField(f.ctx, f.actor, id) }},
				{"restore", func() error { return f.s.RestoreCustomField(f.ctx, f.actor, id) }},
			}
			// Creation already happened inside mustField; assert it logged too.
			if n := changeCount(t, f, "custom_field", id); n != 1 {
				t.Fatalf("creating wrote %d change_log rows, want 1", n)
			}
			for i, step := range steps {
				before := changeCount(t, f, "custom_field", id)
				if err := step.do(); err != nil {
					t.Fatalf("%s: %v", step.name, err)
				}
				if got := changeCount(t, f, "custom_field", id); got != before+1 {
					t.Fatalf("%s (step %d) wrote %d rows, want 1", step.name, i, got-before)
				}
			}
		})
	}
}
```

Plus the two index tests deferred from Task 1.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/store/ -run TestAFieldsKind -v`
Expected: FAIL — `undefined: CreateCustomField`

- [ ] **Step 3: Implement**

Create `internal/store/customfields.go`. Every mutation opens a transaction, does its work, and writes the audit row in the same transaction. `UpdateCustomField` counts values first and refuses a `Kind` change when the count is non-zero, naming the count. `ListCustomFields` gets usage counts with a `LEFT JOIN ... GROUP BY`, and display names with a `LEFT JOIN app_user`.

Options are a set: `SetCustomFieldOptions` replaces them wholesale inside the field's transaction, and the field's `change_log` entry must record the change — fold the option values into the audited shape the same way §5 folds values into the asset's, or the set changes with no diff on the parent and no audit entry at all, which this repo has been bitten by three times.

- [ ] **Step 4: Run on both engines**

Run: `make test`
Expected: PASS on SQLite and Postgres. `go test ./internal/store/` alone is not the gate.

- [ ] **Step 5: Commit**

```bash
git add internal/store/customfields.go internal/store/customfields_test.go
git commit -m "Defining a field is declared state, and retyping one with values is refused"
```

---

### Task 4: Store — values, and the audit fold

**This is the delicate task.** It changes a function every asset write goes through and introduces one for services.

**Files:**
- Create: `internal/store/customvalues.go`
- Create: `internal/store/customvalues_test.go`
- Modify: `internal/store/assets.go` (`assetAudit` at :133 area, `auditedAsset`, every call site)
- Modify: `internal/store/services.go` (new `serviceAudit`, `auditedService`, `CreateService`/`UpdateService`)

**Interfaces:**
- Produces:
  ```go
  func (s *SQLStore) CustomValuesFor(ctx context.Context, entityType, entityID string) ([]CustomValueRow, error)
  // SetCustomValues replaces this entity's values wholesale INSIDE t. It never
  // opens its own transaction: the parent's audit entry must cover it.
  func setCustomValues(ctx context.Context, t *tx, entityType, entityID string, vals map[string]string) error
  // customFieldsAudit renders an entity's values as the sorted, joined string
  // that folds into assetAudit / serviceAudit.
  func customFieldsAudit(ctx context.Context, t *tx, entityType, entityID string) (string, error)

  type CustomValueRow struct {
      domain.CustomFieldValue
      Code, Label, Kind string
      Retired bool
  }
  ```
  and the changed signature:
  ```go
  func auditedAsset(a *domain.Asset, codes []string, custom string) *assetAudit
  type assetAudit struct {
      domain.Asset
      Environments string `db:"environments"`
      CustomFields string `db:"custom_fields"`
  }
  type serviceAudit struct {
      domain.Service
      CustomFields string `db:"custom_fields"`
  }
  func auditedService(svc *domain.Service, custom string) *serviceAudit
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestSettingAValueIsAuditedAgainstTheEntity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-41")
			mustValue(t, f, id, f.assetID, "IT-42")

			diff := lastChangeDiff(t, f, "asset", f.assetID)
			if !strings.Contains(diff, "IT-41") || !strings.Contains(diff, "IT-42") {
				t.Fatalf("the asset's audit entry must carry both the old and the "+
					"new value; got %s", diff)
			}
		})
	}
}

func TestClearingAValueIsAuditedOnTheParent(t *testing.T) {
	// Clearing removes the row -- correct, because the value table holds the
	// current value of something its parent owns, the same shape as
	// asset_environment. But a set replacement that produces no diff on the
	// parent is exactly the failure this repo has hit three times.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			before := changeCount(t, f, "asset", f.assetID)

			mustClearValues(t, f, f.assetID)

			if got := changeCount(t, f, "asset", f.assetID); got != before+1 {
				t.Fatalf("clearing a value wrote %d audit rows, want 1", got-before)
			}
		})
	}
}

func TestReorderingCustomValuesIsNotAChange(t *testing.T) {
	// The fold sorts by code before joining, the same reason dependencyAudit
	// sorts its classes: a set written in a different order is not a change,
	// and an audit trail that says it is teaches its readers to ignore it.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)

			mustSetValues(t, f, f.assetID, map[string]string{
				tagID: "ABC-1234", ccID: "IT-42",
			})
			before := changeCount(t, f, "asset", f.assetID)

			// The same pair, written the other way round.
			mustSetValues(t, f, f.assetID, map[string]string{
				ccID: "IT-42", tagID: "ABC-1234",
			})

			if got := changeCount(t, f, "asset", f.assetID); got != before {
				t.Fatalf("writing the same values in a different order wrote %d "+
					"audit rows; a reordering is not a change", got-before)
			}
		})
	}
}

func TestAServiceAuditEmbedsByValue(t *testing.T) {
	// auditFields PANICS on an anonymous pointer embed, because that shape
	// silently dropped every column from certificate entries for a week while
	// still writing one. Assert the shape rather than trusting it.
	f := reflect.TypeOf(serviceAudit{}).Field(0)
	if !f.Anonymous {
		t.Fatal("serviceAudit must embed domain.Service anonymously")
	}
	if f.Type.Kind() == reflect.Ptr {
		t.Fatal("serviceAudit embeds domain.Service by POINTER; embed by value, " +
			"or every column is silently absent from every change_log entry")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/store/ -run TestSettingAValue -v`
Expected: FAIL

- [ ] **Step 3: Implement**

Write `customvalues.go`. `setCustomValues` deletes this entity's rows and inserts the given ones, inside the caller's transaction, validating each through `domain.CanonicalCustomValue` against its field's kind and live options. A value for an unknown or retired field is refused.

Then change `assetAudit` and `auditedAsset`, and **update every call site** — `CreateAsset`, `UpdateAsset`, `RetireAsset`, `ReparentAsset` and any other. A call site that still compiles while passing an empty string produces an entry that looks complete and omits the values, which is the failure the fold exists to prevent, so grep for `auditedAsset(` and confirm you found them all.

Add `serviceAudit` and `auditedService`, and switch `CreateService`/`UpdateService` from logging `&before.Service` and `svc` directly to logging the wrapper. **Embed by value.**

- [ ] **Step 4: Run the whole suite on both engines**

Run: `make test`
Expected: PASS. Every pre-existing asset and service audit test must still pass — if one changed meaning, that is a bug in this task, not an expected consequence.

- [ ] **Step 5: Verify the fold actually bites**

Temporarily make `customFieldsAudit` return `""` unconditionally. Confirm `TestSettingAValueIsAuditedAgainstTheEntity` and `TestClearingAValueIsAuditedOnTheParent` both FAIL. Revert and confirm `git diff` is clean. A mutant must compile; one that does not proves nothing. Report the failure messages.

- [ ] **Step 6: Commit**

```bash
git add internal/store/customvalues.go internal/store/customvalues_test.go internal/store/assets.go internal/store/services.go
git commit -m "A custom value change is audited against the asset, not the field"
```

---

### Task 5: The registry page

**Files:**
- Create: `internal/web/handlers/customfields.go`
- Create: `web/templates/pages/custom_field_list.html`
- Create: `web/templates/partials/custom_field_form.html`
- Modify: `internal/web/routes.go`, `internal/web/handlers/nav.go`

**Interfaces:**
- Produces:
  ```go
  func (a *App) CustomFieldList(w http.ResponseWriter, r *http.Request)
  func (a *App) CustomFieldCreate(w http.ResponseWriter, r *http.Request)
  func (a *App) CustomFieldUpdate(w http.ResponseWriter, r *http.Request)
  func (a *App) CustomFieldRetire(w http.ResponseWriter, r *http.Request)
  func (a *App) CustomFieldRestore(w http.ResponseWriter, r *http.Request)
  ```

Routes, modelled exactly on the teams block in `routes.go:170,254-256`:

```go
read("GET /custom-fields", app.CustomFieldList)
write("POST /custom-fields", app.CustomFieldCreate)
write("POST /custom-fields/{id}", app.CustomFieldUpdate)
write("POST /custom-fields/{id}/retire", app.CustomFieldRetire)
write("POST /custom-fields/{id}/restore", app.CustomFieldRestore)
```

Nav: add `{Label: "Custom fields", Href: "/custom-fields", Nav: "custom-fields"}` to the **Settings** group in `nav.go:113`, beside Vocabularies and Inflation.

- [ ] **Step 1: Write the failing tests**

```go
func TestTheRegistryShowsWhoDefinedAFieldAndWhy(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	form := url.Values{
		"entity_type": {"asset"}, "code": {"cost_centre"},
		"label":       {"Cost Centre"}, "kind": {"text"},
		"description": {"SAP cost centre finance rebills against"},
	}
	if code := h.post("/custom-fields", form, false).StatusCode; code != http.StatusOK {
		t.Fatalf("creating: got %d", code)
	}
	page := body(t, h.get("/custom-fields", false))
	for _, want := range []string{
		"Cost Centre",
		"SAP cost centre finance rebills against",
		"admin", // the display name, resolved from the opaque created_by
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the registry must show %q -- it is what a new hire reads "+
				"instead of telephoning the vendor", want)
		}
	}
}

func TestTheRegistryIsAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	form := url.Values{
		"entity_type": {"asset"}, "code": {"sneaky"}, "label": {"Sneaky"},
		"kind":        {"text"}, "description": {"should never be created"},
	}
	if code := h.post("/custom-fields", form, false).StatusCode; code == http.StatusOK {
		t.Fatal("a viewer must not be able to define a custom field")
	}
}

func TestAFieldWithNoDescriptionIsRefusedWith422(t *testing.T) {
	// Validation failure re-renders the form partial with error state and
	// returns 422 -- never a 200 with the error buried in the body.
	h := newHarness(t)
	h.login("admin", "admin-password")
	form := url.Values{
		"entity_type": {"asset"}, "code": {"nameless"}, "label": {"Nameless"},
		"kind":        {"text"}, "description": {"  "},
	}
	resp := h.post("/custom-fields", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(body(t, resp), "description") {
		t.Error("the re-rendered form must say which field was rejected")
	}
}
```

- [ ] **Step 2: Run to verify they fail** — `go test ./internal/web/ -run TestTheRegistry -v`

- [ ] **Step 3: Implement**

Handlers follow `render.Respond` for the `HX-Request` branch, return 422 with the re-rendered form partial on validation failure, and 409 on a stale `row_version`. Templates carry the licence header as an HTML comment. The list page groups by entity type and shows retired fields in a separate section with their retirement date, who retired them, and the retained-value count.

- [ ] **Step 4: Run** — `make test`

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers/customfields.go web/templates/ internal/web/routes.go internal/web/handlers/nav.go internal/web/customfields_test.go
git commit -m "The registry answers who defined a field, when, and why"
```

---

### Task 6: Detail pages and the value editor

**Files:**
- Create: `web/templates/partials/custom_fields_show.html`
- Create: `web/templates/partials/custom_fields_form.html`
- Modify: `web/templates/pages/asset_detail.html`, `web/templates/pages/service_detail.html`
- Modify: `internal/web/handlers/assets.go`, `services.go` (load values, accept them on save)
- Modify: `internal/web/edits_test.go` (two `editors` entries)

- [ ] **Step 1: Write the failing tests**

```go
func TestCustomFieldsRenderInTheirOwnSection(t *testing.T) {
	// Grouped and labelled as the organisation's own, never interleaved with
	// built-in fields: a new hire must be able to tell at a glance which of
	// these invctl shipped.
	h := newHarness(t)
	h.login("admin", "admin-password")
	mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")

	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	heading := strings.Index(page, "Defined by your organisation")
	label := strings.Index(page, "Cost Centre")
	if heading < 0 {
		t.Fatal("the custom section must carry a heading naming the organisation, " +
			"not the product")
	}
	if label < heading {
		t.Fatal("the custom field appears above its own heading, which means it is " +
			"interleaved with built-in fields -- the thing a new hire cannot tell apart")
	}
}

func TestTheSectionIsAbsentWhenNoFieldIsDefined(t *testing.T) {
	// An estate that never uses the feature never sees it.
	h := newHarness(t)
	h.login("admin", "admin-password")
	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	if strings.Contains(page, "Defined by your organisation") {
		t.Fatal("the custom section renders with no fields defined")
	}
}

func TestARetiredFieldDisappearsFromTheDetailPage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	assetID := h.refs.Assets["hv-01"]
	mustSetValueViaHTTP(t, h, assetID, id, "IT-42")

	if !strings.Contains(body(t, h.get("/assets/"+assetID, false)), "IT-42") {
		t.Fatal("the value must show before retirement, or this test proves nothing")
	}
	if code := h.post("/custom-fields/"+id+"/retire", url.Values{}, false).StatusCode; code != http.StatusOK {
		t.Fatalf("retiring: got %d", code)
	}
	if strings.Contains(body(t, h.get("/assets/"+assetID, false)), "IT-42") {
		t.Fatal("a retired field still renders on the detail page")
	}
}
func TestAnInvalidValueReturns422AndKeepsTheOthers(t *testing.T) {
	// One bad value must not discard the good ones the operator also typed.
	h := newHarness(t)
	h.login("admin", "admin-password")
	tag := mustCreateFieldViaHTTP(t, h, "asset", "asset_tag", "Asset Tag", "text")
	due := mustCreateFieldViaHTTP(t, h, "asset", "warranty_ends", "Warranty ends", "date")
	assetID := h.refs.Assets["hv-01"]

	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"cf_" + tag: {"ABC-1234"},
		"cf_" + due: {"march next year"},
	}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(body(t, resp), "ABC-1234") {
		t.Error("the re-rendered form dropped the value that was valid")
	}
}
```

And add to the `editors` table at `internal/web/edits_test.go:607`:

```go
{"custom field", "/custom-fields?edit=" + fieldID, "/custom-fields/" + fieldID,
    url.Values{"label": {"Cost Centre"}, "description": {"why"}}},
```

- [ ] **Step 2: Run to verify they fail**

- [ ] **Step 3: Implement.** Widgets by kind: text input, number input, date input, checkbox, select. No inline `<script>` beyond `x-data`.

- [ ] **Step 4: Run** — `make test`, and confirm `TestEveryEditorRefusesASecondSaveFromOneToken` now covers the new editors.

- [ ] **Step 5: Commit**

---

### Task 7: Export

**Files:**
- Modify: `internal/store/export.go` (`ExportAssets` at :105, `ExportServices` at :158)

`ExportServices(rows []ServiceRow) Table` becomes `ExportServices(ctx context.Context, rows []ServiceRow) (Table, error)`; update its call sites.

- [ ] **Step 1: Write the failing tests**

```go
func TestExportIncludesACustomFieldColumn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}
			col := indexOf(table.Header, "Cost Centre")
			if col < 0 {
				t.Fatalf("no Cost Centre column; header is %v", table.Header)
			}
			if got := cellFor(t, table, f.assetID, col); got != "IT-42" {
				t.Fatalf("got %q in the Cost Centre column, want IT-42", got)
			}
		})
	}
}
func TestARetiredFieldStillExportsUnderARetiredHeader(t *testing.T) {
			...as above, then retire the field, re-export, and assert the header
			   is EXACTLY "Cost Centre (retired)" and the cell still reads IT-42.
			   Exact, not Contains: "Cost Centre" would also match the live
			   header and the test would pass without the marker...
}
func TestACustomValueIsDefusedLikeEveryOtherCell(t *testing.T) {
	// A value beginning = + - or @ is a formula the moment a colleague opens
	// the file. WP-G5 defuses at the boundary where text becomes a
	// spreadsheet, and a custom value is not an exception.
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "note", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "=1+1")

			rows, _ := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}
			got := cellFor(t, table, f.assetID, indexOf(table.Header, "note"))
			if strings.HasPrefix(got, "=") {
				t.Fatalf("the cell is still a live formula: %q. WP-G5 defuses at "+
					"the boundary where text becomes a spreadsheet, and a custom "+
					"value is not an exception", got)
			}
}
```

- [ ] **Steps 2-5:** fail, implement, `make test`, commit.

---

### Task 8: Guards, fixture and documentation

**Files:**
- Modify: `internal/seed/` (fields of every kind on an asset and a service, one entity deliberately without a value)
- Create: the API guard, in `internal/api/` or `internal/web/`
- Modify: `docs/API.md`, `CHANGELOG.md`, `docs/ROADMAP.md`

- [ ] **Step 1: The API guard**

```go
func TestCustomFieldsNeverReachTheAPI(t *testing.T) {
	// WP-A2's contract is an exact list of json tags. A custom field must
	// never appear in /api/v1: an integration must not depend on a field one
	// of this estate's administrators can retire.
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	mustSetValueViaHTTP(t, h, h.refs.Assets["hv-01"], id, "IT-42")

	got := h.apiGet(t, "/api/v1/assets?limit=1", readerAllToken)
	if strings.Contains(got.Body, "cost_centre") || strings.Contains(got.Body, "IT-42") {
		t.Fatalf("a custom field reached /api/v1: %s", got.Body)
	}
	assertGoldenJSON(t, "testdata/api/asset.json", got.Body)
}
```

- [ ] **Step 2: Fixture.** At least one field of every kind, on both an asset and a service, with one entity holding a value and one deliberately holding none — a field null everywhere is a field no test exercises, which is how WP-A2 published a column nobody had checked.

- [ ] **Step 3: Docs.** `docs/API.md` gains one line saying custom fields are deliberately absent. `CHANGELOG.md` gets an **Added** entry. `docs/ROADMAP.md` marks WP-A4 **DONE** and updates the parity row — **last, and only with both gates green.**

- [ ] **Step 4: Verify**

Run: `make lint && make test`
Expected: both clean, both engines.

- [ ] **Step 5: Commit**

---

## Definition of done

- [ ] Queries use `?` placeholders and run on both engines
- [ ] No forbidden Postgres-only feature; no `num_nonnulls()`
- [ ] Every declared-state mutation writes a `change_log` row in the same transaction
- [ ] Set replacements produce a diff on the parent
- [ ] Both editors refuse a second save with 409
- [ ] Validation failure returns 422 with the form partial re-rendered
- [ ] Non-GET routes behind CSRF and `RequireAdmin`
- [ ] Custom fields absent from `/api/v1`, asserted
- [ ] `make lint` and `make test` green on both engines
- [ ] Licence header on every new file, blank line before the package clause
- [ ] No new dependency
