<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Deploying invctl with Ansible

One command takes an empty Debian host to a running, healthy invctl. A second
run converges whatever has drifted, restarts only if something really
changed, and after a version bump performs a safe upgrade or refuses —
never half of one.

## After a successful run, nothing answers from outside this host

Say this first because it looks exactly like a broken deployment and it is
not one. `invctl_listen` binds `127.0.0.1:8080` by default and this role
installs no reverse proxy, no TLS certificate and no DNS record. That is
deliberate (spec D3): a proxy already terminates TLS better than this role
would, and certificate renewal is not this role's business.

To confirm the run actually worked, run this **on the host itself**, not from
your workstation:

```
curl http://127.0.0.1:8080/healthz
```

A `curl` from anywhere else timing out or refusing is expected, not a
symptom. When you put a proxy in front, two settings work as a pair and both
matter — `INV_SECURE_COOKIES` (`invctl_secure_cookies` here) and the
`X-Forwarded-Proto` header the proxy must send. See "Two settings behind a
proxy" in `docs/INSTALL.md` for how they interact; setting one without the
other is a documented failure mode, not an edge case.

## Quick start

```
ansible-galaxy collection install -r requirements.yml
cp inventory/hosts.example inventory/hosts
$EDITOR inventory/hosts               # your host, under [invctl]
ansible-playbook -i inventory/hosts playbooks/invctl.yml \
    --ask-vault-pass -e @secrets.vault.yml
```

The collection install is not optional. `tasks/configure.yml` mints the
session key with `lookup('community.general.random_string', ...)`, which runs
on the **control node** — without `community.general` present there, the
first install fails with an opaque "couldn't resolve module/action" error
that names a lookup plugin, not a missing package. `requirements.yml` names
the one collection this role needs; ansible-core alone is not enough.

`invctl_admin_password` is required only for the **first** run against a
given host, and it is a secret: put it in an Ansible Vault file, never in the
inventory. A throwaway host can instead pass it on the command line with
`-e invctl_admin_password=...`. Every later run against that host needs no
credential at all — see "What the role reads back off the host" below.

## The variables

Verbatim from the design spec (§2). No variable outside this table exists;
`defaults/main.yml` is the source of truth if this ever drifts.

| Variable | Default | Notes |
|---|---|---|
| `invctl_version` | `"1.2.0"` | pinned; a repository test fails if this is not the newest `CHANGELOG.md` entry |
| `invctl_source` | `github` | or `files`, for a segmented network — see "Offline / segmented environments" |
| `invctl_listen` | `127.0.0.1:8080` | see D3 above |
| `invctl_db_driver` | `sqlite` | or `postgres` |
| `invctl_db_dsn` | `file:{{ invctl_data_dir }}/invctl.db?_txlock=immediate` | **required** for postgres |
| `invctl_secure_cookies` | `true` | `false` only for a plain-HTTP lab |
| `invctl_admin_username` | `admin` | first run only |
| `invctl_admin_password` | *unset* | **required on a fresh install only**; no default, ever |
| `invctl_admin_users` | `admin` | break-glass; see `docs/RECOVERY.md` |
| `invctl_backup_dir` | `/var/backups/invctl` | |
| `invctl_install_dir` | `/opt/invctl` | matches `docs/INSTALL.md` |
| `invctl_data_dir` | `/var/lib/invctl` | matches `docs/INSTALL.md` |
| `invctl_config_dir` | `/etc/invctl` | |
| `invctl_lock_dir` | `/run/invctl-ansible.lock` | the concurrency guard; see "Concurrency guard" below |
| `invctl_lock_max_age_seconds` | `1800` | how long before an unreleased lock is treated as abandoned |

One more exists beyond the spec's table, added during implementation because
the spec's `github` source turned out to have no way to be tested on a
network with no route to github.com:

| Variable | Default | Notes |
|---|---|---|
| `invctl_release_base_url` | `https://github.com/madalinignisca/invctl/releases/download/v{{ invctl_version }}` | where the `github` source downloads from |

Point it at an internal artefact mirror for a segmented network that cannot
reach github.com but does have a local HTTP server — the middle ground
between the default and copying binaries in by hand with `invctl_source:
files`. Whatever it points at, checksum verification is identical and
mandatory; a mirror is a convenience, never a reason to trust an artefact
more.

## What a second run does — two independent axes (D7)

The **binary** and the **configuration** are handled differently on purpose:

- The **binary** is touched only when `invctl_version` differs from what
  `/opt/invctl/invctl -version` reports on the host: absent → install,
  matching → left alone, different → upgrade.
- The **configuration** — the env file and the systemd unit — is
  re-templated on **every** run, regardless of the binary state. The service
  restarts **only if the rendered file actually differs** from what is
  already there.

This means an out-of-band edit to `/etc/invctl/invctl.env` — someone fixing
it by hand at 2am — is corrected on the next scheduled run, which is the
point of running this on a schedule rather than once. A run where nothing
differs reports `changed=0` and restarts nothing: idempotency is checked, not
assumed.

## What the role reads back off the host, and what that costs you

Two values are read from the host's existing `/etc/invctl/invctl.env` rather
than derived from the inventory, because re-deriving either would produce a
diff on every run, and under D7 a diff means a restart on every run —
exactly what would make a scheduled playbook unusable.

- **`INV_SESSION_KEY`.** Minted once, then kept. Regenerating it on every run
  would sign out every user on every run, and nobody would connect that
  symptom to a scheduled Ansible job.
- **`INV_ADMIN_PASSWORD`**, when no `invctl_admin_password` is supplied on
  that run. This is inert once any account exists — `ensureAdmin` returns
  early — so it does no harm to keep it around, and it is what lets an
  unattended, scheduled run avoid needing a credential in the inventory at
  all after the first install.

The seed password persists in `/etc/invctl/invctl.env`, which is
`0600 root:root`. **It can be deleted by hand without the role putting it
back.** If you remove the `INV_ADMIN_PASSWORD` line after the account
exists, the next run leaves it deleted — the role does not restore it. This
is deliberate: automatically clearing it would be a stateful decision made on
your behalf, and a first install whose seeding half-failed partway through
could then never be retried by re-running the playbook.

This read-back is what makes the constraint on the file's format non-negotiable:
**the role accepts only lines of the exact form `KEY='value'` and refuses the
whole run on anything else** — a double-quoted value, an unquoted value, a
value containing a single quote, a value split across a line break, or the
same key declared twice. Getting this wrong does not raise an error, it signs
everybody out silently, which is why it fails closed instead of guessing.

If you see a refusal naming `/etc/invctl/invctl.env` and a line it cannot
parse: fix that line so it reads exactly `KEY='value'`, or move the file
aside and re-run with `invctl_admin_password` set to seed a new one —
accepting that **every existing session ends**, because the session key goes
with the file.

## "The upgrade did not finish" — the marker

If `/etc/invctl/upgrade-in-progress` exists on the host, an upgrade started
and was never confirmed finished. **The role will do nothing at all while
this file is present** — not even converge configuration — and every run
refuses at its very first task, naming the file and quoting its contents.

This is stricter than it may look, and deliberately so: `invctl` applies
migrations automatically at startup, before binding. If the role instead
walked past the marker and treated a version match as "nothing to do", the
unconditional configuration-and-start step (D7) would start the service —
which would attempt the interrupted migration a second time, outside the
backup gate, against a database in whatever state the first failure left it.

The marker file records: the timestamp the upgrade started, the version
being upgraded from and to, the database driver, and the **path to the
backup taken before anything was replaced**. That backup is the rollback.
Read `docs/UPGRADE.md`, "When a migration fails", decide what happened and
act — most often that means restoring the backup and putting
`/opt/invctl/invctl.<oldversion>` back — and only once you have done that,
clear the marker by hand:

```
rm /etc/invctl/upgrade-in-progress
```

**There is no variable that clears this for you.** That is deliberate:
clearing it is a decision a person makes after looking at what happened, not
a flag that can be left set in an inventory and forgotten.

## Upgrading

Bump `invctl_version` and re-run the playbook. In order, stopping at the
first failure:

1. Stop the service.
2. Back up the database — SQLite: copy `invctl.db` and, if present, its
   `-wal` and `-shm` files, to a name stamped to the second; PostgreSQL:
   `pg_dump --format=custom`.
3. **Gate**: assert the backup exists, is non-empty, and — for SQLite —
   matches the live database byte-for-byte, or — for PostgreSQL — that
   `pg_restore --list` can read it. On any failure, restart the service as it
   was and abort; nothing is replaced.
4. Preserve the outgoing binary as `/opt/invctl/invctl.<oldversion>`.
5. Install the new, checksum-verified binary.
6. Run `invctl -migrate` as the `invctl` user; a non-zero exit aborts.
7. Start the service, then wait for `/healthz` to answer.

Around those seven steps the role adds three things that close gaps the
ordering alone leaves open:

- It **proves the stop**, not just that `systemctl stop` returned — it checks
  systemd's own view and looks for a leftover process still holding the
  binary open, because a copy taken while the writer is still live is a
  torn, silently-wrong backup.
- It backs up **the database the host was actually running**, read from the
  host's own env file, not the one named in the inventory — see "Two changes
  the role will not make in one run" below.
- It marks the host mid-upgrade (see the marker section above) until the new
  version has actually started serving, not merely until the binary swap
  finished.

**There is no rollback command.** `invctl` has none, and this role adds
none. Rolling back means restoring the backup **and** putting
`/opt/invctl/invctl.<oldversion>` back — a new binary against an old schema
is not a supported combination.

**Run with `--check` before a real version bump.** It shows what would
happen — including whether the run would even be permitted to proceed — before
it touches anything.

## What `--check` actually tells you, and what it does not

`--check` reports which of INSTALL / NO-OP / UPGRADE this run would take,
whether the preflight refusals would pass (a fresh install missing
`invctl_admin_password`, a PostgreSQL DSN with no reachable database, mixing a
database change with a version bump, a backup name that would collide with
one already on disk), and what the rendered configuration would be. That is
genuinely useful for rehearsing an upgrade, but it stops there:

**`--check` never downloads or verifies a binary, stops the service, takes a
backup, swaps a binary, or runs a migration.** Every one of those is gated on
`not ansible_check_mode` and replaced with a `debug` describing what a real
run would do instead. This is a deliberate, narrower contract, not an
oversight — earlier, most of that work ran anyway under `--check`, because
two of Ansible's own building blocks do not support check mode the way this
role's design assumes:

- `ansible.builtin.tempfile` has **no** check-mode support at all, so under
  `--check` it is skipped outright and never stages anything — which then
  crashed the role outright (`object of type 'dict' has no attribute 'path'`)
  on the very next task, rather than reporting anything.
- `ansible.builtin.command` has only **partial** check-mode support (the
  `creates`/`removes` workaround, which none of this role's uses of it need):
  every read-only probe this role runs with `command` — the version probe,
  `systemctl show`, the `/proc` leftover scan, `command -v` presence checks,
  `psql ... SELECT 1` — is skipped under `--check` by default, and an
  `assert` reading its `.stdout`/`.rc` right after then throws on an
  undefined attribute instead of failing cleanly.

Two different fixes for two different problems, and they are not
interchangeable:

- The **read-only** probes above are forced to run under `--check` with
  `check_mode: false` on the task itself — safe, because none of them write
  anything, and doing so is what makes the state report (`action=INSTALL` /
  `NO-OP` / `UPGRADE`, the PostgreSQL reachability check, the preflight
  refusals) accurate under `--check` at all.
- Everything that actually **writes** — acquisition and checksum
  verification (`acquire.yml`, both call sites), the stop-and-backup block,
  the marker, the binary swap, and the migration — is skipped whole under
  `not ansible_check_mode`, each replaced with a `debug` naming what would
  have happened and to where. **Do not "fix" this by forcing `tempfile`
  to run under `--check` and letting `get_url`/`copy` continue as normal**:
  `get_url` and `copy` do not populate a real file under check mode either
  (their own check-mode support is `partial`/`full` by *predicting* a
  result, not by actually writing), so the result is not a working preview,
  it is `verify_checksum.yml` asserting a checksum match against a file that
  was never downloaded — trading one crash for a confusing false failure.

`Wait for /healthz to answer 200` (`ansible.builtin.uri`) has **no**
check-mode support and is skipped under `--check` regardless of anything
above — correctly: an operator previewing a change is not meant to get a
live health verdict from a service `--check` never actually started or
reconfigured.

## Concurrency guard

The very first thing this role does — before even looking at
`upgrade-in-progress` — is take an exclusive lock at `invctl_lock_dir`
(`/run/invctl-ansible.lock` by default) by creating that directory with
`mkdir`, the one filesystem primitive that is atomic on its own. **This is a
different mechanism from the upgrade marker, and protects a different
thing**: the marker records that an upgrade did not finish and protects the
*database*; the lock stops two runs from acting on this host *at the same
time* in the first place — an operator re-running what looked like a hang,
landing in the same window as a cron trigger, for instance. Without it, both
runs can read the marker as absent before either has written it, and both
proceed through the preflight, stop, backup and replace concurrently — the
duplicate-backup-name check in `upgrade.yml` only catches a collision on the
destination *name*, after both runs have already raced past every earlier
gate together.

A second run against a locked host is refused immediately, naming the lock
path and when the holding run started. **The lock releases itself when the
run finishes, whether it succeeded or failed** — an `always:` block around
the whole of the role's work removes it either way, so a legitimate failure
(a bad DSN, a refused preflight) does not leave the host locked out for
`invctl_lock_max_age_seconds`. The one case an `always:` block cannot cover
is the control node vanishing outright — a killed `ansible-playbook`, a
severed network — since neither `rescue:` nor `always:` runs if Ansible
never gets to finish talking to the host at all. For that case every lock
records the time it was taken, and a run that finds one older than
`invctl_lock_max_age_seconds` (1800 seconds / 30 minutes by default) treats
it as abandoned, breaks it, and proceeds — loudly, via `debug`, never
silently.

`--check` never takes this lock at all: every mutating action in this role is
already skipped under `--check` (see above), so two concurrent `--check` runs
cannot race each other into a bad state, and acquiring a real lock during a
dry run would itself be the kind of surprise side effect `--check` is not
supposed to have.

## Two changes the role will not make in one run

Changing `invctl_db_dsn` and bumping `invctl_version` in the same run is
refused. The reason: D7 converges configuration *after* the binary work, so
at the point the upgrade preflight runs, the host still holds the *previous*
database configuration. If the role backed up and migrated the database
named in the *new* configuration, it would migrate a database the service
had never used, call that the rollback, and then cut over — leaving the real
data untouched, unbacked-up, and silently abandoned.

Do it in two runs instead:

1. Change `invctl_db_dsn` with `invctl_version` unchanged. Confirm the
   service is healthy against the new database.
2. Bump `invctl_version` in a second run.

## Backups

Backups land in `invctl_backup_dir` (`/var/backups/invctl` by default),
named after the database file they actually came from and stamped to the
second — `elsewhere.db.20260921T120000Z` for a host with a custom DSN, not
always `invctl.db.<stamp>`. **The role refuses to overwrite a backup name
that is already taken**, rather than replacing it: that file may be the only
surviving record of the state before a previous attempt failed, and
overwriting it would destroy the one thing an operator would reach for.
Nothing prunes old backups — that is the operator's job, on whatever
retention policy your GDPR/data-residency posture requires. Keep backups on
EU-based storage; nothing in this role ships one off the host.

## Offline / segmented environments

Set `invctl_source: files` and put exactly two files, with exactly these
names, in `deploy/ansible/roles/invctl/files/` (see that directory's own
`README.md`):

```
invctl_<version>_linux_amd64
invctl_<version>_checksums.txt
```

Download both, on a machine that *can* reach GitHub (or your internal
mirror, via `invctl_release_base_url`), from
`https://github.com/madalinignisca/invctl/releases/download/v<version>/` and
carry them in. **The checksum is verified identically on both paths, and it
is not optional on either.** The checksums file must contain exactly one
record naming `invctl_<version>_linux_amd64` — zero means a wrong or
truncated file, more than one means it was edited or concatenated, and the
role refuses rather than guessing which line to trust.

Neither file is committed to this repository — `files/.gitignore` keeps a
~20 MB binary out of the history, where it would be permanent.

## PostgreSQL

**The role never installs a PostgreSQL server** — a role that did would also
own that server's version, upgrades, tuning and backups, which is a second
product hiding inside a deployment role. Point `invctl_db_dsn` at a database
someone else manages.

`postgresql-client` must be installed on the invctl host itself (not the
database host) — the role asserts this and names the package, but does not
install it. It provides three binaries the role needs at three different
moments: `psql` (confirming the database is reachable, before anything is
written), `pg_dump` (the upgrade backup), and `pg_restore --list` (proving
that backup is actually readable, not merely non-empty).

**This branch is written but has not been exercised against a real
PostgreSQL server** — the test environment for this role had no database
server in scope. The SQLite path has been run end to end, including
mutation-tested failure paths for the stop-proof and the backup gate; the
PostgreSQL path has been read carefully and follows the same structure, but
an operator relying on it for a first production upgrade should treat it as
less proven and rehearse it against a disposable copy first.

**On PostgreSQL, reachability is checked on every run, not only install or
upgrade.** `tasks/main.yml` runs `psql ... SELECT 1` before any configuration
is written, on every plain convergence run as well as an install or upgrade —
this is a deliberate consequence of D7 (the configuration axis, including
this preflight, runs unconditionally), not a side effect nobody noticed. It
means a transient network blip between the invctl host and the database
fails an otherwise routine scheduled run that would have changed nothing.
The alternative — checking reachability only when the binary axis is about
to do something — would let a scheduled convergence run silently stop
verifying that the configured database is actually reachable at all, which
is a worse failure to have hidden. If your PostgreSQL host has a history of
brief blips, budget for an occasional failed convergence run rather than
loosening this check.

## Non-goals

Copied from the design spec — this role deliberately does not do these, and
adding any of them needs a new decision, not a patch to this role:

- No reverse proxy, no TLS certificate, no DNS record.
- No database server — SQLite is file-based and needs none; PostgreSQL must
  already exist elsewhere.
- No firewall management.
- No rollback automation — rollback is restoring a backup and a binary, by a
  person, deliberately.
- `linux/amd64` only.
- One host. This is not a fleet tool; it targets a handful of hosts run
  individually, matching `deploy/ansible/inventory/hosts.example`'s single
  `[invctl]` group.
