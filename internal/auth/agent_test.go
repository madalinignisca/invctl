// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/config"
	"github.com/madalinignisca/invctl/internal/domain"
)

// The monitoring credential is a different principal type from an operator
// account (docs/AUDIT.md rule 6). These tests are about the two things that
// separation rests on: that a token resolves to exactly the credential that
// owns it, and that nothing about a credential can be built by hand.

const (
	tokenA = "aaaa-token-000000000000000000000000"
	tokenB = "bbbb-token-111111111111111111111111"
)

func testCredentials() []config.AgentCredential {
	return []config.AgentCredential{
		{ID: "prom-a", Token: tokenA, Environments: []string{"prod", "transit"}},
		{ID: "prom-b", Token: tokenB, Environments: []string{"dev"}, Vocabulary: "prometheus"},
	}
}

func mustRegistry(t *testing.T, creds []config.AgentCredential) *AgentRegistry {
	t.Helper()
	r, err := NewAgentRegistry(creds)
	if err != nil {
		t.Fatalf("building registry: %v", err)
	}
	return r
}

// TestAuthenticateResolvesTheOwningCredential. Identity is derived from the
// secret rather than claimed beside it: the wire carries the token alone, so
// there is no field in which one credential can name another.
func TestAuthenticateResolvesTheOwningCredential(t *testing.T) {
	r := mustRegistry(t, testCredentials())

	cases := []struct {
		name   string
		token  string
		wantID string
	}{
		{name: "the first credential's token", token: tokenA, wantID: "prom-a"},
		{name: "the second credential's token", token: tokenB, wantID: "prom-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent, ok := r.Authenticate(tc.token)
			if !ok {
				t.Fatalf("a valid token was refused")
			}
			if agent.ID != tc.wantID {
				t.Errorf("id = %q, want %q", agent.ID, tc.wantID)
			}
			// Rule 5: the audit identity is namespaced and built by the
			// constructor, never assembled at the call site.
			actor := agent.Actor()
			if actor.Kind != domain.ActorKindAgent {
				t.Errorf("actor kind = %q, want agent", actor.Kind)
			}
			if actor.ID != domain.AgentActorPrefix+tc.wantID {
				t.Errorf("actor id = %q, want %q", actor.ID, domain.AgentActorPrefix+tc.wantID)
			}
			if err := actor.Validate(); err != nil {
				t.Errorf("the actor a credential produces does not validate: %v", err)
			}
		})
	}
}

func TestAuthenticateRefusesEverythingElse(t *testing.T) {
	r := mustRegistry(t, testCredentials())

	for _, token := range []string{
		"",
		"not-a-token",
		tokenA[:len(tokenA)-1],  // a prefix
		tokenA + "x",            // a suffix
		strings.ToUpper(tokenA), // case matters
		" " + tokenA,            // no trimming: a token is bytes
		domain.AgentActorPrefix + "prom-a",
	} {
		if agent, ok := r.Authenticate(token); ok {
			t.Errorf("token %q authenticated as %s", token, agent.ID)
		}
	}
}

// TestAnEmptyRegistryAuthenticatesNobody. With no credentials configured the
// router mounts no machine-facing route at all, but the registry must be safe
// on its own terms too -- a nil receiver is what a partially wired deployment
// produces.
func TestAnEmptyRegistryAuthenticatesNobody(t *testing.T) {
	var nilRegistry *AgentRegistry
	if nilRegistry.Enabled() {
		t.Error("a nil registry reports itself enabled")
	}
	if _, ok := nilRegistry.Authenticate(tokenA); ok {
		t.Error("a nil registry authenticated a token")
	}

	empty := mustRegistry(t, nil)
	if empty.Enabled() {
		t.Error("an empty registry reports itself enabled")
	}
	if _, ok := empty.Authenticate(tokenA); ok {
		t.Error("an empty registry authenticated a token")
	}
}

// TestAgentScopeCannotBeWidenedThroughAnAuthenticatedCopy. Authenticate hands
// out a copy; a handler that appended to the slice it was given would otherwise
// widen the live credential for every request after it.
func TestAgentScopeCannotBeWidenedThroughAnAuthenticatedCopy(t *testing.T) {
	r := mustRegistry(t, testCredentials())

	first, ok := r.Authenticate(tokenA)
	if !ok {
		t.Fatal("a valid token was refused")
	}
	first.Environments = append(first.Environments, "dev")

	second, ok := r.Authenticate(tokenA)
	if !ok {
		t.Fatal("a valid token was refused")
	}
	if second.Environments.Allows("dev") {
		t.Errorf("scope = %v: one request widened the credential for the next", second.Environments)
	}
}

// TestRegistryRefusesACredentialItCannotBuild. Every failure is a startup
// failure. Skipping a credential that will not build means a collector that
// authenticates today stops authenticating after a config edit, with nothing in
// the logs naming the edit.
func TestRegistryRefusesACredentialItCannotBuild(t *testing.T) {
	cases := []struct {
		name string
		cred config.AgentCredential
		want string
	}{
		{
			name: "no environment scope",
			cred: config.AgentCredential{ID: "prom-a", Token: tokenA},
			want: "environment",
		},
		{
			name: "an id already carrying the actor namespace",
			cred: config.AgentCredential{ID: "monitor:prom-a", Token: tokenA, Environments: []string{"prod"}},
			want: "namespaced",
		},
		{
			name: "an id in mixed case",
			cred: config.AgentCredential{ID: "Prom-A", Token: tokenA, Environments: []string{"prod"}},
			want: "lower case",
		},
		{
			name: "an unknown vocabulary",
			// Misspelt on purpose: the case is "a typo in the deployment is
			// refused at startup". golangci-lint --fix corrected it to the
			// valid name once, which quietly turned this into an assertion that
			// a CORRECT vocabulary is rejected -- and the case still passed,
			// because NewAgentRegistry rejected it for a different reason.
			cred: config.AgentCredential{ID: "prom-a", Token: tokenA, Environments: []string{"prod"}, Vocabulary: "promethues"}, //nolint:misspell // deliberate typo under test
			want: "vocabulary",
		},
		{
			name: "no id at all",
			cred: config.AgentCredential{Token: tokenA, Environments: []string{"prod"}},
			want: "required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAgentRegistry([]config.AgentCredential{tc.cred})
			if err == nil {
				t.Fatalf("accepted %+v", tc.cred)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestNothingRendersATokenBackOut. The usual way a secret reaches a log is
// somebody printing the struct that holds it while debugging something else.
func TestNothingRendersATokenBackOut(t *testing.T) {
	r := mustRegistry(t, testCredentials())
	agent, ok := r.Authenticate(tokenA)
	if !ok {
		t.Fatal("a valid token was refused")
	}
	if rendered := agent.String(); strings.Contains(rendered, tokenA) {
		t.Errorf("Agent.String() contains the token: %s", rendered)
	}

	cred := testCredentials()[0]
	if rendered := cred.String(); strings.Contains(rendered, tokenA) {
		t.Errorf("config.AgentCredential.String() contains the token: %s", rendered)
	}
	if rendered := cred.LogValue().String(); strings.Contains(rendered, tokenA) {
		t.Errorf("config.AgentCredential.LogValue() contains the token: %s", rendered)
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{header: "Bearer abc", want: "abc", ok: true},
		// RFC 7235 says the scheme is case-insensitive, and getting that wrong
		// produces a 401 no amount of staring at the token explains.
		{header: "bearer abc", want: "abc", ok: true},
		{header: "BEARER abc", want: "abc", ok: true},
		{header: "Bearer  abc  ", want: "abc", ok: true},
		{header: "", ok: false},
		{header: "Bearer", ok: false},
		{header: "Bearer ", ok: false},
		{header: "Basic abc", ok: false},
		{header: "abc", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			got, ok := BearerToken(tc.header)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAnAgentCannotReachCanWrite is a compile-time statement written as a test
// so it is read rather than merely true.
//
// Authorizer.CanWrite takes a *domain.AppUser. An *Agent is not one and cannot
// be converted into one, so "a monitoring credential never reaches
// authz.CanWrite" is enforced by the type checker rather than by everyone
// remembering. If a future change gives CanWrite a wider parameter, this
// comment is where to argue about it.
func TestAnAgentCannotReachCanWrite(t *testing.T) {
	authz := NewAuthorizer([]string{"prom-a", "admin"})
	// Even with the credential id sitting in the admin list -- the exact
	// misconfiguration rule 6 warns about, and which config refuses at startup
	// -- there is no user for it, so nothing can be granted.
	if authz.CanWrite(nil) {
		t.Error("CanWrite(nil) granted write access")
	}
	r := mustRegistry(t, testCredentials())
	agent, ok := r.Authenticate(tokenA)
	if !ok {
		t.Fatal("a valid token was refused")
	}
	if agent.ID != "prom-a" {
		t.Fatalf("id = %q", agent.ID)
	}
	// authz.CanWrite(agent) does not compile. That is the test.
}
