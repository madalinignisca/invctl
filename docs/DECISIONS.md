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
