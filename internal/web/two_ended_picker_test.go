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
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-1.1 Task 3: the row controls (Task 1, Task 2) made write CONTROLS honest
// -- shown only when the store would actually accept the write. This file is
// the same honesty applied to the CREATE forms' pickers: a project owner's
// far-end dropdown (link) and provider dropdowns (dependency: endpoint,
// route) must list only options the caller could actually write, filtered in
// Go with the exact predicate the store enforces (permit.Covers, reached
// through Base.CanWriteEntity), never offered-and-refused.
//
// pickerFixture builds one owned far end and one foreign far end for each of
// the three pickers, plus the near-end interface/service the create forms
// hang off. assetIn2/serviceIn2 mirror dep_row_controls_test.go and
// interface_row_controls_test.go's own fixtures: a second in-scope row,
// linked to project alpha here, so a far end can be inside the owner's scope
// without an entity needing to point at itself.
type pickerFixture struct {
	assetIn2   string
	serviceIn2 string

	// ifaceFrom is an unpatched port on fx.assetIn -- the "from" side of any
	// link this fixture posts.
	ifaceFrom string
	// ifaceTargetOwned is an unpatched port on assetIn2 (owned); ifaceTargetForeign
	// is an unpatched port on fx.assetOut (not owned).
	ifaceTargetOwned, ifaceTargetForeign string

	// epOwned is a live endpoint on serviceIn2 (owned); epForeign is a live
	// endpoint on fx.serviceOut (not owned).
	epOwned, epForeign string

	// routeOwned fronts serviceIn2 (owned); routeForeign fronts fx.serviceOut
	// (not owned). Both ids are the route's own id, for the picker; the
	// service each is filtered on is the route's FRONTEND service.
	routeOwned, routeForeign string
}

func setupPickerFixture(t *testing.T, ctx context.Context, h *harness, fx *boundaryFixtures) *pickerFixture {
	t.Helper()
	admin := domain.AdministratorPermit(domain.SystemActor)

	assetIn2 := mustBoundaryAsset(t, ctx, h, "t-picker-asset-2")
	assetLink, err := domain.NewProjectAssetLink(fx.projectAlpha, assetIn2, domain.ProjectOwns, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building the second in-scope asset link: %v", err)
	}
	if err := h.store.LinkProjectAsset(ctx, admin, assetLink); err != nil {
		t.Fatalf("linking the second in-scope asset to alpha: %v", err)
	}

	env, err := h.store.ListEnvironments(ctx)
	if err != nil || len(env) == 0 {
		t.Fatalf("listing environments for the picker fixture: %v", err)
	}
	serviceIn2 := mustBoundaryService(t, ctx, h, "t-picker-svc-2", env[0].ID)
	svcLink, err := domain.NewProjectServiceLink(fx.projectAlpha, serviceIn2, domain.ProjectOwns, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building the second in-scope service link: %v", err)
	}
	if err := h.store.LinkProjectService(ctx, admin, svcLink); err != nil {
		t.Fatalf("linking the second in-scope service to alpha: %v", err)
	}

	mkIface := func(assetID, name string) string {
		iface, err := domain.NewInterface(store.NewID(), assetID, name, domain.FFRJ45)
		if err != nil {
			t.Fatalf("building interface %s: %v", name, err)
		}
		if err := h.store.CreateInterface(ctx, admin, iface); err != nil {
			t.Fatalf("creating interface %s: %v", name, err)
		}
		return iface.ID
	}
	ifaceFrom := mkIface(fx.assetIn, "picker-fixture-from")
	ifaceTargetOwned := mkIface(assetIn2, "picker-fixture-target-owned")
	ifaceTargetForeign := mkIface(fx.assetOut, "picker-fixture-target-foreign")

	mkEndpoint := func(serviceID, name string) string {
		port := 5433
		ep, err := domain.NewEndpoint(store.NewID(), serviceID, name, domain.ProtoTCP, &port, domain.BindHost)
		if err != nil {
			t.Fatalf("building endpoint %s: %v", name, err)
		}
		if err := h.store.CreateEndpoint(ctx, admin, ep); err != nil {
			t.Fatalf("creating endpoint %s: %v", name, err)
		}
		return ep.ID
	}
	epOwned := mkEndpoint(serviceIn2, "picker-fixture-ep-owned")
	epForeign := mkEndpoint(fx.serviceOut, "picker-fixture-ep-foreign")

	mkRoute := func(serviceID, name string) string {
		frontend := mkEndpoint(serviceID, name+"-front")
		pool := &domain.BackendPool{ID: store.NewID(), ServiceID: serviceID, Name: name + "-pool"}
		if err := h.store.CreateBackendPool(ctx, admin, pool); err != nil {
			t.Fatalf("creating backend pool for route %s: %v", name, err)
		}
		r, err := domain.NewRoute(store.NewID(), frontend, "default", pool.ID)
		if err != nil {
			t.Fatalf("building route %s: %v", name, err)
		}
		if err := h.store.CreateRoute(ctx, admin, r); err != nil {
			t.Fatalf("creating route %s: %v", name, err)
		}
		return r.ID
	}
	routeOwned := mkRoute(serviceIn2, "picker-fixture-route-owned")
	routeForeign := mkRoute(fx.serviceOut, "picker-fixture-route-foreign")

	return &pickerFixture{
		assetIn2:           assetIn2,
		serviceIn2:         serviceIn2,
		ifaceFrom:          ifaceFrom,
		ifaceTargetOwned:   ifaceTargetOwned,
		ifaceTargetForeign: ifaceTargetForeign,
		epOwned:            epOwned,
		epForeign:          epForeign,
		routeOwned:         routeOwned,
		routeForeign:       routeForeign,
	}
}

// selectBlock isolates the option list inside a named <select id="...">, so
// an assertion about what one picker offers cannot accidentally match a
// different picker, or an unrelated id string, elsewhere on the same page.
func selectBlock(t *testing.T, page, selectID string) string {
	t.Helper()
	marker := `id="` + selectID + `"`
	mi := strings.Index(page, marker)
	if mi < 0 {
		t.Fatalf("no <select %s> found on the page at all", marker)
	}
	end := strings.Index(page[mi:], "</select>")
	if end < 0 {
		t.Fatalf("<select %s> never closes", marker)
	}
	return page[mi : mi+end]
}

// TestLinkFarEndPickerIsFilteredToOwnedAssets pins Task 3's first picker: the
// "to" side of a new cable (link_form's Targets) lists only ports on assets
// the caller may write.
func TestLinkFarEndPickerIsFilteredToOwnedAssets(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			pfx := setupPickerFixture(t, ctx, h, fx)

			// Positive control: an Administrator's picker lists BOTH the
			// owned and the foreign target -- proving the option renders at
			// all before the owner's narrower view is asserted.
			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/assets/"+fx.assetIn, false))
			adminSelect := selectBlock(t, adminBody, "link-target")
			if !strings.Contains(adminSelect, `value="`+pfx.ifaceTargetOwned+`"`) {
				t.Fatalf("Administrator's link-target picker does not list %s -- the option is "+
					"not proven to render at all", pfx.ifaceTargetOwned)
			}
			if !strings.Contains(adminSelect, `value="`+pfx.ifaceTargetForeign+`"`) {
				t.Fatalf("Administrator's link-target picker does not list %s -- the option is "+
					"not proven to render at all", pfx.ifaceTargetForeign)
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/assets/"+fx.assetIn, false))
			ownerSelect := selectBlock(t, ownerBody, "link-target")
			if !strings.Contains(ownerSelect, `value="`+pfx.ifaceTargetOwned+`"`) {
				t.Errorf("the owner's link-target picker does not list %s, a port on an asset "+
					"they own -- the filter is too strict", pfx.ifaceTargetOwned)
			}
			if strings.Contains(ownerSelect, `value="`+pfx.ifaceTargetForeign+`"`) {
				t.Errorf("the owner's link-target picker lists %s, a port on a FOREIGN asset -- "+
					"offering it is the defect the two-ended-write sweep removed from the row "+
					"controls: a control that looks available and isn't", pfx.ifaceTargetForeign)
			}
		})
	}
}

// TestDependencyEndpointPickerIsFilteredToOwnedServices pins the second
// picker: the endpoint provider dropdown lists only endpoints on services the
// caller may write.
func TestDependencyEndpointPickerIsFilteredToOwnedServices(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			pfx := setupPickerFixture(t, ctx, h, fx)

			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/services/"+fx.serviceIn, false))
			adminSelect := selectBlock(t, adminBody, "dep-endpoint")
			if !strings.Contains(adminSelect, `value="`+pfx.epOwned+`"`) {
				t.Fatalf("Administrator's dep-endpoint picker does not list %s", pfx.epOwned)
			}
			if !strings.Contains(adminSelect, `value="`+pfx.epForeign+`"`) {
				t.Fatalf("Administrator's dep-endpoint picker does not list %s", pfx.epForeign)
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/services/"+fx.serviceIn, false))
			ownerSelect := selectBlock(t, ownerBody, "dep-endpoint")
			if !strings.Contains(ownerSelect, `value="`+pfx.epOwned+`"`) {
				t.Errorf("the owner's dep-endpoint picker does not list %s, an endpoint on a "+
					"service they own", pfx.epOwned)
			}
			if strings.Contains(ownerSelect, `value="`+pfx.epForeign+`"`) {
				t.Errorf("the owner's dep-endpoint picker lists %s, an endpoint on a FOREIGN "+
					"service", pfx.epForeign)
			}
		})
	}
}

// TestDependencyRoutePickerIsFilteredToOwnedServices pins the third picker:
// the route provider dropdown lists only routes whose FRONTEND service the
// caller may write -- the same service authorizeDependencySubjects resolves
// for a route provider.
func TestDependencyRoutePickerIsFilteredToOwnedServices(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			pfx := setupPickerFixture(t, ctx, h, fx)

			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/services/"+fx.serviceIn, false))
			adminSelect := selectBlock(t, adminBody, "dep-route")
			if !strings.Contains(adminSelect, `value="`+pfx.routeOwned+`"`) {
				t.Fatalf("Administrator's dep-route picker does not list %s", pfx.routeOwned)
			}
			if !strings.Contains(adminSelect, `value="`+pfx.routeForeign+`"`) {
				t.Fatalf("Administrator's dep-route picker does not list %s", pfx.routeForeign)
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/services/"+fx.serviceIn, false))
			ownerSelect := selectBlock(t, ownerBody, "dep-route")
			if !strings.Contains(ownerSelect, `value="`+pfx.routeOwned+`"`) {
				t.Errorf("the owner's dep-route picker does not list %s, a route fronting a "+
					"service they own", pfx.routeOwned)
			}
			if strings.Contains(ownerSelect, `value="`+pfx.routeForeign+`"`) {
				t.Errorf("the owner's dep-route picker lists %s, a route fronting a FOREIGN "+
					"service", pfx.routeForeign)
			}
		})
	}
}

// TestEmptyPickerShowsHintNotBlankSelect proves the wording obligation:
// when filtering leaves nothing, the form says so rather than presenting a
// blank dropdown that reads as a broken page. This owner's project holds
// only fx.assetIn, and assetIn is given no port of its own here, so the
// far-end picker is filtered to nothing even though the estate (via
// pfx.ifaceTargetForeign) genuinely has an unpatched port to offer an
// Administrator -- proving this is the filter emptying the picker, not an
// empty estate.
func TestEmptyPickerShowsHintNotBlankSelect(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			admin := domain.AdministratorPermit(domain.SystemActor)
			iface, err := domain.NewInterface(store.NewID(), fx.assetOut, "empty-picker-foreign-target", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building the foreign interface: %v", err)
			}
			if err := h.store.CreateInterface(ctx, admin, iface); err != nil {
				t.Fatalf("creating the foreign interface: %v", err)
			}

			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/assets/"+fx.assetIn, false))
			adminSelect := selectBlock(t, adminBody, "link-target")
			if !strings.Contains(adminSelect, `value="`+iface.ID+`"`) {
				t.Fatalf("Administrator's link-target picker does not list %s -- the estate does "+
					"not actually have a foreign option here, so an empty owner picker below would "+
					"prove nothing", iface.ID)
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/assets/"+fx.assetIn, false))
			ownerSelect := selectBlock(t, ownerBody, "link-target")
			if strings.Contains(ownerSelect, `value="`+iface.ID+`"`) {
				t.Fatalf("the owner's link-target picker lists the foreign port %s", iface.ID)
			}
			if strings.Contains(ownerSelect, "<option") {
				t.Fatalf("the owner's link-target picker still carries an <option> after "+
					"filtering: %q", ownerSelect)
			}
			if !strings.Contains(ownerBody, "There is nothing here you can cable to") {
				t.Errorf("an emptied link-target picker carries no explanatory hint -- an empty " +
					"dropdown with no explanation reads as a broken page")
			}
		})
	}
}

// TestForgedLinkSubmissionOutOfScopeIsRefused: filtering is a courtesy, never
// the enforcement. A project owner who edits the DOM (or scripts a raw POST)
// to name a foreign far end must still be refused at the store, and nothing
// may be written.
func TestForgedLinkSubmissionOutOfScopeIsRefused(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			pfx := setupPickerFixture(t, ctx, h, fx)

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			token := h.csrfToken("/assets/" + fx.assetIn)

			changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)
			linksBefore := h.count(`SELECT COUNT(*) FROM link`)

			resp := h.post("/links", url.Values{
				"csrf_token":          {token},
				"asset_id":            {fx.assetIn},
				"a_interface_id":      {pfx.ifaceFrom},
				"target_interface_id": {pfx.ifaceTargetForeign},
			}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("POST /links naming a foreign far end returned %d, want %d",
					resp.StatusCode, http.StatusForbidden)
			}
			if after := h.count(`SELECT COUNT(*) FROM link`); after != linksBefore {
				t.Errorf("link count moved from %d to %d on a refused forged submission",
					linksBefore, after)
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBefore {
				t.Errorf("change_log grew from %d to %d on a refused forged submission",
					changeLogBefore, after)
			}
		})
	}
}

// TestForgedDependencySubmissionOutOfScopeIsRefused is
// TestForgedLinkSubmissionOutOfScopeIsRefused's dependency-side twin.
func TestForgedDependencySubmissionOutOfScopeIsRefused(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			pfx := setupPickerFixture(t, ctx, h, fx)

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			token := h.csrfToken("/services/" + fx.serviceIn)

			changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)
			depsBefore := h.count(`SELECT COUNT(*) FROM dependency`)

			resp := h.post("/services/"+fx.serviceIn+"/dependencies", url.Values{
				"csrf_token":           {token},
				"provider_endpoint_id": {pfx.epForeign},
				"nature":               {domain.NatureHard},
				"failure_mode":         {"forged submission fixture"},
			}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("POST /services/%s/dependencies naming a foreign provider returned %d, want %d",
					fx.serviceIn, resp.StatusCode, http.StatusForbidden)
			}
			if after := h.count(`SELECT COUNT(*) FROM dependency`); after != depsBefore {
				t.Errorf("dependency count moved from %d to %d on a refused forged submission",
					depsBefore, after)
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBefore {
				t.Errorf("change_log grew from %d to %d on a refused forged submission",
					changeLogBefore, after)
			}
		})
	}
}

// TestCreateFormsHiddenOnForeignPage: the forms themselves are gated on the
// near end the operator is already on, so a project owner viewing an asset
// or service they do not own is not offered a create form at all -- not
// even one whose picker would immediately come back empty.
func TestCreateFormsHiddenOnForeignPage(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)

			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			assetBody := body(t, h.get("/assets/"+fx.assetOut, false))
			if strings.Contains(assetBody, `id="link-form"`) {
				t.Errorf("GET /assets/%s (a foreign asset) carries the link-form -- the create "+
					"form must be gated on the near end (this asset), not merely on whether any "+
					"far end happens to be writable", fx.assetOut)
			}

			serviceBody := body(t, h.get("/services/"+fx.serviceOut, false))
			if strings.Contains(serviceBody, `id="dependency-form"`) {
				t.Errorf("GET /services/%s (a foreign service) carries the dependency-form -- the "+
					"create form must be gated on the near end (this service, the consumer), not "+
					"merely on whether any far end happens to be writable", fx.serviceOut)
			}
		})
	}
}
