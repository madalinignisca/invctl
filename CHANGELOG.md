<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Changelog

What changed, and what you have to do about it.

**This is not a commit log.** `git log` already exists and is better at being
one. An entry earns its place here by changing something an operator has to
know: behaviour they will notice, a setting they must set, a migration that
cannot be undone, or an answer the software now gives differently. Everything
else — refactors, tests, docs, tidying — belongs in the history and not here.

Sections, in the order they matter to somebody upgrading:

| | |
|---|---|
| **Action required** | the upgrade needs a decision or a command. Read before deploying. |
| **Changed** | existing behaviour is different. Nothing to do, but expect it. |
| **Added** | new capability. Ignoring it costs nothing. |
| **Fixed** | it was wrong, now it is not. |

Versions follow [semantic versioning](https://semver.org). While the major
version is `0`, a **minor** bump may change behaviour — that is what `0.x`
means, and it is why **Action required** is the first section rather than a
footnote.

---

## [Unreleased]

### Action required

- **Project owners can now write, and only within their projects.** This is
  the change the rest of WP-G1 was building toward, and it is live: a user
  whose `app_user.role` is `project_owner` may create, edit and retire the
  assets, services and circuits linked to a project they are assigned to, and
  is refused on everything else — every other entity type, and every
  in-scope-looking object in a project they do not hold.

  **Nobody gains access by upgrading.** The role is not assigned to anyone
  automatically, and there is still no UI for assigning a person to a project
  (`internal/store/user_projects.go`, no route in front of it yet). Until an
  Administrator sets both a role and an assignment, every existing account
  behaves exactly as it did before.

  Scope is resolved fresh on every request, with no cache: removing somebody
  from a project takes effect on their next click rather than at the end of a
  session.

- **A project owner can write journal notes on anything they can write.** A
  note takes its scope from the entity it is *about*: it is writable exactly
  when its subject is. Previously `journal_entry` was classified as topology
  and so was Administrator-only, which meant a project owner could create a
  server and then not record a single word about it -- the note being the
  cheapest and most useful thing they had to contribute.

  The subject is read from the stored row on edit and withdrawal, never from
  the submitted form, so naming an asset you own does not let you edit a note
  attached to one you do not.

  The rest of the topology surface -- addresses, interfaces, dependencies,
  cost lines, placements -- is still Administrator-only. That is a known
  limitation rather than a decision that it should stay that way.

- **A project owner creates entities from inside a project, not from the
  generic form.** `POST /projects/{id}/assets/new` and its service and
  circuit siblings create the entity and link it to the project in one
  transaction. `POST /assets` — and the existing "link an entity that already
  exists into this project" routes — stay Administrator-only. That split is
  deliberate: it is what makes "may create in my project" and "may take over
  any existing asset" two different URLs instead of one runtime check.

- **The import surface is Administrator-only, explicitly.** `/imports` and
  `/import/*` now sit behind a role check rather than the general write gate.
  Every imported row is newly created, so a project owner's scope could never
  cover one and the import would have refused every row — a form guaranteed
  to fail is worse than one that is absent.

- **The E2E fixture account now requires a password you choose.** If you run
  the browser suite: `INV_SEED_E2E_PROJECT_OWNER=true` alone no longer creates
  anything. Set `INV_E2E_PROJECT_OWNER_PASSWORD` as well — there is no default
  in the binary or in the specs. The password used to be a constant published
  in this repository, which would have been a working login on any deployment
  where the flag was ever set. Note the flag is a one-way ratchet: unsetting
  it does **not** remove an account it already created, and the seeder now
  logs a warning whenever it stages or finds one.

- **Cost figures are no longer visible to every reader by default.**
  `Authorizer.CanSeeCosts` used to return exactly what `CanRead` did, so every
  authenticated user saw acquisition prices, support contract values and
  project totals. It now consults `app_user.can_see_costs` for everyone
  except Administrators, who still see costs implicitly. If your deployment
  relies on all readers seeing costs, grant `can_see_costs` on the accounts
  that need it — see `docs/rbac-design.md` §3 for why Observers and project
  owners are treated identically here (giving Observers costs implicitly was
  a real defect: demoting a project owner to Observer would have *widened*
  their cost visibility to the whole estate).

- **`INV_ADMIN_USERS` now overrides `app_user.role`, not just seeds it.** A
  user named in the list has full write access regardless of their role
  column — this is required for it to work as the break-glass recovery path
  described in `docs/RECOVERY.md`. A deactivated account named in the list
  still cannot write.

- **The read-only inventory API is absent until you set `INV_API_TOKENS`.**
  Upgrading alone does not turn it on — every route under `/api/v1` answers
  404, indistinguishable from a path that was never defined, until a token is
  configured. That is the answer to "I upgraded and `/api/v1` 404s."

- **Custom fields defined before this release have no owning team, and
  migration `00054` cannot invent one.** They will show as "unassigned" on
  the `/custom-fields` registry from this release forward. Finding and
  assigning them is deliberately left as a follow-up you do at your own pace
  — nothing stops working, and every existing value is untouched — but
  budget a pass through `/custom-fields` to set an owner on each. Every
  *new* field requires one at creation, with no way to skip it.

### Added

- **A read-only, token-scoped inventory API at `/api/v1`.** Assets, services,
  addresses and environments, keyset-paginated like the change log, plus a
  composed Ansible dynamic-inventory view. Built for an Ansible integration and
  an observability join, not for a browser — every route is `GET`, no route
  reaches observed state, cost, or anything personal. Configuration is two
  variables, `INV_API_TOKENS` and `INV_API_SCOPES`; there is no wildcard, so a
  credential must name every environment it is allowed to read. See
  `docs/API.md`.

- **Custom fields.** Define your own typed attributes — text, number, date,
  boolean or a fixed list of choices — on assets or on services, from
  `/custom-fields`. Each one is described, attributed to whoever defined it,
  and audited exactly like a built-in field: every value change writes a
  `change_log` entry against the asset or service it belongs to. Retiring a
  field never deletes the values it already holds. Values leave through their
  own CSV download — "Download custom field values as CSV" on the asset and
  service lists — one column per field, kept separate from the ordinary asset
  and service CSV exports so neither of those loses its own guarantee of
  loading back through the importer. Deliberately absent from `/api/v1` —
  see `docs/API.md`. See `docs/custom-fields-design.md`.

- **A custom field's value is folded into the audit trail as a plain change
  counter, not as text.** A change to `change_log` now shows that a value
  changed, which field, and how many times it has changed, never what it
  changed to — the value itself still lives on the entity's own page. This
  closes the one place in the codebase where the audit trail's "carries no
  personal data, keep it forever" claim previously did not hold: an
  administrator naming a field "Owner email" no longer writes that text into
  an append-only table. It is **forward-only** — any entry written before you
  upgrade still holds the plaintext it was written with, because `change_log`
  is append-only and nothing rewrites a stored entry. The change log and the
  change entry page explain the counter in place, so a row like
  `cost_centre@3` reads as intended rather than as corruption. There is no
  key and nothing to configure: an early build of this same unreleased
  feature folded a keyed HMAC digest instead (`code=#<digest>`, needing
  `INV_AUDIT_FOLD_KEY` held outside the database to be GDPR-correct), which a
  review found was pseudonymisation rather than anonymisation and, for a
  `select` or `boolean` field, invertible with no key at all. That digest is
  gone; migration `00053` drops the table it was persisted in, and
  `INV_AUDIT_FOLD_KEY` is no longer read. See `docs/AUDIT.md` and
  `docs/custom-fields-design.md` §5.

- **A custom field now names an owning team, not just the individual who
  defined it.** `created_by` answers "who defined this field", which turned
  out to be the wrong answer to "who do I ask about it" the moment that
  person leaves — a GDPR erasure request against them already left the
  registry's only attribution blank. `owner_team_id` reuses `team.contact_ref`
  (never a person, already documented as such) and is required on every
  field created from this release forward, editable afterwards like its
  label and description. Shown on the registry, the entity detail page, and
  — specifically, since this is the moment it matters — beside a validation
  error in the value editor. A team that later retires keeps displaying,
  marked "(retired)", on every field that already names it; it is simply not
  offered to a new one. See migration `00054`, `docs/AUDIT.md`, and
  `docs/custom-fields-design.md` §4.

---

## [0.5.0] — 2026-08-17

**What the estate costs, and whose cost it is.** invctl could say what a rack
was bought for and what a circuit is billed at. It could not say what a project
costs, because nothing recorded how big a machine was or who was standing on it.
This release models capacity, divides it between the projects that claim it, and
divides the money the same way — with the judgements it rests on declared,
audited and visible rather than assumed.

**Nine migrations run on first start.** They add columns and tables; none drops
or rewrites anything. On a real estate of 75 assets and 677 audit rows the
upgrade preserved every count with `integrity_check ok` and no foreign-key
violations — but **back up the database before starting the new binary anyway**,
because that is the advice that costs nothing and the exception is the one you
did not test.

### Action required

- **Two judgements have to be made, or two figures stay deliberately blank.**
  Neither has a safe default, and inventing one is how a number nobody agreed
  ends up in a board pack.

  | | |
  |---|---|
  | `cluster.cost_split_cpu` | what proportion of a cluster's cost is attributable to CPU; memory takes the rest. **Until it is set, that cluster's money is not divided at all** and the page says why. One invoice buys cores and memory together and no arithmetic separates them — half and half is not cautious, it is arbitrary. |
  | asset occupancy | who shares a machine, in whole percent. Until it is declared, a shared box's whole capacity lands on whichever project owns it — which is what happened before this release, so nothing changes until you say otherwise. |

- **Every existing cost line was defaulted to `applies_to = universal`**, which
  is what the arithmetic implicitly assumed before the column existed and is
  genuinely right for hardware, power and rack space. It is **not** right for a
  licence only some guests benefit from, and nothing here can detect which lines
  those are. **Scope is unreviewed on every pre-existing line until a person
  looks at it.** A licence spread across guests that derive nothing from it
  makes every other workload subsidise them — and the total stays correct, so
  nothing prompts anybody to check.

- **The capacity figures start empty and say so.** A cluster with no recorded
  host sizes reports that its totals are a floor, not a total. Nothing is
  guessed from observed load: a quiet cluster would raise its own apparent safe
  ratio and licence exactly the overcommitment the findings exist to catch.

### Added

- **Capacity.** Hosts carry cores and memory; workloads carry what they were
  **provisioned** (the hard limit) and what they were **allocated** (the figure
  money is computed on). The two routinely differ, and the gap between them is a
  decision somebody made without pricing it. Clusters carry a declared CPU
  overcommit ratio, written the way it is spoken — `3`, or `1.5`.

- **Storage as a dimension.** A pool is an asset with a raw capacity and a
  redundancy kind; usable capacity is derived, never typed. Ceph at three-times
  replication turns 30 TB raw into 10 TB usable, and the pool page says what the
  other two thirds bought. A workload's claim is recorded per pool, because a
  machine holds its system disk on fast media and its backups on bulk, and those
  are different products at different prices.

- **Who holds a cluster, per dimension.** One project is routinely a different
  percentage of CPU than of memory than of each storage pool — on the demo
  estate, 12.50% / 15.63% / 8.79% / 5.63%. There is no single "project share"
  and the report does not offer one: a blended figure would hide which dimension
  binds, which is the only thing a capacity conversation is about. Idle capacity
  is its own slice, because somebody is paying for the headroom.

- **What that share costs.** Cluster cost divided by usable capacity, with the
  availability premium falling out of `min_hosts` rather than a hand-kept
  multiplier. Run rate and amortised capital stay apart the whole way through.
  Slices sum to the invoice exactly — including the idle one.

- **Cost scoping.** A cost line declares who benefits: **universal** across the
  whole capacity, **conditional** across named guests in proportion to what they
  hold, or **per-consumer** across them equally per head — because a per-VM
  backup licence costs the same for a 64 GB machine as for a 2 GB one. A scoped
  line naming nobody is reported and attributed to nobody, never quietly spread.

- **Shared occupancy.** Several tenants in one machine, each with a declared
  percentage. A total that is not 100 is a finding and the remainder is
  attributed to nobody — normalising 90 up to 100 would inflate every declared
  share by a ninth with nothing on any page to notice.

- **Five capacity findings**, needing no money at all: a project grown past what
  it was priced for (the margin is eroding, nobody is in breach), a cluster
  promising more than it can serve, **more capacity priced across engagements
  than the estate can host** — which no utilisation dashboard can produce,
  because utilisation measures what is taken and this measures what could be
  claimed — plus unmeasured hosts and unattributed workloads.

- **Replacement lineage and price movement.** What a box took over from, so a
  refresh can be priced against what it succeeded; and how each cost kind has
  moved over time, against a recorded inflation series, so "up 23%" becomes a
  real rise or a nominal one.

- **Circuits belong to projects.** A circuit carries a monthly rate and an
  install fee, and nothing said which project it served — so every project
  depending on connectivity reported less than it cost.

### Changed

- **A project's cost rollup now reaches its circuits.** Totals that were
  previously too low by the price of connectivity will rise.

- **The cost report moved** to its own file and gained what it could not see:
  the count of things in a footprint carrying no cost line at all. A total over
  three priced assets in a footprint of forty is a sample, not a budget.

[0.5.0]: https://github.com/madalinignisca/invctl/releases/tag/v0.5.0

---

## [0.4.1] — 2026-08-14

**A security release, and the only one so far.** No feature changes, no
migration, no new setting. Every binary published before this one — 0.1.0
through 0.4.0 — was built against a Go standard library carrying six known
vulnerabilities. **Replace the binary.**

### Action required

- **Replace any invctl binary you downloaded before 2026-08-14.** Stop the
  service, swap the file, start it. Nothing else changes: the database is
  untouched, no migration runs, and no configuration is different.

- **Two of the six are worth knowing about specifically**, because they sit on
  paths this application actually uses rather than on code it merely links:

  | | |
  |---|---|
  | `GO-2026-5856` | an Encrypted Client Hello privacy leak in `crypto/tls`, reached through **LDAP over TLS** — `StartTLS` and `DialURL` |
  | `GO-2026-6091` | JavaScript regexp context tracking in `html/template`, reached through **every page this software renders** |

  The other four — `GO-2026-6090` (post-handshake messages in `crypto/tls`),
  `GO-2026-6089` (`ReadHeaderTimeout` on the unencrypted HTTP/2 check in
  `net/http`), `GO-2026-6088` (`encoding/xml` recursion), `GO-2026-5972`
  (`encoding/asn1` recursion) — are reachable but on narrower paths.

  If your deployment uses **directory authentication**, or terminates **TLS in
  invctl itself** rather than behind a proxy, treat this as the reason to
  upgrade today rather than at your convenience.

- **Nothing here was exploited, and nothing here is known to have been.** These
  are library flaws found and fixed upstream, not incidents. The reason to move
  is that they are published and the fix is a file copy.

### Fixed

- **Built with Go 1.26.6**, which carries the fix for all six. The pin lives in
  `go.mod` and both workflows resolve their toolchain from it, so the version
  that builds a release is the version the repository declares.

### Why you were not told sooner

Worth stating plainly, because it is the actual failure and it was ours.

The vulnerability scan runs in CI, in the same job as the linter. That job had
been **failing on every push since 2026-08-07** — not because anything was
wrong with the code, but because the linter action could not load its own
configuration file and died before starting. `govulncheck` never ran. The
release workflow is a separate workflow that did not run either check, so four
releases went out green over a tree that had not been scanned in a fortnight.

Both are fixed: the action version matches the configuration format, and the
release gate now runs the same checks CI does, so a tag cannot ship over a red
tree again.

One consequence to expect rather than debug: **this check can go red on a
morning when nobody has touched the repository.** Five of the six advisories
above landed overnight against a Go version that had been clean the previous
evening. That is the scanner working.

[0.4.1]: https://github.com/madalinignisca/invctl/releases/tag/v0.4.1

---

## [0.4.0] — 2026-08-13

Performance. **No migration and no new setting** — replace the binary and
restart.

### Action required

- **Nothing.** No migration runs, no configuration changes, and no page answers
  differently. Back up before the upgrade as you would for any other, but there
  is no schema change to reverse.

### Changed

- **The prefix list is about four times faster.** On a 10,000-prefix estate the
  page went from **711ms to 181ms**. It was doing quadratic work: for every
  prefix it rescanned every other prefix to find its children, which is a
  hundred million comparisons at that size and is invisible until an estate gets
  large. The output is identical — same prefixes, same order, same utilisation
  and next-free figures.

  If your IPAM has thousands of prefixes you will notice this. If it has dozens,
  it was never slow and nothing changes.

### Nothing else moved

Measured on the same 10,000-prefix, 50,000-address estate, before and after:
the overview at 16ms, an outage simulation at 4.9ms, an address search at 1.8ms
and the asset list at 1.5ms are unchanged. They were not slow and were not
touched — this release fixes one specific defect rather than tuning everything
in reach.

### Known limits

Unchanged: **authorization is a flat username list** (`INV_ADMIN_USERS`),
granting write access across the whole estate; directory groups are not
consulted, and notes are readable by anyone who can read the entity they hang
off.

Worth stating for this release specifically: **the prefix page still renders
every prefix in the estate**, computing next-free for each. Ten thousand rows is
a large answer to a question nobody asked, and the remaining 181ms is mostly
that. Paginating it is a change to how the page works rather than an
optimisation, so it has not been done quietly.

[0.4.0]: https://github.com/madalinignisca/invctl/releases/tag/v0.4.0

## [0.3.0] — 2026-08-13

One feature: whether a rack can actually be cabled. It completes the physical
group — will it fit (0.1.0), will it stay cool (0.1.0), can it be wired (this).

### Action required

- **Migration `00040` runs at startup** and adds one nullable column
  (`device_type.port_face`). It alters no data and rewrites no table; the
  restart is the usual few seconds. Back up first regardless:
  `sqlite3 invctl.db ".backup 'invctl-$(date -u +%Y%m%d).db'"`.
- **Nothing you rely on changes.** No configuration, no defaults, and no
  existing page answers differently.

### Added

- **Where the ports are, on a catalogued model** — front, rear, or both. One
  field on the hardware catalogue, and the only thing this release asks you to
  type. `both` exists so a patch panel is never reported as wrong-facing.

- **Three cabling findings**, derived from cables, ports and rack positions you
  already record:

  - **Leads that cross the cabinet.** A lead between two boxes whose ports face
    opposite ways leaves the front of one, travels round the cabinet and arrives
    at the back of the other. That is the patch nobody wants to trace.
  - **A heavily cabled box in a narrow cabinet.** Twenty-four or more leads
    landing on one box where the side channel is about 58mm wide. It names the
    count and the cabinet; it does **not** claim the channel is full, because
    cable routing is not modelled and inferring it would be a confident guess.
  - **A cable too short for the span it is declared across.** Within one
    cabinet the distance is arithmetic on the rack unit, plus an allowance for
    the fact that a lead runs to the channel and back rather than diagonally
    between two ports. Either the length is wrong or something is under tension.

  **The length check is same-cabinet only.** Two racks have no recorded distance
  between them — there is no floor plan here — so between cabinets it stays
  silent rather than guessing.

### Known limits

Unchanged and still worth repeating: **authorization is a flat username list**
(`INV_ADMIN_USERS`), granting write access across the whole estate; directory
groups are not consulted. A note written in 0.2.0 is readable by anyone who can
read the entity it hangs off.

New here: a model with no declared port face is **counted as a gap, not
assumed** — so an estate that has catalogued nothing will report a number rather
than a clean bill. That is the intended behaviour and it is the honest one.

[0.3.0]: https://github.com/madalinignisca/invctl/releases/tag/v0.3.0

## [0.2.0] — 2026-08-13

Three features and one bug that had been live for a day.

### Action required

- **Migration `00039` runs at startup** and adds one table (`journal_entry`).
  It creates rather than alters, so no existing row can fail it and no table is
  rewritten — the restart is the usual few seconds. Back up first anyway, as
  every upgrade should:
  `sqlite3 invctl.db ".backup 'invctl-$(date -u +%Y%m%d).db'"`.
- **Nothing else changes behaviour you rely on.** No configuration is added, no
  default moves, and no existing page answers differently.

### Added

- **Circuits are connectivity edges, and a circuit can be cut.** A circuit whose
  two ends land on interfaces of assets in *different* forwarder groups now
  joins those groups in the reachability model — derived from the terminations
  you already record, not declared a second time. `Simulate cutting this` on a
  circuit answers *"the fibre is cut, what goes dark"*, which the asset
  simulator could not: a circuit is not in the containment tree, and cutting it
  removes an edge rather than a vertex.

  The page distinguishes three outcomes, because they need opposite responses
  and an impact result cannot tell them apart: it **joins nothing** (most
  circuits end at the provider, whose side you do not model), it **joins and
  another path survives**, or it **joins and the far side is cut off**.

- **Notes, on anything.** Free-text entries against assets, services, circuits,
  clusters, projects, overlays, redundancy groups, VLANs, prefixes and teams —
  for what a person knows that no column has a place for: why a box is held on
  old firmware, which vendor case covers a fault, what was decided and why.

  Notes appear on the entity's timeline beside the audit trail and observed
  transitions, **labelled as notes**, and they get a reserved share of that
  timeline so a note written during an incident is never pushed off it by the
  churn the incident caused. They are editable — the edit is audited, so the
  previous wording stays recoverable — and withdrawal is soft.

  The author is stored as an opaque user id, like every other actor here, so
  the record carries no personal data. **The body is free text you write**, and
  it is the one field in this system where personal data can legitimately
  arrive; the form says so.

- **CSV download on the asset, service, circuit and prefix lists.** The link
  carries the page's current filters, so the file is what was on screen.

  **The asset export uses the importer's own columns**, so a file can be
  exported, edited in a spreadsheet and loaded back — bulk editing without a
  bulk editor. The parent is a path (`dc-oslo/rack-a1/hv-01`) and the device
  type is `manufacturer_code/model`, because those are what the importer
  resolves.

  Cells beginning `=`, `+`, `-` or `@` are prefixed with an apostrophe.
  Spreadsheets evaluate those, so an asset named `=cmd|'/c calc'!A1` would
  otherwise become code when a colleague opens the file. Nothing is removed and
  the apostrophe does not display.

### Fixed

- **The circuit impact page returned 500 for every circuit**, for about a day
  in the 0.1.x line. It reused a shared template fragment without carrying all
  the fields that fragment reads, which Go's templates refuse at render time.
  Every layer beneath it was tested and green; nothing fetched the page. There
  is now a test that renders both impact pages.

### Known limits

Unchanged from 0.1.0, and worth repeating: **authorization is still a flat
username list** (`INV_ADMIN_USERS`), granting write access across the whole
estate. Directory groups are not consulted.

New in this release: a **note is visible to anyone who can read the entity** —
there is no per-note visibility, and there is no way to restrict who reads one.
Write what you would be comfortable with any signed-in user reading.

[0.2.0]: https://github.com/madalinignisca/invctl/releases/tag/v0.2.0

## [0.1.0] — 2026-08-11

First tagged release. There is no upgrade path to write about, so this entry
describes what you are installing rather than what changed.

Read [`docs/manual/parts/10-installation.md`](docs/manual/parts/10-installation.md)
before the first run. The rest of the operator manual is in `docs/manual/`.

### Action required

- **Set `INV_SESSION_KEY`.** Without it a random key is generated at startup and
  every session is invalidated by a restart — including the restart that
  installs the next upgrade. The server logs a warning and starts anyway,
  because refusing to boot over it would be worse.
- **Set `INV_ADMIN_USERS`.** It is the entire authorization model in this
  release: a comma-separated list of usernames that may write. Everyone else who
  can sign in gets read-only. An empty list means nobody can change anything.
- **Decide the database before you load data.** SQLite and PostgreSQL are both
  first-class and every query runs unmodified on either, but there is no
  supported migration *between* them. Choose per the sizing note in the
  installation guide.
- **Take a backup before every upgrade.** Migrations run automatically at
  startup and are not reversed automatically. On SQLite use
  `sqlite3 invctl.db ".backup 'invctl-YYYYMMDD.db'"` — a plain `cp` of a live
  database in WAL mode is a torn read and has silently lost rows here before.

### Added

- **Inventory and impact.** Assets in a containment tree, services, endpoints
  and dependencies. The question the software exists to answer is *"if this
  fails, what else stops working"*, and the impact engine answers it across
  containment, dependency, network reachability, power and cluster HA.
- **Two databases, one schema.** SQLite (pure Go, `CGO_ENABLED=0`) and
  PostgreSQL. The test suite runs against both, and a change that only passes on
  one is not finished.
- **Declared, observed and provenance kept apart.** What somebody asserts, what
  the estate reports about itself, and where a fact came from are three
  different things with three different audit obligations. See `docs/AUDIT.md`;
  it is normative, not a design essay.
- **An append-only audit trail.** Every change to declared state writes a
  `change_log` row in the same transaction. The actor is an opaque user id, so
  the log carries no personal data and can be kept indefinitely; scrubbing an
  `app_user` answers an erasure request while the log keeps its integrity.
- **Local and LDAP/AD authentication.** Simple bind against a directory, with
  the account upserted on first successful sign-in. The installation guide has
  worked examples for OpenLDAP and Active Directory.
- **Addressing.** VRFs, prefixes with a containment tree, IP ranges, VLANs and
  VLAN groups, first-hop redundancy, and a registry layer for RIR allocations
  and ASNs.
- **Physical modelling.** Racks with elevations, the power chain from an asset's
  input up to the supply behind it, a hardware catalogue whose end-of-support
  dates are inherited by uncatalogued assets, and physical fit — depth, weight
  and airflow.
- **Costs, on four surfaces.** Assets, services, projects and circuits, with
  validity windows, so a rollup reflects what is being paid now.
- **An overview that says what needs a decision**, in three severities: *fault*
  (wrong now), *risk* (one failure away from wrong), *gap* (not recorded, so not
  knowable). The third is the one that makes the other two trustworthy.
- **A machine-facing observation endpoint**, so a monitoring system can report
  health without any credential in this system being able to write inventory.
- **`invctl -version`**, which answers "what is actually deployed here" from the
  binary alone. The build also appears in the rail's footer and in the startup
  log.

### Known limits

Stated because finding them yourself after deploying is worse.

- **Authorization is a username list.** No per-object permissions, no groups, no
  LDAP-group-derived roles. `INV_ADMIN_USERS` grants write access globally.
- **LDAP is simple bind only.** No group lookup, no service-account search, no
  nested-group resolution.
- **The system never acts on your estate.** It has no configuration management,
  no remediation and no discovery agents; it presents state. This is a
  deliberate boundary enforced by a test, not a gap waiting to be filled.
- **No backup or restore command.** Use your database's own tooling.
- **Linux amd64 binaries only.** It is pure Go and builds elsewhere; nothing
  else is tested or published yet.

[0.1.0]: https://github.com/madalinignisca/invctl/releases/tag/v0.1.0
