<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Ansible deployment — design

**Status:** approved 2026-09-21
**Supersedes:** nothing. There is no deployment automation in this repository
today; `docs/INSTALL.md` is a procedure a human follows by hand.

## The problem

Installing invctl is currently a page of shell commands in `docs/INSTALL.md`:
create a system user, make two directories, copy a binary, write a systemd
unit, generate a session key, start it. Every step is correct and every step is
somebody's Tuesday afternoon. For the EPS deployment and the ones after it,
that page needs to become something that runs.

The harder half is not the install. It is what happens on the **second** run.
`docs/UPGRADE.md` is unusually blunt about it:

> There is no rollback command. Roll back by restoring the backup. Nothing else
> works.

> Verify the backup exists and is non-empty before you replace the binary. A
> backup nobody has looked at is a plan, not a rollback.

Migrations are forward-only. The moment somebody bumps a version variable and
re-runs a playbook, that is an upgrade — and an upgrade without a verified
backup is unrecoverable. A role that makes installing easy makes that easy too,
unless it is designed not to.

## Goals

1. One command takes an empty Debian VM or LXC container to a running,
   healthy invctl.
2. SQLite by default; PostgreSQL when opted in, against a database that
   already exists.
3. Works offline, from a binary an operator placed on disk, with the same
   integrity checking as the online path.
4. Re-running it after a version bump performs a **safe** upgrade, or refuses.
5. The pinned version cannot silently fall behind the releases.

## Non-goals

Named because each is a thing somebody will reasonably expect and not find.

- **No reverse proxy, TLS certificate or DNS.** invctl binds to loopback by
  design; what sits in front of it is an existing, separate concern.
- **No PostgreSQL server installation.** See D2.
- **No firewall management.**
- **No rollback automation.** Rollback is restoring a backup and putting the
  previous binary back — a decision with data loss in it, made by a person who
  has looked at what they are about to lose. The role makes rollback
  *possible* (it keeps both) and never performs it.
- **No multi-architecture support.** `linux/amd64` only, because that is the
  only asset the release workflow publishes. When arm64 ships, this changes.
- **No cluster, HA or multi-host topology.** One host, one service.

---

## Decisions

### D1 — The role owns install *and* upgrade, and the backup is a hard gate

Rejected: install-only, refusing when it finds an existing installation.

Install-only is smaller and cannot destroy anything, but it leaves the obvious
action — re-run the playbook after bumping the version — as the one that breaks
the deployment. Operators would do upgrades by hand forever while a tool that
looks like it handles them sits in the repository.

So the role handles both, and the backup is not best-effort. If the backup does
not exist, or exists and is empty, the play **stops before the binary is
replaced** and restarts the service as it was. The failure mode of this role is
"nothing happened", never "half an upgrade".

### D2 — PostgreSQL is external; the role never installs a database server

Rejected: an opt-in flag installing PostgreSQL on the same host.

Two reasons, and the second is the load-bearing one.

The standing infrastructure rule is that a database belongs in its own
container, separate from the application. A convenience flag that co-locates
them would be the path of least resistance and therefore the one that gets
used.

More importantly, a role that installs a database server also owns its
lifecycle: its version, its upgrades, its backups, its tuning, its failure
modes. That is a second product hiding inside a deployment role. Handing the
role a DSN for a database somebody else manages keeps it to one job.

When `invctl_db_driver: postgres`, `invctl_db_dsn` is **required** and the role
fails with a clear message if it is unset. It verifies reachability before
writing any configuration, so a typo fails at the start rather than as a
crash-looping service.

### D3 — No reverse proxy; the bind address is a variable

Rejected: an opt-in Caddy or nginx with automatic certificates.

The role's job ends at a healthy service answering `/healthz` on
`invctl_listen`, default `127.0.0.1:8080` as `docs/INSTALL.md` prescribes.
Installing a proxy would mean owning certificate issuance and its failure
modes, and would fight whatever edge already exists.

The consequence has to be stated loudly in the role's README, because it is
surprising: **after a successful run, nothing answers from outside the host.**
That is correct and it looks broken. The README names the two settings that
matter behind TLS (`INV_SECURE_COOKIES`, and the proxy headers section of
`docs/INSTALL.md`) so the next step is obvious.

### D4 — A CI check fails when the pinned version falls behind the changelog

Rejected: the release workflow editing the variable and committing; and a line
in a release checklist.

A checklist line depends on somebody reading it on a busy day, which is the
failure mode this repository keeps replacing with tests. Having the release
workflow write to the repository adds a new way for a release to go wrong,
during the one process most worth keeping boring.

So: `invctl_version` is pinned in `defaults/main.yml`, and a Go test asserts it
equals the newest `## [x.y.z]` heading in `CHANGELOG.md`. Cutting a release
without bumping the role turns CI red, naming the file to edit. The test lives
in Go because `make test` already runs and CI already gates on it.

### D5 — Checksum verification is mandatory on both paths, and is the same code

Not offered as a choice; recorded because it is the decision most likely to be
"simplified" later.

The offline path exists because a segmented environment cannot reach GitHub. It
does **not** exist because integrity checking is inconvenient there. Both
sources produce a binary and a checksums file, and both go through one verify
task. Sharing the task is deliberate: two verification code paths means the
rarely-exercised one eventually stops verifying, and nobody notices because the
symptom is silence.

### D6 — Secrets live in an EnvironmentFile at 0600 root:root

`docs/INSTALL.md` warns that `Environment=` lines in a unit are world-readable
through `systemctl show`. A PostgreSQL DSN carries a password, so it cannot go
there.

systemd reads `EnvironmentFile=` as root, in the manager, before dropping to
the service user. The `invctl` user therefore never needs to read the file, and
the strictest mode that works is the right one: `0600 root:root`.

---

## 1. Layout

```
deploy/ansible/
  README.md                     operator documentation, including offline
  playbooks/invctl.yml          hosts + role, nothing else
  inventory/hosts.example
  roles/invctl/
    defaults/main.yml
    tasks/main.yml              detect state, dispatch
    tasks/acquire.yml           fetch or read the binary, verify checksum
    tasks/install.yml           user, directories, binary, unit, config
    tasks/upgrade.yml           stop, back up, gate, replace, migrate
    tasks/verify.yml            start and wait for /healthz
    templates/invctl.service.j2
    templates/invctl.env.j2
    files/.gitkeep              where an operator drops an offline binary
    files/README.md             the two exact filenames expected here
    handlers/main.yml
```

In this repository rather than its own, because D4's check has to read both
`CHANGELOG.md` and the role's defaults.

## 2. Variables

| Variable | Default | Notes |
|---|---|---|
| `invctl_version` | `"1.2.0"` | pinned; D4 keeps it honest |
| `invctl_source` | `github` | or `files` |
| `invctl_listen` | `127.0.0.1:8080` | D3 |
| `invctl_db_driver` | `sqlite` | or `postgres` |
| `invctl_db_dsn` | `file:{{ invctl_data_dir }}/invctl.db?_txlock=immediate` | **required** for postgres (D2) |
| `invctl_secure_cookies` | `true` | false only for a plain-HTTP lab |
| `invctl_admin_username` | `admin` | first run only |
| `invctl_admin_password` | *unset* | **required**; no default, ever |
| `invctl_admin_users` | `admin` | break-glass; see `docs/RECOVERY.md` |
| `invctl_backup_dir` | `/var/backups/invctl` | |
| `invctl_install_dir` | `/opt/invctl` | matches `docs/INSTALL.md` |
| `invctl_data_dir` | `/var/lib/invctl` | matches `docs/INSTALL.md` |
| `invctl_config_dir` | `/etc/invctl` | |

`invctl_admin_password` has no default on purpose. A default admin password in
a deployment role is a default admin password in production, and the seeding
code already refuses to invent one silently (`ensureAdmin` logs a generated
password exactly once rather than shipping a known value).

## 3. Acquiring the binary

Release assets are named `invctl_<version>_linux_amd64` and
`invctl_<version>_checksums.txt`. The checksums file is standard `sha256sum`
output — `<hash>  <filename>` — so verification is a plain comparison against
the line for the binary.

- `invctl_source: github` — download both from
  `https://github.com/madalinignisca/invctl/releases/download/v<version>/`.
- `invctl_source: files` — read both from the role's `files/` directory, at
  exactly those filenames.

Then, identically for both: compute the SHA-256 of the binary, compare to the
recorded hash, and fail on mismatch with both values in the message. A missing
file in `files` mode fails naming the two exact filenames and the directory,
because "which file, where" is the entire question an air-gapped operator has.

## 4. The state machine

State is read from the target, never assumed:

```
/opt/invctl/invctl absent            -> INSTALL
present, -version == invctl_version  -> NO-OP (no restart, no changes)
present, -version != invctl_version  -> UPGRADE
```

`invctl -version` prints `invctl v1.2.0 (commit …, built …, linux/amd64, …)`;
the second field carries the version, with a leading `v` to strip.

**INSTALL** — system user `invctl` (no home, nologin), directories, the
verified binary at `/opt/invctl/invctl`, the config file, the unit, then
§6 verification. No explicit migration step is needed: invctl runs migrations
automatically at startup, before binding, and exits on failure rather than
serving a half-migrated schema (`docs/UPGRADE.md`, "How migrations run"). On a
fresh install there is nothing to lose, so the health check in §6 is a
sufficient gate.

**NO-OP** — the whole point of running this twice. No file changes, no handler
fires, no restart. A second run that restarts the service is a role that cannot
be run from cron or in a loop, and it makes "is anything actually different?"
unanswerable.

**UPGRADE** — in order, stopping at the first failure:

1. Stop the service.
2. Back up. SQLite: copy `invctl.db` and, if present, `-wal` and `-shm`, to a
   timestamped name. PostgreSQL: `pg_dump --format=custom`.
3. **Gate.** Assert the backup file exists and its size is greater than zero.
   On failure: restart the service as it was and abort the play. Nothing has
   been replaced at this point, which is the property this ordering exists to
   guarantee.
4. Preserve the outgoing binary as `/opt/invctl/invctl.<oldversion>`.
5. Install the new binary.
6. Run `invctl -migrate` as the `invctl` user; a non-zero exit aborts.
7. Start, then §6.

Step 3 before step 4 is the whole design. Any other order can leave a host with
a new binary and no way back.

**This deliberately diverges from one suggestion in `docs/UPGRADE.md`.** That
document offers running `invctl -migrate` *before* swapping the binary, so a
slow migration is discovered "while the old binary is still serving" — good
advice for a large estate, where the alternative is a long unexplained outage.
The role does not do that, and the trade is worth naming: migrating first means
the outgoing binary keeps serving against a schema it was not built for, and
migrations here are forward-only with no compatibility guarantee in that
direction. The role accepts a longer window of downtime in exchange for never
running a binary against a schema from a later version. An operator who wants
the other trade on a large estate can run `-migrate` by hand first, exactly as
that document describes; the role's own sequence stays the conservative one.

## 5. Configuration

`/etc/invctl/invctl.env`, `0600 root:root` (D6), holding `INV_*` settings.
The unit is `docs/INSTALL.md`'s, including `NoNewPrivileges`, `PrivateTmp`,
`ProtectSystem=strict`, `ProtectHome` and `ReadWritePaths=/var/lib/invctl`,
with `Type=simple` — invctl does not implement `sd_notify` and a `notify` unit
would hang waiting for a readiness message that never comes.

**`INV_SESSION_KEY` is generated only when absent, then persisted.**
Regenerating it on each run would invalidate every session — everybody signed
out, every time the playbook runs, with a symptom nobody would connect to
Ansible. The generated value is written once and read back thereafter.

## 6. Verification

Start the service, then poll `GET /healthz` on `invctl_listen` until it answers
200 or a timeout expires. `/healthz` requires no authentication
(`internal/web/routes.go`), which is what makes it usable here.

A unit that started is not a service that works: invctl exits on a failed
migration, and `systemctl start` returning success only means the fork
happened. The health check is the difference between "systemd is content" and
"the application is serving".

## 7. Testing

`--syntax-check` and `--check` are the cheap pass and catch typos, not
behaviour.

The real test applies the role to a throwaway **Incus** container running
Debian (`incus` 6.0.0 is available on the build host), covering:

1. Fresh install, SQLite, from `files` (offline) — ends with a 200 from
   `/healthz`.
2. Re-run, unchanged — asserts zero changed tasks and that the service was not
   restarted.
3. Upgrade from an older version to `invctl_version` — asserts a backup exists,
   is non-empty, the previous binary was kept, and `/healthz` answers.

**The backup gate is mutation-tested.** Force the backup step to produce an
empty file, re-run the upgrade, and confirm the play aborts *with the old
binary still in place*. A gate that has never been seen refusing is a claim,
not a check — and this is the one gate whose failure is unrecoverable, so it is
the one that must be proven.

The D4 version-sync test is an ordinary Go test and runs in `make test`. It is
proven by temporarily editing the pinned version and watching it go red.

## 8. Risks

- **A successful run leaves nothing reachable from outside the host** (D3).
  Correct, and indistinguishable from a broken deployment if the README does
  not say so first.
- **`files` mode is only exercised deliberately.** The online path runs on
  every normal deployment; the offline path runs when somebody is already
  under pressure in a segmented environment. Testing the offline path in the
  primary scenario (7.1) rather than as an afterthought is the mitigation.
- **The role can perform an upgrade nobody intended** by virtue of the pinned
  version moving under D4's prompting. The backup gate is what makes that
  survivable, and `--check` shows what would happen before it happens.
- **`pg_dump` must exist on the invctl host** for PostgreSQL backups, though
  the server does not. The role checks for it during the upgrade preflight and
  fails early with that specific message, rather than at the moment a backup
  is needed.
