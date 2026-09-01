<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# WP-G4b — saved views

The last third of WP-G4. Tags (G4a) and column configuration (G4c) shipped;
this is the piece that lets somebody keep the filter they built.

`docs/tags-design.md` §0 deferred it with an honest reason: filters need
their own design, not that one storage choice dodges a GDPR obligation. This
is that design.

## 1. What it is, and what it is not

A **Views** control on the asset and service lists, beside Columns. It lists
the views you have saved, opens one on a click, and offers "Save this view…"
which names whatever filters are currently applied.

**It is not a bookmark, and the difference is the whole justification.** A
filtered list is already a URL — `/assets?kind=server&environment=prod` —
which you can bookmark, paste into a runbook, or send to a colleague today.
What a bookmark cannot do is appear inside the product, on the page where
you would use it. That is what this buys.

It also stores the filter's **parts**, not the URL string, so changing a
route or a parameter name does not orphan everybody's saved views.

**Two lists only: assets and services.** They are the ones with query
filtering — seven parameters and six respectively: `kind`, `environment`,
`lifecycle`, `device_type_id`, `q`, `retired`, `tag` on assets;
`environment`, `kind`, `availability`, `project`, `q`, `tag` on services.
Circuits and prefixes have no query filtering at all, so there is nothing to
save; giving them filters is a different work package.

## 2. It gets a table, and that is a reversal worth explaining

`docs/table-configs-design.md` argued G4c out of a table: a column
preference is a display preference about one browser, so `localStorage` holds
it and the database never learns it exists. **That reasoning does not
transfer, and the difference is not size.**

A column preference is incidental — you nudge it and forget it, and losing it
costs fifteen seconds. A saved view is a **named artifact somebody made
deliberately**, and it is wanted on more than one machine. More decisively:
sharing views is a stated future requirement. Building this in `localStorage`
now means building it twice, and either migrating everyone's local views or
telling them their work is gone.

So `saved_view` is the first table in this product whose SUBJECT is a person
rather than its author. That cost is real and is paid here on purpose. The
scrub it requires already exists (`ScrubUser`, WP-G1), which is what makes
now the right time.

```
saved_view(
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  entity      TEXT NOT NULL CHECK (entity IN ('asset','service')),
  name        TEXT NOT NULL,
  params      TEXT NOT NULL,       -- opaque JSON, never queried
  lifecycle   TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','retired')),
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
)
UNIQUE (user_id, entity, name) WHERE lifecycle = 'active'
```

`params` is opaque per `CLAUDE.md`: stored as TEXT, unmarshalled in Go, never
`json_extract`ed and never filtered on. If something in it needs querying, it
is not a parameter and belongs in a column.

## 3. Authorization: a person writes their own views and nobody else's

`saved_view` classifies as **`ScopeSubjectDerived`**, the class
`journal_entry` uses.

**An earlier draft of this section said `ScopeEstateConfig`, and that was
wrong in a way worth recording.** The intent was right — no *project* scope
may reach this table — but the mechanism contradicts it:
`scopedPermit.Covers` consults its `entities` set only for
`ScopeProjectLinked` and `ScopeSubjectDerived`; for `ScopeEstateConfig` it
returns false unconditionally. So a narrow permit minted for the row's owner
could never satisfy `tx.log`, and **the owner would have been refused their
own view**. The implementer hit exactly that and stopped rather than working
around it.

`ScopeSubjectDerived` is safe here for the same reason it is safe for
`journal_entry`: `auth.Authorizer.Permit` builds buckets only for `asset`,
`service` and `circuit`, so an ordinary permit has no `saved_view` bucket and
covers nothing. Only the narrow permit minted **after** the ownership check
can cover a row. The class's own doc comment is widened to say the subject
may be a person rather than only an estate entity.

It therefore takes the `ScopeSubjectDerived` treatment `journal_entry`
already uses (`internal/store/journal.go`'s `authorizeJournalSubject`): the
store method compares the row's `user_id` against `p.Actor().ID` **before any
transaction opens**, and only then mints a narrow permit scoped to the one
row it writes.

On update and delete the `user_id` is read from the **stored row**, never
from the submitted struct. Trusting a submitted owner id would let anybody
name themselves and edit somebody else's view — the same seizure shape
`TestEditingAJournalNoteChecksTheStoredSubjectNotTheSubmittedOne` pins.

**An Administrator gets no exception.** Administrators administer the estate,
not other people's shortcuts, and there is no operational reason to read
somebody's saved views. The `Covers`-everything administrator permit still
passes the permit gate, so the owner check is what enforces this and it must
not be written as "unless administrator".

## 4. Audit: the invariant holds, the contents do not

Writes go through `tx.log` like every other declared mutation. That rule
stopped being only an audit rule at WP-G1 — `tx.log` is the authorization
gate, so a write that skips it is not untraceable, it is unauthorized and
successful. One exception is how an invariant dies, and this one now has five
fixed escalations resting on it.

The cost is noise: `/changes` gains "saved a view" entries.

**The params are redacted.** `domain.RedactedFieldsByEntity` — which is keyed
by Go type name, e.g. `"AppUser"` — gains a `"SavedView"` entry covering
`params`. The log records that a view called "Production servers" was created
and later renamed; it never records what a person repeatedly searches the
estate for. A permanent, append-only record of somebody's search patterns is
a behavioural profile, nothing needs it, and `change_log` is kept forever.

The view's **name** is logged. It is user-authored text and could in
principle carry something personal, but a name is what makes the audit entry
mean anything, and the alternative is an entry that says only "a view
changed".

## 5. Erasure: hard delete, in the scrub's own transaction

`ScrubUser` already runs in `writeSerializable`. It gains:

```sql
DELETE FROM saved_view WHERE user_id = ?
```

**A hard delete, in a codebase whose rule is soft-delete only.** That rule
exists to preserve estate history — a retired asset is how the estate records
that something used to be there. A saved view is not estate history; it is
one person's shortcut, and it exists only for them. Once they are erased it
belongs to nobody and serves nothing, so retaining it is holding personal
data with no purpose, which is the opposite of what the scrub is for.

The `change_log` entries survive, holding the opaque `app_user.id` and the
view name. That is the same position `docs/AUDIT.md` already takes for every
other entry: the trail keeps its integrity and simply stops resolving to a
person.

`docs/AUDIT.md` gains this as the second named exception to soft-delete,
beside the admin-invoked prune of `observed_transition`, with the reasoning
above rather than a bare entry.

## 6. Staleness: say what is missing

A view saved as `environment=prod` still calls itself "Production servers"
after somebody retires or renames that environment, and quietly matches
nothing.

**Validated per render against live vocabulary.** A view whose parameters
reference a term that no longer exists is shown as stale, naming the term:
*environment "prod" no longer exists*. The view still opens and still runs —
it explains its empty result rather than leaving somebody to conclude the
estate is empty.

Nothing is rewritten in storage. Repairing views on rename would mean a
vocabulary edit writing to other people's rows, which needs an authorization
story of its own, and a retire has no correct rewrite because there is
nothing to point at.

This is the same objection this codebase raises everywhere else: a wrong
answer that looks right is worse than an error.

## 7. Testing

Store, both engines:

- **A person cannot write another person's view** — create as A, attempt
  update and delete as B, assert `ErrForbidden` and that the row is
  unchanged. The ownership boundary is the security property here.
- **An Administrator cannot either**, which is deliberate and would otherwise
  look like a bug to whoever reads `Covers` and sees administrators covering
  everything.
- **Update reads the owner from the stored row**, not the submitted struct —
  submit a forged `user_id` naming yourself and assert refusal.
- **The scrub deletes them**, in the same transaction, and the `change_log`
  entries survive.
- **The audit row redacts params** and keeps the name.
- **The active-name uniqueness is per user and per entity** — two people may
  each have "Production servers"; one person may not, on the same list.

E2E, one flow: save a view on `/assets`, reload, open it from the Views menu,
confirm the filters are applied and the URL reflects them.

## 8. Explicitly not in scope

- **Sharing.** The table's shape does not preclude it — a visibility column
  and an owner is the shape it would take — but a shared namespace needs
  rules about naming collisions and about what happens to a shared view when
  its owner is scrubbed, and those are their own design.
- Circuits and prefixes, which have no filtering to save.
- Default views, per-role views, or a view that loads automatically.
- Ordering or foldering of saved views. A person with enough views to need
  folders has a different problem.

## 9. Size

**M.** A migration, a store file with its ownership check, handlers, a
partial, the scrub change, the audit classification, and the tests above. The
schema is small; the authorization and erasure behaviour is where the work is.
