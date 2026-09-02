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

// Item 1 (2026-09-02 group-a-1-1 round): three deliberate exceptions from
// commit a3f0d61 that no test pinned. Reverting all three left the whole
// internal/web suite green -- each one changes what a real request renders
// or does, and each is reachable. This file pins all three.

// ---------------------------------------------------------------------------
// 1a. asset_detail.html:311 -- (.CanWriteEntity "asset" .Asset.ID), NOT
// .IsAdmin. This is the load-bearing one: an under-offer produces no error
// anywhere (the button just is not there), so nothing would ever report it.
// A project owner who owns the asset AND carries the cost-visibility grant
// must see the cost panel's write controls (Add cost, Reprice, Set who it
// covers) on their OWN asset.
func TestAProjectOwnerWithCostGrantSeesCostControlsOnTheirOwnAsset(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			admin := domain.AdministratorPermit(domain.SystemActor)

			ownerRow, err := h.store.GetUserByUsername(ctx, boundaryOwnerUser)
			if err != nil {
				t.Fatalf("looking up %s: %v", boundaryOwnerUser, err)
			}
			if err := h.store.SetUserCostVisibility(ctx, admin, ownerRow.ID, true); err != nil {
				t.Fatalf("granting can_see_costs: %v", err)
			}

			// Positive control first: an Administrator sees the same
			// control on the same asset, so the marker itself is proven to
			// render somewhere before the owner check below means anything.
			h.login(boundaryAdminUser, boundaryAdminPassword)
			addCostForm := `action="/assets/` + fx.assetIn + `/costs"`
			adminBody := body(t, h.get("/assets/"+fx.assetIn, false))
			if !strings.Contains(adminBody, addCostForm) {
				t.Fatalf("GET /assets/%s as Administrator does not carry %q -- the cost panel "+
					"is not proven to render its write form at all", fx.assetIn, addCostForm)
			}
			h.logout()

			// The owner: owns fx.assetIn (project alpha) AND carries the
			// grant just set above. .CanWriteEntity "asset" fx.assetIn asks
			// the same question the store's authorizeCostSubject asks, so
			// this must render exactly like it does for the Administrator.
			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/assets/"+fx.assetIn, false))
			if !strings.Contains(ownerBody, addCostForm) {
				t.Errorf("GET /assets/%s as a granted project owner, on their OWN asset, does "+
					"not carry %q -- asset_cost is ScopeSubjectDerived (role.go) and a project "+
					"owner who owns the asset can write it; gating this panel on .IsAdmin "+
					"instead of .CanWriteEntity would silently take the control away with no "+
					"error anywhere to notice it by", fx.assetIn, addCostForm)
			}

			// And the owner does NOT see it on an asset outside their
			// project, even carrying the same grant -- CanWriteEntity must
			// still be asking about THIS asset, not the caller's role alone.
			ownerOutBody := body(t, h.get("/assets/"+fx.assetOut, false))
			outCostForm := `action="/assets/` + fx.assetOut + `/costs"`
			if strings.Contains(ownerOutBody, outCostForm) {
				t.Errorf("GET /assets/%s as a granted project owner carries %q for an asset "+
					"outside their project", fx.assetOut, outCostForm)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 1b. projects.go:308 -- base.IsAdmin, NOT base.CanWrite, for the tags
// panel. A project's own entity_tag set folds into the "project" change_log
// row, and "project" is ScopeEstateConfig (Administrator-only), same as
// editing the project row itself. Reverting to base.CanWrite offers a
// project owner an "Edit tags" picker on their OWN project that the store
// then 403s on submit.
func TestAProjectOwnerSeesNoTagsEditControlOnTheirOwnProject(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)

			editTagsForm := `action="/projects/` + fx.projectAlpha + `/tags"`

			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/projects/"+fx.projectAlpha, false))
			if !strings.Contains(adminBody, editTagsForm) {
				t.Fatalf("GET /projects/%s as Administrator does not carry %q -- the tags "+
					"panel is not proven to render its edit form at all", fx.projectAlpha, editTagsForm)
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/projects/"+fx.projectAlpha, false))
			if strings.Contains(ownerBody, editTagsForm) {
				t.Errorf("GET /projects/%s as the OWNER of that project unexpectedly carries "+
					"%q -- project's own entity_tag set is ScopeEstateConfig, "+
					"Administrator-only, and this control 403s on submit for a project owner",
					fx.projectAlpha, editTagsForm)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Item 5 (2026-09-02 group-a-1-1 round): internal/web/handlers/projects.go
// still gated the project's own EDIT FORM on base.CanWrite && base.EditRow
// == project.ID, eight lines above the tags fix that already used
// base.IsAdmin. Only the Edit LINK was hidden from a project owner; typing
// ?edit=<id> onto their own project's URL still rendered the edit form
// itself -- action="/projects/<id>", a genuine POST target the store then
// 403s. Both halves proven here: the form is absent from the page a project
// owner can navigate to directly, and the POST it would have submitted is
// still refused server-side with no change_log row (defence in depth, not
// a substitute for the render fix).
func TestAProjectOwnerCannotReachTheirOwnProjectsEditFormByURL(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)

			editForm := `action="/projects/` + fx.projectAlpha + `"`

			// Positive control: an Administrator navigating the same
			// ?edit= URL on the same project sees the form.
			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/projects/"+fx.projectAlpha+"?edit="+fx.projectAlpha, false))
			if !strings.Contains(adminBody, editForm) {
				t.Fatalf("GET /projects/%s?edit=%[1]s as Administrator does not carry %q -- "+
					"the edit form is not proven to render at all", fx.projectAlpha, editForm)
			}
			h.logout()

			// The owner of that SAME project, typing the same URL: the
			// link is already absent (base.IsAdmin gates it), but this
			// proves the form the link would have pointed at is gone too.
			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/projects/"+fx.projectAlpha+"?edit="+fx.projectAlpha, false))
			if strings.Contains(ownerBody, editForm) {
				t.Errorf("GET /projects/%s?edit=%[1]s as the OWNER of that project unexpectedly "+
					"carries %q -- editing the project row itself is ScopeEstateConfig, "+
					"Administrator-only, same as the tags panel eight lines below it",
					fx.projectAlpha, editForm)
			}

			// Defence in depth: even without the form, the POST it would
			// have submitted is still refused, and nothing is written.
			changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)
			resp := h.post("/projects/"+fx.projectAlpha, url.Values{
				"csrf_token":  {h.csrfToken("/projects/" + fx.projectAlpha)},
				"row_version": {"1"}, "code": {"t-alpha"}, "name": {"Alpha (tampered)"},
				"lifecycle": {"active"},
			}, false)
			respBody := drainedBody(t, resp)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("owner POSTing to their own project's edit route = %d (body %q), want 403",
					resp.StatusCode, truncate(respBody))
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBefore {
				t.Errorf("change_log grew from %d to %d on a refused project edit",
					changeLogBefore, after)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 1c. vlan_detail.html:54/64/84 -- deliberately LEFT ON .CanWrite, because
// port-to-VLAN membership is subject-derived through
// authorizeInterfaceSubject: scoped to the port's OWNING ASSET, not
// estate-wide. No test drove POST /vlans/{id}/ports as a project owner at
// all before this. Both directions, on the SAME vlan: a port on the
// owner's own asset succeeds (303, one change_log row); a port on an asset
// outside their project is refused (403, no change_log row) -- proving the
// scoping is per-port, not merely "any project owner may write any VLAN".
func TestAProjectOwnerVLANPortWriteIsSubjectDerived(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			admin := domain.AdministratorPermit(domain.SystemActor)

			// One interface on the owner's own asset, one on an asset
			// outside their project -- created as Administrator, since
			// interface creation is not what this test is proving.
			ifaceIn, err := domain.NewInterface(store.NewID(), fx.assetIn, "eth-sweep-in", "rj45")
			if err != nil {
				t.Fatalf("building the in-scope interface: %v", err)
			}
			if err := h.store.CreateInterface(ctx, admin, ifaceIn); err != nil {
				t.Fatalf("creating the in-scope interface: %v", err)
			}
			ifaceOut, err := domain.NewInterface(store.NewID(), fx.assetOut, "eth-sweep-out", "rj45")
			if err != nil {
				t.Fatalf("building the out-of-scope interface: %v", err)
			}
			if err := h.store.CreateInterface(ctx, admin, ifaceOut); err != nil {
				t.Fatalf("creating the out-of-scope interface: %v", err)
			}

			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			// Direction 1: the owner's own port. Every OTHER check
			// (RequireWrite, CSRF) would pass regardless of subject
			// scoping, so a refusal here could only be
			// authorizeInterfaceSubject running out of scope on a port it
			// actually should admit.
			changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)
			respIn := h.post("/vlans/"+fx.vlanID+"/ports", url.Values{
				"csrf_token":   {h.csrfToken("/vlans/" + fx.vlanID)},
				"interface_id": {ifaceIn.ID}, "mode": {"untagged"},
			}, false)
			bodyIn := drainedBody(t, respIn)
			if respIn.StatusCode != http.StatusSeeOther {
				t.Fatalf("owner adding their OWN port to the VLAN = %d (body %q), want 303",
					respIn.StatusCode, truncate(bodyIn))
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBefore+1 {
				t.Errorf("change_log = %d after the owner's own-port write, want %d "+
					"(exactly one committed write)", after, changeLogBefore+1)
			}

			// Direction 2: a port on an asset outside the owner's project,
			// same VLAN, same route. Refused, and nothing written --
			// proving the scope is per-port, not "any project owner may
			// write any VLAN's membership".
			changeLogBeforeOut := h.count(`SELECT COUNT(*) FROM change_log`)
			respOut := h.post("/vlans/"+fx.vlanID+"/ports", url.Values{
				"csrf_token":   {h.csrfToken("/vlans/" + fx.vlanID)},
				"interface_id": {ifaceOut.ID}, "mode": {"untagged"},
			}, false)
			bodyOut := drainedBody(t, respOut)
			if respOut.StatusCode != http.StatusForbidden {
				t.Fatalf("owner adding an OUT-OF-SCOPE port to the VLAN = %d (body %q), want 403",
					respOut.StatusCode, truncate(bodyOut))
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBeforeOut {
				t.Errorf("change_log grew from %d to %d on a refused out-of-scope port write",
					changeLogBeforeOut, after)
			}
		})
	}
}
