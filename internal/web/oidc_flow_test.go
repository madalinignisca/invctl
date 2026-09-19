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
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/config"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web"
	"github.com/madalinignisca/invctl/internal/web/handlers"
)

// These tests exercise /auth/oidc and /auth/oidc/callback against a REAL
// fake issuer, the same argument internal/auth/oidc_test.go's own comment
// makes: a mock of go-oidc would only prove a mock can be written. The
// difference from that suite is the layer -- these drive the real router
// (state stored in and read back from a real session, a real cookie jar) so
// that "replaying a callback finds nothing" and "the session id changes on
// sign-in" are properties of the whole stack, not of Exchange alone.

// fakeOIDCIssuer is a smaller copy of internal/auth's own fakeIssuer,
// necessarily duplicated rather than imported: it is unexported inside
// package auth, and these tests need to drive it from package web_test.
type fakeOIDCIssuer struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
	issuer string

	mu      sync.Mutex
	codes   map[string]string
	codeSeq int
}

func newFakeOIDCIssuer(t *testing.T) *fakeOIDCIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the issuer's signing key: %v", err)
	}
	f := &fakeOIDCIssuer{key: key, keyID: "test-signing-key", codes: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.serveDiscovery)
	mux.HandleFunc("/keys", f.serveKeys)
	mux.HandleFunc("/token", f.serveToken)
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		// Never hit: OIDCStart's 302 to this URL is read by the test client
		// (redirects are not followed -- see the harness's CheckRedirect),
		// which extracts state and nonce from the Location header and mints
		// its own code with issueCode, standing in for what a browser
		// signing in at Keycloak would produce.
		http.Error(w, "not used by this test suite", http.StatusNotImplemented)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.issuer = f.srv.URL
	return f
}

func (f *fakeOIDCIssuer) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                f.issuer,
		"authorization_endpoint":                f.issuer + "/auth",
		"token_endpoint":                        f.issuer + "/token",
		"jwks_uri":                              f.issuer + "/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
	})
}

func (f *fakeOIDCIssuer) serveKeys(w http.ResponseWriter, _ *http.Request) {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: f.key.Public(), KeyID: f.keyID, Algorithm: "RS256", Use: "sig"},
	}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

// serveToken never checks the PKCE verifier or the client secret -- that
// belongs to Keycloak, not to this fixture -- it only redeems a code this
// same process minted with issueCode, exactly once.
func (f *fakeOIDCIssuer) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")

	f.mu.Lock()
	rawIDToken, ok := f.codes[code]
	delete(f.codes, code) // single use, same as a real token endpoint
	f.mu.Unlock()

	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "fake-access-token-never-a-real-credential",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     rawIDToken,
	})
}

func (f *fakeOIDCIssuer) issueCode(rawIDToken string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codeSeq++
	code := fmt.Sprintf("test-code-%d", f.codeSeq)
	f.codes[code] = rawIDToken
	return code
}

func (f *fakeOIDCIssuer) token(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshalling claims: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       f.key,
	}, &jose.SignerOptions{ExtraHeaders: map[jose.HeaderKey]any{"kid": f.keyID}})
	if err != nil {
		t.Fatalf("building a signer: %v", err)
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("signing the token: %v", err)
	}
	raw, err := sig.CompactSerialize()
	if err != nil {
		t.Fatalf("serializing the token: %v", err)
	}
	return raw
}

const oidcTestClientID = "invctl"

// oidcHarness is a small, self-contained harness for the OIDC flow tests --
// deliberately not built on newHarnessTuned, which has no seam for a
// provider pointed at a fake issuer and carries a great deal of unrelated
// agent/reader wiring this file does not need.
type oidcHarness struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	dsn    string
}

// opts lets a test set the rest of the auth configuration (AuthLDAP,
// AuthOIDC) without every existing caller growing two more booleans it does
// not care about. Applied after the defaults below, so an option wins.
func newOIDCHarness(t *testing.T, issuer *fakeOIDCIssuer, authLocal bool, opts ...func(*config.Config)) *oidcHarness {
	t.Helper()

	ctx := context.Background()
	provider, err := auth.NewOIDCProvider(ctx, auth.OIDCConfig{
		Issuer:      issuer.issuer,
		ClientID:    oidcTestClientID,
		RedirectURL: "https://invctl.example.com/auth/oidc/callback",
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider against the fake issuer: %v", err)
	}

	template, _ := webTemplate(t)
	dsn := filepath.Join(t.TempDir(), "oidc.db")
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
	st := store.New(db)

	sessions := scs.New()
	sessions.Store = sqlite3store.NewWithCleanupInterval(db.SQLDB(), 0)
	sessions.Cookie.Secure = false
	sessions.Cookie.Name = "invctl_session"

	renderer, err := testRenderer(t)
	if err != nil {
		t.Fatalf("parsing templates: %v", err)
	}

	cfg := &config.Config{
		AdminUsers: []string{"admin"},
		AuthLocal:  authLocal,
		AuthOIDC:   true,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	authz := auth.NewAuthorizer(cfg.AdminUsers, st)

	var authenticators []auth.Authenticator
	if authLocal {
		authenticators = append(authenticators, auth.NewLocalAuthenticator(st))
	}

	app := &handlers.App{
		Store:    st,
		Render:   renderer,
		Sessions: sessions,
		Auth:     auth.NewChain(st, authenticators...),
		Authz:    authz,
		Config:   cfg,
		OIDC:     provider,
	}

	server := httptest.NewServer(web.Routes(app, staticFS(t), authz, nil, nil))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("building cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		// Redirects are exactly what these tests inspect -- the Location
		// header carries state and nonce a real browser would never expose
		// to test code, so they must not be followed.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &oidcHarness{t: t, server: server, client: client, dsn: dsn}
}

func (h *oidcHarness) url(path string) string { return h.server.URL + path }

func (h *oidcHarness) get(path string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Get(h.url(path))
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// sessionCookie reads the current session cookie value straight from the
// jar, so a test can assert it changed across RenewToken without depending
// on response header ordering.
func (h *oidcHarness) sessionCookie() string {
	h.t.Helper()
	u, err := url.Parse(h.server.URL)
	if err != nil {
		h.t.Fatalf("parsing server URL: %v", err)
	}
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == "invctl_session" {
			return c.Value
		}
	}
	return ""
}

// startOIDC drives OIDCStart and returns the state and nonce Keycloak would
// have been sent -- read back from the redirect's own query string, the only
// place a client (real or test) can see them, exactly as OIDCCallback later
// reads state from the query string it is invoked with.
func (h *oidcHarness) startOIDC() (state, nonce string) {
	h.t.Helper()
	resp := h.get("/auth/oidc")
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("GET /auth/oidc = %d, want a redirect", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		h.t.Fatalf("parsing Location header: %v", err)
	}
	return loc.Query().Get("state"), loc.Query().Get("nonce")
}

// defaultOIDCClaims mirrors internal/auth/oidc_test.go's own -- a token
// Exchange must accept, so a callback test's failure has exactly one cause.
func defaultOIDCClaims(issuer, nonce, sub string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":                issuer,
		"aud":                oidcTestClientID,
		"sub":                sub,
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Unix(),
		"nonce":              nonce,
		"preferred_username": "alice",
		"name":               "Alice A",
		"email":              "alice@example.com",
	}
}

// TestOIDCCallbackRefusesAMissingState: a callback that arrives with no
// state stored in the session (nobody went through /auth/oidc in this
// session at all) must be refused, not merely fail Exchange -- that guard
// runs before Exchange is ever called (see OIDCCallback's own comment).
func TestOIDCCallbackRefusesAMissingState(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	h := newOIDCHarness(t, issuer, false)

	// No /auth/oidc call happened in this session, so oidc_state, oidc_nonce
	// and oidc_verifier are all unset ("") in the session. The code and
	// token below are otherwise entirely VALID -- nonce "" is chosen to
	// match the empty session nonce exactly -- so this request is refused
	// ONLY by the missing-state guard. A weaker test (an outright garbage
	// code) would also return 401, but for the wrong reason -- Exchange
	// failing on a bad code -- and would still pass with the guard deleted,
	// which is precisely the false confidence the mutation-testing rule in
	// docs/superpowers/specs/2026-09-18-keycloak-oidc-design.md exists to
	// catch. Verified: with the guard removed, this exact request succeeds
	// (see the task report).
	claims := defaultOIDCClaims(issuer.issuer, "", "kc-sub-missing-state")
	code := issuer.issueCode(issuer.token(t, claims))

	resp := h.get("/auth/oidc/callback?state=attacker-supplied&code=" + url.QueryEscape(code))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("callback with no session state = %d, want 401", resp.StatusCode)
	}
	b := body2(t, resp)
	if !strings.Contains(b, "did not succeed") {
		t.Errorf("body does not carry the generic refusal message: %s", b)
	}
}

// TestOIDCCallbackRefusesAReplayedState proves the single-use property:
// state, nonce and the PKCE verifier are deleted from the session the
// instant OIDCCallback reads them, so a second request carrying the same
// state value the session issued once already finds nothing to match.
//
// The second request deliberately uses a FRESH, still-valid code and a
// token with an empty nonce (matching the now-empty session nonce) rather
// than replaying the first code byte-for-byte. A same-code replay would
// also return 401, but via the IdP token endpoint's own single-use
// enforcement (serveToken deletes a code the instant it is redeemed) --
// which says nothing about whether THIS codebase's state guard did
// anything at all. Isolating the guard this way is what makes the test able
// to fail: with it deleted, this exact second request succeeds (verified;
// see the task report).
func TestOIDCCallbackRefusesAReplayedState(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	h := newOIDCHarness(t, issuer, false)

	state, nonce := h.startOIDC()
	claims := defaultOIDCClaims(issuer.issuer, nonce, "kc-sub-replay")
	code := issuer.issueCode(issuer.token(t, claims))
	callback := fmt.Sprintf("/auth/oidc/callback?state=%s&code=%s", url.QueryEscape(state), url.QueryEscape(code))

	first := h.get(callback)
	if first.StatusCode != http.StatusSeeOther && first.StatusCode != http.StatusFound {
		t.Fatalf("first callback = %d, want a redirect (successful sign-in)", first.StatusCode)
	}

	replayClaims := defaultOIDCClaims(issuer.issuer, "", "kc-sub-replay")
	replayCode := issuer.issueCode(issuer.token(t, replayClaims))
	second := h.get(fmt.Sprintf("/auth/oidc/callback?state=%s&code=%s", url.QueryEscape(state), url.QueryEscape(replayCode)))
	if second.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed callback = %d, want 401 -- a state value already consumed must not be usable again", second.StatusCode)
	}
}

// TestOIDCCallbackRenewsTheSessionID is the session-fixation defence Login
// already gets from RenewToken, asserted for this authenticator too.
func TestOIDCCallbackRenewsTheSessionID(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	h := newOIDCHarness(t, issuer, false)

	state, nonce := h.startOIDC()
	before := h.sessionCookie()
	if before == "" {
		t.Fatal("no session cookie after /auth/oidc; the harness cannot assert renewal without one")
	}

	claims := defaultOIDCClaims(issuer.issuer, nonce, "kc-sub-renew")
	code := issuer.issueCode(issuer.token(t, claims))
	resp := h.get(fmt.Sprintf("/auth/oidc/callback?state=%s&code=%s", url.QueryEscape(state), url.QueryEscape(code)))
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("callback = %d, want a redirect", resp.StatusCode)
	}

	after := h.sessionCookie()
	if after == "" {
		t.Fatal("no session cookie after a successful callback")
	}
	if after == before {
		t.Error("session cookie is unchanged after sign-in; RenewToken must issue a fresh session id " +
			"so a session id planted before authentication cannot ride into an authenticated session")
	}
}

// TestOIDCCallbackStoresNoToken is spec D5: the access and refresh tokens
// that complete the exchange are discarded, never persisted. Checked at the
// one place a leak would actually land -- the session store's own table --
// since the schema carries no token column for app_user to check there at
// all (see internal/store/migrations/*/00072_app_user_oidc.sql).
func TestOIDCCallbackStoresNoToken(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	h := newOIDCHarness(t, issuer, false)

	state, nonce := h.startOIDC()
	claims := defaultOIDCClaims(issuer.issuer, nonce, "kc-sub-notoken")
	code := issuer.issueCode(issuer.token(t, claims))
	resp := h.get(fmt.Sprintf("/auth/oidc/callback?state=%s&code=%s", url.QueryEscape(state), url.QueryEscape(code)))
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("callback = %d, want a redirect", resp.StatusCode)
	}

	db, err := store.Open(store.DriverSQLite, "file:"+h.dsn)
	if err != nil {
		t.Fatalf("reopening the test database: %v", err)
	}
	defer db.Close()

	var blobs [][]byte
	if err := db.Reader.Select(&blobs, "SELECT data FROM sessions"); err != nil {
		t.Fatalf("reading session data: %v", err)
	}
	if len(blobs) == 0 {
		t.Fatal("no session rows found after a successful sign-in")
	}
	for _, b := range blobs {
		// The gob-encoded session map carries every key and string value as
		// plain UTF-8 inside the blob, so a stored token would show up as a
		// substring here even though nothing in this codebase ever
		// %-encodes or otherwise obscures it. This is the never-persisted
		// half of D5; the never-relayed half is Exchange's own contract
		// (internal/auth/oidc.go), not something a session-table scan could
		// see either way.
		if bytes.Contains(b, []byte("fake-access-token-never-a-real-credential")) {
			t.Error("the access token was found inside the session store; spec D5 requires it be discarded")
		}
		if bytes.Contains(b, []byte("access_token")) || bytes.Contains(b, []byte("refresh_token")) {
			t.Error("a token field name was found inside the session store")
		}
	}
}

// TestLoginPageOffersKeycloakAndHidesThePasswordForm is the template half of
// spec D3: with OIDC configured and local auth off, the login page must not
// offer a password field at all -- hiding the nav link elsewhere is not
// enforcement (see this repo's own TestHidingAControlIsNotTheEnforcement),
// and the same is true of a form nobody is supposed to use.
func TestLoginPageOffersKeycloakAndHidesThePasswordForm(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	h := newOIDCHarness(t, issuer, false)

	resp := h.get("/login")
	b := body2(t, resp)
	if !strings.Contains(b, "/auth/oidc") {
		t.Error("login page does not offer the Keycloak sign-in link")
	}
	if strings.Contains(b, `name="password"`) {
		t.Error("login page still renders a password field with local auth off -- " +
			"this is the MFA bypass spec D3 exists to close")
	}
}

func body2(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return buf.String()
}
