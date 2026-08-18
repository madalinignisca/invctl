// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web"
	"github.com/madalinignisca/invctl/internal/web/handlers"
)

// Task 9 mounts the read-only, token-scoped inventory API built by earlier
// WP-A2 tasks. These tests cover the mounting site, not the handlers behind
// it: whether the surface exists at all, and whether it can be reached by
// the wrong kind of credential.

// TestTheAPIIsNotMountedWithoutACredential: an estate with no integrations
// must not carry the read surface, exactly as the machine-facing route is
// not mounted without a monitoring credential.
func TestTheAPIIsNotMountedWithoutACredential(t *testing.T) {
	h := newHarness(t) // no readers configured
	resp := h.get("/api/v1/assets", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d, want 404: an estate with no integrations must not carry the read surface",
			resp.StatusCode)
	}
}

// TestTheAPIRefusesABrowserSession: a read route that also accepted a
// session would let an operator's browser credentials satisfy a machine
// surface, which docs/AUDIT.md rule 6 refuses outright rather than
// resolving in either direction -- even when the request also carries a
// valid bearer token.
func TestTheAPIRefusesABrowserSession(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	h.login("viewer", "viewer-password") // establishes the session cookie on h.client's jar

	req := h.request(http.MethodGet, "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer "+readerAllToken)
	resp := h.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 for a request carrying both a session and a token", resp.StatusCode)
	}
}

// TestAnAgentTokenIsRefusedByTheAPI: a monitoring credential is a different
// principal type from a reader (docs/AUDIT.md rule 6), and must not read the
// inventory just because it can write observations.
func TestAnAgentTokenIsRefusedByTheAPI(t *testing.T) {
	h := newHarnessWithReaders(t, testAgentCredentials(), testReaderCredentials())
	req := h.request(http.MethodGet, "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer "+agentProdToken) // a real monitoring credential
	resp := h.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a monitoring credential must not read the inventory", resp.StatusCode)
	}
}

// TestAnAPITokenIsRefusedByObservations: the reverse of the above -- a
// read-only credential must not be able to write an observation.
func TestAnAPITokenIsRefusedByObservations(t *testing.T) {
	h := newHarnessWithReaders(t, testAgentCredentials(), testReaderCredentials())
	req := h.request(http.MethodPost, "/observations", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+readerAllToken)
	resp := h.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a read credential must not write an observation", resp.StatusCode)
	}
}

// TestAReaderCredentialReachesTheMountedAPI proves the mount itself works end
// to end for a valid credential, and that the same request answered twice is
// byte-for-byte identical -- the property apiGet exists to make cheap to
// assert.
func TestAReaderCredentialReachesTheMountedAPI(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())

	first := h.apiGet(t, "/api/v1/environments", readerAllToken)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 for a valid reader credential: %s", first.StatusCode, first.Body)
	}
	second := h.apiGet(t, "/api/v1/environments", readerAllToken)
	if second.StatusCode != first.StatusCode || second.Body != first.Body {
		t.Errorf("two reads of the same route disagree:\n  first:  %d %s\n  second: %d %s",
			first.StatusCode, first.Body, second.StatusCode, second.Body)
	}
}

// TestADevScopedReaderStillAuthenticates: a credential scoped to a single
// environment is still a valid credential at the mounting layer -- narrowing
// what it may see, checked deep in the store (an earlier task), is a
// different concern from whether it is let in at all (this one).
func TestADevScopedReaderStillAuthenticates(t *testing.T) {
	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())

	resp := h.apiGet(t, "/api/v1/environments", readerDevToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 for a validly scoped reader credential: %s", resp.StatusCode, resp.Body)
	}
}

// TestNoAPIRouteIsAWriteRoute walks the registered pattern table rather than
// trusting the diff. WP-A2 says "no write routes" and this is what makes the
// sentence true a year from now, whoever next edits routes.go.
func TestNoAPIRouteIsAWriteRoute(t *testing.T) {
	patterns := web.RegisteredAPIPatternsForTest()
	if len(patterns) == 0 {
		t.Fatal("the read surface's route table is empty; this test would pass vacuously")
	}
	for _, pattern := range patterns {
		if !strings.Contains(pattern, web.APIPrefix) {
			t.Errorf("%q does not carry %s; the route table has drifted from its own prefix",
				pattern, web.APIPrefix)
			continue
		}
		if !strings.HasPrefix(pattern, "GET ") {
			t.Errorf("%q is not a GET; the read surface has no write routes, ever", pattern)
		}
	}
}

// TestOnlyObservationsIsCSRFExempt: routes.go builds the exemption with
// middleware.ExactPath specifically so that /api/v1 cannot inherit it.
// Asserted against the exemption list itself, not the intent behind it -- and
// against a genuinely enabled agent surface, since an exemption list built
// from a disabled one would trivially be empty and prove nothing about the
// list this task is guarding.
func TestOnlyObservationsIsCSRFExempt(t *testing.T) {
	h := newHarness(t)

	registry, err := auth.NewAgentRegistry(testAgentCredentials())
	if err != nil {
		t.Fatalf("building agent registry: %v", err)
	}
	agents := &web.AgentSurface{
		Registry:      registry,
		Handler:       handlers.NewObservationAPI(store.NewObservedRecorder(h.store)),
		SessionCookie: "invctl_session",
	}

	exempt := web.CSRFExemptionsForTest(agents)
	if len(exempt) != 1 || string(exempt[0]) != web.ObservationsPath {
		t.Fatalf("csrf exemptions are %v; only %s may ever be exempt", exempt, web.ObservationsPath)
	}

	// And the empty case: no agent surface must exempt nothing.
	if exempt := web.CSRFExemptionsForTest(nil); len(exempt) != 0 {
		t.Fatalf("a nil agent surface exempted %v; want no exemptions at all", exempt)
	}
}
