# CLAUDE.md — working rules for `invctl`

Infrastructure inventory CMDB. Read `HANDOVER.md` for the domain model and design rationale before writing code. This file covers *how* to build, not *what*.

---

## Stack — locked

Do not substitute any of these. Do not add a dependency not listed here without asking first.

| Layer | Choice | Notes |
|---|---|---|
| Language | Go 1.22+ | stdlib-first |
| Router | `net/http.ServeMux` | Go 1.22 method+wildcard patterns. No chi, no gin, no echo |
| Templates | `html/template` | embedded via `go:embed`. No templ, no jet |
| DB access | `jmoiron/sqlx` | hand-written SQL. **No ORM.** No GORM, no ent, no bun |
| Migrations | `pressly/goose/v3` | plain `.sql` files |
| SQLite driver | `modernc.org/sqlite` | pure Go — keeps `CGO_ENABLED=0` |
| Postgres driver | `jackc/pgx/v5/stdlib` | via `database/sql` |
| Sessions | `alexedwards/scs/v2` | DB-backed store |
| Password hash | `alexedwards/argon2id` | argon2id only. Never bcrypt, never sha* |
| LDAP | `go-ldap/ldap/v3` | |
| CSRF | `justinas/nosurf` | |
| UUID | `google/uuid` | **UUIDv7** (`uuid.NewV7()`) — time-sortable |
| Logging | `log/slog` | stdlib. Structured, no logrus/zap |
| Frontend | HTMX 2.x + Alpine.js 3.x | vendored into `web/static`, not CDN |
| CSS | Tailwind CSS v4 + DaisyUI | see build note below |

### Frontend build note

Prefer the **Tailwind standalone CLI binary** so the toolchain has no Node runtime dependency. DaisyUI is a Tailwind plugin and generally needs to be present on disk, which in practice means an `npm install` at build time even when using the standalone CLI. Verify current compatibility before committing to it — if it forces a full Node toolchain into the build, drop DaisyUI and hand-roll a small component layer in `app.css` instead. Speed of styling is not worth a second runtime in the build pipeline.

Vendor `htmx.min.js` and `alpine.min.js` into `web/static`. This will likely run in a segmented environment with no outbound internet; a CDN reference will simply fail.

---

## Database rules — the ones that get violated

**Every query must run unmodified on both SQLite and PostgreSQL.** The only exception is full-text search, which is explicitly dialect-split behind an interface.

### Never use

`inet`, `cidr`, `macaddr`, `ltree`, native arrays, `ENUM` types, `jsonb` operators in `WHERE`, `SERIAL`, exclusion constraints, `plpgsql`, `num_nonnulls()`, `generate_series()`, `NOW()`/`CURRENT_TIMESTAMP` as a column default, `RETURNING` on multi-row statements.

### Always

- **Placeholders are `?`.** Call `sqlx.Rebind` before execution. Never write `$1` in a query string.
- **IDs are UUIDv7 as `TEXT`**, generated in Go, never by the database.
- **Timestamps are RFC3339 UTC as `TEXT`**, generated in Go, passed as parameters. They sort correctly lexicographically. Never rely on a DB clock.
- **Enums are `TEXT` with a `CHECK (col IN (...))` constraint** plus a matching Go constant set.
- **Booleans use `TRUE`/`FALSE` literals**, not `0`/`1`. Both engines accept the keywords; Postgres rejects integers.
- **`attrs` JSON columns are opaque.** Store as `TEXT`, unmarshal in Go. Never `json_extract` or `->>` in a `WHERE` clause. If you need to filter on it, it isn't an attribute — promote it to a real column.
- **IP addresses use the four-column pattern** (`addr_text`, `addr_family`, `addr_start`, `addr_end` as `BLOB`). Normalize in Go. See `HANDOVER.md` §4.1.
- **Containment queries go through `asset_closure`**, never recursive `parent_id` walks in application code.
- **Every mutation of *declared* state writes a `change_log` row in the same transaction.** No exceptions. If a handler mutates declared state without logging, it's incomplete.
- **Observed state has its own audit obligation — narrower, not absent.** It logs *transitions* to `observed_transition`, never to `change_log`. See "Declared vs observed" below. Reclassifying a column to dodge the audit rule is an architecture decision, not a refactor.
- **Soft delete only, for entities.** Set `lifecycle = 'retired'`. Never delete an asset, service, dependency, cable or topology row.
- **Set and index tables are replaced wholesale, and that is not deletion** — `asset_environment`, `dependency_data_class`, `asset_closure`, `search_index`. They hold the *current value* of something the parent owns, so delete-then-insert inside the parent's transaction is correct. But the parent's `change_log` entry MUST record the change: three separate times a set replacement produced no diff on the parent struct and therefore no audit entry at all, twice for the rows that decide audit scope. Fold the set into the audited value (see `assetAudit`, `dependencyAudit`).
- The only `DELETE FROM` that removes a *fact* rather than replacing a set is the admin-invoked prune of `observed_transition`. There is none in handler code.

### invctl never acts on the estate

This system presents state. It does not push configuration, remediate, restart,
or open a firewall rule — `HANDOVER.md` §1 lists configuration management as a
non-goal and that is a rule, not an aspiration. Observed health may inform what
is *displayed*, labelled as observed with its reporter and age, because showing
is not acting. Nothing in this codebase may trigger a change outside it. The
audience is a person during an incident and the output is understanding.

### Declared vs observed

Three kinds of fact, three obligations. Full normative rules, the column
classification table and the required boundary tests are in **`docs/AUDIT.md`** — read
it before writing any code that accepts input from a monitoring system.

- **Declared** — what somebody asserts *should* be true: an asset exists, this service
  depends on that endpoint, a human verified this edge. Configuration and intent.
  Changes rarely, always because a person decided. Every change is a permanent record.
- **Observed** — what the estate reports about *itself*: reachable, running, last seen.
  Telemetry. Changes constantly, nobody decided it, most reports repeat the last one.
- **Provenance** — `source` and `confidence`. Not a fact about the world, a claim about
  where a fact came from. Laundering provenance is how a fabricated fact becomes an
  authoritative one, so only a `user` actor may write `source = 'declared'`.

The rules that bite most often:

- Naming does not decide the class. `desired_state` is **declared** (intent);
  `verified_at` is **declared** (a person's attestation). Consult the table, don't guess.
- Observed state never becomes intent. Reported `down` never sets `lifecycle`,
  never sets `desired_state`, never deletes a placement. Only a person retires something.
- Log the transition, not the heartbeat — and never route an observation through
  `logUpdate`. `diffJSON` compares every `db`-tagged field except `updated_at`, so a
  moving `observed_at` would log every poll: the exact unbounded growth the rule exists
  to prevent. Observed writes call `RecordObservation`, which writes only on a change of
  `state`.
- Keep three timestamps and never collapse them: `state_since` (onset — "down since
  when" is the 03:00 question), `last_report_at` (server), `reported_at` (caller).
- **`change_log.actor` holds an opaque id** (`app_user.id`), never a username or email,
  so the audit trail carries no personal data and can be kept forever with no retention
  argument. The UI joins to resolve a display name; scrubbing an `app_user` row answers
  an erasure request while the log keeps its integrity and simply stops resolving.
  Attribution is server-derived from the credential, never read from a request payload.
  Every view rendering `actor` renders `actor_kind` beside it.
- A monitoring credential is not an `app_user`, never appears in `INV_ADMIN_USERS`, and
  never reaches `authz.CanWrite`. An observation for an unknown entity is **404, never
  created** — `ON CONFLICT` would turn a narrow token into an inventory-write vector.
- `change_log` is append-only: no `UPDATE`, no `DELETE`, ever. Correct a wrong entry by
  writing a new one. **Never prune on `actor_kind`** — it records who wrote a row, not
  what kind of fact it is, and this repo already writes declared state as `system` and
  `agent`.
- Secret references never enter the audit trail. Redact `identity.secret_ref` in
  `snapshotJSON`/`diffJSON` the way `CreateUser` already redacts `password_hash`.

### SQLite specifics

Set on every connection: `foreign_keys = ON`, `journal_mode = WAL`, `busy_timeout = 5000`, `synchronous = NORMAL`.

Foreign keys are **off by default in SQLite** — without the pragma, every `REFERENCES` clause silently does nothing.

Use two pools: a writer capped at `SetMaxOpenConns(1)` and a separate reader pool. SQLite allows one writer; concurrent handlers without this produce intermittent `SQLITE_BUSY`.

---

## Go conventions

- `internal/domain` has **zero external dependencies** — no `sqlx`, no `net/http`. Entities and business rules only. If domain code imports a driver, the layering is broken.
- Store access goes through the `Store` interface. Handlers never touch `*sqlx.DB`.
- Wrap errors with `fmt.Errorf("doing x: %w", err)`. Never bare `return err` from a non-trivial call. Never `panic` outside `main`.
- Sentinel errors in `domain` (`domain.ErrNotFound`, `domain.ErrConflict`); handlers map them to status codes. Never return a raw driver error to the HTTP layer.
- Constructors validate. `domain.NewService(...)` returns an error for an invalid availability policy — the DB `CHECK` is the second line of defence, not the first.
- Context flows through everything. Every store method takes `ctx context.Context` as its first parameter.
- Table-driven tests. Test the impact engine against the fixture graph in `testdata`, not against mocks.
- Run `gofmt`, `go vet`, and `staticcheck` before considering anything done.

---

## HTTP and HTMX conventions

**Server-rendered HTML. No JSON API for the UI.** Handlers return HTML fragments, not JSON. (A read-only JSON endpoint for Ansible inventory comes post-POC and is separate.)

- Every mutating handler checks the `HX-Request` header: present → render the partial; absent → render the full page. Put this in a single `render.Respond` helper, don't repeat the branch in every handler.
- Swap targets are declared in the template, not chosen by the handler. Default to `hx-swap="outerHTML"` on a wrapping element with a stable id.
- Flash messages use out-of-band swaps (`hx-swap-oob="true"`), so every handler can emit one without the caller coordinating.
- Client-side events go via the `HX-Trigger` response header, not inline scripts.
- Validation errors re-render the form partial with error state and return **HTTP 422**. Never return 200 with an error message buried in the body.
- CSRF token is injected once via `hx-headers` on `<body>`. Every non-GET route goes through the CSRF middleware.
- Redirect after a successful mutation uses `HX-Redirect`, not a 302 — HTMX won't follow a 302 the way you expect.

### Alpine.js scope

Alpine handles **local UI state only**: dropdowns, modals, tab selection, optimistic disable-on-submit. It does not fetch data, does not hold domain state, does not talk to the server. If you're reaching for `x-data` to store something the server knows about, use HTMX instead.

No inline `<script>` blocks beyond `x-data` attributes. Anything longer than a line goes in a file.

---

## Templates

```
web/templates/
  layouts/base.html          full page shell
  pages/<resource>_list.html
  pages/<resource>_detail.html
  partials/<resource>_row.html
  partials/<resource>_form.html
```

A partial must be renderable standalone — that's the whole point. If a partial only works when its parent has already rendered, it's wrong.

Escaping is `html/template`'s job. Never build HTML by string concatenation. Never use `template.HTML` on anything derived from user input.

---

## Auth model (POC)

Two authenticators behind one interface:

1. **Local** — `app_user` with argon2id hash. Seeded admin on first run.
2. **LDAP** — simple bind against `INV_LDAP_URL`. On successful bind, upsert an `app_user` row with `source='ldap'` and no password hash.

**RBAC for the POC is deliberately trivial:** `INV_ADMIN_USERS` is a comma-separated list of usernames. Membership in that list grants write access; everyone else is read-only. Two middleware functions, `RequireAuth` and `RequireWrite` (named `RequireAdmin` at the time). That was the whole authorization model; WP-G1 replaced it.

Structure the check as `authz.CanWrite(user)`. **The claim that used to sit here — that richer roles "should only require changing that function's body, not touching every handler" — was wrong, and WP-G1 retired it by testing it.** `CanWrite` answers a question about a *session*: may this person write at all. Object-level permission is a question about a *row*: may this person write **this** one. No body of `CanWrite` can answer the second, because at the time it is called the row is not known.

The seam that does work, and the one to extend: **`internal/store/store.go`'s `tx.log`.** Every declared mutation already had to pass through it to write its `change_log` row, so it is the one place that sees both the actor and the specific entity being written. `domain.Permit` is checked there. A new role therefore changes `auth.Authorizer.Permit` (what scope a person gets) and possibly `domain.entityScope` (which entity types are project-linked) — and no handler at all. That property is enforced, not hoped for: `internal/web/rbac_boundary_test.go` drives every generated write route, and `internal/store/permit_source_test.go` fails if a permit is minted anywhere outside the named functions.

Note `middleware.RequireWrite` gates on `CanWrite`, which since WP-G1 admits project owners as well as administrators. It was called `RequireAdmin` until that stopped being true. `middleware.RequireAdministrator` is the one that requires an Administrator.

Never log credentials, bind passwords, session tokens, or `identity.secret_ref` contents. `secret_ref` holds a *path*, never a secret; if a code path would put an actual secret in the database, stop and raise it.

---

## Commands

```bash
make dev        # migrate + seed + run, Tailwind watching
make test       # go test ./...
make lint       # gofmt -l, go vet, staticcheck
make migrate    # goose up against $INV_DB_DSN
make seed       # load testdata fixture
make build      # CGO_ENABLED=0 go build -o bin/invctl ./cmd/invctl
```

Tests must pass against **both** engines. `make test` runs the store suite twice — SQLite in-memory and Postgres via a container. A change that only passes on SQLite is not done.

**`go test ./...` on its own is NOT the gate.** With `INV_TEST_POSTGRES_DSN` unset the Postgres half is silently skipped, so it reports green on half the evidence — and a whole day of that ended in a release tag failing CI. Use `make test`. It brings the container up itself and carries the longer timeout the suite genuinely needs; Go's ten-minute default is below what both engines cost.

---

## Definition of done

A change is complete when all of these hold:

- [ ] Queries use `?` placeholders and run on both engines
- [ ] No forbidden Postgres-only feature introduced
- [ ] Mutation of declared state writes a `change_log` row in the same transaction
- [ ] Observed-state writes are idempotent, log only on transition, and touch no declared column
- [ ] Domain constructor validates; DB `CHECK` matches the Go constant set
- [ ] Handler branches correctly on `HX-Request`
- [ ] Validation failure returns 422 with the form partial re-rendered
- [ ] Non-GET route is behind CSRF and `RequireWrite` (or `RequireAdministrator`, for a surface that genuinely needs one)
- [ ] Table-driven test added; `make test` green on both engines
- [ ] `gofmt`, `go vet`, `staticcheck` clean
- [ ] No new dependency, or it was agreed first

---

## Licence

AGPL-3.0-**only** — not "or later"; see README for why. Every source file
(`.go`, `.sql`, `.html`, `.css`, `.js`) opens with the copyright notice and
`SPDX-License-Identifier: AGPL-3.0-only`, and `internal/license` fails if one
does not. New files get it; vendored and generated files must not.

In Go, the notice is followed by a **blank line** before the package clause or
its doc comment — without it the licence becomes the package documentation.

## Never do this

- Add an ORM, a JS framework, or a build step beyond Tailwind
- Write `$1` placeholders, or dialect-specific SQL outside `store/sqlite` and `store/postgres`
- Query inside a JSON column
- Hard-delete a row
- Mutate declared state without a `change_log` entry
- Let an observed-state writer reach a declared column, or derive `lifecycle` from observed health
- Put observed transitions in `change_log`, or prune anything on `actor_kind`
- Let a machine credential write `source = 'declared'`, `verified_by` or `verified_at`
- Put business logic in a template
- Return JSON to the UI
- Store a secret value in the database
- Generate an ID or a timestamp in SQL
- Implement discovery agents, the lint engine, or firewall reconciliation before M5 is done

---

## When something here conflicts with the task

Stop and say so rather than quietly working around it. These rules exist because the portability constraint and the audit trail are the two things that are miserable to retrofit. If a rule genuinely blocks a requirement, that's a conversation, not a workaround.
