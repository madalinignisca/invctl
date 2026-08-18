# Read-only Inventory API (WP-A2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish declared inventory state to machine consumers over a read-only,
token-scoped HTTP surface that cannot become a write surface.

**Architecture:** A third principal type (`auth.Reader`) alongside the existing
operator and monitoring credentials, configured from environment variables and
holding only a token digest. Four keyset-paginated collections, two
single-resource routes and one composed Ansible view, all `GET`, all under
`/api/v1`, mounted only when a credential is configured. Hand-written DTOs in
their own package are the published contract; store structs never cross the
boundary.

**Tech Stack:** Go 1.22+ stdlib, `net/http.ServeMux` method+wildcard patterns,
`jmoiron/sqlx` with hand-written SQL, `modernc.org/sqlite` + `jackc/pgx/v5`.
**No new dependencies.**

**Spec:** `docs/api-design.md`

## Global Constraints

Every task's requirements implicitly include all of these. They come from
`CLAUDE.md` and `docs/api-design.md`; violating one is a rejected task, not a
nitpick.

- **Placeholders are `?`.** Call `sqlx.Rebind` before execution. Never `$1`.
- **Every query must run unmodified on SQLite and PostgreSQL.** No `inet`,
  `cidr`, native arrays, `ENUM`, `jsonb` operators in `WHERE`, `SERIAL`,
  `generate_series()`, `NOW()`, or `RETURNING` on multi-row statements.
- **Booleans are `TRUE`/`FALSE` literals**, never `0`/`1`.
- **No migration in this work package.** If a task appears to need a schema
  change, stop and raise it — the spec says there is none.
- **Nothing here writes.** No `INSERT`, `UPDATE` or `DELETE` in any file this
  plan creates. No `change_log` row, because nothing mutates declared state.
- **Licence header on every new `.go` file**, followed by a **blank line**
  before the package clause:
  ```go
  // invctl — infrastructure inventory
  // Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
  //
  // Licensed under the GNU Affero General Public License, version 3 only —
  // no later version applies. See LICENSE for the full text.
  //
  // SPDX-License-Identifier: AGPL-3.0-only
  ```
- **`internal/domain` keeps zero external dependencies.** Nothing this plan adds
  goes in `domain` except by reusing what is already there.
- **Wrap errors** as `fmt.Errorf("doing x: %w", err)`. Never bare `return err`
  from a non-trivial call. Never `panic` outside `main`.
- **Context first.** Every store method takes `ctx context.Context` first.
- **Table-driven tests against the fixture estate**, not mocks.
- **`make test` is the gate**, not `go test ./...` — the latter silently skips
  Postgres when `INV_TEST_POSTGRES_DSN` is unset and reports green on half the
  evidence.
- **`gofmt`, `go vet`, `staticcheck` clean** before any task is considered done.
- Branch is `wp-a2-inventory-api`. Commit after every task.

---

## File Structure

**Import direction — check this before writing Task 7.** `internal/web` imports
`internal/api`; `internal/api` imports `internal/web/middleware` (for
`ReaderFrom`) and `internal/web/render` (for `JSON`/`JSONError`). That is not a
cycle, because neither `middleware` nor `render` imports `internal/web`. It
becomes one the moment somebody has `internal/api` import `internal/web` or
`internal/web/handlers` — if a task seems to need that, the shared thing belongs
in `middleware` or `render`, not in a back-reference.

**Created:**

| File | Responsibility |
|---|---|
| `internal/config/reader.go` | Parse `INV_API_TOKENS` / `INV_API_SCOPES` into `[]ReaderCredential`. Startup validation only. |
| `internal/config/reader_test.go` | Malformed config refuses to start, with the id in the message. |
| `internal/auth/reader.go` | `Reader`, `ReaderRegistry`. Digest-only lookup. No `domain.Actor`. |
| `internal/auth/reader_test.go` | Authentication, separation from agents, no token retained. |
| `internal/web/middleware/reader.go` | `RequireReader`, `ReaderGuard`, reader in request context. |
| `internal/web/middleware/reader_test.go` | Session refusal, bearer handling, rate limiting after identity. |
| `internal/api/dto.go` | The published contract. Hand-written structs, explicit JSON tags. |
| `internal/api/dto_test.go` | Guard tests: no money, no personal data, no observed state. |
| `internal/api/page.go` | Cursor encode/parse, `limit` clamp, collection envelope. |
| `internal/api/page_test.go` | Malformed cursor is refused, not ignored. |
| `internal/api/api.go` | `API` struct, its dependencies, shared helpers, error mapping. |
| `internal/api/assets.go` | Assets collection + single resource. |
| `internal/api/services.go` | Services collection + single resource. |
| `internal/api/addresses.go` | Addresses collection. |
| `internal/api/environments.go` | Environments collection. |
| `internal/api/ansible.go` | The composed dynamic-inventory view. |
| `internal/store/api.go` | Scoped, keyset-paginated read queries. Read-only by construction. |
| `internal/store/api_test.go` | Scope predicate in SQL, page ordering, both engines. |
| `internal/web/api_test.go` | End-to-end through the router: auth, scope, shapes, golden files. |
| `internal/web/testdata/api/*.json` | Golden JSON for every DTO and the Ansible view. |
| `docs/API.md` | Consumer documentation. |

**Modified:**

| File | Change |
|---|---|
| `internal/config/config.go` | Call `loadReaderCredentials()` from `Load()`; add the field. |
| `internal/auth/security.go` | Four new event constants for reader refusals. |
| `internal/web/routes.go` | Mount `/api/v1/*` behind `RequireReader` when configured. |
| `internal/web/web_test.go` | `newHarnessWithReaders` so tests can configure read tokens. |
| `CHANGELOG.md` | An **Added** entry. |
| `docs/ROADMAP.md` | `WP-A2` marker to DONE, at the very end. |

---

### Task 1: Reader credentials in config

**Files:**
- Create: `internal/config/reader.go`
- Create: `internal/config/reader_test.go`
- Modify: `internal/config/config.go` (add field, call loader from `Load()`)

**Interfaces:**
- Consumes: `splitPairs(envName, raw string) ([]pair, error)` from `internal/config/agent.go`, where `pair` is `struct{ key, value string }`.
- Produces:
  ```go
  type ReaderCredential struct {
      ID           string
      Token        string
      Environments []string
  }
  func loadReaderCredentials() ([]ReaderCredential, error)
  // Config gains: Readers []ReaderCredential
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/config/reader_test.go`. Mirror the style of `agent_test.go`.

```go
func TestReaderCredentialsLoadFromTheTwoVariables(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:tok-a,grafana:tok-g")
	t.Setenv("INV_API_SCOPES", "ansible:prod|staging,grafana:prod")

	creds, err := loadReaderCredentials()
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("got %d credentials, want 2", len(creds))
	}
	// Sorted by id, so a test and a log line agree regardless of how the
	// operator wrote the variable.
	if creds[0].ID != "ansible" || creds[1].ID != "grafana" {
		t.Fatalf("got ids %q/%q", creds[0].ID, creds[1].ID)
	}
	if got := strings.Join(creds[0].Environments, ","); got != "prod,staging" {
		t.Fatalf("got environments %q, want prod,staging", got)
	}
}

func TestAReaderCredentialWithoutAScopeRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:tok-a")
	t.Setenv("INV_API_SCOPES", "")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("a credential with no scope must refuse to start")
	}
	if !strings.Contains(err.Error(), "ansible") {
		t.Fatalf("the error must name the credential; got %v", err)
	}
}

func TestAScopeForAnUnknownReaderRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:tok-a")
	t.Setenv("INV_API_SCOPES", "ansible:prod,typo:prod")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("a scope naming a credential that does not exist must refuse to start")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("the error must name the unknown credential; got %v", err)
	}
}

func TestADuplicateReaderIDRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:tok-a,ansible:tok-b")
	t.Setenv("INV_API_SCOPES", "ansible:prod")

	if _, err := loadReaderCredentials(); err == nil {
		t.Fatal("a duplicate credential id must refuse to start")
	}
}

func TestNoReaderCredentialsIsNotAnError(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "")
	t.Setenv("INV_API_SCOPES", "")

	creds, err := loadReaderCredentials()
	if err != nil {
		t.Fatalf("an estate with no integrations must start: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("got %d credentials, want 0", len(creds))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestReader -v`
Expected: FAIL — `undefined: loadReaderCredentials`

- [ ] **Step 3: Write the implementation**

Create `internal/config/reader.go` with the licence header, then:

```go
// The read-credential half of the machine surface (WP-A2).
//
// Two variables rather than the three the monitoring credentials use: a reader
// has no vocabulary, because it never writes an observation and therefore never
// maps a reporter's words onto a HealthState.
//
// Nothing here logs a token, and ReaderCredential deliberately cannot be
// printed with its secret -- see String and LogValue below.

package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
)

// ReaderCredential is one read-only API credential as configured.
type ReaderCredential struct {
	ID           string
	Token        string
	Environments []string
}

// String renders a credential without its token.
func (c ReaderCredential) String() string {
	return fmt.Sprintf("reader %s (environments=%s)", c.ID, strings.Join(c.Environments, "|"))
}

// LogValue keeps the token out of a structured log line even when somebody logs
// the whole struct.
func (c ReaderCredential) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", c.ID),
		slog.String("environments", strings.Join(c.Environments, "|")),
	)
}

// loadReaderCredentials assembles the credentials from the two variables that
// describe them.
//
// Every failure is a startup failure with the credential id in the message. The
// alternative -- skipping a credential that will not build -- means an
// integration that authenticates today stops authenticating after a config
// edit, with nothing in the logs naming the edit.
func loadReaderCredentials() ([]ReaderCredential, error) {
	tokens, err := splitPairs("INV_API_TOKENS", os.Getenv("INV_API_TOKENS"))
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	scopes, err := splitPairs("INV_API_SCOPES", os.Getenv("INV_API_SCOPES"))
	if err != nil {
		return nil, err
	}

	byID := make(map[string]string, len(scopes))
	for _, p := range scopes {
		byID[p.key] = p.value
	}

	seen := make(map[string]bool, len(tokens))
	creds := make([]ReaderCredential, 0, len(tokens))
	for _, p := range tokens {
		if seen[p.key] {
			return nil, fmt.Errorf("validating config: INV_API_TOKENS names credential %q twice", p.key)
		}
		seen[p.key] = true

		scope, ok := byID[p.key]
		if !ok {
			return nil, fmt.Errorf(
				"validating config: read credential %q has no entry in INV_API_SCOPES; "+
					"there is no wildcard, so every credential must name the environments it may read",
				p.key)
		}
		envs := make([]string, 0, 2)
		for _, code := range strings.Split(scope, scopeSeparator) {
			if code = strings.TrimSpace(code); code != "" {
				envs = append(envs, code)
			}
		}
		if len(envs) == 0 {
			return nil, fmt.Errorf(
				"validating config: read credential %q has an empty environment scope in INV_API_SCOPES", p.key)
		}
		creds = append(creds, ReaderCredential{ID: p.key, Token: p.value, Environments: envs})
	}

	for id := range byID {
		if !seen[id] {
			return nil, fmt.Errorf(
				"validating config: INV_API_SCOPES names credential %q, which is not in INV_API_TOKENS", id)
		}
	}

	sort.Slice(creds, func(i, j int) bool { return creds[i].ID < creds[j].ID })
	return creds, nil
}
```

Then in `internal/config/config.go`: add `Readers []ReaderCredential` to the
`Config` struct beside the existing agent field, and inside `Load()` call
`loadReaderCredentials()` immediately after the agent credentials are loaded,
returning the error unwrapped so the startup message reads the same way.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS, including the pre-existing agent tests.

- [ ] **Step 5: Verify the token cannot be logged**

Run: `go test ./internal/config/ -run TestReader -v && gofmt -l internal/config && go vet ./internal/config/`
Expected: PASS, no gofmt output, no vet findings.

- [ ] **Step 6: Commit**

```bash
git add internal/config/reader.go internal/config/reader_test.go internal/config/config.go
git commit -m "WP-A2: read credentials, and every way of misconfiguring one refuses to start"
```

---

### Task 2: ReaderRegistry

**Files:**
- Create: `internal/auth/reader.go`
- Create: `internal/auth/reader_test.go`

**Interfaces:**
- Consumes: `config.ReaderCredential` (Task 1), `domain.NewEnvironmentScope(codes []string) (EnvironmentScope, error)`, `BearerToken(header string) (string, bool)`.
- Produces:
  ```go
  type Reader struct {
      ID           string
      Environments domain.EnvironmentScope
  }
  func (r *Reader) String() string
  type ReaderRegistry struct{ /* unexported */ }
  func NewReaderRegistry(creds []config.ReaderCredential) (*ReaderRegistry, error)
  func (r *ReaderRegistry) Enabled() bool
  func (r *ReaderRegistry) IDs() []string
  func (r *ReaderRegistry) Authenticate(token string) (*Reader, bool)
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/auth/reader_test.go`:

```go
func mustReaderRegistry(t *testing.T, creds []config.ReaderCredential) *ReaderRegistry {
	t.Helper()
	r, err := NewReaderRegistry(creds)
	if err != nil {
		t.Fatalf("building registry: %v", err)
	}
	return r
}

func TestAReaderAuthenticatesByItsToken(t *testing.T) {
	r := mustReaderRegistry(t, []config.ReaderCredential{
		{ID: "ansible", Token: "tok-a", Environments: []string{"prod"}},
	})
	reader, ok := r.Authenticate("tok-a")
	if !ok {
		t.Fatal("the configured token must authenticate")
	}
	if reader.ID != "ansible" {
		t.Fatalf("got id %q, want ansible", reader.ID)
	}
	if !reader.Environments.Allows("prod") {
		t.Fatal("the reader must carry its configured scope")
	}
	if _, ok := r.Authenticate("wrong"); ok {
		t.Fatal("an unknown token must not authenticate")
	}
}

func TestAnEmptyReaderRegistryAuthenticatesNobody(t *testing.T) {
	var r *ReaderRegistry
	if r.Enabled() {
		t.Fatal("a nil registry is not enabled")
	}
	if _, ok := r.Authenticate("anything"); ok {
		t.Fatal("a nil registry must authenticate nobody")
	}
}

func TestAReaderRegistryRefusesACredentialItCannotBuild(t *testing.T) {
	// There is no wildcard: an empty scope is a startup failure, not
	// "everything".
	if _, err := NewReaderRegistry([]config.ReaderCredential{
		{ID: "broken", Token: "t", Environments: nil},
	}); err == nil {
		t.Fatal("a credential with no environments must refuse to build")
	}
}

func TestAReaderCarriesNoToken(t *testing.T) {
	r := mustReaderRegistry(t, []config.ReaderCredential{
		{ID: "ansible", Token: "sup3rsecret", Environments: []string{"prod"}},
	})
	reader, _ := r.Authenticate("sup3rsecret")
	if strings.Contains(reader.String(), "sup3rsecret") {
		t.Fatal("a reader must not render its token")
	}
}

func TestAReaderHasNoActor(t *testing.T) {
	// A compile-level assertion in test form: Reader deliberately exposes no
	// Actor() method, because it never writes and therefore has no audit
	// identity that could be misused. If somebody adds one, this test is the
	// place the argument has to be had.
	var r any = &Reader{}
	if _, ok := r.(interface{ Actor() domain.Actor }); ok {
		t.Fatal("a reader must not carry an audit actor; it never writes")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/auth/ -run TestAReader -v`
Expected: FAIL — `undefined: NewReaderRegistry`

- [ ] **Step 3: Write the implementation**

Create `internal/auth/reader.go` with the licence header, then a file modelled
directly on `agent.go` with these differences, each of which matters:

- `Reader` has **no** `actor` field and **no** `Actor()` method.
- `Reader` has **no** `Vocabulary`.
- Digest comparison uses `crypto/subtle.ConstantTimeCompare`, exactly as
  `AgentRegistry.Authenticate` does — copy that function's shape rather than
  writing a new comparison.
- Entries sorted by id, so a log line and a test agree.

```go
type Reader struct {
	ID           string
	Environments domain.EnvironmentScope
}

func (r *Reader) String() string {
	return fmt.Sprintf("reader %s (environments=%s)", r.ID, r.Environments)
}

type readerEntry struct {
	reader Reader
	digest [sha256.Size]byte
}

type ReaderRegistry struct {
	entries []readerEntry
}

func NewReaderRegistry(creds []config.ReaderCredential) (*ReaderRegistry, error) {
	r := &ReaderRegistry{entries: make([]readerEntry, 0, len(creds))}
	for _, c := range creds {
		scope, err := domain.NewEnvironmentScope(c.Environments)
		if err != nil {
			return nil, fmt.Errorf("scoping read credential %q: %w", c.ID, err)
		}
		r.entries = append(r.entries, readerEntry{
			reader: Reader{ID: c.ID, Environments: scope},
			digest: sha256.Sum256([]byte(c.Token)),
		})
	}
	sort.Slice(r.entries, func(i, j int) bool { return r.entries[i].reader.ID < r.entries[j].reader.ID })
	return r, nil
}

func (r *ReaderRegistry) Enabled() bool { return r != nil && len(r.entries) > 0 }

func (r *ReaderRegistry) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.reader.ID)
	}
	return out
}

// Authenticate compares against every entry so that timing does not reveal how
// many credentials are configured or which one matched.
func (r *ReaderRegistry) Authenticate(token string) (*Reader, bool) {
	if r == nil || token == "" {
		return nil, false
	}
	got := sha256.Sum256([]byte(token))
	var found *Reader
	for i := range r.entries {
		if subtle.ConstantTimeCompare(got[:], r.entries[i].digest[:]) == 1 {
			found = &r.entries[i].reader
		}
	}
	return found, found != nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS, including all pre-existing agent tests.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/reader.go internal/auth/reader_test.go
git commit -m "WP-A2: the reader registry, which holds a digest and no actor"
```

---

### Task 3: RequireReader

**Files:**
- Create: `internal/web/middleware/reader.go`
- Create: `internal/web/middleware/reader_test.go`
- Modify: `internal/auth/security.go` (four event constants)

**Interfaces:**
- Consumes: `auth.ReaderRegistry` (Task 2), `NewRateLimiter(perSecond float64, burst int) *RateLimiter`, `sessionPresent(r *http.Request, cookie string) (string, bool)` and `unauthorised(w http.ResponseWriter)` / `tooManyRequests(w http.ResponseWriter)` from `internal/web/middleware/agent.go`.
- Produces:
  ```go
  type ReaderGuard struct {
      Registry        *auth.ReaderRegistry
      Credentials     *RateLimiter
      Unauthenticated *RateLimiter
      SessionCookie   string
  }
  func RequireReader(g ReaderGuard) func(http.Handler) http.Handler
  func ReaderFrom(ctx context.Context) (*auth.Reader, bool)
  const ReaderRequestsPerSecond = 10.0
  const ReaderBurst = 60
  ```

- [ ] **Step 1: Add the security event constants**

In `internal/auth/security.go`, beside the existing `EventAgent*` constants:

```go
	// EventReaderRejected is a read credential that did not authenticate.
	EventReaderRejected = "reader_token_rejected"
	// EventReaderSessionConfusion is a browser session arriving on the API.
	EventReaderSessionConfusion = "reader_session_confusion"
	// EventReaderThrottled is a read credential over its rate limit.
	EventReaderThrottled = "reader_rate_limited"
	// EventReaderScopeDenied is a read credential asking for an entity outside
	// its environment scope. The response is a 404 indistinguishable from an
	// absent entity, so this log line is the ONLY place the difference is
	// visible -- an operator debugging "the API returns nothing" finds the
	// answer here and nowhere else.
	EventReaderScopeDenied = "reader_scope_denied"
```

- [ ] **Step 2: Write the failing test**

Create `internal/web/middleware/reader_test.go`. Model it on the existing agent
middleware tests:

```go
func TestABrowserSessionIsRefusedOnTheAPI(t *testing.T) {
	g := testReaderGuard(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer tok-a")
	req.AddCookie(&http.Cookie{Name: g.SessionCookie, Value: "anything"})

	RequireReader(g)(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a session on this route is principal confusion", rec.Code)
	}
}

func TestNoBearerTokenIsUnauthorised(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/assets", nil)
	RequireReader(testReaderGuard(t))(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestAValidReaderReachesTheHandlerAndIsInContext(t *testing.T) {
	var seen string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, ok := ReaderFrom(r.Context())
		if !ok {
			t.Error("the handler must be able to read its principal from the context")
			return
		}
		seen = reader.ID
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer tok-a")

	RequireReader(testReaderGuard(t))(h).ServeHTTP(rec, req)

	if seen != "ansible" {
		t.Fatalf("got reader %q, want ansible", seen)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("got Cache-Control %q, want no-store", got)
	}
}

func TestRepeatedFailureThrottlesTheUnauthenticatedBucket(t *testing.T) {
	g := testReaderGuard(t)
	g.Unauthenticated = NewRateLimiter(0, 1) // one attempt, no refill
	var last int
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/assets", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		RequireReader(g)(okHandler()).ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429 after repeated failures", last)
	}
}

func TestAWorkingReaderNeverTouchesTheUnauthenticatedBucket(t *testing.T) {
	g := testReaderGuard(t)
	g.Unauthenticated = NewRateLimiter(0, 1)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/assets", nil)
		req.Header.Set("Authorization", "Bearer tok-a")
		RequireReader(g)(okHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d got %d, want 200", i, rec.Code)
		}
	}
}
```

`testReaderGuard` builds a `ReaderGuard` over a one-credential registry
(`ansible` / `tok-a` / `{prod}`) with generous limiters and
`SessionCookie: "session"`. `okHandler` returns a handler writing 200.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/web/middleware/ -run TestA -v`
Expected: FAIL — `undefined: RequireReader`

- [ ] **Step 4: Write the implementation**

Create `internal/web/middleware/reader.go` with the licence header. Copy the
**structure and check order** of `RequireAgent` in `agent.go` — that order is
load-bearing and its reasoning is in the comment there:

1. session present → `EventReaderSessionConfusion`, 401 via `render.JSONError`
2. no bearer token → `EventReaderRejected`, `unauthorised(w)`
3. unknown token → consume the unauthenticated bucket, `EventReaderRejected`
   (or `EventReaderThrottled` if the bucket is empty), 401 or 429
4. known token → per-credential bucket keyed by `reader.ID`; over limit →
   `EventReaderThrottled`, 429
5. otherwise attach the reader to a derived context and call `next`

Set `w.Header().Set("Cache-Control", "no-store")` first, before any branch.

Context plumbing uses an unexported key type, exactly as the agent middleware
does:

```go
type readerContextKey struct{}

func ReaderFrom(ctx context.Context) (*auth.Reader, bool) {
	reader, ok := ctx.Value(readerContextKey{}).(*auth.Reader)
	return reader, ok
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/web/middleware/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/web/middleware/reader.go internal/web/middleware/reader_test.go internal/auth/security.go
git commit -m "WP-A2: the read guard, in the same order of risks as the write one"
```

---

### Task 4: DTOs and the contract guards

**Files:**
- Create: `internal/api/dto.go`
- Create: `internal/api/dto_test.go`

**Interfaces:**
- Produces:
  ```go
  type Asset struct {
      ID           string   `json:"id"`
      Name         string   `json:"name"`
      Kind         string   `json:"kind"`
      Lifecycle    string   `json:"lifecycle"`
      Environments []string `json:"environments"`
      Site         *string  `json:"site"`
      Rack         *string  `json:"rack"`
      Role         *string  `json:"role"`
      Addresses    []string `json:"addresses"`
      Services     []string `json:"services"`
  }
  type Service struct {
      ID           string   `json:"id"`
      Code         string   `json:"code"`
      Name         string   `json:"name"`
      Kind         string   `json:"kind"`
      Lifecycle    string   `json:"lifecycle"`
      Environments []string `json:"environments"`
      Criticality  int      `json:"criticality"`
      Assets       []string `json:"assets"`
  }
  type Address struct {
      ID           string   `json:"id"`
      Address      string   `json:"address"`
      Family       int      `json:"family"`
      Asset        *string  `json:"asset"`
      AssetID      *string  `json:"asset_id"`
      Environments []string `json:"environments"`
  }
  type Environment struct {
      ID          string `json:"id"`
      Code        string `json:"code"`
      Name        string `json:"name"`
      Role        string `json:"role"`
      InScope     bool   `json:"in_scope"`
      Criticality int    `json:"criticality"`
  }
  ```

Note: `Service.Criticality` is the domain `Tier` field renamed for the contract,
because `tier` is an internal word and `criticality` is what the UI calls it.
`Service.Environments` is a one-element slice built from the service's single
`EnvironmentID`, so that the contract has one shape for "what is this in" across
every entity even though the schema differs.

- [ ] **Step 1: Write the failing test**

Create `internal/api/dto_test.go`. These are the guard tests, and they work by
reflecting over the DTO structs, so they keep working when somebody adds a field:

```go
// dtoTypes is every struct published by this package. A new DTO goes here, and
// the guards below then apply to it without anybody remembering to add them.
func dtoTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf(Asset{}),
		reflect.TypeOf(Service{}),
		reflect.TypeOf(Address{}),
		reflect.TypeOf(Environment{}),
	}
}

func TestTheAPINeverExposesMoney(t *testing.T) {
	forbidden := []string{"cost", "price", "amount", "supplier", "tariff",
		"currency", "invoice", "spend", "budget", "amorti"}
	assertNoFieldMatches(t, forbidden,
		"WP-A2 publishes topology, not commercial terms; a leaked read token must not expose what the estate costs")
}

func TestTheAPINeverExposesPersonalData(t *testing.T) {
	forbidden := []string{"actor", "contact", "email", "username", "person", "owner"}
	assertNoFieldMatches(t, forbidden,
		"invariant 5: no personal data. Teams and roles, and not on this surface at all")
}

func TestTheAPINeverExposesObservedState(t *testing.T) {
	forbidden := []string{"observed", "health", "state_since", "reporter",
		"last_report", "reported_at"}
	assertNoFieldMatches(t, forbidden,
		"this surface publishes declared state; observed state has its own direction and its own principal")
}

func TestTheAPINeverExposesASecretReference(t *testing.T) {
	forbidden := []string{"secret", "token", "password", "hash", "key"}
	assertNoFieldMatches(t, forbidden,
		"a secret reference is a path and still never belongs in a published payload")
}

func TestEveryDTOFieldHasAJSONTag(t *testing.T) {
	for _, typ := range dtoTypes() {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Tag.Get("json") == "" {
				t.Errorf("%s.%s has no json tag; the contract must be explicit, never derived from a Go name",
					typ.Name(), f.Name)
			}
		}
	}
}

func TestNoDTOEmbedsAStoreOrDomainStruct(t *testing.T) {
	for _, typ := range dtoTypes() {
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).Anonymous {
				t.Errorf("%s embeds %s; a DTO is shaped by the contract and a store struct is shaped by the schema, "+
					"and embedding one means the next migration silently changes the published surface",
					typ.Name(), typ.Field(i).Type)
			}
		}
	}
}

// assertNoFieldMatches lowercases every field name and json tag of every DTO
// and refuses any that contains one of the forbidden substrings.
func assertNoFieldMatches(t *testing.T, forbidden []string, why string) {
	t.Helper()
	for _, typ := range dtoTypes() {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			hay := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
			for _, bad := range forbidden {
				if strings.Contains(hay, bad) {
					t.Errorf("%s.%s (json %q) matches %q -- %s",
						typ.Name(), f.Name, f.Tag.Get("json"), bad, why)
				}
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -v`
Expected: FAIL — the package does not exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/api/dto.go` with the licence header, a package comment
explaining that these structs are the published contract and that adding a field
is a deliberate act, then the four structs exactly as given in the Interfaces
block above.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -v`
Expected: PASS

Note: `TestTheAPINeverExposesPersonalData` forbids `owner`, and
`TestTheAPINeverExposesASecretReference` forbids `key`. If a future field is
legitimately blocked by an over-broad substring, the fix is to argue it out in a
comment and narrow the list — not to delete the guard.

- [ ] **Step 5: Commit**

```bash
git add internal/api/dto.go internal/api/dto_test.go
git commit -m "WP-A2: the published contract, and four guards over what may enter it"
```

---

### Task 5: Cursor and page envelope

**Files:**
- Create: `internal/api/page.go`
- Create: `internal/api/page_test.go`

**Interfaces:**
- Produces:
  ```go
  const DefaultLimit = 100
  const MaxLimit = 500

  type Page[T any] struct {
      Data []T     `json:"data"`
      Next *string `json:"next"`
  }

  // PageRequest is a parsed, validated ?after= and ?limit=.
  type PageRequest struct {
      After string // "" is the first page
      Limit int
  }

  // ParsePageRequest refuses what it cannot use. It never falls back.
  func ParsePageRequest(q url.Values) (PageRequest, error)

  // ErrBadRequest wraps a client mistake; api.go maps it to 400.
  var ErrBadRequest = errors.New("bad request")
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/api/page_test.go`:

```go
func TestAMalformedCursorIsRefusedNotIgnored(t *testing.T) {
	// ParseChangeCursor treats a bad cursor as "first page", which is right for
	// a human clicking a link and catastrophic for a client paginating: it
	// would re-ingest page one forever, with a 200 every time, and never reach
	// the rest of the estate. TestNoParseErrorIsDiscarded exists to refuse
	// exactly this mechanism.
	for _, bad := range []string{"not-a-uuid", "../etc/passwd", "01924e5a zzz", "%%%"} {
		_, err := ParsePageRequest(url.Values{"after": {bad}})
		if err == nil {
			t.Errorf("cursor %q was accepted; a cursor that cannot be used must be refused", bad)
		}
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("cursor %q gave %v, want ErrBadRequest", bad, err)
		}
	}
}

func TestAnAbsentCursorIsTheFirstPage(t *testing.T) {
	p, err := ParsePageRequest(url.Values{})
	if err != nil {
		t.Fatalf("an absent cursor is not an error: %v", err)
	}
	if p.After != "" {
		t.Fatalf("got after %q, want empty", p.After)
	}
	if p.Limit != DefaultLimit {
		t.Fatalf("got limit %d, want %d", p.Limit, DefaultLimit)
	}
}

func TestAValidCursorRoundTrips(t *testing.T) {
	id := "01924e5a-1c2b-7f3a-9d4e-5f6a7b8c9d0e"
	p, err := ParsePageRequest(url.Values{"after": {id}})
	if err != nil {
		t.Fatalf("a well-formed cursor must be accepted: %v", err)
	}
	if p.After != id {
		t.Fatalf("got after %q, want %q", p.After, id)
	}
}

func TestAnOversizedLimitIsClamped(t *testing.T) {
	// A clamp, unlike a swallowed cursor, is documented, in-band and visible in
	// the length of the response the client just received.
	p, err := ParsePageRequest(url.Values{"limit": {"100000"}})
	if err != nil {
		t.Fatalf("an oversized limit is clamped, not refused: %v", err)
	}
	if p.Limit != MaxLimit {
		t.Fatalf("got limit %d, want %d", p.Limit, MaxLimit)
	}
}

func TestAnUnparseableLimitIsRefused(t *testing.T) {
	for _, bad := range []string{"lots", "-1", "0", "1.5"} {
		if _, err := ParsePageRequest(url.Values{"limit": {bad}}); !errors.Is(err, ErrBadRequest) {
			t.Errorf("limit %q gave %v, want ErrBadRequest", bad, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestA -v`
Expected: FAIL — `undefined: ParsePageRequest`

- [ ] **Step 3: Write the implementation**

Create `internal/api/page.go` with the licence header. The cursor **is** the id
of the last row of the previous page, validated as a UUID with
`github.com/google/uuid` (already a dependency) so that a malformed value is
refused before it reaches SQL:

```go
func ParsePageRequest(q url.Values) (PageRequest, error) {
	p := PageRequest{Limit: DefaultLimit}

	if raw := q.Get("after"); raw != "" {
		if _, err := uuid.Parse(raw); err != nil {
			return PageRequest{}, fmt.Errorf(
				"%w: after must be the id of the last row of the previous page", ErrBadRequest)
		}
		p.After = raw
	}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return PageRequest{}, fmt.Errorf("%w: limit must be a positive whole number", ErrBadRequest)
		}
		if n > MaxLimit {
			n = MaxLimit
		}
		p.Limit = n
	}
	return p, nil
}
```

The error message deliberately does not echo the offending value back, so a
malformed cursor cannot be used to reflect content into a client's log.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/page.go internal/api/page_test.go
git commit -m "WP-A2: a cursor that cannot be used is refused, never silently restarted"
```

---

### Task 6: The scoped, paginated store path

**Files:**
- Create: `internal/store/api.go`
- Create: `internal/store/api_test.go`

**Interfaces:**
- Consumes: `domain.EnvironmentScope` and its `Codes() []string`, the existing
  `s.readMany` / `s.readOne` helpers and `placeholders(n int) string` from
  `internal/store/`.
- Produces:
  ```go
  // APIAssetFilter is the API's own filter. Separate from AssetFilter because
  // the UI's list is ordered by name and this one must be ordered by id.
  type APIAssetFilter struct {
      Scope          domain.EnvironmentScope
      After          string
      Limit          int
      Kind           string
      EnvironmentCode string
      IncludeRetired bool
  }
  func (s *SQLStore) APIListAssets(ctx context.Context, f APIAssetFilter) ([]APIAssetRow, error)
  func (s *SQLStore) APIGetAsset(ctx context.Context, scope domain.EnvironmentScope, id string) (*APIAssetRow, error)
  func (s *SQLStore) APIListServices(ctx context.Context, scope domain.EnvironmentScope, after string, limit int) ([]APIServiceRow, error)
  func (s *SQLStore) APIGetService(ctx context.Context, scope domain.EnvironmentScope, id string) (*APIServiceRow, error)
  func (s *SQLStore) APIListAddresses(ctx context.Context, scope domain.EnvironmentScope, after string, limit int) ([]APIAddressRow, error)
  func (s *SQLStore) APIListEnvironments(ctx context.Context, scope domain.EnvironmentScope) ([]domain.Environment, error)
  ```

  Row types carry only what the DTOs need:
  ```go
  type APIAssetRow struct {
      ID, Kind, Name, Lifecycle string
      Site, Rack, Role          *string
      Environments              []string
      Addresses                 []string
      Services                  []string
  }
  type APIServiceRow struct {
      ID, Code, Name, Kind, Lifecycle string
      EnvironmentCode                 string
      Criticality                     int
      Assets                          []string
  }
  type APIAddressRow struct {
      ID, AddrText string
      AddrFamily   int
      AssetID      *string
      AssetName    *string
      Environments []string
  }
  ```

**The scope predicate is the whole point of this task.** An asset is visible iff
it is in **at least one** environment and **every** environment it is in is
within the token's scope. In portable SQL:

```sql
EXISTS (SELECT 1 FROM asset_environment ae WHERE ae.asset_id = a.id)
AND NOT EXISTS (
  SELECT 1 FROM asset_environment ae
  JOIN environment e ON e.id = ae.environment_id
  WHERE ae.asset_id = a.id AND e.code NOT IN (?, ?, ...)
)
```

The `NOT EXISTS … NOT IN` pair is `AllowsAll` expressed in SQL. It must be
applied **inside** the paginated query, never to the page after it is fetched —
filtering after paging returns short pages and eventually an empty one while
rows remain.

- [ ] **Step 1: Write the failing test**

Create `internal/store/api_test.go`. The fixture estate has `sw-core-1` and
`sw-core-2` in `{prod, dev}`, which is the case that makes `AllowsAll` differ
from `AllowsAny`:

```go
func TestABoundaryAssetIsHiddenFromAPartialScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			s := f.s
		dev := mustScope(t, "dev")
		rows, err := s.APIListAssets(context.Background(), APIAssetFilter{Scope: dev, Limit: 500})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		for _, r := range rows {
			if r.Name == "sw-core-1" {
				t.Fatal("sw-core-1 is in {prod, dev}; a {dev} token must not see it, " +
					"or its least sensitive membership decides the disclosure of a production device")
			}
		}

		both := mustScope(t, "dev", "prod")
		rows, err = s.APIListAssets(context.Background(), APIAssetFilter{Scope: both, Limit: 500})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if !containsName(rows, "sw-core-1") {
			t.Fatal("a {dev, prod} token must see the boundary device it declared")
		}
		})
	}
}

func TestAnAssetInNoEnvironmentIsVisibleToNobody(t *testing.T) {
	// "An entity in no environment is covered by nobody, which is a data gap
	// surfaced as a denial rather than an implicit allow." The API inherits
	// that rule rather than inventing a friendlier one.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			orphan := mustAsset(t, f, "orphan-box", domain.KindServer, nil) // no environments

			for _, codes := range [][]string{{"prod"}, {"dev"}, {"prod", "dev", "staging"}} {
				rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{
					Scope: mustScope(t, codes...), Limit: 500,
				})
				if err != nil {
					t.Fatalf("listing for %v: %v", codes, err)
				}
				if containsName(rows, "orphan-box") {
					t.Fatalf("scope %v saw an asset that is in no environment", codes)
				}
			}
			if _, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod"), orphan); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("got %v, want ErrNotFound for an asset in no environment", err)
			}
		})
	}
}

func TestPagesAreOrderedByIDAndDoNotRepeat(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			s := f.s
		scope := mustScope(t, "prod", "dev", "staging", "shared", "transit", "dr")
		seen := map[string]bool{}
		after := ""
		for pages := 0; pages < 50; pages++ {
			rows, err := s.APIListAssets(context.Background(),
				APIAssetFilter{Scope: scope, After: after, Limit: 3})
			if err != nil {
				t.Fatalf("page %d: %v", pages, err)
			}
			if len(rows) == 0 {
				break
			}
			for _, r := range rows {
				if seen[r.ID] {
					t.Fatalf("asset %s appeared on two pages", r.ID)
				}
				seen[r.ID] = true
			}
			for i := 1; i < len(rows); i++ {
				if rows[i-1].ID >= rows[i].ID {
					t.Fatalf("page is not ascending by id: %s >= %s", rows[i-1].ID, rows[i].ID)
				}
			}
			after = rows[len(rows)-1].ID
		}
		if len(seen) == 0 {
			t.Fatal("paged through the estate and saw nothing")
		}
		})
	}
}

func TestPageOrderDependsOnUUIDv7(t *testing.T) {
	// The single-column cursor is correct ONLY because ids are UUIDv7 and
	// therefore time-sortable as text. A future non-v7 id would break page
	// ordering silently, which is the worst way for it to break -- so pin the
	// assumption here, where the failure names the reason.
	a := uuid.Must(uuid.NewV7()).String()
	time.Sleep(2 * time.Millisecond)
	b := uuid.Must(uuid.NewV7()).String()
	if !(a < b) {
		t.Fatal("ids are no longer time-sortable as text; the API's single-column cursor " +
			"in internal/store/api.go must become a (created_at, id) pair")
	}
}

func TestRetiredAssetsAreExcludedUnlessAskedFor(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			scope := mustScope(t, "prod")
			id := mustAsset(t, f, "old-box", domain.KindServer, []string{"prod"})
			if err := f.s.RetireAsset(f.ctx, f.actor, id); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: scope, Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if containsName(rows, "old-box") {
				t.Fatal("a retired asset is kept forever and is not a target; it must be excluded by default")
			}

			rows, err = f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: scope, Limit: 500, IncludeRetired: true})
			if err != nil {
				t.Fatalf("listing with retired: %v", err)
			}
			if !containsName(rows, "old-box") {
				t.Fatal("IncludeRetired must return it; soft delete means the row is still there")
			}
		})
	}
}

func TestAServiceIsScopedByItsSingleEnvironment(t *testing.T) {
	// A service carries one environment_id, not a set, so AllowsAll over a
	// one-element slice is the same as Allows. The test exists so that a future
	// many-to-many service/environment change cannot quietly widen disclosure.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			prod, err := f.s.APIListServices(f.ctx, mustScope(t, "prod"), "", 500)
			if err != nil {
				t.Fatalf("listing for prod: %v", err)
			}
			dev, err := f.s.APIListServices(f.ctx, mustScope(t, "dev"), "", 500)
			if err != nil {
				t.Fatalf("listing for dev: %v", err)
			}
			for _, svc := range prod {
				if svc.EnvironmentCode != "prod" {
					t.Errorf("a prod-scoped read returned %s, which is in %s", svc.Code, svc.EnvironmentCode)
				}
			}
			for _, svc := range dev {
				if svc.EnvironmentCode != "dev" {
					t.Errorf("a dev-scoped read returned %s, which is in %s", svc.Code, svc.EnvironmentCode)
				}
			}
			if len(prod) == 0 || len(dev) == 0 {
				t.Fatal("the fixture estate has services in both; a zero result means the filter is wrong, not strict")
			}
		})
	}
}

func TestAnAddressInheritsItsAssetsEnvironments(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			// sw-core-1 is in {prod, dev}, so its addresses are too, and a
			// {dev} token must not see them.
			dev, err := f.s.APIListAddresses(f.ctx, mustScope(t, "dev"), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, a := range dev {
				if a.AssetName != nil && *a.AssetName == "sw-core-1" {
					t.Fatal("an address is scoped by the environments of the asset holding it; " +
						"a {dev} token reading a {prod, dev} switch's address is the same leak by another route")
				}
			}

			both, err := f.s.APIListAddresses(f.ctx, mustScope(t, "dev", "prod"), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var found bool
			for _, a := range both {
				if a.AssetName != nil && *a.AssetName == "sw-core-1" {
					found = true
				}
			}
			if !found {
				t.Fatal("a {dev, prod} token must see the boundary device's addresses")
			}
		})
	}
}

func TestAnAddressWithNoAssetIsVisibleToNobody(t *testing.T) {
	// An FHRP virtual address has fhrp_group_id and no interface_id, so it
	// reaches no asset and therefore no environment. Same rule as an asset in
	// no environment: a data gap is surfaced as a denial.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			every := mustScope(t, "prod", "dev", "staging", "shared", "transit", "dr")

			rows, err := f.s.APIListAddresses(f.ctx, every, "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, a := range rows {
				if a.AssetID == nil {
					t.Fatalf("address %s has no asset and was returned anyway", a.AddrText)
				}
			}
		})
	}
}
```

`newAPIFixture(t, e)` follows the shape of the existing `newProjectFixture` in
`internal/store/supplier_movement_test.go`: it runs `migrated(t, e)`, seeds the
fixture estate and returns a struct carrying `s *SQLStore`, `ctx context.Context`
and `actor domain.Actor`. Read that constructor before writing this one.
`mustScope(t, codes...)` wraps `domain.NewEnvironmentScope` with `t.Fatal`;
`mustAsset(t, f, name, kind, envCodes)` creates an asset and returns its id;
`containsName(rows, name)` is a two-line loop.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestABoundaryAsset -v`
Expected: FAIL — `undefined: APIListAssets`

- [ ] **Step 3: Write the implementation**

Create `internal/store/api.go` with the licence header and a package comment
stating that **every query in this file is a `SELECT`** and that adding a
mutation here is a design error, not an oversight.

Build queries with `?` placeholders and `sqlx.Rebind`, using `placeholders(n)`
for the scope `IN` list. Pattern for assets:

```sql
SELECT a.id, a.kind, a.name, a.lifecycle,
       site.name AS site, rack.name AS rack, a.manager_role AS role
FROM asset a
LEFT JOIN asset rack ON rack.id = a.parent_id AND rack.kind = 'rack'
LEFT JOIN asset site ON site.id = rack.parent_id AND site.kind = 'site'
WHERE EXISTS (SELECT 1 FROM asset_environment ae WHERE ae.asset_id = a.id)
  AND NOT EXISTS (
    SELECT 1 FROM asset_environment ae
    JOIN environment e ON e.id = ae.environment_id
    WHERE ae.asset_id = a.id AND e.code NOT IN (<scope placeholders>)
  )
  AND (? = '' OR a.id > ?)
  AND (? = '' OR a.kind = ?)
  AND (? OR a.lifecycle <> 'retired')
ORDER BY a.id
LIMIT ?
```

Resolve `Environments`, `Addresses` and `Services` for the page in **one
additional query each**, keyed by the page's asset ids via `placeholders(len(ids))` —
never one query per row. Addresses join `ip_address ip ON ip.interface_id =
i.id JOIN interface i ON i.asset_id = a.id`; an address with no interface (an
FHRP virtual address) has no asset and is therefore visible to nobody, which
matches the spec.

`APIGetAsset` is the same predicate with `a.id = ?` and no cursor. It returns
`domain.ErrNotFound` for both an unknown id and an out-of-scope one — the store
does not distinguish them, so a handler cannot accidentally leak the difference.

- [ ] **Step 4: Run the tests on both engines**

Run: `make test`
Expected: PASS on SQLite and Postgres. `go test ./internal/store/` alone is not
sufficient — with `INV_TEST_POSTGRES_DSN` unset the Postgres half is silently
skipped.

- [ ] **Step 5: Commit**

```bash
git add internal/store/api.go internal/store/api_test.go
git commit -m "WP-A2: AllowsAll as a SQL predicate, applied inside the page and not after it"
```

---

### Task 7: The API handlers and collections

**Files:**
- Create: `internal/api/api.go`
- Create: `internal/api/assets.go`
- Create: `internal/api/services.go`
- Create: `internal/api/addresses.go`
- Create: `internal/api/environments.go`

**Interfaces:**
- Consumes: `middleware.ReaderFrom(ctx)` (Task 3), the DTOs (Task 4),
  `ParsePageRequest` (Task 5), the `API*` store methods (Task 6),
  `render.JSON(w, status, v)` and `render.JSONError(w, status, message)`.
- Produces:
  ```go
  type API struct {
      Store *store.SQLStore
  }
  func New(s *store.SQLStore) *API
  func (a *API) ListAssets(w http.ResponseWriter, r *http.Request)
  func (a *API) GetAsset(w http.ResponseWriter, r *http.Request)
  func (a *API) ListServices(w http.ResponseWriter, r *http.Request)
  func (a *API) GetService(w http.ResponseWriter, r *http.Request)
  func (a *API) ListAddresses(w http.ResponseWriter, r *http.Request)
  func (a *API) ListEnvironments(w http.ResponseWriter, r *http.Request)

  // Row-to-contract conversion. One per entity, and the ONLY place a store row
  // becomes a published field.
  func assetDTO(r store.APIAssetRow) Asset
  func serviceDTO(r store.APIServiceRow) Service
  func addressDTO(r store.APIAddressRow) Address
  func environmentDTO(e domain.Environment) Environment

  // applyAssetFilters validates ?env= and ?kind= against the vocabularies.
  func applyAssetFilters(f *store.APIAssetFilter, q url.Values) error
  // nextCursor is nil on a short page, and the last row's id otherwise.
  func nextCursor[T any](rows []T, limit int, id func(T) string) *string
  func writeError(w http.ResponseWriter, err error)
  ```

- [ ] **Step 1: Write the failing test**

These are exercised end-to-end in Task 10's `internal/web/api_test.go`. For this
task, write a focused test in `internal/api/api_test.go` for the error mapping,
which is the part with its own logic:

```go
func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"a client mistake is 400", fmt.Errorf("%w: limit", ErrBadRequest), http.StatusBadRequest},
		{"an absent entity is 404", domain.ErrNotFound, http.StatusNotFound},
		{"anything else is 500", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, c.err)
			if rec.Code != c.want {
				t.Fatalf("got %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestAnInternalErrorIsNotEchoedToTheClient(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("pq: relation \"asset\" does not exist"))
	if strings.Contains(rec.Body.String(), "relation") {
		t.Fatal("a driver error must not reach the client; it names the schema")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestError -v`
Expected: FAIL — `undefined: writeError`

- [ ] **Step 3: Write the implementation**

`internal/api/api.go` holds `API`, `New`, and:

```go
// writeError maps an error to a status without letting a driver message reach
// the client. Sentinels are mapped; everything else is a 500 with a generic
// body and the real error in the server log.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBadRequest):
		render.JSONError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "bad request: "))
	case errors.Is(err, domain.ErrNotFound):
		render.JSONError(w, http.StatusNotFound, "not found")
	default:
		slog.Error("api", "error", err)
		render.JSONError(w, http.StatusInternalServerError, "internal error")
	}
}
```

Each collection handler follows one shape, so write the first and mirror it:

```go
func (a *API) ListAssets(w http.ResponseWriter, r *http.Request) {
	reader, ok := middleware.ReaderFrom(r.Context())
	if !ok {
		// Unreachable behind RequireReader, and a 500 rather than a default
		// scope: a handler that invented an empty scope would publish the
		// estate to an unauthenticated caller the day somebody mounts it
		// without the guard.
		writeError(w, errors.New("no reader in context"))
		return
	}
	page, err := ParsePageRequest(r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}
	f := store.APIAssetFilter{
		Scope: reader.Environments,
		After: page.After,
		Limit: page.Limit,
	}
	if err := applyAssetFilters(&f, r.URL.Query()); err != nil {
		writeError(w, err)
		return
	}
	rows, err := a.Store.APIListAssets(r.Context(), f)
	if err != nil {
		writeError(w, fmt.Errorf("listing assets: %w", err))
		return
	}
	out := make([]Asset, 0, len(rows))
	for _, row := range rows {
		out = append(out, assetDTO(row))
	}
	render.JSON(w, http.StatusOK, Page[Asset]{
		Data: out,
		Next: nextCursor(rows, page.Limit, func(r store.APIAssetRow) string { return r.ID }),
	})
}
```

`applyAssetFilters` validates `env` and `kind` against the vocabularies and
returns `ErrBadRequest` for an unknown value — an empty collection there would
be indistinguishable from a legitimate empty answer. `nextCursor` returns nil
when the page is short, and the last row's id otherwise.

Every slice in a DTO is built with `make([]string, 0, n)` so it marshals as `[]`
and never as `null` — a consumer that does `for host in group.hosts` must not
have to special-case a null.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -v && go vet ./internal/api/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/
git commit -m "WP-A2: the collections, and an error map that never echoes a driver message"
```

---

### Task 8: The Ansible view

**Files:**
- Create: `internal/api/ansible.go`
- Modify: `internal/api/api_test.go` (group naming and collision tests)

**Interfaces:**
- Consumes: `APIListAssets` with a large limit and `IncludeRetired: false`.
- Produces:
  ```go
  // Inventory is the assembled view. It is NOT the wire shape: MarshalJSON
  // flattens Groups to the top level beside "_meta", which is what Ansible
  // expects and what no plain struct can express.
  type Inventory struct {
      Meta   InventoryMeta
      Groups map[string]InventoryGroup
  }
  type InventoryMeta struct {
      HostVars map[string]map[string]string `json:"hostvars"`
  }
  type InventoryGroup struct {
      Hosts []string `json:"hosts"`
  }
  func (i Inventory) MarshalJSON() ([]byte, error)

  func buildAnsibleInventory(rows []store.APIAssetRow) (Inventory, error)
  func ansibleGroupName(dimension, raw string) string
  func (a *API) Ansible(w http.ResponseWriter, r *http.Request)
  ```

- [ ] **Step 1: Write the failing test**

```go
func TestGroupNamesAreSanitisedAndPrefixed(t *testing.T) {
	cases := []struct{ dimension, raw, want string }{
		{"env", "prod", "env_prod"},
		{"kind", "vm", "kind_vm"},
		{"svc", "billing-api", "svc_billing_api"},
		{"svc", "Billing API", "svc_billing_api"},
		{"site", "dc-1", "site_dc_1"},
	}
	for _, c := range cases {
		if got := ansibleGroupName(c.dimension, c.raw); got != c.want {
			t.Errorf("ansibleGroupName(%q, %q) = %q, want %q", c.dimension, c.raw, got, c.want)
		}
	}
}

func TestAServiceCannotCollideWithAnEnvironment(t *testing.T) {
	// The prefix is what makes this safe: a service literally named "prod" is
	// svc_prod, not prod, and cannot silently widen the env_prod group.
	if ansibleGroupName("svc", "prod") == ansibleGroupName("env", "prod") {
		t.Fatal("a service and an environment of the same name must not produce one group")
	}
}

func TestAGroupNameCollisionIsRefused(t *testing.T) {
	// Two services sanitising to the same name -- billing-api and billing_api --
	// would silently merge into one group and widen the target set of every
	// playbook that uses it. Refuse loudly instead.
	_, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "h1", Kind: "vm", Addresses: []string{"10.0.0.1"},
			Environments: []string{"prod"}, Services: []string{"billing-api"}},
		{ID: "b", Name: "h2", Kind: "vm", Addresses: []string{"10.0.0.2"},
			Environments: []string{"prod"}, Services: []string{"billing_api"}},
	})
	if err == nil {
		t.Fatal("two services sanitising to one group name must be refused, not merged")
	}
}

func TestOnlyAddressableKindsAreHosts(t *testing.T) {
	inv, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "vm-1", Kind: "vm", Addresses: []string{"10.0.0.1"}, Environments: []string{"prod"}},
		{ID: "b", Name: "rack-14", Kind: "rack", Environments: []string{"prod"}},
		{ID: "c", Name: "sw-1", Kind: "switch", Addresses: []string{"10.0.0.9"}, Environments: []string{"prod"}},
		{ID: "d", Name: "vm-2", Kind: "vm", Environments: []string{"prod"}}, // no address
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	hosts := inv.Meta.HostVars
	if _, ok := hosts["vm-1"]; !ok {
		t.Error("an addressable vm must be a host")
	}
	for _, absent := range []string{"rack-14", "sw-1", "vm-2"} {
		if _, ok := hosts[absent]; ok {
			t.Errorf("%s must not be an Ansible host: a rack is not connectable, "+
				"a switch is not in scope for this view, and an addressless vm has nothing to connect to", absent)
		}
	}
}

func TestAnAssetInTwoEnvironmentsIsInTwoGroups(t *testing.T) {
	inv, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "vm-1", Kind: "vm", Addresses: []string{"10.0.0.1"},
			Environments: []string{"prod", "shared"}},
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	for _, g := range []string{"env_prod", "env_shared", "kind_vm"} {
		if len(inv.Groups[g].Hosts) != 1 {
			t.Errorf("group %s must contain the host", g)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestAnsible -v && go test ./internal/api/ -run TestGroup -v`
Expected: FAIL — `undefined: buildAnsibleInventory`

- [ ] **Step 3: Write the implementation**

Create `internal/api/ansible.go`. `buildAnsibleInventory` is a pure function over
rows so it is testable without a store or a request — the handler is a thin
wrapper that fetches and calls it.

```go
// hostKinds are the kinds Ansible can actually connect to. A rack, a PDU or a
// patch panel is a real asset and not a thing with an SSH daemon; listing one
// produces an inventory that fails on first use.
var hostKinds = map[string]bool{
	domain.KindServer: true, domain.KindVM: true, domain.KindHypervisor: true,
}

// ansibleGroupName lowercases, replaces every run of non-alphanumerics with a
// single underscore, and prefixes the dimension so that a service named "prod"
// cannot widen the "prod" environment group.
func ansibleGroupName(dimension, raw string) string {
	var b strings.Builder
	b.Grow(len(dimension) + 1 + len(raw))
	b.WriteString(dimension)
	b.WriteByte('_')

	lastUnderscore := true // suppress a leading underscore after the prefix
	for _, r := range strings.ToLower(raw) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.TrimRight(b.String(), "_")
}
```

`MarshalJSON` assembles a `map[string]any` with `"_meta"` plus one key per
group, so the top level can mix a fixed key with arbitrary group names:

```go
func (i Inventory) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(i.Groups)+1)
	out["_meta"] = i.Meta
	for name, g := range i.Groups {
		if _, clash := out[name]; clash {
			return nil, fmt.Errorf("group %q collides with a reserved key", name)
		}
		out[name] = g
	}
	return json.Marshal(out)
}
```

The inventory marshals to the shape in `docs/api-design.md` §4: a `_meta.hostvars`
map and one key per group holding `{"hosts": [...]}`. Use an explicit
`MarshalJSON` or a `map[string]any` assembled at the end — the top level mixes
`_meta` with arbitrary group names, so it cannot be a plain struct.

Track group names in a `map[string]string` of sanitised name → original source
string; a second source producing an already-seen name with a different original
is the collision, and returns an error naming both.

Host and group lists are sorted before marshalling, so two calls against an
unchanged estate produce byte-identical output and a golden file is stable.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/ansible.go internal/api/api_test.go
git commit -m "WP-A2: the Ansible view, where a merged group would silently widen a playbook"
```

---

### Task 9: Mount the routes

**Files:**
- Modify: `internal/web/routes.go`
- Modify: `internal/web/web_test.go` (`newHarnessWithReaders`, plus the two
  request helpers below — the harness currently has only `get(path, htmx)` and
  `post(path, form, htmx)`, neither of which can set an `Authorization` header)

**Harness additions required by this task** (`internal/web/web_test.go`):

```go
// request builds a request against the harness server without sending it, so a
// test can set an Authorization header. The existing get/post helpers cannot.
func (h *harness) request(method, path string, body io.Reader) *http.Request {
	h.t.Helper()
	req, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	return req
}

// do sends a request built by request.
func (h *harness) do(req *http.Request) *http.Response {
	h.t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("sending request: %v", err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// apiResponse is a read response with its body already drained, so two of them
// can be compared byte for byte.
type apiResponse struct {
	StatusCode int
	Header     http.Header
	Body       string
}

// apiGet performs an authenticated read as the named credential.
func (h *harness) apiGet(t *testing.T, path, token string) apiResponse {
	t.Helper()
	req := h.request("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := h.do(req)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return apiResponse{StatusCode: resp.StatusCode, Header: resp.Header, Body: string(b)}
}
```

Reader fixtures, mirroring the existing `testAgentCredentials()`:

```go
const (
	readerAllID    = "ansible"
	readerAllToken = "test-reader-all"
	readerDevID    = "dev-only"
	readerDevToken = "test-reader-dev"
)

// testReaderCredentials can read the whole fixture estate. There is no
// wildcard, so "everything" is spelled out -- which is the point.
func testReaderCredentials() []config.ReaderCredential {
	return []config.ReaderCredential{
		{ID: readerAllID, Token: readerAllToken,
			Environments: []string{"prod", "dev", "staging", "shared", "transit", "dr"}},
	}
}

// devOnlyReaderCredentials is the partial scope the boundary-device tests need.
func devOnlyReaderCredentials() []config.ReaderCredential {
	return []config.ReaderCredential{
		{ID: readerDevID, Token: readerDevToken, Environments: []string{"dev"}},
	}
}
```

**Interfaces:**
- Consumes: everything above.
- Produces:
  ```go
  // APIPrefix is the one prefix the read surface occupies.
  const APIPrefix = "/api/v1"
  // ReaderSurface mirrors AgentSurface.
  type ReaderSurface struct {
      Registry      *auth.ReaderRegistry
      API           *api.API
      SessionCookie string
  }
  func (s *ReaderSurface) enabled() bool
  // Routes gains a *ReaderSurface parameter.
  func Routes(app *handlers.App, static fs.FS, authz *auth.Authorizer,
      agents *AgentSurface, readers *ReaderSurface) http.Handler
  ```

- [ ] **Step 1: Write the failing test**

In a new `internal/web/api_test.go`:

```go
func TestTheAPIIsNotMountedWithoutACredential(t *testing.T) {
	h := newHarness(t) // no readers configured
	if code := h.get("/api/v1/assets", false).StatusCode; code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: an estate with no integrations must not carry the surface", code)
	}
}

func TestTheAPIRefusesABrowserSession(t *testing.T) {
	// A read route that also accepted a session would let an operator's browser
	// credentials satisfy a machine surface, which is the confusion rule 6
	// refuses outright rather than resolving in either direction.
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	h.login("viewer", "viewer-password") // establishes the session cookie on h.client's jar

	req := h.request("GET", "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer "+readerAllToken)
	if code := h.do(req).StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 for a request carrying both a session and a token", code)
	}
}

func TestAnAgentTokenIsRefusedByTheAPI(t *testing.T) {
	h := newHarnessWithReaders(t, testAgentCredentials(), testReaderCredentials())
	req := h.request("GET", "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer "+agentProdToken) // a real monitoring credential
	if code := h.do(req).StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a monitoring credential must not read the inventory", code)
	}
}

func TestAnAPITokenIsRefusedByObservations(t *testing.T) {
	h := newHarnessWithReaders(t, testAgentCredentials(), testReaderCredentials())
	req := h.request("POST", "/observations", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+readerAllToken)
	if code := h.do(req).StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a read credential must not write an observation", code)
	}
}

func TestNoAPIRouteIsAWriteRoute(t *testing.T) {
	// Walk the registered patterns rather than trusting the diff. WP-A2 says
	// "No write routes" and this is what makes the sentence true a year from
	// now.
	for _, pattern := range registeredPatterns(t) {
		if !strings.Contains(pattern, APIPrefix) {
			continue
		}
		if !strings.HasPrefix(pattern, "GET ") {
			t.Errorf("%q is not a GET; the read surface has no write routes, ever", pattern)
		}
	}
}

func TestOnlyObservationsIsCSRFExempt(t *testing.T) {
	// routes.go builds the exemption with middleware.ExactPath specifically so
	// that /api/v1 cannot inherit it. Assert the list, not the intent.
	exempt := csrfExemptPaths(t)
	if len(exempt) != 1 || exempt[0] != ObservationsPath {
		t.Fatalf("csrf exemptions are %v; only %s may ever be exempt", exempt, ObservationsPath)
	}
}
```

`registeredPatterns` and `csrfExemptPaths` need the router to expose what it
registered. Add a package-level `var registeredAPIPatterns []string` appended to
by the mounting loop, and return the `csrfExempt` slice from a small helper —
both unexported, both test-only readers of state the router already has.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestTheAPI -v`
Expected: FAIL — `newHarnessWithReaders` undefined.

- [ ] **Step 3: Write the implementation**

In `routes.go`, beside the agent block and **after** it, so the reading order
matches the risk order:

```go
	// The read surface (WP-A2). Mounted only when a credential is configured,
	// exactly as the observations route is: an estate with no integrations
	// should not be carrying it.
	//
	// Note what is NOT here: no entry in csrfExempt. Every route is a GET and
	// nosurf ignores safe methods, so the exemption above stays a list of one.
	if readers.enabled() {
		guard := middleware.ReaderGuard{
			Registry:        readers.Registry,
			Credentials:     middleware.NewRateLimiter(middleware.ReaderRequestsPerSecond, middleware.ReaderBurst),
			Unauthenticated: middleware.NewRateLimiter(middleware.UnauthenticatedPerSecond, middleware.UnauthenticatedBurst),
			SessionCookie:   readers.SessionCookie,
		}
		read := middleware.RequireReader(guard)
		api := func(pattern string, h http.HandlerFunc) {
			full := "GET " + APIPrefix + pattern
			mux.Handle(full, read(h))
		}
		api("/assets", readers.API.ListAssets)
		api("/assets/{id}", readers.API.GetAsset)
		api("/services", readers.API.ListServices)
		api("/services/{id}", readers.API.GetService)
		api("/addresses", readers.API.ListAddresses)
		api("/environments", readers.API.ListEnvironments)
		api("/ansible", readers.API.Ansible)
	}
```

The `api` helper hard-codes `"GET "`, which is what makes
`TestNoAPIRouteIsAWriteRoute` true by construction rather than by review.

Update `cmd/invctl` to build the `ReaderSurface` from `cfg.Readers` and pass it
to `Routes`. Update `newHarnessWith`/`newHarnessSecure` in `web_test.go` to take
reader credentials and thread them through; keep `newHarness(t)` meaning "no
readers" so every existing test is unchanged.

- [ ] **Step 4: Run the whole suite on both engines**

Run: `make test`
Expected: PASS. Every pre-existing test must still pass — if `newHarness`
changed behaviour for existing callers, that is a bug in this task.

- [ ] **Step 5: Commit**

```bash
git add internal/web/routes.go internal/web/web_test.go internal/web/api_test.go cmd/invctl
git commit -m "WP-A2: mount the read surface, and prove it cannot inherit the CSRF exemption"
```

---

### Task 10: End-to-end behaviour and golden shapes

**Files:**
- Modify: `internal/web/api_test.go`
- Create: `internal/web/testdata/api/asset.json`, `service.json`, `address.json`, `environment.json`, `ansible.json`

- [ ] **Step 1: Write the failing tests**

```go
func TestAnOutOfScopeAssetIsIndistinguishableFromAnAbsentOne(t *testing.T) {
	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())

	outOfScope := h.refs.Assets["sw-core-1"] // in {prod, dev}, so a {dev} token must miss it
	fabricated := uuid.Must(uuid.NewV7()).String()

	a := h.apiGet(t, "/api/v1/assets/"+outOfScope, readerDevToken)
	b := h.apiGet(t, "/api/v1/assets/"+fabricated, readerDevToken)

	if a.StatusCode != http.StatusNotFound || b.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d and %d, want 404 and 404", a.StatusCode, b.StatusCode)
	}
	if a.Body != b.Body {
		t.Fatalf("the two 404s differ (%q vs %q); a difference is an existence oracle "+
			"that lets a dev token enumerate which ids name real production assets", a.Body, b.Body)
	}
}

func TestAScopeMissIsLoggedEvenThoughItCannotBeInTheResponse(t *testing.T) {
	// The 404 above is deliberately unhelpful to the client, so the operator's
	// log is the only place a misconfigured INV_API_SCOPES is visible. Without
	// this line the decision above becomes undebuggable in production.
	// The server logs from its own goroutines, so the sink must be safe to
	// write and read concurrently -- use the existing syncBuffer, not a plain
	// bytes.Buffer, or this test races under -race. Same pattern as
	// TestAccessLogRecordsTheUser.
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())
	h.apiGet(t, "/api/v1/assets/"+h.refs.Assets["sw-core-1"], readerDevToken)

	logged := buf.String()
	if !strings.Contains(logged, auth.EventReaderScopeDenied) {
		t.Fatalf("a scope miss must be logged as %s; got %q", auth.EventReaderScopeDenied, logged)
	}
	if !strings.Contains(logged, readerDevID) {
		t.Fatalf("the log line must name the credential so an operator can fix the scope; got %q", logged)
	}
	if strings.Contains(logged, readerDevToken) {
		t.Fatal("the log line must never carry the token")
	}
}

func TestTheAssetShapeIsTheGoldenShape(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/assets?limit=1", readerAllToken)
	assertGoldenJSON(t, "testdata/api/asset.json", got.Body)
}

func TestEveryResponseIsNoStore(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	for _, path := range []string{
		"/api/v1/assets", "/api/v1/services", "/api/v1/addresses",
		"/api/v1/environments", "/api/v1/ansible",
	} {
		got := h.apiGet(t, path, readerAllToken)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("%s: got %d, want 200", path, got.StatusCode)
		}
		if cc := got.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: got Cache-Control %q, want no-store -- a cached inventory "+
				"outlives the scope that produced it", path, cc)
		}
	}
}

func TestAMalformedCursorReturns400OverHTTP(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/assets?after=not-a-cursor", readerAllToken)
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: a client paginating with a corrupted cursor must be told, "+
			"not silently restarted at page one forever", got.StatusCode)
	}
}

func TestAnUnknownKindReturns400(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	for _, q := range []string{"?kind=banana", "?env=nowhere"} {
		got := h.apiGet(t, "/api/v1/assets"+q, readerAllToken)
		if got.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400 -- an empty collection here is indistinguishable "+
				"from a legitimate empty answer", q, got.StatusCode)
		}
	}
}

func TestAScopedFilterIsEmptyRatherThanRefused(t *testing.T) {
	// ?env=prod on a {dev} token is not an error: the token's scope is not the
	// client's business, and a filter reveals nothing the caller does not
	// already know about itself.
	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())
	got := h.apiGet(t, "/api/v1/assets?env=prod", readerDevToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", got.StatusCode)
	}
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(got.Body), &page); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(page.Data) != 0 {
		t.Fatalf("got %d assets, want 0", len(page.Data))
	}
	if !strings.Contains(got.Body, `"data":[]`) {
		t.Error("an empty collection must marshal as [] and never as null")
	}
}

func TestPagingTheWholeEstateTerminates(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	seen := map[string]bool{}
	path := "/api/v1/assets?limit=2"
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("paging did not terminate; the cursor is not advancing")
		}
		got := h.apiGet(t, path, readerAllToken)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("page %d: got %d", pages, got.StatusCode)
		}
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Next *string `json:"next"`
		}
		if err := json.Unmarshal([]byte(got.Body), &page); err != nil {
			t.Fatalf("decoding page %d: %v", pages, err)
		}
		for _, a := range page.Data {
			if seen[a.ID] {
				t.Fatalf("asset %s appeared twice while paging", a.ID)
			}
			seen[a.ID] = true
		}
		if page.Next == nil {
			break
		}
		path = "/api/v1/assets?limit=2&after=" + *page.Next
	}
	if len(seen) == 0 {
		t.Fatal("paged the whole estate and saw nothing")
	}
}

func TestTheAnsibleViewIsTheGoldenShape(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/ansible", readerAllToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", got.StatusCode)
	}
	assertGoldenJSON(t, "testdata/api/ansible.json", got.Body)
}
```

`assertGoldenJSON` compares indented JSON and, on mismatch, prints a unified
diff and the command to regenerate (`go test ./internal/web/ -run TestThe -update`).
Add an `-update` flag guarded by `flag.Bool`, as is conventional.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/web/ -run TestAnOutOfScope -v`
Expected: FAIL

- [ ] **Step 3: Make them pass**

Fix whatever they catch. Generate the golden files with `-update` **only after**
reading each one and confirming by eye that it contains no money, no personal
data, no observed state and no field nobody decided to publish. A golden file
generated without being read is a rubber stamp on whatever leaked.

- [ ] **Step 4: Run the whole suite on both engines**

Run: `make test && make lint`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add internal/web/api_test.go internal/web/testdata/api/
git commit -m "WP-A2: the shapes, pinned, and the 404 that cannot be told from an absence"
```

---

### Task 11: Documentation and the roadmap marker

**Files:**
- Create: `docs/API.md`
- Modify: `CHANGELOG.md`
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Write `docs/API.md`**

Consumer documentation in the register of `docs/IMPORT.md` — the reference
somebody reads while wiring an integration. Cover: the two environment
variables with a worked example; that there is no wildcard; the collections and
their parameters; the cursor and how to page; the DTO shapes; the Ansible view
and how to use it as a dynamic inventory script; the status codes.

It must contain this section, because it is the thing that will otherwise
generate a support question:

> **Why can my token not see a device that is obviously in its environment?**
> A token sees an asset only if it is scoped to **every** environment that asset
> is in. `sw-core-1` sits in both `prod` and `dev`, so a `dev`-only token does
> not see it — otherwise the least sensitive environment an asset belongs to
> would decide who may read a production-facing device. Scope the credential to
> both: `INV_API_SCOPES=ansible:prod|dev`. A refused read is logged server-side
> as `reader_scope_denied` with the credential id.

- [ ] **Step 2: Add the CHANGELOG entry**

Under a new unreleased heading, in the **Added** section. It earns its place by
the operator having to set two variables to use it. Note in **Action required**
that the surface is absent until `INV_API_TOKENS` is set — that is the answer to
"I upgraded and `/api/v1` 404s".

- [ ] **Step 3: Move the roadmap marker**

In `docs/ROADMAP.md`, change the `WP-A2` entry to `**DONE**` in the same style as
its neighbours, and add a line to the parity checklist row for the REST API.

Do this **last**, and only with `make test` green on both engines — the file's
own header records that these markers had "simply never been kept" and that a
whole pass was needed to repair them.

- [ ] **Step 4: Verify the invariant guards still hold over the new package**

The estate guard in `internal/estate/guard_test.go` scans the tree, so the new
`internal/api` package comes under it automatically — but confirm rather than
assume, because this work package adds a whole directory:

Run: `go test ./internal/estate/ -v`
Expected: PASS. `TestNothingReachesOutOfThisProcess` must still pass with an
**unchanged** `dialAllowlist`. If it fails, something in `internal/api` acquired
an outbound HTTP capability, which is invariant 9 and not a thing to allowlist
away — WP-A2 is inbound only.

- [ ] **Step 5: Verify the whole thing**

Run: `make lint && make test`
Expected: clean and green on both engines.

- [ ] **Step 6: Commit**

```bash
git add docs/API.md CHANGELOG.md docs/ROADMAP.md
git commit -m "WP-A2: document the read surface and mark the work package done"
```

---

## Definition of done

From `CLAUDE.md`, with the items this work package genuinely touches:

- [ ] Queries use `?` placeholders and run on both engines
- [ ] No forbidden Postgres-only feature introduced
- [ ] No mutation of declared state anywhere in this package, therefore no
      `change_log` obligation — verified by there being no `INSERT`/`UPDATE`/
      `DELETE` in any file created here
- [ ] No observed-state column reaches the published contract
- [ ] Non-GET route: **none exists**, and a test proves it
- [ ] Table-driven tests added; `make test` green on both engines
- [ ] `gofmt`, `go vet`, `staticcheck` clean
- [ ] No new dependency
- [ ] Licence header on every new source file, blank line before `package`
