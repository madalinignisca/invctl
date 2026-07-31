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
