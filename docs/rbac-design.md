<!-- invctl — infrastructure inventory
     Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

     Licensed under the GNU Affero General Public License, version 3 only —
     no later version applies. See LICENSE for the full text.

     SPDX-License-Identifier: AGPL-3.0-only -->

# WP-G1 · Roles and project-scoped write — design

## 1. What this is for, and the company it is sized to

One company, one instance, its own database, its own container. **invctl is
not multi-tenant and this design does not make it one.** There is no tenant
column, no cross-customer isolation, and no SaaS story. A managed provider
running invctl for several customers runs several instances.

The estate this is sized for: **50–100 people. Five to ten infrastructure
staff who buy, assemble, configure and run everything — all Administrators.
The rest are developers, sales, management.** Most people here need to LOOK at
the inventory and must not be able to break it.

That shape is the whole design. It is not a general RBAC engine and it must
not grow into one by accident.

## 2. Three roles

| Role | Read | Write |
|---|---|---|
| **Administrator** | everything | everything |
| **Observer** | everything | nothing |
| **Project owner** | everything | entities linked to their assigned projects |

**Everyone reads everything.** That is the decision the rest of this document
depends on, and it was taken deliberately over hiding what a person is not
assigned to.

What hiding would have cost:
- Every read path needs scoping — lists, detail pages, search, exports, the
  journal, six reports, the API, topology. Roughly 500 store methods, where a
  single missed query is a disclosure rather than a bug.
- **The impact engine would give WRONG answers rather than incomplete ones.**
  "What breaks if this fails" is only correct against the whole dependency
  graph; hide half of it and the tool reports a small blast radius with
  confidence while the real one is large. Same for topology: an asset whose
  upstream is invisible looks standalone. A confidently wrong answer during an
  incident is worse than no answer.
- An asset in no project would be invisible to everyone but Administrators.
  The ownership report found fourteen such assets in the demo estate alone.

**"Project member" is not a role.** With everyone reading everything, a member
assigned to a project gains nothing an Observer does not already have — the
role collapses into Observer, and pretending otherwise would ship a
distinction with no behaviour behind it. Recording who works on a project is
an inventory fact and may be worth having; it is not a permission.

## 3. Costs are an orthogonal grant

`auth.Authorizer.CanSeeCosts` already exists, separate from `CanWrite`. It
stays separate.

- Administrator and Observer see costs implicitly.
- A project owner sees costs only if granted, per person.

The case this serves, in the client's own words: a newly hired product owner
who, for a contractual reason, should not see a project's costs for their
first months. One boolean, one existing axis, no matrix.

## 4. What a project owner may write

**Entities linked to their assigned projects**, through the join tables that
already exist: `project_asset` and `project_service` (migration `00009`).

**They MAY create an entity, provided it is linked to one of their projects in
the same transaction.** An earlier draft forbade creation outright, and that
was wrong: the first thing a product owner does is add a new server to their
own project, and refusing it sends them to an Administrator for the role's
whole purpose. The principle being protected is that no entity is created
OUTSIDE a scope — not that POs cannot create.

**They may NOT link an EXISTING entity to their project.** This is the
difference between the two, and it is a privilege-escalation boundary rather
than a preference:

> A PO assigned to `frontend` links the unowned asset `db-prod` into
> `frontend`. The scope check now answers yes, and they edit production's
> database. They granted themselves the scope.

Creating and linking in one transaction cannot do that, because the entity did
not exist to be seized. Linking something that already exists can, so
`POST /projects/{id}/assets` and its siblings stay **Administrator-only**. A
PO adds their own new server; an Administrator decides what enters a project.

**Both endpoints of a relationship must be in scope.** A dependency edge from
an in-scope service to an out-of-scope one is a write against something the PO
does not own, reachable without touching it directly. Same for containment: a
PO owning a VM does not thereby own its hypervisor, its rack or its site.

**They may not touch estate-wide configuration** — teams, vocabularies,
custom-field definitions, tags, users, projects themselves. A PO retiring a
team or renaming a vocabulary changes what every other person sees, and there
is no project by which to scope it. **Without this line, "scoped write"
silently becomes "write" for everything not project-linked, which is most of
the schema.**

**Entities in no project are Administrator territory**, and that is the
accepted consequence rather than an oversight. The ownership report (WP-G7)
already exists to find them, and letting a PO "claim" one would reintroduce
exactly the escalation above.

## 5. Roles are assigned in invctl, not derived from a directory

LDAP and AD authenticate. **invctl authorises.** A role is a column on
`app_user`, set through invctl's own screens.

This is a deliberate refusal, not an omission. Deriving roles from directory
groups means mapping every customer's group naming, nesting and conventions,
and inheriting whichever directory the customer runs — the point at which
permissions become the product. A customer who genuinely needs
directory-driven, constraint-based, enterprise RBAC is a separate commercial
conversation, not a feature request.

`INV_ADMIN_USERS` remains as the bootstrap: it names who is an Administrator
before anybody can log in to grant roles. It stops being the whole model, and
it doubles as the break-glass in §8.

**The cost of this refusal, stated rather than glossed:** roles live in two
places. Somebody who LEAVES is handled — the directory stops authenticating
them and their role becomes unreachable — but somebody who CHANGES JOB inside
the company keeps whatever invctl role they had until a person changes it.
That is a manual step, it will occasionally be missed, and pretending the
directory covers it would be worse than saying so. An estate that finds this
unacceptable wants directory-driven roles, which is the commercial
conversation above.

## 6. Where enforcement lives

`CanWrite(user)` has five call sites today, and one middleware
(`RequireAdmin`) gates all 148 write routes. **Object-level scope cannot be
decided there**: middleware runs before the handler knows which entity is
being written.

So the shape changes:
- Middleware keeps answering "may this person write AT ALL" — an Observer is
  refused before a handler runs.
- A **project owner reaching a write route needs a second check, in the
  handler, against the object**. That check must be impossible to forget.

The mechanism for "impossible to forget" is the load-bearing part of this
design. **A test that enumerates write routes is not enough**: a new route
added without a check still compiles and ships, and the test only fails if
somebody remembered to add the route to the list — which is the same act of
remembering the mechanism was supposed to remove.

**Prefer a construction where an unguarded handler does not compile.** This
codebase already does exactly that once: `internal/store/boundary_source_test.go`
fails the BUILD if `*SQLStore` satisfies the agent's narrow store interface, so
the observed-state boundary is enforced by the type system rather than by
discipline. The equivalent here is a narrow capability handed to a
project-scoped handler, such that reaching an unscoped write is a compile
error rather than a review finding.

The exact shape is the plan's job, not this document's. What this document
fixes is the requirement: **enforcement must not rest on somebody remembering.**
The auth review of WP-A4 found six admin gates that were correct and untested —
the same failure one level down, and a warning about how invisible this class
of gap is.

**`CLAUDE.md` currently says LDAP group-based roles "should only require
changing that function's body — not touching every handler". That assumption
does not survive object-level scope, and this document retires it.**

## 7. Not built, deliberately

- No tenancy. See §1.
- No per-object ACLs, no permission inheritance, no group nesting.
- No directory-group mapping. See §5.
- No read scoping. See §2.
- No "project member" role. See §2.

## 8. Losing every Administrator

Every role-management route is Administrator-only, so demoting or deactivating
the last one leaves a system nobody can administer.

- **Refuse the last one.** Demoting or deactivating the final active
  Administrator is rejected with a message saying why. Cheap, and it prevents
  the situation rather than recovering from it.
- **The break-glass already exists and stays.** `INV_ADMIN_USERS` names
  Administrators at startup (§5), so an operator with access to the deployment
  can set it and restart. That is the documented recovery path and it needs no
  new mechanism — but it must be WRITTEN DOWN, because a recovery path nobody
  knows about is not one.

## 9. Role and assignment changes are audited

A role grant decides every subsequent mutation that person can make. It is
declared state, and CLAUDE.md's rule admits no exceptions: **every mutation of
declared state writes a `change_log` row in the same transaction.**

The first draft omitted this, which is worse in a security document than in a
feature one. Granting or revoking a role, and assigning or removing a project,
each write an entry naming the actor, the subject, and what changed.
"Who gave this person write access, and when" is precisely the question an
audit trail exists to answer.

## 10. What an Observer can read, and one thing they should not

Universal read (§2) is deliberate and stays. Costs are already excluded for
those not granted them (§3) — that axis exists and predates this design.

**`identity.secret_ref` is the exception worth making.** It holds a PATH to a
credential — a vault location, an integration endpoint — and CLAUDE.md already
forbids logging it. Exposing every integration path in the estate to every
authenticated reader is a larger disclosure than the rest of the inventory,
and redacting one column for non-Administrators is surgical rather than the
five-hundred-method read-scoping project §2 rejects.

This is the boundary between "the inventory is not a secret" and "the way in
is". Everything else stays readable.

## 11. Size

**L, not M.** Both reviewers converged on this and they are right. The scope
covers: a role column and its migration; project assignment; scope checks
across roughly seventy write routes; a compile-time enforcement construction;
the last-Administrator guard; audited role changes; `secret_ref` redaction; and
a test suite for boundaries where a gap is a disclosure rather than a bug.

The estimate matters because an M-sized budget is an incentive to drop exactly
the enforcement work that makes this correct.
