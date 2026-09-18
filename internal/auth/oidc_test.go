// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIssuer is an httptest server that behaves like an OIDC provider:
// discovery document, JWKS, a token endpoint, and ID tokens signed with a
// key the test holds.
//
// A MOCK OF go-oidc WOULD TEST THAT A MOCK CAN BE WRITTEN. The failure modes
// worth catching -- is the signature actually checked, is `aud` actually
// compared -- exist only below that line, inside the library. So this talks
// real HTTP, publishes a real discovery document and a real JWKS, and lets
// OIDCProvider (backed by go-oidc, unmodified) do real discovery and real
// verification against it.
type fakeIssuer struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
	issuer string

	mu      sync.Mutex
	codes   map[string]string
	codeSeq int
}

// newFakeIssuer starts the server and generates the one signing key it
// trusts. Everything it serves is derived from that key and from whatever a
// test hands to (*fakeIssuer).token -- there is no hidden state a test could
// accidentally rely on.
func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the issuer's signing key: %v", err)
	}
	f := &fakeIssuer{key: key, keyID: "test-signing-key", codes: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.serveDiscovery)
	mux.HandleFunc("/keys", f.serveKeys)
	mux.HandleFunc("/token", f.serveToken)
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		// Exchange is called directly with a code minted by issueCode in
		// these tests; nothing here ever redirects a browser through this
		// endpoint. Its presence in the discovery document is what makes
		// this a REAL provider by the spec's definition, even though this
		// suite never drives a browser to it.
		http.Error(w, "not used by this test suite", http.StatusNotImplemented)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.issuer = f.srv.URL
	return f
}

func (f *fakeIssuer) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
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

func (f *fakeIssuer) serveKeys(w http.ResponseWriter, _ *http.Request) {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: f.key.Public(), KeyID: f.keyID, Algorithm: "RS256", Use: "sig"},
	}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

// serveToken is the token endpoint. It never checks the PKCE verifier or the
// client secret -- validating those belongs to Keycloak, not to this test's
// fixture -- it only redeems a code this same process minted with issueCode,
// exactly once, for whatever raw ID token was registered alongside it.
func (f *fakeIssuer) serveToken(w http.ResponseWriter, r *http.Request) {
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

// issueCode registers rawIDToken behind a fresh single-use code, standing in
// for what a real authorization endpoint does after the browser signs in.
func (f *fakeIssuer) issueCode(rawIDToken string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codeSeq++
	code := fmt.Sprintf("test-code-%d", f.codeSeq)
	f.codes[code] = rawIDToken
	return code
}

// token mints an ID token. Every field is settable so a test can make
// exactly one of them wrong:
//   - signWith nil signs with the issuer's real, published key; a non-nil
//     key signs with a key the JWKS never advertises, simulating a forged or
//     stale-key token.
//   - alg "" signs RS256; alg "none" produces an unsigned compact JWS by
//     hand, since go-jose refuses to build one -- go-oidc must still refuse
//     it, and the way it does is by refusing to parse a JWT signed with an
//     algorithm nobody configured it to trust.
func (f *fakeIssuer) token(t *testing.T, claims map[string]any, signWith *rsa.PrivateKey, alg string) string {
	t.Helper()
	if signWith == nil {
		signWith = f.key
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshalling claims: %v", err)
	}

	if alg == "none" {
		header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
		return base64.RawURLEncoding.EncodeToString(header) + "." +
			base64.RawURLEncoding.EncodeToString(payload) + "."
	}
	if alg == "" {
		alg = "RS256"
	}

	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.SignatureAlgorithm(alg),
		Key:       signWith,
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

// defaultClaims is a token that Exchange must accept -- every refusal test
// starts here and breaks exactly one field, so a failure always has a single
// cause.
func defaultClaims(issuer, clientID, nonce, sub string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":                issuer,
		"aud":                clientID,
		"sub":                sub,
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Unix(),
		"nonce":              nonce,
		"preferred_username": "alice",
		"name":               "Alice A",
		"email":              "alice@example.com",
	}
}

// newTestProvider performs real discovery against f.
func newTestProvider(t *testing.T, f *fakeIssuer, clientID string) *OIDCProvider {
	t.Helper()
	p, err := NewOIDCProvider(context.Background(), OIDCConfig{
		Issuer:      f.issuer,
		ClientID:    clientID,
		RedirectURL: "https://invctl.example.com/auth/oidc/callback",
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider against the fake issuer: %v", err)
	}
	return p
}

// captureSecurityLog redirects slog's default logger to buf for the
// duration of the test, following the syncBuffer/slog.SetDefault pattern
// internal/web's tests already use for the same purpose.
func captureSecurityLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

const (
	testClientID = "invctl"
	// A syntactically plausible PKCE verifier (43-128 unreserved characters
	// per RFC 7636). Its value is never checked by the fake issuer -- PKCE
	// verification is Keycloak's job -- so any string of legal length
	// exercises the code path without asserting a rule this suite doesn't
	// own.
	testVerifier = "test-pkce-verifier-0123456789-0123456789-0123456789"
)

// TestExchangeRefusesABadToken is the refusal table: each row is a single
// guard Exchange must apply, and each is mutation-tested separately (see the
// three "mutation-tested" comments below and the task report) by deleting
// the guard and confirming this exact subtest goes red.
func TestExchangeRefusesABadToken(t *testing.T) {
	const nonce = "expected-nonce-0123456789"

	for _, tc := range []struct {
		name     string
		mutate   func(claims map[string]any)
		signWith func(t *testing.T) *rsa.PrivateKey
		alg      string
		want     string
	}{
		{
			name: "signed by the wrong key",
			signWith: func(t *testing.T) *rsa.PrivateKey {
				k, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatalf("generating an untrusted key: %v", err)
				}
				return k
			},
			want: "signature",
		},
		{
			name:   "audience is another client",
			mutate: func(c map[string]any) { c["aud"] = "some-other-client" },
			want:   "audience",
		},
		{
			name:   "issuer is not the configured one",
			mutate: func(c map[string]any) { c["iss"] = "https://evil.example.com/realms/x" },
			want:   "issuer",
		},
		{
			name:   "expired",
			mutate: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
			want:   "expired",
		},
		{
			name: "alg none",
			alg:  "none",
			want: "signature",
		},
		{
			name:   "nonce does not match the session",
			mutate: func(c map[string]any) { c["nonce"] = "not-the-one-we-sent" },
			want:   "nonce",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIssuer(t)
			p := newTestProvider(t, f, testClientID)

			claims := defaultClaims(f.issuer, testClientID, nonce, "user-1")
			if tc.mutate != nil {
				tc.mutate(claims)
			}
			var key *rsa.PrivateKey
			if tc.signWith != nil {
				key = tc.signWith(t)
			}
			code := f.issueCode(f.token(t, claims, key, tc.alg))

			buf := captureSecurityLog(t)

			_, err := p.Exchange(context.Background(), code, testVerifier, nonce)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Exchange() error = %v, want ErrInvalidCredentials", err)
			}
			// The distinction lives in the log, not in what the caller sees
			// (docs/superpowers/specs/2026-09-18-keycloak-oidc-design.md §6):
			// an attacker probing this endpoint must not learn WHICH check
			// failed from the response.
			if err.Error() != ErrInvalidCredentials.Error() {
				t.Errorf("Exchange() returned error text %q, which must never differ from "+
					"the generic ErrInvalidCredentials message -- that would leak the "+
					"reason to whoever is on the other end of the callback", err.Error())
			}
			if logged := buf.String(); !strings.Contains(logged, tc.want) {
				t.Errorf("security log = %q, want it to name the reason %q so an operator "+
					"can tell a bad signature from a replay", logged, tc.want)
			}
		})
	}
}

// TestExchangeAcceptsAGoodToken is the positive control: the six refusals
// above prove nothing if Exchange refuses every token, well-formed or not --
// this is what makes the refusal table trustworthy.
func TestExchangeAcceptsAGoodToken(t *testing.T) {
	const nonce = "expected-nonce-good-token"

	f := newFakeIssuer(t)
	p := newTestProvider(t, f, testClientID)

	claims := defaultClaims(f.issuer, testClientID, nonce, "kc-subject-42")
	code := f.issueCode(f.token(t, claims, nil, ""))

	got, err := p.Exchange(context.Background(), code, testVerifier, nonce)
	if err != nil {
		t.Fatalf("Exchange() with a well-formed token = %v, want success", err)
	}
	want := OIDCClaims{
		Subject:     "kc-subject-42",
		Username:    "alice",
		DisplayName: "Alice A",
		Email:       "alice@example.com",
	}
	if *got != want {
		t.Errorf("Exchange() = %+v, want %+v", *got, want)
	}
}

// TestExchangeRefusesAnEmptySubject pins the whole of the account-matching
// rule (spec D2): Subject is the only claim UpsertOIDCUser matches an
// existing account on, so an empty subject is not "a user with no name", it
// is an identity that isn't one. Mutation-tested below.
func TestExchangeRefusesAnEmptySubject(t *testing.T) {
	const nonce = "expected-nonce-empty-subject"

	f := newFakeIssuer(t)
	p := newTestProvider(t, f, testClientID)

	claims := defaultClaims(f.issuer, testClientID, nonce, "")
	code := f.issueCode(f.token(t, claims, nil, ""))

	buf := captureSecurityLog(t)

	_, err := p.Exchange(context.Background(), code, testVerifier, nonce)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Exchange() with an empty sub claim = %v, want ErrInvalidCredentials", err)
	}
	if logged := buf.String(); !strings.Contains(logged, "subject") {
		t.Errorf("security log = %q, want it to name the empty subject", logged)
	}
}

// TestExchangeWrapsATransportFailure asserts the OTHER half of the error
// contract: a code the token endpoint itself rejects (network problem,
// Keycloak down, or in this fixture simply an unknown code) is an
// operational failure, not a credential failure, and must not come back as
// ErrInvalidCredentials -- the handler needs to tell "your session expired"
// apart from "Keycloak is down" (spec §6).
func TestExchangeWrapsATransportFailure(t *testing.T) {
	f := newFakeIssuer(t)
	p := newTestProvider(t, f, testClientID)

	_, err := p.Exchange(context.Background(), "a-code-nobody-issued", testVerifier, "any-nonce")
	if err == nil {
		t.Fatal("Exchange() with an unknown code succeeded, want an error")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Error("a rejected authorization code is a transport/protocol failure, " +
			"not a verified-and-refused token -- it must not be ErrInvalidCredentials")
	}
}

// TestAuthCodeURLAlwaysUsesPKCE pins "PKCE always, even with a client
// secret": AuthCodeURL never has a code path that omits the challenge, so
// there is no configuration of this provider that produces a URL an
// intercepted-code attack can complete.
func TestAuthCodeURLAlwaysUsesPKCE(t *testing.T) {
	f := newFakeIssuer(t)
	p := newTestProvider(t, f, testClientID)

	raw := p.AuthCodeURL("state-1", "nonce-1", testVerifier)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthCodeURL() = %q, not a valid URL: %v", raw, err)
	}
	q := u.Query()
	if q.Get("code_challenge") == "" {
		t.Errorf("AuthCodeURL() = %q, missing code_challenge", raw)
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("AuthCodeURL() code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("state") != "state-1" {
		t.Errorf("AuthCodeURL() state = %q, want %q", q.Get("state"), "state-1")
	}
	if q.Get("nonce") != "nonce-1" {
		t.Errorf("AuthCodeURL() nonce = %q, want %q", q.Get("nonce"), "nonce-1")
	}
}

// TestNewOIDCProviderRequiresIssuerClientIDAndRedirectURL is the
// configuration-mistake case: a provider built with a hole in its config
// must fail loudly at startup rather than build something that can never
// verify anything.
func TestNewOIDCProviderRequiresIssuerClientIDAndRedirectURL(t *testing.T) {
	f := newFakeIssuer(t)
	base := OIDCConfig{Issuer: f.issuer, ClientID: testClientID, RedirectURL: "https://invctl.example.com/auth/oidc/callback"}

	for _, tc := range []struct {
		name string
		cfg  OIDCConfig
	}{
		{"missing issuer", OIDCConfig{ClientID: base.ClientID, RedirectURL: base.RedirectURL}},
		{"missing client id", OIDCConfig{Issuer: base.Issuer, RedirectURL: base.RedirectURL}},
		{"missing redirect url", OIDCConfig{Issuer: base.Issuer, ClientID: base.ClientID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewOIDCProvider(context.Background(), tc.cfg); err == nil {
				t.Fatalf("NewOIDCProvider(%+v) succeeded, want an error", tc.cfg)
			}
		})
	}
}

// TestNewOIDCProviderWrapsADiscoveryFailure asserts an unreachable issuer at
// startup is an operational failure the operator sees in full, never mapped
// to ErrInvalidCredentials -- there is no credential involved yet.
func TestNewOIDCProviderWrapsADiscoveryFailure(t *testing.T) {
	_, err := NewOIDCProvider(context.Background(), OIDCConfig{
		Issuer:      "http://127.0.0.1:1", // nothing listens here
		ClientID:    testClientID,
		RedirectURL: "https://invctl.example.com/auth/oidc/callback",
	})
	if err == nil {
		t.Fatal("NewOIDCProvider against an unreachable issuer succeeded, want an error")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Error("a discovery failure is an operational problem, not a credential failure")
	}
}
