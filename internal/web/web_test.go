// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"

	"github.com/madalinignisca/invctl/internal/api"
	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/config"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web"
	"github.com/madalinignisca/invctl/internal/web/handlers"
	"github.com/madalinignisca/invctl/internal/web/render"
	webassets "github.com/madalinignisca/invctl/web"
)

// These tests drive the real router over real HTTP against a real database.
// The things worth protecting here -- that a reader cannot write, that a form
// error is a 422 and not a 200, that HTMX gets a fragment and a browser gets a
// page -- are all properties of the whole stack, and a handler tested in
// isolation would demonstrate none of them.

type harness struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	store  *store.SQLStore
	refs   *seed.Refs
}

// webTestFoldKey is a fixed 32-byte key attached to every store this package
// builds, the same way internal/store's own suite uses testFoldKey -- not a
// secret, never leaves this process. Without it, the first custom field
// value any handler test sets would fail with an error (foldDigest refuses
// to fold under an empty key: internal/store/customvalues.go).
var webTestFoldKey = bytes.Repeat([]byte{0x5a}, 32)

// Monitoring credentials the harness configures (docs/AUDIT.md rule 6). They
// are here rather than in the agent tests because every test in this package
// runs against a router that has the machine-facing route mounted -- otherwise
// "an agent token reaches nothing else" would be proved against a server where
// agents reach nothing at all.
//
// Tokens are long enough to satisfy config.MinAgentTokenLength; they are test
// fixtures and nothing else in this repository accepts them.
const (
	agentProdID    = "mon-prod"
	agentProdToken = "prod-token-000000000000000000000000"
	agentDevID     = "mon-dev"
	agentDevToken  = "dev-token-1111111111111111111111111"
	// A reporter that speaks Alertmanager rather than the canonical four, so
	// the per-reporter mapping in rule 13 is exercised by a real vocabulary
	// rather than by the identity one.
	agentPromID    = "mon-prom"
	agentPromToken = "prom-token-22222222222222222222222"
)

// testAgentCredentials is the fixture deployment's rule 6 configuration: prod
// and transit for one collector, the out-of-scope development zone for another.
func testAgentCredentials() []config.AgentCredential {
	return []config.AgentCredential{
		{ID: agentProdID, Token: agentProdToken, Environments: []string{"prod", "transit"}},
		{ID: agentDevID, Token: agentDevToken, Environments: []string{"dev"}},
		{ID: agentPromID, Token: agentPromToken, Environments: []string{"prod"}, Vocabulary: "prometheus"},
	}
}

// Read-only API credentials the harness can configure (WP-A2). Tokens are
// padded to satisfy config.MinAgentTokenLength, which Task 1 enforces for
// read credentials too -- a shorter fixture token would refuse to start
// rather than exercise the guard.
const (
	readerAllID    = "ansible"
	readerAllToken = "reader-all-token-000000000000000000"
	readerDevID    = "dev-only"
	// Padded on purpose to exercise config.MinAgentTokenLength.
	//nolint:gosec // G101: test fixture credential, not a real secret
	readerDevToken = "reader-dev-token-111111111111111111"
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

// newHarness builds the fixture deployment, with the machine-facing route
// mounted and no reader credentials configured. Every test in this package
// runs against it, so "an agent token reaches nothing else" is proved
// against a server where the route exists, and newHarness(t) keeps meaning
// "no readers" so every pre-existing test in this package is unaffected by
// this task.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, testAgentCredentials())
}

// newSecureHarness is the same application declaring itself to be behind TLS,
// which is the only thing that turns HSTS on.
func newSecureHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessSecure(t, testAgentCredentials(), nil, true)
}

// newHarnessWithoutAgents builds the same deployment with no monitoring
// credentials configured, which mounts no machine-facing route and registers no
// CSRF exemption.
func newHarnessWithoutAgents(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, nil)
}

func newHarnessWith(t *testing.T, creds []config.AgentCredential) *harness {
	t.Helper()
	return newHarnessSecure(t, creds, nil, false)
}

// newHarnessWithReaders builds the fixture deployment with both the
// machine-facing route and the read-only API mounted.
func newHarnessWithReaders(t *testing.T, agentCreds []config.AgentCredential, readerCreds []config.ReaderCredential) *harness {
	t.Helper()
	return newHarnessSecure(t, agentCreds, readerCreds, false)
}

var (
	webTemplateOnce sync.Once
	webTemplatePath string
	webTemplateRefs *seed.Refs
	webTemplateErr  error
)

// webTemplate returns a migrated, seeded, user-populated database file that has
// been CLOSED, and the refs from that one seeding.
//
// BUILT ONCE PER PROCESS AND COPIED, NOT REBUILT PER TEST. This package builds
// 243 harnesses and each one was replaying forty migrations and the whole seed.
// Measured per harness: 296ms migrating, 126ms seeding, 45ms hashing the two
// passwords -- 475ms of setup before a single request, or 115s of the package's
// 164s. Under the race detector on a four-core runner that package did not
// finish inside an hour, and the release gate's own comment says a timeout that
// needs raising is WP-I3 asking to be done rather than a number to increase.
//
// The same trick internal/store already uses, and sound for the same reason
// plus one more: the template is seeded ONCE, so the ids it contains are fixed,
// which is what lets every copy share a single Refs. Re-seeding per harness
// would generate fresh UUIDs and no shared map could name them.
//
// SAFE TO SHARE because nothing in this package sets t.Parallel and no test
// writes to refs -- both checked, and both worth re-checking before adding one.
// Every harness still gets a private file that nobody else writes to; what
// changed is how the file comes to exist.
//
// CLOSED BEFORE IT IS COPIED, so the WAL is checkpointed back into the main
// file. Copying an open WAL database is the torn read this project has been
// bitten by before, and it would be no less torn here.
//
// The clock is the one thing a shared template changes: seed timestamps are
// fixed at the moment the template is built rather than at each harness. The
// drift is bounded by how long the package takes, which is under two minutes,
// and no test here asserts on the seed's own timestamps at finer granularity
// than that -- the health tests insert their own rows with their own times.
func webTemplate(t *testing.T) (string, *seed.Refs) {
	t.Helper()
	webTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "invctl-web-template")
		if err != nil {
			webTemplateErr = err
			return
		}
		path := filepath.Join(dir, "template.db")
		db, err := store.Open(store.DriverSQLite, "file:"+path)
		if err != nil {
			webTemplateErr = err
			return
		}
		ctx := context.Background()
		if err := store.Migrate(ctx, db); err != nil {
			_ = db.Close()
			webTemplateErr = fmt.Errorf("migrating: %w", err)
			return
		}
		st := store.New(db).WithFoldKey(webTestFoldKey)
		refs, err := seed.Load(ctx, st)
		if err != nil {
			_ = db.Close()
			webTemplateErr = fmt.Errorf("seeding: %w", err)
			return
		}

		// Two accounts: one with write access, one without. The read-only user
		// is the whole point of having an authorization model at all. Baked
		// into the template because argon2id is deliberately expensive -- 22ms
		// a hash here, which is 11s across the package when done per harness.
		for _, u := range []struct{ name, password string }{
			{"admin", "admin-password"},
			{"viewer", "viewer-password"},
		} {
			hash, err := auth.HashPassword(u.password)
			if err != nil {
				_ = db.Close()
				webTemplateErr = fmt.Errorf("hashing password: %w", err)
				return
			}
			user, err := domain.NewAppUser(store.NewID(), u.name, domain.UserSourceLocal, st.Now())
			if err != nil {
				_ = db.Close()
				webTemplateErr = fmt.Errorf("building user: %w", err)
				return
			}
			user.PasswordHash = &hash
			if err := st.CreateUser(ctx, domain.SystemActor, user); err != nil {
				_ = db.Close()
				webTemplateErr = fmt.Errorf("creating user: %w", err)
				return
			}
		}

		// WP-A4's own custom fields are DELIBERATELY NOT staged into this
		// shared template. Every other test in this package assumes an
		// estate that starts with none defined -- TestTheSectionIsAbsent-
		// WhenNoFieldIsDefined pins that directly -- and this template is
		// copied for every one of them. seed.StageCustomFields is exercised
		// against its own isolated database by
		// TestTheSeedFixtureCoversEveryCustomFieldKind in internal/seed.
		if err := db.Close(); err != nil {
			webTemplateErr = fmt.Errorf("closing the template: %w", err)
			return
		}
		webTemplatePath, webTemplateRefs = path, refs
	})
	if webTemplateErr != nil {
		t.Fatalf("building the web template: %v", webTemplateErr)
	}
	return webTemplatePath, webTemplateRefs
}

func newHarnessSecure(t *testing.T, creds []config.AgentCredential, readerCreds []config.ReaderCredential, secure bool) *harness {
	t.Helper()

	template, refs := webTemplate(t)

	dsn := filepath.Join(t.TempDir(), "web.db")
	data, err := os.ReadFile(template)
	if err != nil {
		t.Fatalf("reading the web template: %v", err)
	}
	if err := os.WriteFile(dsn, data, 0o600); err != nil {
		t.Fatalf("writing the test database: %v", err)
	}
	db, err := store.Open(store.DriverSQLite, "file:"+dsn)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db).WithFoldKey(webTestFoldKey)

	sessions := scs.New()
	// A CLEANUP GOROUTINE PER HARNESS, AND THIS PACKAGE BUILDS 243 OF THEM.
	// sqlite3store.New starts one that runs for the life of the PROCESS, not
	// the test; scs's own documentation names creating a store in a test as
	// exactly the case where that matters. Interval 0 never starts it, and a
	// test living for seconds has no sessions to expire.
	//
	// Measured: 495 goroutines still alive when the last test in this package
	// finished, 243 of them this one, each waking every five minutes to DELETE
	// from a database the test had already closed. That is where CI's "sql:
	// database is closed" lines came from.
	sessions.Store = sqlite3store.NewWithCleanupInterval(db.SQLDB(), 0)
	// THE OTHER 243 ARE scs.New's OWN memstore, AND THEY STAY. It installs one
	// before we can say otherwise, and MemStore.StopCleanup cannot remove it:
	// startCleanup assigns m.stopCleanup INSIDE the goroutine, so calling it
	// straight after New finds nil and silently does nothing, and calling it
	// later is a data race the detector reports (verified both ways -- the
	// delayed call does stop the goroutine and does fail under -race).
	//
	// So they are left alone deliberately. They tick once a minute over an
	// empty map with no I/O, which is a fraction of what the sqlite3 ones cost.
	// Do not "fix" this with a sleep or a retry; fix it upstream.
	sessions.Cookie.Secure = false
	// Named as production names it, because RequireAgent refuses a request
	// carrying this cookie by name and a test against a different name would
	// prove nothing about the deployment.
	sessions.Cookie.Name = "invctl_session"

	renderer, err := render.New(webassets.FS, false, "EUR")
	if err != nil {
		t.Fatalf("parsing templates: %v", err)
	}

	cfg := &config.Config{AdminUsers: []string{"admin"}, AuthLocal: true, SecureCookies: secure}
	authz := auth.NewAuthorizer(cfg.AdminUsers)

	app := &handlers.App{
		Store:    st,
		Render:   renderer,
		Sessions: sessions,
		Auth:     auth.NewChain(st, auth.NewLocalAuthenticator(st)),
		Authz:    authz,
		Config:   cfg,
	}

	var agents *web.AgentSurface
	if len(creds) > 0 {
		registry, err := auth.NewAgentRegistry(creds)
		if err != nil {
			t.Fatalf("building agent registry: %v", err)
		}
		agents = &web.AgentSurface{
			Registry:      registry,
			Handler:       handlers.NewObservationAPI(store.NewObservedRecorder(st)),
			SessionCookie: sessions.Cookie.Name,
		}
		app.Agents = registry
	}

	var readers *web.ReaderSurface
	if len(readerCreds) > 0 {
		registry, err := auth.NewReaderRegistry(readerCreds)
		if err != nil {
			t.Fatalf("building reader registry: %v", err)
		}
		readers = &web.ReaderSurface{
			Registry:      registry,
			API:           api.New(st),
			SessionCookie: sessions.Cookie.Name,
		}
	}

	server := httptest.NewServer(web.Routes(app, staticFS(t), authz, agents, readers))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("building cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		// Redirects are part of what is under test, so they are not followed.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &harness{t: t, server: server, client: client, store: st, refs: refs}
}

// staticFS serves the real embedded assets, so a template referencing a
// missing file would be visible here rather than only in a browser.
func staticFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(webassets.FS, "static")
	if err != nil {
		t.Fatalf("opening embedded static assets: %v", err)
	}
	return sub
}

func (h *harness) url(path string) string { return h.server.URL + path }

// get issues a GET, optionally as an HTMX request.
func (h *harness) get(path string, htmx bool) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.url(path), nil)
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// request builds a request against the harness server without sending it, so
// a test can set an Authorization header. get and post cannot: neither
// exposes the *http.Request before it is sent.
func (h *harness) request(method, path string, reqBody io.Reader) *http.Request {
	h.t.Helper()
	req, err := http.NewRequest(method, h.url(path), reqBody)
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	return req
}

// do sends a request built by request.
func (h *harness) do(req *http.Request) *http.Response {
	h.t.Helper()
	// req is always built by h.request via h.url, which prepends
	// h.server.URL: the harness's own httptest server. Nothing upstream of
	// this call ever supplies a caller- or request-influenced URL.
	//nolint:gosec // G704: fixed base URL; the harness's own httptest server
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("sending request: %v", err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// apiResponse is a read response with its body already drained, so two of
// them can be compared byte for byte without either test racing the other's
// Close.
type apiResponse struct {
	StatusCode int
	Header     http.Header
	Body       string
}

// apiGet performs an authenticated read as the named credential.
func (h *harness) apiGet(t *testing.T, path, token string) apiResponse {
	t.Helper()
	req := h.request(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := h.do(req)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return apiResponse{StatusCode: resp.StatusCode, Header: resp.Header, Body: string(b)}
}

var csrfPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// csrfToken fetches a page and extracts the token from it.
func (h *harness) csrfToken(path string) string {
	h.t.Helper()
	resp := h.get(path, false)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("reading body: %v", err)
	}
	match := csrfPattern.FindSubmatch(body)
	if match == nil {
		h.t.Fatalf("no CSRF token on %s", path)
	}
	// The token is base64, so it can contain "+", which html/template escapes
	// to "&#43;" in the attribute. A browser un-escapes it while parsing; this
	// harness has to do the same or roughly one login in two fails.
	return html.UnescapeString(string(match[1]))
}

// post issues a form POST. Origin is set because nosurf requires one of
// Sec-Fetch-Site, Origin or Referer on every unsafe request -- a browser
// always sends one.
func (h *harness) post(path string, form url.Values, htmx bool) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.url(path), strings.NewReader(form.Encode()))
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.server.URL)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// login signs in and asserts it worked.
func (h *harness) login(username, password string) {
	h.t.Helper()
	token := h.csrfToken("/login")
	resp := h.post("/login", url.Values{
		"csrf_token": {token},
		"username":   {username},
		"password":   {password},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("login as %s returned %d, want 303", username, resp.StatusCode)
	}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------

// TestAnonymousIsRedirectedToLogin: every page except the public ones is
// behind authentication.
func TestAnonymousIsRedirectedToLogin(t *testing.T) {
	h := newHarness(t)

	protected := []string{
		"/", "/assets", "/services", "/environments", "/prefixes", "/network",
		"/search", "/changes", "/reports/spanning",
	}
	for _, path := range protected {
		resp := h.get(path, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s as anonymous returned %d, want 303 to the login page", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Errorf("GET %s redirected to %q, want the login page", path, loc)
		}
	}
}

// TestPublicEndpoints stay reachable without a session.
func TestPublicEndpoints(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/healthz", "/login"} {
		resp := h.get(path, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned %d, want 200", path, resp.StatusCode)
		}
	}
}

// TestHTMXRequestGetsARedirectHeader: HTMX will not follow a 302 the way a
// browser does, so an expired session has to come back as HX-Redirect or the
// login page ends up swapped into a table cell.
func TestHTMXRequestGetsARedirectHeader(t *testing.T) {
	h := newHarness(t)

	resp := h.get("/assets", true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if loc := resp.Header.Get("HX-Redirect"); !strings.HasPrefix(loc, "/login") {
		t.Errorf("HX-Redirect = %q, want the login page", loc)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	h := newHarness(t)
	token := h.csrfToken("/login")

	cases := []struct{ name, username, password string }{
		{"wrong password", "admin", "not-the-password"},
		{"unknown user", "nobody", "any-password"},
		{"empty password", "admin", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.post("/login", url.Values{
				"csrf_token": {token},
				"username":   {tc.username},
				"password":   {tc.password},
			}, false)
			text := body(t, resp)

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
			// The message must not distinguish an unknown user from a wrong
			// password: that difference is a username oracle.
			if !strings.Contains(text, "was not recognised") {
				t.Errorf("body does not carry the generic failure message")
			}
			if strings.Contains(strings.ToLower(text), "no such user") {
				t.Errorf("the response distinguishes an unknown username")
			}
		})
	}
}

// TestCSRFIsEnforced: a POST without a valid token is refused, whatever the
// session says.
func TestCSRFIsEnforced(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	cases := []struct {
		name  string
		token string
	}{
		{name: "missing token", token: ""},
		{name: "garbage token", token: "not-a-real-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.post("/environments", url.Values{
				"csrf_token": {tc.token},
				"code":       {"csrf-test"},
				"name":       {"CSRF test"},
				"role":       {domain.EnvRoleDev},
			}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}

	// And the environment was not created.
	if _, err := h.store.GetEnvironmentByCode(context.Background(), "csrf-test"); err == nil {
		t.Error("an environment was created despite the CSRF failure")
	}
}

// TestReadOnlyUserCannotWrite is the authorization model, end to end.
func TestReadOnlyUserCannotWrite(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	t.Run("can read", func(t *testing.T) {
		for _, path := range []string{"/", "/assets", "/services"} {
			resp := h.get(path, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s as viewer returned %d, want 200", path, resp.StatusCode)
			}
		}
	})

	t.Run("cannot write", func(t *testing.T) {
		token := h.csrfToken("/environments")
		resp := h.post("/environments", url.Values{
			"csrf_token": {token},
			"code":       {"sneaky"},
			"name":       {"Sneaky"},
			"role":       {domain.EnvRoleDev},
		}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("the write did not happen", func(t *testing.T) {
		if _, err := h.store.GetEnvironmentByCode(context.Background(), "sneaky"); err == nil {
			t.Error("a read-only user created an environment")
		}
	})

	t.Run("write controls are not rendered", func(t *testing.T) {
		text := body(t, h.get("/environments", false))
		if strings.Contains(text, `action="/environments"`) {
			t.Error("the create form is rendered for a read-only user")
		}
		if !strings.Contains(text, "read only") {
			t.Error("the page does not tell the user they are read-only")
		}
	})
}

// TestValidationFailureIs422 covers the rule from CLAUDE.md: a validation
// failure re-renders the form partial with error state, never a 200 with the
// message buried in the body.
func TestValidationFailureIs422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/environments")

	// Two failures, and since migration 00004 they are caught in two different
	// places. A missing required field never leaves the constructor. An
	// unrecognised role is well-formed, so the constructor passes it and the
	// store rejects it against the environment_role lookup table -- which is
	// the point of the table, and it must not degrade the response: the
	// operator still gets 422, the form partial, and the field named. If this
	// ever comes back as a bare "that request was not valid", the FK is
	// enforcing the vocabulary but nothing is explaining it.
	cases := []struct {
		name   string
		form   url.Values
		expect string
	}{
		{
			name:   "a missing required field",
			form:   url.Values{"code": {""}, "name": {"No code"}, "role": {"production"}},
			expect: "is required",
		},
		{
			name:   "a role that is not in the lookup table",
			form:   url.Values{"code": {"nowhere"}, "name": {"Nowhere"}, "role": {"not-a-real-role"}},
			expect: "must be one of",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"csrf_token": {token}}
			for k, v := range tc.form {
				form[k] = v
			}
			resp := h.post("/environments", form, true)
			text := body(t, resp)

			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", resp.StatusCode)
			}
			// The form comes back with its error state, ready to resubmit.
			if !strings.Contains(text, `id="environment-form"`) {
				t.Error("the response is not the form partial")
			}
			if !strings.Contains(text, tc.expect) {
				t.Errorf("the response does not show %q", tc.expect)
			}
			// A partial, not a whole page swapped into a div.
			if strings.Contains(text, "<!doctype html>") {
				t.Error("an HTMX request received a full page")
			}
		})
	}
}

// TestHTMXBranching: the same URL serves a fragment to HTMX and a full page to
// a browser, which is what keeps every route deep-linkable.
func TestHTMXBranching(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	full := body(t, h.get("/assets", false))
	if !strings.Contains(full, "<!doctype html>") {
		t.Error("a browser navigation did not receive a full page")
	}
	if !strings.Contains(full, `id="asset-table"`) {
		t.Error("the full page does not contain the table")
	}

	fragment := body(t, h.get("/assets", true))
	if strings.Contains(fragment, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}
	if !strings.Contains(fragment, `id="asset-table"`) {
		t.Error("the fragment is not the table")
	}
}

// TestSuccessfulMutationRedirects: after a successful write, HTMX is told to
// navigate rather than swap.
func TestSuccessfulMutationRedirects(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/environments")

	resp := h.post("/environments", url.Values{
		"csrf_token":  {token},
		"code":        {"qa"},
		"name":        {"QA"},
		"role":        {domain.EnvRoleStaging},
		"criticality": {"3"},
	}, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 with an HX-Redirect", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got != "/environments" {
		t.Errorf("HX-Redirect = %q, want /environments", got)
	}

	env, err := h.store.GetEnvironmentByCode(context.Background(), "qa")
	if err != nil {
		t.Fatalf("the environment was not created: %v", err)
	}
	if env.Role != domain.EnvRoleStaging {
		t.Errorf("role = %s, want staging", env.Role)
	}
}

// TestDependencyValidationRoundTrip drives the form that carries the most
// business rules: nature, tolerance and the exclusive provider.
func TestDependencyValidationRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["orders-web"]
	path := "/services/" + serviceID
	token := h.csrfToken(path)

	t.Run("async without a tolerance is rejected", func(t *testing.T) {
		resp := h.post(path+"/dependencies", url.Values{
			"csrf_token":           {token},
			"provider_endpoint_id": {h.refs.Endpoints["rabbitmq/amqp"]},
			"nature":               {domain.NatureAsync},
			"failure_mode":         {"Events buffer"},
		}, true)
		text := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", resp.StatusCode)
		}
		if !strings.Contains(text, "required for an async dependency") {
			t.Errorf("the response does not explain the tolerance requirement")
		}
	})

	t.Run("no provider is rejected", func(t *testing.T) {
		resp := h.post(path+"/dependencies", url.Values{
			"csrf_token":   {token},
			"nature":       {domain.NatureHard},
			"failure_mode": {"Everything stops"},
		}, true)
		text := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", resp.StatusCode)
		}
		if !strings.Contains(text, "provider endpoint or route is required") {
			t.Errorf("the response does not explain the missing provider")
		}
	})

	t.Run("a valid edge is recorded", func(t *testing.T) {
		resp := h.post(path+"/dependencies", url.Values{
			"csrf_token":           {token},
			"provider_endpoint_id": {h.refs.Endpoints["rabbitmq/amqp"]},
			"nature":               {domain.NatureAsync},
			"tolerance_seconds":    {"120"},
			"failure_mode":         {"Events buffer for two minutes"},
			"data_class":           {"pii", "telemetry"},
		}, true)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}

		ctx := context.Background()
		deps, err := h.store.ListUpstream(ctx, serviceID)
		if err != nil {
			t.Fatalf("listing upstream: %v", err)
		}
		var found bool
		for _, d := range deps {
			if d.Nature == domain.NatureAsync {
				found = true
				if d.ToleranceSeconds == nil || *d.ToleranceSeconds != 120 {
					t.Errorf("tolerance = %v, want 120", d.ToleranceSeconds)
				}
				classes, err := h.store.DataClassesFor(ctx, []string{d.ID})
				if err != nil {
					t.Fatalf("loading data classes: %v", err)
				}
				if len(classes[d.ID]) != 2 {
					t.Errorf("data classes = %v, want two", classes[d.ID])
				}
			}
		}
		if !found {
			t.Error("the dependency was not recorded")
		}
	})
}

// TestImpactPageRendersTheDemoNarrative walks the case the whole tool exists
// for, through the UI rather than through the engine's API.
func TestImpactPageRendersTheDemoNarrative(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	text := body(t, h.get("/assets/"+assetID+"/impact?window=180", false))

	// Both replicas of orders-api live on this hypervisor.
	if !strings.Contains(text, "orders-api") {
		t.Error("orders-api is not reported as affected")
	}
	// The proxy is elsewhere and healthy, but the route it fronts is not.
	if !strings.Contains(text, "partner-gateway") {
		t.Error("the route consumer is not reported; route-as-node is not working through the UI")
	}
	// Quorum survives, so Vault must not appear as affected.
	if strings.Contains(text, "HashiCorp Vault") {
		t.Error("vault is reported as affected despite surviving quorum")
	}
	// And the capacity arithmetic is shown, not just the verdict.
	if !strings.Contains(text, "lost") {
		t.Error("the page does not show the capacity arithmetic")
	}
}

// TestImpactWindowChangesTheAnswer proves the control is wired to the engine.
func TestImpactWindowChangesTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["vm-queue-1"]

	short := body(t, h.get("/assets/"+assetID+"/impact?window=180", true))
	long := body(t, h.get("/assets/"+assetID+"/impact?window=2700", true))

	// The fixture's async edge tolerates 300 seconds.
	if strings.Contains(short, "orders-api") {
		t.Error("a 3-minute outage degraded a consumer that buffers for 5 minutes")
	}
	if !strings.Contains(long, "orders-api") {
		t.Error("a 45-minute outage did not degrade a consumer that buffers for 5 minutes")
	}
}

func TestUnknownAssetIs404(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.get("/assets/00000000-0000-0000-0000-000000000000", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSecurityHeaders: cheap defences that should never silently disappear.
func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/login", false)
	resp.Body.Close()

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for header, value := range want {
		if got := resp.Header.Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q, want a self-only script-src", csp)
	}
	// The CSP-safe Alpine build is vendored precisely so this is not needed.
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows unsafe-eval: %q", csp)
	}
}

// TestLogoutEndsTheSession.
func TestLogoutEndsTheSession(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.get("/", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed-in request returned %d", resp.StatusCode)
	}

	token := h.csrfToken("/")
	logout := h.post("/logout", url.Values{"csrf_token": {token}}, false)
	logout.Body.Close()

	after := h.get("/", false)
	after.Body.Close()
	if after.StatusCode != http.StatusSeeOther {
		t.Errorf("status after sign-out = %d, want a redirect to the login page", after.StatusCode)
	}
}

// TestOpenRedirectIsRefused: /login?next=... must not be able to send someone
// to another site after they authenticate.
func TestOpenRedirectIsRefused(t *testing.T) {
	hostile := []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		"/\\evil.example",
		"https:/evil.example",
	}
	for _, next := range hostile {
		t.Run(next, func(t *testing.T) {
			// A fresh harness per case: after a successful sign-in, /login
			// redirects away and there is no form to read a token from.
			h := newHarness(t)
			token := h.csrfToken("/login")
			resp := h.post("/login", url.Values{
				"csrf_token": {token},
				"username":   {"admin"},
				"password":   {"admin-password"},
				"next":       {next},
			}, false)
			resp.Body.Close()

			if loc := resp.Header.Get("Location"); loc != "/" {
				t.Errorf("next=%q redirected to %q, want /", next, loc)
			}
		})
	}

	t.Run("a legitimate internal path is honoured", func(t *testing.T) {
		h := newHarness(t)
		token := h.csrfToken("/login")
		resp := h.post("/login", url.Values{
			"csrf_token": {token},
			"username":   {"admin"},
			"password":   {"admin-password"},
			"next":       {"/services"},
		}, false)
		resp.Body.Close()

		if loc := resp.Header.Get("Location"); loc != "/services" {
			t.Errorf("Location = %q, want /services", loc)
		}
	})
}

// TestSearchThroughTheUI closes the loop on M5.
func TestSearchThroughTheUI(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	cases := []struct{ query, want string }{
		{"10.20.30.11", "vm-vault-1"},
		{"aa:bb:cc:00:10:01", "hv-01"},
		{"FCH2033V0YR", "hv-01"},
		{"5432", "PostgreSQL"},
	}
	for _, tc := range cases {
		text := body(t, h.get("/search?q="+url.QueryEscape(tc.query), true))
		if !strings.Contains(text, tc.want) {
			t.Errorf("searching for %q did not surface %q", tc.query, tc.want)
		}
	}
}

// TestStaticAssetsAreServed. A stylesheet that 404s does not break a page --
// it renders unstyled, which is easy to miss in a test that only checks status
// codes and easy to ship. So the assets are asserted directly, including their
// content type, because a CSS file served as text/plain is refused by the
// browser just as firmly as a missing one.
func TestStaticAssetsAreServed(t *testing.T) {
	h := newHarness(t)

	assets := []struct{ path, wantType string }{
		{"/static/app.css", "text/css"},
		{"/static/htmx.min.js", "javascript"},
		{"/static/alpine.min.js", "javascript"},
		{"/static/app.js", "javascript"},
	}
	for _, a := range assets {
		resp := h.get(a.path, false)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned %d, want 200", a.path, resp.StatusCode)
			continue
		}
		if len(body) == 0 {
			t.Errorf("GET %s returned an empty body", a.path)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, a.wantType) {
			t.Errorf("GET %s content type = %q, want it to contain %q", a.path, ct, a.wantType)
		}
	}
}

// TestEveryPageTemplateRenders walks each page a signed-in user can reach.
// A template error surfaces as a 500, and a page nobody visits in a test is a
// page that breaks in the demo.
func TestEveryPageTemplateRenders(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	serviceID := h.refs.Services["orders-api"]
	// hv-01 hosts no service instance, so its Workloads table is empty and every
	// field inside {{range .Instances}} goes unevaluated -- which meant no test
	// in this suite had ever rendered that block. Removing a field it referenced
	// would have left the suite green while /assets/{any VM that runs something}
	// returned 500, for exactly the page an operator opens during an incident.
	// vm-app-1 runs two orders-api containers.
	hostingAssetID := h.refs.Assets["vm-app-1"]

	pages := []string{
		"/",
		"/assets",
		"/assets/" + assetID,
		"/assets/" + assetID + "/impact",
		"/assets/" + hostingAssetID,
		"/assets/" + hostingAssetID + "/impact",
		"/services",
		"/services/" + serviceID,
		"/environments",
		"/reports/spanning",
		"/changes",
		"/search?q=vault",
		"/network",
	}
	for _, path := range pages {
		resp := h.get(path, false)
		text := body(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned %d", path, resp.StatusCode)
			continue
		}
		if !strings.Contains(text, "</html>") {
			t.Errorf("GET %s did not render a complete page", path)
		}
		// html/template writes this when a field does not exist on the data.
		if strings.Contains(text, "<no value>") {
			t.Errorf("GET %s rendered <no value>; a template references a missing field", path)
		}
	}
}

// TestVerifyKeepsThePanelItCameFrom. The two dependency panels render the same
// row partial in opposite orientations. The verify request has to say which
// one it came from, or the swapped-in row names the wrong service and the
// operator sees a link to the provider where the consumer should be.
func TestVerifyKeepsThePanelItCameFrom(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// pgsql-core has upstream deps (vault) and downstream consumers
	// (orders-api, sso), so both panels are populated.
	serviceID := h.refs.Services["pgsql-core"]
	page := body(t, h.get("/services/"+serviceID, false))

	// The rendered buttons must carry a direction, and both values must appear.
	for _, want := range []string{"direction=upstream", "direction=downstream"} {
		if !strings.Contains(page, want) {
			t.Errorf("the service page has no verify button carrying %s", want)
		}
	}

	// And the handler must honour it: verifying a downstream row comes back
	// shaped as a downstream row, naming the consumer.
	deps, err := h.store.ListDownstream(context.Background(), serviceID)
	if err != nil {
		t.Fatalf("listing downstream: %v", err)
	}
	if len(deps) == 0 {
		t.Fatal("fixture has no downstream dependencies on pgsql-core")
	}
	dep := deps[0]

	token := h.csrfToken("/services/" + serviceID)
	fragment := body(t, h.post("/dependencies/"+dep.ID+"/verify?direction=downstream",
		url.Values{"csrf_token": {token}}, true))
	if !strings.Contains(fragment, dep.ConsumerCode) {
		t.Errorf("downstream verify returned a row without the consumer %q: %s", dep.ConsumerCode, fragment)
	}
	if !strings.Contains(fragment, "/services/"+dep.ConsumerServiceID) {
		t.Errorf("downstream verify linked somewhere other than the consumer service")
	}
}

// TestFlashSurvivesAValidationFailure. A flash is consumed exactly once, and
// the layout renders it from the *page* data. The 422 path builds the embedded
// form context before the page context, so popping the session on every call
// meant the form swallowed a pending message and the page rendered without it.
//
// Reaching that path takes a queued flash followed by a failed submission with
// no GET in between -- which is exactly what happens to someone submitting a
// bad form right after a successful one with JavaScript disabled.
func TestFlashSurvivesAValidationFailure(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/environments")

	// Queue a flash, and deliberately do not follow the redirect: the message
	// is still sitting in the session.
	created := h.post("/environments", url.Values{
		"csrf_token":  {token},
		"code":        {"qa"},
		"name":        {"QA"},
		"role":        {domain.EnvRoleStaging},
		"criticality": {"3"},
	}, false)
	created.Body.Close()
	if created.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: create returned %d, want 303", created.StatusCode)
	}

	// Now fail validation on the same session, as a plain browser POST.
	page := body(t, h.post("/environments", url.Values{
		"csrf_token": {token},
		"code":       {""},
		"name":       {"No code"},
		"role":       {domain.EnvRoleDev},
	}, false))

	if !strings.Contains(page, "Environment qa created.") {
		t.Error("the pending flash was consumed by the form context and never rendered")
	}
	if !strings.Contains(page, "is required") {
		t.Error("the validation error is missing")
	}
}

// TestFlashIsShownExactlyOnce.
func TestFlashIsShownExactlyOnce(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/environments")

	resp := h.post("/environments", url.Values{
		"csrf_token":  {token},
		"code":        {"qa"},
		"name":        {"QA"},
		"role":        {domain.EnvRoleStaging},
		"criticality": {"3"},
	}, false)
	resp.Body.Close()

	if first := body(t, h.get("/environments", false)); !strings.Contains(first, "Environment qa created.") {
		t.Error("the flash was not shown")
	}
	if second := body(t, h.get("/environments", false)); strings.Contains(second, "Environment qa created.") {
		t.Error("the flash was shown twice")
	}
}

// TestAccessLogRecordsTheUser. Authenticate attaches the user to a *derived*
// context, so any middleware wrapping it sees only the original. With the
// logger on the outside every line read user=- -- including authenticated
// writes, which are the lines the log exists for.
func TestAccessLogRecordsTheUser(t *testing.T) {
	// The server logs from its own goroutines, so the sink has to be safe to
	// write and read concurrently.
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	h := newHarness(t)
	h.login("admin", "admin-password")

	buf.Reset()
	resp := h.get("/assets", false)
	resp.Body.Close()

	// WAITED FOR, NOT READ IMMEDIATELY. The access line is written by the
	// server's handler goroutine after the response is flushed, so the client
	// can have its answer before the line exists -- a race that is invisible on
	// an idle machine and cost a release on a loaded CI runner, where the
	// buffer came back completely empty.
	//
	// A bounded wait rather than a sleep: it returns as soon as the line lands,
	// so the ordinary case stays instant and only a genuine absence takes the
	// full second.
	logged := ""
	for i := 0; i < 100; i++ {
		logged = buf.String()
		if strings.Contains(logged, "path=/assets") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logged, "path=/assets") {
		t.Fatalf("the request was not logged after waiting: %s", logged)
	}
	if strings.Contains(logged, "user=-") {
		t.Errorf("an authenticated request logged user=-: %s", logged)
	}
	if !strings.Contains(logged, "user=admin") {
		t.Errorf("the access log does not name the signed-in user: %s", logged)
	}
}

// TestLoginRedirectEscapesTheNextPath. r.URL.Path is decoded, so pasting it
// straight into a query string lets a path containing & or = split into extra
// parameters and send the user somewhere else after signing in.
func TestLoginRedirectEscapesTheNextPath(t *testing.T) {
	h := newHarness(t)

	// A path containing characters that are significant in a query string.
	resp := h.get("/assets/a%26b%3Dc", false)
	resp.Body.Close()

	location := resp.Header.Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parsing redirect %q: %v", location, err)
	}
	next := parsed.Query().Get("next")
	if next != "/assets/a&b=c" {
		t.Errorf("next = %q, want the whole path %q (redirect was %q)",
			next, "/assets/a&b=c", location)
	}
	if len(parsed.Query()) != 1 {
		t.Errorf("the redirect grew extra query parameters: %v", parsed.Query())
	}
}

// syncBuffer is a concurrency-safe log sink. httptest serves each request on
// its own goroutine, so an unguarded bytes.Buffer races with the assertions.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// TestAuditTrailAlwaysShowsWhoAndWhatKind. `change_log.actor` is free text and
// a machine writes into the same column, so a name alone does not establish
// that a person did something. Three of the four views rendered the name
// without the kind -- including the entity detail pages, which are what an
// incident review actually opens.
func TestAuditTrailAlwaysShowsWhoAndWhatKind(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	serviceID := h.refs.Services["orders-api"]

	// Every page that renders the change log. The fixture is seeded by a
	// 'system' actor, so the kind is present to find.
	for _, path := range []string{
		"/",                      // dashboard recent-changes panel
		"/changes",               // the global log
		"/assets/" + assetID,     // asset history
		"/services/" + serviceID, // service history
	} {
		page := body(t, h.get(path, false))
		if !strings.Contains(page, "seed") {
			t.Errorf("GET %s does not render the actor at all", path)
			continue
		}
		if !strings.Contains(page, ">system<") {
			t.Errorf("GET %s renders an actor without its actor_kind; "+
				"a machine's entry is indistinguishable from a person's", path)
		}
	}
}

// TestChangeLogRendersAResolvedDisplayName. change_log.actor holds an
// app_user.id, never a username (docs/DECISIONS.md, 2026-07-28 decisions);
// the UI must still show a human-readable name, resolved by the store.
func TestChangeLogRendersAResolvedDisplayName(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/prefixes")

	resp := h.post("/prefixes", url.Values{
		"csrf_token": {token},
		"cidr_text":  {"10.88.0.0/24"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating the prefix returned %d, want 303", resp.StatusCode)
	}

	admin, err := h.store.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("loading the admin user: %v", err)
	}
	changes, err := h.store.ListRecentChanges(context.Background(), 5)
	if err != nil {
		t.Fatalf("listing recent changes: %v", err)
	}
	if len(changes) == 0 || changes[0].Actor != admin.ID {
		t.Fatalf("most recent change actor = %+v, want the admin user's opaque id %q", changes, admin.ID)
	}

	// The store-level TestChangeLogActorIsAnOpaqueID already proves the raw
	// column holds the id and that resolution degrades gracefully; this only
	// needs to prove the template actually renders the resolved name rather
	// than the id it was handed. (The page legitimately contains the raw id
	// elsewhere -- it is the entity id in the app_user creation entry's own
	// snapshot -- so this does not assert its absence.)
	page := body(t, h.get("/changes", false))
	if !strings.Contains(page, ">admin<") {
		t.Error("the change log does not resolve the signed-in user's id to their username")
	}
}

// ---------------------------------------------------------------------------
// M1: network topology data entry -- interfaces, links, addresses, prefixes.

// TestReadOnlyUserCannotWriteNetworkTopology extends the authorization model
// check to the M1 write routes: a route that exists only for the seeder until
// now is a real product gap, and it must be behind the same RBAC as everything
// else the moment it gets a handler.
func TestReadOnlyUserCannotWriteNetworkTopology(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	assetID := h.refs.Assets["hv-01"]
	cases := []struct {
		name string
		path string
	}{
		{"add interface", "/assets/" + assetID + "/interfaces"},
		{"assign address", "/addresses"},
		{"patch cable", "/links"},
		{"declare prefix", "/prefixes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := h.csrfToken("/assets/" + assetID)
			resp := h.post(tc.path, url.Values{"csrf_token": {token}}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s as viewer returned %d, want 403", tc.path, resp.StatusCode)
			}
		})
	}
}

// TestCSRFIsEnforcedOnNetworkRoutes: the CSRF middleware wraps the whole mux,
// but a route added without a form referencing the shared token is exactly
// the kind of gap that goes unnoticed until it ships.
func TestCSRFIsEnforcedOnNetworkRoutes(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/prefixes", url.Values{
		"csrf_token": {"not-a-real-token"},
		"cidr_text":  {"10.99.0.0/24"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if _, err := h.store.ResolveAddress(context.Background(), "10.99.0.1"); err == nil {
		t.Error("a prefix was declared despite the CSRF failure")
	}
}

// TestInterfaceFormValidationIs422 exercises SetMAC's error path through the
// handler: a garbled MAC must re-render the form with error state at 422, not
// silently drop the field or return 200.
func TestInterfaceFormValidationIs422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	path := "/assets/" + assetID
	token := h.csrfToken(path)

	resp := h.post(path+"/interfaces", url.Values{
		"csrf_token":  {token},
		"name":        {"eth9"},
		"form_factor": {domain.FFRJ45},
		"mac":         {"not-a-mac"},
	}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(text, `id="interface-form"`) {
		t.Error("the response is not the interface form partial")
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}
}

// TestUnpatchingACableRemovesItAsAFarEnd drives the full soft-delete path for
// a cable through the UI: retiring a link must not leave it visible as
// anyone's far end, on either side, and re-patching an already-patched port
// must be rejected with a 422.
func TestUnpatchingACableRemovesItAsAFarEnd(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	assetA := h.refs.Assets["hv-01"]
	assetB := h.refs.Assets["hv-02"]
	if assetA == "" || assetB == "" {
		t.Skip("fixture does not have the two hypervisors this test patches")
	}

	// Fresh, unpatched interfaces, so this test does not depend on which
	// fixture ports happen to be cabled already.
	token := h.csrfToken("/assets/" + assetA)
	create := h.post("/assets/"+assetA+"/interfaces", url.Values{
		"csrf_token":  {token},
		"name":        {"test-patch-a"},
		"form_factor": {domain.FFRJ45},
	}, false)
	create.Body.Close()
	if create.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating interface on A returned %d", create.StatusCode)
	}
	token = h.csrfToken("/assets/" + assetB)
	create = h.post("/assets/"+assetB+"/interfaces", url.Values{
		"csrf_token":  {token},
		"name":        {"test-patch-b"},
		"form_factor": {domain.FFRJ45},
	}, false)
	create.Body.Close()
	if create.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating interface on B returned %d", create.StatusCode)
	}

	ifaceA := interfaceIDByName(t, h, assetA, "test-patch-a")
	ifaceB := interfaceIDByName(t, h, assetB, "test-patch-b")

	token = h.csrfToken("/assets/" + assetA)
	patch := h.post("/links", url.Values{
		"csrf_token":          {token},
		"asset_id":            {assetA},
		"a_interface_id":      {ifaceA},
		"target_interface_id": {ifaceB},
	}, false)
	patch.Body.Close()
	if patch.StatusCode != http.StatusSeeOther {
		t.Fatalf("patching the cable returned %d", patch.StatusCode)
	}

	// The peer cell renders "<asset> · <port>"; this is the marker to look
	// for, rather than the bare port name, which also appears unconditionally
	// in the "patch to" dropdown's option list once the port is available
	// again -- a plain substring check would pass even with a live bug.
	assetBName := assetName(t, h, assetB)
	peerMarker := assetBName + " · test-patch-b"

	page := body(t, h.get("/assets/"+assetA, false))
	if !strings.Contains(page, peerMarker) {
		t.Fatal("the patched cable is not shown on the asset page")
	}

	t.Run("patching either end again is rejected", func(t *testing.T) {
		token := h.csrfToken("/assets/" + assetA)
		resp := h.post("/links", url.Values{
			"csrf_token":          {token},
			"asset_id":            {assetA},
			"a_interface_id":      {ifaceA},
			"target_interface_id": {ifaceB},
		}, true)
		text := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", resp.StatusCode)
		}
		if !strings.Contains(text, "already patched") {
			t.Error("the response does not explain that the port is already patched")
		}
	})

	rows, err := h.store.ListInterfaces(ctx, assetA)
	if err != nil {
		t.Fatalf("listing A's interfaces: %v", err)
	}
	var linkID string
	for _, r := range rows {
		if r.Name == "test-patch-a" {
			linkID = r.LinkID
		}
	}
	if linkID == "" {
		t.Fatal("the new cable has no link id")
	}

	token = h.csrfToken("/assets/" + assetA)
	unpatch := h.post("/links/"+linkID+"/retire", url.Values{
		"csrf_token": {token},
		"asset_id":   {assetA},
	}, false)
	unpatch.Body.Close()
	if unpatch.StatusCode != http.StatusSeeOther {
		t.Fatalf("unpatching returned %d, want 303", unpatch.StatusCode)
	}

	assetAName := assetName(t, h, assetA)
	reverseMarker := assetAName + " · test-patch-a"

	after := body(t, h.get("/assets/"+assetA, false))
	if strings.Contains(after, peerMarker) {
		t.Error("the retired cable still shows on A's page as the far end")
	}
	afterB := body(t, h.get("/assets/"+assetB, false))
	if strings.Contains(afterB, reverseMarker) {
		t.Error("the retired cable still shows on B's page as the far end")
	}
}

func assetName(t *testing.T, h *harness, assetID string) string {
	t.Helper()
	a, err := h.store.GetAsset(context.Background(), assetID)
	if err != nil {
		t.Fatalf("getting asset %s: %v", assetID, err)
	}
	return a.Name
}

func interfaceIDByName(t *testing.T, h *harness, assetID, name string) string {
	t.Helper()
	rows, err := h.store.ListInterfaces(context.Background(), assetID)
	if err != nil {
		t.Fatalf("listing interfaces of %s: %v", assetID, err)
	}
	for _, r := range rows {
		if r.Name == name {
			return r.ID
		}
	}
	t.Fatalf("no interface named %s on asset %s", name, assetID)
	return ""
}

// TestPrefixesPageHTMXBranching: the prefixes list follows the same
// full-page-vs-fragment rule as every other page in the tool.
func TestPrefixesPageHTMXBranching(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	full := body(t, h.get("/prefixes", false))
	if !strings.Contains(full, "<!doctype html>") {
		t.Error("a browser navigation did not receive a full page")
	}

	token := h.csrfToken("/prefixes")
	resp := h.post("/prefixes", url.Values{
		"csrf_token": {token},
		"cidr_text":  {"10.77.0.0/24"},
	}, true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 with an HX-Redirect", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got != "/prefixes" {
		t.Errorf("HX-Redirect = %q, want /prefixes", got)
	}

	if _, err := h.store.ResolveAddress(context.Background(), "10.77.0.1"); err != nil {
		t.Errorf("the prefix was not created: %v", err)
	}
}

// TestSearchRanksTheExactNameFirst, through the real router and the seeded
// estate -- where `hv-01` competes with the bridge `hv-01-br0` hanging off it.
//
// This is the shape a viewer actually hits: they type a hostname, and the first
// link had better be the host. It is asserted at this level as well as in the
// store because the ordering only matters if it survives rendering.
func TestSearchRanksTheExactNameFirst(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/search?q=hv-01", false))

	first := firstAssetLink(t, page)
	if first != h.refs.Assets["hv-01"] {
		t.Errorf("the first result is not hv-01; a bridge named after the host outranked it")
	}
	// The bridges are still there -- ranking orders, it does not filter.
	if !strings.Contains(page, "hv-01-br0") {
		t.Error("the near matches were dropped rather than ranked")
	}
}

// firstAssetLink returns the id in the first /assets/ link on a page.
func firstAssetLink(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`href="/assets/([0-9a-f-]{30,})"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no asset link on the page")
	}
	return m[1]
}

// TestVocabularyValidationRerendersTheForm.
//
// From a review. The store's two required-field checks returned a plain wrapped
// ErrInvalid rather than a *ValidationError, so validationErrors() did not
// recognise them and the handler fell through to a bare
// "That request was not valid." — 422 with no form, no field highlighted and
// whatever was typed lost. The template's per-field hooks were unreachable.
//
// The HTML `required` attributes are client-side only, so this is one curl away.
func TestVocabularyValidationRerendersTheForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/vocabularies", url.Values{
		"csrf_token": {h.csrfToken("/vocabularies?table=cost_kind")},
		"table":      {"cost_kind"},
		"code":       {"rent"},
		"label":      {""}, // the field the store rejects
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a term with no label returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(page, `class="field-error"`) {
		t.Error("the response is not the re-rendered form: no field error on the page")
	}
	if !strings.Contains(page, "rent") {
		t.Error("the re-rendered form lost the code that was typed")
	}
}

// HSTS follows the deployment's own statement that it is behind TLS.
//
// Sent unconditionally it would be a lie from a development box and, worse, a
// year-long self-inflicted outage on any host that later has to serve plain
// HTTP. INV_SECURE_COOKIES already means "this is behind TLS", so it is the
// same switch rather than a second one that can disagree with the first.
func TestHSTSFollowsTheTLSDeclaration(t *testing.T) {
	t.Run("absent when the deployment is not declared HTTPS", func(t *testing.T) {
		h := newHarness(t)
		resp := h.get("/login", false)
		resp.Body.Close()
		if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("HSTS sent over plain HTTP: %q", got)
		}
	})

	t.Run("present when it is", func(t *testing.T) {
		h := newSecureHarness(t)
		resp := h.get("/login", false)
		resp.Body.Close()
		got := resp.Header.Get("Strict-Transport-Security")
		if !strings.Contains(got, "max-age=31536000") {
			t.Errorf("HSTS = %q, want a year", got)
		}
		if !strings.Contains(got, "includeSubDomains") {
			t.Errorf("HSTS = %q, want includeSubDomains", got)
		}
	})

	t.Run("a feature this app never uses is denied to anything injected", func(t *testing.T) {
		h := newHarness(t)
		resp := h.get("/login", false)
		resp.Body.Close()
		if got := resp.Header.Get("Permissions-Policy"); !strings.Contains(got, "camera=()") {
			t.Errorf("Permissions-Policy = %q", got)
		}
	})
}

// railGroupsMarkedCurrent returns the rail groups this page tells the browser
// it is in, read out of the rendered markup.
//
// Through the real router deliberately. The rail's group is chosen by a handler
// from a slug, resolved by NavFor, and rendered by the layout -- three places,
// and the bug this guards lived in the seam between the first two. A unit test
// on any one of them would have passed throughout.
var railGroupPattern = regexp.MustCompile(`data-group="([^"]+)"\s+data-open="[^"]*"\s+data-current="([^"]*)"`)

func railGroupsMarkedCurrent(t *testing.T, page string) []string {
	t.Helper()
	matches := railGroupPattern.FindAllStringSubmatch(page, -1)
	if len(matches) == 0 {
		t.Fatal("no rail groups in the rendered page; this assertion would be vacuous")
	}
	var open []string
	for _, m := range matches {
		if m[2] == "true" {
			open = append(open, m[1])
		}
	}
	return open
}

// TestTheRailOpensTheSectionTheLinkWasClickedIn.
//
// THE BUG: the rail's Firewalls and Switches entries live under Network and
// point at /assets with a kind filter. The asset list passed the plain "assets"
// slug whatever the filter was, so it matched the Assets entry -- which is
// under ESTATE. Clicking Firewalls under Network expanded Estate, and because
// the rail then persisted that as a preference, Estate stayed expanded on every
// page afterwards.
//
// Both directions are checked. Asserting only that Network opens would still
// pass with a handler that opened everything.
func TestTheRailOpensTheSectionTheLinkWasClickedIn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	for _, tc := range []struct {
		path, want string
	}{
		{"/assets", "Estate"},
		{"/assets?kind=firewall", "Network"},
		{"/assets?kind=switch", "Network"},
		// A kind with no rail entry of its own is reached by the filter box on
		// the asset page, so it is still Estate. Guards the fallback.
		{"/assets?kind=server", "Estate"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			open := railGroupsMarkedCurrent(t, body(t, h.get(tc.path, false)))
			if len(open) != 1 || open[0] != tc.want {
				t.Errorf("%s marks %v as current, want exactly [%s]", tc.path, open, tc.want)
			}
		})
	}
}

// TestTheRailNeverPersistsWhereYouAre. The other half of the same bug, and the
// half that made it stick: "you are here" is a fact about one render, while a
// collapsed group is a preference. The rail wrote the first into the store for
// the second, so visiting a page left its group expanded for good.
//
// Asserted against the script because there is nothing else to assert it
// against -- and the shape of the mistake is a call, which is visible.
func TestTheRailNeverPersistsWhereYouAre(t *testing.T) {
	src, err := fs.ReadFile(webassets.FS, "static/app.js")
	if err != nil {
		t.Fatalf("reading app.js: %v", err)
	}
	// Anchored on the component, because app.js holds more than one thing with
	// a toggle() and cutting on the first one read a different component
	// entirely -- which passed, having found no dataset.current in it.
	_, component, found := strings.Cut(string(src), "Alpine.data('navGroup'")
	if !found {
		t.Fatal("app.js has no navGroup component; this test no longer reads what it thinks")
	}
	init, _, found := strings.Cut(component, "toggle()")
	if !found {
		t.Fatal("navGroup has no toggle; this test no longer reads what it thinks")
	}
	_, current, found := strings.Cut(init, "dataset.current")
	if !found {
		t.Fatal("app.js no longer branches on dataset.current; this test asserts nothing")
	}
	if strings.Contains(current, "this.remember()") {
		t.Error("navGroup.init remembers the group the server marked current, so one " +
			"visit to a page expands its rail section on every page afterwards")
	}
}

// exec runs a statement against the harness database, for the handful of states
// that cannot be produced through the application -- a job orphaned by a
// process that died, for instance.
func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	writer := h.store.DB().Writer
	if _, err := writer.Exec(writer.Rebind(query), args...); err != nil {
		h.t.Fatalf("exec (%s): %v", query, err)
	}
}
