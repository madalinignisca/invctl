# Keycloak (OIDC) authentication — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sign in to invctl through Keycloak, with MFA enforced at Keycloak.

**Architecture:** OIDC does **not** implement the existing `Authenticator` interface — that interface takes a password invctl sees, and the authorization-code flow never gives it one. OIDC is a second shape: two public GET routes, an `oidc` package that owns discovery and token verification, and an account resolved by the immutable `sub`. Everything downstream of the session is unchanged.

**Tech Stack:** Go 1.26, `github.com/coreos/go-oidc/v3` v3.21.0, `golang.org/x/oauth2` v0.37.0, `alexedwards/scs/v2` (existing), goose migrations, `net/http.ServeMux`.

**Spec:** `docs/superpowers/specs/2026-09-18-keycloak-oidc-design.md` — read it before Task 1. Every decision below argues from it.

## Global Constraints

Copied from `CLAUDE.md` and the spec. Every task's requirements include these.

- Placeholders are `?`; call `sqlx.Rebind`. Never write `$1`.
- Every query runs unmodified on SQLite **and** PostgreSQL. `make test` is the gate, not `go test ./...` — the latter silently skips Postgres.
- IDs are UUIDv7 `TEXT` generated in Go. Timestamps are RFC3339 UTC `TEXT` generated in Go. **Never** in SQL.
- Enums are `TEXT` + a **named** `CHECK` constraint (`TestEveryEnumConstraintIsNamed`) plus a matching Go constant set.
- **A portable `ALTER TABLE` goes in BOTH dialect directories, never `migrations/shared/`.** `Migrate()` runs all of `shared/` before any dialect file, and `sqlite/00005_named_constraints.sql` rebuilds `app_user` — verified 2026-09-18. A shared column addition is silently dropped on **fresh installs** while existing databases stay green. This cost migration `00070` a rewrite.
- Never log credentials, bind passwords, session tokens, or token material of any kind.
- Licence header + a blank line before the `package` clause on every new file (`internal/license` fails otherwise).
- `gofmt`, `go vet`, `staticcheck` clean. A doc comment on an exported function starts with its name (ST1020).
- `go` is not on `PATH` by default in this environment: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin` first.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/store/migrations/sqlite/00072_app_user_oidc.sql` | `subject` column, widened `source` CHECK, partial unique index — SQLite spelling |
| `internal/store/migrations/postgres/00072_app_user_oidc.sql` | the same, PostgreSQL spelling |
| `internal/auth/oidc.go` | discovery, the authorization URL, callback verification. Owns everything that talks to Keycloak |
| `internal/auth/oidc_test.go` | the fake issuer and every refusal test |
| `internal/store/users.go` *(modify)* | `UpsertOIDCUser` |
| `internal/config/config.go` *(modify)* | `OIDC` config block, `AuthOIDC`, validation, the `AuthLocal` default flip |
| `internal/web/handlers/auth.go` *(modify)* | `OIDCStart`, `OIDCCallback` |
| `internal/web/routes.go` *(modify)* | register both routes |
| `internal/web/route_registration_test.go` *(modify)* | allowlist both, with reasons |
| `web/templates/pages/login.html` *(modify)* | the Keycloak button; hide the password form when local auth is off |
| `docs/INSTALL.md`, `docs/RECOVERY.md`, `CHANGELOG.md` *(modify)* | configuration, break-glass, Action required |

---

### Task 1: Migration 00072 — `subject` and the widened `source`

**Files:**
- Create: `internal/store/migrations/sqlite/00072_app_user_oidc.sql`
- Create: `internal/store/migrations/postgres/00072_app_user_oidc.sql`
- Test: `internal/store/oidc_schema_test.go`

**Interfaces:**
- Produces: `app_user.subject TEXT` nullable; `source` accepting `'oidc'`; unique index `app_user_subject_key` over live rows.

**BOTH dialect directories. Never `shared/`.** See Global Constraints — `sqlite/00005` rebuilds `app_user`, so a shared addition vanishes on fresh installs only.

SQLite cannot alter a CHECK constraint, so widening `source` there is create-copy-drop-rename (`sqlite/00005_named_constraints.sql` has the pattern to copy). PostgreSQL uses `DROP CONSTRAINT` / `ADD CONSTRAINT`.

- [ ] **Step 1: Write the failing test**

```go
func TestAppUserCarriesTheOIDCColumns(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			// A row with source='oidc' and a subject must be accepted. Before
			// the migration the CHECK rejects the source and the column does
			// not exist, so this is the whole test.
			id := NewID()
			if _, err := s.DB().Writer.Exec(s.DB().Writer.Rebind(
				`INSERT INTO app_user (id, username, source, subject, is_active, created_at)
				 VALUES (?, ?, 'oidc', ?, TRUE, ?)`),
				id, "alice", "kc-sub-1", domain.FormatTime(s.Now())); err != nil {
				t.Fatalf("inserting an oidc user: %v", err)
			}
			_ = ctx
		})
	}
}

func TestTwoAccountsCannotShareASubject(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, _ := newStore(t, e)
			at := domain.FormatTime(s.Now())
			ins := func(username, subject string) error {
				_, err := s.DB().Writer.Exec(s.DB().Writer.Rebind(
					`INSERT INTO app_user (id, username, source, subject, is_active, created_at)
					 VALUES (?, ?, 'oidc', ?, TRUE, ?)`), NewID(), username, subject, at)
				return err
			}
			if err := ins("alice", "kc-sub-1"); err != nil {
				t.Fatalf("first insert: %v", err)
			}
			if err := ins("bob", "kc-sub-1"); err == nil {
				t.Fatal("two accounts shared one subject. The partial unique index is " +
					"what makes account takeover by subject collision impossible rather " +
					"than merely unlikely (spec D2).")
			}
		})
	}
}

func TestSubjectIsNullableForLocalAndLDAP(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, _ := newStore(t, e)
			at := domain.FormatTime(s.Now())
			for i, src := range []string{"local", "ldap"} {
				if _, err := s.DB().Writer.Exec(s.DB().Writer.Rebind(
					`INSERT INTO app_user (id, username, source, is_active, created_at)
					 VALUES (?, ?, ?, TRUE, ?)`),
					NewID(), fmt.Sprintf("u%d", i), src, at); err != nil {
					t.Fatalf("%s user with no subject: %v", src, err)
				}
			}
			// Two NULL subjects must not collide: a partial unique index over
			// NULLs would make a second password user impossible to create.
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `make test 2>&1 | grep -A3 TestAppUserCarriesTheOIDCColumns`
Expected: FAIL — `CHECK constraint failed` or `no such column: subject`.

- [ ] **Step 3: Write the PostgreSQL migration**

```sql
-- +goose Up
ALTER TABLE app_user ADD COLUMN subject TEXT;
ALTER TABLE app_user DROP CONSTRAINT app_user_source_check;
ALTER TABLE app_user ADD CONSTRAINT app_user_source_check
  CHECK (source IN ('local','ldap','oidc'));
-- Partial: only OIDC accounts carry a subject, and two NULLs must not collide.
CREATE UNIQUE INDEX app_user_subject_key ON app_user(subject) WHERE subject IS NOT NULL;

-- +goose Down
DROP INDEX app_user_subject_key;
ALTER TABLE app_user DROP CONSTRAINT app_user_source_check;
ALTER TABLE app_user ADD CONSTRAINT app_user_source_check
  CHECK (source IN ('local','ldap'));
ALTER TABLE app_user DROP COLUMN subject;
```

- [ ] **Step 4: Write the SQLite migration**

Copy the create-copy-drop-rename shape from `sqlite/00005_named_constraints.sql`'s `app_user` block. The new table declares every existing column plus `subject TEXT`, with the widened named constraint, then `INSERT INTO app_user_new SELECT …, NULL FROM app_user`, `DROP TABLE app_user`, `ALTER TABLE app_user_new RENAME TO app_user`, then recreate every index the old table had **plus** `app_user_subject_key`.

Read the existing block rather than guessing the column list — a dropped column here silently loses user data.

- [ ] **Step 5: Run to verify it passes**

Run: `make test 2>&1 | grep -E "TestAppUserCarries|TestTwoAccountsCannot|TestSubjectIsNullable|^(ok|FAIL)"`
Expected: PASS on sqlite and postgres. `TestEveryDialectMigrationHasBothHalves` and `TestEveryEnumConstraintIsNamed` still pass.

- [ ] **Step 6: Verify a FRESH install keeps the column**

Run:
```bash
rm -f /tmp/oidc-fresh.db
./bin/invctl -migrate  # with INV_DB_DSN=file:/tmp/oidc-fresh.db
sqlite3 /tmp/oidc-fresh.db "PRAGMA table_info(app_user);" | grep subject
```
Expected: the `subject` row is present. **This is the check that catches the `shared/` trap**, which fails only on fresh installs.

- [ ] **Step 7: Commit**

```bash
git add internal/store/migrations internal/store/oidc_schema_test.go
git commit -m "migration(00072): subject column and oidc source on app_user"
```

---

### Task 2: `domain` classification and `UpsertOIDCUser`

**Files:**
- Modify: `internal/domain/classification.go` (add `subject` to `DeclaredColumns["app_user"]`)
- Modify: `internal/domain/*.go` — add `Subject *string \`db:"subject"\`` to `domain.AppUser`
- Modify: `internal/store/users.go`
- Test: `internal/store/oidc_users_test.go`

**Interfaces:**
- Consumes: Task 1's schema.
- Produces:
  ```go
  func (s *SQLStore) GetUserBySubject(ctx context.Context, subject string) (*domain.AppUser, error)
  func (s *SQLStore) UpsertOIDCUser(ctx context.Context, subject, username, displayName, email string) (*domain.AppUser, error)
  ```
  `UpsertOIDCUser` returns `domain.ErrConflict` wrapped with the colliding username when a **different** account already holds that username (spec D4).

**`app_user` has `SELECT *` readers — a column with no struct field breaks sqlx's `StructScan` outright.** Migration `00070` cost ~25 web tests to exactly this omission. Add the field in this task, not later.

- [ ] **Step 1: Write the failing tests**

```go
func TestUpsertOIDCUserMatchesOnSubjectNotUsername(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			first, err := s.UpsertOIDCUser(ctx, "kc-sub-1", "alice", "Alice A", "alice@example.com")
			if err != nil {
				t.Fatalf("first sign-in: %v", err)
			}
			// Same person, renamed in Keycloak. Must be the SAME account.
			again, err := s.UpsertOIDCUser(ctx, "kc-sub-1", "alice.smith", "Alice Smith", "alice@example.com")
			if err != nil {
				t.Fatalf("second sign-in: %v", err)
			}
			if again.ID != first.ID {
				t.Errorf("a rename in Keycloak created a second account (%s then %s). "+
					"Matching is on the immutable sub precisely so a rename does not "+
					"orphan somebody's roles (spec D2).", first.ID, again.ID)
			}
			if again.Username != "alice.smith" {
				t.Errorf("username = %q, want the current Keycloak name", again.Username)
			}
		})
	}
}

func TestUpsertOIDCUserRefusesAUsernameHeldByAnotherAccount(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			// A local account already called admin.
			if _, err := s.CreateUser(ctx, testPermit, "admin", "Local Admin", "hash"); err != nil {
				t.Fatalf("seeding the local admin: %v", err)
			}
			_, err := s.UpsertOIDCUser(ctx, "kc-sub-attacker", "admin", "Not The Admin", "x@example.com")
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("UpsertOIDCUser with a colliding username = %v, want ErrConflict.\n"+
					"    Without this, anybody who can cause a Keycloak account named "+
					"'admin' to exist inherits this system's admin account (spec D2/D4).", err)
			}
			if !strings.Contains(err.Error(), "admin") {
				t.Errorf("the refusal does not name the conflicting username: %v", err)
			}
		})
	}
}

func TestUpsertOIDCUserCreatesAnObserverWithNoProjects(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			u, err := s.UpsertOIDCUser(ctx, "kc-sub-2", "bob", "Bob B", "bob@example.com")
			if err != nil {
				t.Fatalf("first sign-in: %v", err)
			}
			if u.Role != domain.RoleObserver {
				t.Errorf("role = %q, want observer. Keycloak answers WHO, never WHAT "+
					"(spec D1) -- a new account gets nothing until an Administrator "+
					"grants it.", u.Role)
			}
			if u.PasswordHash != nil {
				t.Error("an OIDC account has a password hash; credentials never touch us")
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `make test 2>&1 | grep -E "UpsertOIDCUser|undefined"`
Expected: FAIL — `s.UpsertOIDCUser undefined`.

- [ ] **Step 3: Add the struct field and classification**

```go
// Subject is the OIDC `sub` claim: an immutable identifier issued by the
// provider. NULL for local and LDAP accounts. Matching on it rather than on
// username is what stops a Keycloak account named `admin` inheriting this
// system's admin (docs/superpowers/specs/2026-09-18-keycloak-oidc-design.md D2).
Subject *string `db:"subject"`
```

Add `"subject"` to `DeclaredColumns["app_user"]` — it is declared: a person's identity at the provider, recorded because they signed in, not observed from the estate.

- [ ] **Step 4: Implement the store methods**

`GetUserBySubject` is `GetUserByUsername`'s shape with `WHERE subject = ?`.

`UpsertOIDCUser`:
1. `GetUserBySubject`. Found → update `username`, `display_name`, `email`, `last_login_at`; return it.
2. Not found → `GetUserByUsername`. Found → **return `domain.ErrConflict`** naming the username. This is the whole of D4.
3. Not found either → insert with `source='oidc'`, `subject`, `role=observer`, `password_hash=NULL`.

Every write goes through `s.write(ctx, p, …)` and logs to `change_log` like `UpsertLDAPUser` does.

- [ ] **Step 5: Run to verify they pass**

Run: `make test`
Expected: PASS both engines. `TestEveryColumnIsClassified` passes.

- [ ] **Step 6: Mutation-test the collision guard**

Remove the `GetUserByUsername` branch so a collision inserts instead of refusing. `TestUpsertOIDCUserRefusesAUsernameHeldByAnotherAccount` must go red. Restore with `cp` — **never `git checkout --`**, which has destroyed uncommitted work in this repo.

- [ ] **Step 7: Commit**

```bash
git add internal/domain internal/store
git commit -m "store: resolve an OIDC account by subject, refuse a username collision"
```

---

### Task 3: `internal/auth/oidc.go` — discovery and verification

**Files:**
- Create: `internal/auth/oidc.go`
- Create: `internal/auth/oidc_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces:
  ```go
  type OIDCConfig struct {
      Issuer, ClientID, ClientSecret, RedirectURL string
  }
  type OIDCProvider struct{ /* unexported */ }

  func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error)
  // AuthCodeURL returns the URL to redirect to, given per-attempt secrets.
  func (p *OIDCProvider) AuthCodeURL(state, nonce, verifier string) string
  // Exchange verifies everything and returns the verified claims.
  func (p *OIDCProvider) Exchange(ctx context.Context, code, verifier, nonce string) (*OIDCClaims, error)

  type OIDCClaims struct {
      Subject, Username, DisplayName, Email string
  }
  ```
  `Exchange` returns `ErrInvalidCredentials` for any verification failure and a wrapped error for transport failures — the handler distinguishes them exactly as the password path does.

**This is the only file that talks to Keycloak.** Everything else in the codebase stays ignorant of OIDC.

- [ ] **Step 1: Add the dependencies**

```bash
go get github.com/coreos/go-oidc/v3@v3.21.0
go get golang.org/x/oauth2@v0.37.0
go mod tidy
```

- [ ] **Step 2: Write the fake issuer and the failing tests**

The helper is the point of this task — it must be a **real** issuer, not a mock of `go-oidc`:

```go
// fakeIssuer is an httptest server that behaves like an OIDC provider:
// discovery document, JWKS, and ID tokens signed with a key the test holds.
//
// A MOCK OF go-oidc WOULD TEST THAT A MOCK CAN BE WRITTEN. The failure modes
// worth catching -- is the signature actually checked, is `aud` actually
// compared -- exist only below that line, inside the library.
type fakeIssuer struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	issuer string
}

func newFakeIssuer(t *testing.T) *fakeIssuer { /* … */ }

// token mints an ID token. Every field is settable so a test can make exactly
// one of them wrong.
func (f *fakeIssuer) token(t *testing.T, claims map[string]any, signWith *rsa.PrivateKey, alg string) string
```

Then the refusal table. Each row is a guard that can be deleted:

```go
func TestExchangeRefusesABadToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(claims map[string]any)
		signWith func(f *fakeIssuer) *rsa.PrivateKey
		alg     string
		want    string
	}{
		{name: "signed by the wrong key",
			signWith: func(f *fakeIssuer) *rsa.PrivateKey { return otherKey(t) },
			want:     "signature"},
		{name: "audience is another client",
			mutate: func(c map[string]any) { c["aud"] = "some-other-client" },
			want:   "audience"},
		{name: "issuer is not the configured one",
			mutate: func(c map[string]any) { c["iss"] = "https://evil.example.com/realms/x" },
			want:   "issuer"},
		{name: "expired",
			mutate: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
			want:   "expired"},
		{name: "alg none",
			alg:  "none",
			want: "signature"},
		{name: "nonce does not match the session",
			mutate: func(c map[string]any) { c["nonce"] = "not-the-one-we-sent" },
			want:   "nonce"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// … build, exchange, assert errors.Is(err, auth.ErrInvalidCredentials)
			// and that the LOG line names tc.want while the returned error does not
			// leak it to the user.
		})
	}
}

func TestExchangeAcceptsAGoodToken(t *testing.T) {
	// The positive control. Six refusal tests prove nothing if Exchange
	// refuses everything -- they would all pass for the wrong reason.
}
```

- [ ] **Step 3: Run to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/auth/ -run OIDC -v`
Expected: FAIL — `undefined: NewOIDCProvider`.

- [ ] **Step 4: Implement**

`NewOIDCProvider` calls `oidc.NewProvider(ctx, cfg.Issuer)` (this performs discovery — it is the startup network call) and builds an `oauth2.Config` plus an `*oidc.IDTokenVerifier` with `ClientID` as the audience.

`AuthCodeURL` uses `oauth2.S256ChallengeOption(verifier)` — **PKCE always, even with a client secret.** It costs nothing and removes code interception.

`Exchange` exchanges the code with `oauth2.VerifierOption(verifier)`, pulls `id_token` from the extra fields, verifies it, then compares `nonce` **in constant time** and maps claims. Any verification failure returns `ErrInvalidCredentials`; a transport failure is wrapped with `fmt.Errorf`.

Claims: `sub` → `Subject`, `preferred_username` → `Username`, `name` → `DisplayName`, `email` → `Email`. **A missing or empty `sub` is a hard refusal** — it is the whole of the account-matching rule.

- [ ] **Step 5: Run to verify they pass**

Run: `go test ./internal/auth/ -run OIDC -count=1`
Expected: PASS.

- [ ] **Step 6: Mutation-test three guards**

Delete the audience check, then the nonce comparison, then the `sub`-empty check. The matching named test must go red each time. Restore with `cp` between each.

- [ ] **Step 7: Widen the outbound allowlist, deliberately**

`TestNothingReachesOutOfThisProcess` permits one destination today, for LDAP. Add the issuer host with a comment carrying the same argument:

```go
// The OIDC issuer. The SECOND outbound destination in this codebase, and it
// is here for the same reason LDAP's entry is: authentication is the one
// thing that genuinely has to ask somebody else. Discovery and JWKS fetch
// are the only calls; no token is relayed and nothing is fetched per request.
```

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/auth internal/estate
git commit -m "auth: OIDC discovery, PKCE authorization URL and token verification"
```

---

### Task 4: Configuration

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 3's `auth.OIDCConfig`.
- Produces: `Config.AuthOIDC bool`, `Config.OIDC auth.OIDCConfig`.

- [ ] **Step 1: Write the failing tests**

```go
func TestOIDCIssuerEnablesOIDC(t *testing.T)                    // INV_OIDC_ISSUER set -> AuthOIDC true
func TestOIDCRequiresClientIDAndRedirectURL(t *testing.T)       // named error per missing setting
func TestConfiguringOIDCTurnsLocalAuthOff(t *testing.T)         // spec D3
func TestLocalAuthCanBeForcedBackOnForRecovery(t *testing.T)    // INV_AUTH_LOCAL=true wins
func TestAtLeastOneAuthenticatorIsRequired(t *testing.T)        // the existing rule, now counting OIDC
```

`TestConfiguringOIDCTurnsLocalAuthOff` is the behaviour change and deserves the loudest failure message: with OIDC configured and `INV_AUTH_LOCAL` unset, `AuthLocal` must be **false** — otherwise any account with a password hash bypasses Keycloak and its MFA entirely, which is the whole point (spec D3).

- [ ] **Step 2: Run to verify they fail**
- [ ] **Step 3: Implement**

```go
OIDC: auth.OIDCConfig{
    Issuer:       os.Getenv("INV_OIDC_ISSUER"),
    ClientID:     os.Getenv("INV_OIDC_CLIENT_ID"),
    ClientSecret: os.Getenv("INV_OIDC_CLIENT_SECRET"),
    RedirectURL:  os.Getenv("INV_OIDC_REDIRECT_URL"),
},
```

`AuthOIDC` is `c.OIDC.Issuer != ""`. The default flip must distinguish *unset* from *explicitly false*, so read the raw variable rather than relying on `envBool`'s default:

```go
// INV_AUTH_LOCAL defaults to FALSE once OIDC is configured -- the inverse of
// its default without OIDC, and deliberately. Leaving the password form
// reachable would let any account with a hash skip Keycloak and its MFA
// (spec D3). Setting it explicitly to true is the documented recovery path
// and wins; see docs/RECOVERY.md.
if c.AuthOIDC && os.Getenv("INV_AUTH_LOCAL") == "" {
    c.AuthLocal = false
}
```

Extend `Validate`: `if !c.AuthLocal && !c.AuthLDAP && !c.AuthOIDC` is the error, and when `AuthOIDC` require `ClientID` and `RedirectURL` with a named message each.

- [ ] **Step 4: Run to verify they pass**
- [ ] **Step 5: Commit**

---

### Task 5: Handlers and routes

**Files:**
- Modify: `internal/web/handlers/auth.go`
- Modify: `internal/web/routes.go`
- Modify: `internal/web/route_registration_test.go`
- Test: `internal/web/oidc_flow_test.go`

**Interfaces:**
- Consumes: Task 3's provider, Task 2's `UpsertOIDCUser`, Task 4's config.
- Produces: `GET /auth/oidc`, `GET /auth/oidc/callback`.

**Both routes are registered with bare `mux.HandleFunc`, like `/login`** — and therefore **must be added to the allowlist in `route_registration_test.go`** or `TestEveryRouteIsRegisteredThroughARegistrarOrAnAllowlistedException` fails:

```go
`"GET /auth/oidc"`:          "starts the OIDC redirect; unauthenticated by necessity",
`"GET /auth/oidc/callback"`: "the IdP redirects here; state is its CSRF defence, not a session token",
```

- [ ] **Step 1: Write the failing tests**

```go
func TestOIDCCallbackRefusesAMissingState(t *testing.T)
func TestOIDCCallbackRefusesAReplayedState(t *testing.T)   // second use fails
func TestOIDCCallbackRenewsTheSessionID(t *testing.T)      // fixation defence
func TestOIDCCallbackStoresNoToken(t *testing.T)           // nothing in session or DB
func TestLoginPageOffersKeycloakAndHidesThePasswordForm(t *testing.T)
```

`TestOIDCCallbackStoresNoToken` asserts spec D5: after a successful callback, neither the session nor `app_user` contains an access or refresh token.

- [ ] **Step 2: Run to verify they fail**
- [ ] **Step 3: Implement `OIDCStart`**

Generate `state`, `nonce` and a PKCE verifier with `crypto/rand`; put all three in the session; redirect to `AuthCodeURL`.

- [ ] **Step 4: Implement `OIDCCallback`**

Read and **immediately delete** `state`, `nonce` and verifier from the session — single use, so a replay finds nothing. Compare `state` in constant time; mismatch → security event + generic refusal. `Exchange`. `UpsertOIDCUser`. On `ErrConflict` render the login page with the collision message naming the username and pointing at `/users`. Then `RenewToken`, `Put`, redirect — the existing path, reused unchanged.

Every refusal logs via `auth.LogSecurityEvent` with the reason and shows the user a generic message. **The distinction lives in the log, where it helps the operator and not an attacker** — the rule the password handler already follows.

- [ ] **Step 5: Template**

Keycloak button when `AuthOIDC`; password form only when `AuthLocal`. Every posting element declares its swap target — read the handler for the class rather than guessing; `TestNoClassTwoHandlerEverRendersAFragment` will fail a class-2 handler that renders markup.

- [ ] **Step 6: Run to verify they pass** — `make test`
- [ ] **Step 7: Mutation-test the state check**

Delete the `state` comparison; `TestOIDCCallbackRefusesAMissingState` and the replay test must both go red. Restore with `cp`.

- [ ] **Step 8: Commit**

---

### Task 6: E2E — one round trip through a real Keycloak

**Files:**
- Create: `e2e/oidc.spec.ts` (follow the existing spec layout — read `docs/E2E.md` FIRST; these suites have prerequisites that fail in ways looking nothing like the cause)
- Create: `docker-compose.e2e.yml` service for Keycloak, or extend the existing compose file

**Interfaces:**
- Consumes: Tasks 1-5, end to end.

**Why this exists when the unit layer already covers every refusal.** It covers
what unit tests are structurally blind to: the redirect URL matching, the cookie
flags, and whether the two servers can actually reach each other. `INV_SECURE_COOKIES`
behind a proxy is already a documented footgun in `docs/INSTALL.md`, and a
mismatched redirect URI is the single most common OIDC setup failure. Neither
shows up below the browser.

**Scope is one round trip.** Not exhaustive UI coverage — `CLAUDE.md` is explicit
that E2E is for critical user flows only.

- [ ] **Step 1: Add a Keycloak service and a seeded realm**

A realm export JSON with one client (the redirect URI matching what the test
config sets) and one user with a known password. Import it at container start so
the test does not configure Keycloak through its admin API — a test that sets up
its own fixture through a second API is two things that can break.

- [ ] **Step 2: Write the failing spec**

```ts
test('signs in through Keycloak and lands authenticated', async ({ page }) => {
  await page.goto('/login');
  // The password form must NOT be there: OIDC is configured, so local auth is
  // off by default (spec D3). Asserting its absence is asserting the MFA
  // boundary holds, not a cosmetic detail.
  await expect(page.locator('input[name=password]')).toHaveCount(0);

  await page.getByRole('link', { name: /keycloak/i }).click();
  // Now on Keycloak's own login page, at a different origin.
  await page.fill('#username', 'e2e-user');
  await page.fill('#password', 'e2e-password');
  await page.click('#kc-login');

  // Back on invctl, authenticated.
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByText('What needs a decision')).toBeVisible();
});

test('signing out ends the invctl session', async ({ page }) => {
  // … sign in as above, then sign out, then assert /assets redirects to /login.
  // invctl's session is its own (spec D5): this asserts OUR session ended, and
  // deliberately does not assert anything about Keycloak's.
});
```

- [ ] **Step 3: Run to verify it fails**

Run: `npx playwright test e2e/oidc.spec.ts`
Expected: FAIL — no Keycloak button on the login page yet, or the redirect URI is rejected.

- [ ] **Step 4: Make it pass**

Most likely fixes are configuration rather than code by this point: the redirect
URL in the test environment must match the realm export exactly, and
`INV_SECURE_COOKIES` must be false for plain HTTP in the test harness.

- [ ] **Step 5: Confirm it can fail**

Change the realm's redirect URI to something else and re-run. The test must fail
at the Keycloak page rather than passing. Restore. **A green E2E that would pass
against a broken redirect configuration is testing nothing** — and this suite
exists precisely to catch that class.

- [ ] **Step 6: Commit**

```bash
git add e2e docker-compose.e2e.yml
git commit -m "test(e2e): one round trip through a real Keycloak"
```

---

### Task 7: Documentation

**Files:**
- Modify: `docs/INSTALL.md`, `docs/RECOVERY.md`, `CHANGELOG.md`, `docs/manual/parts/12-directory.md`, `docs/manual/MANIFEST.yaml`

- [ ] **Step 1: `INSTALL.md`** — the four `INV_OIDC_*` settings, a worked Keycloak client example, and the note that the redirect URL must match exactly because it is the most common setup failure.
- [ ] **Step 2: `RECOVERY.md`** — `INV_AUTH_LOCAL=true` plus a restart, beside the existing break-glass entry. State the property: if you can reach the host you can recover; if you cannot, you cannot bypass MFA.
- [ ] **Step 3: `CHANGELOG.md`** — under **Action required**, because `INV_AUTH_LOCAL`'s default inverts when OIDC is configured. An operator who does not read this loses the password form without warning.
- [ ] **Step 4: manual** — extend the directory fragment to cover OIDC beside LDAP, and bump **only** that fragment's `generated_at`.
- [ ] **Step 5: Run `bash tools/manual-stale.sh`** — expect all fragments current.
- [ ] **Step 6: Commit**

---

## Evidence gate

Before review, state what would be true if this were broken and what was run to show it is not.

- A token signed by a key we do not trust is accepted → the wrong-key test.
- A token for another client is accepted → the audience test.
- A callback can be replayed → the replayed-state test.
- A Keycloak account named `admin` inherits the local admin → the collision test.
- A new OIDC user arrives with write access → the observer test.
- The migration vanishes on a fresh install → the fresh-install check in Task 1 Step 6.
- Every guard above was **mutation-tested**: deleted, observed red, restored.
- `make test` green on both engines; `make lint` clean.
