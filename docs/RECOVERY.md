<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Recovering from "nobody can write"

`docs/rbac-design.md` §8: *"a recovery path nobody knows about is not one."*
This page is that path, written down.

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
configured (local or LDAP) to sign in as an existing active user first — any
active account, even an Observer — then name that username.

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
