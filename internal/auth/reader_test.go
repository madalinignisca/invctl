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

// TestAReaderRegistryDoesNotWidenScopeThroughAnAuthenticatedCopy mirrors the
// equivalent AgentRegistry test: Authenticate hands out a copy, so a handler
// appending to the slice it was given cannot widen the live credential for
// the next request.
func TestAReaderRegistryDoesNotWidenScopeThroughAnAuthenticatedCopy(t *testing.T) {
	r := mustReaderRegistry(t, []config.ReaderCredential{
		{ID: "ansible", Token: "tok-a", Environments: []string{"prod"}},
	})

	first, ok := r.Authenticate("tok-a")
	if !ok {
		t.Fatal("a valid token was refused")
	}
	first.Environments = append(first.Environments, "dev")

	second, ok := r.Authenticate("tok-a")
	if !ok {
		t.Fatal("a valid token was refused")
	}
	if second.Environments.Allows("dev") {
		t.Fatalf("scope = %v: one request widened the credential for the next", second.Environments)
	}
}

// TestAReaderRegistryListsIDsSorted exercises IDs() and its nil-safety: a log
// line and a test should always agree on the order.
func TestAReaderRegistryListsIDsSorted(t *testing.T) {
	r := mustReaderRegistry(t, []config.ReaderCredential{
		{ID: "zeta", Token: "tok-zeta-0000000000000", Environments: []string{"prod"}},
		{ID: "alpha", Token: "tok-alpha-000000000000", Environments: []string{"prod"}},
	})
	got := r.IDs()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}

	var nilRegistry *ReaderRegistry
	if ids := nilRegistry.IDs(); ids != nil {
		t.Fatalf("a nil registry's IDs() = %v, want nil", ids)
	}
}
