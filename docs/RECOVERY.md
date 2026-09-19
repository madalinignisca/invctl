<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Recovery — nobody can write, and nobody can sign in

`docs/rbac-design.md` §8: *"a recovery path nobody knows about is not one."*
This page is that path, written down.

Two different lockouts live here. The first is about **authorization** —
everybody gets in and nobody may change anything. The second is about
**authentication** — nobody gets in at all, which is what single sign-on makes
possible for the first time. They have different fixes and only one of them can
be arranged after the fact, so read the second one *before* you need it.

---

# Part one — nobody can write

## Symptom

Every account that reaches a write route gets refused — every mutating page
either shows read-only controls or every non-GET request is rejected. Nobody
who signs in has `role = 'administrator'` in `app_user` any more, whether
because the last Administrator was demoted, deactivated, left the company and
was never handed off, or a role change was made in error.

This is not a bug: `RoleObserver` and `RoleProjectOwner` are both meant to be
unable to write anything estate-wide (see `internal/auth/auth.go`'s
`CanWrite`), and the last-Administrator guard (§8, `CountActiveAdministrators`)
exists specifically to make this situation require a deliberate act to reach,
not to make it impossible. If it happens anyway — a manual database edit, a
restore from an older backup, anything outside invctl's own screens — this is
how you get back in.

## Fix: `INV_ADMIN_USERS`

Set the `INV_ADMIN_USERS` environment variable to a comma-separated list of
usernames, and restart the invctl process.

```
INV_ADMIN_USERS=someone.who.can.sign.in
```

This is a **deliberate override, not a seed**. A user named here has full
write access regardless of what their `app_user.role` column says — that is
the whole point: you are setting this variable *because* the role column is
wrong, so it has to win over it, not merely nudge it. See
`internal/auth/auth.go`'s `isAdministrator` and
`docs/rbac-design.md` §5.

One condition still applies: **the named account must be active.**
Deactivation is not defeated by this variable — an account with
`is_active = FALSE` cannot write even when named here. This matters because
an ex-employee's username can sit in `INV_ADMIN_USERS` in a config file long
after they left; if it be enough by itself to restore write access, removing
someone's `app_user` row on their last day would count for nothing. Name an
account that is both listed here and currently active.

If you don't have any active account to name, use whichever authenticator is
configured (local, LDAP or OIDC) to sign in as an existing active user first —
any active account, even an Observer — then name that username. For OIDC the
username to name is the `preferred_username` claim, which is what invctl stores
and what `INV_ADMIN_USERS` is compared against — not the email address, and not
the subject.

## Verifying recovery worked

1. Restart the invctl process after setting the variable — it is read once at
   startup, not polled.
2. Sign in as the named account.
3. Confirm a write actually succeeds: edit any field on an asset and save it.
   Seeing a write control on the page is not enough proof; a stale form can
   render before authorization is checked. A successful save is.

## Handing the role back so the variable can be removed

`INV_ADMIN_USERS` is a break-glass, not a permanent admin list — leaving
usernames in it after recovery means the role column stops being the source
of truth silently, which is the exact confusion this page exists to prevent.

Once you can write again:

1. Go to the user administration screen and set `role = administrator` on the
   account(s) that should hold it permanently, through invctl's own UI (this
   is audited — `docs/rbac-design.md` §9 — and answers "who has write access
   and since when" later, which a bare environment variable cannot).
2. Confirm with `CountActiveAdministrators` (surfaced on the same screen) that
   at least one active Administrator now exists by role alone.
3. Remove the names from `INV_ADMIN_USERS` and restart again.
4. Confirm write access still works with the variable unset — this proves the
   role column, not the override, is now doing the work.

---

# Part two — nobody can sign in

## Symptom

The login page offers **only** the single sign-on button, and the identity
provider does not answer: Keycloak is down, its certificate expired, the realm
was deleted, or the network between here and there is broken. There is no
password form, because `INV_AUTH_LOCAL` defaults to `false` once
`INV_OIDC_ISSUER` is set — deliberately, so that the provider's MFA is the only
way in rather than one of two ways in.

Nobody can sign in, so nobody can grant anything, so Part one's fix is
unreachable: `INV_ADMIN_USERS` names an account you have no way of
authenticating as.

## The fix has to be arranged in advance

**Create a break-glass local account now, while sign-in still works.**

An Administrator can create a local account with a password on `/users` at any
time, including on a deployment where `INV_AUTH_LOCAL` is `false` — the account
is simply unusable until local sign-in is switched on. That asymmetry is what
makes this work: the account is created during business as usual, and only the
environment variable changes during the incident.

1. While the IdP is up, sign in as an Administrator and create a local account
   on `/users` with a long unique password. Give it a name that says what it is
   (`breakglass`, not somebody's name — it belongs to the deployment, not to a
   person).
2. Grant it the Administrator role on the same screen, or name it in
   `INV_ADMIN_USERS`. The second is better here: `INV_ADMIN_USERS` is read from
   configuration at startup, so it still works if the database is what went
   wrong.
3. Put the password wherever your organisation keeps break-glass credentials —
   somewhere that does **not** require signing in to invctl, and does not
   require the same IdP.

Then, on the morning it is needed:

```
INV_AUTH_LOCAL=true
```

Restart. The password form reappears beside the sign-on button, the
break-glass account works, and everything else — roles, projects, the change
log — is exactly as it was.

## Why turning local sign-in on will not save you by itself

`INV_AUTH_LOCAL=true` on its own is **not** a recovery path, and this is the
trap worth understanding before you rely on it.

invctl seeds its first administrator only when the user table is completely
empty (`ensureAdmin` in `cmd/invctl/main.go`). On a deployment that has been
running on single sign-on, it is not empty — every person who has ever signed
in through Keycloak has an `app_user` row. So switching local sign-in on gives
you a password form and **no account that has a password**: OIDC accounts are
stored with no hash at all, and there is no password-reset command.

The result is a login page that looks like a way in and is not. An account with
a password has to exist before the outage, which is why this section is about
preparation rather than about a command to run.

## Fresh installs: there is no administrator until you name one

On a **new** OIDC-only deployment, `ensureAdmin` does not run either — it
returns immediately when local sign-in is off — so no seeded administrator is
ever created. The first person to sign in through Keycloak becomes an
**observer with no projects**, and so does the second.

Set `INV_ADMIN_USERS` to a Keycloak `preferred_username` as part of the first
deployment. Without it a fresh OIDC-only install has nobody who can grant a
role to anybody, including themselves, and the estate is readable and
unwritable.

**This is recoverable, and by exactly the route part one describes**: set the
variable to the username of somebody who has already signed in, restart, and
they are an Administrator. It is worth doing at first deployment only because
discovering it later means discovering it at the moment you needed to write
something.
