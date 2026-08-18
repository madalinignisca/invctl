// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/config"
)

// testReaderGuard builds a ReaderGuard over a one-credential registry
// (ansible / tok-a / {prod}) with generous limiters and SessionCookie:
// "session".
func testReaderGuard(t *testing.T) ReaderGuard {
	t.Helper()
	registry, err := auth.NewReaderRegistry([]config.ReaderCredential{
		{ID: "ansible", Token: "tok-a", Environments: []string{"prod"}},
	})
	if err != nil {
		t.Fatalf("building reader registry: %v", err)
	}
	return ReaderGuard{
		Registry:        registry,
		Credentials:     NewRateLimiter(ReaderRequestsPerSecond, ReaderBurst),
		Unauthenticated: NewRateLimiter(1000, 1000),
		SessionCookie:   "session",
	}
}

// okHandler returns a handler that always writes 200.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestABrowserSessionIsRefusedOnTheAPI(t *testing.T) {
	g := testReaderGuard(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer tok-a")
	req.AddCookie(&http.Cookie{Name: g.SessionCookie, Value: "anything"})

	RequireReader(g)(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a session on this route is principal confusion", rec.Code)
	}
}

func TestNoBearerTokenIsUnauthorised(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
	RequireReader(testReaderGuard(t))(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestAnUnrecognisedTokenIsUnauthorised(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer wrong")
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
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
		req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
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
		req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
		req.Header.Set("Authorization", "Bearer tok-a")
		RequireReader(g)(okHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d got %d, want 200", i, rec.Code)
		}
	}
}

func TestACredentialOverItsOwnRateLimitIsThrottled(t *testing.T) {
	g := testReaderGuard(t)
	g.Credentials = NewRateLimiter(0, 1) // one request, no refill
	var last int
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
		req.Header.Set("Authorization", "Bearer tok-a")
		RequireReader(g)(okHandler()).ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429 once the credential's own bucket is exhausted", last)
	}
}

// TestTheReaderGuardNamesItsOwnVariableInThe401 pins a message that had leaked
// across the seam: /api/v1 answered agent.go's "a valid monitoring credential
// is required", which sends an Ansible integrator debugging a 401 to
// INV_AGENT_TOKENS -- the wrong variable, on the wrong surface, for a
// credential that would not have worked here anyway. Both refusal paths are
// asserted, because they are two call sites and either can drift back.
func TestTheReaderGuardNamesItsOwnVariableInThe401(t *testing.T) {
	const want = `{"error":"a valid read credential is required; see INV_API_TOKENS"}`
	cases := map[string]string{
		"no bearer token":    "",
		"unrecognised token": "wrong",
	}
	for name, bearer := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			RequireReader(testReaderGuard(t))(okHandler()).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != want {
				t.Fatalf("got body %q, want %q", got, want)
			}
			if strings.Contains(rec.Body.String(), "monitoring") {
				t.Fatalf("the reader route sent the monitoring surface's message: %s", rec.Body.String())
			}
		})
	}
}
