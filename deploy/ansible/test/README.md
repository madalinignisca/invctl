<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Testing `roles/invctl` against a throwaway Incus container

The one target any test in this repository uses is `invctl-ansible-test`, a
Debian 13 Incus container defined by `deploy/ansible/inventory/incus-test.ini`
and created/destroyed by `incus-harness.sh`. **There is an unrelated
container on this host, `mailadmin-test`, belonging to different work — every
teardown here names `invctl-ansible-test` literally, never a pattern, a loop
over `incus list`, or `--all`.**

## The three subcommands

```bash
./test/incus-harness.sh up     # create the container, install python3
./test/incus-harness.sh run [ansible-playbook args...]
./test/incus-harness.sh down   # delete invctl-ansible-test, and only it
```

`incus` needs `sudo` on this host (the socket is root-only) — the script
already runs every Incus and Ansible command through `sudo`; that is not to
be "fixed" by adding a user to a group.

**Always run `down`, including after a failure.** `down` is idempotent —
"already gone" is success — but it is NOT idempotent to skip it: a container
left behind makes the next `up` collide on the name, and it counts against
the same host as `mailadmin-test`.

**Several scenarios below recreate the container rather than reusing it.**
That is deliberate and cheap. Reusing a host whose schema has already been
migrated forward would mean testing a downgrade, which invctl does not
support — and a test that can fail for an unsupported reason proves nothing
about the property it claims to guard. None of Task 5's scenarios need a
fresh container (they only ever go forward from 1.2.0 to 1.2.0), but Task 6's
upgrade scenarios do, and the same harness commands apply there unchanged.

## Before every `up`

The release artefacts in `roles/invctl/files/` are **gitignored and must be
re-downloaded** (or copied from a mirror with no egress to github.com) before
`-e invctl_source=files` will work:

```bash
curl -fsSL -o roles/invctl/files/invctl_1.2.0_linux_amd64 \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_linux_amd64
curl -fsSL -o roles/invctl/files/invctl_1.2.0_checksums.txt \
  https://github.com/madalinignisca/invctl/releases/download/v1.2.0/invctl_1.2.0_checksums.txt
```

## Common flags every scenario below uses

```bash
BASE="-e invctl_source=files -e invctl_secure_cookies=false"
```

- `-e invctl_source=files` — the container has **no egress to github.com**;
  the default (`github`) source cannot be exercised here. Point
  `invctl_release_base_url` at a local mirror instead if you specifically
  need to prove the download path (see `defaults/main.yml`).
- `-e invctl_secure_cookies=false` — the container serves plain HTTP, and
  invctl sets the `Secure` cookie flag by default (correct behind TLS, which
  is where this is meant to end up). Without this the health check and any
  cookie-bearing request would fail on a technicality unrelated to what is
  under test.
- `-e invctl_admin_password=...` is needed **only** for the first run against
  a fresh container. `ensureAdmin` returns early once any account exists, so
  every scenario after the first install omits it — and Step 2 in Task 5
  omits it *specifically* to prove that requirement actually holds.

## Task 5 scenarios — convergence, and a broken config stops the run (D7)

Every command below is `cd deploy/ansible` first. Container: `up` once at the
top, `down` once at the bottom; individual scenarios reuse it, per the note
above.

### 1. Install, capture the before state

```bash
./test/incus-harness.sh up
./test/incus-harness.sh run $BASE -e invctl_admin_password=harness-only-not-a-real-password
sudo incus exec invctl-ansible-test -- systemctl show invctl -p ActiveEnterTimestamp
sudo incus exec invctl-ansible-test -- sha256sum /etc/invctl/invctl.env
```
Proves nothing by itself; it is the baseline every later scenario diffs
against.

### 2. Three halves, all under D7

**Half 1 — identical inputs change nothing.** Re-run with the same flags
(password omitted, proving spec §2's "required only on a fresh install"
actually holds). Expect `changed=0`, an unchanged `ActiveEnterTimestamp`, and
a byte-identical `invctl.env` (same session key, same admin password).

**Half 2 — a changed setting converges without a version bump.** Run again
with `-e invctl_listen=127.0.0.1:8099`, with `invctl_version` untouched.
Expect: a non-zero `changed` count, `INV_LISTEN='127.0.0.1:8099'` on disk, a
200 from the new port, a *later* `ActiveEnterTimestamp`, and
`/opt/invctl/invctl -version` still reporting `v1.2.0` — the two axes (binary
vs. configuration) are independent. Revert by re-running without the
override; the run converges back to `:8080` and reports `changed` again.
**This is the half a pre-D7 draft would have gotten backwards**: it would
have reported `changed=0` and left the host on the wrong port.

Then an out-of-band edit (`sed` the listen address directly on the host,
bypassing Ansible) is corrected on the next plain run — the tampered value is
overwritten, not preserved, which is the whole point of D7: the file and the
unit are templated on *every* run, not just when the role remembers to.

**Half 3 — a file this role cannot parse stops the run.** See "Malformed
configuration" below; it was previously a separate numbered step but belongs
conceptually with the other two D7 halves, since all three are the claim
"configuration converges on every run, correctly, or not at all."

### Malformed configuration: three shapes, one refusal

`tasks/read_env.yml` accepts exactly one line form: blank, a `#` comment, or
`INV_KEY='value'` with no single quote inside the value. Anything else is
refused rather than guessed at, because the session key lives in this file
and reading it wrongly signs out every user.

Each of the three is applied to a saved-good copy of `invctl.env`
(`/root/invctl.env.good` on the container), and each fails at the same named
task, **`Refuse a configuration file this role cannot have written`**, with a
non-zero playbook exit and the file left untouched:

- **(a) truncated value** — `INV_SESSION_KEY='abc` (no closing quote). Passes
  any check that merely asks "does the file mention `INV_SESSION_KEY=`".
- **(b) double-quoted assignment** — `INV_LOG_LEVEL="debug"`. Not the
  single-quoted form this role writes.
- **(c) the same variable assigned twice** — a second `INV_LISTEN=...` line
  appended. Caught by a *different* half of the same assertion: zero
  unparsed lines, but the set of assigned keys is not unique.

After each, the good copy is restored (`cp`, never `git checkout --`) and a
plain run confirms `changed=0` again.

**The unreadable case is the one `failed_when: false` used to swallow.**
`chmod 000` on the file was tried first and **does not reproduce it on this
host** — Ansible connects as root here, and root can read a `000` file
regardless of its mode, so that run reported `changed=2` (the mode itself
converged back to `0600`, which is correct behaviour, not a demonstration of
"unreadable"). The variant that actually reproduces "exists and cannot be
read" is making the path a directory:

```bash
sudo incus exec invctl-ansible-test -- sh -c \
  'mv /etc/invctl/invctl.env /tmp/e && mkdir /etc/invctl/invctl.env'
```

This fails the play at the `slurp` task itself (`Is a directory`), which is
exactly the property under test: a file that exists and cannot be read stops
the play, it is never treated as empty. Restore with
`rmdir ... && mv /tmp/e ...`, not `chmod`.

### Proving the fail-closed behaviour is the fix, not an accident

`read_env.yml` was mutated back to its old, fail-open shape — `failed_when:
false` added to the `Read it` slurp task, and the
`Refuse a configuration file this role cannot have written` assertion
deleted — and scenario (a) (the truncated session key) re-run against it.

**Observed with the mutation in place:** the play **succeeded**
(`changed=2`, `failed=0`) and `INV_SESSION_KEY` on the host changed from the
truncated fragment `Ri1HPk` to a freshly minted 32-byte value
(`djglUlU8...`). That is the defect this role exists to prevent: every user
signed out by a run that reported itself routine, with nothing in the
output looking wrong. The mutation was reverted with `cp` from a saved
backup (never `git checkout --`), and a plain run afterward confirmed
`changed=0` again.

### A newline cannot inject a setting

```bash
./test/incus-harness.sh run $BASE \
  -e '{"invctl_admin_users":"admin\nINV_SECURE_COOKIES=true"}'
```
JSON `\n` becomes a real newline in the Jinja value. Fails at
`Refuse a configuration value containing a quote or a line break` in
`tasks/main.yml`, and `INV_SECURE_COOKIES` on the host is unchanged.

Proven the same way: the `[\r\n]` half of that assertion was deleted, the
same command re-run, and **observed** to succeed (`changed=2`, `failed=0`)
with a second line, `INV_SECURE_COOKIES=true'`, appended to the env file —
flipping a security setting from a value that is nominally a username list,
invisibly. Restored with `cp`; the leftover malformed line then correctly
tripped `read_env.yml`'s *own* strict-parse refusal on the very next run (a
second, independent guard catching what the first mutation let through), so
the good copy of the file was restored before a plain run confirmed
`changed=0`.

### A deleted administrator password line stays deleted

```bash
sudo incus exec invctl-ansible-test -- sed -i '/^INV_ADMIN_PASSWORD=/d' /etc/invctl/invctl.env
./test/incus-harness.sh run $BASE
```
The line is not restored (`grep -c INV_ADMIN_PASSWORD` reads `0`), and the
run after that is `changed=0`. An operator who removed it meant to; the role
neither restores it nor removes it on their behalf.

### `changed=0` is a decision, not inertness

```bash
sudo incus exec invctl-ansible-test -- rm -f /opt/invctl/invctl
./test/incus-harness.sh run $BASE -e invctl_admin_password=harness-only-not-a-real-password
```
With the binary gone, the probe reports "(none)", the role takes the INSTALL
path (`changed` is non-zero), and `-version` reports `v1.2.0` again — proving
the earlier `changed=0` runs reflected "nothing differs", not "this role
does not look".

### `--check` on a converged host

```bash
./test/incus-harness.sh run $BASE --check
```

**This found a real defect, fixed in the same commit as this file's
companion change to `tasks/main.yml`.** `ansible.builtin.command` skips
under `--check` by default (it cannot know whether a command has a side
effect), so the version-probe task (`Ask the installed binary what it is`)
never ran, `invctl_version_probe.stdout` was undefined, and the very next
task's `.split()[1]` threw — `--check` failed outright (`failed=1`) instead
of reporting what a real run would do. Fixed with `check_mode: false` on
that one task: `-version` is read-only, so forcing it to run under `--check`
is safe. After the fix, `--check` against a converged host reports
`failed=0`.

**One task genuinely cannot run under `--check`, and that is expected, not a
defect:** `Wait for /healthz to answer 200` (`ansible.builtin.uri`) is
skipped in check mode, because it would otherwise perform a real GET against
a service `--check` has not actually started or reconfigured. Its absence
from the check-mode recap is correct — an operator using `--check` to preview
an upgrade is not meant to get a live health verdict from it, only a diff of
what would change.

## Teardown

```bash
./test/incus-harness.sh down
sudo incus list --format csv -c n   # mailadmin-test still present, ours gone
```
