# Directory authentication — LDAP and Active Directory

> Covers: `INV_AUTH_LDAP` and the `INV_LDAP_*` settings
> Regenerated when: the LDAP authenticator or its configuration changes.

Sign-in against an existing directory, so people use the account they already
have and leaving the company removes their access here too.

## What it does, exactly

**Simple bind, and nothing else.** invctl takes the username typed into the
login form, substitutes it into a DN template, and binds to the directory with
that DN and the typed password. If the bind succeeds, the person is who they
say they are.

On the first successful sign-in it creates a local `app_user` row with
`source='ldap'` **and no password hash**. That row exists to hang sessions and
audit entries off — the audit trail refers to an opaque user id rather than a
name, which is what lets it be kept indefinitely without holding personal data.
The credential itself stays in the directory and never reaches this database.

Be clear about what this is not, because the shape of it decides your DN
template:

- **No service account.** invctl never binds as anybody but the person signing
  in, so there is no bind password to store, rotate or leak.
- **No search.** It does not look the user up to discover their DN — it
  *constructs* the DN. If your users are spread across several OUs, a single
  template cannot reach all of them.
- **No groups.** Group membership is not read, so it cannot grant anything.
  Write access comes from `INV_ADMIN_USERS`, a list of usernames in this
  application's own configuration.
- **No provisioning.** Accounts appear on first successful sign-in, not in
  advance.

## Settings

| Variable | Example | Notes |
|---|---|---|
| `INV_AUTH_LDAP` | `true` | turns it on |
| `INV_LDAP_URL` | `ldaps://dc01.example.com:636` | `ldap://` or `ldaps://` |
| `INV_LDAP_BIND_DN` | `uid=%s,ou=people,dc=example,dc=com` | **must contain `%s`** |
| `INV_LDAP_STARTTLS` | `false` | upgrade a plain `ldap://` connection before binding |
| `INV_LDAP_SKIP_VERIFY` | `false` | see the warning below |
| `INV_AUTH_LOCAL` | `true` | keep at least one local account; see below |

The server refuses to start if `INV_LDAP_BIND_DN` has no `%s` — a template
without a substitution point would bind every user as the same DN.

## The password crosses the network, so encrypt it

A simple bind sends the password. Use `ldaps://`, or `ldap://` with
`INV_LDAP_STARTTLS=true`.

**The server will not start with an unencrypted LDAP configuration.** A plain
`ldap://` URL with StartTLS off is refused rather than warned about, because a
warning in a startup log is not read by the person whose password is on the
wire.

`INV_LDAP_SKIP_VERIFY=true` **is also refused.** It disables certificate
verification, which means any host that can answer on that address can collect
your users' passwords by accepting every bind — the exact attack TLS is there to
prevent. If your directory uses an internal CA, install that CA in the system
trust store where it belongs:

```bash
sudo cp internal-ca.crt /usr/local/share/ca-certificates/
sudo update-ca-certificates
```

## OpenLDAP

Users under one OU, `uid` as the login name:

```bash
INV_AUTH_LDAP=true
INV_LDAP_URL=ldaps://ldap.example.com:636
INV_LDAP_BIND_DN='uid=%s,ou=people,dc=example,dc=com'
INV_ADMIN_USERS=agrindheim,jlarsen
```

Check the template against the directory before deploying it — this is the same
bind invctl will do:

```bash
ldapwhoami -H ldaps://ldap.example.com:636 \
  -D 'uid=agrindheim,ou=people,dc=example,dc=com' -W
```

If that succeeds and invctl does not, the problem is TLS trust or the network,
not the template.

## Active Directory

AD accepts several DN forms for a simple bind, and **userPrincipalName is by far
the easiest**, because it needs no OU in the template — which is what makes a
single template work across an organisational tree.

```bash
INV_AUTH_LDAP=true
INV_LDAP_URL=ldaps://dc01.example.com:636
INV_LDAP_BIND_DN='%s@example.com'
INV_ADMIN_USERS=agrindheim,jlarsen
```

People then sign in with `agrindheim` and bind as
`agrindheim@example.com`.

The alternatives, and why they are usually worse:

| Form | Template | Problem |
|---|---|---|
| userPrincipalName | `%s@example.com` | none — recommended |
| `DOMAIN\user` | `EXAMPLE\%s` | works; needs the NetBIOS name, which is not always what people know |
| Full DN | `CN=%s,OU=Staff,DC=example,DC=com` | breaks whenever somebody moves OU, and `CN` is usually a display name rather than a login |

Point `INV_LDAP_URL` at a domain controller that answers LDAPS on 636 — or at a
load-balanced name if you have one, since a single DC in the variable is a
single point of failure for sign-in. Port 3268 (global catalog) also works and
is worth using in a multi-domain forest.

Verify from the invctl host, not from your laptop:

```bash
ldapwhoami -H ldaps://dc01.example.com:636 -D 'agrindheim@example.com' -W
```

## Usernames it will accept

Letters, digits, and `. - _ @`. Anything else is rejected before a connection is
opened.

This is not fussiness. The DN is assembled by substitution, so a username
containing DN metacharacters could otherwise change what the DN *means* — the
LDAP equivalent of SQL injection. A user whose login name contains a space or a
comma cannot sign in, and that is the intended trade.

An empty password is also rejected before the connection. Most directories treat
a bind with an empty password as an **anonymous** bind, which succeeds — so
without that check, a blank password would authenticate anybody.

## Keep one local account

Leave `INV_AUTH_LOCAL=true` and keep the seeded administrator.

When the directory is unreachable, LDAP sign-in fails for everybody — and the
failure is deliberately *not* silent: a directory outage stops the chain rather
than falling through to the next authenticator, because degrading quietly would
turn an outage into a confusing authorization problem. A local account is how
you get in to look at the logs.

Both authenticators run in the same login form. There is no second page and
nothing for the person signing in to choose.

## Who can change anything

**Signing in and being allowed to write are two different questions, and your
directory only answers the first.** LDAP decides who you are. invctl decides
what you may do, from its own records, and never consults a directory group.

```bash
INV_ADMIN_USERS=agrindheim,jlarsen
```

`INV_ADMIN_USERS` names **Administrators** — estate-wide write, and the only
people who can grant roles to anybody else. It is read at startup, so adding a
name means editing configuration and restarting.

It is no longer the whole model. Every account also carries a **role**, set on
`/users`:

| | |
|---|---|
| **Administrator** | writes anything, sees every cost, manages users |
| **Project owner** | writes the assets, services and circuits belonging to projects they own — and nothing outside them |
| **Observer** | reads everything, writes nothing |

**A project owner can write.** If you read one sentence here, read that one:
granting the role is not a way of giving somebody a slightly better read-only
account. Scope comes from project membership, so an owner with no projects can
change nothing at all, and an owner of one project cannot touch the rest of the
estate.

Seeing money is a **separate grant** again (`can_see_costs` on `/users`), not
implied by any role except Administrator. A project owner who may change a
cost line must also be allowed to read one — otherwise they could write a price
they cannot see, which is its own leak.

`docs/ROLES.md` is the full account, including what a project owner is
deliberately refused and why. Two consequences of the directory boundary are
worth planning around regardless:

- **A leaver loses sign-in immediately**, which is the half that matters,
  because authentication is the directory's job. Their invctl account and its
  role linger until somebody tidies them.
- **Adding somebody to an AD group changes nothing here.** Group-derived roles
  do not exist; a role is granted in invctl, by an Administrator, and recorded
  in the change log like any other decision.

## Checking it works

1. Restart, and watch the startup log — a bad LDAP configuration refuses to
   start and says which setting.
2. Sign in as a directory user. On success `source=ldap` appears against the
   new account.
3. Confirm read-only is real: a **new** directory user — not in
   `INV_ADMIN_USERS`, and not yet granted a role — should see the rail's footer
   say `read only`, and every edit control should be absent rather than
   present-and-failing. Absent from `INV_ADMIN_USERS` is no longer the same
   statement as read-only, so check the footer rather than inferring it from
   the variable.
4. Sign in as a local account too, so you know that route still works before you
   need it.

A failed bind is logged as a security event with the username and the reason.
An unreachable directory logs an operational error — the two are distinguished
on purpose, because "wrong password" and "the DC is down" send you to entirely
different places.
