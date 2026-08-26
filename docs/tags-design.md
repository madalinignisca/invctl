<!-- invctl — infrastructure inventory
     Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

     Licensed under the GNU Affero General Public License, version 3 only —
     no later version applies. See LICENSE for the full text.

     SPDX-License-Identifier: AGPL-3.0-only -->

# WP-G4a · Tags — design

## 0. Why this is only a third of WP-G4

The roadmap bundles tags, saved filters and table configs as one M. They are
not one feature, and one of them carries an obligation the others do not.

**There is no per-user state anywhere in invctl today.** Every reference to
`app_user` is attribution — `created_by`, `retired_by` — always through a
`LEFT JOIN` so a GDPR scrub leaves the row readable. Nothing in this product
has a PERSON as its subject.

- **Tags** are estate-level declared state. No new surface. This document.
- **Saved filters** are deferred, and the first draft gave a bad reason for
  it. It claimed making them SHARED avoids a personal-data obligation. **That
  is wrong**: a saved filter is associated with whoever created it through
  attribution, exactly as every other row here is, so sharing redistributes
  access rather than erasing the obligation. Shared is also not obviously
  better — network ops and security want different filters, and a shared
  namespace cannot hold two "production review" filters meaning different
  things. The honest reason to defer is that filters need their own design,
  not that one storage choice dodges GDPR.
- **Table configs** are genuinely per-user, and would be the first table in
  this product whose SUBJECT is a person rather than its author. **Deferred
  until WP-G1**, because per-user state is close to meaningless while
  authorization is a comma-separated list of usernames.

  An earlier draft of this line claimed the scrub path answering an erasure
  request for `created_by` would extend to it cheaply. **There is no such
  path.** `scrub` appears in nine comments across this codebase, every join is
  written defensively so a scrubbed account degrades to a raw id, and
  `docs/AUDIT.md` rests its indefinite-retention argument on the operation —
  but no code performs it, and there is no user-management route at all. The
  only write to `app_user` outside a test is `last_login_at`. Anything storing
  data whose SUBJECT is a person needs that operation to exist first.

## 1. Is this a third estate-defined metadata mechanism?

It has to be asked, because a senior review of WP-A4 found that feature had
built a second attribution mechanism when `internal/help`'s "yours to edit"
pill was already the first, and warned that the next one would be the third.

Tags are a genuinely different SHAPE, not a second spelling of custom fields:

| | custom field | tag |
|---|---|---|
| values per entity | one per field | many |
| scope | one entity type per field | crosses entity types |
| typed | five kinds, validated | none, a label is a label |
| answers | "what is this box's cost centre" | "which things are in the DR scope" |

A `select` custom field is the closest relative, and it is still
single-valued and bound to one entity type. Tags exist to cut ACROSS the
estate; custom fields exist to describe one kind of thing precisely.

That said, the overlap is real enough that the UI must not present them as
rivals. Both are estate-defined, and both belong under the same "yours, not
the vendor's" framing that the help pill already establishes.

## 2. Explicit creation, not free-form

A tag is created in a registry before it can be applied, the same way a custom
field is defined before it holds a value.

The alternative — type a word into a box and a tag springs into existence — is
how estates end up with `dr`, `DR`, `disaster-recovery` and `disater-recovery`
meaning one thing. Tag sprawl is the standard failure of this feature, and it
is unrecoverable without a merge tool nobody wants to build.

Cost: applying a tag needs an extra step the first time. Accepted.

## 3. Shape

A `tag` table — id, code, label, description, colour?, created_by, created_at,
retired_at, row_version — plus the applications.

**Applications are polymorphic**, following `journal_entry` (`00039`),
`asset_health` (`00005`) and `custom_field_value` (`00051`): one
`entity_tag(tag_id, entity_type, entity_id)` rather than `asset_tag`,
`service_tag`, `project_tag`... The trade-off, stated rather than assumed:

- **Against**: `entity_id` carries no foreign key, so nothing at the database
  level stops a row pointing at nothing. `certificate_asset` and
  `certificate_service` (`00015`) take the per-type route precisely to keep
  that FK.
- **For**: tags cut ACROSS entity types by definition, and the query tags
  exist to answer — "everything tagged `dr`, whatever it is" — is a UNION over
  N tables in the per-type shape and a single indexed scan in this one.
  `journal_entry` and `asset_health` are polymorphic for precisely that
  reason, and `custom_field_value` is the most recent precedent.
- **The first draft's defence of this was wrong and is withdrawn.** It argued
  soft-delete-only removes the dangling-reference risk. Soft delete is a
  POLICY, not a schema constraint: a hand-written fix, a bad import or a
  migration defect can still leave `entity_id` pointing at nothing, and the
  database will accept it silently. Choosing polymorphic means accepting that
  and compensating for it, not pretending it cannot happen.

**So it is compensated, explicitly:**
- `entity_type` carries a `CHECK` limiting it to the types that are actually
  taggable, so a typo cannot invent a new entity kind.
- A store-level integrity check finds rows whose `entity_id` matches nothing,
  tested on both engines. It is cheap, and it is the thing the missing foreign
  key would otherwise have done.
- If a future entity type stops being soft-delete-only, this decision has to
  be revisited. Say so here so the next person meets it.

**Retire, never delete.** A tag is an entity.

**Indexes, stated rather than discovered later.** WP-G7 shipped a report whose
primary query no existing index could serve, because every team index pointed
the other way. Tags have the same exposure. The two questions are "what is
tagged `dr`" (`entity_tag(tag_id)`) and "what tags does this entity carry"
(`entity_tag(entity_type, entity_id)`), so both get an index, and the query
plan is CHECKED on both engines rather than assumed — SQLite and PostgreSQL
choose differently, and a partial index that helps one may be ignored by the
other.

## 4. Audit

Tags are declared state: somebody asserts them. So:

- Creating, editing, retiring and restoring a tag writes a `change_log` row.
- **Applying and removing tags is a SET REPLACEMENT on the entity**, and the
  set must fold into that entity's audited value. This codebase has been
  bitten four separate times by a set replacement producing no diff on its
  parent and therefore no audit entry at all — `asset_environment`,
  `dependency_data_class`, and twice in WP-A4. Fold the tag set the way
  `assetAudit` folds custom values, and TEST that a tag change alone produces
  a diff.
- The tag set folds as SORTED codes, so the audit reads
  `tags: "dr,prod" -> "dr,prod,pci"` and a reader can see what moved.
  **`TestReorderingTagsIsNotAChange` is named here and is part of the
  definition of done**: applying the same set in a different order must write
  no audit entry. The equivalent test exists for custom values because that
  invariant was violated twice; a design that omits the test for a bug this
  codebase has hit four times is a design assuming it will not happen again.

**Renaming a tag's code rewrites the fold for every entity carrying it**, so
the next unrelated save on each of them logs a spurious `tags` diff.
`docs/custom-fields-design.md` §7 documents exactly this hazard for field
codes, and the first draft of this document did not carry it across. Two ways
out, and the choice is made here rather than left to the implementer: **fold
the tag's stable id, not its code**, and resolve codes for display. The audit
row then reads less well to a human, which is the cost — but a rename is a
correction somebody makes deliberately, and it must not manufacture a change
on a hundred unrelated entities.

## 4a. How a tag actually gets applied

**The first draft never said.** It specified creation, audit and filtering, and
omitted the only workflow that matters: marking twelve assets for a DR audit.
WP-G7's first draft had the same shape — data model right, interaction model
absent — and both reviewers led with it here too.

- **From the entity's own page**: a tag picker on the asset, service or project
  detail page, showing the tags it already carries and offering the live ones
  it does not. The same shape as the custom-field editor beside it.
- **In bulk, from the list views**: select rows in the filtered asset or
  service list and apply a tag to the selection. This is the reason tags exist,
  and doing it one entity at a time is the failure mode that makes people stop.
  Reuse WP-G7 piece 3's pattern rather than inventing a second one: select-all
  applies to the CURRENT FILTERED VIEW and says so, the confirmation names the
  count and the tag, and each entity gets its own `change_log` row sharing one
  batch id.
- **Creating a tag mid-task must not mean leaving the page.** A tag is created
  once per audit, unlike a custom field defined once for hundreds of entities,
  so the friction lands far more often. The registry stays the place tags are
  MANAGED, but create-and-apply has to be reachable from where the operator
  already is, or the sprawl control simply becomes a reason not to tag.
- **Who may create one**: tag creation and application are both `write()`.
  A read-only user sees tags and filters by them and cannot apply them —
  consistent with every other mutation here, and worth stating because an
  incident is exactly when somebody read-only wants to mark things.

**Sprawl gets an escape hatch, in writing.** Explicit creation reduces
`dr`/`DR`/`disaster-recovery`, it does not prevent it, and the honest answer to
"what happens when it occurs anyway" is a `MergeTag(from, to)` that repoints
applications and retires the loser. It is not in this piece, and it is
committed to here so the answer is not "nobody wants to build it".

## 4b. Tags are not inherited

`asset_closure` exists, so "tag the datacentre, everything inside inherits it"
is askable. **The answer is no**, and it is not a shortcut:

An inherited tag is not a fact anybody asserted about the child. This whole
codebase separates what somebody DECLARED from what was derived, and a tag
that appears on an asset because of its grandparent is neither — it would show
in the entity's audited value having never been written, or not show there and
be invisible to the audit entirely. Both are worse than not having it.

What people actually want from that question is a QUERY: "everything tagged
`dr`, including things contained by something tagged `dr`". That is a filter
option walking `asset_closure` at read time, it stores nothing, it asserts
nothing, and it can be added without touching the audit model. Different
feature, and the honest place for it.

## 5. Filtering is the point

Tags that cannot be filtered on are decoration. The estate lists already build
their filters from query params (`assetFilterFrom`, `internal/web/handlers/
assets.go:235`), so a tag filter joins that struct rather than inventing a
second filtering path.

Multi-tag filtering is AND, not OR: "show me things tagged `dr` AND `pci`" is
the question people actually ask, and OR is reachable by asking twice.

**The AND query is the one place an implementer will invent something wrong**,
so the shape is fixed here. Count the DISTINCT matching tags per entity and
require it to equal how many were asked for:

```sql
SELECT a.id FROM asset a
JOIN entity_tag et ON et.entity_id = a.id AND et.entity_type = 'asset'
WHERE et.tag_id IN (?, ?)
GROUP BY a.id
HAVING COUNT(DISTINCT et.tag_id) = ?
```

Three things about it, all easy to get wrong:
- The `IN` list and the trailing count are BOTH dynamic. Build with `sqlx.In`
  then `Rebind` — never string-build the placeholders, and never `$1`.
- **Guard the empty case explicitly.** With no tags asked for, the `HAVING`
  becomes `= 0` and the filter silently returns everything. An empty tag
  filter must mean "do not filter", decided before the query is built.
- It runs unmodified on both engines: no `INTERSECT`, no array containment, no
  `generate_series`.

## 6. What is deliberately NOT here

- **No colours-as-meaning.** A colour may be stored for display, but nothing
  may depend on it. Colour is not data.
- **No tag hierarchies.** Nested tags are a taxonomy, and this product already
  has vocabularies for that.
- **No auto-tagging rules.** That is the lint engine, gated behind M5, and
  a rule that applies tags is a rule that ACTS on the estate.
- **No per-user tag visibility.** See §0.
