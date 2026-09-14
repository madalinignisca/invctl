# Device-type component templates — design

**WP-C1, the unbuilt half.** Status: design, not built.
Decisions taken with Gabriel 2026-09-13.

**Correction, 2026-09-14: `power_input` is not a template kind.** The
sections below still describe it as one because that is what was designed on
2026-09-13; Task 4's build made the exclusion concrete and it is recorded
here rather than silently reworded. `power_input.feed_id` is
`NOT NULL REFERENCES power_feed(id)` — the row **is** the connection to a
feed, not a count of how many PSUs a model has. A catalogue template has no
feed to point at and cannot acquire one without a power-model change that is
out of scope, so a power-input template component could never be
instantiated into a real row. A `kind` an operator can select that is
guaranteed to instantiate into nothing is a trap, not a feature. `kind`
keeps its discriminator shape so `port` or `outlet` can be added once they
have somewhere to go; only `power_input` itself is refused. See migration
`00067`'s header for the full reasoning.

## The problem, measured

`DCS-7050SX3-48YC8` is a 48-port switch. The demo estate has **four of them and
fourteen interfaces between them.**

That is the whole argument. Nobody hand-types 48 ports, so the estate is not
merely tedious to maintain — it is quietly *incomplete*, and every answer
computed over ports (what is patched, what is free, what a cut takes out) is
computed over a fraction of the real thing. A `PowerEdge R660` fares better at
four interfaces per asset across six assets, which is what "somebody typed the
ones they cared about" looks like.

## Goal

A `device_type` carries a list of the components every instance of that model
has. Creating an asset of that type brings them into existence.

## Non-goals, and why each is out

- **Modules and module bays.** A chassis with bays, and modules that occupy one
  and carry their own components, is a second data model with its own
  instantiation semantics. Nothing in the estate or on the roadmap depends on
  it. Its own entry.
- **Inventory items.** Recording a specific SFP or disk against an asset is a
  third model. Same reasoning.
- **Outlets.** WP-C1's struck-through text lists "outlets" as a component kind
  and **there is no outlet table to instantiate into.** WP-B1 describes the
  chain as "panel → feed → outlet → asset input"; what shipped is panel → feed →
  input, with `power_input.feed_id` referencing the feed directly. Either
  outlets were deliberately collapsed into feeds and B1's prose is stale, or it
  is a fourth scope unbuilt inside a DONE marker — alongside A3, B3 and C1
  itself. **That is a power-model question and it is not resolved here.** It is
  recorded in ROADMAP against B1 so it stops being invisible.
- **The generic mechanism (WP-A3).** Deliberately not extracted. This codebase
  extracts a shared mechanism when the second caller appears, and A3's own entry
  and the roadmap appendix both say to build this concretely here and extract
  later. Building the abstraction first is the thing that has been argued
  against twice in writing.

## Data model

One table, one row per component a model has.

As built (see the correction above — power_input was designed here but
excluded before Task 4 shipped):

```sql
CREATE TABLE device_type_component (
  id              TEXT PRIMARY KEY,
  device_type_id  TEXT NOT NULL REFERENCES device_type(id),
  kind            TEXT NOT NULL
                    CONSTRAINT dtc_kind_check CHECK (kind IN ('interface')),
  name            TEXT NOT NULL,
  position        INTEGER NOT NULL,

  -- interface-shaped, NULL if a future kind doesn't use them
  form_factor     TEXT REFERENCES interface_form_factor(code),
  speed_mbps      INTEGER,
  is_mgmt         ...,

  lifecycle, created_at, updated_at, row_version
);
CREATE UNIQUE INDEX dtc_name_key ON device_type_component(device_type_id, kind, name)
  WHERE lifecycle = 'active';
```

**One table rather than one per kind.** The attribute sets barely overlap, which
argues for splitting — but naming, ordering, range expansion, instantiation and
the skip-what-exists rule are *identical* across kinds, and splitting duplicates
all of it to avoid a few nullable columns. `domain.NewDeviceTypeComponent`
validates that the columns set match the kind; the CHECK is the second line of
defence, per CLAUDE.md.

**The unique index is scoped to live rows**, the shape migration `00064`
established for prefixes: a withdrawn template component must not keep its name
reserved for ever.

`position` exists so `Ethernet1/2` sorts after `Ethernet1/1` and before
`Ethernet1/10`, which lexical ordering does not.

## Range expansion

The template stores **one row per component**. The form accepts a range —
`Ethernet[1-48]` — and expands it on submit.

Storage stays honest: 48 rows are 48 rows, each individually editable,
withdrawable and auditable, and the template says plainly what it will produce.
A stored pattern would say "48 ports" until the day somebody needs port 33 to
differ, and then the pattern has to be broken apart anyway.

Expansion is `domain.ExpandRange(spec string) ([]string, error)` — a pure
function with its own table-driven tests, not template logic and not SQL. It
refuses an unbounded or reversed range and caps the count, because
`Ethernet[1-100000]` is a denial of service against your own database.

## Instantiation

**At creation, when the asset names a device type that has templates.** Each
component is an ordinary `interface` row, created in the same transaction as
the asset, each with its own `change_log` entry — they are declared state
and the audit rule has no exception for rows a template suggested.
`power_input` is not instantiated because it is not a template kind (see the
correction at the top of this document).

**A one-time copy, not a live relationship.** Once created, a component belongs
to the asset. This is the decision the original entry already took and it
stands: *later type edits do not rewrite existing instances.* Rewriting them
would put a change into `change_log` attributed to an operator who never made
it — the same misattribution `RetireInterface` refuses, and `RetireEnvironment`
after it.

## Applying to an asset that already exists

**Required, not a follow-up.** Without it the feature helps only assets created
from tomorrow, and the four switches with fourteen ports between them — the
reason the feature exists — stay wrong.

An explicit, operator-initiated action on the asset: *apply this type's
template*. It **adds what is missing by name and never removes or overwrites
anything.** A port somebody already recorded is theirs, whatever the template
says about it.

## Drift as a finding

The counterpart to not rewriting instances. `EstateFindings` already aggregates
families (`PowerFindings`, `CapacityFindings`); this adds one: an asset whose
components differ from its type's template.

Severity `FindingGap` — "not recorded, so not knowable" — because that is what
drift here usually means: the model gained ports the asset never got, not that
anything is broken. One row per kind of finding, per that file's own rule.

## Testing obligations

- `ExpandRange`: table-driven, including refusals (unbounded, reversed, over the
  cap, malformed).
- Instantiation writes one `change_log` row per component, in the asset's
  transaction — mutation-proven, since a set write producing no audit entry is a
  failure this repo has already made three times.
- Apply-to-existing skips by name and never overwrites — the test that matters,
  because the alternative silently destroys operator data.
- Both engines, via `Engines(t)`.
- The unique index is live-scoped: withdraw a template component, redeclare the
  same name, both succeed.

## Open question, deliberately left open

Whether `port_pass_through` (panel strands) should be templatable as a third
kind. A patch panel's front/rear ports are `interface` rows *plus* pass-through
pairs, so it is not one more `kind` — it is a pair-wise structure. Left out
until somebody wants it; the `kind` CHECK is where it would go.
