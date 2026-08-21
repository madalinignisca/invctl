# Installing and running invctl

> Covers: installation, configuration, the first run, upgrades and backups
> Regenerated when: the configuration surface, the command-line flags, the
> migration runner or the startup sequence changes.

For whoever installs and keeps this running. The rest of the manual is for
people using it; this part is for the person they call when it will not start.

invctl is **one static binary** with everything embedded — templates, CSS,
JavaScript and migrations. There is no asset pipeline to deploy, no runtime to
install and nothing to fetch at startup, which is deliberate: it is expected to
run in segmented networks with no outbound access.

## Install

Download the release, verify it, put it somewhere sensible.

```bash
VERSION=0.1.0
BASE=https://github.com/madalinignisca/invctl/releases/download/v${VERSION}

curl -fLO ${BASE}/invctl_${VERSION}_linux_amd64
curl -fLO ${BASE}/invctl_${VERSION}_checksums.txt

sha256sum -c invctl_${VERSION}_checksums.txt

sudo install -m 0755 invctl_${VERSION}_linux_amd64 /usr/local/bin/invctl
invctl -version
```

`sha256sum -c` is not a formality. The binary is served over a link somebody
will eventually paste into a chat window, and the checksum file is the only
thing that says the bytes you have are the bytes that were built.

Linux amd64 is the only published build. It is pure Go with `CGO_ENABLED=0`, so
it compiles for other platforms unchanged — but nothing else is tested or
published, and this guide does not pretend otherwise.

## Choose the database before you load anything

Both engines are first class. Every query in the codebase runs unmodified on
either, and the test suite runs twice — a change that only passes on one is not
finished.

| | **SQLite** | **PostgreSQL** |
|---|---|---|
| Good for | a single instance, one team, tens of thousands of assets | more than one instance, or an existing Postgres you already back up |
| Setup | nothing — a file appears | a database, a role, a connection string |
| Backups | `sqlite3 .backup` | whatever you already do |
| Concurrency | one writer at a time, handled internally | many |

**There is no supported migration between them.** Exporting from one and
importing into the other is not a feature and is not tested. Pick now.

If you are unsure, pick SQLite. A single-instance CMDB on SQLite is a boring,
fast, easily backed-up thing, and the option to have been on Postgres is worth
less than the operational simplicity — until you need two instances, at which
point you were always going to need Postgres.

## Configure

Everything is environment variables. Nothing is read from a config file, so
whatever manages your services is the only place settings live.

### The ones you must set

| Variable | What it does |
|---|---|
| `INV_DB_DRIVER` | `sqlite` or `postgres` |
| `INV_DB_DSN` | `file:/var/lib/invctl/invctl.db?_txlock=immediate`, or a `postgres://` URL |
| `INV_LISTEN` | address to bind; `127.0.0.1:8080` behind a proxy |
| `INV_SESSION_KEY` | base64, at least 32 bytes. See below — this one bites. |
| `INV_ADMIN_USERS` | comma-separated usernames allowed to **write** |

### The ones you probably want

| Variable | Default | Notes |
|---|---|---|
| `INV_SECURE_COOKIES` | `false` | **`true` in production.** A secure cookie is never sent over plain HTTP, so turning this on without TLS means sign-in silently fails to stick |
| `INV_SESSION_TIMEOUT` | `12h` | idle timeout |
| `INV_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `INV_CURRENCY` | `EUR` | display only; it does no conversion |
| `INV_AUTH_LOCAL` | `true` | local accounts with argon2id hashes |
| `INV_AUTH_LDAP` | `false` | see [Directory authentication](12-directory.md) |
| `INV_ADMIN_USERNAME` | `admin` | the seeded first account |
| `INV_ADMIN_PASSWORD` | — | its password; a random one is generated and logged once if unset |
| `INV_AUDIT_FOLD_KEY` | — | generated and persisted in the database if unset. See below — setting it is the GDPR-correct deployment, not just hardening |

At least one of `INV_AUTH_LOCAL` and `INV_AUTH_LDAP` must be on. The server
refuses to start with both off, rather than starting and accepting nobody.

### `INV_SESSION_KEY` is the one people miss

Leave it unset and a random key is generated at startup. The server warns and
runs — but every session dies on every restart, **including the restart that
installs your next upgrade**, so an upgrade logs out everybody mid-shift and
looks like a fault.

```bash
openssl rand -base64 48
```

Set it once, keep it with your other secrets, and never rotate it casually:
rotating it signs everyone out.

### `INV_AUDIT_FOLD_KEY` — set it, but never rotate it by accident

A custom field's value is never written to `change_log` as text. Instead a
keyed HMAC-SHA256 digest of it is folded into the audited entry, so a change
still shows up as a diff without the value itself reaching the log. Left
unset, invctl generates the key once and persists it in the database.

That default is not wrong, but it is worth understanding precisely: a keyed
digest whose key sits in the **same database** as the digests it protects is
*pseudonymisation*, not anonymisation (GDPR Art. 4(5), Recital 26) — the
"additional information" that could re-identify a value is not "kept
separately". Setting `INV_AUDIT_FOLD_KEY` so the key lives **outside** the
database — with your other secrets, the same as `INV_SESSION_KEY` — is what
makes it the GDPR-correct deployment, not merely an extra precaution.

Unlike the session key, **this one must never change under a running
deployment's data.** A fresh key changes every digest ever folded, and every
entity that holds a custom value gets a spurious diff on its very next save —
forever, because `change_log` is append-only and no entry is ever rewritten.
So do **not** generate a fresh key with `openssl rand -base64 48` and set it
the way you would for `INV_SESSION_KEY` above. If you want to move the key
out of the database into `INV_AUDIT_FOLD_KEY`, read the **existing** one out
first:

```sql
SELECT key_b64 FROM audit_fold_key;
```

and set `INV_AUDIT_FOLD_KEY` to that exact value. Setting it to anything else
is a deliberate key rotation — invctl detects the mismatch against what is
already persisted and logs a prominent warning at startup (never the key
itself), but it does not refuse to start, since a genuine rotation is a
legitimate operator decision. Unsetting the variable again falls back to the
key already in the database, unchanged.

## First run

```bash
sudo useradd --system --home /var/lib/invctl --shell /usr/sbin/nologin invctl
sudo mkdir -p /var/lib/invctl && sudo chown invctl:invctl /var/lib/invctl
```

Migrations run automatically at startup. There is no separate install step and
no "did you remember to migrate" failure mode. To apply them without serving —
useful when you want the schema change and the restart to be separate events —
run `invctl -migrate`.

A systemd unit, minus the environment:

```ini
[Unit]
Description=invctl — infrastructure inventory
After=network-online.target
Wants=network-online.target

[Service]
User=invctl
Group=invctl
ExecStart=/usr/local/bin/invctl
WorkingDirectory=/var/lib/invctl
EnvironmentFile=/etc/invctl/invctl.env
Restart=on-failure
RestartSec=5s

# It writes to its own directory and nowhere else, needs no privileged port
# and never executes anything. If any of these break it, that is worth
# investigating rather than removing.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/invctl
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
```

Keep `/etc/invctl/invctl.env` at mode `0640`, owned `root:invctl`. It holds the
session key and the database password.

On first start with no accounts, an administrator is created and its password
is logged **once**. If you did not set `INV_ADMIN_PASSWORD`, capture it from the
journal before it rotates away.

## Put TLS in front of it

invctl speaks plain HTTP and does not terminate TLS. Run it behind whatever you
already use — nginx, HAProxy, Caddy, a Kubernetes ingress — and set
`INV_SECURE_COOKIES=true` once TLS is actually in front.

The proxy must pass the `Host` header through and preserve `Origin` or
`Referer` on non-GET requests: CSRF protection rejects an unsafe request that
carries neither, and stripping them produces a login form that answers 400 for
what looks like a correct password.

## Upgrading

The expected sequence, and what each step is for:

```bash
# 1. Back up. Always, even for a patch release.
sudo -u invctl sqlite3 /var/lib/invctl/invctl.db \
  ".backup '/var/lib/invctl/backups/invctl-$(date -u +%Y%m%dT%H%M%SZ).db'"

# 2. Read what you are about to install.
#    CHANGELOG.md, "Action required" first.

# 3. Replace the binary.
sudo install -m 0755 invctl_0.2.0_linux_amd64 /usr/local/bin/invctl

# 4. Restart. Migrations run here.
sudo systemctl restart invctl

# 5. Confirm what is actually running.
invctl -version
journalctl -u invctl -n 20
```

**Use `sqlite3 .backup`, not `cp`.** The database runs in WAL mode, so a plain
copy of a live file is a torn read that can be missing recent writes. That is
not hypothetical — it lost rows on this project's own demo.

What to expect:

- **Migrations are automatic and forward-only.** They run at startup, in order,
  once. Rolling back a release means restoring the backup, not running the
  binary in reverse.
- **A failed migration stops startup.** The server does not serve a
  half-migrated schema. Read the error, restore, and raise it.
- **Downtime is a restart**, a few seconds, unless a migration has to rewrite a
  large table — the changelog says so when that is true.
- **Sessions survive** if `INV_SESSION_KEY` is set, and do not if it is not.
- **Read `Action required` before every upgrade.** While the version is `0.x`, a
  minor bump may change behaviour. That is what `0.x` means.

Skipping versions is fine: migrations are cumulative and run in order.

## Backups

The database is the whole of the state. There is nothing else on disk to keep —
no uploads, no generated files, no cache that matters.

```bash
# SQLite — consistent against a running server
sqlite3 /var/lib/invctl/invctl.db ".backup '/backup/invctl-$(date -u +%F).db'"

# PostgreSQL
pg_dump --format=custom invctl > /backup/invctl-$(date -u +%F).dump
```

Verify one occasionally by restoring it somewhere else and starting a binary
against it. A backup nobody has restored is a hypothesis.

Restoring is: stop the service, put the file back, start the service. If the
backup came from an older release, the newer binary will migrate it forward on
startup.

## When it will not start

The startup log says why. It is structured and the first few lines carry it.

| Symptom | Cause |
|---|---|
| `INV_DB_DRIVER must be sqlite or postgres` | typo, or an empty variable that the unit file did not pass through |
| `at least one of INV_AUTH_LOCAL or INV_AUTH_LDAP must be enabled` | both were turned off |
| `INV_LDAP_BIND_DN must contain %s for the username` | the template has no substitution point |
| Sign-in appears to succeed, then returns to the login page | `INV_SECURE_COOKIES=true` without TLS in front |
| Sign-in returns 400 | the proxy stripped `Origin` and `Referer` |
| Everyone is signed out after a restart | `INV_SESSION_KEY` is unset |
| Signs in, but nothing can be edited | the username is not in `INV_ADMIN_USERS` |

For anything else, `INV_LOG_LEVEL=debug` and the journal. Include the output of
`invctl -version` in a bug report — it names the exact build.
