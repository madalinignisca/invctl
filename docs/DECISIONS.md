# Decisions

Answers to `HANDOVER.md` §11, plus the deviations from the handover schema and
the two stack choices that needed resolving. Recorded here because several code
comments point at this file, and because a decision without its reasoning is
just a constraint nobody can safely revisit.

---

## §11 open questions

### 1. Kubernetes granularity — *needed by M2*

**One `service_instance` per replica, placed on the node it runs on.**

Per-workload with a replica count is tempting because it is less data, but it
makes the impact engine useless for exactly the case Kubernetes exists for: if
three replicas of a workload are recorded as one row against the cluster, then
draining a node reports either nothing or everything, and neither is true. The
whole value of the placement phase is knowing which node each copy is on.

The consequence, once a discovery reconciler exists, is that it owns those rows
outright and nobody hand-edits them. For the POC they are seeded like any other
instance.

`rt_k8s` keeps `replicas_desired` as declared intent, so a drift check ("three
declared, two placed") is available later without a schema change.

### 2. Interface identity across reboots — *needed by M1*

**`(asset_id, name)` is the key. MAC is an attribute.**

A MAC follows the card, not the port: replace a failed NIC and the cable, the
patch panel port and the switch port are all unchanged, but the MAC is new.
Virtual interfaces are worse — many regenerate a MAC on every boot unless
pinned. Keying on MAC would make a NIC swap look like a decommission plus a
new interface, and would break the cable topology every time.

So `UNIQUE (asset_id, name)` is the identity, and `mac` is an indexed attribute
that search resolves through. A reconciler matching on MAC alone would be
wrong; matching on name and *updating* the MAC is correct.

### 3. Change log granularity — *needed by M1*

**Field-level diffs, with a full snapshot on create.**

Full-row snapshots are simpler but answer the wrong question. "Who changed the
availability policy" is the question people actually arrive with, and against
snapshots that means diffing rows by eye. Field-level diffs answer it directly
and are far smaller, which matters for a table that is never pruned.

The usual objection to diffs — that point-in-time reconstruction needs a
starting point — is handled by the create entry carrying a complete snapshot
under `"new"`. Replaying from creation reconstructs any row at any time.

`updated_at` is excluded from diffs. It changes on every write and would bury
the field that actually changed.

A no-op update writes no entry at all. An audit trail full of empty entries is
worse than one without them, because it trains people not to read it.

### 4. Multi-tenancy of the transit zone — *needed by M1*

**Yes, an asset may belong to several transit environments — and transit
membership never counts towards a span.**

The span report exists to surface segmentation exceptions. A firewall or a
transit switch bridging two segments is not an exception; it is the mechanism.
Counting transit membership would put every firewall permanently at the top of
the report, and a report whose top entries are always the same is a report
nobody reads.

So `SpanningAssets` counts only non-transit memberships and flags an asset in
more than one. The shared switch carrying production and development VLANs is
a finding; the edge firewall in production and transit is not.

### 5. Where certificates live — *needed by M3*

**An opaque `certificate_id` string, not an entity.**

Certificates are already managed somewhere — Vault PKI, an internal CA, ACME —
and each of those is authoritative for expiry, chain and rotation. Modelling
them here would create a second copy that is wrong within a month.

The column holds a reference into whichever system issues them
(`vault-pki/orders-api` in the fixture). If certificate expiry becomes
something this tool should reason about, it arrives as a discovery source
populating a real entity, and this column becomes its natural key.

---

## Deviations from the handover schema

Two columns were added. Both are noted in the migration that introduces them.

### `service_instance.shard`

The `sharded` availability policy is defined in §3.3 and evaluated in §6 phase
2 as "every shard has ≥1 replica" — but the schema has nowhere to record which
shard an instance belongs to. Overloading `role` was the alternative and it is
worse: `role` already means primary/standby for `active_passive`, and one
column meaning two different things depending on a third is how data models rot.

`EvaluateCapacity` falls back to `role` when `shard` is empty, so the overloaded
form still works if it turns up in existing data.

### `dependency.lifecycle`

"Soft delete only" is a hard rule, and a dependency edge is the single thing
operators most often need to withdraw after entering it wrongly — a mistyped
provider, an edge recorded against the wrong consumer. Without a lifecycle
column the only options were a hard `DELETE` (against the rules) or leaving the
wrong edge in place (silently corrupting every impact report).

Values are `active` and `retired` only. Retired edges are excluded from the
graph, from both dependency panels, and from the impact engine, but the row and
its audit history remain.

---

## Stack decisions

### Tailwind without DaisyUI

`CLAUDE.md` asks for the Tailwind standalone CLI and flags DaisyUI as needing
verification. Verified: the standalone binary (v4.3.3) runs with no Node
runtime at all, but DaisyUI is an npm package that has to be present on disk,
so using it would mean an `npm install` in the build — a second runtime in the
pipeline purely for styling convenience.

Dropped, as `CLAUDE.md` allows, in favour of a small hand-rolled component
layer in `web/src/app.css`. The generated stylesheet is committed, so the Go
build never needs the Tailwind binary; `make css` regenerates it and downloads
the binary on demand.

### The CSP build of Alpine

The standard Alpine build compiles `x-data` attribute expressions with the
`Function` constructor, which requires `script-src 'unsafe-eval'`. For a tool
holding an estate's entire topology, weakening the CSP for styling convenience
is a poor trade.

`@alpinejs/csp` is vendored instead. Components are registered in
`web/static/app.js` and referenced by name from `x-data`, which keeps the CSP
at `script-src 'self'` with no `unsafe-eval`. A test asserts that header, so
switching builds cannot silently weaken it.

### `BYTEA` as the byte-column type

The handover writes `addr_start BLOB`. PostgreSQL has no `BLOB` type, so a
literal reading would have forced the whole core schema to be dialect-split.

`BYTEA` works on both: PostgreSQL requires it, and SQLite does not recognise
the name, so the column takes NUMERIC affinity — which only ever coerces *text*
that looks numeric and therefore leaves blob values untouched. Bytewise
comparison then behaves identically on both engines.

This is subtle enough that `TestByteRangeContainment` asserts a byte-for-byte
round trip and the full containment query on both engines rather than trusting
the reasoning.

### FTS5 tokeniser characters

`.` and `:` are token characters so an IPv4 address, an IPv6 address and a MAC
each stay one token instead of fragmenting into meaningless numbers.

`-` is deliberately *not*, which was a corrected mistake: with it,
`hv-01-renamed` was a single token and the prefix query used for type-ahead
could never match `renamed`. Splitting on the hyphen makes `orders-api`
findable as `orders`, as `api`, and as the whole string. The test that caught
this passed on PostgreSQL — whose `LIKE '%q%'` does substring matching — and
failed on SQLite, which is exactly why the suite runs against both.

---

## Deliberately not built

Everything past M5, per §7: discovery agents, the lint engine, firewall
reconciliation, and the read-only Ansible inventory endpoint. The schema
carries the columns they need (`source`, `confidence`, `first_seen`,
`last_seen`, `verified_by`, `verified_at`, `firewall_rule_ref`) so that adding
them later is not a migration of existing data.

---

## Decisions taken during the network-reachability work (2026-07-28)

### The audit trail carries an opaque identifier, not a name

`change_log.actor` holds `app_user.id` (a UUIDv7), never a username or an email.
The UI joins to `app_user` to display a name.

This removes the storage-limitation problem at the root rather than managing it: the
audit trail contains no personal data, so it can be kept forever without a retention
argument. `app_user` still holds the username and email — it must, to authenticate
anyone — but that is a small, current, purpose-limited table. An erasure request is
satisfied by scrubbing the `app_user` row; the audit trail keeps referential integrity
through the opaque id and simply stops resolving to a name, which is exactly the
behaviour an append-only log should have.

LDAP-sourced accounts get the same treatment: the directory owns the identity, this
system stores a local id.

The rule generalises, and review found two places where it had to: **any column that
records who did something stores an id, never a name.** `dependency.verified_by` is a
person's attestation and held a username, which `logUpdate` then baked as a literal
into `change_log.diff` — where scrubbing the `app_user` row would not have reached it,
because a diff stores a value rather than resolving a join. It now stores the id and
resolves for display like `actor` does. Separately, creating an account wrote the
username, display name and email into the create snapshot; those are redacted per
entity, so the entry still says an account was created and still resolves to a name
while the account exists.

Redaction is per entity, not per column name, because `display_name` is a person on
`app_user` and a Windows service's description on `rt_windows` — judging by column name
alone would either leak the first or destroy the second.

`actor_kind` stays, because "was this a person or a machine" is not personal data and
is the thing a reader needs at a glance.

### invctl never acts on the estate

This system presents state. It does not push configuration, does not remediate, does
not restart anything, does not open or close a firewall rule. `HANDOVER.md` §1 already
lists configuration management as a non-goal; this promotes it to a rule that governs
every future feature.

The consequence for observed health: it may inform what is *displayed* — an impact
report may account for what monitoring reports, clearly labelled as observed with its
reporter and age — because showing is not acting. It may never trigger anything. There
is no remediation path to build, so the "a lying credential gets a node rebooted" risk
does not arise: nothing reboots anything.

The audience is a person during an incident, and the output is understanding.

### Agent credentials are an environment allowlist

`INV_AGENT_TOKENS` holds a comma-separated list of `id:token` pairs, matching the
deliberately trivial POC RBAC of `INV_ADMIN_USERS`. An agent not on the list cannot
write. Revocation is removing an entry and restarting.

A credential table with hashed tokens, `last_used_at` and `revoked_at` is the right
answer for a real deployment and is post-POC work. The env allowlist is chosen with
that trade understood, not by accident.

### Schema churn is free until the POC is signed off

Until the POC is declared done, migrations may be destructive: rewrite tables, move
columns, drop things. The estate is a fixture and there is no data to preserve.

After sign-off this stops. From that point migrations are additive and reversible,
every release must upgrade an existing database in place, and a destructive migration
needs an explicit decision recorded here. Note the boundary when it is crossed — the
first deployment holding real data is the moment this rule changes.

---

## 2026-07-28 — M4: reachability applies

### The gate was inverted, not deleted

M3 shipped reachability computed and reported but unable to move a status, behind
`Request.ApplyReachability` defaulting off. M4 makes it live. The flag survives as
`Request.ReportReachabilityOnly` with the sense reversed, so the zero-value request is
production behaviour and the report-only view stays reachable.

Keeping it costs one branch and buys the honest way to introduce this to an estate that
has just entered its cabling and not yet checked it: look at what the model concludes
before letting it change answers you already trust.

### A partitioned provider is not a failed provider

Seam 2 forces an unreachable provider's status to `down` before propagation runs. That
is right for deciding the consumer's fate — the consumer cannot reach it, so the effect
is identical — and wrong for explaining it.

While the seams were gated this path had never executed. Flipping the default exposed
the consequence: the report said *"hard dependency on pgsql-core is down"* about a
database that was up, healthy, and not even listed among the affected services. During
an incident that sends somebody to inspect a green service while the break is in the
path.

`propagationReason` now takes the pre-downgrade status and says "unreachable" when the
network, not the provider, caused the change. `Cause` classifies such an edge as
`reachability` rather than `dependency`, because Cause answers *where do I go to fix
this?*.

The general lesson, worth keeping: **a flag defaulting off defers test coverage, not
just risk.** Everything behind M3's gate was untested-in-effect by construction. Flipping
a default is the moment latent defects in the gated path become live, and it deserves the
scrutiny of a feature landing rather than the ceremony of a one-line change.

### "Nothing breaks" had to stop being printed above a list of things that broke

A network asset can be lost without any service changing status while still isolating
assets or leaving a group without redundancy. The impact page's empty state keyed on
service impact alone, so it printed **Nothing breaks** above a populated reachability
panel. `HasNetworkFinding` splits the two, and the copy now says a status did not change
*and* the network did.

### `endpoint.exposure` did not become load-bearing after all

The design doc flagged this as an M4 blocker: exposure had been decorative, and making it
matter would silently change results for everyone who typed `internal` because it sounded
modest. In the built form it does not. Exposure feeds Seam 3 only, which builds
`Result.Unreachable` as a separate list and never merges into `statuses`. A mistyped
exposure can add a row to a report; it cannot move a status. The review pass over every
endpoint's exposure is worth doing before anyone *acts* on that panel, and is not a
precondition for M4 being correct. If Seam 3 is ever promoted to move statuses, it
becomes a blocker again.

### Review found the same defect one hop down, and an ordering hazard under it

`code-reviewer` caught two things the direct-endpoint fix did not, both confirmed by
reverting the fix and watching the new tests fail:

**The route path launders the network effect.** `routeStatuses` folds member
reachability into a route's status *before* `phasePropagate` sees it, so a route whose
backends are unreachable from its own frontend arrives looking exactly like one whose
backends crashed. Comparing against the consumer's own hop cannot tell them apart —
that hop is fine. `routeHealth` now carries the status twice, applied and network-free,
and `netEffect` is derived from the gap between them rather than from `reachOfDep`. One
rule now covers both shapes. Proven by `TestPartitionThroughARouteIsNotReportedAsFailed`,
which failed with `"hard dependency on route orders.example.com is down"` about a healthy
proxy.

**`Cause` was decided by unordered SQL.** `SELECT * FROM dependency WHERE lifecycle = ?`
had no `ORDER BY`, and `phasePropagate` records attribution from whichever edge last
changed a status. Two providers that independently produce the same terminal status —
one dead, one partitioned — meant row order picked the explanation, and SQLite and
Postgres are free to disagree about the same data. The portability rule this project is
built on was being violated in the answer rather than in the schema. Now `ORDER BY id`,
which is meaningful because UUIDv7 sorts by creation time.

Which of two tied explanations wins is now stable but arbitrary. Reporting all
contributing causes instead of the last one is a product decision and has not been made
— recorded under known limits.

**`WontRestart` rows inherited the wrong `Cause`.** A startup dependency never changes a
status (`NatureStartup` always propagates ok), so it never reaches the attribution
assignment, and the row inherited whichever unrelated edge moved the service. `Via` and
`Reason` were already repointed; `Cause` now follows them via `startupFault.NetEffect`.
Not user-visible today — the WontRestart table does not render `Cause` — which is why it
is worth fixing now rather than after something starts reading it.

### `make test` was green only when invoked as `go test`

Unrelated to reachability, found by running the real gate. The Makefile exports
`INV_LISTEN=0.0.0.0:8088` for the demo server, so `TestLoadDefaults` asserted the default
port while a non-default port sat in its environment. It failed under `make test` and
passed under a bare `go test` — the suite's verdict depended on how it was invoked, and
the build cache hid it.

The fix belongs in the test, not the Makefile: a test named "defaults" must own its
environment. `pristineEnv` clears every `INV_*` variable `Load` reads.

---

## 2026-07-28 — M5: the fixture, multi-asset outage, and a channel that had never run

### Correction to the M4 entry above

The M4 section states that a mistyped `endpoint.exposure` "can add a row to a report; it
cannot move a status", and that the design doc's worry about exposure becoming
load-bearing was therefore resolved. **That was a description of dead code, not of the
design.** Seam 3 folds exposure findings into `Result.Services` when every non-local
endpoint of a service has lost its anchor, and always did — the fold simply never fired.

`betterFoldAnchors` called `reach(host, anchor.GroupID)`, but `reach` resolves *both*
arguments as asset ids through `modelled()`/`netOf()`, which index `net_attachment.asset_id`
and `asset_closure.descendant_id`. A `net_group` id is neither, so the lookup returned
`ok=false` for every input in the estate and `computeUnreachable` could never emit a row.
The exposure channel had been inert since it was written.

Repaired with `reachGroup`, which is the asymmetric question an anchor actually asks: one
end is an asset that attaches to groups, the other *is* a group. So exposure is
load-bearing after all, and the review pass over every endpoint's exposure that the design
doc asked for before M4 is now genuinely owed.

### The demo said "Nothing breaks" about losing the entire edge firewall pair

Two independent causes, both fixed:

- Seam 3 inert, above, so the anchors nothing could reach produced no finding.
- `computeRedundancyLost` skipped any group whose status was `down`, reasoning it was
  "already reported down elsewhere". True only for a group something *attaches* to, which
  surfaces via `Isolated`. Nothing attaches to an edge pair — hosts attach to the core,
  which uplinks to the edge — so losing both halves deleted the last remaining signal at
  the moment it mattered most.

The result was a strictly larger outage producing a strictly quieter page, and an
unqualified all-clear about the loss of the estate's only route out. This is the same
class of failure that started the whole network work, arriving through the feature built
to fix it.

### An asset that is down is off, not cut off

`computeIsolated` reported assets that were themselves inside the outage. Losing a rack
told the operator that nine powered-off machines had lost management reachability. The
liveness rule spells the guard out — `alive := !down && !(needsNet && isolated)` is one
`&&` precisely so a loss is charged once — and the *report* never got the same guard.

### Cause must describe whatever decided the status

`Cause` tested the isolation count before the propagation edge, so a service that lost an
instance to isolation *and absorbed it* still read as a network problem. `vault` lost one
of three nodes, quorum held, and it went degraded purely through its soft edge to `sso` —
yet the row said "network path, not the service itself" about `sso`, which was 100% down
and listed two rows above. `orders-api`, same edge, same provider, byte-identical Reason
and Via, correctly said dependency. Two consumers of one provider giving contradictory
guidance is indefensible under any reading.

The propagated cases are now tested first. `via` is set only when a dependency *worsened*
a service, and status is monotonic, so a non-empty `via` means that edge carried it to the
status shown.

**The scenario test asserted the old behaviour.** It was written by an agent that did not
build the fixture, specifically to avoid recording implementation output as if it were
design — and for this one field it recorded it anyway. The separation caught most of what
it was meant to catch and not all of it; the independent operator-truth review is what
found this, with the rendered rows side by side.

### Coverage was answering a different question than it appeared to

The panel read "0 asset(s) modelled directly, 0 with no declared attachment" while
`hv-01`, `hv-02` and `hv-03` were precisely the assets with direct attachments. The
numbers count assets that *carry a service instance* — the right denominator for an impact
report, since an asset hosting nothing cannot change a service's status — but the sentence
claimed to describe the estate. Reworded to say which population it measures, and
`GroupsWithoutUplinkOrAnchor` is now rendered rather than computed and withheld.

`TopologyDeclared` was added because "no topology declared" was being inferred from the
counts being zero, which is also true of a topology attached only to assets that host
nothing.

### Attachment pins were the third silent set-replacement

`net_attachment_member` holds which chassis a cable actually lands on, and
`CreateNetAttachment` snapshotted only the parent row. Whether `hv-03` is pinned to one
chassis or two is the single fact that turns "lose sw-core-2" from a redundancy note into
four services down, and the trail could not answer who declared it single-homed. Same
shape as asset environments and dependency data classes, same fix (`netAttachmentAudit`).
There is no update path for members, so the create entry is the only chance to record them.

### The window and the outage set must re-render together

The window selector swapped only `#impact-result`, so the Remove links and the add-form
kept the window the page was first loaded with. Change to 8 h, remove an asset, and you
land on a 3-minute answer under an 8-hour heading — and async dependencies genuinely read
the window. The Remove link's own comment already reasons this out for its own direction:
"swapping only the result panel would leave every one of them describing an outage that is
no longer being simulated." Either every restatement re-renders or none may, so the window
is now a full navigation too.

---

## 2026-07-28 — M6: the observed-state seam

### AUDIT.md rule 7 contradicted rule 10, and the strict reading was unimplementable

Rule 7 said "only a `user` actor may write the provenance value `declared`". Rule 10 names
`SystemActor` as a legitimate writer of declared state — it seeds the inventory — and
`UpsertLDAPUser` creates accounts as `Kind:"system"`. Both cannot hold once the check is
enforced at the store entry points, and the strict reading was **verified to fail the
seed** at "seeding instance of vault on vm-vault-1".

Resolved toward the threat model: `CheckProvenanceWrite` denies **agent** actors. Laundering
matters because a credential arrives over the network and asserts more authority than it was
issued; `SystemActor` is this process and is not reachable from outside, so denying it closes
nothing. AUDIT.md rule 7 has been amended in place with this reasoning.

### The guard existed and nothing called it

`domain.CheckProvenanceWrite` was written, unit-tested, and invoked from **zero** store
methods. `make test` was red on both engines when the workflow finished, because the
boundary-test agent wrote `TestOnlyUserActorWritesDeclaredSource`, watched it fail, and
honoured its instruction not to touch production code to make a test pass. That is the
process working: the milestone landed with a red suite and a correct explanation rather than
a green suite and a hole.

Now wired into `CreateDependency`, `UpdateDependency`, `CreateInstance` and `UpdateInstance`,
plus two guards it did not cover:

- `CheckAttestationWrite` on `VerifyDependency`. `verified_by`/`verified_at` are a *person's*
  attestation; a machine writing them is a rubber stamp on an undocumented `chd` edge and on
  the firewall rule justified by it. Separate from provenance because it is a different
  claim — a machine may legitimately *report* an edge and may never *sign it off*, so neither
  check implies the other.
- `ConfidenceFor`. A machine's self-graded certainty was stored verbatim; rule 7 says
  confidence is set by the store from the credential. An agent's write is now fixed at
  `AgentConfidence`.

### Scope used the entity's most permissive environment

`authoriseScope` asked `AllowsAny`: does the credential cover *any* environment this entity
is in? The seeded estate has exactly the shape that makes that fail — `sw-core-1` and
`sw-core-2` are in `{prod, dev}` and prod is `in_scope` — so a dev collector's token could
assert that a production core switch was up or down. Rule 6 forbids this in as many words.

Now `AllowsAll`. A reading is visible in every environment the entity sits in, so the
credential must cover all of them.

**The existing scope test passed throughout**, because every asset it used belonged to
exactly one environment. A test whose fixture cannot express the failure is not a test of it.

### A reporter could declare its own staleness horizon

`interval_seconds` arrived from the payload with no ceiling. Declare ten years and rule 8
never fires for you: the collector dies, the estate stays green forever, and the one signal
that an intruder killed the collector is gone. Capped at `MaxIntervalSeconds` (6h) as a
domain constant, for the same reason `FlapThreshold` is one — a value the reporter can
influence is a value the reporter can use to hide.

### A flap episode never closed, which made compression a mute button

`FlapSettled` measured quiet from `state_since`, which moves on **every** transition
including the compressed ones. Any cadence faster than one change per `FlapWindow` kept an
episode alive indefinitely: toggling once every four minutes produced twenty real state
changes and zero ledger rows, at a rate that would never have qualified for compression.

An episode now also ends when it stops earning its suppression — when its own average rate
falls below the rate that opened it. With hysteresis at half the opening rate, because
opening and closing on the same number would make the flap detector itself flap.

### The clock-skew tolerance was a weapon inside its own limit

Reports more than 300s ahead were refused. A report 295s ahead was **accepted and stored as
sent**, became the monotonicity floor, and every truthful report from that credential was
then discarded as stale for five minutes — re-poison once per window and the entity freezes
indefinitely while its collector reports honestly into a black hole. Measured at 108
consecutive discarded reports over an hour.

Future timestamps inside the tolerance are now **clamped to the server clock**. The tolerance
keeps doing its real job (a collector a few seconds fast is not an error) and loses the
ability to reserve a position in the ordering. We cannot know the future, so a report
claiming it is treated as having arrived now.

### Rule 15's failure had been relocated, not fixed

The entity timeline is a `UNION ALL` of `change_log` and `observed_transition` under **one**
`LIMIT`. Observed rows are unboundedly noisier by construction, so 75 ordinary transitions —
deliberately below `FlapThreshold`, so nothing was even compressed — pushed every declared
row off an asset's timeline, including an edit made seconds earlier. Rule 15 is about exactly
this failure on `/changes`; sharing a budget moved it onto the screen an incident review
actually opens.

Each arm now gets its own budget and the merge happens in Go. Both timestamps are RFC3339
UTC TEXT with the id as tiebreak, so the order is total and identical on both engines.

### "down · just now"

`state_since` was rendered nowhere in the application. A reading showed its state pill
immediately followed by an unlabelled last-report age, so an entity that went down twenty
minutes ago read "down … just now" — pointing an incident review at whatever happened in the
last minute. The onset now leads and is labelled, and the poll age says "polled". Rule 3
forbids collapsing the three timestamps; the page must not let them *read* as one either.

### M6 follow-ups: the seven that were left open

Two of them had been under-triaged rather than judged minor, and both changed what the M6
commit message could truthfully claim.

**The boundary test could be stepped around by moving a string.** Rule 1's whole argument is
that the observed/declared boundary is structural rather than a convention, and
`TestObservedPathTouchesNoDeclaredTable` is what backs that. It resolved string constants
declared *in the file under inspection only*, so `const q = "UPDATE asset SET lifecycle = ..."`
in `health.go`, called from `observed.go`, produced a bare identifier the walker resolved to
nothing — no statement, no check, both boundary tests green while the observed path retired an
asset. Constants are now collected package-wide and bare identifiers are resolved. Verified by
reproducing the evasion: it now fails with `observed.go:1033: update writes to "asset"`.

**The drift queue was unbounded and unreachable.** `unmatched_observation` upserts on
`(entity_type, entity_ref, reporter)`, and `entity_ref` is whatever the reporter claimed — so
novel refs never collide and the counter never absorbs them. An authenticated credential could
drive unbounded unindexed inserts, each its own transaction against SQLite's single writer,
and `prunableTable` hard-coded `observed_transition` so nothing could ever remove them. Same
writer contention rule 4 exists to prevent, arriving through the drift queue instead of the
heartbeat path.

Bounded at `MaxUnmatchedPerReporter`, and given a way out via `PruneUnmatchedObservations` —
a *separate* method, not a table parameter on the existing prune, so rule 10's property holds.
Deliberately not folded into `PruneObservedTransitions`: the transition ledger is evidence and
carries the 365-day in-scope floor, the drift queue is a worklist, and sharing one entry point
would mean one set of options guarding two different risks with the weaker argument winning.

### The rest

- **`MIN(interval_seconds)`, not `MAX`.** A reporter declares a cadence per reading, so one
  entity watched lazily gave the whole credential a lazy horizon and a dead collector stayed
  "reporting" for as long as its slowest row allowed — with the fast-cadence entity correctly
  stale on the same screen. The tightest promise is the one that proves it is alive.
- **A credential that never checked in is now shown as silent**, not omitted. The panel exists
  so a dead collector is one alertable event; a collector provisioned and never deployed was
  simply absent from it, and "never started" and "started and stopped" are different findings.
- **The drift queue is rendered.** It was written from the first webhook and read by nothing —
  the same as dropping the report, except that it also grows.
- **The 24h override cap moved into `Validate`.** It lived only in the constructors, which is a
  cap describable as "must not use the other path" — the shape rule 1 rejects — and `Validate`
  is what the store entry point actually runs.
- **The override panel names the entity.** It printed the entity *type*, so two silenced
  machines rendered as two identical rows reading "asset".

### Two test assertions that were not testing what they said

- The override "why" check matched the reason anywhere on an admin's page, where it also
  survives inside the amend form's `<input value="...">`. Deleting the reason from the banner
  left it green while a read-only operator — who gets no amend form — saw no reason at all.
  Now anchored to the banner markup.
- The flap escape-hatch check was `Contains(page, "degraded")`, which is already true on a
  service page with zero observations: the override form has a `degraded` option and the
  flapping badge uses `pill-degraded`. It held with the escape hatch entirely disabled. Now
  anchored to the transition row itself.

Both were verified by reproducing the exact perturbation that used to pass.

---

## 2026-07-28 — security scan (Trivy 0.69.3 + Semgrep 1.156.0)

Ran against HEAD 0cc2bf2, weighted toward the M6 credential and webhook surface.

**The M6 surface came back clean.** Verified structurally, not just by tool output: the agent
token path is reachable from exactly one mount; the handler's store field is the two-method
`ObservedStore` and `*SQLStore` does not satisfy it; the CSRF exemption is an `ExactPath` built
from a constant with glob metacharacters refused; the agent route rejects both an established
user context and a bare session cookie by name; token comparison is SHA-256 then
`subtle.ConstantTimeCompare` with no early break; body 64 KiB, batch 100,
`DisallowUnknownFields` rejecting `reporter`/`source`/`confidence`; and no code path logs,
hashes-and-logs or stringifies a token.

Zero critical, zero high, zero secrets. The one CVE (`GO-2026-5932`, `x/crypto/openpgp`
unmaintained) is unreachable — nothing imports openpgp; `x/crypto` is pulled indirectly for
argon2 and by go-ldap. Of 136 Semgrep findings, 132 were template-engine noise: generic HTML
rules that do not model `html/template`'s contextual escaping, and a Django CSRF rule firing on
`<form>`.

Three real findings, all outside M6.

### The override renewal bug was mine, from earlier the same day

`Amend` caps at 24h **from now**; the `Validate` cap added a few hours earlier in the M6
follow-ups capped at 24h **from CreatedAt**. Before that change `Validate` enforced direction
only, so `Amend` worked as its comment described. Adding the second check made the two
contradict, and the stricter one wins.

Measured: an override 26h old could not be renewed **at all**. The operator's only route during
a multi-day incident was to clear the row and write a new one, losing the continuity the row is
kept for — at exactly the moment the feature exists for.

The window is now measured from the last decision (`UpdatedAt`, which equals `CreatedAt` on a
fresh row) rather than from creation. What rule 14 protects against is an override running
**unattended** for more than a day; an amend is a person coming back and deciding again, which
is the opposite of unattended, and each renewal is separately audited. The ceiling still holds
per decision, so a renewal is not a permanent override with extra steps.

Worth recording as a pattern: the fix that introduced this was itself closing a "the cap only
lives in the constructor" finding. Moving an invariant to a more central place is right, and it
is also when a second copy of the same invariant starts disagreeing with the first.

### `envBool` failed open on every security flag

It swallowed parse errors and returned the fallback. `INV_SECURE_COOKIES=yes` is not a value
`strconv.ParseBool` accepts, so it silently produced **insecure** cookies; `INV_LDAP_STARTTLS=yes`
silently produced a **plaintext** bind. "yes"/"no"/"on"/"off" are the spellings somebody reaches
for and the ones that fail. Now collected and refused at startup, all at once so an operator with
two typos learns about both on the first start.

### A plaintext LDAP bind was the obvious configuration

`INV_LDAP_STARTTLS` defaults to false, so setting a URL and a bind DN and starting sent an
operator's password in clear. This is the only place in the application where a human credential
crosses the network. Refused now unless the channel is encrypted by either route — `ldaps://` is
TLS from the first byte, StartTLS upgrades a plain `ldap://`.

`INV_LDAP_SKIP_VERIFY` is **also refused**. It was a loud startup warning first, on the grounds
that a lab directory with a self-signed certificate is a legitimate thing to develop against and
the channel is still encrypted. Overruled, and the argument against it is the stronger one:
encryption without verification is not authentication of the peer. Anything that can answer the
connection — a DNS answer somebody controls, a host on the path — presents its own certificate,
is accepted, and collects an operator's password on every sign-in, while the login looks
completely normal. A warning is a thing that scrolls past once and then lives in a systemd unit
forever, which is precisely how a lab setting reaches production.

A lab that genuinely needs a self-signed directory adds its CA to the host trust store. That is a
real step somebody takes deliberately, and it does not follow the config into a deployment
carrying real credentials.

The `tls.Config` still reads `SkipVerify` rather than hardcoding `false`: a struct that silently
ignores its own configuration is worse than one that cannot be given a bad value, and
`config.validate` is what makes the bad value unreachable.

Both are GDPR-relevant in the transport sense: operator passwords and session cookies are
credentials of identifiable people. Nothing was found leaking personal data at rest —
`change_log.actor` and `health_override.actor` carry opaque ids, and webhook errors echo only
environment codes and UUIDv7 ids.

---

## 2026-07-28 — the quality gate

`make lint` was gofmt, go vet and a staticcheck invocation that **printed an install hint and
exited 0** when the tool was absent. A gate that passes because it did not run is worse than no
gate, because it is believed — the same fail-open shape as the `envBool` bug fixed the same day,
in the thing that was supposed to catch bugs like it. Missing tooling is now a failure, with
`make tools` to install it.

### The linter set is chosen, not inherited

`.golangci.yml` enables what speaks to what this codebase actually is: hand-written SQL on two
engines, a server-rendered HTML surface, and one route that authenticates a machine credential.
`rowserrcheck` and `sqlclosecheck` because there is no ORM to clean up after us; `errorlint`
because CLAUDE.md mandates `%w`; `exhaustive` because a new `HealthState` or `Cause` must not
fall silently through a switch; `bodyclose`/`noctx` for the HTTP surface; `gosec` throughout.

There is no baseline file and no `nolint` sprinkled to reach green. Every exclusion carries the
reason it is safe, and the two `//nolint` directives in the tree are both "this typo is the
subject of the test".

Tests are forgiven `errcheck`/`bodyclose`/`noctx` — resources whose leak ends when the process
exits seconds later, and whose failure the test surfaces far more loudly than a linter can.
Production code is forgiven nothing.

### What it found

**Twelve handlers called `r.ParseForm` with no body limit**, including `Login`, which needs no
session to reach. `ParseForm` reads the body to completion into memory, so a single
unauthenticated request could ask the process to buffer as much as the sender liked. The M6
observation route had a 64 KiB cap; nothing on the browser surface had any. Fixed once as
`middleware.LimitBody` in the chain rather than in twelve handlers, because the per-handler
version's failure mode is the thirteenth handler.

The end-to-end test for it was **not load-bearing and I nearly shipped it that way**: CSRF parses
the form to find its token and rejects a tokenless request at 400 before the body is ever read,
so the probe returned 400 with the limit present *and* absent. Moved to a direct middleware test,
which fails properly when the cap is removed.

### The autofix broke two tests, and the tests caught it

`golangci-lint --fix` ran `misspell` over test data and "corrected" `"promethues"` to
`"prometheus"` in two places whose entire purpose was asserting that a **misspelt** vocabulary is
rejected. One inverted into "a valid name is rejected" and went red; the other kept passing while
testing something else. Both restored with `//nolint:misspell` explaining that the typo is the
subject.

`perfsprint` also rewrote a loop into a `strings.Builder` named `joinedSb131`. Replaced by hand
with `strings.Join`.

Worth keeping as a rule: an autofix that edits test data can change what a test *means*. Review
the diff, never the summary.

### Established by experiment rather than assumed

`gosec` G706 flags logging `r.URL.Path` as log injection. Rather than suppress on belief, the
behaviour was measured: this application configures `slog.TextHandler`, and a value containing a
newline comes out quoted and escaped — `path="/assets\nlevel=INFO msg=\"forged\""`. No line can
be forged. The exclusion records the experiment and the condition that would invalidate it: a
handler that writes values raw.

`govulncheck` reports **0 reachable vulnerabilities**, independently corroborating the earlier
Trivy read that the `x/crypto/openpgp` advisory does not apply — nothing imports it.

### CI

There was none. `.github/workflows/ci.yml` runs the same commands as the local targets — a CI
that checks more than `make lint` teaches people to push and wait, one that checks less makes
green meaningless. Both engines, race detector as a separate job so the fast signal does not wait
on the slow one, and `go mod tidy` asserted to be a no-op because a drifting `go.sum` is how an
unreviewed dependency arrives.

That last check found `go.mod` already stale: `argon2id`, `scs`, `go-ldap`, `uuid` and `nosurf`
are all directly imported and were marked `// indirect`. No new module enters the graph.

---

## 2026-07-29 — the pre-sign-off schema migrations

A schema review before sign-off found 49 things; 24 were claimed expensive-after-sign-off
and 8 survived adversarial verification. Three are fixed here. The rest are additive and can
wait, for a reason worth writing down.

### What SQLite can actually alter, measured

The review's own passes disagreed, so it was tested against the pinned driver rather than
argued. `modernc.org/sqlite v1.54.0` ships SQLite **3.53.3**, and it supports considerably
more `ALTER TABLE` than this codebase assumed:

| Statement | Result |
|---|---|
| `ADD CONSTRAINT <name> CHECK (...)` | works, and validates existing rows |
| `ALTER COLUMN c SET NOT NULL` | works, and validates existing rows |
| `DROP CONSTRAINT <name>` | works — **named constraints only** |
| `DROP CONSTRAINT` on an *unnamed inline* constraint | `no such constraint` |
| `ADD CONSTRAINT ... FOREIGN KEY` / `UNIQUE`, `ALTER COLUMN ... TYPE` | syntax error |

So the whole "missing CHECK" category is cheap forever, and migration `00008`'s comment
saying otherwise is stale. Exactly three shapes cannot be fixed later: **an unnamed inline
constraint, a foreign key's `ON DELETE`, and a column's type.** All three migrations below
are the first of those.

**From now on, name every constraint.** `CONSTRAINT <name> CHECK (...)` rather than a bare
inline one. Every constraint written before today is unnamed and therefore permanent;
naming them costs nothing and retires this entire class of problem.

### `service_instance` gains a lifecycle

A placement was withdrawn by writing `desired_state = 'disabled'` — and `DisableInstance`
logged that as `ActionRetire`, so the code already meant "this placement is gone" while the
column said "it is deliberately not running". Two facts in one column, which is the
intent-collapse AUDIT.md says makes drift undetectable.

The visible consequence: the withdrawn row kept its slot in
`UNIQUE (service_id, host_asset_id, ordinal)`, so re-recording that service on that host
failed with a 422 against a field the operator could not resolve. **A rebuilt VM could never
be recorded where it was.** Reproduced through the store API on both engines before the fix.

Now `lifecycle` (existence) beside `desired_state` (intent), with uniqueness as a partial
index over live placements. `DisableInstance` becomes `RetireInstance` and the button reads
*Withdraw*. Every comparable table already had this shape — `net_group_member`,
`net_uplink`, `net_attachment`, `health_override` — this one was missed.

Withdrawn placements are excluded from the impact graph rather than loaded and ignored: a
retired row was never capacity for the question being asked, so counting it would make
"1 of 2 lost" out of a service that has one. A `disabled` placement still loads, because it
exists and is expected back.

### `change_log` is the table that can never be rebuilt afterwards

Rule 10 makes it append-only, which also removes every ordinary route to altering it — the
add/copy/drop/rename dance used on `service_instance.source` in `00008` is unavailable
because its second step is literally `UPDATE change_log`. A full rebuild is the only option,
and after sign-off rebuilding the audit table is precisely the act the append-only guarantee
forbids. If it was ever going to be right, it had to be now.

Its `action` CHECK was already too narrow and unnamed, so it could never be widened: rule 10
records the cost being paid, with the retention prune logging itself as `delete`. Verifying
a dependency logged as `update`; clearing an override logged as `update`. Now
`verify`/`clear`/`prune`/`import` exist.

It also had no constraints beyond NOT NULL, which does not stop an empty string —
`entity_type=''`, `actor=''`, `diff='not json'`, `at='yesterday'` were all accepted by the
permanent record of who changed what. Now named CHECKs on each, including an `at` pattern
that catches a timestamp written in some other shape, which would otherwise sort wrongly
forever in a table ordered by that column.

Every row is carried across verbatim. Verified against a populated table on both engines:
row count identical, and every id identical, across a down-then-up cycle.

### `identity` uniqueness now fires, and respects the lifecycle

Three defects, one rebuild. `UNIQUE (realm, name)` was inline and undroppable while the
table carries a lifecycle, so a retired identity reserved its name forever — in the table
where the natural response to a compromised credential is to retire it and recreate it under
the same name. `realm` was NULLABLE, and `NULL <> NULL`, so the constraint did not fire at
all for the realm-less local accounts most likely to collide. And `lifecycle` was the only
lifecycle column in the schema without a CHECK, so `'retierd'` was storable and silently
matched no filter.

`realm` is now `NOT NULL DEFAULT ''`, normalised in one place (`realmOrEmpty`) so "no realm"
stays expressible as a nil pointer in Go.

### Down migrations are now exercised

`TestDownMigrationsRun` runs every down migration on both engines, to zero and back. Nothing
did before, and after sign-off "reversible" becomes a rule rather than an aspiration — a
down migration nobody has run is a claim, not a property. These three are also the first in
this project to rebuild a table, which is where a down migration goes wrong.

---

## 2026-07-29 — vocabularies: lookup tables and named constraints

Two changes, one before sign-off because both alter shapes that freeze at it.

### Why not just "avoid enums, guard in code"

The stack never used SQL `ENUM` — `CLAUDE.md` forbids it — so the MySQL objections to that
type (ordinal storage, silent coercion, `ORDER BY` by hidden integer) never applied. What did
apply is the rigidity: a `CHECK (col IN (...))` is a closed vocabulary baked into DDL, and on
SQLite an *unnamed* one can never be dropped.

But "guard in code only" inverts the risk for half of these columns, so the split is by what
the value **does**:

- **Domain vocabularies** describe the estate, grow, and are mostly passed through by code.
  `asset.kind`, `service.kind`, `interface.form_factor`, `environment.role`,
  `ip_address.role`, `rt_container.engine`, `dependency_data_class.data_class` → **lookup
  tables**, keyed on the code already stored so no data migrates and no query changes.
- **Behavioural enums** select a code path. A value arriving as data makes every Go `switch`
  fall through silently, which is worse than a rebuild — and this codebase already paid for
  that once when `service_instance.source` had no CHECK. They keep `TEXT` + `CHECK`, but
  **every constraint is now named**, so widening one is a one-line `ALTER` instead of a table
  rebuild.

### The classification error, and where it came from

`asset.kind` and `environment.role` were put in the *domain* bucket on the grounds that code
passes them through. It does not: `CanHostInstances` and `IsAttachable` switch on the kind,
and `IsTransit`/`SpanningAssets` on the role. By the split's own criterion they are
behavioural.

The consequence was measured on both engines: `bridge` added as data could be stored, an
asset created — and then `CanHostInstances = false`, `IsAttachable = false`, with no
diagnostic. **The feature the whole change was requested for did not work.** A bridge that
can carry nothing and take no network attachment is not a bridge.

The brief given to the implementers said "implement it, do not relitigate it". They flagged
the problem anyway, recorded it in the migration header, and shipped it as instructed. The
instruction turned a correct objection into a documented defect; the flag should have been
treated as the stop signal it was.

**Resolved by making the behaviour travel with the vocabulary row** rather than reverting the
classification: `asset_kind.can_host_instances`, `asset_kind.is_attachable`,
`environment_role.is_transit`. The Go switches are gone — one authority, in the less
convenient place, rather than two that can disagree. `domain.AttachableAssetKinds` survives
only as the set the migration seeds `TRUE` for, so the two cannot drift at install time.

Both flags default `FALSE`, so a value added without thinking is inert rather than quietly
granted placement rights nobody considered.

### What SQLite can and cannot unfreeze — corrected

Earlier notes said naming a constraint makes it droppable. That is true of a **CHECK** and
false of a **UNIQUE**: `ALTER TABLE ... DROP CONSTRAINT <name>` on a named UNIQUE returns
`constraint may not be dropped`. So the ten UNIQUEs this work names gained nothing, and two
anonymous ones (`application.code`, `backend_pool(service_id, name)`) are equally permanent
either way.

**For new tables, prefer `CREATE UNIQUE INDEX` over an inline `UNIQUE`** — an index can be
dropped on both engines, a table constraint cannot on SQLite. Nothing to do about the
existing ones short of a rebuild, which is not worth it for constraints nobody expects to
widen.

### The down migration wedged on first use of the feature

Restoring the original vocabulary CHECK failed the moment anyone had added a value — the
first thing the up migration invites. Because those files are `NO TRANSACTION`, the failure
left a half-built table behind while goose recorded the version as applied: a database that
could be migrated neither forward nor back.

The down migrations no longer restore the vocabulary CHECKs. Deleting the rows that used
added values destroys real inventory to satisfy a development convenience; coercing them puts
a false fact in the estate. So the column returns as unconstrained TEXT and the down is
honest about being one-way in that one respect.

### Three test classes that did not exist

Each was found by breaking the thing it now guards:

- **Nothing asserted a constraint had a name.** Stripping every `CONSTRAINT` token from
  migration 00005 — reducing it to a no-op rebuild of sixteen tables — left the suite green.
- **Nothing compared constraint names across engines.** Two disagreed
  (`service_instance_runtime_check` vs `..._runtime_type_check`, and a `source_checked` scar
  from 00008's column dance), which meant widening either needed two different statements.
- **Every migration test ran against an empty database**, so the `INSERT ... SELECT` copy at
  the heart of a rebuild was exercised against zero rows. Removing `owner_team` from a copy
  list was invisible: twenty assets lost it and the suite stayed green.

### The foreign key check that could not fail

Several SQLite migrations rebuild a table, which means dropping it, which means turning
foreign key enforcement off first — six tables cascade off `service_instance` alone, and a
`DROP` with enforcement on silently takes their rows. Each of those migrations ended with
`PRAGMA foreign_key_check`.

It never worked. goose runs migration statements with `Exec`, the pragma reports violations
as **result rows**, and `Exec` discards them. Measured: on a database holding a known
dangling reference, `Exec` returns `nil` while `Query` on the identical statement returns the
violation. Every one of those six pragmas was decorative, and a rebuild that orphaned half the
estate would have reported a clean upgrade.

Now `store.verifyForeignKeys` runs the same check with `Query`, once, after every migration —
not in each migration, because a check a migration has to remember is a check the next
migration forgets. It covers every migration ever added without anyone thinking about it.
PostgreSQL is skipped: it enforces references continuously and has no mode in which a
migration can switch that off.

The pragmas are removed rather than left in place, with a comment where the first one was
saying why. A statement that reads like a safety net and is not one is worse than no
statement, because the next person reads it and stops looking.

## 2026-07-30 — Same-band edges are rails, not curves

The neighbourhood diagram drew a same-band connection as a quadratic arc dipping
into its band's lane, nested spans at increasing depths. The depth system's whole
purpose was that a span drawn inside another stays inside it. For curves that is
unachievable: near a shared endpoint the long arc is necessarily shallower than
the short one — its control point is far away — so the short arc dips below and
must come back through, whatever depths are assigned. Holding the containment
would need depth superlinear in span, which no lane can afford. This is a property
of the curve family, not a bug in the depth assignment.

Measured over 4000 generated neighbourhoods, the curves drew 36,608 crossings
between arcs sharing an endpoint — none counted by anything, because both the
ordering model and the tests treated a shared endpoint as a junction.

So same-band edges are now **rails**: straight down from the anchor, along at the
assigned depth, straight up. Flat profiles make "deeper" mean *below at every
shared x*, and three consequences follow:

- **Depths became a real interval colouring.** Overlapping spans must not share a
  depth (collinear runs read as one line); contained spans must be strictly
  shallower than their container. The old level cap is gone — the lane is sized
  from what is assigned, and the level only grows with real nesting or a real
  overlap clique.
- **Anchors fan along the box edge**, deepest innermost, step shrinking so every
  rail keeps a distinct vertical even on a hub (a clamped shared anchor was
  measured to reintroduce 255 crossings; the shrinking step reintroduces zero).
- **The drawn geometry now equals the ordering model for same-band pairs by
  construction** — interleave crosses exactly once, nesting and disjointness
  cross zero times — so the model gained a term it could finally afford: a rail
  pierced by a cross-band edge leaving a box strictly inside its span. The sweep
  now minimises piercings it previously created freely (8,534 → 5,818 drawn on
  the corpus). `Layout.DrawnCrossings` measures the placed polylines and is the
  number a legibility claim must cite; the slot model remains the optimiser's
  objective, and the residual gaps between them are enumerated in measure.go.

Corpus totals: 56,142 drawn crossings with curves, 17,020 with rails; the
shared-endpoint class went to zero. On the seeded 27-asset estate: 1,415 → 773.

Dependency edges also gained an arrowhead at the provider end (`marker-end`,
two defs so an optional dependency's arrow matches its faint stroke) — direction
was previously carried only in hover text.

---

## 2026-07-31 — Projects, and absorbing `application`

Assets and services now belong to a **project**, which is the business view of the estate:
who owns what, and who is standing on somebody else's work. Everything else in the model
answers a technical question — an environment says where a thing runs, containment says what
it sits inside, a dependency says what it needs — and none of them answers "what does this
project consist of", which is what a product owner or a CTO asks. It is deliberately a
different shape: a project cuts *across* environments and racks rather than nesting inside
them.

### Two relations, and the asymmetry is the whole design

`owns` — the thing exists for this project. **At most one project may own a given asset or
service**, enforced by a partial unique index on `(asset_id)` / `(service_id)` where
`relation = 'owns' AND lifecycle = 'active'`, not by application code that checks first. It
is the anchor a later cost model attributes 100% of a thing's cost to, and it is the only
relation the derived footprint follows.

`uses` — the project depends on it and shares it with others. Any number of projects may use
one thing, and **nothing is derived from a `uses` link**. What is inside somebody else's
hypervisor is their footprint, not yours; deriving through `uses` would quietly attribute
another team's estate — and later another team's costs — to whoever declared a `uses` link
first.

### The footprint is derived at read time and stored nowhere

If a project owns a hypervisor, the guests inside it and the services running on them are
part of what it costs and what breaks when it breaks — but nobody declared them, so no row
says they belong to it. Writing that back would make a derived fact indistinguishable from a
declared one in `change_log` (the laundering rule 7 of `AUDIT.md` exists to prevent), and it
would go stale the moment a VM moved. Every derived number on the overview is labelled as
derived, beside the declared ones. A manager who cannot trace a number stops believing all
of them.

An implied asset that another project owns outright is reported as a **conflict** rather than
absorbed, because silently counting it would double-count it the day cost lands.

### `application` is gone — migration `00010`, and it is destructive

`application` grouped services and only services, had no UI anywhere in the app, and held one
row in the fixture. Projects group assets *and* services and carry the owns/uses distinction
it never had. Keeping both would leave two overlapping ways to say "these things belong
together" and the weaker one would rot.

Migration `00010` therefore copies every application into `project` **keeping its id** (so
`change_log` rows written against `entity_type = 'application'` still resolve), turns
`service.application_id` into an `owns` link, reindexes search, and then drops the column and
the table.

This repository's rule is that a destructive migration needs an explicit recorded decision
after sign-off. **Sign-off has not happened.** The estate is a fixture plus one throwaway
demo database, and the standing position — "until we get a production ready, and a first real
client, even rewrite the migrations from zero" — applies. This is free now and would not be
in a month; the entry exists so that later readers know it was a decision and not an
oversight.

No rebuild was needed on either engine, which is not obvious and was measured rather than
assumed, against the same pinned driver as the 2026-07-29 entry:

| Statement | Result |
|---|---|
| `ALTER TABLE service DROP COLUMN application_id`, with `idx_service_app` present | `error in index idx_service_app after drop column: no such column` |
| `DROP INDEX idx_service_app;` then the same `DROP COLUMN` | works; the resulting DDL carries no `REFERENCES application(id)` |

So SQLite needs the covering index dropped first and PostgreSQL does not care, and the
twelve-table rebuild `00002` had to perform is avoided entirely.

**The migration writes no `change_log` row.** No migration in this repository does: the trail
records what operators do to the estate, and this is the schema arriving. It is a closer call
than usual because data moves rather than only shape — `00009` reserved an `import` action for
exactly this — and the choice is recorded here rather than left to be inferred.

### A migration that would otherwise have been untestable

`Migrate()` has no partial entry point, and the existing upgrade test re-runs `Migrate` over
an already-migrated database, which for the newest migration is a no-op. A real test of the
absorb has to reach the state *before* it, put an application and a service pointing at it
into that schema, and only then migrate the rest of the way — so the highest-risk statement in
the change would have had no coverage at all **and would have looked covered**. That is the
same shape as a flag defaulting off: it defers the test, not just the risk. `migrateTo` (a
`goose` `UpTo` against the dialect directory) exists for that one test.

Two things it caught that a reading would not have:

- The test's own "the table is gone" assertion was wrong on PostgreSQL. Each test runs in its
  own schema with `search_path = <schema>,public`; once the migration correctly dropped
  `<schema>.application`, a bare `SELECT 1 FROM application` resolved through to a stale
  `public.application` left by an old pre-isolation run and reported the table as present.
  Name resolution cannot express "absent **here**", so both existence checks now ask the
  engine's catalogue about `current_schema()` — the only engine-specific statements in the
  store tests, and worth the exception.
- Mutating the migration confirmed the assertions bite: removing the ownership copy fails with
  `owns links = map[]`, and removing the search reindex fails with
  `search_index still holds 1 application documents`.

### The fixture now argues for itself

Three projects, and every link is chosen so that one derived panel has something true to say —
a demo where the interesting boxes are empty proves nothing, and one full of invented findings
is worse. Each finding below is a consequence of the estate the rest of the seed already
describes:

- **platform** owns the three hypervisors, **orders** owns `vm-app-1` inside one of them → a
  footprint conflict, which is the double-counting a cost model would otherwise inherit.
- **orders** owns `vm-app-1`, and the Veeam agent runs on it uninvited → an implied service
  somebody else owns, on your hardware.
- **Nobody owns `haproxy-edge`** → an unowned dependency: the edge every partner order crosses,
  with no team to escalate to. The most realistic finding in the fixture, and it exists because
  the seed deliberately leaves it unclaimed.
- **orders** declared `uses` for the database and SSO but not for the queue → shared versus
  external, side by side, from one dependency list.
- `orders-api-dev` is in no project at all. The service list has to show "belongs to nobody" as
  an ordinary state, because in a real estate most rows start there.

### Ownership is not on the service form

The create-service form lost its old Application picker and gained nothing in its place.
Ownership is a link with a relation on it, and the place to say "this project owns that
service" is the project overview, where the choice between `owns` and `uses` is visible.
Offering only half of it on the service form would make `owns` look like the only kind of
belonging. The service list and detail page *show* the owner — or `unowned` — and the list can
filter by it.

---

## 2026-07-31 — End of life, and the shape of the cost model it precedes

`asset` and `service` gained an optional `eol_date`, and `/reports/expiry` reads
it. This is the first half of a financials feature; the second half — acquisition,
support and operating costs — follows, and the design was settled before either
was built because the two halves fail together if the first one guesses wrong.

### One date, and it is declared

Warranty end, support end and planned replacement are three different facts.
Modelled as three columns they become three columns nobody fills in consistently,
and then no report can combine them because "expiring" has to pick one. A single
date plus a note says what a person actually knows.

It reads like a fact about the world, which is exactly the trap `docs/AUDIT.md`
exists for: somebody read a contract and typed it. Nothing observes it, nothing
derives it, and a monitoring credential may never write it — a machine that could
set an EOL date could quietly age out any asset in the estate.

**Its passing is inert.** No lifecycle moves, no placement is removed, nobody is
paged. A date changes what one page says and nothing else, which is the same rule
as "invctl never acts on the estate" applied to time.

`YYYY-MM-DD`, ten characters, sorting lexicographically for the same reason the
RFC3339 timestamps do. Support ends on a day, not at a second; storing `00:00:00Z`
would invite a reader to believe a precision that is not there and would make "is
it past?" depend on a timezone nobody chose. The comparison is on whole days in
UTC, and an item whose support ends **today is not yet expired** — support runs to
the end of that day. Getting that wrong makes every renewal look a day late.

The database CHECK is a shape test — `length` and `substr`, the two string
functions both engines agree on — and the real parse is in Go, which rejects
`2027-02-31` where the constraint cannot. Named `<table>_<column>_check`: the
convention was not a guess, `TestConstraintNamesMatchAcrossEngines` failed on a
`_shape` suffix because its PostgreSQL half filters `pg_constraint` to names
ending `_check`. Measured on the pinned driver: `ADD COLUMN` then `ADD CONSTRAINT
… CHECK` both apply on SQLite 3.53.3, validate existing rows, and leave the table
unrebuilt.

### The report is about time, and about what it cannot see

Already-expired rows are **always** included, whatever the horizon. A window is a
question about the future; something that already lapsed is not a forecast, and
dropping it because it fell outside a twelve-month view is how an estate
accumulates unsupported hardware nobody is looking at.

Workload rides up through `asset_closure`, so a service on a guest counts for the
hypervisor. Without that, every expiring host in a virtualised estate reads as
carrying nothing — and "expires in 58 days, carries 6 services, best tier 1" is
the row a reader acts on, where the bare date is not.

Attribution is to the **owner only**. A project that merely uses a thing is not
who has to replace it, and naming it would send the renewal conversation to the
wrong team.

The page closes by counting what carries no date at all. Four dated rows in an
estate of four hundred reads as "almost nothing expires"; the true answer is
"almost nothing is recorded", and a report that does not say so is flattering
rather than useful.

### The cost model this is the first half of

Settled now, built next, recorded here because the failure mode is structural:

**Cost attaches to what is billed, and does not inherit.** A VM's cost is not
inherited from its hypervisor, it is a *share* of it — and if three guests each
inherit the host's monthly cost the estate reports three times one box. Silent
double-counting destroys trust in a cost report permanently, and it is the same
failure the footprint conflict panel already exists to catch one layer up. So the
cost goes on the thing that appears on an invoice, and the existing footprint
derivation does the rest. A project owning a VM inside somebody else's hypervisor
pays for no metal — which the conflict panel already says, now with a number.

**Splitting a shared thing is allocation, and allocation is declared, never
derived.** It needs a driver (vCPU, RAM, equal shares) and a policy, and every
choice is arguable. When it comes it will be a share on a `uses` link — "orders
uses hv-01 at 30%" — with the owner carrying the remainder, typed by a person and
audited like everything else.

**Three numbers, never one.** Capital committed (the one-time sum), monthly run
rate (monthly + yearly/12) and annualised. A single "this project costs €X" is
where cost reports go to die, because somebody always finds the switch bought once
inside a monthly figure.

**Kind × period, not three fixed fields.** "One-time acquisition / yearly support
/ monthly operating" is two dimensions; encoding the period in the kind doubles
the vocabulary the first time monthly support appears.

**Minor units as INTEGER, one currency in config.** Never a float, never
`NUMERIC`/`MONEY` — arithmetic differs across the engines. Summing mixed
currencies needs FX rates with valuation dates, which is a subsystem rather than a
column, and is a stated non-goal.

**A validity window from day one**, queried only for "current" at first. Without
it a renewal at a new price overwrites history and the only record left is
`change_log`, which is an audit trail rather than something to query. Squarely in
the category this repository calls miserable to retrofit.

**Open, and deliberately not decided yet:** contract values change what this
database is. Today anyone who can log in reads everything and RBAC is a
comma-separated list of admins; adding acquisition prices and licence costs makes
the estate commercially sensitive in a way it is not now. Whether cost visibility
is a third permission rather than a property of read access is easier to settle
before a client's numbers are in it than after.

---

## 2026-07-31 — Costs: what is billed, and never one number

The second half of the financials work, on the shape the EOL entry above recorded.
Migration `00012` adds `cost_kind` plus `asset_cost`, `service_cost` and
`project_cost`; the project overview totals them.

### Cost attaches to what is billed, and does not inherit

A VM's cost is not inherited from its hypervisor — it is a *share* of it, and
three guests each inheriting the host's monthly figure report three times one box.
The double-count is silent, compounds, and is found by the first person to
reconcile the page against an invoice, after which no number on it is believed
again. It is the same failure the footprint conflict panel catches one layer up.

So a cost line goes on the thing that appears on an invoice, and the existing
footprint derivation does the rest. Nothing in the code divides, spreads or
apportions. Splitting a shared thing is **allocation**; it needs a driver and a
policy, every choice is arguable, and when it arrives it will be a share somebody
*declared* on a `uses` link rather than a number the system invented.

The rollup therefore reports the same three buckets the dependency panel already
uses, because they are the same three conversations: **yours** (owned, plus what
owning implies, minus anything another project owns outright), **on your estate
but not yours** (shown, never added, and it names who carries it), and **you use,
they pay**. Only the first is a number the project is answerable for.

### Three totals, and the year is the exact one

Capital committed, monthly run rate, annual. A single "this project costs X" is
where a cost report dies, because somebody finds the switch bought once inside a
monthly figure and is right to stop reading. A one-off contributes **nothing** to
a run rate, which is the single most important line in `domain/cost.go`.

The annual figure is accumulated in its own right rather than as twelve times the
monthly, and that changed after seeing it rendered: a €940 yearly contract has no
exact monthly share, and twelve of 78.33 is 939.96. Telling a reader their €940
contract costs €939.96 a year invites them to check the arithmetic instead of the
estate. **The year is exact and the month carries the rounding** — the right way
round, since a yearly figure can always be stated exactly and a monthly share of
it sometimes cannot. The month rounds to nearest rather than truncating, or every
figure in the estate would read low, always in the same direction.

### Shape

**Kind × period.** "One-time acquisition / yearly support / monthly operating" is
two dimensions wearing one coat. `kind` is a lookup table because it is
descriptive — a new kind of spend is data, and `/vocabularies` can add one.
`period` is a `CHECK` with a matching Go constant set because it is *behavioural*:
Go divides a yearly figure by twelve, so a period added by `INSERT` would be
storable and silently uncomputable. Same line migration `00004` drew between
`service_kind` and `availability`.

**Minor units in INTEGER, one currency in config** (`INV_CURRENCY`, default EUR).
Never a float, never `NUMERIC`/`MONEY` — their arithmetic differs across engines.
Mixed currencies need FX rates with valuation dates: a subsystem, not a column,
and a stated non-goal.

**Three tables, not one polymorphic one**, mirroring `project_asset` /
`project_service`. Real foreign keys, so a typo in an import cannot invent a row
that inflates a total and belongs to nothing. Project-level lines exist because
SaaS, retainers and domains attach to no box and no service; without them a
project total is systematically low in a way nobody notices.

**The validity window ships from day one** though only "current" is queried. A
renewal at a new price would otherwise overwrite its predecessor, leaving only
`change_log` — an audit trail, not something to query.

### Visibility follows read access, deliberately

Anyone who can log in sees costs; the split stays read versus read-write. This
application is not yet aimed at an audience some of whom must be kept from
commercial figures, and a permission nobody has thought through is worse than
none because it looks like protection.

`authz.CanSeeCosts` exists anyway and returns exactly what `CanRead` does. Cost
visibility is the most likely *first* thing a real deployment separates from
ordinary read access, and when that day comes the change should be one function
body rather than every template that renders an amount — the same argument that
made `CanWrite` a one-liner.

### Three guard tests fired, and one test of mine was worthless

- `TestNoAssembledWriteReachesChangeLog` flagged `updateCost`/`retireCost` for
  building their table name at run time. The names are three package-level
  `costTable` values and never a caller's string — which is also why the routes
  are `/assets/{id}/costs` rather than `/costs/{type}/{id}` — so they are on the
  allowlist with that reasoning written out.
- `TestEveryColumnIsClassified` required the four new tables in `domain` and in
  `AUDIT.md` before it would go green.
- `golangci-lint` found `priceService` unused, which was not a style problem: the
  **service path through the rollup had no test at all**. It has one now.
- **`TestProjectCostsCountAnAssetOnce` passed with the dedup guard deleted.**
  `impliedAssets` already excludes a project's own declared assets, so nothing in
  that test was ever reachable twice and the assertion could not fail. The real
  duplicate is an asset that is *implied* and *also* declared with a `uses` link —
  what a project looks like when somebody wrote the `uses` link before the
  ownership above it existed. Rewritten around that, it fails when the guard is
  removed. A test that cannot fail is worse than no test: it reports coverage
  that is not there.

### What the fixture now says

Prices on the racks, the transit, the three hypervisors, the out-of-support
network kit, two closed-source services and a database support contract, plus
SaaS on the projects. The structural finding falls out of the ownership links
rather than being staged: **the platform team pays for nearly everything, and the
project that ships the product owns one VM and appears to cost almost nothing.**
Reading its small run rate beside "€X a month sits on this project's footprint
that Platform & Core Services owns" is the conversation the feature exists to
start.

### Still open

Amortising acquisition over acquisition → EOL, as its own labelled line, never
folded into the run rate. It needs an acquisition date the model does not have.
Deferred until the three totals have been read by somebody who wants it.

---

## 2026-07-31 — Amortisation, and the acquisition date that was already there

The deferred half of the cost entry above. It needed **no migration**, which is
the part worth recording.

### The acquisition date already existed

A one-off cost line's `valid_from` *is* the day it was paid — that is what the
field means for a `once` line — and the end-of-support date landed on `asset` and
`service` in the EOL entry. Adding an `acquired_on` column would have created a
second field holding the same fact, and the two would disagree the first time
somebody edited either. So amortisation is arithmetic over two columns that were
already there and already audited, and the only schema-adjacent change is that
`asset_cost` and `service_cost` now join their parent to read `eol_date`.

`project_cost` deliberately has no such join. A project has no end-of-support,
and a setup fee for a SaaS subscription bought nothing with a life — so a
project-level one-off is counted as unamortisable rather than silently dropped.

### Straight line, to the month, and never in the run rate

Straight line from acquisition to end-of-support. Declining balance, salvage
values and part-months are accounting rather than inventory, and this is not the
system of record for any of them — the figure exists to make a run rate honest and
to be checkable on the back of an envelope.

**It is never added into `MonthlyMinor`.** A run rate is what leaves the bank this
month; amortisation is an accounting view of money that already did. Adding them
silently would double-count every purchase. `TotalMonthlyMinor()` exists for the
one sentence on each page that says it is adding them.

Two boundaries that a reading would get wrong and a test caught:

- **The last day of a life is outside it.** An asset supported until the 1st is
  not still depreciating on the 1st.
- **A life under a month is not capital to spread**, and neither is something
  bought after it was already unsupportable. Dividing by something near zero turns
  a small purchase into an enormous monthly figure.

Only a one-off amortises. A monthly bill is already a run rate and spreading it
again would count it twice.

### Amortisable is not the same as contributing

`AmortisedMonthlyMinor` returns a figure *and* whether the line could be spread at
all, because they are different questions. A fully depreciated switch is
amortisable and contributes nothing; a switch with no EOL date is not amortisable
at all. `CostTotals` therefore carries both an `Amortisable` and an
`Unamortisable` count, and every page renders the second: a figure covering two of
nine purchases must not look like a figure covering nine.

`Add` grew two parameters rather than gaining a second method to call afterwards.
A two-call API where the second call is optional is a two-call API where the
second call gets forgotten, and the symptom would be an amortisation figure that
is silently zero.

### The fixture acquires things in the past

Every one-off in the seed now carries an acquisition date relative to the seeding
clock: the hypervisors bought roughly three years ago, the core switches long
enough ago to be fully written off, `hv-03` bought later and carrying **no** EOL
date so the unamortisable counter has something real to report. A fixture where
everything was acquired this morning would show every asset at the very start of
its life and prove nothing about the arithmetic.

Three mutations confirmed the tests bite: folding amortisation into the run rate,
treating "no EOL date" as amortisable-with-zero, and dropping the parent join each
fail a specific assertion.

---

## 2026-07-31 — Search ranking, and two engines that disagreed

Searching `hv-01` returned the bridge `hv-01-br0` above the hypervisor `hv-01`.
Found while verifying the amortisation panel, which is the only reason it was
found at all: it is exactly the kind of defect nobody reports and everybody works
around.

### Both engines were right, which was the problem

SQLite ranks with **bm25**, which favours short documents. The hypervisor's
indexed body carries its serial, vendor and model; the bridge's carries almost
nothing, so the bridge scored higher. PostgreSQL orders by **trigram
similarity** against the title, which put the exact match first.

Neither is wrong as an information-retrieval choice. But an operator mid-incident
must not get a different first result depending on which engine the deployment
runs, and this is a rare case where the portability rule cannot be satisfied by
writing one query — search is the codebase's one sanctioned dialect split. So the
answer is to stop asking the database which hit is more relevant and decide it in
**Go**, where both engines get the same rule.

Order: exact name, exact code, name starts with the query, name contains it,
everything else. Ties break on the shorter name — `hv-01` is a more specific
answer to `hv-01` than `hv-01-br0` is — then alphabetically, so the result is
stable rather than merely usually right.

### Sorting cannot recover a row the query never returned

Ranking after the fact only reorders what came back. In an estate where two
hundred rows contain the string, the exact one can fall outside the query's
`LIMIT` before Go ever sees it. Two changes follow:

- The text query **over-fetches** `limit × 4`, so the row that will rank first is
  still in the set.
- A name typed **in full** is treated as the identifier it is and looked up
  directly, alongside a serial or a MAC — `searchExactNames` covers asset names,
  service codes and names, and project codes and names. That is the guarantee;
  the ranking is the polish.

### Each half was covering for the other

The first pair of tests passed with **either** half deleted, because each
compensated for the other. That is a green suite proving nothing, so both were
rewritten to isolate their mechanism:

- `TestRankingDecidesWhenNothingMatchesExactly` searches a **partial** string, so
  no exact lookup fires and the ordering is the ranker's alone. Removing
  `rankResults` fails it with `[hv-01-br0 hv-01-br1 hv-01]` — the original bug,
  reproduced.
- `TestAnExactNameSurvivesASmallLimit` puts **twenty** bridges ahead of the
  hypervisor with a limit of 2, so the over-fetch window of 8 does not reach it.
  Removing the exact-name lookup fails it.

Both tests give the hypervisor a long indexed body on purpose. Without it bm25
does not rank it last, and the tests would pass against an implementation that
does no ranking at all.

### The list filters had the same bug, on a page people use more

Fixing the global search and then checking the estate showed `/assets?q=hv-01`
still leading with the bridge. It is a different code path and a worse one: the
list pages order for **browsing** — assets by `kind`, services by `tier` — and
`bridge` sorts before `hypervisor` every single time, so the wrong answer was
not even luck.

A filtered list *is* a search whatever the box is called, so both now apply the
same `nameRank`, on the field the operator actually typed: an asset's name, a
service's code. `sort.SliceStable` keeps the browse order as the tie-break, so a
query matching fifty things equally still reads the way the page always does, and
an unfiltered list is untouched — asserted, because a ranking that quietly became
the permanent sort would be a different bug wearing this fix's clothes.

The ranker returns a `less` function rather than sorting, because the callers
hold different slice types. An earlier attempt passed a hand-rolled
`sort.Interface` to `sort.SliceStable`, which compiles — the parameter is `any` —
and panics at run time on anything that is not a slice.

---

## 2026-07-31 — Three reviews, and what they found

Security (automated tooling plus a manual read), Go code quality, and a database
review against a live PostgreSQL 17, over `c75318c..HEAD` — 24 commits and ~14.8k
insertions, none of it previously reviewed.

**Clean:** 0 critical and 0 high CVEs, no secrets in the working tree or in 44
commits of history, no IaC misconfiguration. Every new mutating route sits behind
CSRF and `RequireAdmin`; every new read behind `RequireAuth`. The runtime-assembled
cost table names cannot be reached from a request. Index coverage for every new
query shape is index-driven on both engines — the closure walks, the expiry
joins, the `IN (...)` cost fetch. The partial unique indexes were verified
empirically across all eight ownership state transitions on both engines.

### The cost rollup was reporting a wrong number

`ProjectFootprint` took the neighbourhood node budget — 60, chosen so an SVG stays
legible — and truncated its implied assets to it **before** conflicts, implied
services or costs were computed. A rendering limit leaking into an accounting one.

Measured: a project owning a rack of eighty priced guests reported **€60 a month
against a real €80**. The twenty past the budget were not costed, not counted as
unpriced, and mentioned nowhere. A total a reader cannot trace is the exact
failure this feature is arranged to avoid.

The budget now lives in `ProjectMap`, the only caller that draws anything, and it
reports what it cut. The footprint is complete. There is no extra database work —
`impliedAssets` already fetched every row and cut the slice in Go afterwards.

That change made the id lists unbounded, so the five queries they feed are now
chunked at 500. Both drivers refuse past a limit and at different points —
measured, SQLite at 32,767 bound parameters and pgx at 65,536 — each surfacing as
an opaque driver error with nothing an operator can act on.

### `"1.-5"` was €0.95

`strconv.ParseInt` accepts a leading sign, so the fraction `-5` parsed to 5 and
was *subtracted* from the whole part. Silently wrong money, no error, and nobody
would ever look. The fraction is digits-only now. The overflow cap also only
covered the stored value and not `AnnualMinor()`'s ×12, so a figure that fit the
column wrapped negative a page later; it is `MaxInt64/12/100`.

### Two schema divergences the constraint test could not see

- **`amount_minor INTEGER` is `int4` on PostgreSQL** — a €21,474,836.47 ceiling —
  and 64-bit on SQLite. A data-centre acquisition above it saved on one engine and
  returned `integer out of range` on the other, which `translateWriteErr` does not
  recognise, so it reached HTTP as a 500 carrying a raw driver error. Now `BIGINT`.
- **`id TEXT PRIMARY KEY` is nullable on SQLite.** Only an `INTEGER PRIMARY KEY`
  is implicitly `NOT NULL`; SQLite treats NULLs as distinct in a unique index, so
  several such rows can coexist, each invisible to every statement keyed on
  `WHERE id = ?` while still counted by every aggregate.

`TestConstraintNamesMatchAcrossEngines` compares a flat *set* of CHECK names, so
it can see neither nullability, nor which table a constraint sits on, nor an
expression. `TestColumnShapesMatchAcrossEngines` is the missing half — and it
immediately showed the nullable-primary-key problem was **estate-wide, 30
columns**, not the four in `00012`. Migration `00013` fixes all of them.

Measured on the pinned driver: `ALTER COLUMN … SET NOT NULL` succeeds, *validates
existing rows*, needs no rebuild, and `DROP NOT NULL` reverses it. The stored DDL
text does not change — the constraint is enforced without appearing in
`sqlite_master`, which is why the shape test asks `pragma_table_info` rather than
parsing DDL. `00013`'s down path is deliberately asymmetric: SQLite drops the
constraints because there the previous state genuinely was nullable, and
PostgreSQL does nothing because there a primary key always implied `NOT NULL` and
the engine refuses to pretend otherwise (`SQLSTATE 42P16`).

### Smaller, all real

- `ListProjects` was the one list filter without `LOWER()`. SQLite's `LIKE` is
  case-insensitive for ASCII and PostgreSQL's is not, so a search that worked on
  the demo returned nothing on PostgreSQL.
- `UpsertVocabularyTerm`'s required-field checks returned a wrapped sentinel
  rather than a `*ValidationError`, so the handler answered 422 with a bare
  string instead of the re-rendered form — the template's per-field hooks were
  unreachable from that path.
- `retireCost` selected on the cost id alone, ignoring the parent in the URL: an
  admin could retire a cost on one asset through another asset's URL, with a
  correct `change_log` entry and an incorrect redirect.
- LIKE metacharacters were unescaped in four filters. Parameterised throughout,
  so never an injection — but an asset tag containing `_` matched any character.
- `dynamicTargetAllowlist` exempted a **file**, so any future dynamic-table write
  added to one of the four would inherit the exemption silently. Each now carries
  an exact expected count; adding a fifth write to `costs.go` fails until somebody
  says so.
- Migration `00010` would abort mid-flight on an empty application code, and gave
  a *retired* service an active `owns` link — invisible in the UI while occupying
  the single owner slot, so nobody could own that service and no screen said why.

### Noted, not acted on

The test PostgreSQL container is Alpine/musl, which does not implement locale
collation and therefore byte-orders. Every `ORDER BY <text>` agrees across engines
*here* and would not on a glibc PostgreSQL. Nothing today depends on it — the
orderings are display-only and every truncation decision re-sorts in Go — but the
comments claim collation-independence that this environment cannot falsify.
Pinning the container to glibc, or creating the test database `LC_COLLATE=C`,
would put the claim under test.

---

## 2026-07-31 — Teams and roles, and the free text they replace

Migration `00009`'s own header named this gap: *"`owner_team` is free text on four
tables with no referential integrity at all."* This closes it. `team` becomes a
first-class entity, `responsibility_role` a vocabulary, and the four free-text
columns are gone.

### Two axes, deliberately separate

`team_id` says **who** looks after a thing; `manager_role` says **in what
capacity**. A team without a role is "the platform team's box, nobody has said
who in it" — common, and fine. A role without a team is **rejected**: it claims a
capacity is held and refuses to say by whom, which reads as recorded when it is
not.

Both optional, because a small estate where everyone looks after everything gains
nothing from filling them in, and a field people are forced to complete is a field
full of noise. Absent renders as *"nobody recorded"* rather than a blank cell — a
gap somebody has not filled in and a gap nobody noticed look identical otherwise.

### Why team is a table and role is a lookup

A team is an organisational fact with a life: it forms, owns things, is disbanded,
and its rows must survive that as `retired` so `change_log` keeps resolving. Every
lookup in this schema is a closed vocabulary whose rows are never retired, so a
team is not one. A role is the opposite — descriptive, nothing branches on its
value, and an estate wanting `dpo` should add one as data. The same line migration
`00004` drew between `service_kind` and `availability`.

**Retiring a team does not clear it from what it looked after.** A disbanded team
still named by live assets is the estate saying "this was theirs and nobody has
picked it up", which is a finding; nulling the column would erase the question
along with the answer, and silently.

### Role is documentation, not authorization

"Who can manage it" means who to call, not who the application will let through.
Nothing reads `manager_role` to decide anything and `authz.CanWrite` does not
consult it. That separation is the remit — this system presents state. When LDAP
group roles land this column is where they will look, which is an argument for
recording it accurately now and none at all for enforcing it yet.

### Teams, never people

No individual is named anywhere. `contact_ref` holds a group address, a ticket
queue or a channel; the application cannot tell that from a person's mailbox and
does not try, so the rule lives in the field hint, the help topic and
`docs/AUDIT.md` — exactly as `identity.secret_ref` is documented to hold a path
and never a secret. A CMDB kept for the life of an estate, with an append-only
`change_log`, must carry nothing anybody could ask to have erased. That is the
same argument that made `change_log.actor` an opaque id.

A test asserts the warning is on both the create and the edit form. If it ever
disappears, the only thing keeping personal data out of this database has
disappeared with it.

### The four columns are dropped and NOT backfilled

That is a choice, not an oversight. A backfill has to invent an id per distinct
string, and this repository generates every id as a UUIDv7 in Go and never in SQL
— a rule worth more than the contents of a fixture. Building the two-phase runner
that would let Go do it between two migrations is machinery this POC does not
need: the only estates in existence are the test fixture and a demo database
rebuilt on every deploy, and the seed creates the six teams properly. A deployment
with real data would need that second phase, and would be past the point where
this file calls a destructive migration free.

### The fixture

Six teams, each with a group contact. `haproxy-edge` is now looked after by
**nobody**, which sharpens a finding the fixture already made: no project owns the
edge either. The service every partner order crosses has no team to escalate to
and no budget holder, and both pages say so plainly.

---

## 2026-07-31 — The teams review, and two bugs that made the feature unreachable

Three reviews over the teams commit. The security and code-quality findings are
below; the database review found the two that mattered most, and both were mine,
both from a mechanical find-and-replace.

### The team picker was empty on every page that could set one

`asset_list.html` and `service_list.html` build their form context with an
explicit `dict`, and both omitted `Teams` and `Roles`. `html/template` resolves a
missing key to the zero value without erroring, so `{{range .Teams}}` iterated
nothing and every picker rendered with only its `—` placeholder.

**No human could set a team on an asset or a service at all.** Only the seeder
wrote those columns. Every test passed, because they all read seeded data or the
422 path — which passes a real struct and therefore works. The test that now
guards it counts `<option>` elements, because "the page contains a team code"
would also pass on a page that lists teams somewhere else entirely.

### Every project's search body was a raw UUID

Renaming `owner_team` to `team_id` across the codebase turned
`Body: strDeref(p.OwnerTeam)` into `Body: strDeref(p.TeamID)` — a UUID indexed as
searchable text. `indexAsset` and `indexService` correctly *dropped* the team from
their bodies in the same commit; only projects substituted it.

Two consequences, both reproduced against the demo database. The team name stopped
being findable through its project, which is the one thing the old free text
bought. And because UUIDv7 is time-sortable, every project written in the same
millisecond shares a leading token, so FTS5 matched all three on the fragment
`019fb8d6` and skewed bm25 for every other document. The body is now empty; the
team is its own searchable entity.

### Index shape, measured

`00014` indexed `(team_id)` alone, but every count in `teamSelect` pairs
`team_id = t.id` with `lifecycle <> 'retired'`. Measured on a 200,000-asset
estate: **112 ms and 88,687 buffer hits to render 27 rows** — roughly 693 MB of
buffer traffic. With `lifecycle` as a second column, 19.8 ms and 311 buffers with
zero heap fetches; SQLite 165 ms → 11 ms. Migration `00016` reshapes three of the
four. The partial `WHERE team_id IS NOT NULL` predicate *is* usable with a bound
parameter — both planners prove the implication — so it stays.

`idx_identity_team` is dropped outright: `identity.team_id` is written once by the
seeder and read by nothing, so the index cost write amplification and served no
query.

And `responsibilityOptions` was calling the full three-subquery `ListTeams` to
populate a `<select>` that renders two strings per row — on every asset form,
every service form and every project list. `TeamOptions` costs 1 buffer.

### An index could vanish from one engine with the whole suite green

The reviewer proved it by deleting `idx_service_team` from the PostgreSQL half and
watching everything pass. `TestColumnShapesMatchAcrossEngines` compares columns;
`TestConstraintNamesMatchAcrossEngines` compares CHECK names; nothing compared
indexes. `TestIndexesMatchAcrossEngines` is the third guard, and it immediately
found the two pg_trgm indexes that legitimately exist on PostgreSQL only — the
sanctioned FTS5-versus-trigram split — which are now an explicit allowlist entry
rather than a silent pass.

### Smaller, all real

- `manager_role` was the only lookup-backed column not checked by
  `requireVocabulary`, so an unknown value reached the foreign key and SQLite
  reported it as `FOREIGN KEY constraint failed` with no column — a bare 422 with
  nothing highlighted and the form contents lost.
- `contact_ref` reached the append-only `change_log` unredacted (security review).
  Correcting the mistake the hint warns against recorded the personal value a
  *second* time, as the `old` side of the diff. Redacted globally like
  `secret_ref`; the team row keeps the real value because that one is correctable.
- A degraded picker could silently clear a team and attribute the removal to a
  human. `submittedString` distinguishes "the operator cleared it" from "the field
  never rendered".
- A retired team stayed searchable by its contact with no screen offering to
  change it.
- The service form lost its picker selections on a 422 — the regression that bites
  hardest on the one rule this feature added.
- `TestTeamsShowWhatTheyAreAnswerableFor` passed with **both** `TeamID` filters
  deleted from the store, because an unfiltered list contains the rows it looked
  for. Rewritten with absences in both directions; it now fails on five
  assertions under that mutation. Third time this session.

### Recorded, not fixed

The list filters match **ASCII only**. `lower` folds bytes `A-Z` to match SQLite's
`LOWER()`, while PostgreSQL's is locale-aware and folds the whole string — so the
two sides fold asymmetrically there and `ÖKO` finds `Ökonomie` on SQLite and not
on PostgreSQL. Swapping in `strings.ToLower` just moves the failure to the other
engine; a folded shadow column would fix it and is not worth a column yet. The
comment above the filter claimed the `LOWER`-on-both-sides trick made the engines
agree. It does not, and now says so.

---

## 2026-07-31 — TLS certificates, and the audit entry that recorded nothing

The second half of the certificate work: handlers, screens, seed and tests. The
schema and store landed with the teams review.

### Certificates are the missing half of the expiry report

Hardware support lapses on a five-year cycle; a certificate lapses every ninety
days, and its expiry is an immediate outage rather than a procurement
conversation. `not_after` is an EOL date in every way that matters, so
certificates join the report that already answers "what runs out, who owns it,
what rides on it" — and for a certificate, *what rides on it* is how many places
it is deployed. One wildcard on four load balancers is four outages.

The report now counts undated certificates separately, because a blank there is
worse than a date in the past: **every** certificate has an expiry, so a blank
means nobody looked rather than that the question does not apply.

### Wildcard matching happens in Go, and matches what a client does

`*.example.com` covers `orders.example.com`, and does **not** cover
`example.com` or `a.b.example.com`. That is RFC 6125 and, more to the point,
what every TLS client actually does. A CMDB answering more loosely than the
clients do would tell somebody they were covered while they were about to serve
a name error — and looser is the easy implementation: a suffix match or a `LIKE`
gets all three cases wrong in the direction that reassures.

It is Go rather than SQL because encoding that portably in SQL would be a worse
lie than putting it where it can be tested. The names are a child table so the
question is a real query, which is the whole reason `certificate_san` exists.

### Never key material

`key_ref` holds a path or a vault reference and is redacted from `change_log`
like `secret_ref`. The public certificate body is not stored either — it is bulk
with no query value, and a column that accepts certificate-shaped text is where
a private key eventually gets pasted.

### The audit entry recorded nothing, and only mutation found it

`certificateAudit` embedded `*domain.Certificate` rather than
`domain.Certificate`. It compiled, it ran, and it produced an audit entry
containing **only the SANs** — `snapshotJSON` walks db-tagged fields and does not
follow an embedded pointer, so every column of the certificate was absent from
the permanent trail.

Two tests passed over it. `TestChangingTheNamesIsAudited` passed because the SANs
are on the outer struct. `TestAKeyReferenceNeverReachesTheAuditTrail` passed **for
the wrong reason entirely**: the key was absent because *everything* was absent,
and deleting the redaction rule did not make it fail. That is what exposed it —
mutating a rule and noticing the test did not care.

An audit entry that records nothing is worse than no entry, because it looks like
coverage. `TestTheCertificateSnapshotIsComplete` now asserts every column by
name, and both mutations fail it.

`assetAudit` and `dependencyAudit` embed by value and had the answer all along;
`dependencyAudit` also sorts its set before joining, so re-submitting the same
members in a different order is not reported as a change. The certificate audit
now does both.

### The fixture makes five points

A wildcard deployed in four places; a partner certificate expiring in eleven days
that **nobody** is recorded as renewing — on the edge no project owns and no team
looks after, so the estate's sharpest finding now has three faces; an internal
certificate that lapsed six weeks ago and is still deployed, which is what an
internal CA with no automation looks like; one ordinary well-managed certificate,
so the alarming rows are not the only rows; and one with no expiry recorded at
all.

### Small things worth keeping

The SAN field accepts a paste straight out of `openssl x509 -text` — newlines or
commas, `DNS:` prefixes stripped — because that is how anybody actually fills it
in. Fingerprints are normalised to lowercase hex with separators removed, so the
same certificate pasted from openssl, a browser and a console is one row rather
than three; a length that is neither 40 nor 64 characters is rejected, because a
truncated fingerprint looks like an identity and is not one.

One bug found by probing rather than review: the first fingerprint normaliser
used `strings.Map` with a negative sentinel to mark invalid characters.
`strings.Map` **drops** a negative return rather than inserting one, so `zz:qq`
came back as the empty string and was then treated as no fingerprint at all.
Silently discarding what somebody typed is the worst of the three possible
behaviours.

---

## 2026-07-31 — The certificate reviews: a paste that would have been permanent

Three reviews over the certificate feature. The security one found the thing this
feature's own migration header had named as the threat and then left open.

### The SAN field would store a pasted private key, forever

`splitNames` accepted any token. Paste a PEM block into the "also covers"
textarea and each line became a `certificate_san` row, FTS-indexed, **and** folded
into the audited value — so it landed in the append-only `change_log`, where it is
unerasable by design. The rows and the index are correctable by an edit; the
audit row is not.

Verified before fixing: a pasted key split into seven stored names.

Names are now validated as hostnames or IP addresses — a leading `*.` label
allowed and nothing else — with the free-text fields capped (`issuer` 256,
`serial` 128, `key_ref` 512) and control characters and NUL refused. NUL
specifically, because PostgreSQL rejects it in TEXT and SQLite does not, so a
NUL-carrying value is a row that saves on one engine and 500s on the other.

**All or nothing.** My first fix kept the tokens that parsed and rejected the
rest — and my own test caught that a PEM block splits into armour lines that fail
and base64 lines that are, letter for letter, valid single-label hostnames. Half a
key stored is still a key stored. A submission containing anything invalid now
stores nothing at all.

The names also moved onto the `Certificate` struct rather than travelling beside
it as a second store parameter, because that parameter was the seam through which
an unvalidated name could reach the database.

### `IP Address:2001:db8::1` became `ip` and `2001:db8::1`

Cutting at the first colon of any token mangled every value that legitimately
contains one: a bare IPv6 address `2001:db8::1` was stored as `db8::1`, and
`host:443` as `443`. A wrong stored name makes "what covers this address" answer
wrongly, and silently. Only known prefixes are stripped now — and per *line*
before per *name*, because openssl's `IP Address:` carries a space that
whitespace-splitting tore in half.

### Fabricated audit entries

`undeployCertificate` logged unconditionally, so POSTing the retire route with two
ids that had never been deployed together wrote a removal into the permanent trail
and reported success — a fabricated fact, which is worse than a missing one. It
now returns `ErrNotFound` on zero rows affected. `deployCertificate` likewise
wrote a fresh entry every time an unchanged deployment was re-submitted; it now
compares before and after, which is the rule `logUpdate` enforces everywhere else
and that a hand-built diff string quietly opts out of.

### The root cause of the empty-audit bug is closed

`auditFields` recursed only into anonymous **struct** embeds, so an anonymous
**pointer** embed was silently skipped — the shape that made `certificateAudit`
record nothing. Only certificates had a completeness test, so the next audit
struct could have regressed unnoticed. It now panics with a message saying to
embed by value. A panic rather than a silent deref because this is a struct
literal in one package, resolved at compile time, and the failure it replaces is
an audit trail that looks complete and is empty.

### Smaller, all real

- Certificates never called `requireRole`, reintroducing in one file the bug fixed
  in three others the same day: an unknown role reached the foreign key, and
  SQLite reports that with no column name, so the operator got a bare 422 with
  nothing highlighted and a lost form.
- The expiry report's "what this report cannot see" callout was gated on
  `or UndatedAssets UndatedServices`, ignoring certificates entirely — and the
  top "no date recorded" stat summed only those two. My test passed by
  coincidence: the fixture leaves `hv-03` undated, which kept the `or` true.
- `TestAKeyReferenceNeverReachesTheAuditTrail` iterated a slice without asserting
  it was non-empty, so it passed against a store that wrote no audit entry at all
  — the fifth test-that-cannot-fail this session, and the review found it in the
  very file whose sibling test exists *because* of that pattern.
- The guard test exempted dynamic INSERTs, defending deletion of history but not
  fabrication of it: an assembled `INSERT INTO <var>` aimed at `change_log` would
  forge audit rows with the suite green. It now counts INSERTs, which immediately
  corrected two stale per-file budgets.

### Decided, not changed

`key_ref` renders to every authenticated session, including read-only, while
`secret_ref` appears in no template at all. The asymmetry is deliberate: a
certificate's key location is what an operator needs during a renewal, and the
authorization model today is read versus read-write with no finer grain. If cost
visibility ever gets its own permission, this belongs behind the same one.

---

## 2026-07-31 — The database review: a guard that could not protect its own commit

### `00016`'s deliverable was unprotected by the guard written in the same commit

`TestIndexesMatchAcrossEngines` compared index **names**. `00016` reshapes three
indexes from `(team_id)` to `(team_id, lifecycle)` — and the names do not change.
A database review reverted the shape on one dialect half and the entire suite
stayed green. I reproduced it before believing it.

The guard's own comment was honest about the limitation — *"names are compared,
not definitions… the failure mode this exists for is a whole index going missing
rather than one drifting a column"* — but the very next migration's failure mode
was the other one. **A guard that cannot protect the commit that adds it is not
yet a guard.**

It now compares the tuple `(name, table, ordered columns, partial)`, and includes
primary keys and unique constraints, which were excluded from both halves before
— so a `UNIQUE` could vanish from one engine and only a functional test that
happened to exist would catch it. Constraint-backed indexes are keyed by
table+columns rather than by name, because SQLite calls them `sqlite_autoindex_*`
and PostgreSQL names them after the constraint.

Two mutations that previously passed now fail with the shapes named: the
`(team_id, lifecycle)` revert, and dropping `WHERE not_after IS NOT NULL` from
`idx_certificate_expiry`.

Known gap, stated rather than pretended away: two constraints with the same name
and different expressions still pass. Normalising PostgreSQL's
`pg_get_constraintdef` against SQLite's raw CHECK text is real work and nothing
has diverged that way yet.

### Migration `00017`: two indexes added, three removed, all measured

Every read of the two link tables pairs `certificate_id = ?` with
`lifecycle = 'active'`, and lifecycle was not in the primary key. Measured on
60,000 certificates and 180,000 deployments:

| | PostgreSQL | SQLite |
|---|---|---|
| `expiryCertificateReach` before | 590 ms | 87 ms |
| after `(certificate_id, lifecycle)` | **136 ms** | 68 ms |

The reach loop was costing roughly **seven times the rest of the expiry report put
together**. No code change; both engines get an index-only scan.

Three indexes go: `idx_certificate_san_name`, `idx_certificate_asset_asset` and
`idx_certificate_service_service` index the *non-leading* column of each table,
and nothing queries in that direction — the host filter runs in Go because a
wildcard match is not a LIKE, search resolves through `search_index`, and no page
looks up certificates from an asset. They were built for reverse lookups that
were never written. An index nothing uses is a claim about a query that does not
exist.

`idx_certificate_team` is deliberately **not** reshaped the way `00016` reshaped
its three. Measured: identical plan, identical buffers. The teams case gained
because `teamSelect`'s subqueries are COUNTs answerable from the index alone;
`certificateSelect` is `SELECT c.*` and must visit the heap regardless. Applying a
previous migration's conclusion mechanically is how a schema fills with indexes
nobody measured.

### The ordering agreement in CI is an artefact of the container

`ListCertificates` used `ORDER BY … subject_cn` and did not re-sort in Go. The
PostgreSQL container is Alpine/**musl**, which implements no locale collation, so
`en_US.utf8` degenerates to byte order and happens to match SQLite. On a glibc or
ICU-collated database the list orders differently. `expiry.go` already states the
rule this query broke — *"which of two rows sharing a date comes first must not
depend on the server's collation"*. Now sorted in Go, along with the SAN display
order.

### Smaller

- `expiryCertificateReach` had **no test at all** — `expiry_test.go` contained the
  word "certificate" zero times. Unexercised: the `ServiceCount` reuse, the
  `SUM(COUNT(*))` that returns `numeric` on PostgreSQL and INTEGER on SQLite, and
  the two-IN-list argument ordering its own comment worries about. The new test
  uses asymmetric counts (two assets, one service) so transposing the halves
  changes the answer; mutating the argument order fails it.
- `TestColumnShapesMatchAcrossEngines` iterated the SQLite side only, so a column
  added to the PostgreSQL half was invisible to it — caught previously by an
  unrelated boundary test, by accident. Now symmetric.
- The `ON DELETE CASCADE` on `certificate_san` is the only cascade in the schema
  and is very nearly dead: nothing deletes a certificate, and the two link tables
  use NO ACTION, so it can fire only for a certificate deployed nowhere. Kept as a
  data-repair backstop, documented so no reader concludes that deleting a
  certificate is something the application does, and now pinned by a test — the
  review showed removing it from one half was invisible.
- `certificate.fingerprint` is UNIQUE and nullable, and both engines treat NULLs
  as distinct, so several certificates with no fingerprint is a legitimate state.
  PostgreSQL 15+ offers `UNIQUE NULLS NOT DISTINCT`; the migration now says it
  must never be used, because SQLite has no equivalent.

### Recorded, not fixed

Searching by `issuer` is **ASCII-only** and the engines diverge beyond that:
`lower` folds A–Z to match SQLite's `LOWER()`, while PostgreSQL's is locale-aware
and folds the whole string, so the two sides fold asymmetrically there. Measured —
issuer `Bundesdruckerei ÖCA` searched for `ÖCA` matches on SQLite and not on
PostgreSQL. A subject is a hostname and ASCII in practice; an issuer is a CA's
display name, and for an EU estate umlauts are ordinary. `strings.ToLower` does
not fix it, it moves the failure to the other engine. A folded shadow column
would, and is not worth a column yet.

---

## 2026-07-31 — The reverse view, and an index that came back shaped for it

A pass over the running demo to check every feature was actually reachable found
one real gap: **an asset or service page did not show the certificates deployed
on it.**

A certificate page already listed where it was deployed. The question during an
incident runs the other way — *this box is serving a certificate error, what is on
it* — and that direction did not exist. A team's page had the same shape: the
platform team renews two certificates and its own page did not say so.

### The index came back shaped for the query, exactly as promised

Migration `00017` dropped `idx_certificate_asset_asset` and
`idx_certificate_service_service` because nothing queried in that direction, and
said: *"if a reverse lookup is ever added, the index comes back shaped for it
rather than guessed at now."*

`00018` is that. The query the pages now run is
`WHERE asset_id = ? AND lifecycle = 'active'`, so lifecycle is the **second
column** — not absent, which is what the dropped indexes had and what would have
sent both engines to the heap for it. That is the same correction `00016` and
`00017` each had to make. Guessing the shape a year early would have produced the
wrong index *and* the appearance of having thought about it.

### The count test caught its own page changing

Adding a certificate column to the team list broke
`TestTeamCountsAgreeWithWhatTheTeamPageLists`, which reads the numeric cells and
expects a fixed number of them. That is the test working: the fix was to extend it
to four counts rather than to loosen it, so the new column is now covered by the
same list-versus-page agreement as the other three.

The reverse view's own test asserts absences in both directions — a certificate
deployed elsewhere must not appear, and a team that does not renew one must not
list it. Removing the `WHERE asset_id = ?` clause fails it.

---

## Cost attribution: allocated, not used — *2026-08-14*

**Cost is attributed on what a workload is ALLOCATED, not on what it uses.**

The full specification is `docs/COST-ATTRIBUTION.md`; this records the decision
and why, because it is the one that will be argued with.

**It is argued with because the other answer sounds better.** "Charge for what
they actually consume" is fairer in principle and is what a cloud provider does.
It was considered and rejected for this system, for reasons that are structural
rather than preference:

**Allocation is declared; usage is observed.** A person agreeing that a virtual
machine gets eight cores is an assertion someone made and can be held to. A
hypervisor reporting thirty percent CPU is telemetry — it changes constantly,
nobody decided it, and most readings repeat the last. `docs/AUDIT.md` separates
those two classes absolutely: observed state never writes a declared column, it
records transitions rather than heartbeats, and it arrives through
`RecordObservation` and nowhere else.

So a usage basis could never be a configuration flag that quietly repoints the
same column at a different number. It would be a second source of fact with its
own audit obligations, its own retention argument, and its own failure mode when
a collector goes silent. That is a work package, not a switch.

**A bill nobody agreed to is not defensible.** Allocation is what a person signed
up for; a slice computed from it can be explained in a meeting. A slice computed
from a fortnight of samples cannot be reproduced once the samples are pruned —
and this system prunes observed transitions by design.

### How the door stays open

The seam is **one function** taking a capacity figure and returning a slice,
indifferent to which class of fact produced the figure. Not an interface with a
single implementation, not a strategy registry. If keeping even that costs more
than slight indirection, collapse it: the second basis does not exist, and
speculative structure for it would be the mess this note exists to prevent.

### The part that must not be deferred

**Every computed slice records the basis it was computed on.** A switch that
silently changes what a number *means* produces two months of figures on one
basis, a flag flip, and a graph with a discontinuity nobody can explain a year
later. Stamping `basis` on the result costs nothing now and cannot be retrofitted
honestly — the old rows would have to be guessed at.
