// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// TestADependencyNamingARetiredIdentityKeepsIt is the regression the filter
// change could have introduced. See the comment at the two ListIdentities call
// sites: an option that vanishes from a <select> does not leave the field
// unchanged, it clears it.
func TestADependencyNamingARetiredIdentityKeepsIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-still-named",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	consumer := h.refs.Services["orders-api"]
	endpoint := h.lookup(`SELECT id FROM endpoint ORDER BY id LIMIT 1`)
	dep, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
		ConsumerServiceID:  consumer,
		ProviderEndpointID: &endpoint,
		Nature:             domain.NatureHard,
		FailureMode:        "orders cannot authenticate",
		IdentityID:         &identity.ID,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building dependency: %v", err)
	}
	if err := h.store.CreateDependency(ctx, admin, dep, nil); err != nil {
		t.Fatalf("creating dependency: %v", err)
	}

	// The dependency correction row opens on ?dep=, not ?edit= -- ?edit= is
	// spent on the service form itself (services.go's own comment: "this page
	// already spends EditRow on the service itself AND on an endpoint row").
	page := "/services/" + consumer + "?dep=" + dep.ID
	marker := `value="` + identity.ID + `"`

	// POSITIVE CONTROL: the option is there while the credential is live, so
	// the assertion after the withdrawal is about the filter and not about
	// the picker never having rendered.
	before := body(t, h.get(page, false))
	if !strings.Contains(before, marker) {
		t.Fatalf("the correction row does not offer %s even while it is live; this test "+
			"would then prove nothing", identity.Name)
	}

	if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
		t.Fatalf("withdrawing the credential: %v", err)
	}

	after := body(t, h.get(page, false))
	if !strings.Contains(after, marker) {
		t.Fatalf("the correction row no longer offers the RETIRED credential the " +
			"dependency names. An option that vanishes from a <select> does not leave " +
			"the field unchanged -- the browser falls back to the empty first option, " +
			"and saving the row silently clears identity_id on the edges of exactly " +
			"the credential somebody is mid-incident about. ListIdentities is called " +
			"with IncludeRetired: true at internal/web/handlers/deps.go and " +
			"services.go for this reason.")
	}
	// And it is still the SELECTED one on the correction row, not merely
	// present somewhere on the page. The identity <select> appears twice --
	// once on the "add a dependency" create form (forms.html:646, which never
	// marks a selection because nothing is being corrected there) and once on
	// this dependency's own inline correction row (rows.html:69, which does).
	// Scanning every occurrence of the marker rather than only the first is
	// what makes this assertion about the CORRECTION row specifically, rather
	// than about whichever <select> the page happens to render first.
	foundSelected := false
	for idx := strings.Index(after, marker); idx != -1; {
		option := after[idx:min(idx+200, len(after))]
		if strings.Contains(option, "selected") {
			foundSelected = true
			break
		}
		rest := strings.Index(after[idx+1:], marker)
		if rest == -1 {
			break
		}
		idx = idx + 1 + rest
	}
	if !foundSelected {
		t.Error("the retired credential is offered but not selected on the correction " +
			"row, so saving it would post a different value than the one stored")
	}
}
