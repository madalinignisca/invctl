<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Keycloak (OIDC) authentication

**Status:** designed 2026-09-18, decisions taken with Gabriel. Not implemented.

**Goal:** single sign-on against Keycloak, with **MFA enforced at Keycloak**.

---

## 1. Why this is not a third `Authenticator`

The existing interface is:

```go
Authenticate(ctx context.Context, username, password string) (*domain.AppUser, error)
```

Both current authenticators take a username and a password **that invctl sees**.
The authorization-code flow does not work that way: the person authenticates at
Keycloak, invctl never receives the password, and what comes back is a code to
exchange. OIDC cannot implement this interface and must not be bent to fit it.

**The interface is unchanged by this work.** Local and LDAP keep it; OIDC is a
second shape of authentication with its own narrow surface.

### The alternative that was rejected, and why

Keycloak's **direct grant** (resource-owner password credentials) *would* fit
`Authenticate()` almost unchanged — invctl posts the username and password to
Keycloak and gets a token back. It was rejected because it defeats the stated
goal: **a password grant bypasses MFA entirely.** It is also removed in OAuth
2.1. Recording it here so the option is not rediscovered as an easy win.

## 2. Decisions

Each was taken deliberately; the reasoning is the part worth keeping.

### D1. Keycloak answers WHO, never WHAT — **decided**

Keycloak authenticates. **Authorization stays entirely in invctl:** roles are
granted on `/users`, project scope is invctl's own records, cost visibility is a
separate grant. Keycloak groups are not consulted and no claim maps to a role.

A new OIDC account arrives as an **observer with no projects**, exactly as a new
LDAP account does, until an Administrator grants it something.

**Why not group-derived roles.** They collide with a property this codebase
already asserts: a role change is a person's decision, recorded in
`change_log`. A role derived from a Keycloak group changes with no audit entry
here at all, and `/users` would have to stop offering a picker it could not
honour. Project scope and `can_see_costs` have no sensible Keycloak equivalent
regardless — a project owner's scope is a list of projects only invctl knows —
so splitting authorization across two systems would mean two places to look when
somebody asks "why can't I change this?", which `docs/ROLES.md` exists to answer
in one.

### D2. Accounts are matched on `sub`, never on username — **decided**

Keycloak's `sub` is an immutable UUID. invctl stores it and matches on it.

**The attack this closes.** The LDAP path upserts on *username*. If OIDC did the
same, anybody who can cause a Keycloak account named `admin` to exist inherits
this system's existing `admin` account and its roles. In a realm with
self-registration or an HR sync, that is an account takeover that looks like
normal operation. Matching on `sub` makes the collision **structurally
impossible** rather than dependent on trusting the realm's account creation.

It also matches how the rest of the system behaves: `change_log.actor` already
holds an opaque id precisely so that identity does not ride on a human-readable
name.

**The cost, stated rather than discovered later:** existing accounts are not the
same accounts. An LDAP user who starts signing in through Keycloak arrives as a
new observer with no roles, and somebody re-grants them. Acceptable at this
estate's size. Deliberate account linking — letting an Administrator attach a
`sub` to an existing account — is a later addition if migrating users turns out
to matter, not part of this work.

### D3. When OIDC is configured, local login is off by default — **decided**

`INV_AUTH_LOCAL` defaults to **false** once `INV_OIDC_ISSUER` is set. The login
page offers "Sign in with Keycloak" and no password form.

**Why not keep local login available.** It would leave a bypass: any account
with a local hash could skip Keycloak and its MFA entirely, and somebody would
have to audit that none do. The goal is MFA enforced at Keycloak; an
always-available password path quietly makes that untrue.

**Why not a password for the break-glass account.** It would make the
highest-value account in the system permanently reachable on the weakest
authenticator.

**Recovery is host access.** Setting `INV_AUTH_LOCAL=true` and restarting
restores the password form. This is the right bar: if you can reach the host you
can recover, and if you cannot reach the host you cannot bypass MFA. It goes in
`docs/RECOVERY.md` beside the existing break-glass procedure.

### D4. Username collision refuses the sign-in — **decided**

`app_user.username` is `UNIQUE`. A Keycloak `preferred_username` colliding with
an existing local or LDAP account is refused with a message naming the conflict
and pointing at `/users`.

**Why not auto-rename.** `alice` and `alice (keycloak)` both live, with nobody
sure which is which, is worse than a refusal. **Why not namespace every OIDC
username** (`oidc:alice`): it makes every displayed name uglier to solve a
problem most estates never hit, and it breaks the property worth keeping —
**what invctl calls you is what Keycloak calls you**, with no translation layer.

A collision means two identity systems claim one name and somebody should decide
which is which. That is a person's decision, so the software asks for one.

### D5. Sessions outlive Keycloak's opinion — **decided, and it is a trade**

invctl's session is its own. Access and refresh tokens complete the exchange and
are then **discarded** — not persisted in the session, not in the database.
invctl does not call Keycloak again on subsequent requests.

**The consequence:** disabling somebody in Keycloak does not end their invctl
session. It lasts until it expires or an Administrator deactivates them on
`/users`.

**Why accept that.** Refresh-token polling would close the gap and would make
every page load depend on Keycloak being reachable — coupling this system's
availability to the IdP's. Better to state the gap in the documentation than to
couple availability quietly. `INV_SESSION_TIMEOUT` bounds it.

## 3. Surface

### Routes

| | |
|---|---|
| `GET /auth/oidc` | generates `state`, `nonce`, PKCE verifier; stores them in the session; redirects to Keycloak |
| `GET /auth/oidc/callback` | verifies `state`, exchanges the code, verifies the ID token, resolves the account, starts the session |

Both public, both GET, neither behind CSRF middleware — `state` is the CSRF
defence for this flow, and a redirect back from an external IdP cannot carry
this application's CSRF token.

### Flow

1. Login page offers **Sign in with Keycloak**.
2. `/auth/oidc` stores `state`, `nonce`, PKCE verifier; redirects.
3. Keycloak authenticates the person. **MFA happens here, outside invctl.**
4. Callback verifies `state`, exchanges code + verifier, verifies the ID token
   signature against JWKS and checks `nonce`, `iss`, `aud`, expiry.
5. `sub` resolves to an account: created on first sign-in, matched thereafter.
6. `Sessions.RenewToken` then `Sessions.Put` — the existing path, reused
   unchanged, so session fixation is defended the same way for every
   authenticator.

### Configuration

| | |
|---|---|
| `INV_OIDC_ISSUER` | e.g. `https://keycloak.example.com/realms/corp`. Its presence enables OIDC |
| `INV_OIDC_CLIENT_ID` | the Keycloak client |
| `INV_OIDC_CLIENT_SECRET` | optional; with PKCE a public client is viable |
| `INV_OIDC_REDIRECT_URL` | must match Keycloak exactly — the most common setup failure |

**Discovery runs at startup.** A bad issuer refuses the start and names the
setting, matching how a bad LDAP configuration already behaves. A server that
starts and then cannot authenticate anybody is worse than one that will not
start.

## 4. Data model

**Migration `00072`, in BOTH dialect directories, never `shared/`.**
`Migrate()` runs all of `shared/` before any dialect file, and
`sqlite/00005_named_constraints.sql` rebuilds `app_user` — so a shared column
addition here is silently dropped on **fresh installs** while existing databases
stay green. That asymmetry cost migration `00070` a rewrite on 2026-09-17.

```sql
ALTER TABLE app_user ADD COLUMN subject TEXT;
-- source CHECK widened to ('local','ldap','oidc'), named constraint
CREATE UNIQUE INDEX app_user_subject_key
  ON app_user(subject) WHERE subject IS NOT NULL;
```

SQLite cannot alter a CHECK constraint, so widening `source` is
create-copy-drop-rename there and `DROP CONSTRAINT` / `ADD CONSTRAINT` on
PostgreSQL — which is the other reason this migration is per-dialect.

`subject` is nullable because only OIDC accounts have one; the partial unique
index makes two accounts sharing a `sub` impossible rather than unlikely.
`password_hash` stays NULL for OIDC accounts, extending the rule its own comment
already states for LDAP.

`subject` must be classified in `domain.DeclaredColumns`
(`TestEveryColumnIsClassified`), and the widened constraint must stay named
(`TestEveryEnumConstraintIsNamed`).

**`subject` never enters `change_log`.** The actor column holds the opaque
`app_user.id` as it does today; `sub` is an external identifier and recording it
would put a second identity into the audit trail.

## 5. Dependencies

Agreed 2026-09-18: **`github.com/coreos/go-oidc/v3`** (v3.21.0 at time of
writing) and **`golang.org/x/oauth2`** (v0.37.0). Neither is currently in the
tree, directly or indirectly.

`CLAUDE.md` permits this explicitly: *"prefer a small custom implementation over
a heavy third-party package, except for genuinely hard, well-solved problems
(crypto, TLS)."* Verifying a signature against a rotating remote key set is that
exception. Hand-rolling JWKS caching, key rotation and `alg`-confusion defence
is how a decade of JWT vulnerabilities happened.

**This adds the second outbound destination in the codebase.**
`TestNothingReachesOutOfThisProcess` allows exactly one today, for LDAP.
Widening it is a deliberate line in the diff with a comment giving the same
argument LDAP's carries: authentication is the one thing that genuinely has to
ask somebody else. It must not appear as a quiet allowlist entry.

## 6. Errors

Every failure below shows the user a **generic** message. The distinction lives
in the log, where it helps the operator and not an attacker — the rule the
current login handler already follows.

| Failure | Handling |
|---|---|
| `state` missing or mismatched | refuse; security event. This is CSRF on the callback, not user error |
| `state` replayed | refuse; security event. Single-use |
| `nonce` mismatch | refuse; security event. Replay |
| signature, `iss` or `aud` invalid | refuse; security event naming which |
| token expired | refuse; ordinary sign-in failure |
| Keycloak unreachable at exchange | operational error logged in full; generic message |
| username collision (D4) | refuse with the specific message naming the conflict |

## 7. Testing

### A fake issuer, not a mocked library

An `httptest` server publishing `/.well-known/openid-configuration` and a JWKS,
signing ID tokens with a key the test holds. The code under test uses `go-oidc`
unchanged and performs real discovery, real signature verification and real
claim checks against it.

Mocking `go-oidc` would test that a mock can be written. The failure modes that
matter — *is the signature actually checked, is `aud` actually compared* — exist
only below that line.

### Required tests

Each corresponds to a guard that can be deleted, and **each is mutation-tested:
delete the guard, watch the named test go red, restore.**

- ID token signed by the wrong key is refused
- `aud` naming a different client is refused
- `iss` not matching the configured issuer is refused
- expired token is refused
- `alg: none` is refused
- callback with no `state`, or a state not in session, is refused
- a valid `state` replayed is refused the second time
- `nonce` not matching the session is refused
- two sign-ins with the same `sub` resolve to ONE account
- same `sub` with a changed `preferred_username` still resolves to that account
- different `sub` with a colliding username is refused with D4's message
- a successful callback renews the session id
- no token is written to the session or the database

**A security test nobody has seen fail is a claim, not a check.** On
2026-09-17 an auth review found that deleting the per-b-end permit check in
`authorizeBreakoutSubjects` left the entire store and web suites green, because
every test used an administrator permit. That is the failure this list exists to
avoid repeating.

### Other layers

- **Boundary:** the new routes appear in the committed route inventory with the
  correct gate. They are public GETs; the assertion worth making is that they
  did not land in `write` by accident.
- **E2E:** one browser test against a real Keycloak in a container — sign in,
  land authenticated, sign out. The unit layer covers the refusals; this exists
  because wiring, redirect URLs and cookie flags are what unit tests are blind
  to, and `INV_SECURE_COOKIES` behind a proxy is already a documented footgun.

### Explicitly not tested

**That Keycloak enforces MFA.** That is Keycloak's job. Asserting it here would
test someone else's product against a container we configured — green because we
set it up, not because it is true of the deployed realm.

## 8. Out of scope

- **Group-derived roles** (D1). Authorization stays in invctl.
- **Account linking** — attaching a `sub` to an existing local or LDAP account
  (D2). Worth building if migrating existing users proves painful; not now.
- **Token relay.** invctl does not act on behalf of the user against other
  systems and holds no token after the callback (D5).
- **Back-channel logout.** RP-initiated logout at Keycloak is a plausible later
  addition; session revocation on IdP disable is the same question and is
  refused for the availability reason in D5.
- **SAML.** Different protocol, no demand stated.
