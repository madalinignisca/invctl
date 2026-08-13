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
