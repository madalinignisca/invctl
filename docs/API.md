# Read-only inventory API

A machine-facing, read-only view of declared state, under `/api/v1`. Built for
an Ansible dynamic inventory and for an observability system joining a metric
label back to a name and a placement.

**Not a write surface.** No route here creates, edits or retires anything —
every route is `GET`. **Not observed state.** Health, `state_since`, reporters
and transitions never appear; this is the estate as somebody declared it, not
as something reported it. **Not money, not personal data.** No price, no
supplier, no actor, no team contact.

---

## Configuration

Two environment variables. There is no third: unlike the monitoring
credentials (`INV_AGENT_TOKENS`), a reader needs no vocabulary — it never
writes an observation, so there is nothing to map a reporter's words onto.

```
INV_API_TOKENS=ansible:<token>,grafana:<token>
INV_API_SCOPES=ansible:prod|staging,grafana:prod
```

`INV_API_TOKENS` is `id:token` pairs, comma separated. `INV_API_SCOPES` maps
each id to the pipe-separated list of environment codes that credential may
read.

**There is no wildcard.** A credential enumerates every environment it may
read. A reader that should see everything names every environment explicitly;
adding an environment to an estate later never silently widens what an
existing credential can see.

**Tokens must be at least 24 characters.** A shorter one refuses to start the
process, naming the credential (never the token) in the error.

**Every misconfiguration is a startup failure, not a warning**, and it names
the credential:

- a duplicate id in `INV_API_TOKENS`
- two credentials sharing one token
- an id in `INV_API_SCOPES` with no matching entry in `INV_API_TOKENS`, or the
  reverse
- an empty scope for a credential (`ansible:` with nothing after the colon)

The reasoning is the same in every case: skipping a credential that will not
build would mean an integration that authenticates today silently stops
authenticating after an unrelated config edit, with nothing in the log naming
what changed.

**If `INV_API_TOKENS` is unset, the API is not mounted at all.** Every route
under `/api/v1` answers 404, indistinguishable from a path that was never
defined. This is the answer to *"I upgraded and the API is missing"*: set
`INV_API_TOKENS` and restart. An estate running no integrations should not
carry the attack surface of one that is.

---

## Authentication and rate limits

Send the token as a bearer credential:

```
Authorization: Bearer <token>
```

The guard checks, in this order, and refuses on the first that fails:

1. **A browser session on this route is refused outright** — 401. Either a
   confused client, or an attempt to have a session ride a bearer credential's
   route; the guard does not try to arbitrate which.
2. **No bearer token, or an unrecognised one** — 401.
3. **Rate limit, applied only after identity is established**, so a flood by
   one credential cannot deny the others — 429. Ten requests per second
   sustained, bursts up to sixty. A repeated *authentication failure* is
   throttled on its own, separate bucket, so a working client never touches it
   and a brute-force attempt cannot lock one out.

Every refusal is written to the security log server-side; nothing is a silent
drop. Every response carries `Cache-Control: no-store`.

`/api/v1/*` is not CSRF-exempt and does not need to be — every route is a
`GET`, and CSRF protection only ever applies to unsafe methods.

---

## Routes

All `GET`, all under `/api/v1`.

| Route | Returns |
|---|---|
| `GET /assets` | collection — `?after= &limit= &env= &kind= &lifecycle=` |
| `GET /assets/{id}` | one asset, or 404 |
| `GET /services` | collection — `?after= &limit=` |
| `GET /services/{id}` | one service, or 404 |
| `GET /addresses` | collection — `?after= &limit=` |
| `GET /environments` | collection, unpaginated |
| `GET /ansible` | the composed Ansible dynamic-inventory view, unpaginated |

**A query string that is not valid URL encoding is a 400.** `?after=%zz` is
not a bad cursor — it is a query string `net/http` cannot parse, and its
default behaviour is to drop the offending pair entirely, which would hand the
caller page one with a 200. It is refused instead, and the raw query is never
echoed back.

**A query parameter the route does not define is a 400**, not a silently
ignored value — `?limt=5` is refused rather than quietly falling back to the
default limit with a 200. A parameter repeated twice (`?limit=5&limit=9`) is
the same shape of mistake and is refused the same way.

The single-resource routes (`/assets/{id}`, `/services/{id}`) exist for the
observability join: a metric carries an id in a label, and the consumer
resolves it to a name and a placement without paging the whole estate.

---

## Pagination

Keyset, on `id`, ascending. Collections return:

```json
{"data": [ ... ], "next": "<cursor>"}
```

`next` is the id to pass as `?after=` for the following page, or `null` on the
last page. `data` is always a JSON array — `[]` on an empty collection, never
`null`, so a consumer never has to special-case a missing list.

`limit` defaults to 100. A request for more than 500 is **clamped to 500, not
refused** — the substitution is visible in-band, in the length of the response
the client just received, which is why this is the one place the API
substitutes a value rather than rejecting the request (contrast the cursor,
below).

**A malformed `?after=` cursor is a 400, never silently restarted at page
one.** A cursor is the id of the last row of the previous page; if it is not a
well-formed id, the request is refused rather than quietly treated as "no
cursor". A well-formed one is normalised to the hyphenated lower-case
spelling before it is used, so the braced, `urn:uuid:`-prefixed, unhyphenated
and upper-case forms an id parser accepts all select the same rows rather than
quietly skipping or repeating a page. A client that kept going on a corrupted cursor would restart at page
one forever — re-fetching the first hundred rows on every call and never
reaching the rest of the estate, with a 200 every time and nothing anywhere
saying so.

`/environments` returns the same `{"data": [...], "next": null}` envelope even
though it is never paginated — the surface has exactly one envelope shape
everywhere, rather than a special case for the one route that happens to be
small.

---

## Filters

`?env=`, `?kind=` and `?lifecycle=` all belong to **`/assets` alone** — see the
route table above. None of the three exists on `/services` or `/addresses`:
`serviceQueryParams` and `addressQueryParams` accept only `after` and `limit`,
so `?env=prod` against `/services` is not a filter that quietly matches
nothing — it is the same 400 "unknown query parameter" any other misspelled or
unsupported parameter gets. Within `/assets`, all three narrow **within** a
token's scope and can never widen it — the scope check runs first, and a
filter selects a subset of what survived it.

**An unrecognised filter *value* is a 400, not an empty collection.**
`?kind=toaster` and `?lifecycle=purged` are both refused, for the same reason
a bad cursor is refused rather than silently downgraded: a value the caller
cannot use must never be replaced by something that looks like a legitimate,
if surprising, answer.

**`?env=` is case-insensitive; `?kind=` and `?lifecycle=` are not.**
`?env=PROD`, `?env=Prod` and `?env=%20prod` all filter exactly as `?env=prod`
does, and return byte-identical responses. `?kind=VM` and `?lifecycle=Active`
are both a 400.

That asymmetry is deliberate, and it follows from where each vocabulary lives.
An environment code is lower-cased when it is stored, so `PROD` and `prod` are
not two values — they are the same environment written two ways, and refusing
one of them would refuse a correct answer. Asset kinds and lifecycles are a
fixed set of constants, and `VM` is simply not one of them: that is a typo, and
a typo must be refused rather than answered with an empty collection.

**`?env=` is validated against the credential's own scope, not against the
estate — see "Why is `?env=` a 400 instead of an empty list?" below.**

### Retired entities

Collections exclude retired rows by default — see "Retired assets, services
and addresses are asymmetric" below for the full rule and the id-fetch
exception.

---

## The five things that will surprise you

### 1. Why can my token not see a device that is obviously in its environment?

**A token sees an asset only if it is scoped to *every* environment that asset
is in.** `sw-core-1` sits in both `prod` and `dev`. A `dev`-only token does
not see it — not because of a bug, but because the alternative would let the
*least* sensitive environment an asset belongs to decide who may read a
production-facing device.

Fix it by scoping the credential to every environment the boundary device
sits in:

```
INV_API_SCOPES=ansible:prod|dev
```

An asset in **no** environment at all is visible to nobody, by the same rule —
that is a data gap made visible rather than papered over with an implicit
allow.

### 2. An id outside your scope 404s exactly like an id that doesn't exist

`GET /assets/{id}` for an id that names a real asset outside the token's scope
returns the same 404, with the same body, as an id that names nothing at all.
This is deliberate: a distinguishable response (a 403, say) would let a token
enumerate ids and learn which ones name real assets in the estate, without
ever being able to read one — an existence oracle built entirely out of
refusals.

The refused read **is** logged, just not to the caller. It appears server-side
as a `reader_scope_denied` security event carrying the credential id, the
entity type and the id that was asked for. If a client reports "your API says
this doesn't exist" and you believe otherwise, that log is where the two
readings get told apart.

### 3. Why is `?env=` a 400 instead of an empty list, when the environment doesn't even exist?

This is about `/assets?env=X` specifically — the only route `?env=` exists on;
`/services` and `/addresses` refuse it outright as an unknown parameter (§
Filters, above). On `/assets`, `?env=X` is a 400 — "env is not in this
credential's scope" — whenever `X` is outside the credential's scope,
**whether or not `X` names a real environment**. It is never an empty 200.

This used to work the other way: an out-of-scope `env` returned an empty
collection, on the reasoning that a token's own scope is not the client's
business. That reasoning does not survive `/environments` existing on the same
surface — the scope is already published to the client, in full, by that
route, so refusing by scope tells a caller only what it could compute for
itself. Returning 200-with-empty for it instead produced a plausible,
well-formed, empty inventory with no signal the moment somebody's
`INV_API_SCOPES` was edited under them — indistinguishable from "you asked
about a real, legitimately empty environment." The 400 closes that: it is
never a lie (the statement is equally true whether `X` exists or not), and it
also closes the reverse leak — validating against the estate would let a
token learn the entire environment vocabulary by trying names one at a time,
since an unknown code and a real-but-out-of-scope code would have answered
differently.

### 4. Retired assets, services and addresses are asymmetric

Collections exclude retired rows by default. `/assets` accepts
`?lifecycle=retired`, which returns **only** retired assets — there is no way
to ask for "everything, retired included" in one request. `/services` and
`/addresses` have **no such parameter**: their retired rows are not reachable
through this API at all, by any query, under any filter.

`GET /assets/{id}` and `GET /services/{id}` are the exception: both return a
retired entity when asked for by id, because the caller already named that
specific id and the payload's `lifecycle` field says what it is — useful for
answering "was this host retired?" during an incident, which is exactly the
moment somebody looks an id up. This produces a real asymmetry worth knowing
about: **a retired asset fetched by id still carries its `addresses`, while
`/addresses` excludes those same rows from the collection.** Fetching by id
and listing are different questions, and this API answers them differently on
purpose.

### 5. Placement — `site` and `rack` — is published unscoped

**A token sees the site and rack *name* of any asset it can read, even when
that site or rack sits in an environment outside the credential's scope.**
Only the name crosses — no id, no address, nothing that lets the name be used
to reach or enumerate the building. Everything else about the estate is
scoped; this one field is not, and that is a decision rather than an
oversight.

Scoping placement would gut the field rather than protect it: a site or rack
is typically declared in `shared` or in no environment at all, and under the
rule that an entity in no environment is visible to nobody, `site` and `rack`
would come back `null` for essentially every reader — destroying the feature
in the name of hiding a building's name.

**The operational consequence: never name an environment after a client or a
customer.** A `site` or `rack` name crosses every scope boundary unscoped, so
an environment code like `acme-prod` discloses that client's name to any token
that can read anything hosted there — the one piece of information this
design cannot keep inside a scope.

---

## `GET /services/{id}` and disappearing history

`GET /services/{id}` for a service whose every host has been retired answers
`"assets": []` — identical to a service that was never placed anywhere.
**This route does not answer "where did this used to run"; only "where does
it run now."** If you need placement history, it lives in the change log, not
here.

`GET /ansible` has the same blind spot in a stronger form: a retired asset
never appears in the Ansible view at all, under any host or group, because the
view only ever considers `lifecycle = 'active'` assets — see "The Ansible
view" below. This is not the `/assets/{id}`-vs-`/services/{id}` asymmetry from
§4: there is no id-fetch route for the Ansible view to make an exception on, so
a retired host is simply absent, with nothing anywhere in the response saying
so.

---

## DTO shapes

DTOs are hand-written and published deliberately — they are never the
database row or the domain struct. A migration adding a column never changes
what this API returns; publishing a new field is always a separate, reviewed
edit. Every example below is drawn from the repository's golden test fixtures
(`internal/web/testdata/api/`).

**Custom fields (WP-A4) are deliberately absent from every shape below.** An
estate's own attributes are administrator-defined and administrator-retirable;
an integration must not come to depend on one that can disappear at another
person's discretion. `TestCustomFieldsNeverReachTheAPI` pins this against the
same golden fixture the Asset shape below is drawn from.

### Asset

```json
{
  "id": "00000000-0000-0000-0000-000000000001",
  "name": "dc-oslo",
  "kind": "site",
  "lifecycle": "active",
  "environments": ["prod"],
  "site": "dc-oslo",
  "rack": null,
  "addresses": [],
  "services": []
}
```

`site` and `rack` are the nearest containing asset of that kind, unscoped —
see §5 above. `addresses` and `services` are sorted and de-duplicated: a
service with three replicas on one host appears once.

**There is no `role` field.** An earlier revision published one, sourced from
`asset.manager_role` and documented here as a capacity like `"database"`.
`manager_role` is a foreign key into `responsibility_role`, whose whole
vocabulary is `owner`, `operator`, `approver`, `oncall`, `custodian`,
`vendor` — it could never carry that value — and it means nothing without the
team it qualifies, which this surface deliberately does not publish. Use
`kind` for what a machine is and `services` for what it runs.

### Service

```json
{
  "id": "00000000-0000-0000-0000-000000000001",
  "code": "vault",
  "name": "HashiCorp Vault",
  "kind": "secrets",
  "lifecycle": "active",
  "environments": ["prod"],
  "criticality": 1,
  "assets": ["vm-vault-1", "vm-vault-2", "vm-vault-3"]
}
```

`criticality` is the domain's `tier` field, renamed at the contract boundary
because "tier" is an internal word. `environments` is a one-element array —
built from the service's single `environment_id` — so every entity in this API
carries "what environments is this in" as an array, even though the schema
holds it differently for an asset (a set) and a service (one column).
`assets` are the hosts the service is placed on, subject to §4's retired-host
exclusion.

### Address

```json
{
  "id": "00000000-0000-0000-0000-000000000001",
  "address": "10.20.10.2",
  "family": 4,
  "asset": "sw-core-1",
  "asset_id": "00000000-0000-0000-0000-000000000002",
  "environments": ["dev", "prod"]
}
```

`family` is `4` or `6`. An address's environments are the environments of the
asset it belongs to — an address with no asset is visible to nobody, by the
same rule §1 states for assets. An FHRP virtual address, which carries no
interface, is likewise visible to nobody.

### Environment

```json
{
  "id": "00000000-0000-0000-0000-000000000002",
  "code": "prod",
  "name": "Production",
  "role": "production",
  "in_scope": true,
  "criticality": 1
}
```

`GET /environments` returns only the environments inside the credential's own
scope. This is the vocabulary a consumer needs before it can use `/assets`'s
`?env=` filter at all — every code you can legally pass to it appears here.

### The Ansible view

`GET /ansible` composes a full dynamic inventory document from the assets
visible to the token, unpaginated on the wire (the consumer is Ansible, which
expects one document, not a feed — paging happens internally against the
store instead). Only `lifecycle = 'active'` assets are considered, and only
kinds Ansible can actually connect to: `server`, `vm`, `hypervisor`, each with
at least one address. A rack, a patch panel or a PDU is a real asset and not a
thing with an SSH daemon; listing one would produce an inventory that fails on
first use.

Abbreviated from `internal/web/testdata/api/ansible.json` — every value below
is a real entry from that fixture, with most hosts and groups omitted for
space:

```json
{
  "_meta": {
    "hostvars": {
      "vm-vault-1": {
        "ansible_host": "10.20.30.11",
        "invctl_id": "00000000-0000-0000-0000-000000000012",
        "invctl_kind": "vm",
        "invctl_site": "dc-oslo"
      }
    }
  },
  "env_prod": {"hosts": ["hv-01", "hv-02", "vm-vault-1", "..."]},
  "kind_vm": {"hosts": ["vm-vault-1", "..."]},
  "svc_vault": {"hosts": ["vm-vault-1", "vm-vault-2", "vm-vault-3"]}
}
```

`ansible_host` is the asset's **first address after a plain string sort** of
its `addresses` list — the same list, sorted the same way, that `GET /assets`
publishes. That is a lexicographic sort, not a numeric one: an asset holding
`10.20.10.9` and `10.20.10.11` gets `10.20.10.11`, because `1` sorts before
`9`. It is stable and it is not necessarily the management NIC. If a host must
be reached on a particular interface, set that in your playbook or inventory
plugin rather than assuming this route picked it.

**A host name claimed by two assets is a 500, naming both asset ids in the
server log** — never a merge. `asset.name` is unique only among live siblings,
so `vm-app-1` under one hypervisor and `vm-app-1` under another is legal;
Ansible keys `hostvars` by name, so merging them would silently point a
playbook at whichever asset happened to be written last.

Groups are formed along three dimensions — every environment an asset is in,
its kind, and each service it runs — and prefixed by dimension (`env_`,
`kind_`, `svc_`) so a service literally named `prod` cannot collide with the
`prod` environment's group. An asset in two environments appears in both
groups' host lists, because it is in both environments.

Group names are sanitised to Ansible's identifier rules: lower case, every run
of non-alphanumeric characters collapsed to one underscore. **A collision
after sanitisation between two different sources is a 500, naming both source
names in the server log** — never a silent merge of the two groups into one,
which would quietly widen the target set of every playbook that uses the
merged name.

Use it as Ansible's dynamic inventory: point a script or an inventory plugin
at `GET /api/v1/ansible` with the bearer token attached in the `Authorization`
header. The document is the full `hostvars` + groups shape Ansible expects
natively (`_meta.hostvars` plus one `{"hosts": [...]}` object per group), so
no adapter or transformation step sits between this route and `ansible-playbook
-i`.

---

## Status codes

| Status | When |
|---|---|
| 400 | a query string that is not valid URL encoding at all (`?after=%zz`); a malformed `?after=` cursor; an unparseable or non-positive `?limit=`; an unrecognised `?kind=`/`?lifecycle=` value; an unknown query parameter name, or one repeated; `?env=` naming an environment outside the credential's scope |
| 401 | no bearer token; an unrecognised token; a browser session present on the route |
| 404 | an id that does not exist, **or** an id that exists but is outside the credential's scope — byte-identical either way, see §2 |
| 429 | the credential, or repeated failed authentication attempts, over the rate limit |
| 500 | the estate cannot be rendered into this contract: an Ansible group-name collision after sanitisation, or a host name claimed by two assets. Both name the conflicting sources in the server log and neither says anything about them to the client |

Every error body is `render.JSONError`'s shape and never echoes a raw database
error — a 500 is logged in full server-side and answered to the client with a
generic body.
