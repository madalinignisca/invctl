# Identity surface — design

**WP-J8.** Status: design, not built.
Decisions taken with Gabriel 2026-09-15.

## What the identity surface is

The pages an operator uses to declare a credential, correct it, withdraw it, and
**record that it was rotated**. `identity` is the row that answers "what does this
service authenticate as", and `secret_ref` is where the material lives — a path,
never the material itself.

The table has existed since migration `00003` and has been hardened twice. What
it has never had is a way in: `CreateIdentity` and `ListIdentities` sit in
`internal/store/deps.go:887,903` and **no route reaches either**. Three rows
exist in the entire system and the seeder wrote all three.

## Why it earns its place

`rotation_days` and `last_rotated` are the point of the table, and **nothing has
ever written `last_rotated`.** A rotation policy that cannot record a rotation is
not an incomplete feature, it is an inert one: every identity in every deployment
is permanently in the state "policy says 90 days, no rotation has ever been
recorded", and nothing anywhere says so.

`domain.Identity.RotationOverdue` (`internal/domain/endpoint.go:258`) is the
proof. It is a business rule with **zero callers**, and it answers `false` for
every row in the demo estate — correctly, and uselessly, because the question it
was asked cannot be answered from data nobody can enter.

The second reason is the one the cable-bundle doc makes: a page nothing reads is
a label. The identity detail page has a real consumer already waiting —
`dependency.identity_id` and `rt_windows.logon_identity_id` both point here, so
"what would notice if this credential died" is a join, not a new model.

Closing this takes `writeSurfaceUnbuilt` (`internal/store/write_surface_test.go:153`)
to **zero**. Deleting that entry is part of the work: the map is two-directional
and an entity that grows both verbs while still listed there fails the build.

## Schema — migration `00069`

Both engines, byte-identical apart from Postgres needing no `CONSTRAINT`
gymnastics.

```sql
-- The optimistic-concurrency token. identity acquires one for the same reason
-- link acquired one in 00066: it is growing a correction path, and two
-- operators fixing the same credential's realm must not silently overwrite
-- each other.
ALTER TABLE identity ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;

-- Date shape only, exactly as 00011 did for eol_date and 00015 for a
-- certificate's validity window: length and separators are the most both
-- engines agree on, and Go rejects 2027-02-31 where this cannot.
ALTER TABLE identity ADD CONSTRAINT identity_last_rotated_check
  CHECK (last_rotated IS NULL OR (length(last_rotated) = 10
         AND substr(last_rotated, 5, 1) = '-' AND substr(last_rotated, 8, 1) = '-'));
```

**No `created_at`, no `updated_at`, and that is the decision rather than an
omission.** `00066` is the precedent and it is exact: `link` also carries no
timestamps, also grew a correction path, and got `row_version` **only**. Three
reasons it is right here too. A backfill would have to invent a value for every
existing row, and a fabricated creation date in a CMDB is worse than no date.
`change_log` already answers both questions precisely and permanently — the
create entry is when it was declared, the newest entry is when it last changed,
and both carry who. And this table's meaningful timestamp is `last_rotated`,
which is a *fact about the credential*; an `updated_at` beside it would be a
second, weaker date that invites the wrong reading at 03:00.

**The `CHECK` validates existing rows, and that is safe here by construction**:
no code path has ever written `last_rotated`, so it is NULL in every deployment.
A hand-edited database with a malformed value fails the migration loudly, which
is the correct outcome.

`00011`'s header records that `ALTER TABLE … ADD CONSTRAINT … CHECK` was
*measured* to work with no table rebuild against the pinned driver
(`modernc.org/sqlite` v1.54.0, SQLite 3.53.3). It is non-standard SQLite, so
re-measure against whatever the driver is pinned to at build time rather than
trusting this line.

**If the list page ships a team filter, the index ships in the same migration**:

```sql
CREATE INDEX idx_identity_team ON identity(team_id, lifecycle) WHERE team_id IS NOT NULL;
```

shaped like `idx_asset_team`. `00016` dropped exactly this index and named this
moment: *"When the team page grows an identities section it can come back,
shaped for whatever that query turns out to be rather than guessed at now."*

### Classification

`row_version` joins the `"identity"` list in `internal/domain/classification.go:307`
as **declared**, as it is for `asset`, `service` and every other entity that
carries one. `TestEveryColumnIsClassified` reads the live schema and fails
otherwise. The classification costs nothing at read time: `auditFields`
(`internal/store/diff.go:80-86`) excludes `row_version` and `updated_at` from
every diff, because *"an audit entry reading `row_version: 4 -> 5` tells a reader
nothing they can use"*.

`identity` needs **no change** in `domain.entityScope` or in `docs/AUDIT.md`'s
write-scope table: it is already `ScopeEstateConfig` (`internal/domain/role.go:485`),
which is right and stays right — a credential reference applies to every project
and is owned by none of them.

## The domain type

### The constructor takes a spec

`NewIdentity(id, kind, name)` is positional and cannot accept `realm`,
`secret_ref`, `rotation_days` or `team_id`, so the seeder builds a valid identity
by assigning four fields *after* construction (`internal/seed/seed.go:898-908`)
— which is to say the constructor cannot build a valid value, and the validation
it performs is not the validation the row gets.

This repo has hit this twice and recorded it both times.
`domain.CableBundleSpec`'s doc comment: *"a positional signature had to be
replaced mid-branch when it could not accept a required field, so a constructor
that cannot pass a required value cannot build a valid one."*
`DeviceTypeComponentSpec` says the same.

```go
type IdentitySpec struct {
    Kind, Name   string
    Realm        *string
    SecretRef    *string
    RotationDays *int
    TeamID       *string
}

func NewIdentity(id string, spec IdentitySpec) (*Identity, error)
func (i *Identity) Validate() error
```

`Validate` separate from the constructor because the update path has to run the
same rules — `Environment.Validate` (`internal/domain/asset.go:85`) is the shape,
and its doc comment names the defect this prevents: *"the checks lived inside
NewEnvironment, so UpdateEnvironment wrote whatever it was handed and the table
CHECK was the only thing standing between a form and a blank name."*

**`IdentitySpec` deliberately has no `LastRotated`.** See "one writer" below.

**`NewIdentity` takes no `now`**, unlike almost every other constructor here,
because the table has no timestamp to stamp. Worth the one-line comment, or the
next person adds the parameter back out of habit.

### Rotation is three-valued, not two

`RotationOverdue` returns `false` when `rotation_days` is set and `last_rotated`
is NULL — it treats *"policy says 90 days, nobody has ever recorded a rotation"*
identically to *"no policy at all"*. **All three identities in the demo estate
are in the first state**, and the surface would report all three as fine.

**These are opposite facts and the surface must say so.**

- `rotation_days IS NULL` — **unmanaged**. Nobody has asked for this credential
  to be rotated. There is nothing to be late for. A `cert_subject` or a `human`
  row is often legitimately here.
- `rotation_days` set, `last_rotated IS NULL` — **the estate has a rule for this
  credential and no evidence it has ever been followed.** Either it has never
  been rotated since the day it was created, or it has and nobody wrote it down.
  invctl cannot tell which, and both are worth somebody's attention.

Collapsing them is how a credential that has never been rotated in four years
renders identically to one nobody ever intended to rotate.

So the rule becomes a state, and the boolean goes:

```go
type RotationState string

const (
    RotationUnmanaged     RotationState = "unmanaged"      // no policy
    RotationNeverRecorded RotationState = "never_recorded" // policy, no record
    RotationWithinWindow  RotationState = "within_window"
    RotationOverdue       RotationState = "overdue"
    RotationUnreadable    RotationState = "unreadable"     // stored value will not parse
)

func (i *Identity) RotationStatus(now time.Time) RotationState
// RotationDueOn returns nil unless the status is within_window or overdue.
// NOT merely "unless both fields are set": both ARE set when last_rotated is
// 2026-02-31, a value the CHECK accepts and ParseDate refuses, and a due date
// computed from a zero time is the same class of lie RotationUnreadable exists
// to prevent.
func (i *Identity) RotationDueOn() *string
```

`RotationOverdue` is **deleted, not kept beside this**. A two-valued answer to a
three-valued question is what produced the defect; leaving it in place leaves the
next caller a way to reintroduce it.

`RotationUnreadable` exists because the alternative is the failure this repo
keeps finding: `RotationOverdue` returns `false` on a parse error, so an
unparseable stored value reads as *healthy*. A state that cannot be read must
never render as a state that is fine.

### `last_rotated` is a DATE

`YYYY-MM-DD`, `domain.DateFormat`, parsed with `ParseDate`. `domain/time.go:43-49`
already states the rule: *"A date rather than a timestamp wherever the fact is
about a DAY… storing 00:00:00Z alongside it would invite a reader to believe a
precision that is not there."* `rotation_days` is a count of days; "we rotated it
on the 3rd" is the fact anybody actually has.

Nothing has ever written the column, so this is a free choice today and an
expensive one after the first real rotation.

## Rules

### One writer for `last_rotated`, and it is the rotation action

**`last_rotated` is written by `RecordIdentityRotation` and by nothing else** —
not by create, not by correct. The roadmap is explicit that recording a rotation
*"is the feature, not a side effect of an edit form, because it is the thing
somebody actually does"*, and a create form that also stamps a date buries the
rotation inside a create snapshot where no reader will find it.

Declaring a credential that already exists and recording when it was last rotated
is **two acts and two audit entries**, both of which are true. That is the cost
and it is the right one.

This is testable rather than hoped for, in the shape of
`TestTheOnlyFactDeletingStatementIsThePrune` and
`internal/store/permit_source_test.go`: one test parses `internal/store` and
fails on any statement writing `last_rotated` outside the named function.

### Recording a rotation is an audited `update`, and the log *is* the history

`action` stays `'update'`. The `change_log` CHECK allows exactly
`create|update|delete|retire`, and `docs/AUDIT.md` rule 10 already records what
adding a value costs: *"altering a CHECK in SQLite means rebuilding the table"*.
`VerifyDependency` (`internal/store/deps.go:785`) is the precedent in every
respect — a distinct store method and a distinct route that stamp one column and
log through `logUpdate`.

The entry is a one-field diff:

```json
{"last_rotated": {"old": null, "new": "2026-09-08"}}
```

It carries everything the feature needs and nothing it does not: **when the
rotation happened** (`new`), **when it was recorded** (`change_log.at`), and
**who recorded it** (`change_log.actor`, opaque `app_user.id`, server-derived
from the credential, rendered beside `actor_kind` like every other actor in this
codebase).

The consequence is worth stating because it is the answer to "the table only
keeps the latest date": **`change_log` holds the whole rotation history**, for
free, permanently, append-only. The detail page renders the entity's change
history and a rotation is visibly a `last_rotated` change in it. Do **not** build
a rotation-specific history query — that means a predicate inside `diff`, and
querying inside JSON is banned (rule 15: fold in Go).

**A rotation recorded with the date already stored writes nothing at all** — no
UPDATE, no `change_log` row, no `row_version` bump — and returns nil, the way
`RetireEnvironment` returns nil for an already-retired row rather than *"claim a
withdrawal that did not happen"*. Without this, `diffJSON` returns `ok=false`,
`logUpdate` skips the entry, and the UPDATE still moves `row_version`: a
declared-state write with no audit row, which since WP-G1 is an authorization
bypass and not merely an untraceable change.

**No attestation check.** `CheckAttestationWrite` is deliberately **not** applied,
unlike `VerifyDependency`. Recording a rotation is a statement of fact about a
credential, not a person putting their name to an edge; no column stores who
rotated it, only `change_log.actor` does, and that is unforgeable by construction
(rule 5). Applying it would also break the seeder, which writes as `SystemActor`.
Agent credentials never reach these routes anyway — rule 6 forbids an
agent-reachable handler from returning an identity row at all.

### A rotation may be backdated. It may not be post-dated.

Somebody rotated the credential last Tuesday and is catching up on Thursday.
Refusing that forces them to record a date they know is wrong, which is worse
than the thing the refusal was protecting.

The asymmetry is the whole argument, and it is why this is not a slippery slope:

- **A past date can only ever make a finding worse.** It can move a credential
  from "within window" to "overdue"; it can never hide anything.
- **A future date hides an overdue finding for a rotation that has not
  happened.** That is the one direction that turns this feature into a way of
  silencing itself.

So: **any date not after today, `422` with the form partial re-rendered
otherwise.** No monotonicity rule — a date earlier than the stored one is
allowed, because the stored one may simply have been wrong, and `change_log`
records both values and the actor. The audit trail is the control here, not a
constraint. (Compare `inflation_rate`, which is *"corrected in place rather than
superseded… a revised index for 2024 was always one figure somebody had wrong."*)

**Recording a rotation against a retired identity is refused**, with a field
message naming the identity — the shape `SetBundleMembers` uses for a retired
bundle. Rotating a withdrawn credential is not a thing that happened.

### Withdrawal refuses nothing and rewrites nothing

Soft delete only: `lifecycle = 'retired'`, never a `DELETE`.

Two tables point at `identity` — `dependency.identity_id` and
`rt_windows.logon_identity_id`, both `ON DELETE NO ACTION`. Neither is touched.

**Refusing while a live dependency still names the credential would break the
incident path this table was hardened for.** Migration `00003` states the case
plainly: *"the natural response to a compromised credential is to retire it and
create its replacement under the same name"*, which is why the uniqueness index
is scoped to `lifecycle = 'active'`. Blocking retirement until every dependency
has been re-pointed would mean, at exactly the wrong moment, that the compromised
credential cannot be withdrawn. And rewriting those rows automatically would
write `change_log` entries attributing a dependency change to whoever clicked
withdraw — the misattribution `RetireInterface` and `RetireEnvironment` both
refuse to commit.

`RetireEnvironment`'s ruling carries over unchanged: **what is stored keeps
displaying.** A dependency still naming a retired identity shows it, marked
retired. What changes is that it stops being offered as a *new* choice.

**The withdrawal is informed, not blind**: the detail page and the retire
confirmation show what still points at the identity, the way
`TeamOwnershipCounts` feeds the team-retirement screen. `IdentityUsage(ctx, id)`
returns the live dependencies and Windows services that name it.

There is **no restore path** and no trap in that, which is the difference from
the cable-bundle mistake: the live-scoped unique index means a retired
identity's `(realm, name)` is immediately available to a replacement, so nobody
is ever stranded.

### `secret_ref` — nothing to do, and here is why nobody should do it

**The audit trail is already covered. Do not "add" redaction and do not remove
it.** `domain.RedactedFields` (`internal/domain/errors.go:273`) holds
`secret_ref` globally, and both `snapshotJSON` and `diffJSON` honour it via
`domain.IsRedacted`, so `change_log` records *that* it changed and never what to.
`TestSnapshotRedactsSecretRef` and `TestSecretRefNeverReachesTheAuditTrail`
already pin it. The reason is in the map's own comment and in rule 12: *"a
complete, permanent, widely-readable map of where every credential lives is a
reconnaissance gift"*.

**One thing to watch:** redaction is keyed on the `db` tag and the owning struct
name. If the implementer introduces an audited wrapper shape (an `identityAudit`,
the way `assetAudit` folds in a set), it must **embed `domain.Identity` by
value** — `auditFields` panics on an anonymous pointer embed precisely because
that shape once made an entire audit entry silently empty.

**The read path is a separate gate and it is not free.** Display redaction lives
in the handler's view model — `depRowData.SecretRef`
(`internal/web/handlers/forms.go:846-854`), Administrator-only — and its comment
says why it is not in the template: *"a template-side `{{if .IsAdmin}}` around
`.Dep.IdentitySecretRef` is one `{{end}}` away from leaking it, and it does
nothing at all for a CSV export, which never passes through a template."* The
identity pages compute the same thing in the same place.

**The list page never renders the path at all, for anybody.** It shows only
whether one is recorded. One screen listing every credential path in the estate
is the reconnaissance gift described above, in its most convenient possible form,
and it is one CSV export away from leaving the building. The path itself renders
on the **detail** page, to an Administrator, one credential at a time.

CLAUDE.md's rule stands and is restated here because this is the table it is
about: **`secret_ref` holds a path, never a secret. If a code path would put an
actual secret in the database, stop and raise it** — that is a conversation, not
a workaround.

### The write surface is Administrator-only

```
GET  /identities                  read
GET  /identities/{id}             read
POST /identities                  writeAdminOnly
POST /identities/{id}             writeAdminOnly
POST /identities/{id}/retire      writeAdminOnly
POST /identities/{id}/rotation    writeAdminOnly
```

`identity` is `ScopeEstateConfig`, so `tx.log` refuses a project owner's write
regardless of the route gate — `team` relies on exactly that and uses plain
`write`. **This diverges deliberately, and the reason is `secret_ref`:** a
correction form has to render the stored value to be a correction form, and a
form that silently omits a field blanks the column on save. Gating at the door
keeps the only surface that renders a secret path behind the same gate that
already protects it on the dependency page, and makes the refusal honest before
a form is filled in rather than after it is submitted. `/users` and
`/users/{id}/projects` take `writeAdminOnly` for the same class of reason.

CLAUDE.md sanctions this explicitly: *"behind CSRF and `RequireWrite` (or
`RequireAdministrator`, for a surface that genuinely needs one)"*.

The **GETs stay readable by any authenticated user** — name, realm, kind, team
and rotation status are what somebody needs mid-incident, and none of it is
sensitive. `internal/web/rbac_boundary_test.go` drives every generated write
route, so the four POSTs join its population automatically and must be refused
for a project owner.

The edit-form partial is rendered only for an Administrator. `hx-confirm` is
on **withdraw**, which is why that form keeps `hx-post` — htmx never shows the
confirmation dialog for a plain form submission. The declare form and the
correct form deliberately do **not** carry `hx-post`: each can end in an
ordinary 409 (a duplicate `(realm, name)` on declare; either that or somebody
else recording a rotation while the correct form sat open, a cost this spec
prices explicitly below), and htmx 2 does not swap a 409 by default, so
`hx-post` there would leave the button doing nothing visible. The rotation
form keeps `hx-post`: its only conflict status is 422, which does swap.

### Concurrency

The correct form carries the `row_version` token and returns **409** on a
stale write, like every other edit form — `TestEveryEditFormCarriesItsVersion`
derives its population from handlers that reach `submittedVersion`, and
`00066`'s header warns that a correction path built without a token is not
flagged by that census, it is simply *absent* from it.

**AMENDED 2026-09-15 (auth review, Task 5 fix round 1).** The original text
here said the withdraw form carries the token too. It does not, and that is
right rather than an oversight: `RetireIdentity` takes no client-submitted
version at all — it re-reads the row itself and guards its own write against
what it just read, the same shape `RetireBundle` and `RetireTeam` already
use. A token on that form would be inert markup nothing reads, and would
misrepresent to a reader that the withdraw path is optimistically locked
against a submitted version when it is not. The narrow internal race this
still leaves — two withdrawals of the same credential landing between
`RetireIdentity`'s own read and its own write — surfaces as the same 409 a
stale form would, but nothing the operator submitted made it stale.

**The rotation action carries no token and bumps the version**, following
`VerifyDependency` exactly. Two operators recording a rotation of the same
credential is not a conflict worth a 409. The cost is stated rather than hidden:
an operator with a correction form already open gets a spurious 409 after
somebody else records a rotation, even though the form shows no field that moved.
That is accepted, because the alternative — a version token that does not move
when the row changes — is worse, and because `requireVersion`'s contract is about
the row, not about a subset of its fields.

**The two existing identity writers must bump the new column.**
`BulkAssignOwnership` and `ReassignTeamOwnership`
(`internal/store/bulk_ownership.go:298-304`, `internal/store/team_reassignment.go`)
keep their `WHERE team_id …` guards — that guard is still the whole eligibility
check and this does not revisit it — but must now also do
`row_version = row_version + 1`. Otherwise a bulk assignment changes `team_id`
under an open edit form whose token still validates, and the form's save silently
reverts the assignment. `docs/ownership-report-design.md` §4 is amended in the
same commit; see "What this overturns" below.

## The pages

### List — `GET /identities`

Columns: **name**, **realm**, **kind**, **team**, **rotation**, **secret ref
recorded?**, **lifecycle**. Default order `realm, name` (what `ListIdentities`
already does), live rows only.

**Rotation status is a column, and it is the reason the page exists.** One pill
per row, rendering the five states directly:

| State | Pill |
|---|---|
| `unmanaged` | `no policy` (muted) |
| `never_recorded` | `never recorded` |
| `within_window` | `due in 34 d` |
| `overdue` | `overdue by 12 d` |
| `unreadable` | `date unreadable` |

Filters: name substring, `kind`, `lifecycle` (so a retired credential can be
found), `team`, and **rotation state** — the last one is what the findings link
into.

`ListIdentities` gains an `IdentityFilter` parameter.

**CORRECTED 2026-09-15, and the correction is load-bearing.** An earlier draft of
this section said *"nothing outside tests calls it today"*, inherited from
`docs/ROADMAP.md`'s claim that *"no route reaches either of them"*. That is true
of `CreateIdentity` and **false of `ListIdentities`**, which has two production
callers: `internal/web/handlers/deps.go:238` and
`internal/web/handlers/services.go:340`. Both feed the dependency identity
`<select>` (`web/templates/partials/rows.html:69`,
`web/templates/partials/forms.html:646`), and today it returns **every**
identity, retired ones included.

**So a live-only default at those call sites silently clears `identity_id`.**
The correction row renders `<option value="">—</option>` first and marks
`selected` on the matching id; drop a retired identity from the slice and no
option matches, the browser falls back to the empty one, and saving a correction
about the *auth method* posts `identity_id=""` and wipes the column. A data
change on a form about something else, with no error and no way to notice.

Both existing call sites therefore pass `IncludeRetired: true`, with the reason
in a comment beside each and a regression test. This is the same rule
`RetireEnvironment` already states and this document already repeats: **what is
stored keeps displaying.** What changes is only what is offered as a *new*
choice — and narrowing the dependency *create* form alone needs two slices and
the "marked retired, not newly selectable" treatment, which is out of scope
here.

### Detail — `GET /identities/{id}`

Header (name, realm, kind, lifecycle, team), the rotation panel, the
**used-by** panel, and the change history.

The rotation panel states all three facts plainly and never collapses them:
the policy (`every 90 days`, or `no rotation policy`), the last recorded
rotation (`2026-06-01`, or **`never recorded`** — never a blank), and the derived
state. `secret_ref` renders here, to an Administrator, as a path.

The used-by panel lists live dependencies and Windows services naming this
identity. It is what makes the page worth opening and what makes withdrawal an
informed act.

The history panel is `TimelineForEntityAndNeighbours`, which every entity detail
page uses (`docs/AUDIT.md` rule 15) — not `ListChangesForEntity`, whose own doc
comment says *"No page calls this any more, and that is deliberate."* A rotation
reads there as a `last_rotated` change with its actor and `actor_kind`.

`NeighbourRefs` has no `identity` case, so the timeline degrades to the
subject's own history. That is correct and is what this section describes, but
note the consequence: the used-by panel and the timeline disagree about what a
neighbour of an identity is. **Widening `NeighbourRefs` so an identity's
timeline folds in the dependencies that name it is a genuine improvement to the
03:00 question and a separate decision — not this work.**

### The rotation action

A date input defaulting to today plus a button, on the **detail page only** — a
date input on every list row is noise. `POST /identities/{id}/rotation`, `422`
with the partial re-rendered on a future date or a retired identity.

## Findings

Rotation state reaches `EstateFindings` (`internal/store/findings.go:93`), one
row per **kind** of finding, with the count and one concrete example, like every
other source. `internal/store/template_drift.go` is the recent example of a
finding whose severity is reasoned rather than assumed.

| Finding | Severity | Reasoning |
|---|---|---|
| Rotation overdue | **Fault** | The estate's own declared rule says 90 days and it has been 200. Something is wrong *now* — the same shape as *"a contract has lapsed"*, which the severity comment names as the archetypal Fault. |
| Policy set, no rotation ever recorded | **Gap** | The inventory does not know when this was last rotated, so it cannot say whether the rule is met. Calling it a Fault would claim knowledge nobody has: it might have been rotated last week by somebody who did not write it down. **Gap is the severity that makes the other two trustworthy** — *"a report that cannot say 'I do not know' is a report that guesses."* |
| A live dependency names a **retired** identity | **Gap** | The inventory contradicts itself: either the edge is stale or the service is authenticating with a withdrawn credential, and it is not knowable from here. Same call `template_drift` made for the same reason. The counter-argument — that a withdrawn credential still in use is sharper than a missing port template and should be a Fault — is real; it is recorded here rather than left for the next person to re-make. **This is the one finding to cut if the package runs long**, and cutting it costs one query. |

**`rotation_days IS NULL` is not a finding.** A credential nobody intended to
rotate is not a problem, and flagging every `cert_subject` and `human` row would
swamp the page — *"teach people to ignore the page"*, the reasoning
`EstateFindings` already applies to expected power convergence and
`template_drift` applies to extra components. It renders as `no policy` on the
list, which is enough.

**No "due soon" band.** It would need a horizon constant nobody has asked for,
and the list already sorts by days remaining. Add it when somebody asks, with
`ExpiryHorizonMonths` as the precedent for where the number lives.

## The demo estate must show this

**A requirement, not a nicety.** Two features have now shipped rendering as empty
pages, and this one starts from three identities that are all in the *same*
state (`rotation_days = 90`, `last_rotated` NULL) — so a fresh estate would
demonstrate exactly one of five rotation states and one of three findings.

**Four states are seeded, not five.** `RotationUnreadable` is unreachable through
the application by design — `RecordIdentityRotation` refuses anything `ParseDate`
rejects and the `CHECK` refuses anything of the wrong shape, so the only values
that reach it are ten-character near-dates like `2026-02-31` in a corrupt or
hand-edited row. Seeding one would teach a reader that the estate produces them,
which it does not. It is covered by unit tests and by a web test that writes the
value past the Go layer.

`b.identities()` (`internal/seed/seed.go:888`) declares, at minimum:

- one **within window** — a rotation recorded ~30 days ago against a 90-day policy;
- one **overdue** — ~200 days ago against a 90-day policy, so the Fault finding has a row;
- one **never recorded** — policy, no rotation, so the Gap finding has a row (this is what all three are today);
- one **unmanaged** — no `rotation_days` at all;
- one **retired**, still named by a live dependency, so the third finding and the "what is stored keeps displaying" rule are both visible.

Two constraints on how:

**Dates are relative to the seeder's clock** (`b.now`), never literals, or the
demo drifts: the within-window row silently becomes overdue some weeks after the
fixture was written. `lifetimes()` already does this.

**At least one is seeded through `RecordIdentityRotation`, not through the create
call**, so the demo detail page shows a real rotation entry in its history. This
also exercises the rule that `last_rotated` has exactly one writer — the seeder
is the first caller that would otherwise be tempted to set it on the struct.

## What this supersedes, and what has to change with it

`docs/ownership-report-design.md` §4 says, in bold, that adding `row_version` to
`identity` *"was wrong and the review was right"*, on the grounds that it
introduces versioning into an entity type that has never had it.
`internal/store/team_reassignment.go:125-129` repeats it and adds *"that decision
is not to be revisited here"*.

**That decision was right for the work it was made in, and is superseded here for
a reason it did not have.** It was about *bulk reassignment*, where
`UPDATE … WHERE team_id IS NULL` is itself the atomic eligibility check and a
version token would add nothing; that remains true and this design does not
change it. What is new is a **correction form** — the exact circumstance `00066`
added a token to `link` for, an entity that had likewise never had one, and whose
header spells out the failure: *"two operators correcting the same cable's medium
and length at the same time silently overwrite each other."* The drift objection
is answered by `DEFAULT 1` and a migration, which is how `link` answered it.

Three things move together, in one commit:

1. migration `00069` adds the column;
2. the two bulk-ownership identity branches bump it while keeping their existing
   guards;
3. `docs/ownership-report-design.md` §4 and `team_reassignment.go`'s comment are
   amended to say the column now exists, why, and that the reassignment guard is
   unchanged.

Leaving (3) undone leaves two design docs flatly contradicting each other, which
is how the `prefix.vlan_id` pair happened.

**SIGNED OFF by Gabriel, 2026-09-15.** Flagged rather than assumed, because the
earlier doc asked for it not to be revisited.

### Amend §4 to say superseded, never "wrong"

The amendment to `ownership-report-design.md` §4 must say that the column now
exists **for the correction path**, that the reassignment guard is unchanged and
is still the whole eligibility check there, and that adding a token to *that*
path would still be wrong. Do not amend it to read as though the original
reasoning was bad. It was not: it was a condition -- *do not add a token you are
not going to maintain everywhere* -- and nobody was offering to maintain one at
the time, so no was the right answer. This work pays the condition; it does not
refute it. An amendment that reads as a retraction loses a good argument, and
the next person to face this question gets a worse answer for it.

### Bumping is ADDITIVE. The `WHERE` guards stay.

Both bulk branches keep `WHERE team_id IS NULL` / `WHERE team_id = ?` exactly as
they are and gain `row_version = row_version + 1` beside the `SET`. **Do not
"simplify" either one into a version check.** That guard is what produces the
per-item `assigned` / `no_longer_unowned` outcome the ownership report argues
for on its own merits -- *"All-or-nothing would punish the operator for someone
else's correctly-made edit"* -- and replacing it with a token comparison
silently converts a skip-and-report into a 409, which is the opposite behaviour
for the same event.

There are **exactly three statements that write `identity`** in the whole
codebase, verified rather than assumed:

| Where | What it must do |
|---|---|
| `internal/store/deps.go:890` (`CreateIdentity`) | insert `row_version = 1`, like every other create |
| `internal/store/team_reassignment.go:224` | keep `WHERE id = ? AND team_id = ?`, add the bump |
| `internal/store/bulk_ownership.go:303` | keep `WHERE id = ? AND team_id IS NULL`, add the bump |

A fourth writer appearing later must bump it too, or the token stops being one.
That is the whole substance of the original objection and it is the thing this
work is committing to maintain.

## Not this work

**A machine-recorded rotation.** A Vault webhook stamping `last_rotated` is a
genuinely useful thing and it is a different design. `last_rotated` is
**declared** — somebody asserts a rotation happened — so a credential writing it
needs the whole declared/observed argument redone from `docs/AUDIT.md` rules 1-7,
and rule 6 currently forbids an agent-reachable handler from returning an
identity row at all. Nothing in this work makes it harder; nothing in it should
be read as a step towards it.

**Indexing identities for global search.** It needs a per-type link mapping on
the search-results surface, and the 03:00 question is *"what does this service
authenticate with"*, which the dependency panel already answers from the other
direction. If it is ever built: name, realm and kind only — **`secret_ref` must
never enter `search_index`**, which is readable by every authenticated user
including the read-only ones that the display gate exists to keep it from.

**A restore path.** A retired identity's `(realm, name)` is immediately reusable
by design, so the documented response to a compromised credential — retire it,
declare its replacement under the same name — needs no un-retire, and adding one
would put two live rows in contention for the same name.

**A "due soon" finding band.** See Findings above.

**Certificates.** `certificate` has its own expiry model, its own findings and its
own `key_ref`. A `cert_subject` identity is a principal, not a certificate, and
joining the two is a separate piece of work with a real question in it (which of
the two owns the expiry date).

## Global constraints

CLAUDE.md's, in full, and binding rather than background:

- `?` placeholders only, `sqlx.Rebind` before execution, every query runs
  unmodified on **both** engines; `make test` green on SQLite **and** Postgres —
  `go test ./...` alone silently skips the Postgres half.
- UUIDv7 `TEXT` ids and RFC3339 UTC `TEXT` timestamps generated **in Go, never in
  SQL**; `last_rotated` is a `YYYY-MM-DD` date, also generated in Go.
- Enums are `TEXT` + `CHECK` plus a matching Go constant set.
- **Every declared mutation writes a `change_log` row in the same transaction.**
  Since WP-G1 this is an authorization invariant, not only an audit rule: a
  declared mutation that does not log is an unauthorized change that succeeded.
- **Soft delete only.** `lifecycle = 'retired'`, never a `DELETE`.
- Constructors validate; the DB `CHECK` is the second line of defence.
- Validation failure returns **HTTP 422** with the form partial re-rendered —
  never a 200 with an error buried in the body.
- Every non-GET route is behind **CSRF** and `RequireWrite` (here:
  `RequireAdministrator`, argued above).
- **AGPL-3.0-only header on every new file** (`.go`, `.sql`, `.html`), with a
  **blank line** before the package clause in Go or the licence becomes the
  package doc. `git add` before the licence scan.
- **No new dependency.**
