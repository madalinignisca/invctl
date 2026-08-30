<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Upgrading invctl

For a first installation, see `docs/INSTALL.md`.

## The short version

```
invctl -version                     # write down what you are on
systemctl stop invctl
<back up the database>              # see below; this is the rollback plan
install -m 0755 invctl /opt/invctl/invctl
systemctl start invctl
journalctl -u invctl -n 50          # migrations report here
```

Read the release note in `CHANGELOG.md` first, and specifically its
**Action required** section. It is first in that file because entries there
need a decision before you deploy, not after somebody notices.

## There is no rollback command

**Roll back by restoring the backup.** Nothing else works, and this is the
one thing to understand before upgrading.

invctl applies migrations at startup and has no command that reverses them.
The `.sql` files each carry a `-- +goose Down` section because goose requires
one, but no code path runs it: the binary's flags are `-migrate`, `-seed`,
`-seed-topup`, `-dev`, `-version` and the prune family, and none of them
steps a migration backwards.

Even if there were such a command, a down migration that drops a column
destroys whatever was written into it since the upgrade. A restored backup
loses the same window and is honest about it.

So: **the backup is not a precaution, it is the procedure.** Take it while
the service is stopped.

## Backing up

### SQLite

Stop the service first. The database is one file plus its write-ahead log,
and copying the `.db` while the process is running gives you a file whose WAL
you did not copy.

```
systemctl stop invctl
cp /var/lib/invctl/invctl.db     /var/backups/invctl.db.$(date -u +%Y%m%dT%H%M%SZ)
cp /var/lib/invctl/invctl.db-wal /var/backups/  2>/dev/null || true
cp /var/lib/invctl/invctl.db-shm /var/backups/  2>/dev/null || true
```

If you would rather not stop it, `sqlite3 invctl.db ".backup /path/out.db"`
takes a consistent copy of a live database — but you are stopping it to swap
the binary anyway, so the simple copy is usually the right one.

### PostgreSQL

```
pg_dump --format=custom --file=/var/backups/invctl-$(date -u +%Y%m%dT%H%M%SZ).dump invctl
```

Restore with `pg_restore --clean --if-exists`.

**Verify the backup exists and is non-empty before you replace the binary.**
A backup nobody has looked at is a plan, not a rollback.

## How migrations run

They run automatically at startup, before the server binds. If one fails,
invctl exits with the error rather than serving against a half-migrated
schema — so a failed upgrade is a service that will not start, not a service
that starts and behaves strangely.

To separate the schema change from the restart, apply them first:

```
invctl -migrate      # applies and exits
```

This is worth doing on a large estate, where you would rather find out that a
migration is slow while the old binary is still serving.

There are **two version tables**, because the shared and dialect-specific
migration sets are numbered independently and one table would make
`shared/00001` and `sqlite/00001` collide:

- `goose_db_version` — the shared schema
- `goose_db_version_dialect` — the SQLite- or PostgreSQL-specific objects

Both are applied on every start, shared first.

## When a migration fails

The service will not start, and the error names the migration.

1. **Do not re-run it hoping for a different result.** goose records a
   migration as applied only when it succeeds, so a failed one will be
   retried on the next start — against a database that may now be in
   whatever state the failure left.
2. **Read the error.** A constraint being added to a table that already
   violates it is the most likely cause, and the message names the
   constraint.
3. **Restore the backup**, put the previous binary back, and start it. You
   are now running the old version against the old schema, which is a
   working system.
4. Then work out what the data violated, at leisure, on a copy.

### The class of migration that does this

A migration that adds a `CHECK` or a `UNIQUE` constraint can fail on data
that was legal before. Adding a column with a default cannot; creating a new
table cannot.

This is not hypothetical here. The public demo is deliberately writable, so
visitors create rows the seeded fixture never had, and a uniqueness
constraint added later had to be checked against the live database first —
not against the fixture, which by construction contained no violation.

If a release note mentions a new constraint, query for violations **before**
upgrading. The note should tell you what to look for; if it does not, that is
a defect in the note.

## Behaviour changes are the real risk, not schema changes

A migration that fails is loud. A behaviour change that lands quietly is how
somebody loses a screen they had yesterday and opens a support conversation
you have to reconstruct.

The worked example, from this project's own history:

> **Cost visibility narrowed.** `Authorizer.CanSeeCosts` used to return
> exactly what `CanRead` did, so every authenticated user saw acquisition
> prices, contract values and project totals. It now consults
> `app_user.can_see_costs` for everyone except Administrators.
>
> Upgrading without reading that entry means a group of people silently lose
> access to cost figures, and the first you hear of it is a complaint.
>
> **Before upgrading:** list who currently needs to see costs.
> **After upgrading:** grant `can_see_costs` to each of them on `/users`.

That is the shape to look for in every release note: not "what changed in the
schema" but "who can now see or do something different". `CHANGELOG.md` puts
those under **Action required** for exactly this reason.

## After the upgrade

```
invctl -version                             # confirm what is actually running
journalctl -u invctl -n 50                  # migrations, and any warnings
curl -fsS http://127.0.0.1:8080/healthz     # {"database":"...","status":"ok"}
```

Then sign in and check one page that reads and one that writes. A migration
that succeeded and a binary that starts do not together prove that the
application works.

Watch the startup log for the two warnings that mean a setting was lost in
the move: a generated `INV_SESSION_KEY`, and anything about insecure cookies.
Both are described in `docs/INSTALL.md`.

## Keeping the previous binary

Keep the binary you are replacing:

```
cp /opt/invctl/invctl /opt/invctl/invctl.$(invctl -version | awk '{print $2}')
```

Rolling back means restoring the database backup **and** putting that binary
back. A new binary against an old schema is not a supported combination — it
will try to migrate forward again.

## Next

- `docs/INSTALL.md` — first installation, and every setting.
- `docs/ROLES.md` — who can do what, including the cost-visibility grant.
- `docs/RECOVERY.md` — getting back in when no account can write.
