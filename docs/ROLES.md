<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Roles and access

invctl has three roles. An Administrator sets them on the `/users` screen.
Nothing else sets them — not LDAP, not a deploy, not a job title.

## The three roles

**Administrator** — reads and writes everything, sees costs, and is the only
role that can administer accounts, run imports, or change estate-wide
configuration.

**Observer** — reads everything, writes nothing.

**Project owner** — reads everything, and writes the assets, services and
circuits linked to the projects they are assigned to.

## Everyone reads everything

There are no read restrictions. An Observer sees the whole estate — every
asset, every dependency, every project — not only what concerns them. So does
a project owner. This surprises people who expect a role to narrow what they
can see, so it is worth saying before you discover it.

The reasoning is in `docs/rbac-design.md` §2. Hiding rows by role would mean
filtering every list, detail page, report and API response, and the impact
engine would start giving wrong answers — "what breaks if this fails" is only
correct when computed against the whole graph. An asset in no project would
become invisible to everyone but Administrators; the ownership report found
fourteen such assets in the demo estate alone.

**One exception.** The path to a stored credential (`identity.secret_ref` on a
dependency) is shown only to Administrators. It is redacted in the handler
before the page is built, not hidden in the template. A path is not a secret,
but a complete map of where every secret lives is a reconnaissance gift, and
that is a different thing from inventory data.

## Cost visibility is a separate grant

Administrators see costs. Everyone else — Observer and project owner alike —
sees them only if an Administrator ticks **can see costs** on their account.

**This narrows behaviour when you upgrade.** Before this release every
authenticated reader saw acquisition prices, contract values and project
totals. After it, they do not until granted.

> **Before upgrading:** write down who currently needs to see cost figures.
> **After upgrading:** go to `/users` and grant it to each of them.
>
> Do this as part of the upgrade, not in response to somebody reporting that a
> screen they used yesterday is now empty.

Being able to change a thing does not imply being allowed to see what it cost.
Those are different questions, and a project owner running their own estate
does not thereby learn what a shared cluster costs.

The grant is deliberately one control for both roles. If Observers saw costs
implicitly, demoting a project owner to Observer would *widen* their cost
visibility from their own projects to the entire estate — a narrower role must
never show a person more.

## A job change does not update a role

If your company uses LDAP, invctl authenticates against it, so a person who
leaves and is disabled in the directory can no longer sign in. But **roles are
not derived from directory groups.** A role is a column on the account, set in
invctl.

*Why:* mapping every customer's group naming and nesting means inheriting
their directory as a dependency, and the product turns into "enforce your org
chart" rather than "the inventory's access model".

*What it costs:* somebody who changes job **inside** the company keeps the
invctl role they had. Nothing notices and nothing reconciles it. An
Administrator has to change it, and that step will occasionally be missed.

This is an accepted trade-off rather than an oversight. Build a habit around
it: when somebody changes role internally, check `/users`.

## Making somebody a project owner

Two steps, and **both are required**:

1. On `/users`, set their role to **Project owner**.
2. In the **Projects** column on their row, assign them one or more projects.

A project owner with no assignments can write **nothing at all**. That is by
design — scope comes from assignment, and no assignment means no scope — and
it will be your first support question after going live.

Assigning or releasing a project takes effect on that person's next request.
There is no cache, so removing somebody from a project removes their access to
it immediately rather than at the end of their session.

## What a project owner can and cannot do

**Can:**

- Create an asset, service or circuit **from inside one of their project
  pages**. The entity is created and linked to the project in one step.
- Edit and retire the assets, services and circuits linked to their projects.
- Write journal notes on anything they can write.

**Cannot:**

- Use the generic "new asset" form. Creation happens inside a project, because
  an entity created outside one would belong to nobody.
- Link an **existing** entity into their project. That stays with
  Administrators deliberately: otherwise anyone could pull any asset in the
  estate into a project they hold and inherit write access to it.
- Manage IP addresses, interfaces, dependencies, cost lines, service
  placements, certificates, network links or clusters — even on their own
  assets.
- Administer accounts, run imports, or change anything estate-wide: teams,
  tags, vocabularies, custom field definitions, projects themselves.

The third item is a **known limitation, not a decision**. The mechanism to
extend scope to those types exists and journal notes already use it. Do not
plan around the limitation as though it were permanent, and do not assume it
is already fixed.

## "Why can't I change this?"

Two different refusals, and the wording tells you which:

| What you see | What it means |
|---|---|
| **You have read-only access.** | You are an Observer. You have no write access anywhere. |
| **You are not allowed to do that.** | You are a project owner, and this particular thing is outside your projects — or is an estate-wide setting no project owner may change. |
| **This requires an Administrator.** | The screen is Administrator-only, such as user administration or imports. |

If you are a project owner seeing the second message on something you believe
is yours, check with an Administrator that the entity is actually linked to one
of your projects. Being able to see it is not the same as being assigned it —
everyone sees everything.

## Break-glass

`INV_ADMIN_USERS` is a comma-separated list of usernames in the server's
environment. Anyone named there has full write access **regardless of what
their role column says**, because the list is consulted live on every check and
takes precedence. It is how you get back in when no account can administer
invctl any more.

`docs/RECOVERY.md` has the procedure. Two things not to miss:

- The roster shows when this is in force, rather than hiding it: such an
  account displays **Administrator (from INV_ADMIN_USERS)** next to a role
  picker that says something else. If you see that, you are looking at an
  instance in break-glass mode.
- **Set the role properly as well as naming the account.** Because the
  environment variable wins, an account can have full access while its stored
  role still reads Observer — and then removing the variable later leaves the
  estate with nobody able to write. This is not hypothetical; it was the state
  of the public demo until it was noticed and corrected.

## What is recorded

Every role grant, cost-visibility grant, project assignment and release is
written to the change log in the same transaction as the change itself. `/changes`
shows who did what, and when.

The log identifies people by an internal account id, never by name or email —
the display name you see on `/changes` is resolved by looking the account up as
the page renders. That is what lets the log be kept indefinitely: scrubbing an
account erases the personal data while every audit entry stays intact and
simply stops resolving to a name.
