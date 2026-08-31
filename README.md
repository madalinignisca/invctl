# invctl

An infrastructure inventory (CMDB) for a segmented estate, covering physical
network, physical compute, virtualisation, and the workloads on top of them.

The point of it is not that it stores assets — a spreadsheet does that. The
point is that relationships are first-class, so it can answer:

- If I reboot node 3, what breaks?
- What talks to this database, over which port, using which credential?
- Which assets sit in more than one environment?
- Which services are running fine right now but will not come back after a
  restart?

See `HANDOVER.md` for the design record and `docs/DECISIONS.md` for the
decisions taken during this build.

## Status

**Release candidate for 1.0.** Thirty work packages are complete —
physical estate (racks, power chains, cables, path tracing, physical fit),
networking (VRFs, prefixes, VLANs, FHRP, L2VPN, ASNs), virtualisation and
clusters, cost attribution and capacity modelling, bulk import, custom
fields, the read-only JSON API, and role-based access control.

The gate for 1.0 is deliberately narrow: **a company should be able to
deploy this on their own estate and run it.** From 1.0 the database schema,
the URLs and the `INV_*` environment variables are stable, and a breaking
change to any of them means 2.0. `/api/v1` is covered by that promise —
fields may be added, never removed or renamed.

To run it: `docs/INSTALL.md`. To upgrade it: `docs/UPGRADE.md`. What each
role may do: `docs/ROLES.md`.

The original milestones, which are the foundation everything above sits on:

| Milestone | What it covers | State |
|---|---|---|
| M0 | Config, migrations on both engines, sessions, local login, CSRF | done |
| M1 | Environments, assets, closure maintenance, membership, change log | done |
| M2 | Applications, services, instances, all four runtime tables | done |
| M3 | Endpoints, routes, pools, dependencies, both dependency panels | done |
| M4 | Impact engine, simulate-outage on every asset | done |
| M5 | Global search (IP / MAC / serial / hostname / code / port), LDAP | done |

**Not started, and not planned for 1.0:** discovery agents, the lint engine,
and firewall reconciliation. Each would let invctl reach out and act on the
estate, which is a different product with a different risk profile — see
`HANDOVER.md` §1 on configuration management as a non-goal.

An earlier version of this section also listed the Ansible inventory
endpoint as unstarted. It shipped: `GET /api/v1/ansible`, off until
`INV_API_TOKENS` is set. See `docs/API.md`.

Remaining before 1.0, and tracked in `docs/ROADMAP.md`: documentation
finishing, and marking the roadmap itself honest.

## Try the demo

Needs Go 1.22+. Nothing else; the demo runs on SQLite and the binary embeds its
own templates and assets.

```bash
make demo
```

Then open <http://localhost:8088> and sign in as `admin` / `demo-password`.

The demo binds `0.0.0.0:8088`, so it is also reachable from another machine on
this network at `http://<this-host>:8088`. That is deliberate — it makes the
tool easy to show someone — but it is plain HTTP with a fixed password, so keep
it to a trusted network. `make dev INV_LISTEN=127.0.0.1:8088` keeps it local.

The database is seeded with an estate built to exercise the interesting cases,
not a toy one. Things worth trying, in order:

1. **Assets → `hv-01` → Simulate losing this.**
   Three of Vault's nodes are spread across hypervisors, so Vault does *not*
   appear: quorum survives. `pgsql-core` reports **degraded**, not down — the
   standby can take over, but promotion is manual. Both replicas of
   `orders-api` turn out to live on this one hypervisor, so it goes **down**,
   and so does `partner-gateway` — even though the proxy it goes through is
   perfectly healthy on another host. That last one is the finding a
   spreadsheet cannot produce.

2. **Assets → `rack-a1` → Simulate losing this.**
   Two of three Vault nodes are in this rack, so now the cluster is down. Look
   at **Will not restart**: `sso` is still serving and will keep serving, but
   it reads its database credential from Vault at boot and will fail to start
   the next time anything restarts it. Also note the reported **dependency
   cycle** — `sso → pgsql-core → vault → sso` is real, and it means there is no
   clean order to bring the group back.

3. **Assets → `vm-queue-1` → Simulate, then change the outage length.**
   At 3 minutes, `orders-api` is unaffected — it buffers order events for 300
   seconds. At 45 minutes it degrades. The window control is wired to the
   engine, not decoration.

4. **Search.** Paste `10.20.30.11` (resolves to the VM holding it *and* the
   containing prefix), `aa:bb:cc:00:10:01` (a MAC, in any format),
   `FCH2033V0YR` (a serial), or `5432` (a bare number is treated as a port).

5. **Environment spans.** `sw-core-1` carries production and development VLANs
   and is flagged. `fw-edge-1` spans production and transit and is not —
   brokering between segments is what a transit zone is for.

To run the same demo against PostgreSQL instead:

```bash
make run-postgres     # starts the container and points invctl at it
```

## Commands

```bash
make dev            # migrate + seed + run on SQLite, templates reload live
make demo           # throw away the database and start fresh
make run-postgres   # run against PostgreSQL in Docker
make build          # CGO_ENABLED=0 static binary in bin/invctl
make css            # regenerate the stylesheet (downloads Tailwind if needed)
make test           # full suite against both engines
make test-sqlite    # SQLite only, no Docker required
make lint           # gofmt, go vet, staticcheck
make migrate        # apply migrations to $INV_DB_DSN and exit
make seed           # load the demo estate and exit
```

## Configuration

Deploying on your own server — database, systemd unit, TLS, and the settings
that are wrong by default in production — is `docs/INSTALL.md`. Upgrading an
instance that already exists is `docs/UPGRADE.md`.

```bash
INV_DB_DRIVER=sqlite              # or postgres
INV_DB_DSN=file:invctl.db?_txlock=immediate
INV_LISTEN=0.0.0.0:8088           # what `make demo` uses; :8080 if unset
INV_SESSION_KEY=<32 random bytes, base64>   # generated if unset
INV_ADMIN_USERS=gabriel,nikolaj   # comma-separated break-glass override — see docs/RECOVERY.md
                                 # roles and what each may do: docs/ROLES.md
INV_AUTH_LOCAL=true
INV_AUTH_LDAP=false
INV_SEED=false                    # load the demo estate when the database is empty
INV_SECURE_COOKIES=false          # true behind TLS
INV_API_TOKENS=ansible:<token>    # id:token pairs; unset means /api/v1 is not mounted at all
INV_API_SCOPES=ansible:prod|dev   # required once INV_API_TOKENS is set; no wildcard
```

LDAP, when enabled:

```bash
INV_LDAP_URL=ldap://127.0.0.1:1389
INV_LDAP_BIND_DN=cn=%s,ou=users,dc=invctl,dc=test
INV_LDAP_STARTTLS=false
```

`docker compose --profile ldap up -d` starts a directory with two test users
(`nikolaj` / `ldappass1`, `ingrid` / `ldappass2`) for exercising that path.

A custom field's value is folded into the audit trail as a plain change
counter, not as text — which field, how many times it has changed, never the
value itself. There is no key involved and nothing to configure; see
`docs/AUDIT.md`'s `custom_field_value` row for the mechanism.

If no account exists on first run, one is created. Without
`INV_ADMIN_PASSWORD` a random password is generated and logged once — there is
no default password.

If every account ever loses write access — the last Administrator was
demoted, deactivated, or never handed off — `docs/RECOVERY.md` is the
documented way back in.

## How it is put together

```
cmd/invctl          entrypoint, wiring only
internal/config     env parsing, validated at startup
internal/domain     entities and business rules, zero external dependencies
internal/store      Store, portable SQL, closure maintenance, change log
  /migrations       goose files: shared/ plus one dir per dialect
internal/impact     the impact engine
internal/auth       local + LDAP authenticators, authorization
internal/web        router, handlers, middleware, template rendering
internal/seed       the demo estate
web/templates       html/template: layouts, pages, partials
web/static          vendored htmx, Alpine (CSP build), generated app.css
docs/DECISIONS.md   the decisions taken during this build
```

Three ideas carry most of the weight:

**Containment is a tree, everything else is a graph.** Containment lives in
`parent_id` plus a closure table, so "everything at or below this rack" is one
indexed query. Cabling, dependencies and identity use are cyclic graphs in
explicit edge tables. Conflating the two is the standard failure mode of
home-grown CMDBs.

**A service is logical; an instance is a running copy.** Dependencies and
ownership attach to the service, so an edge is written once rather than once
per replica. Placement attaches to the instance, which is what makes outage
simulation possible at all.

**Impact is a fixed-point iteration, not a traversal.** A service's status
depends on all its inbound edges and dependency cycles are normal, so the
engine iterates until nothing changes. Status is monotonic within a run
(`ok → degraded → down`, never back), which guarantees termination.

## Portability

One schema and one query set run on SQLite and PostgreSQL. Placeholders are
always `?` and go through `sqlx.Rebind`; IDs are UUIDv7 text generated in Go;
timestamps are RFC3339 UTC text generated in Go; enums are `TEXT` with a
`CHECK` mirrored by a Go constant set. IP addresses use a four-column pattern
(text, family, and big-endian byte bounds) so containment is a range scan
rather than an `inet` operator.

Search is the one sanctioned dialect split — FTS5 on SQLite, `pg_trgm` on
PostgreSQL — behind a single interface.

`make test` runs the store suite against both engines. A change that only
passes on SQLite is not done, and that rule has already earned its keep: the
FTS5 tokeniser and PostgreSQL's `LIKE` disagreed about hyphenated names, and
only the SQLite half caught it.

## Testing

```bash
make test        # both engines
make test-race   # with the race detector
make cover       # coverage summary
```

- `internal/domain` — table-driven tests over every availability policy, every
  dependency nature, IP and MAC normalisation, and the validation pairings the
  database `CHECK` cannot express portably.
- `internal/store` — closure maintenance and subtree rebuilds on reparent,
  cycle refusal, the change log written in the same transaction as its
  mutation, rollback leaving neither behind, soft delete, error translation,
  and search. Every one runs against both engines.
- `internal/impact` — asserted against the seeded fixture rather than mocks, so
  a passing suite means the demo is correct. Covers every case `HANDOVER.md`
  §10 asks for.
- `internal/web` — the real router over real HTTP: authentication,
  read-only versus write access, CSRF, 422-with-form-partial on validation
  failure, HTMX fragment versus full page, `HX-Redirect`, open-redirect
  refusal, and security headers.
- `internal/auth` — credential handling, the indistinguishability of failure
  modes, operational errors not masquerading as bad passwords, and LDAP DN
  injection.

## Copyright and licence

Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

invctl is free software, licensed under the **GNU Affero General Public
License, version 3 only**. The full text is in [LICENSE](LICENSE).

**Version 3 only, not "or later", and deliberately so.** The reason to choose
the AGPL at all is §13: run a modified version so that people interact with it
over a network, and you owe those users the source. "Or later" would grant every
recipient the option of terms the Free Software Foundation has not written yet —
so a future version that softened that clause would reopen exactly the hole the
licence was chosen to close, retroactively and with no way to withdraw it.

This costs nothing and is not a one-way door. The copyright holder above holds
all of it, so he can relicense, or add "or later", whenever he decides to. The
reverse is impossible: a permission granted cannot be taken back.

Every source file carries the notice and an `SPDX-License-Identifier`, and
`internal/license` fails the build if one does not — a file added without it has
unstated copyright, and the omission is invisible in review because the header
is identical in every other file. Vendored code (`web/static/htmx.min.js`,
`web/static/alpine.min.js`) and generated output (`web/static/app.css`) are
exempt and are asserted to *stay* exempt, so nothing of somebody else's is ever
claimed as ours.
