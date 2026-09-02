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

// WP-1.1 Task 1: a project owner who owns BOTH ends of a dependency -- the
// consumer service and the provider's owning service -- has been able to
// write it at the store layer since authorizeDependencySubjects (deps.go)
// landed, but no control was ever rendered: depRows fed isAdmin into
// CanWrite regardless of what the caller actually owned. This file pins the
// four-way boundary (both ends, consumer only, provider only, neither) plus
// the one thing that must NOT move with it: the secret ref stays
// Administrator-only even for a project owner who now sees the controls.
type depRowFixture struct {
	// serviceIn is already owned by project alpha (via setupBoundary);
	// serviceIn2 is a second service linked to alpha here, so a dependency
	// can have BOTH ends inside the owner's project without depending on a
	// service being both its own consumer and provider.
	serviceIn2 string

	// depBoth: consumer=serviceIn (owned), provider=serviceIn2 (owned).
	depBoth string
	// depConsumerOnly: consumer=serviceIn (owned), provider=serviceOut (not).
	depConsumerOnly string
	// depProviderOnly: consumer=serviceOut (not owned), provider=serviceIn2 (owned).
	depProviderOnly string
	// depNeither: consumer=serviceOut, provider=serviceOut -- neither end owned.
	depNeither string

	// depWithSecret carries an identity with a secret_ref, both ends owned
	// by the project owner -- so CanWrite is true, and SecretRef must still
	// be redacted for anyone who is not a full Administrator.
	depWithSecret string
	vaultRef      string
}

func setupDepRowFixture(t *testing.T, ctx context.Context, h *harness, fx *boundaryFixtures) *depRowFixture {
	t.Helper()
	admin := domain.AdministratorPermit(domain.SystemActor)

	env, err := h.store.ListEnvironments(ctx)
	if err != nil || len(env) == 0 {
		t.Fatalf("listing environments for the dep-row fixture: %v", err)
	}
	serviceIn2 := mustBoundaryService(t, ctx, h, "t-alpha-svc-2", env[0].ID)
	link, err := domain.NewProjectServiceLink(fx.projectAlpha, serviceIn2, domain.ProjectOwns, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building the second in-scope service link: %v", err)
	}
	if err := h.store.LinkProjectService(ctx, admin, link); err != nil {
		t.Fatalf("linking the second in-scope service to alpha: %v", err)
	}

	mkEndpoint := func(serviceID, name string) string {
		port := 5432
		ep, err := domain.NewEndpoint(store.NewID(), serviceID, name, domain.ProtoTCP, &port, domain.BindHost)
		if err != nil {
			t.Fatalf("building endpoint %s: %v", name, err)
		}
		if err := h.store.CreateEndpoint(ctx, admin, ep); err != nil {
			t.Fatalf("creating endpoint %s: %v", name, err)
		}
		return ep.ID
	}
	epOnIn2 := mkEndpoint(serviceIn2, "ep-on-in2")
	epOnOut := mkEndpoint(fx.serviceOut, "ep-on-out")

	mkDep := func(consumerID, providerEndpointID string) string {
		ep := providerEndpointID
		d, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
			ConsumerServiceID:  consumerID,
			ProviderEndpointID: &ep,
			Nature:             domain.NatureHard,
			FailureMode:        "dep-row-controls fixture",
			Source:             domain.SourceDeclared,
		}, h.store.Now())
		if err != nil {
			t.Fatalf("building dependency: %v", err)
		}
		if err := h.store.CreateDependency(ctx, admin, d, nil); err != nil {
			t.Fatalf("creating dependency: %v", err)
		}
		return d.ID
	}

	depBoth := mkDep(fx.serviceIn, epOnIn2)
	depConsumerOnly := mkDep(fx.serviceIn, epOnOut)
	depProviderOnly := mkDep(fx.serviceOut, epOnIn2)
	depNeither := mkDep(fx.serviceOut, epOnOut)

	// A second, dedicated dependency for the secret-ref check, both ends
	// owned, carrying an identity with a real (path-shaped) secret_ref.
	identity, err := domain.NewIdentity(store.NewID(), domain.IdentityServiceAccount, "dep-row-secret-identity")
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	vaultRef := "kv/prod/dep-row-controls/fixture"
	identity.SecretRef = &vaultRef
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	epForSecret := mkEndpoint(serviceIn2, "ep-on-in2-secret")
	identityID := identity.ID
	dSecret, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
		ConsumerServiceID:  fx.serviceIn,
		ProviderEndpointID: &epForSecret,
		Nature:             domain.NatureHard,
		FailureMode:        "dep-row-controls secret fixture",
		Source:             domain.SourceDeclared,
		IdentityID:         &identityID,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building the secret-carrying dependency: %v", err)
	}
	if err := h.store.CreateDependency(ctx, admin, dSecret, nil); err != nil {
		t.Fatalf("creating the secret-carrying dependency: %v", err)
	}

	return &depRowFixture{
		serviceIn2:      serviceIn2,
		depBoth:         depBoth,
		depConsumerOnly: depConsumerOnly,
		depProviderOnly: depProviderOnly,
		depNeither:      depNeither,
		depWithSecret:   dSecret.ID,
		vaultRef:        vaultRef,
	}
}

// verifyAction is the marker rendered only inside {{if .CanWrite}} on
// rows.html's dependency_row -- the Verify button's own hx-post target.
func verifyAction(depID string) string {
	return `hx-post="/dependencies/` + depID + `/verify`
}

// TestDependencyRowControlsAreTwoEnded drives all four ownership
// combinations through one project owner, on the two service pages that
// between them carry every dependency the fixture built (a dependency's
// consumer is always the page it appears on, in the upstream panel).
func TestDependencyRowControlsAreTwoEnded(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			dfx := setupDepRowFixture(t, ctx, h, fx)

			// Positive control: an Administrator sees Verify on every row,
			// including the one where NEITHER end is anybody's project --
			// proving the marker itself renders before the owner checks
			// below mean anything.
			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminOutBody := body(t, h.get("/services/"+fx.serviceOut, false))
			for _, id := range []string{dfx.depProviderOnly, dfx.depNeither} {
				if !strings.Contains(adminOutBody, verifyAction(id)) {
					t.Fatalf("GET /services/%s as Administrator does not carry %q -- the row "+
						"partial is not proven to render its controls at all", fx.serviceOut, verifyAction(id))
				}
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			// fx.serviceIn's upstream panel carries depBoth and depConsumerOnly.
			inBody := body(t, h.get("/services/"+fx.serviceIn, false))
			if !strings.Contains(inBody, verifyAction(dfx.depBoth)) {
				t.Errorf("GET /services/%s as the owner of BOTH ends does not carry %q -- "+
					"authorizeDependencySubjects grants this write at the store layer, and the "+
					"row must offer it", fx.serviceIn, verifyAction(dfx.depBoth))
			}
			if strings.Contains(inBody, verifyAction(dfx.depConsumerOnly)) {
				t.Errorf("GET /services/%s carries %q for a dependency where the owner holds "+
					"only the CONSUMER end -- the provider end (serviceOut) is not theirs, and "+
					"the store refuses this write", fx.serviceIn, verifyAction(dfx.depConsumerOnly))
			}

			// fx.serviceOut's upstream panel carries depProviderOnly and depNeither.
			outBody := body(t, h.get("/services/"+fx.serviceOut, false))
			if strings.Contains(outBody, verifyAction(dfx.depProviderOnly)) {
				t.Errorf("GET /services/%s carries %q for a dependency where the owner holds "+
					"only the PROVIDER end -- the consumer end (serviceOut) is not theirs, and "+
					"the store refuses this write", fx.serviceOut, verifyAction(dfx.depProviderOnly))
			}
			if strings.Contains(outBody, verifyAction(dfx.depNeither)) {
				t.Errorf("GET /services/%s carries %q for a dependency the owner has no claim "+
					"on at either end", fx.serviceOut, verifyAction(dfx.depNeither))
			}
		})
	}
}

// TestDependencyRowSecretRefStaysAdministratorOnly is THE ONE THING THAT
// MUST NOT BREAK: a project owner who now sees the write controls on a
// dependency they own both ends of must still get domain.Redacted for the
// secret ref -- SecretRef is gated on isAdmin alone, deliberately narrower
// than the new two-ended CanWrite.
func TestDependencyRowSecretRefStaysAdministratorOnly(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			dfx := setupDepRowFixture(t, ctx, h, fx)

			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/services/"+fx.serviceIn, false))
			if !strings.Contains(adminBody, dfx.vaultRef) {
				t.Fatalf("GET /services/%s as Administrator does not carry the raw secret_ref "+
					"%q -- the field is not proven to render at all", fx.serviceIn, dfx.vaultRef)
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/services/"+fx.serviceIn, false))

			// This owner owns both ends of depWithSecret (serviceIn and
			// serviceIn2), so they DO see the write controls on that row.
			if !strings.Contains(ownerBody, verifyAction(dfx.depWithSecret)) {
				t.Fatalf("GET /services/%s as the owner of both ends of the secret-carrying "+
					"dependency does not carry %q -- the fixture must grant CanWrite here for "+
					"the redaction check below to mean anything", fx.serviceIn, verifyAction(dfx.depWithSecret))
			}
			if strings.Contains(ownerBody, dfx.vaultRef) {
				t.Errorf("GET /services/%s as a project owner (NOT an Administrator) carries "+
					"the raw secret_ref %q -- SecretRef must stay gated on isAdmin even though "+
					"this owner now sees the row's write controls", fx.serviceIn, dfx.vaultRef)
			}
			if !strings.Contains(ownerBody, domain.Redacted) {
				t.Errorf("GET /services/%s as a project owner does not carry %q anywhere -- "+
					"the redacted placeholder itself is not proven to render", fx.serviceIn, domain.Redacted)
			}
		})
	}
}

// upstreamTableColumns returns the number of <th> cells in the upstream
// dependency table's header and the number of <td> cells in its first data
// row, for a rendered service page.
//
// Crude on purpose: a full HTML parse would be more precise and would also
// hide the thing being measured behind a library. What matters here is only
// that the two counts agree.
func upstreamTableColumns(t *testing.T, page string) (headerCells, rowCells int) {
	t.Helper()
	const marker = "<th>Provider</th>"
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatalf("no upstream dependency table on the page -- the fixture must "+
			"seed one, or this test measures nothing:\n%.400s", page[:min(len(page), 400)])
	}
	head := page[i:]
	endHead := strings.Index(head, "</thead>")
	if endHead < 0 {
		t.Fatal("upstream table header never closes")
	}
	headerCells = strings.Count(head[:endHead], "<th")

	body := head[endHead:]
	startRow := strings.Index(body, "<tr")
	endRow := strings.Index(body, "</tr>")
	if startRow < 0 || endRow < 0 || endRow < startRow {
		t.Fatal("upstream table has a header but no data row -- fixture problem")
	}
	rowCells = strings.Count(body[startRow:endRow], "<td")
	return headerCells, rowCells
}

// TestDependencyTableHeaderMatchesItsRows pins the column count.
//
// WRITTEN BECAUSE IT DID NOT. Widening the row's actions cell to the two-ended
// CanWrite left the header still gated on IsAdmin, so a project owner who owns
// both ends of an edge -- exactly the person that widening was for -- got a row
// with one more cell than its header. Every existing assertion passed: they all
// searched for a control's presence or absence, and none counted anything. The
// browser pass found it by looking at the table.
//
// The header now asks depRowList.AnyWritable, which is per-table rather than
// per-page because a dependency's write permission is two-ended and two rows in
// one table can legitimately differ.
func TestDependencyTableHeaderMatchesItsRows(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			setupDepRowFixture(t, context.Background(), h, fx)

			check := func(who string) {
				page := body(t, h.get("/services/"+fx.serviceIn, false))
				header, row := upstreamTableColumns(t, page)
				if header != row {
					t.Errorf("%s sees %d header cells and %d row cells in the upstream "+
						"dependency table -- misaligned by %d", who, header, row, row-header)
				}
			}

			h.login(boundaryAdminUser, boundaryAdminPassword)
			check("an Administrator")
			h.logout()

			// The case that was broken: the row renders its actions cell for
			// this person and the header did not.
			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			check("a project owner owning both ends")
			h.logout()

			h.login(boundaryObserverUser, boundaryObserverPass)
			check("an Observer owning nothing")
			h.logout()
		})
	}
}
