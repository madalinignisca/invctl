<!-- invctl — infrastructure inventory
     Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

     Licensed under the GNU Affero General Public License, version 3 only —
     no later version applies. See LICENSE for the full text.

     SPDX-License-Identifier: AGPL-3.0-only -->

# WP-G7 · Ownership report — design

## 1. Why

`RetireTeam` (`internal/store/teams.go`) already decided the hard part, and
decided it correctly:

> It does NOT clear the team from what it looked after. A retired team is how
> the estate says "this used to be theirs and nobody has picked it up", which
> is a finding; silently nulling the column would erase the question along
> with the answer.

That is deliberately **not** the WordPress model, where deleting an author
forces reassigning their posts. A post NEEDS an author to render — a display
requirement. An inventory entry does not. "Nobody picked this up after the
team disbanded" is a fact about the estate and one of the more useful things a
CMDB can say at 03:00. Forcing reassignment at retirement converts that fact
into a guess made under time pressure by whoever happened to be disbanding the
team, who is rarely the person who knows.

The sentence immediately after it, though, promises more than the product
delivers: *"the pages render a retired team plainly so the gap is visible."*
Visible **one entity at a time**. Nothing answers "what has no owner?" across
the estate, and none of the five existing reports looks.

This work package is that missing half: keep the finding, make it findable.

## 2. Three findings, two shapes

**Entity-level — one row per affected thing:**

1. **Unowned** — `team_id IS NULL`. Nobody ever said who looks after this.
2. **Owner retired** — the team exists but was disbanded. Somebody said, and
   the answer expired. More actionable than (1): there is a name to start
   from, and often the person who inherited the work.

Reported as two distinct sections, never merged. "Never answered" and "answer
went stale" are different conversations with different people.

**Team-level — one row per team, NOT per owned entity:**

3. **Owner has no contact** — the team is active and owns things, but its
   `contact_ref` is empty. The ownership chain resolves to a team code with no
   mailing list, queue or channel behind it. The owner exists and is still
   unreachable, which is the original problem arriving one step later.

Shape matters here. A team owning 40 assets with no contact is ONE finding
with one fix — edit the team — not 40 rows. Listing it per entity would bury
every other finding in the report. Show the team once with a count of what it
owns and a link to fix it.

Note `RetireTeam` sets `ContactRef = nil` only on the copy it hands to
`indexTeam`; the stored column is untouched. A retired team's contact is
still displayable, and that is deliberate — it is often the fastest route to
whoever absorbed the work.

## 3. Scope: product-wide, on purpose

Every team-owned entity: `asset.team_id`, `service.team_id`,
`project.team_id`, `identity.team_id`, `custom_field.owner_team_id`.

NOT a custom-fields feature. A senior review of WP-A4 found it had built a
second, feature-local attribution mechanism when `internal/help`'s
"yours to edit" / "defined by the engine" pill was already the first. A
custom-field-only orphan view would be the third. One mechanism.

## 4. Bulk assignment

An estate arriving at this report has tens of orphans, not one. One-at-a-time
would make the report a list of chores. Bulk is the point.

**Audit: one `change_log` row per entity, never one for the batch.** Each
entity's ownership is its own declared-state mutation; a single row saying
"assigned 11 things" is not an audit trail, it is a receipt. The batch is a UI
convenience, not an audit unit.

**Stale rows are SKIPPED and reported, not 409'd as a batch.** The submission
contract (`docs/custom-fields-design.md` §6) says a submission may only name
what the operator was shown. They were shown 11 orphans and asked to assign
those 11. If one stopped being an orphan in the meantime — somebody else
assigned it, or its team was restored — assigning it anyway acts on a state
they never saw. Skip it, assign the rest, and say plainly which were skipped
and why. All-or-nothing would punish the operator for someone else's
correctly-made edit.

**`identity` has no `row_version`** while `asset`, `service` and `project`
do (verified against the schema). Bulk assignment there cannot use the
optimistic-concurrency token the other three carry. Options, in preference
order: (a) add `row_version` to `identity` in the same migration and bring it
in line with every other editable entity — this is the honest fix and the
column is cheap; (b) scope the guard to `WHERE team_id IS NULL` so the update
is conditional on the condition that put it in the report. Do NOT silently
assign without any guard.

## 5. Retiring a team warns

`RetireTeam` counts nothing today and says nothing. Before confirming, show
what the team looks after — "12 assets, 3 services, 2 custom fields".

**A warning, never a block.** The reasoning in §1 stands: forcing the choice
at retirement is worse than leaving the finding. This only removes the
silence.

Smallest piece here, and probably the highest value: it stops orphans being
created unknowingly, which is cheaper than finding them afterwards.

## 6. Surfaces

- `GET /reports/ownership` — `read()`, alongside the existing five reports.
- Assignment is a mutation: `write()`, CSRF, `RequireAdmin`.
- Reuse the team picker in `web/templates/partials/forms.html:226`.
- The report must state its own emptiness honestly: "no ownership gaps" is a
  real answer and must not look like a failed query.

## 7. Not the lint engine

This is lint-shaped — a rule that finds estate conditions — and CLAUDE.md
forbids the lint engine before M5. The five existing reports set the
precedent that a targeted query surface is a report, not a rule engine. When
the lint engine arrives, these three conditions are inputs to it, not
duplicates of it.

## 8. Demo fixture

The live demo currently has **11 custom fields, all unowned** — an honest
upgrade artefact, since `StageCustomFields` skips a populated estate and no
migration can guess an owner. Good material: it is exactly what an estate
upgrading into this feature sees.

For the other conditions the seed needs to stage: at least one entity owned by
a since-retired team, and one active team with no `contact_ref` (today the
demo has zero — every team has a contact, so condition 3 has nothing to find).
