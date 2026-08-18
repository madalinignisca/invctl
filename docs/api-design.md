# Read-only inventory API — design (WP-A2)

Machine-facing, read-only, declared state only. The consumers named in the
roadmap are an Ansible dynamic inventory and an observability join; the surface
is built as a small set of collections plus one composed view, so a third
consumer arrives by adding a collection rather than by breaking one.

**No migration. No schema change.** This work package publishes existing tables
through a narrower shape than the UI sees. Every field in that shape is opted
in by hand, which is the whole reason the DTOs live in their own package.

---

## 1. What this is not

- **Not a write surface.** No POST, no PATCH, no DELETE, ever. `WP-A2` in
  `docs/ROADMAP.md` says "No write routes" and a guard test enforces it rather
  than trusting the sentence.
- **Not observed state.** Health, `state_since`, reporters and transitions are
  absent. The observability consumer is usually the same system that *reported*
  the health, so returning it would be circular; more importantly, keeping the
  two directions apart is what makes the surface easy to reason about — agents
  write observed, readers read declared, and neither can do the other's job.
- **Not money.** No price, no supplier, no tariff, no amortisation. A leaked
  read token exposes topology, not commercial terms.
- **Not personal data.** No actor, no team contact, no username. Invariant 5
  holds here by construction, because the fields are never assembled.
- **Not an outbound capability.** Every request is inbound. Invariant 9 and
  `TestNothingReachesOutOfThisProcess` are untouched, and no `dialAllowlist`
  entry is needed. This work package is not WP-G2.

---

## 2. The third principal

The codebase already separates two principal types and the separation is
enforced by the type system rather than by discipline: `authz.CanWrite` takes a
`*domain.AppUser`, so an `auth.Agent` cannot reach it even by mistake — it does
not compile.

`auth.Reader` is the third. It is not an `AppUser` and not an `Agent`, it holds
no token (the registry keeps a SHA-256 digest and nothing else), and it carries
no `domain.Actor` at all — a reader never writes, so it has no audit identity to
misuse.

**Two registries, two secrets, neither implying the other.** An
`INV_AGENT_TOKENS` token is refused on `/api/v1`; an `INV_API_TOKENS` token is
refused on `/observations`. This is the reason the read capability was not
simply added to the existing monitoring credential: a collector's token lives on
whatever box runs the collector, and merging the capabilities would mean one
leaked secret both falsifies health *and* exfiltrates the inventory.

### Configuration

Two variables, mirroring `loadAgentCredentials`, which uses three for a reason
that does not apply here (agents need a vocabulary; readers do not).

```
INV_API_TOKENS=ansible:<token>,grafana:<token>
INV_API_SCOPES=ansible:prod|staging,grafana:prod
```

- Every failure is a **startup** failure, with the credential id in the message.
  Skipping a credential that will not build means a client that authenticates
  today stops authenticating after a config edit, with nothing in the logs
  naming the edit.
- A duplicate id, a token named in `INV_API_SCOPES` but absent from
  `INV_API_TOKENS`, or an empty scope all refuse to start.
- **There is no wildcard.** `domain.NewEnvironmentScope` rejects an empty set
  with "there is no wildcard", because "a default that widens authorization is
  how a scope becomes decorative". A reader that should see everything
  enumerates every environment, and adding an environment later is therefore a
  deliberate act rather than a silent widening.
- When no credential is configured the routes are **not mounted at all**, exactly
  as the observations route is not. An estate that has no integrations should not
  be carrying the surface.

---

## 3. Scope: what a token may see

A token's `domain.EnvironmentScope` filters rows through `asset_environment`,
using **`AllowsAll`** — the same predicate observations use, not the more
permissive `AllowsAny`.

The write-side comment argues that an entity sits in a set of environments and a
reading about it is visible in all of them, so the most permissive environment
must not decide. Disclosure is the mirror image of that argument and it holds
the same way: `sw-core-1` sits in `{prod, dev}`, and a `{dev}` token that could
read it would learn the name, site, rack and addresses of a production-facing
device by way of its least sensitive membership.

The consequence is deliberate and must be documented for operators, because it
will look like a bug: **a `{dev}` token does not see boundary devices.** A
consumer that needs them declares `INV_API_SCOPES=x:prod|dev`. The escape hatch
is an explicit statement of intent, which is the point.

An asset in **no** environment is visible to nobody. That matches the existing
rule — "an entity in no environment is covered by nobody, which is a data gap
surfaced as a denial rather than an implicit allow" — and it means the API makes
such a gap visible rather than papering over it.

### Absence, not refusal

An out-of-scope entity is **absent from collections, and a fetch of its id
returns 404 — byte-identical to the 404 for an id that does not exist.** A 403
would confirm that the row exists, which is an existence oracle over the estate:
a `{dev}` token could enumerate ids and learn which ones name real production
assets without ever reading one.

This is genuinely hostile to debugging: a misconfigured `INV_API_SCOPES` is
indistinguishable from an empty estate to the client. The mitigation is on the
**operator's** side, not the client's — a scope miss writes a security event
naming the credential, the entity type and the scope, so the answer is in the
server log even though it can never be in the response.

---

## 4. Surface

All `GET`. All under `/api/v1`.

| Route | Returns |
|---|---|
| `GET /api/v1/assets` | collection, `?after= &limit= &env= &kind= &lifecycle=` |
| `GET /api/v1/assets/{id}` | one asset, or 404 |
| `GET /api/v1/services` | collection, `?after= &limit=` |
| `GET /api/v1/services/{id}` | one service, or 404 |
| `GET /api/v1/addresses` | collection, `?after= &limit=` |
| `GET /api/v1/environments` | collection, unpaginated (bounded and small) |
| `GET /api/v1/ansible` | composed view, unpaginated |

The single-resource routes exist for the observability join: a metric carries an
id in a label and the consumer resolves it to a name and a placement without
paging the estate. They are the routes §3's 404 rule is about.

Collections return:

```json
{"data": [ ... ], "next": "<cursor>"}
```

`next` is `null` on the last page.

### Filters

`env` and `kind` narrow **within** the token's scope and can never widen it: the
scope predicate is applied first and `env` selects a subset of what survived.

**`env` is validated against the token's own scope, not against the estate.**
`?env=X` where X is in the credential's scope filters and returns 200; `?env=X`
where X is not — *whether or not X exists* — is a **400** reading "env is not in
this credential's scope".

This replaces an earlier rule that returned an empty collection for an
out-of-scope `env` "because the token's scope is not the client's business". That
rationale does not survive `/api/v1/environments` existing on the same surface:
the scope **is** published to the client, in full, by another route, so refusing
by scope tells it only what it can already compute offline. Three things follow,
and all of them are improvements:

- It closes an existence oracle. Validating against the estate meant an unknown
  code returned 400 while a real out-of-scope one returned 200-empty, so any
  token could enumerate the environment vocabulary by dictionary. The
  namespace is a dozen short human-chosen words, so that leak is total rather
  than targeted — and it becomes a genuine disclosure the day an environment is
  named after a client (`acme-prod`, `dmz-payments`).
- It says nothing false. The message is equally true whether X exists or not,
  where a 400 claiming "unknown environment" for a real one would be a lie.
- **It fixes a §6 violation in the previous rule.** A consumer whose
  `INV_API_SCOPES` was edited out from under it received a plausible,
  well-formed, empty inventory with a 200 and no signal — a value arrives,
  cannot be used as the caller meant it, and is replaced by something
  indistinguishable from a legitimate answer. That is the exact shape §6 exists
  to refuse, and the earlier rule mandated it.

It is also cheaper: validating `env` against the estate required a database
lookup and threaded a store handle into filter parsing; validating against the
scope is an in-memory check against a slice the request already holds.

A filter *value* that is not a real asset kind or lifecycle is a **400**, not an
empty collection, for the same reason.

### Pagination

Keyset on `id` alone, ascending. IDs are UUIDv7 and therefore time-sortable, so
a single-column cursor gives creation order without a second column and without
a tiebreak. This is *only* correct because of the UUIDv7 choice, so the
dependency is stated in a comment and pinned by a test — a future non-v7 id
would break page ordering silently, which is the worst way for it to break.

The external shape matches the change log's — an opaque cursor string and a link
to the next page — while the internals are simpler than `store.ChangeCursor`'s
`{At, ID}` pair, which needs both because `change_log` is ordered by a timestamp
that can repeat.

`limit` defaults to 100 and is clamped to 500. A clamp rather than a refusal
because the substitution is documented, in-band and visible in the length of the
response the client just received; see §6 for why a cursor gets the opposite
treatment.

### Asset DTO

```json
{
  "id": "01924e5a-...",
  "name": "vm-db-2",
  "kind": "vm",
  "lifecycle": "active",
  "environments": ["prod"],
  "site": "dc-1",
  "rack": "r14",
  "addresses": ["10.2.0.14"],
  "services": ["billing-api"]
}
```

There is no `role`. An earlier revision of this document showed one, sourced
from `asset.manager_role` and described as a capacity like `"database"`.
`manager_role` is an FK into `responsibility_role` — `owner`, `operator`,
`approver`, `oncall`, `custodian`, `vendor` — so it can never hold that value,
and migration 00014 is explicit that a role without the team it qualifies is
not expressible. `kind` already carries `server`/`vm`/`switch`, and
`services` carries the functional grouping the Ansible view needs. Publishing a
real functional-role column later is additive and non-breaking.

Retired assets are excluded unless `?lifecycle=retired` asks for them. Soft
delete means the rows are still there, and an inventory that silently targets
decommissioned hosts is worse than one that omits them.

**DTOs are hand-written structs in `internal/api/dto.go` and are never store
structs.** Store structs are shaped by the schema; DTOs are shaped by the
contract. If they were the same type, every migration would be a potential
breaking change to a published surface and nobody would notice until a consumer
broke. Separated, adding a column is inert by default and publishing a field is
a deliberate edit.

### The other DTOs

```json
// service
{"id": "...", "code": "billing-api", "name": "Billing API", "kind": "api",
 "lifecycle": "active", "environments": ["prod"], "criticality": 1,
 "assets": ["vm-db-2"]}

// address
{"id": "...", "address": "10.2.0.14", "family": 4,
 "asset": "vm-db-2", "asset_id": "...", "environments": ["prod"]}

// environment
{"id": "...", "code": "prod", "name": "Production",
 "role": "production", "in_scope": true, "criticality": 1}
```

An address is scoped by the environments of the asset it belongs to; an address
with no asset is visible to nobody, by the same rule as §3.

### The Ansible view

Groups are formed from every environment an asset is in, its kind, and each
service it runs. An asset in two environments appears in two groups, because it
is in two environments.

Hosts are limited to `server`, `vm` and `hypervisor` **with at least one
address**. A rack, a patch panel or a PDU is a real asset and not a thing
Ansible can connect to; listing it produces an inventory that fails on first
use.

```json
{
  "_meta": {"hostvars": {
    "vm-db-2": {
      "invctl_id": "01924e5a-...",
      "invctl_site": "dc-1",
      "invctl_kind": "vm",
      "ansible_host": "10.2.0.14"
    }
  }},
  "env_prod":        {"hosts": ["vm-db-2"]},
  "env_shared":      {"hosts": ["vm-db-2"]},
  "kind_vm":         {"hosts": ["vm-db-2"]},
  "svc_billing_api": {"hosts": ["vm-db-2"]}
}
```

Group names are sanitised to Ansible's identifier rules (lower case, non
alphanumerics to `_`) and prefixed by dimension, so a service named `prod`
cannot collide with the `prod` environment. A collision after sanitisation is a
**500 with the two source names in the log**, not a silent merge of two groups
into one — a merged group silently widens the target set of every playbook that
uses it.

A host name claimed by two assets is refused the same way: a **500 with both
asset ids in the log**. `asset.name` is unique only among live siblings, so
`vm-app-1` under two different hypervisors is legal, and Ansible keys
`hostvars` by name — merging them would resolve one environment's host to the
other's address and connect successfully, which is the group collision with a
worse blast radius.

`ansible_host` is `addresses[0]` after a plain string sort, so `10.20.10.11`
precedes `10.20.10.9`. Documented rather than made clever: a numeric or
role-aware choice is a policy this work package does not have, and an
undocumented arbitrary one is worse than a stated arbitrary one.

Only `lifecycle = 'active'` assets appear.

---

## 5. The guard

`RequireReader` mirrors `RequireAgent`, including the order of its checks,
because that order encodes the risks:

1. **A browser session on this route is refused outright.** Either a confused
   client or an attempt to have one principal type ride the other's credentials.
2. **Bearer token, or its absence.** No token is 401.
3. **Rate limit, applied after identity**, so a flood by one credential cannot
   deny the others. The unauthenticated bucket is consumed on failure only, so a
   working client never touches it and a brute-force attempt cannot lock one out.

Every refusal is a security event, never a silent drop. `Cache-Control: no-store`
on every response.

### CSRF

`/api/v1/*` is **not** added to the CSRF exemption list. It does not need to be —
nosurf ignores safe methods, and every route here is a `GET`.

`routes.go` already anticipated this work: the exemption for the observations
route is built as `middleware.ExactPath`, whose implementation calls nosurf's
`ExemptPath` and never `ExemptGlob` or `ExemptRegexp`, with a comment saying
that is "what stops the planned `/api/inventory` from inheriting it for free".
A test asserts the exemption list still contains exactly one entry.

---

## 6. Errors

Through `render.JSONError`, which already exists for the machine surface.

| Status | When |
|---|---|
| 400 | a query string that is not valid URL encoding, malformed cursor, unparseable `limit`, unknown filter value |
| 401 | no bearer token, unknown token, or a browser session present |
| 404 | unknown id, **and** an id outside the token's scope |
| 429 | rate limited |
| 500 | the estate cannot be rendered into the contract: an Ansible group-name collision, or a host name claimed by two assets. Both sources named in the log, neither in the body |

`r.URL.Query()` is not used anywhere in `internal/api`. It calls
`url.ParseQuery` and discards the error, so `?after=%zz` drops the pair on the
floor and the caller gets page one with a 200 — the same silent substitution
in a place the package's own AST guard cannot see, because the discarded error
is inside `net/http`. One helper parses the query string, refuses a parse
error with a 400, and never echoes the raw query back.

A well-formed cursor is also **canonicalised**, not merely validated:
`uuid.Parse` accepts braced, `urn:uuid:`-prefixed, unhyphenated and upper-case
forms, and the cursor is compared as TEXT against hyphenated lower-case ids, so
keeping whichever spelling arrived skipped or repeated a page with a 200.

**A malformed cursor is a 400 and never a silent restart.** `ParseChangeCursor`
treats a bad cursor as "no cursor, therefore the first page", which is right for
a human clicking a link in a browser and wrong here.
`TestNoParseErrorIsDiscarded` exists to refuse exactly this mechanism — "a value
arrives, cannot be used, and is silently replaced by something
indistinguishable from a legitimate answer" — and a client paginating with a
corrupted cursor would restart at page one forever, re-ingesting the first
hundred assets and never reaching the rest, with a 200 every time.

---

## 7. Tests

Table-driven, against the fixture estate rather than mocks, on both engines via
`make test`.

Guard tests, in the idiom the codebase already uses for its invariants:

- `TestNoAPIRouteIsAWriteRoute` — every registered `/api/v1` pattern begins `GET `
- `TestTheAPINeverExposesMoney` — reflect over the DTOs and refuse any field
  matching cost, price, amount, supplier, tariff
- `TestTheAPINeverExposesPersonalData` — no actor, contact, email, username
- `TestTheAPINeverExposesObservedState` — no state, reporter, `state_since`
- `TestAnAgentTokenIsRefusedByTheAPI`
- `TestAnAPITokenIsRefusedByObservations`
- `TestOnlyObservationsIsCSRFExempt`
- `TestAMalformedCursorIsRefusedNotIgnored`
- `TestAnOutOfScopeAssetIsIndistinguishableFromAnAbsentOne` — same status, same
  body, for an out-of-scope id and a fabricated one
- `TestABoundaryAssetIsHiddenFromAPartialScope` — `sw-core-1` in `{prod, dev}`
  is absent for a `{dev}` token and present for a `{prod, dev}` one
- `TestPageOrderDependsOnUUIDv7` — pins the single-column cursor's assumption
- `TestAGroupNameCollisionIsRefused`
- `TestTheAPIRequiresNoOutboundCapability` — covered by the existing estate guard

Golden-file JSON shape tests for every DTO and for the Ansible view, so a change
to the published contract appears as a diff in review rather than as a broken
consumer.

---

## 8. Out of scope for this work package

Write routes; cost fields; observed state; a change-log endpoint; an OpenAPI
document; any UI for issuing or revoking tokens. Revocation is an edit to the
unit file and a restart, which is the same operational story the monitoring
credentials already have.

## 9. Documentation

`docs/API.md` for consumers — the routes, the shapes, the scoping rule and the
worked `INV_API_SCOPES` example for boundary devices. A CHANGELOG **Added**
entry. The roadmap marker for `WP-A2` moves to DONE only once both engines are
green.
