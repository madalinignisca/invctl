# Directory authentication — LDAP, Active Directory and Keycloak

> Covers: `INV_AUTH_LDAP` with the `INV_LDAP_*` settings, and `INV_OIDC_ISSUER`
> with the `INV_OIDC_*` settings
> Regenerated when: the LDAP or OIDC authenticator, or its configuration,
> changes.

Sign-in against an existing directory, so people use the account they already
have and leaving the company removes their access here too.

Two ways to do that, and the choice is mostly about where the password goes.
**LDAP** takes the password typed into invctl's own form and binds with it —
invctl sees it, in memory, every time. **OIDC** sends the person to your
identity provider and never sees a password at all, which is what makes
multi-factor authentication possible: MFA is a conversation between the person
and the IdP, and a form that only collects a password has nowhere to put it.

Everything below about *who may change anything* applies to both, and is the
part people most often assume their directory decides. It does not.

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

## Keycloak and other OIDC providers

The browser goes to the identity provider, the person proves who they are by
whatever means that provider demands — password, MFA, hardware token, a
conditional-access policy you configured somewhere else entirely — and comes
back with a signed assertion. invctl verifies the assertion and starts a
session.

**invctl never learns the password, and that is the point.** It also never
learns whether MFA was used, because it does not need to: the IdP decides
whether the sign-in was good enough, and the assertion is the answer.

### Settings

| Variable | Example | Notes |
|---|---|---|
| `INV_OIDC_ISSUER` | `https://sso.example.com/realms/example` | setting it is what turns SSO on |
| `INV_OIDC_CLIENT_ID` | `invctl` | the client you created in Keycloak |
| `INV_OIDC_CLIENT_SECRET` | *(from Keycloak)* | for a confidential client |
| `INV_OIDC_REDIRECT_URL` | `https://invctl.example.com/auth/oidc/callback` | must match Keycloak's registered redirect URI **exactly** |

There is no `INV_AUTH_OIDC` toggle. An issuer nobody consumes would be a
setting that looks enabled and is not, so the issuer's presence is the switch.

### In Keycloak

A confidential client in your realm, standard flow on, with the redirect URI
set to the same string as `INV_OIDC_REDIRECT_URL`. Nothing else in invctl reads
the realm — no groups, no roles, no attribute mapping — so there is nothing
else to configure on the Keycloak side beyond the authentication policy you
want enforced, which is where MFA belongs.

**Require MFA in the realm's browser flow, not here.** invctl has no setting
that demands it and deliberately does not: a second factor enforced by the
application that just received an assertion saying the person is authenticated
is a second factor enforced in the wrong place.

**But check what your realm actually enforces, because the default is weaker
than it looks.** Keycloak's stock browser flow makes the OTP step
*conditional* on the user having configured one — so somebody who never
enrolled signs in with a password alone, and nothing about that looks like a
failure from either side. Adding an OTP step is not the same as requiring one.

To get MFA for everybody you have to make enrolment unavoidable: set the OTP
subflow in your browser flow to **Required** rather than Conditional, or make
"Configure OTP" a default required action — and note that the required action
applies to accounts created afterwards, so existing users need handling too.
Label details move between Keycloak versions, so confirm it the only way that
proves anything: **sign in as a test user who has never enrolled, and check
you are forced to enrol rather than let through.**

**invctl cannot check this for you and does not pretend to.** It verifies the
token's signature, issuer, audience, expiry and nonce — but the token does not
have to say how the person authenticated, and invctl does not require it to.
A sign-in that used only a password and one that used a hardware key arrive
here identical. What the realm enforces is what you get.

**Turn LDAP off as well, unless you mean it.** `INV_AUTH_LDAP` is a password
route with no second factor, exactly like a local account, so leaving it on
beside an issuer is the same bypass `INV_AUTH_LOCAL` would be. It defaults to
off and stays off unless you set it; if you set it to `true` alongside an
issuer the login page will show the password form, because a form that works
should be visible rather than hidden.

The rule behind that, which also matters on a deployment with no SSO at all:
**the login page shows a password form whenever any password authenticator is
enabled** — local or LDAP. A form that is hidden while `POST /login` still
answers is not a closed route, it is a closed route the operator cannot see.
The inverse used to be possible too: an LDAP-only deployment, with
`INV_AUTH_LOCAL=false` and `INV_AUTH_LDAP=true`, rendered no form and no
sign-on button at all — a working back end nobody could reach.

**Usernames in your realm decide who can be an Administrator here.**
`INV_ADMIN_USERS` is matched against the `preferred_username` claim, so
whoever can set usernames in Keycloak can hand somebody the name in that
variable — and a rename onto an existing account promotes *that* account at
its next sign-in, which is when invctl learns the new name. Keycloak's
defaults already prevent this (realm self-registration off, "Edit username"
off), which is why it is a precondition to keep rather than a hole to plug:
keep username assignment in administrators' hands, and treat the names in
`INV_ADMIN_USERS` as privileged strings in both systems.

### `INV_AUTH_LOCAL` flips to off, and it matters

Once `INV_OIDC_ISSUER` is set, `INV_AUTH_LOCAL` defaults to **`false`** —
the inverse of its default everywhere else. The login page then offers the
sign-on button alone, with no password form.

This is the whole reason to deploy SSO. A password form still answering beside
it is a route around the MFA you just required, available to anybody who knows
a username and a password and nothing else.

Setting `INV_AUTH_LOCAL=true` explicitly keeps both, and both then appear on
the same login page with nothing for the person to choose — the same way LDAP
and local already share it.

**Read `docs/RECOVERY.md` part two before deciding.** With local sign-in off,
an identity provider outage is a total lockout, and the account that gets you
back in has to exist *before* the outage: turning local sign-in on afterwards
produces a password form that no account can use, because accounts created
through SSO have no password at all.

### Accounts, renames and roles

On the first successful sign-in invctl creates an `app_user` row with
`source='oidc'` and **no password hash**, exactly as LDAP does.

**The account is matched on the provider's subject, not on the username.** The
subject is the IdP's immutable identifier for that person; the username is a
label that can change. So renaming somebody in Keycloak keeps their invctl
account, their role, their projects and their audit history attached to them —
where matching on a name would have silently created a second account and left
the first one's history orphaned.

The reverse case is refused rather than resolved: a sign-in carrying a username
that **another** account already holds is rejected. Two people with a claim to
one username is a directory problem, and quietly merging them or renaming one
would be invctl inventing an answer to a question it cannot see.

New accounts arrive as **observers with no projects** — able to read, able to
change nothing. Roles are granted afterwards, on `/users`, by an Administrator.

**Deactivating an account stops it signing in here, whatever Keycloak thinks.**
The provider has no idea you deactivated somebody and will go on vouching for
them happily; invctl refuses the sign-in at the callback and logs it as a
failure, the same way LDAP does. The person gets the ordinary refusal page
rather than a session that silently does nothing.

**A sign-in carrying no `preferred_username` is refused too.** That is not an
attack, it is a realm that lost its username mapper or a client that lost the
`profile` scope — but invctl has to write *something* as the username, and
writing an empty one over a real account makes that account unreachable. The
refusal names the claim in the log, so it reads as the configuration problem it
is rather than as a bad password.

### Sessions end here, not there

Signing out of invctl ends the invctl session. It does not end the Keycloak
session, and nothing invctl does can — single sign-*out* is a separate protocol
this does not implement.

The practical consequence: after signing out, clicking the sign-on button may
put you straight back in without being asked for anything, because Keycloak
still considers you signed in. That is the IdP's session doing its job. To end
that one, sign out of Keycloak.

invctl keeps **no token**. The authorization code is exchanged once, the
identity token is verified, and what survives is an ordinary invctl session
cookie. Nothing is refreshed and the IdP is not contacted again until the next
sign-in — which also means a person disabled in Keycloak keeps their invctl
session until it expires. Deactivate them on `/users` too if that matters.

## Keep one local account

Leave `INV_AUTH_LOCAL=true` and keep the seeded administrator.

When the directory is unreachable, LDAP sign-in fails for everybody — and the
failure is deliberately *not* silent: a directory outage stops the chain rather
than falling through to the next authenticator, because degrading quietly would
turn an outage into a confusing authorization problem. A local account is how
you get in to look at the logs.

Both authenticators run in the same login form. There is no second page and
nothing for the person signing in to choose.

**With SSO the advice is the same and the arithmetic is not.** Keeping local
sign-in on defeats the MFA you deployed SSO to enforce, so the answer is not
`INV_AUTH_LOCAL=true` and a seeded administrator — it is one deliberate
break-glass account, created in advance, with local sign-in left off until the
day it is needed. `docs/RECOVERY.md` part two is that procedure.

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
  in the change log like any other decision. A Keycloak realm role or group
  membership changes nothing here either, for the same reason.

With SSO, name the `preferred_username` claim in `INV_ADMIN_USERS` — that is
what invctl stores as the username and what the variable is compared against.
Not the email address, and not the subject.

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

For SSO, the first two steps are different and the rest are the same:

1. Restart, and watch the startup log. Discovery runs at startup, so a bad
   issuer **refuses the start and names the setting** rather than waiting to
   fail on somebody's first sign-in.
2. Sign in through the button. On success `source=oidc` appears against the new
   account, and the role column says observer — that is correct, not a
   permissions failure.
3. Confirm the password form is genuinely absent from `/login`, rather than
   present and ignored. Present means MFA has a route around it.

A failed bind is logged as a security event with the username and the reason.
An unreachable directory logs an operational error — the two are distinguished
on purpose, because "wrong password" and "the DC is down" send you to entirely
different places.
