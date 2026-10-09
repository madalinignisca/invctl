<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Ansible deployment — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One command takes an empty Debian host to a running, healthy invctl; a
second run converges whatever has drifted, restarts only if something really
changed, and after a version bump performs a safe upgrade or refuses — never
half of one, and never *past* a half of one.

**Architecture:** A single Ansible role, `deploy/ansible/roles/invctl`, in this
repository rather than its own — D4's version-sync check has to read both
`CHANGELOG.md` and the role's `defaults/main.yml`, and a check that spans two
repositories is a check nobody runs. **Two independent axes, and keeping them
independent is the whole shape of the role (D7).** The *binary* is a three-state
machine keyed on what `/opt/invctl/invctl -version` reports on the target:
absent → INSTALL, equal → NO-OP, different → UPGRADE. The *configuration* is not
a state machine at all: the env file and the unit are templated on every run,
and a restart fires only when a template actually reports `changed`. The upgrade
path's safety is its step *ordering* plus two things the ordering alone does not
give: a **durable marker** so a run cannot proceed past an upgrade that was
interrupted, and reading the **running** database configuration off the host
rather than trusting the inventory's.

**Tech Stack:** `ansible` core 2.21.4 (`ansible.builtin`, plus
`community.general` 13.4.0 for the `incus` connection plugin and the
`random_string` lookup), `incus` 6.0.0 for the throwaway test container, Go 1.26
for the one test that lives in `make test`. No molecule. No ansible-lint. No
`community.postgresql`. No `sqlite3` CLI on the target. All were checked for on
this host; molecule and ansible-lint are absent and adding either needs
sign-off, `community.postgresql` is explicitly rejected by D2 as a new
dependency for one query, and the SQLite backup is validated without a client
(Task 6).

**Spec:** `docs/superpowers/specs/2026-09-21-ansible-deployment-design.md`, **at
`c30da28` or later** — read it before Task 1. That commit adds **D7** and fixes
the D6/§4 contradiction, and this plan is written against the amended text.

**Revision:** this plan was rewritten after an adversarial review found ten
defects in the previous draft. Three of them — an interrupted upgrade being
walked past on the next run, backing up the wrong database, and a backup name
that overwrites its predecessor — share one shape: **the role's safety depended
on state it did not read back or protect.** Where a fix needed a new test, the
test is in here and so is the mutation that proves it can fail. The comments
those fixes carry are written for whoever inherits this, because none of the
three was visible in a diff.

---

## Global Constraints

Copied verbatim from the spec and from `CLAUDE.md`. Every task's requirements
include these.

### D7 — configuration converges on every run; only the binary has a NO-OP

The decision the rest of this plan is shaped by. From the spec:

> - The config file and the unit file are templated on **every** run.
> - A restart happens when, and only when, one of them actually changed.
> - The binary's version still drives INSTALL / NO-OP / UPGRADE independently.
>
> A run where nothing differs is still zero changed tasks and no restart. A run
> where the DSN changed rewrites the file and restarts.

And the reasoning, which is the part that stops somebody "simplifying" it back:

> A role that cannot converge configuration is not idempotent, it is inert, and
> convergence is the reason to use Ansible rather than a shell script.

**Where §4's NO-OP paragraph still reads "No file changes, no handler fires, no
restart", D7 governs.** That sentence predates D7 and describes the *binary*
axis; §7 scenario 2 likewise. The property that survives is narrower and
correct: *a run with identical inputs* changes nothing and restarts nothing.

**D7 also has a sharp edge, and the role handles it explicitly.** Because
configuration converges *after* the binary work, an upgrade runs while the host
still holds the *previous* configuration. The database the stopped service was
using is therefore the one in the host's env file, not necessarily the one in
the inventory. Task 6 reads it back and refuses a run that would change both at
once. Without that, changing `invctl_db_dsn` and `invctl_version` together backs
up and migrates the **new** database and then cuts over to it, losing nothing
visibly and everything actually.

### The layout, exactly (spec §1)

```
deploy/ansible/
  README.md                     operator documentation, including offline
  playbooks/invctl.yml          hosts + role, nothing else
  inventory/hosts.example
  roles/invctl/
    defaults/main.yml
    tasks/main.yml              detect state, dispatch
    tasks/acquire.yml           fetch or read the binary, then include:
    tasks/verify_checksum.yml   the ONE verification both paths share (D5)
    tasks/install.yml           user, directories, binary, unit, config
    tasks/upgrade.yml           stop, back up, gate, replace, migrate
    tasks/verify.yml            start and wait for /healthz
    templates/invctl.service.j2
    templates/invctl.env.j2
    files/.gitkeep              where an operator drops an offline binary
    files/README.md             the two exact filenames expected here
    handlers/main.yml
```

**Two files are added beyond this, and both for the same reason the spec itself
gave for `verify_checksum.yml`: a thing that must be done identically in more
than one place is one file, not two copies.**

- **`tasks/configure.yml`.** D7 makes templating a per-run concern shared by all
  three binary states, and §1's layout predates D7 — it still describes the unit
  and the config as something `install.yml` does. They cannot live there, since
  `install.yml` runs only on a fresh install. Sixty lines of templating in
  `main.yml` would make the file documented as "detect state, dispatch" do
  neither.
- **`tasks/read_env.yml`.** Two callers need the host's *existing* configuration
  parsed: `configure.yml`, to read back the session key and the seed password,
  and `upgrade.yml`, to learn which database the service was actually using. The
  parser has to be strict and fail closed (see below), and a strict parser
  written twice is a strict parser that is strict in one place. Included once
  from `main.yml`; both callers read the result.

### Reading the host's env file: strict, and fail closed

This file holds `INV_SESSION_KEY`. Getting its parsing wrong does not throw an
error, it signs every user out — so the rules are narrow on purpose:

- A file that **exists and cannot be read** is a reason to stop, never a reason
  to treat it as empty. `slurp` runs without `failed_when: false`.
- The only accepted line forms are a comment, a blank line, and exactly
  `KEY='value'` with no single quote inside the value. Double-quoted, unquoted,
  whitespace-padded, truncated and duplicated assignments are **malformed**, and
  malformed is a refusal rather than a guess. A truncated `INV_SESSION_KEY='abc`
  passes any "does the file mention the key" test and then yields the malformed
  text as the value.
- Only "the file is absent" means "this is a first install".

### The variables, exactly (spec §2)

| Variable | Default | Notes |
|---|---|---|
| `invctl_version` | `"1.2.0"` | pinned; D4 keeps it honest |
| `invctl_source` | `github` | or `files` |
| `invctl_listen` | `127.0.0.1:8080` | D3 |
| `invctl_db_driver` | `sqlite` | or `postgres` |
| `invctl_db_dsn` | `file:{{ invctl_data_dir }}/invctl.db?_txlock=immediate` | **required** for postgres (D2) |
| `invctl_secure_cookies` | `true` | false only for a plain-HTTP lab |
| `invctl_admin_username` | `admin` | first run only |
| `invctl_admin_password` | *unset* | **required on a fresh install**; no default, ever |
| `invctl_admin_users` | `admin` | break-glass; see `docs/RECOVERY.md` |
| `invctl_backup_dir` | `/var/backups/invctl` | |
| `invctl_install_dir` | `/opt/invctl` | matches `docs/INSTALL.md` |
| `invctl_data_dir` | `/var/lib/invctl` | matches `docs/INSTALL.md` |
| `invctl_config_dir` | `/etc/invctl` | |

**No variable outside this table is introduced.** The service user and group are
the literal `invctl` that `docs/INSTALL.md` prescribes and the unit hardcodes;
the release URL is the literal from spec §3; the health-check retry budget is
fixed. In particular there is **no variable that clears the interrupted-upgrade
marker** — clearing it is a deliberate human act with a documented command, not
a flag somebody can leave set in an inventory.

**`invctl_admin_password` is required only on a fresh install** (spec §2), since
`ensureAdmin` returns early once any account exists. Demanding it on every run
would mean keeping a credential in the inventory purely to satisfy an assertion,
and would stop the playbook running unattended — which is the thing D7 otherwise
makes possible. **When there is no effective password the line is omitted from
the rendered file entirely**, so an operator who deletes it by hand finds it
still deleted after the next run. The role does *not* remove it automatically
after a successful install: that would be stateful, and a first install whose
seeding half-failed could then never retry.

### The state machine, exactly (spec §4, as amended by D7)

```
/opt/invctl/invctl absent            -> INSTALL
present, -version == invctl_version  -> NO-OP (binary untouched)
present, -version != invctl_version  -> UPGRADE
```

Configuration is templated on every run regardless of which of those three it
is — **unless an interrupted upgrade is marked, in which case the run refuses
before doing anything at all.**

**UPGRADE — in order, stopping at the first failure:**

1. Stop the service.
2. Back up. SQLite: copy `invctl.db` and, if present, `-wal` and `-shm`, to a
   timestamped name. PostgreSQL: `pg_dump --format=custom`.
3. **Gate.** Assert the backup file exists and its size is greater than zero.
   On failure: restart the service as it was and abort the play.
4. Preserve the outgoing binary as `/opt/invctl/invctl.<oldversion>`.
5. Install the new binary.
6. Run `invctl -migrate` as the `invctl` user; a non-zero exit aborts.
7. Start, then §6.

> Step 3 before step 4 is the whole design. Any other order can leave a host
> with a new binary and no way back.

**The role does three things the spec's seven steps do not name, and each closes
a hole the ordering alone leaves open.** They are additions in service of D1's
stated property — "the failure mode of this role is *nothing happened*, never
*half an upgrade*" — not departures from it:

- **A durable marker** written immediately before step 4 and cleared only after
  step 6 succeeds *and* the health check passes. Without it, a `-migrate` that
  fails leaves the new binary on disk and the service stopped; the next run sees
  a version that matches the pin, takes the NO-OP path, and D7's unconditional
  `configure.yml` + `verify.yml` **starts the service** — which migrates
  automatically at startup, outside the backup gate. The backup from the
  interrupted attempt does still exist, so this is not unrecoverable; it is the
  role walking silently past a half-finished upgrade, which is worse than
  refusing.
- **The backup and the migration use the database the *host* was running**, read
  from its env file, not the one in the inventory. See the D7 edge above.
- **The gate proves the backup is usable, not merely non-empty.** A truncated or
  corrupt file has a non-zero size.

On step 6, the spec is now explicit, and it is the resolution of what was a
contradiction with D6:

> The config file is `0600 root:root`, so the `invctl` user cannot read its own
> configuration — and `-migrate` runs after `config.Load`, so it needs
> `INV_DB_DRIVER` and `INV_DB_DSN`. The two database variables are therefore
> passed explicitly to that one command, from Ansible, with `no_log`, rather
> than by making the file readable. Running the migration as **root** is not the
> way out: on SQLite it would create root-owned `-wal` and `-shm` files next to
> the database, which the service then cannot write, converting a successful
> migration into a service that will not start.

### Verified facts — do not re-derive

- Release assets are `invctl_<version>_linux_amd64` and
  `invctl_<version>_checksums.txt`, under
  `https://github.com/madalinignisca/invctl/releases/download/v<version>/`.
- The checksums file is standard `sha256sum` output: `<hash>  <filename>`, **two
  spaces**, and the filename is bare — no directory component.
- `invctl -version` prints
  `invctl v1.2.0 (commit bc48c6affb18, built 2026-09-18T23:16:04Z, linux/amd64, go1.26.6)`.
  The **second whitespace-separated field** is the version, with a leading `v`
  to strip. It is printed **before `config.Load`** (`cmd/invctl/main.go:99-105`),
  so it works on a box with no environment set at all — which is what makes it
  usable as the state probe.
- `invctl -migrate` applies migrations and exits. It runs **after**
  `config.Load` (`cmd/invctl/main.go:125`), so it needs `INV_DB_DRIVER` and
  `INV_DB_DSN` in its environment. Migrations also run automatically at startup
  before binding, and a failure exits rather than serving. **That automatic
  startup migration is why the marker in Task 6 exists**: starting the service
  is itself a migration, performed outside any gate.
- `GET /healthz` needs no authentication (`internal/web/routes.go:136`).
- Current latest release is 1.2.0; that is the initial `invctl_version`.
- The newest heading in `CHANGELOG.md` is `## [1.2.0] — 2026-09-19` (em dash).
- `psql`, `pg_dump` and `pg_restore` all come from `postgresql-client`. The role
  asserts they are present and names the package; it never installs it, and
  never installs a database server (D2).
- A SQLite database file begins with the 16 bytes `SQLite format 3\0`. The first
  15 are printable ASCII, which is what the role checks — see Task 6 for why the
  NUL is left out.

### Repository rules that bite here

- **Licence header on every new file.** `internal/license/guard_test.go` enforces
  `.go .sql .html .css .js` only, so YAML is not checked — but
  `docker-compose.yml`, `.github/workflows/ci.yml` and `.github/workflows/release.yml`
  all carry it, and new YAML and shell files carry it too. Markdown in `docs/`
  and in `deploy/` carries it in an HTML comment. In Go, a **blank line** follows
  the notice before the `package` clause.
- **`make test` is the gate**, not `go test ./...`. The Go test in Task 1 runs
  inside it and needs no database, but the gate is still `make test`.
- `gofmt`, `go vet`, `staticcheck`/`golangci-lint` clean for the Go file.
- `go` is not on `PATH` by default here: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin`.
- **Restore a mutation with `cp` from a saved copy, never `git checkout --`.**
  That command has destroyed uncommitted work in this repository.

### The test host, and the rule about it

- **Incus needs `sudo` on this host.** Plain `incus` fails with a socket
  permission error; `sudo -n incus ...` works passwordless. **Every Incus command
  in this plan is written `sudo incus ...`.** Do not add the user to a group and
  do not change daemon permissions — that is a host change nobody asked for.
- **There is an unrelated container already running here, `mailadmin-test`,
  belonging to different work.** The test container is named
  **`invctl-ansible-test`** and teardown names it explicitly:
  `sudo incus delete --force invctl-ansible-test`. **No `--all`, no loop over
  `incus list`, no pattern.** A pattern that matches more than you meant is how
  unrelated work gets destroyed — the same trap as the `pkill` self-match this
  environment has already been bitten by twice.
- **Teardown must run even when the test fails**, or a failed run leaves the
  container behind and the next run collides on the name. Task 2 delivers this
  as an idempotent `down` subcommand, and every scenario ends with one.
- **Several scenarios in Task 6 recreate the container rather than reusing it.**
  That is deliberate and is cheap; reusing a host whose schema has already been
  migrated forward would mean testing a downgrade, which this software does not
  support, and a test that can fail for an unsupported reason is a test that
  proves nothing about its own subject.
- **Never target an existing host.** The only inventory this plan's tests use is
  `deploy/ansible/inventory/incus-test.ini`, which contains exactly one entry
  and that entry is `invctl-ansible-test`.

---

## File structure

| File | Responsibility |
|---|---|
| `deploy/ansible/roles/invctl/defaults/main.yml` | the variable table above, and only it |
| `internal/license/role_version_pin_test.go` | D4 — the pin equals the newest changelog heading |
| `deploy/ansible/test/incus-harness.sh` | create, bootstrap and **destroy** `invctl-ansible-test` |
| `deploy/ansible/test/README.md` | the scenarios and what each proves |
| `deploy/ansible/inventory/incus-test.ini` | the one disposable target |
| `deploy/ansible/inventory/hosts.example` | what an operator copies |
| `deploy/ansible/playbooks/invctl.yml` | hosts + role, nothing else |
| `deploy/ansible/roles/invctl/tasks/main.yml` | **marker refusal**, probe, read_env, preflight, dispatch, then always configure and verify |
| `deploy/ansible/roles/invctl/tasks/read_env.yml` | strict, fail-closed parse of the host's env file |
| `deploy/ansible/roles/invctl/tasks/acquire.yml` | github or files, then the shared verify |
| `deploy/ansible/roles/invctl/tasks/verify_checksum.yml` | D5 — the **one** verification |
| `deploy/ansible/roles/invctl/tasks/install.yml` | user, directories, binary. **No templates** |
| `deploy/ansible/roles/invctl/tasks/upgrade.yml` | preflight, stop, back up, **gate**, marker, replace, migrate |
| `deploy/ansible/roles/invctl/tasks/configure.yml` | **D7** — env file and unit, every run |
| `deploy/ansible/roles/invctl/tasks/verify.yml` | start, then poll `/healthz` |
| `deploy/ansible/roles/invctl/templates/invctl.service.j2` | `docs/INSTALL.md`'s unit, with `EnvironmentFile=` |
| `deploy/ansible/roles/invctl/templates/invctl.env.j2` | D6 — `0600 root:root` |
| `deploy/ansible/roles/invctl/handlers/main.yml` | `reload systemd`, `restart invctl` |
| `deploy/ansible/roles/invctl/files/README.md`, `files/.gitkeep`, `files/.gitignore` | the two exact filenames; keep a 20 MB binary out of git |
| `deploy/ansible/README.md` | D3 — operator documentation, including the loud sentence |
| `CHANGELOG.md`, `docs/INSTALL.md` *(modify)* | announce the role and point at it |

---
### Task 1: The pinned version, and the Go test that keeps it honest (D4)

**Files:**
- Create: `deploy/ansible/roles/invctl/defaults/main.yml`
- Create: `internal/license/role_version_pin_test.go`

**Interfaces:**
- Produces: every variable in the Global Constraints table, at the defaults the
  spec names. Every later task reads these and adds none.
- Produces: a `make test` failure when `CHANGELOG.md` gains a version heading the
  role has not been bumped to.

**Why this is first, and its own task.** It is the one deliverable with no
dependency on a container, a network or a role that runs, and it is the one a
reviewer can accept or reject on its own. `internal/license` is already the home
of the checks that read the repository's own *files* rather than its behaviour —
`TestTheLinterPinMatchesCI` is the precedent and says so in its own doc comment.
A version stated in two places is exactly the shape that needs something reading
both.

- [ ] **Step 1: Write `defaults/main.yml`**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# PINNED, AND A TEST KEEPS IT HONEST. internal/license/role_version_pin_test.go
# fails when CHANGELOG.md's newest version heading is not this number, so
# cutting a release without bumping this line turns CI red and names the file to
# edit. The release workflow deliberately does NOT write here: having a release
# commit to the repository adds a new way for the one process most worth keeping
# boring to go wrong (spec D4).
invctl_version: "1.2.0"

# github downloads the release assets; files reads them from this role's files/
# directory for a segmented environment. BOTH verify the checksum, through the
# same task file (spec D5).
invctl_source: github

# invctl binds plaintext HTTP and this role installs no proxy, no certificate
# and no DNS (spec D3). After a successful run NOTHING answers from outside this
# host. That is correct; see deploy/ansible/README.md.
invctl_listen: "127.0.0.1:8080"

invctl_db_driver: sqlite

# Required when invctl_db_driver is postgres, and the role fails naming it. The
# role never installs a database server: a role that installs one also owns its
# version, upgrades, backups and tuning, which is a second product hiding inside
# a deployment role (spec D2).
invctl_db_dsn: "file:{{ invctl_data_dir }}/invctl.db?_txlock=immediate"

# Correct behind TLS, which is where this is expected to end up. Set false only
# for a plain-HTTP lab -- docs/INSTALL.md, "Two settings behind a proxy".
invctl_secure_cookies: true

invctl_admin_username: admin

# invctl_admin_password HAS NO DEFAULT, ON PURPOSE, and never will. A default
# admin password in a deployment role is a default admin password in production.
#
# It is required ONLY on a fresh install (spec §2): ensureAdmin returns early
# once any account exists, so demanding it on every run would mean keeping a
# credential in the inventory to satisfy an assertion and nothing else -- and
# would stop the playbook running unattended, which D7 otherwise makes possible.
# On later runs tasks/configure.yml reads the value back off the host.

# Break-glass. Read docs/RECOVERY.md before deciding what goes here.
invctl_admin_users: admin

invctl_backup_dir: /var/backups/invctl
invctl_install_dir: /opt/invctl
invctl_data_dir: /var/lib/invctl
invctl_config_dir: /etc/invctl
```

- [ ] **Step 2: Write the failing test**

Create `internal/license/role_version_pin_test.go`:

```go
// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package license

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var (
	// The pin, as defaults/main.yml states it. Quotes are optional in YAML and
	// both spellings are accepted, because a future edit that drops them is a
	// formatting change and must not silently disable this check.
	rolePin = regexp.MustCompile(`(?m)^invctl_version:\s*"?([0-9]+\.[0-9]+\.[0-9]+)"?\s*$`)
	// The newest released version. CHANGELOG.md is newest-first, so the FIRST
	// match is the one. An `## [Unreleased]` heading cannot match: the pattern
	// requires digits, which is why it is spelled this way rather than
	// `\[([^\]]+)\]`.
	changelogNewest = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\]`)
)

// TestTheAnsibleRolePinMatchesTheChangelog fails when the deployment role pins
// a version that is not the newest release.
//
// THE ALTERNATIVES WERE BOTH WORSE. A line in a release checklist depends on
// somebody reading it on a busy day, which is the failure mode this repository
// keeps replacing with tests. Having the release workflow edit the variable and
// commit adds a new way for a release to go wrong, during the one process most
// worth keeping boring (spec D4).
//
// The consequence of it going stale is quiet rather than loud: the role keeps
// deploying a version that is no longer current, and nothing anywhere says so.
// A silent wrong answer is the class of failure that needs a test rather than a
// convention.
//
// EQUALITY, NOT "NOT BEHIND", AND DELIBERATELY. A release bumps the changelog
// heading and this pin in the same commit, so equality holds through a release.
// Pinning the role to a version that has no changelog entry should fail: it
// would deploy something no release note describes.
//
// Deliberately in internal/license, beside TestTheLinterPinMatchesCI: this
// package is already where the checks that read the repository's own files
// live, and a version pin is exactly that kind of fact.
func TestTheAnsibleRolePinMatchesTheChangelog(t *testing.T) {
	root := repoRoot(t)

	defaults, err := os.ReadFile(filepath.Join(root,
		"deploy", "ansible", "roles", "invctl", "defaults", "main.yml"))
	if err != nil {
		t.Fatalf("reading the role defaults: %v", err)
	}
	pin := rolePin.FindSubmatch(defaults)
	if pin == nil {
		t.Fatal("the role defaults declare no invctl_version -- if the pin moved, " +
			"this test must move with it rather than being deleted: a version stated " +
			"in two places is exactly the shape that needs something reading both")
	}

	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("reading the changelog: %v", err)
	}
	newest := changelogNewest.FindSubmatch(changelog)
	if newest == nil {
		t.Fatal("CHANGELOG.md has no `## [x.y.z]` heading")
	}

	if string(pin[1]) != string(newest[1]) {
		t.Errorf("the Ansible role pins invctl %s and the newest release is %s.\n"+
			"Edit deploy/ansible/roles/invctl/defaults/main.yml and set\n"+
			"    invctl_version: \"%s\"\n"+
			"A role pinned behind the releases deploys an old version and says nothing "+
			"about it, which is why this is a test and not a checklist line (spec D4).",
			pin[1], newest[1], newest[1])
	}
}
```

- [ ] **Step 3: Run it and watch it pass, then prove it can fail**

```bash
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
cd /home/gabriel/apps/infra-inventory
go test ./internal/license/ -run TestTheAnsibleRolePinMatchesTheChangelog -count=1 -v
```
Expected: PASS (1.2.0 == 1.2.0).

Now the part that matters. **A test that has never been observed failing is a
claim, not a check.**

```bash
cp deploy/ansible/roles/invctl/defaults/main.yml /tmp/defaults.main.yml.bak
sed -i 's/^invctl_version: "1.2.0"/invctl_version: "1.1.1"/' \
  deploy/ansible/roles/invctl/defaults/main.yml
go test ./internal/license/ -run TestTheAnsibleRolePinMatchesTheChangelog -count=1
```
Expected: FAIL, naming `deploy/ansible/roles/invctl/defaults/main.yml` and the
number to write.

```bash
cp /tmp/defaults.main.yml.bak deploy/ansible/roles/invctl/defaults/main.yml
go test ./internal/license/ -run TestTheAnsibleRolePinMatchesTheChangelog -count=1
```
Expected: PASS again. **`cp`, never `git checkout --`.**

- [ ] **Step 4: Run the gate**

```bash
make test 2>&1 | tail -30
make lint 2>&1 | tail -20
```
Expected: green, and `internal/license` in the `ok` list.

- [ ] **Step 5: Commit**

```bash
git add deploy/ansible/roles/invctl/defaults/main.yml internal/license/role_version_pin_test.go
git commit -m "deploy: pin invctl 1.2.0 in the role, and test it against the changelog"
```

---

### Task 2: The disposable Incus container, and getting rid of it

**Files:**
- Create: `deploy/ansible/test/incus-harness.sh`
- Create: `deploy/ansible/inventory/incus-test.ini`
- Create: `deploy/ansible/inventory/hosts.example`

**Interfaces:**
- Consumes: nothing.
- Produces: `deploy/ansible/test/incus-harness.sh up|down|run`. Every later task
  tests against it and nothing else. `run` wraps `sudo -n ansible-playbook` and
  forwards its arguments, so no later step types a bare `ansible-playbook` at a
  container it has to remember to name.

**Why this is its own task, before any role logic exists.** Everything after it
needs a target, and the rules about *which* target are the ones with a blast
radius. There is an unrelated `mailadmin-test` container on this host belonging
to different work; the danger is not that the role is wrong, it is that a
cleanup pattern matches something it did not mean to.

- [ ] **Step 1: Write the harness**

```bash
#!/usr/bin/env bash
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

# A throwaway Debian container to apply the invctl role to, and the one command
# that destroys it.
#
# THE NAME IS A CONSTANT AND NOT AN ARGUMENT. This host runs containers
# belonging to unrelated work, and the way unrelated work gets destroyed is a
# cleanup pattern that matches more than it meant to -- `--all`, a loop over
# `incus list`, a grep. Teardown here names one container, literally, and can
# therefore delete nothing else however badly it is invoked.
#
# incus needs sudo on this host: the socket is not readable by this user. That
# is deliberate and is not to be "fixed" by adding anybody to a group.

set -euo pipefail

CONTAINER=invctl-ansible-test
IMAGE=images:debian/13
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANSIBLE_DIR="$(dirname "$HERE")"

usage() {
	cat <<'USAGE'
usage: incus-harness.sh up | down | run [ansible-playbook args...]

  up    create invctl-ansible-test and make it usable by Ansible
  down  delete invctl-ansible-test, and only it. Safe to run twice.
  run   sudo ansible-playbook against invctl-ansible-test

Always finish with `down`. If a test aborted, run `down` by hand: a container
left behind makes the next `up` collide on the name.
USAGE
}

up() {
	if sudo incus info "$CONTAINER" >/dev/null 2>&1; then
		echo "$CONTAINER already exists; run 'down' first" >&2
		exit 1
	fi
	sudo incus launch "$IMAGE" "$CONTAINER"
	# systemd has to be up before systemctl means anything inside. --wait
	# returns non-zero for "degraded", which a minimal container often is and
	# which is not a reason to stop.
	sudo incus exec "$CONTAINER" -- systemctl is-system-running --wait || true
	# Ansible modules are Python. The image may or may not carry python3; this
	# is idempotent either way, and it is the ONLY thing the container needs
	# from the internet in the offline (files) scenario.
	sudo incus exec "$CONTAINER" -- sh -c \
		'apt-get update -qq && apt-get install -y -qq python3 ca-certificates'
	echo "$CONTAINER is ready"
}

down() {
	# --force stops it first. Missing is success: teardown runs after a failure,
	# and a teardown that fails because there was nothing to tear down turns a
	# test failure into two.
	sudo incus delete --force "$CONTAINER" 2>/dev/null || true
	echo "$CONTAINER is gone"
}

run() {
	# sudo, because the incus connection plugin shells out to `incus` and the
	# socket is root-only here. community.general lives in
	# /usr/lib/python3/dist-packages, which root sees.
	sudo -n ansible-playbook \
		-i "$ANSIBLE_DIR/inventory/incus-test.ini" \
		"$ANSIBLE_DIR/playbooks/invctl.yml" \
		"$@"
}

case "${1:-}" in
up) up ;;
down) down ;;
run)
	shift
	run "$@"
	;;
*)
	usage
	exit 1
	;;
esac
```

```bash
chmod +x deploy/ansible/test/incus-harness.sh
```

- [ ] **Step 2: Write the two inventories**

`deploy/ansible/inventory/incus-test.ini`:

```ini
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

# THE ONLY TARGET ANY TEST IN THIS REPOSITORY EVER USES, and it is a container
# created and destroyed by deploy/ansible/test/incus-harness.sh. One entry, by
# name, so a stray -i cannot reach anything real.
[invctl]
invctl-ansible-test ansible_connection=community.general.incus ansible_incus_remote=local ansible_python_interpreter=/usr/bin/python3
```

`deploy/ansible/inventory/hosts.example`:

```ini
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

# Copy to hosts, edit, and keep it out of this repository.
#
# invctl_admin_password is NOT here and must not be put here in plaintext: it is
# the first administrator's password. Pass it from a vault, or on the command
# line for a throwaway. It is needed only for the FIRST run against a host; the
# role reads it back off the host afterwards, so scheduled runs need no
# credential at all.
[invctl]
inventory.example.com

[invctl:vars]
ansible_user=root
```

- [ ] **Step 3: Prove the harness works, and prove teardown works**

```bash
cd /home/gabriel/apps/infra-inventory/deploy/ansible
./test/incus-harness.sh up
sudo incus exec invctl-ansible-test -- python3 --version
sudo incus exec invctl-ansible-test -- systemctl --version | head -1
```
Expected: a Python 3.x and a systemd version.

Now confirm nothing else is in range:

```bash
sudo incus list --format csv -c n
```
Expected: `mailadmin-test` **and** `invctl-ansible-test`, both present. Note
`mailadmin-test` is somebody else's and must still be there at the end of every
task in this plan.

```bash
./test/incus-harness.sh down
./test/incus-harness.sh down   # twice: teardown after a failure must be safe
sudo incus list --format csv -c n
```
Expected: `invctl-ansible-test` gone, `mailadmin-test` untouched, and the second
`down` exits 0.

- [ ] **Step 4: Commit**

```bash
git add deploy/ansible/test deploy/ansible/inventory
git commit -m "deploy(test): a throwaway Incus container, and one command that deletes it"
```

---

### Task 3: Acquiring the binary, and the one verification both paths share (D5)

**Files:**
- Create: `deploy/ansible/roles/invctl/tasks/acquire.yml`
- Create: `deploy/ansible/roles/invctl/tasks/verify_checksum.yml`
- Create: `deploy/ansible/roles/invctl/files/README.md`
- Create: `deploy/ansible/roles/invctl/files/.gitkeep`
- Create: `deploy/ansible/roles/invctl/files/.gitignore`

**Interfaces:**
- Consumes: `invctl_version`, `invctl_source` from Task 1.
- Produces: `invctl_stage.path` — a temporary directory on the target holding a
  **verified** `invctl_<version>_linux_amd64`. Tasks 4 and 6 copy from there and
  never verify again, because verification already happened exactly once.

**D5 is the point of this task.** Both sources end at the same tasks: find the
record for this binary, compute the real hash, assert they are equal. Two
verification code paths means the rarely-exercised one eventually stops
verifying and nobody notices, because the symptom is silence.

**The record must be matched whole.** A substring match on the filename accepts
a line for `other_invctl_1.2.0_linux_amd64`, `invctl_1.2.0_linux_amd64.sig` or
anything else ending in the right characters — which hands the verification the
hash of a different file and lets the wrong binary through with a clean bill of
health. That is precisely the outcome D5 exists to prevent, so the pattern
anchors both ends, spells the two spaces, escapes the version's dots, and the
role asserts that **exactly one** line matches.

- [ ] **Step 1: Write `files/README.md`, `.gitkeep` and `.gitignore`**

`files/README.md` (the outer fence is four backticks because the content itself
contains fenced blocks):

````markdown
<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Offline binaries go here

Set `invctl_source: files` and put **exactly these two files** in this
directory, with exactly these names, where `<version>` is `invctl_version` from
`defaults/main.yml`:

```
invctl_<version>_linux_amd64
invctl_<version>_checksums.txt
```

For 1.2.0 that is `invctl_1.2.0_linux_amd64` and
`invctl_1.2.0_checksums.txt`. Download both from

```
https://github.com/madalinignisca/invctl/releases/download/v1.2.0/
```

on a machine that can reach it, and carry them in.

**Both files. The checksum is verified here exactly as it is on the online
path** — the offline path exists because a segmented environment cannot reach
GitHub, not because integrity checking is inconvenient there. A binary carried
in on a laptop has had more hands on it than one fetched over TLS, not fewer.

Do not add other files to the checksums file or rename the binary. The role
requires exactly one record naming exactly `invctl_<version>_linux_amd64` and
refuses when it finds none or more than one.

Neither file is committed: `.gitignore` here keeps a 20 MB binary out of the
repository's history, where it would be permanent.
````

`files/.gitignore`:

```
# The release artefacts an operator drops here are large and are not ours to
# version. A binary committed by accident is permanent in the history.
invctl_*_linux_amd64
invctl_*_checksums.txt
```

`files/.gitkeep`: empty. Git does not track a directory, and an operator
following README.md must find `files/` already there rather than create it and
wonder whether the name is right.

- [ ] **Step 2: Write `acquire.yml`**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# Put a verified binary in a staging directory on the target. Two sources, ONE
# verification -- see verify_checksum.yml (spec D5).
#
# Nothing here touches /opt/invctl. An acquire that fails leaves the installed
# binary exactly as it was, which is what lets the upgrade path treat "we have
# the new binary" and "we have replaced the old one" as two separate facts.
#
# Included only by install.yml and upgrade.yml, never unconditionally: on a
# NO-OP run nothing is downloaded and nothing is staged, which is most of why a
# scheduled run costs nothing.

- name: Stage the release artefacts somewhere temporary
  ansible.builtin.tempfile:
    state: directory
    suffix: invctl-acquire
  register: invctl_stage
  changed_when: false

- name: Download the release binary from GitHub
  ansible.builtin.get_url:
    url: "https://github.com/madalinignisca/invctl/releases/download/v{{ invctl_version }}/invctl_{{ invctl_version }}_linux_amd64"
    dest: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_linux_amd64"
    mode: "0644"
  when: invctl_source == 'github'

- name: Download the checksums file from GitHub
  ansible.builtin.get_url:
    url: "https://github.com/madalinignisca/invctl/releases/download/v{{ invctl_version }}/invctl_{{ invctl_version }}_checksums.txt"
    dest: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_checksums.txt"
    mode: "0644"
  when: invctl_source == 'github'

# "Which file, where" is the ENTIRE question an air-gapped operator has, so the
# refusal answers it in full rather than reporting a path that was not found.
- name: Assert both offline artefacts are present on the control node
  ansible.builtin.assert:
    that:
      - (role_path ~ '/files/invctl_' ~ invctl_version ~ '_linux_amd64') is file
      - (role_path ~ '/files/invctl_' ~ invctl_version ~ '_checksums.txt') is file
    fail_msg: >-
      invctl_source is 'files' and the release artefacts are not where the role
      reads them from. Put BOTH of these, with exactly these names, in
      {{ role_path }}/files/ :
      invctl_{{ invctl_version }}_linux_amd64 and
      invctl_{{ invctl_version }}_checksums.txt .
      Download them from
      https://github.com/madalinignisca/invctl/releases/download/v{{ invctl_version }}/
      on a machine that can reach it. The checksums file is not optional: the
      offline path verifies exactly as the online one does (spec D5).
  delegate_to: localhost
  become: false
  run_once: true
  when: invctl_source == 'files'

- name: Copy the offline binary to the target
  ansible.builtin.copy:
    src: "invctl_{{ invctl_version }}_linux_amd64"
    dest: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_linux_amd64"
    mode: "0644"
  when: invctl_source == 'files'

- name: Copy the offline checksums file to the target
  ansible.builtin.copy:
    src: "invctl_{{ invctl_version }}_checksums.txt"
    dest: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_checksums.txt"
    mode: "0644"
  when: invctl_source == 'files'

- name: Verify the binary against its recorded checksum
  ansible.builtin.include_tasks: verify_checksum.yml
```

- [ ] **Step 3: Write `verify_checksum.yml` — the one verification**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# THE ONLY CHECKSUM VERIFICATION IN THIS ROLE, and both acquisition paths end
# here. Sharing it is deliberate: two verification code paths means the
# rarely-exercised one eventually stops verifying, and nobody notices because
# the symptom is silence (spec D5).
#
# The checksums file is standard sha256sum output: `<hash>  <filename>`, two
# spaces, bare filename. Parsed rather than fed to `sha256sum -c` because the
# file lists a bare filename and would have to be run from the right directory,
# and because stat's checksum needs no shell.

- name: Read the checksums file
  ansible.builtin.slurp:
    src: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_checksums.txt"
  register: invctl_checksums_raw

- name: Find the checksums record for this binary
  ansible.builtin.set_fact:
    # THE ANCHORS AND THE COUNT ARE THE SECURITY PROPERTY, not tidiness.
    #
    # An unanchored filename match -- `select('search', 'invctl_1.2.0_linux_amd64$')`
    # -- also matches a line for `other_invctl_1.2.0_linux_amd64`, and would
    # then verify our binary against somebody else's hash. A checksums file is
    # attacker-influenced in exactly the scenario this check exists for, so the
    # record is matched whole: 64 lowercase hex, the two spaces sha256sum
    # writes, then the filename and nothing else.
    #
    # regex_escape because the version contains dots, and an unescaped dot
    # matches any character -- 1.2.0 would accept a record for 1x2y0.
    invctl_checksum_records: >-
      {{ (invctl_checksums_raw.content | b64decode).splitlines()
         | select('match', '^[0-9a-f]{64}  invctl_'
                  ~ (invctl_version | regex_escape) ~ '_linux_amd64$')
         | list }}

- name: Assert exactly one record names this binary
  ansible.builtin.assert:
    that:
      - invctl_checksum_records | length == 1
    fail_msg: >-
      invctl_{{ invctl_version }}_checksums.txt contains
      {{ invctl_checksum_records | length }} records for
      invctl_{{ invctl_version }}_linux_amd64, and exactly one is required.
      Zero means the file is for a different version, is truncated, or has
      Windows line endings. More than one means it has been edited or
      concatenated, and the role will not pick. Nothing has been installed.
      A verification that accepts "some line looked close enough" is not a
      verification (spec D5).

- name: Take the recorded hash
  ansible.builtin.set_fact:
    invctl_expected_sha256: "{{ (invctl_checksum_records | first).split() | first }}"

- name: Compute the SHA-256 of the staged binary
  ansible.builtin.stat:
    path: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_linux_amd64"
    get_checksum: true
    checksum_algorithm: sha256
  register: invctl_staged_binary

- name: Verify the staged binary
  ansible.builtin.assert:
    that:
      - invctl_staged_binary.stat.exists
      - invctl_staged_binary.stat.checksum == invctl_expected_sha256
    fail_msg: >-
      CHECKSUM MISMATCH for invctl_{{ invctl_version }}_linux_amd64 from
      '{{ invctl_source }}'.
      recorded: {{ invctl_expected_sha256 }}
      computed: {{ invctl_staged_binary.stat.checksum | default('(no file)') }}
      Nothing has been installed and nothing has been replaced. Do not work
      around this by re-running: a binary that does not match its checksum is
      either a truncated download or not the binary that was published.
    success_msg: >-
      invctl {{ invctl_version }} verified from '{{ invctl_source }}':
      {{ invctl_expected_sha256 }}
```

- [ ] **Step 4: Watch it fail, on a corrupted binary**

This needs the rest of the role to exist before it can run through the playbook,
so drive `acquire.yml` alone from a throwaway playbook written to `/tmp` —
**not** to the repository:

```bash
cd /home/gabriel/apps/infra-inventory/deploy/ansible
cat > /tmp/acquire-only.yml <<'EOF'
- hosts: invctl
  gather_facts: false
  vars:
    invctl_version: "1.2.0"
    invctl_source: files
  tasks:
    - ansible.builtin.include_role:
        name: invctl
        tasks_from: acquire.yml
EOF
./test/incus-harness.sh up
mkdir -p roles/invctl/files
curl -fsSL -o roles/invctl/files/invctl_1.2.0_linux_amd64 \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_linux_amd64
curl -fsSL -o roles/invctl/files/invctl_1.2.0_checksums.txt \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_checksums.txt
cat -A roles/invctl/files/invctl_1.2.0_checksums.txt | head -2
```
Expected: one line ending `$` (LF, not `^M$`), with **two spaces** between the
hash and the bare filename. `cat -A` rather than `cat` because a CR would make
the anchored pattern match nothing, and the refusal message names that cause.

```bash
cp roles/invctl/files/invctl_1.2.0_linux_amd64 /tmp/invctl-1.2.0.good
printf 'x' >> roles/invctl/files/invctl_1.2.0_linux_amd64
sudo -n ansible-playbook -i inventory/incus-test.ini /tmp/acquire-only.yml
cp /tmp/invctl-1.2.0.good roles/invctl/files/invctl_1.2.0_linux_amd64
sudo -n ansible-playbook -i inventory/incus-test.ini /tmp/acquire-only.yml
```
Expected: FAIL at "Verify the staged binary" printing both hashes, then PASS
with the `success_msg` naming the hash.

- [ ] **Step 5: Prove the record match cannot be fooled — the decoy**

This is the mutation test for the anchoring, and it is the one that would have
caught the previous draft.

```bash
cp roles/invctl/files/invctl_1.2.0_checksums.txt /tmp/checksums.good
# A record for a DIFFERENT file whose name ends the same way, carrying a hash
# that is not ours. An unanchored match would take this one.
printf '%s  other_invctl_1.2.0_linux_amd64\n' \
  "$(printf 'decoy' | sha256sum | cut -d' ' -f1)" \
  >> roles/invctl/files/invctl_1.2.0_checksums.txt
sudo -n ansible-playbook -i inventory/incus-test.ini /tmp/acquire-only.yml
```
Expected: **PASS**, still verifying against our own record — the decoy is not
counted at all, because the pattern is anchored at the front.

Now confirm the count assertion itself bites:

```bash
cp /tmp/checksums.good roles/invctl/files/invctl_1.2.0_checksums.txt
cat /tmp/checksums.good >> roles/invctl/files/invctl_1.2.0_checksums.txt   # duplicate the real record
sudo -n ansible-playbook -i inventory/incus-test.ini /tmp/acquire-only.yml
```
Expected: **FAIL** at "Assert exactly one record names this binary", reporting
2.

```bash
: > roles/invctl/files/invctl_1.2.0_checksums.txt
sudo -n ansible-playbook -i inventory/incus-test.ini /tmp/acquire-only.yml
cp /tmp/checksums.good roles/invctl/files/invctl_1.2.0_checksums.txt
```
Expected: **FAIL** reporting 0, then restore. **`cp`, never `git checkout --`.**

To see the fix actually matter, drop the `^` and the count assertion, re-run the
decoy case, and watch it verify against the decoy's hash and fail the *binary*
comparison for the wrong reason — then restore.

- [ ] **Step 6: Watch the missing-file refusal, and the online path**

```bash
mv roles/invctl/files/invctl_1.2.0_checksums.txt /tmp/
sudo -n ansible-playbook -i inventory/incus-test.ini /tmp/acquire-only.yml
mv /tmp/invctl_1.2.0_checksums.txt roles/invctl/files/
sudo -n ansible-playbook -i inventory/incus-test.ini /tmp/acquire-only.yml -e invctl_source=github
```
Expected: FAIL naming **both** filenames and the `files/` directory — not a
"path not found" from the copy module. Then PASS from GitHub, with the same
`success_msg` and the **same hash** as the `files` run. The same hash from both
paths is the evidence that they share one verification, which is the whole of
D5.

- [ ] **Step 7: Tear down and commit**

```bash
./test/incus-harness.sh down
cd /home/gabriel/apps/infra-inventory
git status --short deploy/ansible/roles/invctl/files
```
Expected: `.gitignore`, `.gitkeep` and `README.md` only. **The two downloaded
artefacts must not appear.**

```bash
git add deploy/ansible/roles/invctl/tasks deploy/ansible/roles/invctl/files
git commit -m "deploy: acquire the binary from GitHub or disk, and verify both against one whole record"
```

---

### Task 4: Fresh install, strict read-back, and configuration that converges (D7)

**Files:**
- Create: `deploy/ansible/roles/invctl/tasks/main.yml`
- Create: `deploy/ansible/roles/invctl/tasks/read_env.yml`
- Create: `deploy/ansible/roles/invctl/tasks/install.yml`
- Create: `deploy/ansible/roles/invctl/tasks/configure.yml`
- Create: `deploy/ansible/roles/invctl/tasks/verify.yml`
- Create: `deploy/ansible/roles/invctl/templates/invctl.service.j2`
- Create: `deploy/ansible/roles/invctl/templates/invctl.env.j2`
- Create: `deploy/ansible/roles/invctl/handlers/main.yml`
- Create: `deploy/ansible/playbooks/invctl.yml`

**Interfaces:**
- Consumes: Task 1's defaults, Task 3's `invctl_stage.path`.
- Produces: `invctl_installed_version` — `''` when nothing is installed,
  otherwise the version with its leading `v` stripped.
- Produces: `invctl_env` — a dict of the host's **current** `INV_*` settings,
  built by a parser that refuses rather than guesses. Task 6 reads
  `invctl_env['INV_DB_DRIVER']` and `invctl_env['INV_DB_DSN']` from it to learn
  which database the service was actually using, which is the fix for D7's sharp
  edge.
- Produces: the refusal that stops a run proceeding past an interrupted upgrade.
  Task 6 writes the marker; `main.yml` is where it is read.

**The structural point is D7: two axes, kept apart.** `install.yml` handles the
binary, only on a fresh install. `configure.yml` handles the env file and the
unit, **every** run. `verify.yml` starts and health-checks, every run.
`main.yml` is the only file that knows which binary state it is in.

**The read-back fails closed, and that is a correctness property rather than a
nicety.** The env file holds `INV_SESSION_KEY`. Treating an unreadable or
half-written file as empty mints a new key, which signs every user out — on a
run that should have been silent, with a symptom nobody would connect to
Ansible. So: a file that exists and cannot be read stops the play, and a file
whose lines are not exactly the form this role writes stops the play.

**`main.yml`'s mismatch branch fails rather than upgrading, and that is not a
placeholder.** Until Task 6 exists, a host with a different version installed
must be told the role will not touch its binary. Refusing is a correct,
shippable behaviour at this point in the sequence; quietly doing nothing is not.
Task 6 replaces the `fail` with the include.

- [ ] **Step 1: Write `templates/invctl.env.j2` (D6)**

```jinja
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

# MANAGED BY ANSIBLE, AND REWRITTEN ON EVERY RUN (spec D7). Edit a variable in
# your inventory, not this file: an edit here survives exactly until the next
# playbook run, which is the point -- configuration converges.
#
# 0600 root:root. docs/INSTALL.md warns that `Environment=` lines in a unit are
# world-readable through `systemctl show`, and a PostgreSQL DSN carries a
# password. systemd reads EnvironmentFile= as root, in the manager, BEFORE
# dropping to the service user -- so the invctl user never needs to read this
# file and the strictest mode that works is the right one (spec D6).
#
# EVERY LINE IS EXACTLY KEY='value' AND tasks/read_env.yml ACCEPTS NOTHING ELSE.
# The role reads this file back to recover the session key, so the writer and
# the reader have to agree on one form; anything else is a refusal rather than a
# guess. tasks/main.yml rejects a value containing a single quote, a carriage
# return or a newline, any of which would end the line early and let the rest of
# the value become a setting of its own.
INV_LISTEN='{{ invctl_listen }}'
INV_DB_DRIVER='{{ invctl_db_driver }}'
INV_DB_DSN='{{ invctl_db_dsn }}'
INV_SECURE_COOKIES='{{ invctl_secure_cookies | string | lower }}'

# GENERATED ONCE AND THEN READ BACK, never regenerated. This matters more under
# D7 than it did before: the template now runs on EVERY run, so a key minted
# here each time would sign everybody out every time the playbook runs (spec
# §5). tasks/configure.yml reads the existing value and mints one only when the
# file genuinely has none.
INV_SESSION_KEY='{{ invctl_session_key }}'

INV_ADMIN_USERNAME='{{ invctl_admin_username }}'
{% if invctl_admin_password_effective | length > 0 %}
# Seeds the FIRST administrator and is ignored once any account exists
# (ensureAdmin returns early). Required only for a fresh install; read back off
# this file on later runs so an unattended run needs no credential.
#
# OMITTED ENTIRELY WHEN THERE IS NO EFFECTIVE PASSWORD, rather than written as
# an empty value: an operator who deletes this line by hand once the account
# exists must find it still deleted after the next run. The role does not delete
# it for them -- that would be stateful, and a first install whose seeding
# half-failed could then never retry.
INV_ADMIN_PASSWORD='{{ invctl_admin_password_effective }}'
{% endif %}

# Break-glass. See docs/RECOVERY.md.
INV_ADMIN_USERS='{{ invctl_admin_users }}'
```

Ansible's `template` module runs Jinja with `trim_blocks` on, so the newline
after `{% if %}` and `{% endif %}` is consumed and the rendered file has no
stray blank line either way. Confirm that in Step 11 by diffing two renders
rather than assuming it.

- [ ] **Step 2: Write `templates/invctl.service.j2`**

`docs/INSTALL.md`'s unit, with `Environment=` replaced by `EnvironmentFile=`:

```jinja
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

# MANAGED BY ANSIBLE. This is docs/INSTALL.md's unit with one change:
# EnvironmentFile= instead of Environment= lines, because `systemctl show`
# discloses the latter to everybody and a DSN carries a password (spec D6).
[Unit]
Description=invctl infrastructure inventory
After=network-online.target
Wants=network-online.target

[Service]
# Type=simple, NOT Type=notify. invctl does not implement sd_notify; a notify
# unit waits for a readiness message that never arrives and systemd eventually
# fails the start.
Type=simple
User=invctl
Group=invctl
WorkingDirectory={{ invctl_data_dir }}
EnvironmentFile={{ invctl_config_dir }}/invctl.env
ExecStart={{ invctl_install_dir }}/invctl
Restart=on-failure
RestartSec=5s

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths={{ invctl_data_dir }}

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 3: Write `handlers/main.yml`**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# THE MECHANISM D7 RESTS ON. A template task reports `changed` only on a real
# diff, and a handler notified by it fires only then. That gives "do not restart
# when nothing changed" WITHOUT also giving "do not look at the configuration"
# -- which is exactly the pair the first draft of the spec conflated.
- name: reload systemd
  ansible.builtin.systemd_service:
    daemon_reload: true

- name: restart invctl
  ansible.builtin.systemd_service:
    name: invctl
    state: restarted
```

- [ ] **Step 4: Write `tasks/read_env.yml` — strict, and fail closed**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# Parse the configuration ALREADY ON THE HOST, once, for the two callers that
# need it: configure.yml recovers the session key and the seed password from it,
# and upgrade.yml learns which database the service was actually using.
#
# ONE PARSER, FOR THE SAME REASON THERE IS ONE CHECKSUM VERIFIER (spec D5): a
# strict parser written twice is a parser that is strict in one place.
#
# THIS FILE FAILS CLOSED, DELIBERATELY, AND AN EARLIER DRAFT DID NOT. It used
# `slurp` with `failed_when: false`, so an unreadable or half-written file read
# as empty -- which mints a NEW INV_SESSION_KEY and signs out every user, on a
# run that reported itself routine. Losing everybody's session is not an error
# anything raises; it is just Monday morning. So "cannot read" and "cannot
# parse" both stop the play.

- name: The one assignment form this role writes and accepts
  ansible.builtin.set_fact:
    # KEY='value': single-quoted, no single quote inside, nothing before or
    # after. Everything else is malformed.
    #
    # A truncated INV_SESSION_KEY='abc passes a naive "does the file contain
    # INV_SESSION_KEY='" test and then yields the malformed text as the value,
    # which is then written back to the host as the session key. Double-quoted,
    # unquoted, whitespace-padded and duplicated assignments are all likewise
    # something a human did that this role must not silently reinterpret.
    invctl_env_record_re: "^INV_[A-Z0-9_]+='[^']*'$"

- name: Look for the configuration already on the host
  ansible.builtin.stat:
    path: "{{ invctl_config_dir }}/invctl.env"
  register: invctl_env_stat

# NO failed_when HERE. A file that exists and cannot be read is a reason to
# stop. ONLY "the file is absent" means "this is a first install".
- name: Read it
  ansible.builtin.slurp:
    src: "{{ invctl_config_dir }}/invctl.env"
  register: invctl_env_slurp
  when: invctl_env_stat.stat.exists
  no_log: true

- name: Split it into lines
  ansible.builtin.set_fact:
    invctl_env_lines: >-
      {{ (invctl_env_slurp.content | b64decode).splitlines()
         if invctl_env_stat.stat.exists else [] }}
  no_log: true

- name: Separate the assignments from the comments and the blank lines
  ansible.builtin.set_fact:
    invctl_env_records: >-
      {{ invctl_env_lines | select('match', invctl_env_record_re) | list }}
    invctl_env_unparsed: >-
      {{ invctl_env_lines
         | reject('match', invctl_env_record_re)
         | reject('match', '^#')
         | reject('match', '^$')
         | list }}
  no_log: true

- name: Refuse a configuration file this role cannot have written
  ansible.builtin.assert:
    that:
      - invctl_env_unparsed | length == 0
      - (invctl_env_records
         | map('regex_replace', "^(INV_[A-Z0-9_]+)='.*$", '\1')
         | list | unique | length) == (invctl_env_records | length)
    fail_msg: >-
      {{ invctl_config_dir }}/invctl.env has
      {{ invctl_env_unparsed | length }} line(s) this role cannot parse, or
      assigns the same variable twice, and it will not guess at what was meant.
      Every line must be blank, a comment, or exactly KEY='value' with no single
      quote in the value.
      THE SESSION KEY LIVES IN THIS FILE: reading it wrongly would replace the
      key and sign out every user, so a file that is not exactly what this role
      writes stops the run instead.
      Fix the line, or move the file aside and re-run with
      invctl_admin_password set to seed it again -- ALL SESSIONS WILL END if you
      do that, because the key goes with it.
  when: invctl_env_stat.stat.exists
  no_log: true

- name: Build the configuration map
  ansible.builtin.set_fact:
    # zip rather than a separator character: a DSN or a password can contain
    # any byte except a single quote and a newline, so there is no separator
    # that is safe to split on.
    invctl_env: >-
      {{ dict(invctl_env_records
                | map('regex_replace', "^(INV_[A-Z0-9_]+)='.*$", '\1') | list
              | zip(invctl_env_records
                | map('regex_replace', "^INV_[A-Z0-9_]+='(.*)'$", '\1') | list)) }}
  no_log: true
```

- [ ] **Step 5: Write `tasks/main.yml`**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# TWO AXES, KEPT APART (spec D7).
#
#   the BINARY  -- a three-state machine: absent / same / different
#   the CONFIG  -- not a state machine at all; templated every run, and a
#                  restart only when a template really changed
#
# The first draft of the spec keyed everything on the binary and therefore
# ignored the configuration whenever the version matched. That is not
# idempotent, it is inert: the DSN on the host could drift from the DSN in the
# inventory with nothing to show for it.
#
# STATE IS READ FROM THE TARGET, NEVER ASSUMED (spec §4).

# --- before anything else -------------------------------------------------

- name: Look for an interrupted upgrade
  ansible.builtin.stat:
    path: "{{ invctl_config_dir }}/upgrade-in-progress"
  register: invctl_upgrade_marker

- name: Read the interrupted upgrade's note
  ansible.builtin.slurp:
    src: "{{ invctl_config_dir }}/upgrade-in-progress"
  register: invctl_upgrade_marker_body
  when: invctl_upgrade_marker.stat.exists

# THIS REFUSAL IS THE FIRST TASK IN THE ROLE FOR A REASON, AND THE REASON IS NOT
# OBVIOUS FROM THE DIFF.
#
# If `invctl -migrate` fails during an upgrade, the NEW binary is already on
# disk and the service is stopped. On the next run `-version` now MATCHES the
# pin, so the binary axis says NO-OP -- and D7's unconditional configure + verify
# would then START the service. invctl migrates automatically at startup, before
# binding (docs/UPGRADE.md), so that start is a second migration attempt made
# OUTSIDE the backup gate, on a database whose state nobody has looked at.
#
# The backup from the interrupted attempt does still exist, so this is not
# unrecoverable. It is the role walking silently past a half-finished upgrade,
# which is the one behaviour D1 says it must never have.
- name: Refuse to proceed past an interrupted upgrade
  ansible.builtin.fail:
    msg: >-
      An upgrade on this host did not finish. {{ invctl_config_dir }}/upgrade-in-progress
      records it:

      {{ invctl_upgrade_marker_body.content | b64decode }}

      The role will do NOTHING -- not even converge configuration -- until a
      person has decided what happened, because starting invctl would itself
      apply migrations, outside the backup gate, to a database nobody has
      looked at.
      Read docs/UPGRADE.md, "When a migration fails". The backup named above
      exists and is the rollback. When you have decided and acted, clear the
      marker by hand:
        rm {{ invctl_config_dir }}/upgrade-in-progress
      There is deliberately no variable that does this for you.
  when: invctl_upgrade_marker.stat.exists

# --- what is actually on this host ----------------------------------------

- name: Look for an installed binary
  ansible.builtin.stat:
    path: "{{ invctl_install_dir }}/invctl"
  register: invctl_binary

# `invctl -version` is printed BEFORE config.Load (cmd/invctl/main.go:99-105),
# so this works on a host whose configuration is missing or wrong -- which is
# exactly the host somebody is standing at when they need to know what is
# deployed.
- name: Ask the installed binary what it is
  ansible.builtin.command:
    argv: ["{{ invctl_install_dir }}/invctl", -version]
  register: invctl_version_probe
  changed_when: false
  when: invctl_binary.stat.exists

# `invctl v1.2.0 (commit ..., built ..., linux/amd64, go1.26.6)` -- the second
# whitespace-separated field, with the leading v stripped.
- name: Record the installed version
  ansible.builtin.set_fact:
    invctl_installed_version: >-
      {{ (invctl_version_probe.stdout.split()[1] | regex_replace('^v', ''))
         if invctl_binary.stat.exists else '' }}

- name: Read the configuration already on the host
  ansible.builtin.include_tasks: read_env.yml

# --- preflight ------------------------------------------------------------

# ONLY ON A FRESH INSTALL (spec §2). ensureAdmin returns early once any account
# exists, so requiring this on every run would keep a credential in the
# inventory to satisfy an assertion and nothing else -- and would stop the
# playbook running unattended, which D7 otherwise makes possible.
- name: Refuse a fresh install without an administrator password
  ansible.builtin.assert:
    that:
      - invctl_admin_password is defined
      - (invctl_admin_password | default('')) | length > 0
    fail_msg: >-
      invctl_admin_password is required for a FIRST install of this host and has
      no default, ever. A default admin password in a deployment role is a
      default admin password in production. Pass it from a vault, or with
      -e invctl_admin_password=... for a throwaway. Later runs against this host
      will not need it.
  no_log: true
  when: invctl_installed_version == ''

- name: Refuse PostgreSQL without a DSN
  ansible.builtin.assert:
    that:
      - invctl_db_dsn is defined
      - invctl_db_dsn is search('^postgres')
    fail_msg: >-
      invctl_db_driver is 'postgres' and invctl_db_dsn is not a PostgreSQL DSN.
      This role never installs a database server (spec D2): point it at one
      somebody else manages.
  no_log: true
  when: invctl_db_driver == 'postgres'

- name: Collect the values that will be written to the environment file
  ansible.builtin.set_fact:
    invctl_env_candidate_values:
      - "{{ invctl_listen }}"
      - "{{ invctl_db_dsn }}"
      - "{{ invctl_admin_username }}"
      - "{{ invctl_admin_password | default('') }}"
      - "{{ invctl_admin_users }}"
  no_log: true

# A SINGLE QUOTE ENDS THE VALUE EARLY. A NEWLINE ENDS THE LINE EARLY, AND
# WHATEVER FOLLOWS BECOMES A SETTING OF ITS OWN -- so a password containing a
# line break could set INV_SECURE_COOKIES=false, and nothing would look wrong.
# Both are refused rather than escaped: this role reads the file back, and one
# unambiguous form is worth more than the ability to carry an exotic value.
- name: Refuse a configuration value containing a quote or a line break
  ansible.builtin.assert:
    that:
      - (invctl_env_candidate_values | select('search', "'") | list | length) == 0
      - (invctl_env_candidate_values | select('search', '[\r\n]') | list | length) == 0
    fail_msg: >-
      A configuration value contains a single quote, a carriage return or a
      newline. The environment file writes every value as KEY='value' on one
      line, and reads it back in exactly that form; a quote would truncate the
      value and a line break would turn the remainder into a separate setting.
      Change the value -- most often this is a password out of a vault that
      picked up a trailing newline.
  no_log: true

- name: Require psql for the PostgreSQL reachability check
  ansible.builtin.command:
    argv: [sh, -c, 'command -v psql']
  register: invctl_psql
  changed_when: false
  failed_when: false
  when: invctl_db_driver == 'postgres'

# community.postgresql is deliberately NOT used: it is not installed here and
# would be a new dependency for one query (spec D2). psql comes from
# postgresql-client, the same package §8 already needs for pg_dump.
- name: Fail early when psql is missing
  ansible.builtin.assert:
    that:
      - invctl_psql.rc == 0
    fail_msg: >-
      psql is not on PATH on this host. Install postgresql-client: it provides
      psql (this check), pg_dump (the upgrade backup) and pg_restore (the check
      that the backup is readable). The database SERVER stays wherever it is;
      only the client is needed here.
  when: invctl_db_driver == 'postgres'

# BEFORE ANY CONFIGURATION IS WRITTEN (spec D2), so a typo fails at the start
# rather than as a crash-looping service somebody has to read a journal for.
- name: Verify the PostgreSQL database is reachable
  ansible.builtin.command:
    argv: [psql, "{{ invctl_db_dsn }}", -tAc, 'SELECT 1']
  register: invctl_pg_reachable
  changed_when: false
  failed_when: false
  no_log: true
  when: invctl_db_driver == 'postgres'

- name: Fail when the database cannot be reached
  ansible.builtin.assert:
    that:
      - invctl_pg_reachable.rc == 0
    fail_msg: >-
      The PostgreSQL database named by invctl_db_dsn could not be reached, and
      nothing has been written. The role does not install a database server
      (spec D2); create the database first, per the SQL in docs/INSTALL.md.
  when: invctl_db_driver == 'postgres'

- name: Report the state this run is acting on
  ansible.builtin.debug:
    msg: >-
      binary: installed={{ invctl_installed_version | default('(none)', true) }}
      pinned={{ invctl_version }}
      action={{ 'INSTALL' if invctl_installed_version == ''
                else ('NO-OP' if invctl_installed_version == invctl_version
                      else 'UPGRADE') }};
      configuration is converged on every run regardless (spec D7).

# --- the binary axis ------------------------------------------------------

- name: Install
  ansible.builtin.include_tasks: install.yml
  when: invctl_installed_version == ''

# REPLACED BY tasks/upgrade.yml IN TASK 6. Until then the role refuses rather
# than doing nothing: a host pinned to a version it is not running is a fact an
# operator has to be told, and a silent no-op tells them nothing.
- name: Refuse an upgrade this role cannot yet perform
  ansible.builtin.fail:
    msg: >-
      invctl {{ invctl_installed_version }} is installed and {{ invctl_version }}
      is pinned. This role does not perform upgrades yet. Nothing has been
      changed.
  when:
    - invctl_installed_version != ''
    - invctl_installed_version != invctl_version

# --- the configuration axis, always ---------------------------------------

- name: Converge the configuration and the unit
  ansible.builtin.include_tasks: configure.yml

- name: Start it and prove it is serving
  ansible.builtin.include_tasks: verify.yml
```

- [ ] **Step 6: Write `tasks/install.yml`**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# A fresh install: the user, the directories and the binary. The config file and
# the unit are NOT here -- they belong to configure.yml, which main.yml runs on
# every run (spec D7). Templating them here would mean they were only ever
# written on the first run, which is the inert behaviour D7 corrects.
#
# NO EXPLICIT MIGRATION STEP IS NEEDED: invctl runs migrations automatically at
# startup, before binding, and exits on failure rather than serving a
# half-migrated schema (docs/UPGRADE.md). On a fresh install there is nothing to
# lose, so the health check in verify.yml is a sufficient gate (spec §4).

- name: Create the invctl system group
  ansible.builtin.group:
    name: invctl
    system: true

- name: Create the invctl system user
  ansible.builtin.user:
    name: invctl
    group: invctl
    system: true
    create_home: false
    shell: /usr/sbin/nologin

- name: Create the directories
  ansible.builtin.file:
    path: "{{ item.path }}"
    state: directory
    owner: "{{ item.owner }}"
    group: "{{ item.group }}"
    mode: "{{ item.mode }}"
  loop:
    # The binary is root-owned and the service user only executes it: a service
    # that can rewrite its own binary is a privilege escalation waiting for a
    # file-permission mistake.
    - { path: "{{ invctl_install_dir }}", owner: root, group: root, mode: "0755" }
    - { path: "{{ invctl_data_dir }}", owner: invctl, group: invctl, mode: "0750" }
    - { path: "{{ invctl_config_dir }}", owner: root, group: root, mode: "0750" }
    # Backups are database dumps: everything in the inventory, in one file.
    - { path: "{{ invctl_backup_dir }}", owner: root, group: root, mode: "0700" }
  loop_control:
    label: "{{ item.path }}"

- name: Acquire and verify the binary
  ansible.builtin.include_tasks: acquire.yml

- name: Install the verified binary
  ansible.builtin.copy:
    src: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_linux_amd64"
    dest: "{{ invctl_install_dir }}/invctl"
    remote_src: true
    owner: root
    group: root
    mode: "0755"
```

- [ ] **Step 7: Write `tasks/configure.yml` — the D7 deliverable**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# RUNS ON EVERY RUN, whatever the binary axis decided (spec D7). Change
# invctl_listen or the DSN in your inventory, re-run without touching
# invctl_version, and the host converges and restarts. Change nothing and the
# templates report no diff, no handler fires, and the run costs a probe.
#
# Two values come from the host rather than from the inventory, because
# re-deriving them would produce a spurious diff on every run -- and under D7
# that means a RESTART on every run, which is precisely what makes a scheduled
# playbook unusable. read_env.yml has already parsed them, strictly.

# A NEW KEY ON EVERY RUN SIGNS EVERYBODY OUT ON EVERY RUN, and nobody would
# connect that symptom to Ansible (spec §5). Minted once, then read back for
# ever. read_env.yml has already refused a file it could not parse, so a missing
# key here means genuinely missing, never "unreadable".
- name: Keep the existing session key, or mint one
  ansible.builtin.set_fact:
    invctl_session_key: >-
      {{ invctl_env['INV_SESSION_KEY']
         if 'INV_SESSION_KEY' in invctl_env
         else lookup('community.general.random_string', length=32, base64=true) }}
  no_log: true

# Prefer a supplied password so an operator who passes it from a vault on every
# run stays diff-free; fall back to the host's so an unattended run does too.
# Empty means the line is omitted from the rendered file entirely -- see the
# template. An operator who deleted it meant to delete it.
- name: Decide the administrator password to render, if there is one
  ansible.builtin.set_fact:
    invctl_admin_password_effective: >-
      {{ invctl_admin_password
         if (invctl_admin_password | default('')) | length > 0
         else (invctl_env['INV_ADMIN_PASSWORD'] | default('')) }}
  no_log: true

- name: Write the configuration
  ansible.builtin.template:
    src: invctl.env.j2
    dest: "{{ invctl_config_dir }}/invctl.env"
    owner: root
    group: root
    mode: "0600"
  no_log: true
  notify: restart invctl

- name: Write the systemd unit
  ansible.builtin.template:
    src: invctl.service.j2
    dest: /etc/systemd/system/invctl.service
    owner: root
    group: root
    mode: "0644"
  notify:
    - reload systemd
    - restart invctl
```

- [ ] **Step 8: Write `tasks/verify.yml`**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# A UNIT THAT STARTED IS NOT A SERVICE THAT WORKS. invctl exits on a failed
# migration, and `systemctl start` returning success only means the fork
# happened. This is the difference between "systemd is content" and "the
# application is serving" (spec §6).

- name: Apply any pending restart before checking health
  # Flushed HERE rather than at the end of the play, so the health check below
  # tests the configuration this run wrote and not the one it replaced.
  ansible.builtin.meta: flush_handlers

- name: Enable and start invctl
  # NO daemon_reload HERE, deliberately: the module reports `changed` whenever
  # it performs one, which would make every run a changed run and destroy the
  # property Task 5 asserts. The `reload systemd` handler covers it, and fires
  # only when the unit file really changed.
  ansible.builtin.systemd_service:
    name: invctl
    enabled: true
    state: started

# /healthz requires no authentication (internal/web/routes.go:136), which is
# what makes it usable here -- there is no session to obtain first.
- name: Wait for /healthz to answer 200
  ansible.builtin.uri:
    url: "http://{{ invctl_listen }}/healthz"
    status_code: 200
  register: invctl_health
  retries: 30
  delay: 2
  until: invctl_health.status is defined and invctl_health.status == 200
  changed_when: false

- name: Report what is serving
  ansible.builtin.debug:
    msg: >-
      invctl {{ invctl_version }} is serving on {{ invctl_listen }};
      /healthz reports {{ invctl_health.json | default({}) }}.
      NOTHING ANSWERS FROM OUTSIDE THIS HOST -- this role installs no reverse
      proxy, no TLS certificate and no DNS (spec D3). See deploy/ansible/README.md.
```

- [ ] **Step 9: Write `playbooks/invctl.yml`**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# Hosts and the role, and nothing else (spec §1). Logic that creeps into a
# playbook is logic the role cannot be tested without.
- name: invctl
  hosts: invctl
  become: true
  roles:
    - role: invctl
```

Ansible resolves `role: invctl` from the playbook directory's sibling `roles/`
via the default `roles_path` search. Confirm with `--list-tasks` in the next
step rather than assuming it.

- [ ] **Step 10: Syntax check, then scenario 7.1 — fresh install, SQLite, from `files`**

```bash
cd /home/gabriel/apps/infra-inventory/deploy/ansible
sudo -n ansible-playbook -i inventory/incus-test.ini playbooks/invctl.yml --syntax-check
sudo -n ansible-playbook -i inventory/incus-test.ini playbooks/invctl.yml --list-tasks
```
Expected: no syntax error; `Look for an interrupted upgrade` is the **first**
task, and `configure.yml` and `verify.yml` are included *after* the binary
dispatch. If role resolution fails, add `deploy/ansible/ansible.cfg` carrying
the licence header and a `roles_path`, and say so in the commit.

```bash
./test/incus-harness.sh up
curl -fsSL -o roles/invctl/files/invctl_1.2.0_linux_amd64 \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_linux_amd64
curl -fsSL -o roles/invctl/files/invctl_1.2.0_checksums.txt \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_checksums.txt

./test/incus-harness.sh run \
  -e invctl_source=files \
  -e invctl_secure_cookies=false \
  -e invctl_admin_password=harness-only-not-a-real-password
```
Expected: `failed=0` and the final debug line reporting `/healthz`.
`invctl_secure_cookies=false` because the container is plain HTTP with no proxy.

- [ ] **Step 11: Check the things a green play does not prove**

```bash
sudo incus exec invctl-ansible-test -- curl -fsS http://127.0.0.1:8080/healthz; echo
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
sudo incus exec invctl-ansible-test -- stat -c '%U %G %a %n' /etc/invctl/invctl.env
sudo incus exec invctl-ansible-test -- systemctl show invctl -p User -p Type
sudo incus exec invctl-ansible-test -- sh -c 'systemctl show invctl | grep -c INV_ADMIN_PASSWORD'
sudo incus exec invctl-ansible-test -- ls -l /var/lib/invctl
sudo incus exec invctl-ansible-test -- sh -c \
  "grep -cvE \"^(#.*|[[:space:]]*|INV_[A-Z0-9_]+='[^']*')\$\" /etc/invctl/invctl.env"
```
Expected, in order: `{"database":"sqlite","status":"ok"}` or equivalent;
`invctl v1.2.0 (...)`; `root root 600 /etc/invctl/invctl.env`; `User=invctl` and
`Type=simple`; **`0`** — D6's whole reason: the password is not in
`systemctl show`; `invctl.db` owned by `invctl`; and **`0`** again — every line
the role wrote is in the one form `read_env.yml` accepts, including around the
`{% if %}`, so the writer and the reader genuinely agree.

- [ ] **Step 12: Tear down and commit**

```bash
./test/incus-harness.sh down
cd /home/gabriel/apps/infra-inventory
git add deploy/ansible
git commit -m "deploy: install invctl, read its configuration back strictly, converge it every run"
```

---

### Task 5: Identical inputs change nothing; a changed setting converges; a broken file stops the run (D7)

**Files:**
- Modify: `deploy/ansible/roles/invctl/tasks/*.yml` — only where the evidence
  below shows a task reporting `changed` when nothing did
- Create: `deploy/ansible/test/README.md`

**Interfaces:**
- Consumes: Task 4, entire.
- Produces: the properties Task 6 depends on. If a run with identical inputs is
  not visibly quiet, a run that *does* something is not visibly loud, and the
  upgrade's evidence becomes unreadable.

**Three things get proven here, and the last two are the ones that were missing
from the previous draft:**

1. *A second run with identical inputs changes nothing.* `changed=0`, no
   restart, session key unchanged.
2. *A second run with a changed setting converges it, without a version bump.*
   The half D7 exists for.
3. *A configuration file the role cannot parse stops the run* rather than being
   read as empty — because being read as empty is how everybody gets signed out.

- [ ] **Step 1: Install, then capture the before state**

```bash
cd /home/gabriel/apps/infra-inventory/deploy/ansible
./test/incus-harness.sh up
curl -fsSL -o roles/invctl/files/invctl_1.2.0_linux_amd64 \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_linux_amd64
curl -fsSL -o roles/invctl/files/invctl_1.2.0_checksums.txt \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_checksums.txt

BASE="-e invctl_source=files -e invctl_secure_cookies=false"
./test/incus-harness.sh run $BASE -e invctl_admin_password=harness-only-not-a-real-password

sudo incus exec invctl-ansible-test -- systemctl show invctl -p ActiveEnterTimestamp > /tmp/invctl-before.txt
sudo incus exec invctl-ansible-test -- sha256sum /etc/invctl/invctl.env > /tmp/invctl-env-before.txt
```

- [ ] **Step 2: Half 1 — identical inputs, and deliberately no password**

The second run omits `invctl_admin_password` entirely. That is not laziness: it
is the assertion that spec §2's "required only on a fresh install" actually
holds, and that `read_env.yml` plus `configure.yml` recover the value rather
than rendering an empty one.

```bash
./test/incus-harness.sh run $BASE | tee /tmp/invctl-second-run.txt
grep -E 'changed=0 .*failed=0' /tmp/invctl-second-run.txt

sudo incus exec invctl-ansible-test -- systemctl show invctl -p ActiveEnterTimestamp > /tmp/invctl-after.txt
diff /tmp/invctl-before.txt /tmp/invctl-after.txt
sudo incus exec invctl-ansible-test -- sha256sum /etc/invctl/invctl.env > /tmp/invctl-env-after.txt
diff /tmp/invctl-env-before.txt /tmp/invctl-env-after.txt
```
Expected: **`changed=0`** and both `diff`s empty — the service was not
restarted and the env file is byte-identical, including its session key and its
administrator password.

If `changed=0` does not hold, the recap's `changed` list names the task. Likely
culprits in order: a `daemon_reload` left on the `systemd_service` task in
`verify.yml`; a session key being re-minted; the admin password rendering empty;
or a stray blank line from the `{% if %}` in the env template. Fix the cause and
re-run from Step 1. **Do not "fix" it by skipping the task.**

- [ ] **Step 3: Half 2 — change one setting, no version bump, watch it converge**

```bash
./test/incus-harness.sh run $BASE -e invctl_listen=127.0.0.1:8099 | tee /tmp/invctl-converge.txt
grep -E 'changed=[1-9][0-9]* .*failed=0' /tmp/invctl-converge.txt
sudo incus exec invctl-ansible-test -- grep INV_LISTEN /etc/invctl/invctl.env
sudo incus exec invctl-ansible-test -- curl -fsS http://127.0.0.1:8099/healthz; echo
sudo incus exec invctl-ansible-test -- systemctl show invctl -p ActiveEnterTimestamp
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
```
Expected, all five: a non-zero `changed` count with `failed=0`;
`INV_LISTEN='127.0.0.1:8099'`; a 200 from the **new** port; an
`ActiveEnterTimestamp` later than `/tmp/invctl-after.txt`; and
**`invctl v1.2.0`** — the binary was not touched, because the two axes are
independent.

**This is the assertion the pre-D7 plan got backwards.** Under the first draft
this run would have printed `changed=0` and left the host on port 8080.

```bash
./test/incus-harness.sh run $BASE | grep -E 'changed=[1-9][0-9]* .*failed=0'
sudo incus exec invctl-ansible-test -- curl -fsS http://127.0.0.1:8080/healthz; echo
```
Expected: changed again, and 200 on 8080.

- [ ] **Step 4: Half 2, the harder version — an out-of-band edit is corrected**

```bash
sudo incus exec invctl-ansible-test -- sh -c \
  "sed -i \"s|^INV_LISTEN=.*|INV_LISTEN='127.0.0.1:9999'|\" /etc/invctl/invctl.env"
./test/incus-harness.sh run $BASE | grep -E 'changed=[1-9][0-9]* .*failed=0'
sudo incus exec invctl-ansible-test -- grep INV_LISTEN /etc/invctl/invctl.env
sudo incus exec invctl-ansible-test -- curl -fsS http://127.0.0.1:8080/healthz; echo
```
Expected: the run reports `changed`, the file reads
`INV_LISTEN='127.0.0.1:8080'` again, and the service answers there. **The
tampered value is corrected, not preserved** — that is convergence, and it is
the whole of D7.

- [ ] **Step 5: Half 3 — a malformed configuration file stops the run**

Three shapes, each of which the previous draft would have read as "no session
key present" and silently replaced.

```bash
sudo incus exec invctl-ansible-test -- cp /etc/invctl/invctl.env /root/invctl.env.good
sudo incus exec invctl-ansible-test -- sha256sum /etc/invctl/invctl.env

# (a) truncated value -- passes "the file mentions INV_SESSION_KEY" and nothing else
sudo incus exec invctl-ansible-test -- sh -c \
  "sed -i \"s|^INV_SESSION_KEY='\\(.\\{6\\}\\).*|INV_SESSION_KEY='\\1|\" /etc/invctl/invctl.env"
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- sha256sum /etc/invctl/invctl.env
```
Expected: the play **fails** at `Refuse a configuration file this role cannot
have written`, with a non-zero exit, **and the file is unchanged** — the refusal
happens before anything is written, so a broken file is not made worse.

```bash
# (b) double-quoted assignment
sudo incus exec invctl-ansible-test -- cp /root/invctl.env.good /etc/invctl/invctl.env
sudo incus exec invctl-ansible-test -- sh -c \
  'printf "INV_LOG_LEVEL=\"debug\"\n" >> /etc/invctl/invctl.env'
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"

# (c) the same variable twice
sudo incus exec invctl-ansible-test -- cp /root/invctl.env.good /etc/invctl/invctl.env
sudo incus exec invctl-ansible-test -- sh -c \
  "printf \"INV_LISTEN='127.0.0.1:7777'\n\" >> /etc/invctl/invctl.env"
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"

# restore and confirm the host is healthy and quiet again
sudo incus exec invctl-ansible-test -- cp /root/invctl.env.good /etc/invctl/invctl.env
./test/incus-harness.sh run $BASE | grep -E 'changed=0 .*failed=0'
```
Expected: (b) and (c) both fail with the same named assertion, and the restored
run is `changed=0`.

Now the unreadable case, which is the one `failed_when: false` used to swallow:

```bash
sudo incus exec invctl-ansible-test -- chmod 000 /etc/invctl/invctl.env
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- chmod 600 /etc/invctl/invctl.env
```
Expected: the play fails at the `slurp`. **Ansible connects as root here, so
mode 000 may still be readable** — if the run succeeds, replace this with a
case Ansible genuinely cannot read (for example make the path a directory:
`mv /etc/invctl/invctl.env /tmp/e && mkdir /etc/invctl/invctl.env`) and restore
afterwards. Record in `test/README.md` which variant was used and why; the
property under test is "a file that exists and cannot be read stops the play",
not any particular way of making it unreadable.

- [ ] **Step 6: Prove the fail-closed behaviour is the fix, not an accident**

```bash
cp roles/invctl/tasks/read_env.yml /tmp/read_env.yml.bak
```

Put the old, fail-open shape back: add `failed_when: false` to the `Read it`
task and delete the `Refuse a configuration file this role cannot have written`
assertion. Re-run case (a):

```bash
sudo incus exec invctl-ansible-test -- sh -c \
  "sed -i \"s|^INV_SESSION_KEY='\\(.\\{6\\}\\).*|INV_SESSION_KEY='\\1|\" /etc/invctl/invctl.env"
./test/incus-harness.sh run $BASE | grep -E 'changed=[0-9]+ .*failed=0'
sudo incus exec invctl-ansible-test -- grep INV_SESSION_KEY /etc/invctl/invctl.env
```
Expected: the run **succeeds**, reports `changed`, and the session key is now
either a brand-new value or the malformed fragment written back as if it were
one. **Every user has just been signed out by a run that reported itself
routine.** That is the defect, observed.

```bash
cp /tmp/read_env.yml.bak roles/invctl/tasks/read_env.yml
sudo incus exec invctl-ansible-test -- cp /root/invctl.env.good /etc/invctl/invctl.env
./test/incus-harness.sh run $BASE | grep -E 'changed=0 .*failed=0'
```
Expected: restored, and quiet again. **`cp`, never `git checkout --`.**

- [ ] **Step 7: A value with a newline cannot inject a setting**

```bash
./test/incus-harness.sh run $BASE \
  -e '{"invctl_admin_users":"admin\nINV_SECURE_COOKIES=true"}' ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- grep INV_SECURE_COOKIES /etc/invctl/invctl.env
```
Expected: the play **fails** at `Refuse a configuration value containing a quote
or a line break`, and `INV_SECURE_COOKIES` on the host is still `false`. JSON
`\n` becomes a real newline, so without the check that value would have written
a second line and flipped a security setting with nothing in the output looking
wrong.

Prove the check is what stopped it: delete the `[\r\n]` condition, re-run, and
observe `INV_SECURE_COOKIES='true'` appearing on the host from a variable that
is nominally a username list. Restore with `cp`, then re-run plain and confirm
`changed=0` once the injected line has been converged away.

- [ ] **Step 8: A deleted administrator password line stays deleted**

```bash
sudo incus exec invctl-ansible-test -- sed -i '/^INV_ADMIN_PASSWORD=/d' /etc/invctl/invctl.env
./test/incus-harness.sh run $BASE | grep -E 'changed=[0-9]+ .*failed=0'
sudo incus exec invctl-ansible-test -- sh -c 'grep -c INV_ADMIN_PASSWORD /etc/invctl/invctl.env'
./test/incus-harness.sh run $BASE | grep -E 'changed=0 .*failed=0'
```
Expected: **`0`** — the line is not restored, and the run after that is quiet.
The seed password is inert once an account exists, and an operator who removed
it meant to. The role does not put it back and does not remove it for them.

- [ ] **Step 9: Prove `changed=0` is a decision and not inertness**

```bash
sudo incus exec invctl-ansible-test -- rm -f /opt/invctl/invctl
./test/incus-harness.sh run $BASE -e invctl_admin_password=harness-only-not-a-real-password \
  | grep -E 'changed=[1-9][0-9]* .*failed=0'
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
```
Expected: a non-zero `changed` count and a healthy v1.2.0 again. The password is
supplied because this *is* a fresh install by the role's own definition — which
is itself the assertion that the install-only requirement keys off the probe and
not off a guess.

- [ ] **Step 10: `--check` on a converged host**

```bash
./test/incus-harness.sh run $BASE --check
```
Expected: `failed=0`. `--check` is what shows an operator what a version bump
would do *before* it happens, which is the mitigation named in spec §8 for the
"upgrade nobody intended" risk. Note in `deploy/ansible/test/README.md` if any
task cannot run in check mode and why.

- [ ] **Step 11: Write `deploy/ansible/test/README.md`**

Markdown with the HTML-comment licence notice, covering: the container is
`invctl-ansible-test` and nothing else; `up`, `run`, `down`; **always run
`down`, including after a failure**; that several Task 6 scenarios recreate the
container rather than reusing it, and why (reusing an already-migrated host
would mean testing an unsupported downgrade); the artefacts in
`roles/invctl/files/` are gitignored and must be re-downloaded;
`-e invctl_secure_cookies=false` is required because the container is plain
HTTP; `-e invctl_admin_password=...` is needed only for the first run against a
fresh container; and every scenario with its exact commands and what it proves.
State that **scenario 2 has three halves now** and that the convergence and
fail-closed halves are the ones that would have passed vacuously before.

- [ ] **Step 12: Tear down and commit**

```bash
./test/incus-harness.sh down
sudo incus list --format csv -c n    # mailadmin-test still present, ours gone
cd /home/gabriel/apps/infra-inventory
git add deploy/ansible
git commit -m "deploy: prove convergence, and that an unparsable config stops the run"
```

---

### Task 6: Upgrade — prove the stop, back up, **gate**, mark, replace, migrate

**Files:**
- Create: `deploy/ansible/roles/invctl/tasks/upgrade.yml`
- Modify: `deploy/ansible/roles/invctl/tasks/main.yml` (replace the `fail` with
  the include; clear the marker after the health check)

**Interfaces:**
- Consumes: `invctl_installed_version` and `invctl_env` from Task 4,
  `invctl_stage.path` from Task 3.
- Produces: `invctl_upgrade_started` — `main.yml` clears the marker only when
  this run set it, and only after `verify.yml` has passed.
- `upgrade.yml` deliberately does **not** start the service. `main.yml` runs
  `configure.yml` and then `verify.yml` afterwards, which is what satisfies spec
  §4 step 7 — starting it here would start it against the configuration from
  before this run (D7).

**THE STEP ORDERING IS THE DELIVERABLE, BUT IT IS NOT SUFFICIENT ON ITS OWN.**
Back up, prove the backup, and only then touch the binary: that is D1, and any
other order can leave a host with a new binary and no way back. Four things sit
around that ordering because the ordering alone does not give them, and each was
found by somebody attacking the plan rather than reading it:

1. **The stop is proven, not assumed.** `systemd_service: state=stopped` can
   report success while the process is still running — systemd having lost the
   main PID, or a stop that hit `TimeoutStopSec`. Copying `invctl.db` and its
   `-wal` under a live writer yields a torn backup that is perfectly non-empty,
   so the size gate passes and the binary is replaced. The gate watches the
   backup's size; nothing watched the thing that could corrupt it.
2. **The backup and the migration use the database the *host* was running**,
   read out of its own env file — not the inventory's. Under D7 configuration
   converges *after* the binary work, so during an upgrade the inventory DSN is
   not yet the running one. Changing `invctl_db_dsn` and `invctl_version`
   together would otherwise back up and migrate the **new** database and then
   cut over to it, silently, on PostgreSQL.
3. **The gate proves the backup is usable, not merely non-empty.** A truncated
   or corrupt file has a non-zero size.
4. **A marker makes an interrupted upgrade visible to the next run**, which
   `main.yml` refuses on. Without it a failed `-migrate` leaves a matching
   version on disk, the next run takes the NO-OP path, and D7's unconditional
   verify **starts** the service — a second migration attempt outside the gate.

**And this deliberately diverges from one suggestion in `docs/UPGRADE.md`**,
which offers running `invctl -migrate` *before* swapping the binary so a slow
migration is found while the old binary still serves. The role does not: the
outgoing binary would then serve against a schema it was not built for, and
migrations are forward-only with no compatibility guarantee in that direction.
The role accepts a longer window of downtime in exchange. An operator who wants
the other trade on a large estate runs `-migrate` by hand first.

- [ ] **Step 1: Write `tasks/upgrade.yml` — preflight**

```yaml
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

---
# STEP 3 BEFORE STEP 4 IS THE WHOLE DESIGN (spec §4). Everything inside the
# block below is undoable; everything after it is not. The block/rescue is how
# that boundary is enforced rather than merely intended.
#
# Every refusal in this preflight happens BEFORE the service is stopped, so a
# refusal costs no downtime at all.

- name: Acquire and verify the new binary before anything is stopped
  ansible.builtin.include_tasks: acquire.yml

# THE DATABASE THIS UPGRADE ACTS ON IS THE ONE THE HOST WAS RUNNING, NOT THE ONE
# IN THE INVENTORY, AND THAT DISTINCTION IS NOT COSMETIC.
#
# D7 converges configuration AFTER the binary work, so at this point the host
# still holds the previous settings. Bump invctl_version and change
# invctl_db_dsn in the same run and the inventory's DSN names a database this
# service has never used: the role would dump THAT one, call it the rollback,
# migrate it, and then cut over -- with the real data untouched, unbacked-up and
# abandoned. On PostgreSQL nothing about that looks wrong from the outside.
#
# So: refuse the combined operation, and use the host's own values even though
# the assertion above has just proved them equal. If the assertion is ever
# loosened, the backup still covers the right database.
- name: Refuse to change the database and the version in one run
  ansible.builtin.assert:
    that:
      - "'INV_DB_DRIVER' in invctl_env"
      - "'INV_DB_DSN' in invctl_env"
      - invctl_env['INV_DB_DRIVER'] == invctl_db_driver
      - invctl_env['INV_DB_DSN'] == invctl_db_dsn
    fail_msg: >-
      This run would upgrade invctl from {{ invctl_installed_version }} to
      {{ invctl_version }} AND point it at a different database, and the role
      will not do both at once. The backup it would take, and the migrations it
      would run, would apply to the NEW database while the data you care about
      sits in the old one.
      Do it in two runs: first move the database with the version unchanged and
      confirm the service is healthy, then bump the version.
      (If INV_DB_DRIVER or INV_DB_DSN is missing from
      {{ invctl_config_dir }}/invctl.env altogether, this host was not set up by
      this role; fix that before upgrading.)
  no_log: true

- name: Act on the database the service was actually using
  ansible.builtin.set_fact:
    invctl_upgrade_db_driver: "{{ invctl_env['INV_DB_DRIVER'] }}"
    invctl_upgrade_db_dsn: "{{ invctl_env['INV_DB_DSN'] }}"
  no_log: true

- name: Name this upgrade's backup
  ansible.builtin.set_fact:
    # UTC, from the control node, in the format docs/UPGRADE.md uses. One clock
    # for the whole run, so every file from one upgrade shares one stamp.
    invctl_backup_stamp: "{{ lookup('pipe', 'date -u +%Y%m%dT%H%M%SZ') }}"

- name: Work out where this upgrade's backup will go
  ansible.builtin.set_fact:
    invctl_backup_path: >-
      {{ (invctl_backup_dir ~ '/invctl.db.' ~ invctl_backup_stamp)
         if invctl_upgrade_db_driver == 'sqlite'
         else (invctl_backup_dir ~ '/invctl-' ~ invctl_backup_stamp ~ '.dump') }}
    invctl_backup_sidecars: >-
      {{ [invctl_backup_dir ~ '/invctl.db-wal.' ~ invctl_backup_stamp,
          invctl_backup_dir ~ '/invctl.db-shm.' ~ invctl_backup_stamp]
         if invctl_upgrade_db_driver == 'sqlite' else [] }}

- name: Look for a backup already at those names
  ansible.builtin.stat:
    path: "{{ item }}"
  register: invctl_backup_collision
  loop: "{{ [invctl_backup_path] + invctl_backup_sidecars }}"

# THE STAMP IS SECOND-PRECISION AND `copy` OVERWRITES WITHOUT ASKING. Retry an
# upgrade inside the same second -- or re-run after fixing something quickly --
# and the second attempt's backup lands on the first attempt's name. The file it
# would destroy is the most valuable one on the host: the snapshot taken before
# a migration that then went wrong. Refusing costs a second of an operator's
# patience; overwriting costs the rollback.
- name: Refuse to overwrite a backup that is already there
  ansible.builtin.assert:
    that:
      - (invctl_backup_collision.results | selectattr('stat.exists') | list | length) == 0
    fail_msg: >-
      A backup already exists at
      {{ invctl_backup_collision.results | selectattr('stat.exists')
         | map(attribute='stat.path') | join(', ') }}.
      The role will not overwrite it: if a previous attempt failed, that file is
      the state of the database before it did, and it is the rollback. Move it
      aside deliberately, or wait a second and run again -- the name is stamped
      to the second.

- name: Require pg_dump and pg_restore before an upgrade begins
  ansible.builtin.command:
    argv: [sh, -c, 'command -v pg_dump && command -v pg_restore']
  register: invctl_pg_tools
  changed_when: false
  failed_when: false
  when: invctl_upgrade_db_driver == 'postgres'

# EARLY, AND WITH THAT SPECIFIC MESSAGE (spec §8). These must exist on the
# invctl host even though the server does not, and finding that out at the
# moment a backup is needed means finding it out with the service stopped.
- name: Fail early when the PostgreSQL client tools are missing
  ansible.builtin.assert:
    that:
      - invctl_pg_tools.rc == 0
    fail_msg: >-
      pg_dump and pg_restore must both be on PATH on this host: the first takes
      the backup that is the rollback plan, the second is how the role proves
      the backup is readable rather than merely non-empty. Install
      postgresql-client. The database server itself stays where it is.
  when: invctl_upgrade_db_driver == 'postgres'

# "Put it back as it was" has to know what it was. An operator may have stopped
# this service deliberately -- during an incident, or before a maintenance
# window -- and a rescue that starts it regardless is not restoring anything, it
# is making a second unexpected change on top of a failure.
- name: Record whether the service was running before this upgrade
  ansible.builtin.command:
    argv: [systemctl, show, invctl, --property=ActiveState, --value]
  register: invctl_active_before
  changed_when: false
```

- [ ] **Step 2: Write `tasks/upgrade.yml` — the gated block**

```yaml
- name: Stop, back up and prove the backup
  block:
    - name: Stop invctl
      # Stopped first: copying a .db while the process is running gives you a
      # file whose write-ahead log you did not copy (docs/UPGRADE.md).
      ansible.builtin.systemd_service:
        name: invctl
        state: stopped

    - name: Ask systemd what state the unit is in now
      ansible.builtin.command:
        argv: [systemctl, show, invctl, --property=ActiveState, --value]
      register: invctl_active_now
      changed_when: false

    - name: Ask systemd whether a main process remains
      ansible.builtin.command:
        argv: [systemctl, show, invctl, --property=MainPID, --value]
      register: invctl_main_pid_now
      changed_when: false

    - name: Look for any process still running the invctl binary
      # systemd's own view is not enough: it can lose track of a main PID, and
      # a stop that hit TimeoutStopSec leaves the unit deactivating while the
      # process is very much alive. This asks the kernel instead.
      #
      # /proc and readlink rather than lsof or fuser: neither is guaranteed on
      # a minimal Debian container and this role installs no packages. `exit 0`
      # because the loop finding nothing is the good case.
      ansible.builtin.command:
        argv:
          - sh
          - -c
          - >-
            for p in /proc/[0-9]*; do
              [ "$(readlink "$p/exe" 2>/dev/null)" = "{{ invctl_install_dir }}/invctl" ] &&
                echo "${p#/proc/}";
            done; exit 0
      register: invctl_leftover
      changed_when: false

    # THIS ASSERTION IS INSIDE THE BLOCK ON PURPOSE. If it fails, the rescue
    # restores the service and the play aborts with nothing replaced.
    #
    # WITHOUT IT THE GATE HAS A DOOR IT DOES NOT WATCH. `state: stopped` can
    # report success while the process still holds the database open; the copy
    # then runs against a live writer and produces a torn file that is
    # perfectly non-empty. Every later check passes -- size, header, even a
    # checksum against a source that is itself moving -- and the binary is
    # replaced behind a backup that will not restore. The gate measures the
    # backup; this measures the thing that can ruin it.
    - name: The service is actually stopped, not merely asked to stop
      ansible.builtin.assert:
        that:
          - (invctl_active_now.stdout | trim) in ['inactive', 'failed']
          - (invctl_main_pid_now.stdout | trim) == '0'
          - (invctl_leftover.stdout | trim) == ''
        fail_msg: >-
          invctl was asked to stop and has not: systemd reports
          ActiveState={{ invctl_active_now.stdout | trim }},
          MainPID={{ invctl_main_pid_now.stdout | trim }}, and these processes
          are still running the binary: {{ invctl_leftover.stdout | trim | default('none', true) }}.
          Backing up now would copy a database that is still being written to
          and produce a file that is the right size and the wrong contents.
          Nothing has been backed up and nothing will be replaced. Look at
          `journalctl -u invctl` and at TimeoutStopSec before running again.

    - name: Back up the SQLite database
      ansible.builtin.copy:
        src: "{{ invctl_data_dir }}/invctl.db"
        dest: "{{ invctl_backup_path }}"
        remote_src: true
        owner: root
        group: root
        mode: "0600"
      when: invctl_upgrade_db_driver == 'sqlite'

    - name: Look for the write-ahead log files
      ansible.builtin.stat:
        path: "{{ invctl_data_dir }}/invctl.db{{ item }}"
      loop: ["-wal", "-shm"]
      register: invctl_wal
      when: invctl_upgrade_db_driver == 'sqlite'

    - name: Back up the write-ahead log files that exist
      ansible.builtin.copy:
        src: "{{ item.stat.path }}"
        dest: "{{ invctl_backup_dir }}/{{ item.stat.path | basename }}.{{ invctl_backup_stamp }}"
        remote_src: true
        owner: root
        group: root
        mode: "0600"
      loop: "{{ invctl_wal.results | default([]) | selectattr('stat.exists') | list }}"
      loop_control:
        label: "{{ item.stat.path }}"

    - name: Back up the PostgreSQL database
      ansible.builtin.command:
        argv:
          - pg_dump
          - --format=custom
          - "--file={{ invctl_backup_path }}"
          - "{{ invctl_upgrade_db_dsn }}"
      no_log: true          # the DSN carries a password
      changed_when: true
      when: invctl_upgrade_db_driver == 'postgres'

    - name: Stat the backup
      ansible.builtin.stat:
        path: "{{ invctl_backup_path }}"
        get_checksum: true
        checksum_algorithm: sha256
      register: invctl_backup_stat

    # THE GATE, PART ONE: it exists and it is not empty.
    - name: The backup exists and is not empty
      ansible.builtin.assert:
        that:
          - invctl_backup_stat.stat.exists
          - invctl_backup_stat.stat.size > 0
        fail_msg: >-
          The backup at {{ invctl_backup_path }} is missing or empty. There is
          no rollback command in invctl: rollback IS restoring this file. The
          upgrade stops here, with {{ invctl_installed_version }} still
          installed.

    - name: Checksum the live SQLite database for comparison
      ansible.builtin.stat:
        path: "{{ invctl_data_dir }}/invctl.db"
        get_checksum: true
        checksum_algorithm: sha256
      register: invctl_db_live
      when: invctl_upgrade_db_driver == 'sqlite'

    - name: Read the first 15 bytes of the SQLite backup
      # A SQLite file starts with the 16 bytes `SQLite format 3\0`. The first 15
      # are printable ASCII and the sixteenth is a NUL, which would have to
      # survive a round trip through JSON to be compared -- so the role checks
      # the 15 and gets the same guarantee without the encoding problem.
      #
      # `head` rather than the sqlite3 CLI: this role installs no packages on
      # the target, and a database engine is a heavy dependency for reading a
      # magic number.
      ansible.builtin.command:
        argv: [head, -c, "15", "{{ invctl_backup_path }}"]
      register: invctl_backup_magic
      changed_when: false
      when: invctl_upgrade_db_driver == 'sqlite'

    # THE GATE, PART TWO: it is USABLE, not merely present.
    #
    # A TRUNCATED FILE HAS A NON-ZERO SIZE. A copy interrupted by a full disk,
    # or onto a filesystem that reported success and lost the tail, passes part
    # one and restores to nothing. Part one asks "did something get written";
    # part two asks "is what got written the database".
    - name: The SQLite backup is a faithful copy of a SQLite database
      ansible.builtin.assert:
        that:
          - invctl_backup_magic.stdout == 'SQLite format 3'
          - invctl_backup_stat.stat.size == invctl_db_live.stat.size
          - invctl_backup_stat.stat.checksum == invctl_db_live.stat.checksum
        fail_msg: >-
          The backup at {{ invctl_backup_path }} is not a faithful copy of
          {{ invctl_data_dir }}/invctl.db.
          header: {{ invctl_backup_magic.stdout | default('(unreadable)') }} (want 'SQLite format 3')
          size:   {{ invctl_backup_stat.stat.size }} vs {{ invctl_db_live.stat.size }}
          sha256: {{ invctl_backup_stat.stat.checksum }} vs {{ invctl_db_live.stat.checksum }}
          A backup that is the right size and the wrong contents is worse than
          no backup, because it is trusted. Nothing has been replaced.
      when: invctl_upgrade_db_driver == 'sqlite'

    - name: Confirm the PostgreSQL dump can be read back
      # pg_restore --list reads the whole archive's table of contents without
      # touching a database, so it detects a truncated or corrupt custom-format
      # dump for the cost of reading the file. pg_restore ships in
      # postgresql-client, which this host already needs for pg_dump.
      ansible.builtin.command:
        argv: [pg_restore, --list, "{{ invctl_backup_path }}"]
      register: invctl_pg_restore_list
      changed_when: false
      failed_when: false
      when: invctl_upgrade_db_driver == 'postgres'

    - name: The PostgreSQL dump is readable and not empty
      ansible.builtin.assert:
        that:
          - invctl_pg_restore_list.rc == 0
          - invctl_pg_restore_list.stdout_lines | length > 0
        fail_msg: >-
          pg_restore could not read {{ invctl_backup_path }} (exit
          {{ invctl_pg_restore_list.rc }}). A dump that cannot be listed cannot
          be restored, whatever its size says. Nothing has been replaced.
          pg_restore said: {{ invctl_pg_restore_list.stderr | default('') }}
      when: invctl_upgrade_db_driver == 'postgres'

    - name: Report the verified backup
      ansible.builtin.debug:
        msg: >-
          Backup verified: {{ invctl_backup_path }}
          ({{ invctl_backup_stat.stat.size }} bytes, readable). Proceeding to
          replace the binary.
  rescue:
    # ONLY IF IT WAS RUNNING. An operator may have stopped this deliberately;
    # "put it back as it was" must mean as it was, not "started".
    - name: Put the service back exactly as it was
      ansible.builtin.systemd_service:
        name: invctl
        state: started
      when: (invctl_active_before.stdout | trim) == 'active'

    - name: Abort — nothing has been replaced
      ansible.builtin.fail:
        msg: >-
          The upgrade to {{ invctl_version }} stopped before anything was
          replaced. invctl {{ invctl_installed_version }} is still installed, and
          {{ 'has been restarted'
             if (invctl_active_before.stdout | trim) == 'active'
             else 'has been left stopped, because it was not running when this run began' }}.
          Fix the cause and run this again: the failure mode of this role is
          "nothing happened", never "half an upgrade" (spec D1).
```

- [ ] **Step 3: Write `tasks/upgrade.yml` — past the gate**

```yaml
# From here the host changes in ways that cannot be undone by aborting.

# WRITTEN BEFORE THE FIRST WRITE INTO /opt, CLEARED ONLY WHEN THE NEW VERSION IS
# SERVING (main.yml, after verify.yml).
#
# If `invctl -migrate` below fails, the new binary is already on disk and the
# service is stopped. On the next run `-version` MATCHES the pin, so the binary
# axis says NO-OP -- and D7's unconditional configure + verify would START the
# service. invctl migrates automatically at startup, before binding, so that
# start is a second migration attempt made OUTSIDE this gate, against a database
# in whatever state the failure left. The backup above still exists, so this is
# recoverable; it is the role walking silently past a half-finished upgrade,
# which is the one thing D1 says it must never do.
#
# No DSN in this file: it carries a password, and D6's point is one 0600 file
# holding secrets, not two.
- name: Mark this host as mid-upgrade before anything in /opt is touched
  ansible.builtin.copy:
    dest: "{{ invctl_config_dir }}/upgrade-in-progress"
    owner: root
    group: root
    mode: "0600"
    content: |
      An upgrade of invctl started and has not been confirmed finished.

      started_at:   {{ invctl_backup_stamp }}
      from_version: {{ invctl_installed_version }}
      to_version:   {{ invctl_version }}
      db_driver:    {{ invctl_upgrade_db_driver }}
      backup:       {{ invctl_backup_path }}

      The backup above was taken with the service stopped and verified
      readable before the binary was replaced. It is the rollback.
      See docs/UPGRADE.md, "When a migration fails".
      Delete this file by hand once you have decided what happened.

- name: Remember that this run wrote the marker
  ansible.builtin.set_fact:
    invctl_upgrade_started: true

- name: Keep the outgoing binary
  # Rolling back means restoring the database backup AND putting this binary
  # back. A new binary against an old schema is not a supported combination
  # (docs/UPGRADE.md). The role keeps both and performs neither: rollback is a
  # decision with data loss in it, made by a person who has looked at what they
  # are about to lose.
  ansible.builtin.copy:
    src: "{{ invctl_install_dir }}/invctl"
    dest: "{{ invctl_install_dir }}/invctl.{{ invctl_installed_version }}"
    remote_src: true
    owner: root
    group: root
    mode: "0755"

- name: Install the verified new binary
  ansible.builtin.copy:
    src: "{{ invctl_stage.path }}/invctl_{{ invctl_version }}_linux_amd64"
    dest: "{{ invctl_install_dir }}/invctl"
    remote_src: true
    owner: root
    group: root
    mode: "0755"

- name: Apply migrations with the new binary, as the invctl user
  # AS THE INVCTL USER, NOT ROOT. On SQLite, root would create root-owned -wal
  # and -shm files beside the database that the service then cannot write,
  # turning a successful migration into a service that will not start (spec §4
  # step 6). runuser rather than Ansible's become, because become from root to
  # an unprivileged user needs setfacl on the target and this role installs no
  # packages.
  #
  # -m preserves the environment below. -u execs directly and does not use the
  # user's shell, which is /usr/sbin/nologin.
  #
  # The config file is 0600 root:root (D6), so the invctl user CANNOT read its
  # own configuration -- and -migrate runs after config.Load
  # (cmd/invctl/main.go:125), so it needs the two database variables. They are
  # passed explicitly here, with no_log, rather than by loosening the file, and
  # they are the HOST's values rather than the inventory's, for the reason the
  # preflight gives. It needs nothing else: local auth is on by default, so
  # validation passes.
  ansible.builtin.command:
    argv:
      - runuser
      - -u
      - invctl
      - -m
      - --
      - "{{ invctl_install_dir }}/invctl"
      - -migrate
  environment:
    INV_DB_DRIVER: "{{ invctl_upgrade_db_driver }}"
    INV_DB_DSN: "{{ invctl_upgrade_db_dsn }}"
  no_log: true
  changed_when: true
```

- [ ] **Step 4: Wire it into `tasks/main.yml`**

Replace the `Refuse an upgrade this role cannot yet perform` task with:

```yaml
- name: Upgrade
  ansible.builtin.include_tasks: upgrade.yml
  when:
    - invctl_installed_version != ''
    - invctl_installed_version != invctl_version
```

and append, **after** the existing `Start it and prove it is serving`:

```yaml
# CLEARED ONLY HERE: after -migrate returned zero AND /healthz answered 200. If
# either failed, the play has already stopped and the marker is still on disk,
# which is what makes the next run refuse instead of starting the service into a
# second ungated migration.
- name: Clear the upgrade marker now that the new version is serving
  ansible.builtin.file:
    path: "{{ invctl_config_dir }}/upgrade-in-progress"
    state: absent
  when: invctl_upgrade_started | default(false)
```

- [ ] **Step 5: Confirm the 1.1.1 release assets exist before relying on them**

```bash
for f in invctl_1.1.1_linux_amd64 invctl_1.1.1_checksums.txt; do
  curl -sIL -o /dev/null -w "%{http_code} $f\n" \
    "https://github.com/madalinignisca/invctl/releases/download/v1.1.1/$f"
done
```
Expected: `200` for both. If either is not, use `1.1.0` throughout and say so in
the commit; do not proceed against a version whose assets do not exist.

- [ ] **Step 6: Confirm `runuser -m` actually carries the DSN**

The resolution of the D6/§4 contradiction. Nothing should depend on it until it
has been seen working.

```bash
cd /home/gabriel/apps/infra-inventory/deploy/ansible
./test/incus-harness.sh up
sudo incus exec invctl-ansible-test -- useradd --system --no-create-home --shell /usr/sbin/nologin probe
sudo incus exec invctl-ansible-test -- env FOO=bar runuser -u probe -m -- sh -c 'echo "FOO=$FOO"'
./test/incus-harness.sh down
```
Expected: `FOO=bar`. If it prints `FOO=` then `-m` does not preserve here;
change the task to
`argv: [runuser, -u, invctl, --, env, "INV_DB_DRIVER={{ invctl_upgrade_db_driver }}", "INV_DB_DSN={{ invctl_upgrade_db_dsn }}", "{{ invctl_install_dir }}/invctl", -migrate]`
and add a comment recording that the DSN is then briefly visible in `ps` to
other users on the host, with `no_log` still set. Do **not** reach for root as
the way out — spec §4 step 6 names why.

- [ ] **Step 7: Scenario 7.3 — a real upgrade, 1.1.1 to 1.2.0**

```bash
cd /home/gabriel/apps/infra-inventory/deploy/ansible
./test/incus-harness.sh down; ./test/incus-harness.sh up
for v in 1.1.1 1.2.0; do
  curl -fsSL -o roles/invctl/files/invctl_${v}_linux_amd64 \
    "https://github.com/madalinignisca/invctl/releases/download/v${v}/invctl_${v}_linux_amd64"
  curl -fsSL -o roles/invctl/files/invctl_${v}_checksums.txt \
    "https://github.com/madalinignisca/invctl/releases/download/v${v}/invctl_${v}_checksums.txt"
done
BASE="-e invctl_source=files -e invctl_secure_cookies=false"
SEED="-e invctl_admin_password=harness-only-not-a-real-password"

./test/incus-harness.sh run $BASE $SEED -e invctl_version=1.1.1
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
./test/incus-harness.sh run $BASE          # invctl_version is 1.2.0 from defaults
```
Expected: `v1.1.1` healthy, then `action=UPGRADE`, the "Backup verified" debug
line, and `/healthz` answering. No `invctl_admin_password` on the second run: an
upgrade is not a fresh install.

```bash
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
sudo incus exec invctl-ansible-test -- ls -l /opt/invctl/
sudo incus exec invctl-ansible-test -- ls -l /var/backups/invctl/
sudo incus exec invctl-ansible-test -- ls -l /etc/invctl/
sudo incus exec invctl-ansible-test -- curl -fsS http://127.0.0.1:8080/healthz; echo
sudo incus exec invctl-ansible-test -- ls -l /var/lib/invctl/
```
Expected: `v1.2.0`; `invctl` **and** `invctl.1.1.1`; a non-zero-byte
`invctl.db.<stamp>` in the backup directory; **no `upgrade-in-progress` in
`/etc/invctl`** — it was written and then cleared; a 200 from `/healthz`; and
every file in `/var/lib/invctl` **including any `-wal` and `-shm`** owned by
`invctl`, not root. That last check is what proves the migration did not run as
root.

- [ ] **Step 8: The gate refuses an EMPTY backup — four assertions**

Recreate the container rather than downgrading in place. **Restoring the 1.1.1
binary onto an already-migrated schema is the unsupported combination this plan
itself warns about**, so a test built that way can fail for a reason unrelated
to the gate — and a test that can fail for the wrong reason proves nothing about
its subject.

```bash
cp roles/invctl/tasks/upgrade.yml /tmp/upgrade.yml.bak
./test/incus-harness.sh down; ./test/incus-harness.sh up
./test/incus-harness.sh run $BASE $SEED -e invctl_version=1.1.1
```

Now force the backup to produce an empty file — replace the body of the
`Back up the SQLite database` task with:

```yaml
    - name: Back up the SQLite database
      ansible.builtin.file:
        path: "{{ invctl_backup_path }}"
        state: touch
        owner: root
        group: root
        mode: "0600"
      when: invctl_upgrade_db_driver == 'sqlite'
```

```bash
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
sudo incus exec invctl-ansible-test -- systemctl is-active invctl
sudo incus exec invctl-ansible-test -- ls -l /opt/invctl/
sudo incus exec invctl-ansible-test -- ls -l /etc/invctl/
```
Expected, all four — **keep these exactly as written; they are jointly
sufficient and an unrelated failure does not produce the combination**:

1. the play **fails** at `The backup exists and is not empty`, with the "no
   rollback command" message and then the rescue's abort message;
2. `PLAY EXIT` is non-zero;
3. `invctl -version` still reports **`v1.1.1`**;
4. the service is **`active`** — the rescue restarted it, because it was running
   — and `/opt/invctl/` holds **no** preserved copy and `/etc/invctl/` holds
   **no** `upgrade-in-progress`, because nothing past the gate ran.

- [ ] **Step 9: The gate refuses a CORRUPT backup that is not empty**

The case part one cannot see. Same container, still on 1.1.1.

Restore `upgrade.yml`, then instead make the copy truncate — replace the `Back
up the SQLite database` task body with a `command` that copies only the first
4096 bytes:

```yaml
    - name: Back up the SQLite database
      ansible.builtin.command:
        argv: [sh, -c, "head -c 4096 {{ invctl_data_dir }}/invctl.db > {{ invctl_backup_path }}"]
      changed_when: true
      when: invctl_upgrade_db_driver == 'sqlite'
```

```bash
cp /tmp/upgrade.yml.bak roles/invctl/tasks/upgrade.yml   # then apply the edit above
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
sudo incus exec invctl-ansible-test -- ls -l /var/backups/invctl/
```
Expected: the play fails at **`The SQLite backup is a faithful copy of a SQLite
database`** — *not* at part one, because the file is 4096 bytes and has a valid
`SQLite format 3` header. The size and checksum comparisons are what catch it.
`v1.1.1` still installed, no preserved copy, no marker. The backup file is left
on disk, which is correct: the role does not delete evidence.

```bash
cp /tmp/upgrade.yml.bak roles/invctl/tasks/upgrade.yml
```

Then delete the whole `The SQLite backup is a faithful copy` assertion, re-run
the truncated case, and watch the upgrade **succeed** behind a 4 KB backup.
Restore with `cp`. That is the defect part two exists for, observed.

- [ ] **Step 10: The stop is proven, not assumed**

Same container, still on 1.1.1. Make the stop lie: point the assertion's inputs
at a unit that is still running, by replacing the `Stop invctl` task with a
no-op so the service is never stopped at all.

```yaml
    - name: Stop invctl
      ansible.builtin.debug:
        msg: "deliberately not stopping, to prove the next assertion bites"
```

```bash
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
sudo incus exec invctl-ansible-test -- systemctl is-active invctl
sudo incus exec invctl-ansible-test -- ls -l /var/backups/invctl/
```
Expected: the play fails at **`The service is actually stopped, not merely asked
to stop`**, naming the surviving PID; `v1.1.1` still installed; the service
still `active`; and **no backup file was created**, because the assertion sits
before the first copy.

```bash
cp /tmp/upgrade.yml.bak roles/invctl/tasks/upgrade.yml
```

Now delete that assertion, re-run with the same no-op stop, and watch a backup
be taken from a live database and the upgrade proceed. The file will usually
even be byte-identical — which is the point: **the failure is intermittent and
therefore invisible until it matters.** Restore with `cp`.

- [ ] **Step 11: An interrupted migration marks the host, and the next run refuses**

The most valuable test in this task, because the defect it guards was invisible
in review.

```bash
./test/incus-harness.sh down; ./test/incus-harness.sh up
./test/incus-harness.sh run $BASE $SEED -e invctl_version=1.1.1
cp roles/invctl/tasks/upgrade.yml /tmp/upgrade.yml.bak
```

Make `-migrate` fail — replace the `Apply migrations` task's `argv` with
`[sh, -c, 'exit 1']`, keeping everything else.

```bash
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
sudo incus exec invctl-ansible-test -- systemctl is-active invctl
sudo incus exec invctl-ansible-test -- cat /etc/invctl/upgrade-in-progress
```
Expected: the play fails; `invctl -version` now reports **`v1.2.0`** (the binary
*was* replaced, which is exactly the half-finished state); the service is
**inactive**; and the marker names the from- and to-versions and the backup
path.

Now restore `upgrade.yml` and run again — the run an operator would naturally
make:

```bash
cp /tmp/upgrade.yml.bak roles/invctl/tasks/upgrade.yml
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- systemctl is-active invctl
```
Expected: the play **fails at the very first task**, `Refuse to proceed past an
interrupted upgrade`, printing the marker and the `rm` command — and the service
is **still inactive**, because the role did nothing at all.

**That is the whole finding.** Without the marker this run would have seen a
version matching the pin, taken the NO-OP path, converged configuration and
*started the service* — applying migrations automatically at startup, outside
the gate, to a database nobody had looked at.

Prove it, then put it back:

```bash
sudo incus exec invctl-ansible-test -- rm /etc/invctl/upgrade-in-progress
./test/incus-harness.sh run $BASE
sudo incus exec invctl-ansible-test -- systemctl is-active invctl
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
```
Expected: with the marker cleared by hand the run proceeds, the service starts,
and `/healthz` answers on v1.2.0 — the documented recovery works.

To see the defect itself, delete the `Look for an interrupted upgrade` /
`Refuse to proceed` pair from `main.yml`, redo the failed-migration setup, and
watch the next run start the service silently. Restore with `cp`.

- [ ] **Step 12: The role refuses to move the database and the version together**

```bash
./test/incus-harness.sh down; ./test/incus-harness.sh up
./test/incus-harness.sh run $BASE $SEED -e invctl_version=1.1.1
./test/incus-harness.sh run $BASE \
  -e invctl_db_dsn='file:/var/lib/invctl/elsewhere.db?_txlock=immediate' ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- /opt/invctl/invctl -version
sudo incus exec invctl-ansible-test -- ls -l /var/backups/invctl/ /var/lib/invctl/
```
Expected: the play fails at `Refuse to change the database and the version in
one run`, telling the operator to do it in two; `v1.1.1` still installed; **no
backup taken and no `elsewhere.db` created**, because the refusal is in the
preflight, before the service is even stopped.

Then confirm the two-step path works: move the DSN alone (no version change),
watch it converge and restart, then bump the version and watch the upgrade back
up `elsewhere.db` rather than the old file.

- [ ] **Step 13: The role refuses to overwrite an existing backup**

**Do this deterministically. Do not race the clock.** Guessing the stamp by
calling `date` yourself and hoping the play starts in the same second is a test
that usually passes because the collision never happened — which proves nothing
and looks identical to a pass that proves everything. Read the stamp the play
itself chose, then create the collision against that exact value:

```bash
# 1. One run that gets far enough to report its stamp, then fails the gate
#    for the unrelated reason we already know how to force (Step 8's touch).
./test/incus-harness.sh run $BASE 2>&1 | tee /tmp/run1.log
STAMP=$(grep -o 'invctl_backup_stamp[^0-9]*\([0-9TZ]\{16\}\)' /tmp/run1.log \
        | grep -o '[0-9]\{8\}T[0-9]\{6\}Z' | head -1)
test -n "$STAMP" || { echo "no stamp in the play output -- add a debug task"; exit 1; }

# 2. Put a file exactly where the NEXT run would write, using a stamp we control.
sudo incus exec invctl-ansible-test -- \
  sh -c "echo pretend > /var/backups/invctl/invctl.db.FIXED"

# 3. Pin the stamp so the collision is certain, not probable.
./test/incus-harness.sh run $BASE -e invctl_backup_stamp=FIXED ; echo "PLAY EXIT: $?"
```

Expected: a failure at `Refuse to overwrite a backup that is already there`
naming `/var/backups/invctl/invctl.db.FIXED`, non-zero exit, and the binary
untouched.

Pinning the stamp through `-e` is what makes this deterministic, so
`invctl_backup_stamp` must be settable from the command line — it is a
`set_fact` in preflight, so give it an `| default(...)` form that an extra
variable can override, and note in the task comment that the override exists
for this test and is not an operator-facing knob.

Then remove the collision check and re-run the same three commands: the play
must succeed and `invctl.db.FIXED` must now contain a real database instead of
the word `pretend` — which is the destruction the check exists to prevent, and
the reason this test is worth making deterministic rather than leaving to luck.
Restore it. Record in `test/README.md` that the stamp is overridable for this
test only.

- [ ] **Step 14: A deliberately stopped service is left stopped**

```bash
./test/incus-harness.sh down; ./test/incus-harness.sh up
./test/incus-harness.sh run $BASE $SEED -e invctl_version=1.1.1
sudo incus exec invctl-ansible-test -- systemctl stop invctl
sudo incus exec invctl-ansible-test -- systemctl is-active invctl   # inactive
```

Force the gate to fail again (the `state: touch` edit from Step 8), run, and
check what the rescue did:

```bash
./test/incus-harness.sh run $BASE ; echo "PLAY EXIT: $?"
sudo incus exec invctl-ansible-test -- systemctl is-active invctl
cp /tmp/upgrade.yml.bak roles/invctl/tasks/upgrade.yml
```
Expected: the play fails at the gate, the abort message says the service *was
not running when this run began*, and `systemctl is-active` still reports
**inactive**. A rescue that started it would have made a second unexpected
change on top of a failure, during whatever incident had it stopped.

- [ ] **Step 15: The run after an upgrade is quiet**

An upgrade that leaves the host in a state where the *next* run is noisy undoes
Task 5's property without anyone noticing.

```bash
./test/incus-harness.sh down; ./test/incus-harness.sh up
./test/incus-harness.sh run $BASE $SEED -e invctl_version=1.1.1
./test/incus-harness.sh run $BASE
./test/incus-harness.sh run $BASE | grep -E 'changed=0 .*failed=0'
```
Expected: **`changed=0`**.

- [ ] **Step 16: Tear down and commit**

```bash
./test/incus-harness.sh down
sudo incus list --format csv -c n    # mailadmin-test still present
cd /home/gabriel/apps/infra-inventory
git diff --stat deploy/ansible                   # confirm no mutation survived
git add deploy/ansible
git commit -m "deploy: upgrade behind a gate that proves the stop, the backup and the finish"
```

---

### Task 7: The README, and the sentence that stops it looking broken (D3)

**Files:**
- Create: `deploy/ansible/README.md`
- Modify: `docs/INSTALL.md`
- Modify: `docs/UPGRADE.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: the finished role.
- Produces: nothing code depends on. **This is a deliverable, not a footnote.**

**The consequence of D3 has to be stated loudly**, because it is surprising:
after a successful run, nothing answers from outside the host. That is correct
and it is indistinguishable from a broken deployment if the README does not say
so first — spec §8 names it as the first risk. **And the marker needs its own
section**, because an operator who meets it meets it at the worst possible
moment and the message has to be backed by a page.

- [ ] **Step 1: Write `deploy/ansible/README.md`**

HTML-comment licence notice, then, in this order:

1. **"After a successful run, nothing answers from outside this host."** First
   heading after the title, not a note near the end. invctl binds
   `127.0.0.1:8080` by design, this role installs no reverse proxy, no TLS
   certificate and no DNS, and `curl http://127.0.0.1:8080/healthz` **on the
   host** is how you confirm it works. Name the two settings that matter once
   there is TLS in front — `INV_SECURE_COOKIES` (`invctl_secure_cookies`) and
   the `X-Forwarded-Proto` header — and point at "Two settings behind a proxy"
   in `docs/INSTALL.md`, stating that they work as a pair.
2. **Quick start.** Copy `inventory/hosts.example`, pass
   `invctl_admin_password` from a vault **for the first run against a host**,
   run `ansible-playbook -i inventory/hosts playbooks/invctl.yml`.
3. **The variable table**, verbatim from spec §2, with `invctl_admin_password`
   marked required-on-a-fresh-install-only and never defaulted.
4. **What a second run does — D7.** Two axes, stated plainly: the **binary** is
   only touched when `invctl_version` differs from what is installed; the
   **configuration** is rewritten every run and the service restarts **only if
   the rendered file actually differs**. An out-of-band edit to
   `/etc/invctl/invctl.env` is corrected on the next run — that is the point of
   running this on a schedule. A run where nothing differs reports `changed=0`.
5. **What the role reads back off the host, and what that costs you.**
   `INV_SESSION_KEY` (regenerating it would sign everybody out on every run) and
   `INV_ADMIN_PASSWORD` when no variable is supplied. State that the seed
   password persists in a `0600 root:root` file, is **inert once any account
   exists** (`ensureAdmin` returns early), and **can be deleted by hand without
   the role putting it back**. State that the role does not delete it for you,
   and why: that would be stateful, and a first install whose seeding half-failed
   could then never retry.
   Then state the constraint that makes the read-back safe: **the role accepts
   only lines of the form `KEY='value'` and refuses the whole run on anything
   else**, including a value containing a quote or a line break. Give the exact
   refusal an operator will see and what to do about it — fix the line, or move
   the file aside and re-seed, accepting that every session ends because the
   key goes with it.
6. **"The upgrade did not finish" — the marker.** Its own heading. What
   `/etc/invctl/upgrade-in-progress` means, that the role will do **nothing at
   all** while it is there (not even converge configuration), why that is
   stricter than it looks (starting invctl applies migrations, outside the
   gate), what the file records, that the backup it names is the rollback, the
   pointer to `docs/UPGRADE.md` "When a migration fails", and the exact `rm`
   that clears it. Say plainly that there is **no variable** to clear it and
   that this is deliberate.
7. **Upgrading.** The seven ordered steps, plus the four things the role does
   around them: proves the stop, backs up the database the *host* was using,
   proves the backup is readable, and marks the host until the new version is
   serving. Then: there is no rollback command; rollback is restoring the backup
   **and** putting `/opt/invctl/invctl.<oldversion>` back; `--check` shows what a
   version bump would do before it does it.
8. **Two changes the role will not make in one run.** Moving the database
   (`invctl_db_dsn`) and bumping `invctl_version` together is refused, with the
   reason — the backup and the migrations would apply to the new database while
   the data sits in the old one — and the two-run recipe.
9. **Backups.** Where they go, that names are stamped to the second, and that
   the role **refuses rather than overwrites** if a name is taken, because the
   file it would destroy is the snapshot from before a failed attempt. Nothing
   prunes them; that is the operator's.
10. **Offline / segmented environments.** `invctl_source: files`, the two exact
    filenames, where they go, that the checksum is verified identically on both
    paths and is not optional, and that the checksums file must contain exactly
    one record for the binary.
11. **PostgreSQL.** External only; the role never installs a server;
    `postgresql-client` must be on the invctl host for `psql` (startup
    reachability), `pg_dump` (the backup) and `pg_restore` (proving the backup
    is readable), even though the server is elsewhere.
12. **Non-goals**, copied from the spec: no proxy/TLS/DNS, no database server,
    no firewall management, no rollback automation, `linux/amd64` only, one host.

- [ ] **Step 2: Add a pointer in `docs/INSTALL.md`**

One short section after "The systemd unit", before "TLS in front": this page is
the procedure a person follows by hand, and `deploy/ansible/` is the same
procedure as something that runs. Say that the role encodes exactly this page,
including the `EnvironmentFile=` warning — and that it goes one step further and
uses `EnvironmentFile=` unconditionally rather than only when a database
password is involved. State that this page stays authoritative for what each
setting means. Add `deploy/ansible/README.md` to the "Next" list.

- [ ] **Step 3: Add a paragraph to `docs/UPGRADE.md`**

In "When a migration fails", after step 4: if the upgrade was run by the Ansible
role, `/etc/invctl/upgrade-in-progress` is on the host and names the backup, and
**no further playbook run will do anything until it is removed by hand** — which
is the role's way of stopping a well-meant re-run from starting the service and
migrating again outside the gate. This belongs here and not only in the role's
README, because this is the page somebody reads at 03:00.

- [ ] **Step 4: Add a `CHANGELOG.md` entry**

Under a new **Unreleased** section, in **Added** — not **Action required**:
nothing an existing operator must do changes. Name the role, the offline path,
the backup gate and the interrupted-upgrade marker. Then, because D4 makes it
matter, state that `deploy/ansible/roles/invctl/defaults/main.yml` must be
bumped with every release and that `make test` fails until it is.

- [ ] **Step 5: Confirm the D4 test still passes against the edited changelog**

```bash
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
cd /home/gabriel/apps/infra-inventory
go test ./internal/license/ -run TestTheAnsibleRolePinMatchesTheChangelog -count=1 -v
```
Expected: PASS. An `## [Unreleased]` heading must not match `changelogNewest` —
the pattern requires digits. **If this fails, the regex is wrong, not the
changelog.**

- [ ] **Step 6: Full gate, then commit**

```bash
make test 2>&1 | tail -30
make lint 2>&1 | tail -20
bash tools/manual-stale.sh
```
Expected: green, and no manual fragment reported stale (nothing in `internal/`
changed except one test file).

```bash
git add deploy/ansible/README.md docs/INSTALL.md docs/UPGRADE.md CHANGELOG.md
git commit -m "docs(deploy): what the role does, what it refuses, and why nothing answers"
```

---

## Evidence gate

Before any reviewer is invoked, state what would be true if this were broken and
what was run to show it is not. **Attack the evidence, not the style.**

| If this were broken | What was run |
|---|---|
| The role deploys a version older than the newest release, silently | Task 1 Step 3 — the pin was edited to 1.1.1, the test went red naming the file, and was restored |
| The offline path stopped verifying checksums | Task 3 Step 4 — the binary was corrupted and the play refused with both hashes; Step 6 — `github` and `files` produced the **same** hash from the **same** task file |
| A checksums record for a *different* file is accepted as ours | Task 3 Step 5 — a decoy record for `other_invctl_1.2.0_linux_amd64` was added and ignored; a duplicated record and an empty file were each refused by the exactly-one assertion; removing the anchor was shown to change the outcome |
| An air-gapped operator gets "file not found" and no idea which file | Task 3 Step 6 — the checksums file was removed and the refusal named both filenames and the directory |
| `systemctl start` succeeded but the application was not serving | Task 4 Step 11 — `/healthz` returned 200 and the binary reported v1.2.0 |
| The DSN password is world-readable via `systemctl show` | Task 4 Step 11 — `systemctl show invctl \| grep -c INV_ADMIN_PASSWORD` returned `0`, and the env file is `root root 600` |
| The role writes a file its own parser would reject | Task 4 Step 11 — every line of the rendered file matches the one accepted form, counted on the host |
| A run with identical inputs restarts the service and signs everybody out | Task 5 Step 2 — `changed=0`, `ActiveEnterTimestamp` unchanged, env file byte-identical, **and no admin password supplied** |
| **The role cannot converge configuration — the pre-D7 failure** | **Task 5 Steps 3-4 — `invctl_listen` changed with no version bump and the host moved to the new port and restarted while the binary stayed v1.2.0; then the file was edited out of band and the next run corrected it** |
| **An unreadable or half-written config file is read as empty, minting a new session key and signing everybody out** | **Task 5 Steps 5-6 — truncated, double-quoted and duplicated assignments each stopped the play with the file unchanged; then the old fail-open shape was restored and the run was watched succeeding while replacing the key** |
| A value containing a newline injects a second setting | Task 5 Step 7 — a newline in `invctl_admin_users` was refused; with the check removed it was watched setting `INV_SECURE_COOKIES` on the host |
| A deleted seed-password line is silently restored | Task 5 Step 8 — deleted, and still absent after two runs, the second of which was `changed=0` |
| `changed=0` means the role is inert rather than converged | Task 5 Step 9 — the binary was deleted and the next run reinstalled it |
| **The service is asked to stop, does not, and the backup is torn** | **Task 6 Step 10 — the stop was replaced by a no-op; the play refused at `The service is actually stopped` naming the live PID, with no backup file created. With that assertion deleted, a backup was taken from a live database and the upgrade proceeded** |
| The upgrade replaces the binary with no usable backup | Task 6 Step 8 — the backup was forced empty; the play aborted at the gate, the service was restarted, `invctl -version` still reported v1.1.1, no preserved copy and no marker |
| **A truncated backup passes because it is not empty** | **Task 6 Step 9 — the backup was truncated to 4096 bytes with a valid SQLite header; the play refused at the faithful-copy assertion, not at the size one. With that assertion deleted, the upgrade succeeded behind a 4 KB backup** |
| **A failed migration is walked past on the next run, starting the service into a second ungated migration** | **Task 6 Step 11 — `-migrate` was forced to fail; the marker was written and the binary was already v1.2.0; the next run refused at the FIRST task with the service still inactive; clearing the marker by hand let it recover** |
| **The upgrade backs up and migrates the wrong database** | **Task 6 Step 12 — `invctl_db_dsn` and `invctl_version` changed together were refused in the preflight, with no backup taken and no new database file created; the two-run path was then shown to work** |
| A retried upgrade overwrites the backup from the attempt that failed | Task 6 Step 13 — a file at the computed name caused a refusal rather than an overwrite |
| A rescue starts a service the operator had deliberately stopped | Task 6 Step 14 — stopped by hand, gate forced to fail, service still `inactive` and the abort message said so |
| The previous binary is not kept, so rollback is impossible | Task 6 Step 7 — `/opt/invctl/invctl.1.1.1` present after the upgrade |
| Migrations ran as root and left `-wal`/`-shm` the service cannot write | Task 6 Step 6 — the `runuser` mechanism was checked in isolation first; Step 7 listed `/var/lib/invctl` and every file including the WAL was owned by `invctl` |
| An upgrade leaves the host where the next run is noisy | Task 6 Step 15 — the run after the upgrade reported `changed=0` |
| An operator deploys this and thinks it is broken because nothing answers | Task 7 Step 1 — first heading in the README, with the confirmation command |
| An operator meets the marker at 03:00 and finds nothing written about it | Task 7 Steps 1 and 3 — its own README section, and a paragraph in `docs/UPGRADE.md` where somebody actually looks |
| The test harness destroyed unrelated work | Task 2 Step 3 and every teardown — `sudo incus list` shows `mailadmin-test` present before and after every scenario |

**Every mutation above was restored with `cp` from a saved copy.** `git diff
--stat deploy/ansible` is run before the final commit (Task 6 Step 16) to
confirm none survived.

**Explicit testing skips, stated rather than left as oversights:**

- **No molecule and no ansible-lint.** Neither is installed on this host and
  adding either needs sign-off. `--syntax-check`, `--check` and the scenarios
  above against a real container cover more than a lint pass would.
- **No E2E browser test.** Nothing here changes a page. The application's own
  Playwright suite (`docs/E2E.md`) is unaffected, and the browser layer has
  nothing to say about whether a backup gate held.
- **The PostgreSQL scenarios are not run end-to-end.** The role's
  PostgreSQL-specific code is seven tasks — the DSN assertion, the `psql`
  presence and reachability checks, the `pg_dump`/`pg_restore` presence check,
  the `pg_dump` backup, the `pg_restore --list` readability check and the
  migration's environment — and exercising them needs a second container running
  a database server, which is scope D2 explicitly excludes. The SQLite path is
  the default, is what `docs/INSTALL.md` recommends for the estate size this
  targets, and is fully exercised including every gate. **Flag this to the
  reviewer rather than letting it look covered**: the postgres branch has been
  read, not run. In particular the `pg_restore --list` readability check has the
  same *shape* as the SQLite faithful-copy check that Task 6 Step 9 proves, but
  it has not itself been seen refusing.
- **The backup-collision test pins the stamp rather than racing the clock**
  (Task 6 Step 13). An earlier draft guessed the timestamp by calling `date`
  and hoped the play started in the same second — which usually passes because
  the collision never happened, and looks identical to a pass that proves
  something. `invctl_backup_stamp` is overridable via `-e` for this test alone,
  so the collision is certain. Noted here because the override is production
  code existing for a test, which is a cost worth stating: the alternative was
  a guard whose test mostly did not exercise it.
- **`--check` is not asserted task-by-task.** It is run once, on a converged
  host, and asserted only to not fail.
