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
