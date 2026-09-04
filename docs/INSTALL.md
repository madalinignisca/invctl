<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Installing invctl

invctl is a single static binary. Templates, stylesheets and JavaScript are
compiled into it, so there is nothing to serve from disk and no Node runtime
anywhere. It needs a database and an address to listen on.

This page covers putting it on your own server. For upgrading an instance
that already exists, see `docs/UPGRADE.md`. For a throwaway instance to look
at, `make demo` is faster than any of this.

## Before you start

- **Go 1.26.6** to build from source (`go.mod` pins it), or a binary someone
  built for you. The build sets `CGO_ENABLED=0`, so the result runs on any
  Linux with a matching architecture and needs no shared libraries.
- **`git` and `make`**, if you are building. Obvious once said and absent from
  a minimal server image: a clean Debian 13 has neither, and the first command
  below is `git clone`.
- **A database.** SQLite needs nothing installed. PostgreSQL needs a server.
- **A reverse proxy that terminates TLS.** invctl speaks plain HTTP and will
  not do TLS itself. This is deliberate — your proxy already does it better,
  and certificate renewal is not this program's business.

## Which database

**Start with SQLite.** One file, no server, no credentials, no network. For
one instance serving a 50–100 person company it is the right answer, and the
answer that leaves you least to get wrong. The write-ahead log files sit
beside the database file; back up all of them together.

**Choose PostgreSQL** when you already run one, when the application and the
database need to live on different hosts, or when the inventory must survive
the loss of the machine serving it.

Every query in invctl runs unmodified on both engines and the test suite runs
against both on every change. Moving later means pointing `INV_DB_DSN` at a
new database and starting up: migrations run on an empty one automatically.

## Build

```
git clone https://github.com/madalinignisca/invctl.git
cd invctl
make build          # produces bin/invctl
```

**The build needs outbound internet, and for more than Go modules.** It also
downloads the Tailwind standalone binary — around 110 MB — to compile the
stylesheet. That is fine on a workstation and a problem on a locked-down
server, which is the environment this program expects to end up in.

So build somewhere with network access and copy `bin/invctl` to the target.
That is what "a binary someone built for you" above means in practice, and it
is the normal case rather than the exception: the result is one static file
with everything compiled in, which is the whole reason it ships that way.

## The database

### SQLite

Nothing to set up. Leave `INV_DB_DSN` unset and it uses
`file:invctl.db?_txlock=immediate` relative to the working directory, which
is why the unit below sets `WorkingDirectory`. Point it somewhere explicit if
you would rather not depend on that:

```
INV_DB_DRIVER=sqlite
INV_DB_DSN=file:/var/lib/invctl/invctl.db?_txlock=immediate
```

### PostgreSQL

```sql
CREATE DATABASE invctl;
CREATE USER invctl WITH PASSWORD 'a strong password';
ALTER DATABASE invctl OWNER TO invctl;
```

```
INV_DB_DRIVER=postgres
INV_DB_DSN=postgres://invctl:password@localhost:5432/invctl?sslmode=require
```

Migrations run at startup either way. To apply them without starting the
server — useful when you want the schema change and the restart to be
separate steps — run `invctl -migrate`, which applies and exits.

## The systemd unit

```ini
[Unit]
Description=invctl infrastructure inventory
After=network-online.target
Wants=network-online.target

[Service]
# Type=simple, NOT Type=notify. invctl does not implement sd_notify; a
# notify unit waits for a readiness message that never arrives and systemd
# eventually fails the start.
Type=simple
User=invctl
Group=invctl
WorkingDirectory=/var/lib/invctl
ExecStart=/opt/invctl/invctl
Restart=on-failure
RestartSec=5s

Environment=INV_LISTEN=127.0.0.1:8080
Environment=INV_DB_DRIVER=sqlite
Environment=INV_DB_DSN=file:/var/lib/invctl/invctl.db?_txlock=immediate

# Both of these matter behind TLS. See "Two settings behind a proxy" below.
Environment=INV_SECURE_COOKIES=true
Environment=INV_SESSION_KEY=<openssl rand -base64 32>

# First run only; see "The first administrator".
Environment=INV_ADMIN_USERNAME=admin
Environment=INV_ADMIN_PASSWORD=<a strong password>

# Break-glass. See docs/RECOVERY.md before deciding what goes here.
Environment=INV_ADMIN_USERS=admin

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/invctl

[Install]
WantedBy=multi-user.target
```

```
sudo useradd --system --no-create-home --shell /usr/sbin/nologin invctl
sudo mkdir -p /opt/invctl /var/lib/invctl
sudo install -m 0755 bin/invctl /opt/invctl/invctl
sudo chown -R invctl:invctl /var/lib/invctl
sudo systemctl daemon-reload
sudo systemctl enable --now invctl
journalctl -u invctl -f
```

Environment lines in a unit file are world-readable via `systemctl show`. If
that is not acceptable for your database password, use `EnvironmentFile=`
pointing at a file owned by root and mode `0600`.

## TLS in front

invctl listens in plaintext on `INV_LISTEN`. Bind it to loopback and put a
proxy in front. Health-check `GET /healthz`, which needs no authentication
and answers with the driver and status as JSON.

nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host              $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
}
```

HAProxy:

```
backend invctl
    option httpchk GET /healthz
    http-request set-header X-Forwarded-Proto https
    server app 127.0.0.1:8080 check
```

### Two settings behind a proxy, and they work as a pair

**`INV_SECURE_COOKIES=true`.** It defaults to `false`, which is correct for
plain HTTP on a laptop and wrong the moment there is TLS in front. It marks
the session cookie `Secure`.

**`X-Forwarded-Proto: https` from the proxy.** invctl decides whether a
request is "secure" from `r.TLS` — which is nil behind a terminating proxy —
and falls back to this header. **It only trusts the header when
`INV_SECURE_COOKIES` is true**, so that a deployment not behind a proxy
cannot be talked into believing a forged header. Set both or neither; setting
the header alone does nothing, and setting secure cookies alone gives you a
cookie the browser will send but a CSRF layer that thinks the request is
insecure.

## The first administrator

```
INV_ADMIN_USERNAME=admin
INV_ADMIN_PASSWORD=<a strong password>
```

These are read on every start but only used when the account does not exist
yet. **If `INV_ADMIN_PASSWORD` is unset, invctl generates a random password
and logs it.** That is convenient for a demo and a poor way to run a server:
the password is then in your journal, and in your journal's backups.

After signing in, read `docs/ROLES.md` and give people real roles. The
seeded account exists to bootstrap the first real one.

## Sessions

**`INV_SESSION_KEY` is generated when unset**, and invctl logs a warning
saying so. A generated key means every restart invalidates every session —
tolerable for a demo, an unexplained mass logout on a server. Generate one
once and keep it:

```
openssl rand -base64 32
```

`INV_SESSION_TIMEOUT` defaults to `12h`.

## Authentication

**At least one of `INV_AUTH_LOCAL` or `INV_AUTH_LDAP` must be enabled** —
invctl refuses to start with neither, rather than starting with no way in.

Local accounts are the default. For LDAP:

```
INV_AUTH_LDAP=true
INV_LDAP_URL=ldaps://directory.example.com:636
INV_LDAP_BIND_DN=uid=%s,ou=people,dc=example,dc=com
INV_LDAP_STARTTLS=false        # true to upgrade a plain ldap:// before binding
```

The bind DN is a template; `%s` is the username. The bind is simple — a
successful bind is the whole check. **Group membership is not consulted, and
roles are not derived from your directory** — see `docs/ROLES.md` for why,
and for what that costs you when somebody changes job.

`INV_LDAP_SKIP_VERIFY` exists for a lab and **invctl refuses to start with it
set** in any configuration it considers real, because accepting any
certificate hands every operator's password to whoever is in the middle.

## Optional surfaces, both off until you configure them

### Read-only JSON API

Nothing under `/api/v1` exists until `INV_API_TOKENS` is set — the routes
answer 404, indistinguishable from a path that was never defined.

```
INV_API_TOKENS=ansible:<token>,grafana:<token>
INV_API_SCOPES=ansible:prod|staging,grafana:prod
```

Tokens are **at least 24 characters**; a token used twice, or shared with an
agent credential, refuses to start. Endpoints are `/api/v1/assets`,
`/assets/{id}`, `/services`, `/services/{id}`, `/addresses`,
`/environments` and `/ansible`. See `docs/API.md`.

### Monitoring webhook

`POST /observations` exists only once `INV_AGENT_TOKENS` is set.

```
INV_AGENT_TOKENS=mon-prod:<token>,mon-oob:<token>
INV_AGENT_SCOPES=mon-prod:prod|transit,mon-oob:dev
INV_AGENT_VOCAB=mon-prod:prometheus
```

Same 24-character minimum. A monitoring credential is not a user account: it
can report health and nothing else, and it can never create an entity. The
rules it lives under are in `docs/AUDIT.md` rule 6.

## What not to set on a real deployment

`INV_SEED`, `INV_SEED_COMPANY` and `INV_SEED_OBSERVATIONS` load the demo
estate and its fake telemetry. An operator's first view should be an honest
empty inventory.

`INV_SEED_E2E_PROJECT_OWNER` creates a login-capable account for the browser
test suite. **It is a one-way ratchet: unsetting it later does not remove an
account it already created.** It also does nothing without
`INV_E2E_PROJECT_OWNER_PASSWORD`, which has no default precisely so that the
flag alone cannot produce a working login. See `docs/E2E.md`.

## When it will not start

invctl validates its configuration before binding and says which variable is
wrong. The ones that stop it: no authenticator enabled, `INV_LDAP_SKIP_VERIFY`
set, a token under 24 characters, a duplicate credential id, a token shared
between two credentials, and a boolean variable set to something that is not
a boolean.

## Three failures that look like something else

**Login appears to succeed and then bounces back to the login page.** The
session cookie is marked `Secure` and the browser is not on HTTPS, or the
reverse is true — check `INV_SECURE_COOKIES` against how the site is actually
served.

**Every form POST is refused.** The CSRF layer believes the request is
insecure. Behind a proxy that means `X-Forwarded-Proto` is missing, or
`INV_SECURE_COOKIES` is false so the header is not trusted. They work as a
pair.

**A script that logs in gets "Your session expired", while a browser works
fine.** You are almost certainly scraping the CSRF token out of the HTML with
`grep`. The token is base64 and can contain `+`, which `html/template`
correctly escapes to `&#43;` in the attribute. A browser turns that back into
a `+` before it submits; a shell script posts the escaped text, the token no
longer matches its cookie, and the refusal says nothing about encoding.

A working check needs three things — the cookie jar, the entity decoded, and
an `Origin` header, since the same-origin check is separate from the token:

```sh
TOK=$(curl -s -c jar http://127.0.0.1:8080/login \
      | grep -oE 'name="csrf_token" value="[^"]+"' \
      | sed -e 's/^.*value="//' -e 's/"$//' -e 's/&#43;/+/g')
curl -s -b jar -c jar -H 'Origin: http://127.0.0.1:8080' \
     -d username=admin -d password=... --data-urlencode "csrf_token=$TOK" \
     http://127.0.0.1:8080/login
```

This matters because verifying the install is the next thing you do after
starting it, and the failure looks like a session bug rather than an encoding
one — which is exactly the wrong place to go looking.

## Next

- `docs/UPGRADE.md` — upgrading an existing instance.
- `docs/ROLES.md` — the three roles, and who can do what.
- `docs/RECOVERY.md` — getting back in when no account can write.
- `docs/API.md` — the read-only API in detail.
