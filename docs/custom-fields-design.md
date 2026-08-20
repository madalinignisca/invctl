# Custom fields — design (WP-A4)

Estate-specific attributes without a migration. An administrator defines a
field once, it appears on every asset or service, and the values are edited,
audited and exported like anything else this system stores.

**The constraint that shaped this design is not in the roadmap entry.** A
company defines a handful of custom fields, then hires somebody new. That
person opens an asset page, sees "Cost Centre", assumes it is part of invctl,
and when it behaves unexpectedly they call the vendor. Every decision below
that looks like extra work — the registry page, the description column, the
grouped section, keeping values after retirement — exists to make the product
answer "who added this, when, and why" without a phone call.

---

## 1. What this is not

- **Not searchable and not filterable.** Values never appear in the search
  index and there is no `?cf_...=` query parameter. This is a deliberate step
  back from the roadmap's "searchable": every query surface is another place a
  field can appear to misbehave, and the design goal is that there is exactly
  one place to look — the entity's own page.
- **Not published in the read-only API.** WP-A2's `TestTheContractIsExactlyTheseFields`
  asserts each DTO's json tags are exactly a literal list. Custom fields must
  not appear in `/api/v1`, and a guard asserts it rather than leaving it to the
  allowlist's good behaviour. An integration must never depend on a field one
  of this estate's administrators can retire.
- **Not on every entity.** Assets and services only. Adding `project` later is
  a `CHECK` constraint and a template include, not a redesign.
- **Not in the CSV importer.** WP-A1 imports do not set custom values.
- **Not a second vocabulary system.** `internal/help` already draws the line
  this feature extends: seven lookup tables are editable "because they describe
  an estate's own conventions", while the values whose meaning the ENGINE
  defines live in Go. A custom field is the far end of the same axis — entirely
  the estate's, meaning nothing to the engine.

---

## 2. Schema

Three tables. No column is added to `asset` or `service`.

```sql
custom_field
  id           TEXT PRIMARY KEY         -- UUIDv7, generated in Go
  entity_type  TEXT NOT NULL CHECK (entity_type IN ('asset','service'))
  code         TEXT NOT NULL            -- machine name, lower case
  label        TEXT NOT NULL            -- what a reader sees
  kind         TEXT NOT NULL CHECK (kind IN
                 ('text','number','date','boolean','select'))
  description  TEXT NOT NULL            -- why this field exists. NOT optional.
  created_by   TEXT NOT NULL REFERENCES app_user(id)
  created_at   TEXT NOT NULL            -- RFC3339 UTC, generated in Go
  retired_at   TEXT
  retired_by   TEXT REFERENCES app_user(id)
  row_version  INTEGER NOT NULL DEFAULT 1

CREATE UNIQUE INDEX custom_field_live_code_key
  ON custom_field (entity_type, code) WHERE retired_at IS NULL;

custom_field_option                     -- `select` only
  id           TEXT PRIMARY KEY
  field_id     TEXT NOT NULL REFERENCES custom_field(id)
  value        TEXT NOT NULL
  label        TEXT NOT NULL
  position     INTEGER NOT NULL
  retired_at   TEXT
  UNIQUE (field_id, value)

custom_field_value
  id           TEXT PRIMARY KEY
  field_id     TEXT NOT NULL REFERENCES custom_field(id)
  entity_id    TEXT NOT NULL            -- asset.id or service.id
  value_text   TEXT NOT NULL
  created_at   TEXT NOT NULL
  updated_at   TEXT NOT NULL
  row_version  INTEGER NOT NULL DEFAULT 1
  UNIQUE (field_id, entity_id)
```

**`description` is NOT NULL and not empty.** An administrator who cannot say
why a field exists is the origin of the support call this design is built
against, and the cheapest moment to ask is while they are creating it.

**`entity_id` carries no foreign key**, because it points at either an asset or
a service. That is the established shape here — `change_log`, `asset_health`
and `observed_transition` all reference entities as `(entity_type, entity_id)`
with no FK. Orphans cannot arise from deletion, because neither assets nor
services are ever hard-deleted. `entity_type` lives on the DEFINITION only, so
there is one source of truth rather than two that can drift apart.

**The unique index is partial**, so a retired `cost_centre` can be created
again later. Both engines accept `CREATE UNIQUE INDEX ... WHERE` with identical
syntax; the migration must still be verified on both.

**One value column, not four.** `value_text` holds every type, with the type
held on the definition — dates as `YYYY-MM-DD`, booleans as `true`/`false`,
numbers canonicalised. Typed columns would need a constraint saying exactly one
is non-null, and `num_nonnulls()` is forbidden here, leaving a long `CASE`
expression that must behave identically on both engines. Storing everything as
text also matches how this repo already stores every id and every timestamp.

---

## 3. Types and validation

| kind | stored as | validated as |
|---|---|---|
| `text` | the text | non-empty after trim, bounded length, no control characters |
| `number` | canonical decimal | a decimal number; `1,234` and `1 234` are refused |
| `date` | `YYYY-MM-DD` | a real calendar date |
| `boolean` | `true` / `false` | exactly one of those two after normalisation |
| `select` | the option's `value` | must be a **live** option of that field |

Validation lives in a `domain` constructor, which returns a `ValidationError`;
the `CHECK` constraint is the second line of defence, never the first. A
validation failure re-renders the form partial with error state and returns
**422**, per the HTTP conventions.

**A field's `kind` cannot change once any value exists.** The label and the
description stay editable forever — those are the things an administrator
actually needs to correct — but retyping a field with 214 text values into a
number is a data migration wearing a form control. The refusal names the count
and suggests creating a new field.

**Retiring a `select` option follows the same rule as retiring a field**:
existing values keep displaying, no new value may select it.

**Both editors carry `row_version` and refuse a second save from one token**,
returning 409 — the definition editor on `custom_field`, and the value editor on
**the parent asset or service**, not on `custom_field_value`. A submission is
applied per entity in one statement, so a per-value token would need one hidden
token per rendered field and would still not describe what the operator is
editing: they
are editing `vm-db-2`, not `vm-db-2`'s cost centre. The audit entry is written
against the entity for the same reason, and every other editor in this repo is
per-entity. Setting values bumps the parent's `row_version`. Invariant 4 admits no exception, and
`TestEveryEditorRefusesASecondSaveFromOneToken` walks the editors, so a new one
without it fails an existing guard rather than shipping.

---

## 4. Attribution — the registry

`/custom-fields`, behind `RequireAdmin` and CSRF like every other mutating
surface. Flat, not under an `/admin` prefix: this repo has no such prefix, and
`/teams`, `/vocabularies` and `/inflation` are all administrative surfaces
sitting at the top level with the `write()` route helper carrying the guard. It lists, for each field: label, code, kind, entity type,
description, who defined it, when, how many entities hold a value, and whether
it is retired and by whom.

**`created_by` and `retired_by` hold an opaque `app_user.id`**, joined for
display, exactly as `change_log.actor` does. Scrubbing an `app_user` row for an
erasure request leaves the registry intact and simply stops resolving a name —
the same property that lets the audit trail be kept forever without a retention
argument.

On a detail page, custom fields render in **their own section**, under a
heading naming the organisation rather than the product, never interleaved with
built-in fields. The section is absent entirely when no live field is defined
for that entity type, so an estate that never uses the feature never sees it.

*Usage counts cost one `COUNT` per field on registry load. At the scale this
page operates — tens of fields — that is not worth caching, and stating it here
is cheaper than discovering it later.*

---

## 5. Audit

**A value change is audited against the asset or service, not against the
field**, because the question a reader is asking is "what changed about
`vm-db-2`".

The mechanism is the one already proven for sets. `assetAudit` embeds
`domain.Asset` BY VALUE and folds the environment set into a sorted,
comma-joined string; `auditedAsset(a, codes)` builds it; `t.logUpdate` diffs
before against after. Custom values fold in the same way, as a second field:

```go
type assetAudit struct {
    domain.Asset
    Environments string `db:"environments"`
    CustomFields string `db:"custom_fields"`   // "asset_tag=ABC,cost_centre=IT-42"
}
```

Sorted by code before joining, so reordering is never reported as a change —
the same reason `dependencyAudit` sorts its classes.

`auditedAsset(a *domain.Asset, codes []string) *assetAudit` gains the values it
must fold, and every existing call site is updated with it. A call site that
still compiles while passing nothing would produce an entry that looks complete
and silently omits the custom values — the failure this fold exists to prevent.

`diffJSON` walks only `db`-tagged struct fields and cannot expand a map, so
per-field diff keys would require changing `auditFields`, which every entity's
audit flows through. That function's own comment records its last defect being
"invisible for a week", and a folded string carries both the old and the new
value in full. The precedent wins; the machinery is left alone.

**Services need a `serviceAudit` type that does not yet exist.** `UpdateService`
currently logs `&before.Service` and `svc` directly. Introducing the wrapper is
part of this work, and it must embed `domain.Service` **by value** — an
anonymous pointer embed silently drops every column from the entry while still
writing one, which `auditFields` now panics on rather than tolerates.

Field definitions are declared state in their own right: create, edit, retire
and restore each write their own `change_log` row against `custom_field`.

**A custom value's text enters `change_log` permanently, and redaction is
all-or-nothing.** `domain.IsRedacted` consults a global list keyed by column, so
the whole fold CAN be redacted wholesale under `custom_fields` — but the fold is
a single opaque key, so there is no name INSIDE it to key on, and no way to
redact one field's values while keeping the rest. The lever exists and it is a
blunt one: pulling it hides every custom value from every audit entry.
`docs/AUDIT.md`'s position that the audit trail "carries no personal data and
can be kept forever with no retention argument" therefore rests, for this
feature alone, on no administrator creating a field that holds personal data:
an "Owner email", a "Contact", a "Requested by".

This is stated rather than engineered. The principled fix is a per-field
"holds personal data" flag that redacts that field's value inside the fold, and
it is a schema change deferred to a later work package. Until then the
constraint is operational and belongs where the administrator is: the field
creation form warns that a custom field's values are recorded permanently in
the audit trail and must not hold personal data, and `docs/API.md` and the
registry repeat it. An estate that needs such a field needs the flag first.

---

## 6. Retirement and restore

Retiring sets `retired_at` and `retired_by`. The field disappears from detail
pages and from forms. **Retiring a field deletes no value, ever** — soft delete
for entities is the rule, and a bulk delete of 214 declared values is exactly
what this codebase does not do.

**A submission says what it means, and says nothing about what it omits.** A
field absent from a submission is UNTOUCHED; an explicit empty value clears it.
Absence is never read as a clear, because a form's field set and the writer's
field set diverge the moment anything retires or restores a field between render
and submit — and a writer inferring intent from absence inherits every one of
those discrepancies.

**And the converse, which is the same rule read backwards: the operator must be
SHOWN everything the submission will decide.** A form that draws a blank where a
value already exists produces a submission that is honest about what it names
and wrong about what it means — the writer commits the empty draw faithfully.
Both halves are needed. Enforcing only the first makes the render path the place
the data is lost.

It follows that **a submission may only name what the operator was shown.** A
clear-all posts an explicit blank for each field THE FORM DREW — not for every
value the entity holds (`CustomValuesFor` returns retired rows deliberately), and
not for every field live at submit time (another administrator may have added one
since). Both of those are payloads assembled from a query rather than from a
rendering, and both destroy data the operator never saw.

**An operator clearing one value is a different act, and it does remove the
row — including on a retired field, which is precisely why no surface may
enumerate a retired field's value into a submission.** Restore
therefore brings back the field and every value STILL RETAINED. That is correct rather than an exception: `custom_field_value` holds the
current value of something its parent owns, which is the same shape as
`asset_environment` and `dependency_data_class` — "set and index tables are
replaced wholesale, and that is not deletion". Delete-then-insert inside the
parent's transaction is the right mechanism, and the parent's `change_log`
entry MUST record it. That is precisely what the folded `CustomFields` string
in §5 is for: this repo has been bitten three times by a set replacement that
produced no diff on the parent struct and therefore no audit entry at all.

The registry shows a retired field with its retirement date, who retired it,
and how many values are retained, and offers **Restore**, which clears the two
columns and brings the field and every value back. Restore is refused if a live
field already holds the same `(entity_type, code)`.

---

## 7. Export

CSV export gains one column per field, live or retired, with retired columns
headed `Label (retired)`. Every value passes through the same formula-injection
defusing WP-G5 already applies at the boundary where text becomes a
spreadsheet.

*A wide file is the accepted cost. An estate with thirty historical fields gets
thirty extra columns, and the alternative — a separate custom-fields export —
means the asset export no longer round-trips.*

`ExportAssets(ctx, rows) (Table, error)` already takes a context and can query.
`ExportServices(rows) Table` does not, and gains both — a signature change this
work package owns.

---

## 8. Rendering

```
web/templates/partials/custom_fields_show.html    the grouped section
web/templates/partials/custom_fields_form.html    the inputs
web/templates/pages/custom_field_list.html        the registry
web/templates/partials/custom_field_form.html     define / edit a field
```

The show and form partials are included by the asset and service pages and must
be renderable standalone, as every partial here must. Widgets follow the kind:
a text input, a number input, a date input, a checkbox, a select. No new
JavaScript — Alpine only for local state if a form needs it, and nothing that
fetches.

---

## 9. Tests

Table-driven, against the fixture estate, on both engines via `make test`. The
fixture gains at least one field of every kind, on both an asset and a service,
with at least one asset holding a value and one deliberately holding none — a
field that is null everywhere is a field whose behaviour no test exercises,
which is how WP-A2 published a column nobody had checked.

- `TestACustomValueChangeIsAuditedAgainstTheEntity`
- `TestReorderingCustomValuesIsNotAChange`
- `TestRetiringAFieldKeepsEveryValue` / `TestRestoringAFieldBringsThemBack`
- `TestARetiredFieldStillExports`, with the `(retired)` header
- `TestAFieldsKindCannotChangeWhileValuesExist`
- `TestASelectValueMustBeALiveOption`
- `TestARetiredOptionKeepsExistingValuesAndRefusesNewOnes`
- `TestTheRegistryNeverStoresAUsername` — `created_by` is an id, and scrubbing
  the user leaves the row readable
- `TestCustomFieldsNeverReachTheAPI` — the WP-A2 DTOs and golden files are
  unchanged by any custom field existing
- `TestAValueForAnUnknownFieldIsRefused`
- `TestClearingAValueIsAuditedOnTheParent` — the set-replacement diff actually
  appears, rather than the row vanishing silently
- `TestBothCustomFieldEditorsRefuseASecondSave` — 409, per invariant 4
- `TestServiceAuditEmbedsByValue` — the pointer-embed panic, asserted rather
  than relied upon
- Every mutation writes a `change_log` row, per the standing guard

---

## 10. Out of scope, restated

Search, filtering, the read-only API, entity types beyond asset and service,
bulk value editing, importer support, per-field permissions, and computed or
derived fields. Each is additive later; none is load-bearing now.
