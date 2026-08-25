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
2. **Owner cannot act** — the team exists but its lifecycle says it will not
   answer. **This is NOT `lifecycle = 'retired'`.** `team_lifecycle_check`
   permits `planned`, `active`, `deprecated` and `retired`, and the domain
   also defines `maintenance`. An entity owned by a DEPRECATED team is
   arguably the most interesting finding in the report — a team on its way out
   that still owns forty things and nobody has noticed — and a binary
   active/retired test misses it silently. Silent false assurance is worse
   than a false positive.

   Classify every lifecycle value explicitly, and query the classification:
   `active` is an eligible owner; `deprecated` and `planned` are transitional
   (owned, but flag it); `retired` is ineligible. Enumerate them in one place
   so a new lifecycle value cannot be added without someone deciding which
   bucket it falls in.

   **A caveat I claimed too strongly in the first draft**: this was described
   as "more actionable than (1), because there is a name to start from". The
   schema records no REASON for retirement. If team-X merged into team-Y, this
   report says "owner retired: team-X" and offers no path to team-Y — the name
   is a dead end too. Either retirement grows a "succeeded by" reference, or
   the claim comes out. Do not ship the claim without the mechanism.

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
do. The first draft preferred adding one. **That was wrong and the review was
right**: it introduces versioning into an entity type that has never had it,
so older instances would not increment it and the schema drifts. Use the
guard that matches the condition instead — `UPDATE ... WHERE team_id IS NULL`
is itself an atomic eligibility check, and zero rows affected IS the
"no longer unowned" outcome. Simpler, and it needs no migration.

**Report a per-item outcome, not a count.** "10 updated, 1 skipped" tells the
operator nothing: they cannot tell whether the skipped row was claimed by a
colleague, failed validation, or hit a write error. Return one outcome per id
with a reason — `assigned`, `no_longer_unowned`, `write_failed` — and render
them. A silent partial operation is expensive precisely when it matters.

**A batch identifier, written at the same time as the per-entity rows.** The
per-entity rows stay: each entity's ownership is its own declared-state
mutation. But fifty rows written in one second do not tell a reader "one
operator claimed fifty things, twenty-two succeeded" — timestamp clustering
is not evidence, because retries and concurrent operators produce the same
shape. Carry a batch id on each row so the batch is reconstructable.
**This cannot be added retroactively**: rows written without it lose their
operational context permanently, and `change_log` admits no UPDATE.

## 5. Retiring a team offers the fix

`RetireTeam` counts nothing today and says nothing. Before confirming, show
what the team looks after — "12 assets, 3 services, 2 custom fields".

**Neither a bare warning nor a block — offer the fix in the same flow.** One
review argued this should BLOCK until everything is reassigned; §1 argues it
must not force a guess. Both are satisfied by making the right thing easy:
show what the team owns, and offer bulk reassignment right there, with
"retire anyway" remaining available.

That is also the moment when reassignment is CHEAPEST and most accurate. The
person disbanding a team knows why it is going away and usually who absorbed
its work; the person reading the report three weeks later does not. The report
then catches what slipped through, rather than being the primary tool.

Smallest piece here, and probably the highest value: it stops orphans being
created unknowingly, which is cheaper than finding them afterwards.

## 6. Surfaces

- `GET /reports/ownership` — `read()`, alongside the existing five reports.
- Assignment is a mutation: `write()`, CSRF, `RequireAdmin`.
- Reuse the team picker in `web/templates/partials/forms.html:226`.
- The report must state its own emptiness honestly: "no ownership gaps" is a
  real answer and must not look like a failed query.

**The interaction model, which the first draft left out entirely and which is
the difference between a usable report and a list of chores.** Forty orphans
across five projects need DIFFERENT owners. A flat list with one team picker
means the operator either assigns all forty to one team — wrong, and silently
so — or opens each in turn, which is what bulk was meant to avoid. So:

- Group by entity type, and allow narrowing by project or site before
  selecting. The unit of work is "these twelve networking assets go to
  Network Ops", never "everything on this page".
- Select-all applies to the CURRENT filtered view and says so, because
  select-all over an unfiltered list is how the wrong bulk assignment happens.
- Bound the page. A report rendering ten thousand checkboxes is its own
  outage.
- Confirm before writing: "12 assets → Network Ops" with the count and the
  target named.

**Close the loop on the no-contact finding.** "Link to fix it" leaves the
operator editing a team and navigating back to guess whether it worked. Return
them to the report with the finding gone, the same way any other fix-and-
return flow in this codebase behaves.

## 7. Not the lint engine

This is lint-shaped — a rule that finds estate conditions — and CLAUDE.md
forbids the lint engine before M5. Saying "it is a report, not a rule engine"
is a label unless something makes it true, and a review was right to press on
it: by the time lint arrives this code exists, and leaving it is always easier
than folding it in. Two systems answering the same question differently is the
actual failure — an operator asking "what is unowned" and getting two answers.

So commit to the migration path now, in writing:

- **Name the vocabulary here and once.** The lifecycle classification in §2 —
  eligible / transitional / ineligible owner — is the authoritative definition
  of "cannot act", and lint consumes it rather than restating it.
- **The three conditions are findings, not rules.** They carry no severity, no
  suppression, no schedule, no exception list. The moment any of those is
  wanted, it belongs in lint, and this report becomes a VIEW over lint's
  findings or is deleted.
- Write that intent into the code that computes them, so the next person
  meets it before deciding to extend this instead.

## 8. Demo fixture

The live demo currently has **11 custom fields, all unowned** — an honest
upgrade artefact, since `StageCustomFields` skips a populated estate and no
migration can guess an owner. Good material: it is exactly what an estate
upgrading into this feature sees.

For the other conditions the seed needs to stage: at least one entity owned by
a since-retired team, and one active team with no `contact_ref` (today the
demo has zero — every team has a contact, so condition 3 has nothing to find).

## 9. Indexes, and a gap this report walks straight into

**Every existing team index is the wrong way round for this query.**
`00016_team_index_shape.sql` creates `idx_asset_team ON asset(team_id) WHERE
team_id IS NOT NULL`, and `idx_service_team` matches. Migration `00054` added
`idx_custom_field_owner_team ... WHERE owner_team_id IS NOT NULL` in the same
shape. Those partial indexes exist to make "what does this team own" fast —
the opposite of this report's primary question, which is `team_id IS NULL`.
Nothing here can use them.

So the unowned query is a full scan of every owned entity type unless indexes
are added for it. State the index set in the implementation, and check the
plan on BOTH engines rather than assuming: SQLite and PostgreSQL choose
differently, and a partial index that helps one may be ignored by the other.
Bound the result set regardless — an index does not save a page that renders
fifty thousand rows.

## 10. Size, and how to ship it

The roadmap says S. **That is not honest.** The scope is: a read model with
lifecycle classification across five entity types, dual-engine tests for each,
a bulk mutation with per-item outcomes, a batch identifier on an append-only
log, index migrations, a change to team retirement, and an interaction model
with grouping and bounds. Calling that small is an incentive to drop exactly
the safeguards that make it correct.

**M, split into three shippable pieces**, in this order:

1. **The findings, read-only.** The three conditions, the lifecycle
   classification, the indexes, the bounds. Useful alone: the demo has eleven
   unowned custom fields today and no way to see them.
2. **The retirement flow.** Counts, and reassignment offered inline. Smallest
   piece, highest value, and it stops orphans being created unknowingly —
   cheaper than finding them afterwards.
3. **Bulk assignment.** Per-item outcomes, batch id, the interaction model.
   The piece with the real correctness surface, and the one that benefits most
   from the first two being in use before it is written.
