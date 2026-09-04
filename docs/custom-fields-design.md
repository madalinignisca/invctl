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
| `number` | the trimmed original text | a decimal number representable by an `<input type="number">` widget — the WHATWG "valid floating-point number" grammar exactly: an optional leading `-` (never `+`), one or both of a digit series and a `.`-prefixed digit series (so `.5` is valid — the integer part is optional when a fraction follows — and so is `1e3`, an exponent form), and nothing else. `1,234`, `1 234`, a leading `+`, and a bare trailing `.` (`5.`) are refused |
| `date` | `YYYY-MM-DD` | a real calendar date with a year **greater than zero** — HTML's valid-date-string grammar excludes year `0000`, which Go's own calendar does not, so the domain layer rejects it explicitly |
| `boolean` | `true` / `false` | exactly one of those two after normalisation |
| `select` | the option's `value` | must be a **live** option of that field |

Validation lives in a `domain` constructor, which returns a `ValidationError`;
the `CHECK` constraint is the second line of defence, never the first. A
validation failure re-renders the form partial with error state and returns
**422**, per the HTTP conventions.

**The invariant, stated once rather than left implicit in each row above:
every value the store accepts must survive a round trip through its own form
widget unchanged.** A value the store holds but its widget cannot draw back is
not a display bug — the browser draws the widget's own blank instead, and a
form's own blank posts as an explicit clear (§6), so the next unrelated save
on that entity silently destroys the value. `number`'s grammar above is
narrower than "a decimal number" in the abstract for exactly this reason: it
is the grammar of a decimal number an `<input type="number">` can actually
render, which is a stricter thing — and it is also, in one direction, WIDER
than the naive reading of that grammar: an absent integer part (`.5`) and an
exponent (`1e3`) both round-trip through the widget, so both are accepted
rather than refused. `date`'s year-zero exclusion is the same invariant
applied to a kind whose validator otherwise checks against a real calendar
rather than a widget grammar — Go's calendar has no lower bound, HTML's does.
The same invariant is why `select` displays, and marks as retired, a value
naming an option retired since it was set (below) rather than omitting it
from the widget.

The round-trip invariant is proved by `TestEveryStoredCustomValueSurvivesA-
RoundTripThroughItsWidget` (`internal/web/customfield_roundtrip_test.go`)
against two INDEPENDENTLY written models of each load-bearing kind's widget
grammar — one in `internal/domain` (the validator, deciding what may be
stored) and one in the test itself (the oracle, modelling what the widget
renders back) — never one implementation shared between the two. A shared
implementation would make the test agree with itself regardless of whether
either side is right, which is exactly how this invariant was violated twice
in the same kind before the oracle caught the second violation.

**A field's `kind` cannot change once any value exists.** The label and the
description stay editable forever — those are the things an administrator
actually needs to correct — but retyping a field with 214 text values into a
number is a data migration wearing a form control. The refusal names the count
and suggests creating a new field.

**Retiring a `select` option is NOT the same as retiring a field, and the
difference is the opposite of what it sounds like.** Retiring a FIELD keeps
its values in storage but removes them from every surface -- the detail page,
the editor, everything -- because a value the form draws is a value a
clear-all can enumerate and destroy, so not drawing it IS the protection (see
"a submission may only name what the operator was shown" below); Restore
brings back the field and every retained value. Retiring an OPTION is the
case where a value does keep displaying: no new value may select it, but the
one already stored is still drawn. "Keeps displaying" means literally that: the value editor renders the retired option
too, marked as retired, whenever it is the value the entity currently holds —
never for any other retired option — so the widget can actually draw back
what is stored (the invariant above) instead of falling back to its own blank
"not set" choice, which the next unrelated save would then post as an
explicit clear.

**All three editors carry `row_version` and refuse a second save from one
token**, returning 409 — the definition editor and the options editor, both
on `custom_field` (each its own token, since they can be open independently),
and the value editor on **the parent asset or service**, not on
`custom_field_value`. A submission is applied per entity in one statement, so
a per-value token would need one hidden token per rendered field and would
still not describe what the operator is editing: they are editing `vm-db-2`,
not `vm-db-2`'s cost centre. The audit entry is written against the entity
for the same reason, and every other editor in this repo is per-entity.
Setting values bumps the parent's `row_version`. Invariant 4 admits no
exception, and `TestEveryEditorRefusesASecondSaveFromOneToken` walks the
editors, so a new one without it fails an existing guard rather than
shipping.

---

## 4. Attribution — the registry

`/custom-fields`. Flat, not under an `/admin` prefix: this repo has no such
prefix, and `/teams`, `/vocabularies` and `/inflation` are all administrative
surfaces sitting at the top level. It lists, for each field: label, code,
kind, entity type, description, who defined it, when, how many entities hold
a value, and whether it is retired and by whom.

**`GET /custom-fields` ships as `read()`** (`internal/web/routes.go`),
reachable by any authenticated user — only the mutating routes
(`POST /custom-fields`, `.../{id}`, `.../{id}/retire`, `.../{id}/restore`,
`.../{id}/options`) sit behind `RequireAdmin` and CSRF like every other
mutating surface, carried by the `write()` route helper. That is deliberate:
the support-burden goal this whole feature is built for (see the opening
paragraph) is served better by a read-only viewer being able to open the
registry and see who defined a field and why than by locking the whole page
down. `TestTheRegistryIsReadableByAnyAuthenticatedUser` and
`TestDefiningACustomFieldIsAdminOnly` (`internal/web/customfields_test.go`)
cover the read half and the write half separately, each named for what it
actually asserts.

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

### An individual was the wrong answer to "who do I ask"

`created_by` answers "who defined this field" — a fact worth keeping, and it
stays. But a senior review found that the product's answer to the question
somebody stuck actually asks — **who do I ask about this** — was that same
column, resolved to an individual's display name. That fails in exactly the
turnover scenario this feature exists to defend against: the person who
defined `cost_centre` leaves the company. The registry's own `LEFT JOIN`
comment (`internal/store/customfields.go`) already documents that a GDPR
erasure request against that person leaves the row "readable... without a
name to show for it" — one erasure request quietly blanks the feature's only
attribution surface.

`custom_field.owner_team_id` (migration `00054`) is the fix, and it reuses
rather than invents: `team.contact_ref` (migration `00014`) already exists
for exactly this, already documented and GDPR-argued as non-personal — "a
GROUP address, a ticket queue or a channel... never an individual". A team
outlives the people in it the same way it already does on `asset.team_id`
and `service.team_id`.

**Nullable in the schema, required on the create form, with no escape
hatch.** The eleven fields that existed before this migration have no owner
and a migration cannot invent one — nobody can guess who owns `cost_centre`
from the schema alone. They become visible orphans on the registry,
deliberately: **finding and assigning them is a separate piece of work**, not
something this change should paper over with a default team nobody chose.
Every field defined through the create form from this point on must name an
active team; an estate with no active teams is refused outright, pointed at
`/teams`, the same wording migration `00014`'s own comment already uses
("create the teams first and set `team_id`"). The owner stays editable
forever after that, the same as `label` and `description`.

**A RETIRED team is not offered for a NEW field, but keeps displaying on a
field that already names it** — the identical rule this document already
states for a retired `select` option (§3 above), for the identical reason:
what is STORED must keep displaying, what is RETIRED must not be newly
selectable. The registry, the edit form's owner picker, and the value
editor's owner line all mark a retired owner "(retired)" rather than hiding
it.

Rendered everywhere the support-burden goal (this document's opening
paragraph) actually needs it: the registry beside "Defined by", the
detail-page show panel, and — the moment a senior review specifically called
out as missing — the value editor, right beside the validation error an
operator is looking at when they need to know who to ask. The team's
`contact_ref` is shown when it has one, since that is the actionable part;
the team's own code is the fallback when it does not.

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
    CustomFields string `db:"custom_fields"`   // "asset_tag@1,cost_centre@3"
}
```

Each value is folded as a **change counter**, not the value itself — see the
GDPR note below.

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

### GDPR: a change counter, not the value

`foldCustomValues` (`internal/store/customvalues.go`) folds a **plain change
counter** into each pair instead of the value itself — `cost_centre@3`,
where `3` is `custom_field_value.row_version` at the point the fold runs.
`setCustomValues` already carries `row_version` across the delete-then-insert
that replaces this set — advancing it by one on a real change, preserving it
verbatim on a no-op resubmission — so it is already exactly the change token
this fold needs, and nothing invents a second counter.

This keeps the property the fold exists for — a set replacement that leaves
every column of the parent untouched still produces a diff, because the
counter still moves when the value does — while writing nothing about the
value itself into `change_log`.

**This replaced a keyed HMAC-SHA256 digest** (`code=#<digest>`), which held
for three days before a review found it was the wrong primitive. A digest
whose key sits in the same database and backup as the log it protects is
**pseudonymisation, not anonymisation** (GDPR Art. 4(5), Recital 26) — the
"additional information" that could re-identify a value was not "kept
separately", so `INV_AUDIT_FOLD_KEY` had to be qualified with an
environment-variable caveat to reach the GDPR-correct deployment at all. On
inspection it was worse than that: identical values digest identically and
carry no other secret, so for a `select` or `boolean` field a reader needs no
key whatsoever to invert one — `/changes` is readable by any authenticated
user (§4), and a `select` field's option list is published on the registry,
so every possible value is already public. A 48-bit digest could also
collide on one (field, entity) pair and write **no `change_log` row at
all** — `diffJSON` reporting `changed=false` while `row_version` bumped and
the value changed on the entity's own page, a mutation of declared state
with no audit entry.

A counter has none of this. It is not personal data under any reading,
because it carries no information about the value at all — not even whether
two different values are the same length or the same shape. It cannot
collide between two values of the same field, because it is monotonic
rather than a hash of bounded width. And there is no key: nothing to
generate, hold outside the database, divulge in an environment variable, or
accidentally rotate under a running deployment's data.

**The cost is unchanged, still real, and still accepted, not hidden.**
`change_log` shows that a custom value changed and which field, **never what
it changed to**. The current value still lives in `custom_field_value` and
on the entity's own page; only the audit trail's copy of it is gone. Do not
present this as having made a custom field's value disappear — it has not,
and the value **editor** (`custom_fields_form.html`) still carries a warning
for exactly that reason. The **creation** form (`custom_field_form.html`)
carries the matching one, worded for whoever names the field rather than
whoever later types a value.

**The fix is forward-only, and the log is now heterogeneous across two
boundaries rather than one.** `change_log` is append-only — no `UPDATE`, no
`DELETE`, ever — so an entry written before the digest still holds the
plaintext value it was written with, and an entry written under the digest
(a matter of days, in this deployment's history) still holds
`code=#<digest>`. Neither is rewritten. Only an entry written from this
change forward carries `code@<n>`. A reader of an old entry meets a format
that no longer exists in the code, and that is expected, not a bug — the fix
only changes what NEW entries record.

Neither `docs/API.md` nor the registry repeats the warning; nothing in
WP-A4 propagates it anywhere beyond the two forms named above.

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

A dedicated CSV export, `ExportAssetCustomFields` / `ExportServiceCustomFields`,
gains one column per field, live or retired — separate from `ExportAssets` and
`ExportServices`, which carry none. See the correction at the end of this
section for why they are separate rather than merged.

**Every header is `Label (code)`, unconditionally — never plain `Label`, and
never conditional on whether another field happens to collide.** A retired
column reads `Label (retired YYYY-MM-DD) (code)`, the marker, the day it was
retired, and the code all applied every time. `custom_field.label` carries no
uniqueness constraint (§2/§3, deliberately — a cross-task constraint might
forbid a legitimate rename), so two live fields, or two retired ones, can
share one label. An earlier version of this rule appended the code only when
a collision was actually detected, and that failed twice over:

- the appended form (`Label (code)`) could itself collide with an untouched
  field whose OWN label already happened to read `Label (code)` — a
  disambiguation pass that can disambiguate its way into a fresh collision
  is not a fix;
- worse, a brand new field arriving with a label that collides with an
  existing one silently renamed the EXISTING field's header, from `Label` to
  `Label (code)`, with nothing about the existing field having changed. A
  diff, or any spreadsheet tool keyed by header name, reads that as a column
  removed and another added — denting this section's own promise that the
  export is where an operator goes to see what they still hold.

Appending the code unconditionally removes both failure modes for a header's
dependence on OTHER fields: it never changes because something else was
created, retired or restored, and a field's header depends only on that
field's own code and retirement state — never on anything about a different
field.

**It does NOT also survive a rename of the field's own code**, and this
section should not claim otherwise: `UpdateCustomField` edits `code` freely,
the same as `label` (`TestCodeStaysEditableWithValues`,
`internal/store/customfields_test.go`), and nothing here freezes it the way
a `kind` change is refused while values exist. Renaming a code moves this
header exactly as renaming a label does, and it moves something else too:
`foldCustomValues` (`internal/store/customvalues.go`) keys the `change_log`
audit fold on `code`, so renaming a field's code rewrites that fold for
every entity already holding a value against it. The next unrelated value
edit on any of them then logs a `custom_fields` diff for a value that did
not itself change, because the KEY naming it did.

**The claim is exactly this much, and Ruling AQ exists because two earlier
versions of this section claimed more than is true:**

1. **Live vs live is collision-free.** Two live fields' headers can never
   match, because `code` is unique among LIVE rows only —
   `custom_field_live_code_key` (§2) is a *partial* index. This is the
   guarantee that actually matters day to day, and it is genuinely true.

2. **Any other pair CAN collide, and that is accepted, not closed.** A label
   is free text, so a label can be written to imitate whatever marker this
   header format composes — there is no marker a human cannot copy. Two
   RETIRED fields sharing a code, a label, and the same UTC-day retirement
   date collide (§6 explicitly touts code reuse after retirement as
   intended, not abuse, so this is ordinary use, not a contrived attack). And
   a LIVE field can imitate a RETIRED field's entire dated header, not just
   the bare word: a field labelled literally `"Support (retired 2026-01-10)"`
   with code `support` renders byte-identically to a *retired* field
   labelled `"Support"` with code `support` that was retired on that exact
   day — a date is precisely the kind of string somebody copies out of a
   changelog, a ticket, or a report like this one. No header component
   composed from user-editable text closes this; only the field id would,
   and it is unreadable, which defeats the point of a header. Two earlier
   drafts of this section claimed the marker, then the retirement date,
   closed collision "completely" — both claims were tested against a real
   engine and both were false.

3. **A collision is a display ambiguity only, never a data one — the
   sentence that makes accepting (2) reasonable rather than reckless.**
   Every value stays keyed by `field_id` internally, so two columns sharing
   an identical header still each carry their own field's values correctly;
   nothing is ever written under, read from, or attributed to the wrong
   field. An operator who sees two identically headed columns consults the
   registry (§4) to tell them apart; what they cannot do is trust the header
   alone as an identifier, which was never a promise this export made for
   any column, custom or built-in.

The cost is a redundant-looking suffix on a header whose code is just the
snake_case of its label — noise for a human, and exactly the price worth
paying for a header that is unambiguous for a tool and stable across a diff,
which is what this section is actually for. **Do not make this conditional
again for readability** — that is the mistake this rule replaced, twice.

Every value passes through the same formula-injection defusing WP-G5 already
applies at the boundary where text becomes a spreadsheet.

*Custom fields leave through their own export, `ExportAssetCustomFields` and
`ExportServiceCustomFields`, deliberately separate from `ExportAssets` and
`ExportServices`. An earlier version of this section put the custom-field
columns directly on the asset and service exports and argued FOR that on the
grounds that a separate export "means the asset export no longer
round-trips" — that reasoning was backwards. `assetImportColumns`
(`internal/store/import.go`) is a closed, unknown-column-is-an-error set that
has never included a custom-field column, so folding them into `ExportAssets`
did not preserve the round trip; it broke it, silently, the moment an estate
defined even one custom field — `ParseAssetCSV` refused the file outright.
The separate export is the option that PRESERVES the round trip: `ExportAssets`
and `ExportServices` stay exactly the importer's own column set (services have
no importer yet, but the same rule applies pre-emptively), and the
custom-field columns live in their own, explicitly non-importable download —
the same footing `ExportPrefixes` already documents for itself: chosen to be
READ, not to round-trip. `attrs` is excluded from both the importer and this
export, same as it always was.*

`ExportAssets(ctx, rows) (Table, error)` and `ExportAssetCustomFields(ctx,
rows) (Table, error)` both take a context and can query. `ExportServices(rows)
Table` is a free function again, same as before this work package briefly
turned it into a `*SQLStore` method to resolve a custom-field column set that
no longer lives there; `ExportServiceCustomFields(ctx, rows) (Table, error)`
is the method that needs the database now.

---

## 8. Rendering

```
web/templates/partials/custom_fields_show.html    the grouped section
web/templates/partials/custom_fields_form.html    the inputs
web/templates/pages/custom_field_list.html        the registry
web/templates/partials/custom_field_form.html     define / edit a field
```

The show and form partials are included by the asset and service pages and must
be renderable standalone, as every partial here must. Widgets follow the kind: a text input, a number input, a date input, a select —
and **a three-state select for `boolean`**, offering blank, yes and no.

Not a checkbox. A checkbox cannot represent "no assertion": unchecked and
unrecorded are the same state on the wire, so a shared multi-field form posts a
value for every boolean on every save. An operator opening the panel to correct
an unrelated text field would thereby record `false` against every boolean the
entity had never held — the UI fabricating a negative declaration as a side
effect of an unrelated edit. The three-state select submits an empty string for
"not recorded", which is exactly the clear signal the other four kinds already
use, so it reuses a path that is already correct instead of needing one of its
own. No new
JavaScript — Alpine only for local state if a form needs it, and nothing that
fetches.

---

## 9. Tests

Table-driven, against the fixture estate, on both engines via `make test`. The
fixture gains at least one field of every kind, on both an asset and a service,
with at least one asset holding a value and one deliberately holding none — a
field that is null everywhere is a field whose behaviour no test exercises,
which is how WP-A2 published a column nobody had checked.

- `TestSettingAValueIsAuditedAgainstTheEntity`
- `TestReorderingCustomValuesIsNotAChange`
- `TestRetiringAFieldKeepsEveryValue` / `TestARetiredFieldOffersRestoreAndKeepsItsValues`
- `TestARetiredFieldStillExportsUnderARetiredHeader`, with the `(retired)` header
- `TestAFieldsKindCannotChangeWhileValuesExist`
- `TestASelectValueMustBeALiveOption`
- `TestRetiringAnOptionKeepsItSelectableOnExistingValues`
- `TestTheRegistryResolvesTheCreatorWithoutStoringAUsername` — `created_by` is an id, and scrubbing
  the user leaves the row readable
- `TestCustomFieldsNeverReachTheAPI` — the WP-A2 DTOs and golden files are
  unchanged by any custom field existing
- `TestAValueForAnUnknownFieldIsRefused`
- `TestClearingAValueIsAuditedOnTheParent` — the set-replacement diff actually
  appears, rather than the row vanishing silently
- `TestEveryEditorRefusesASecondSaveFromOneToken` — 409, per invariant 4
- `TestAnAuditShapeEmbedsByValue` — the pointer-embed panic, asserted rather
  than relied upon
- Every mutation writes a `change_log` row, per the standing guard

Seven of the thirteen names above were wrong until 2026-09-03. Every one of
those tests existed and had done since the work package merged -- under a
different name -- so this list read as a coverage inventory while being a
false index: a reader checking whether a property was tested would have
found nothing and concluded it was not. That is worse than an absent list,
because an absent list invites you to go and look.

`TestSectionNineNamesTestsThatExist` (internal/store) now parses this
section and fails if a name here has no matching `func` under internal/.
Rename a test and this list fails with it, which is the only arrangement
under which a documented index stays true.

---

## 10. Out of scope, restated

Search, filtering, the read-only API, entity types beyond asset and service,
bulk value editing, importer support, per-field permissions, and computed or
derived fields. Each is additive later; none is load-bearing now.
