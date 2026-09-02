// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The role-aware UI -- WP-G1 Task 17.
//
// Reuses rbac_boundary_test.go's harness and fixtures wholesale
// (setupBoundary, boundaryFixtures, the three boundary personas): that file
// already builds a project owner assigned to project "alpha", owning one
// asset, one service and one circuit, with a second asset/service/circuit of
// each kind left outside every project the owner holds. This file drives the
// SAME fixtures through actual page renders instead of routescan's
// route-only enumeration, because a rendered page is the one thing that
// route list cannot tell you: whether a specific control is drawn.

// ---------------------------------------------------------------------------
// Step 2, test 1: a project owner sees an edit control on their own asset,
// service and circuit, and not on the ones outside their project -- on ONE
// PAGE for circuits (the list page renders both circuitIn and circuitOut
// together), and across two GETs each for assets and services, which have no
// per-row list control and gate their own edit link on the detail page.
func TestAProjectOwnerSeesEditControlsOnTheirOwnAssetsAndNotOnOthers(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			// Assets: the "Edit" link is entity-specific (asset_detail.html,
			// Base.CanWriteEntity), so it appears on the owner's own asset
			// page and not on the outside one.
			assetInBody := body(t, h.get("/assets/"+fx.assetIn, false))
			wantAssetEdit := fmt.Sprintf(`/assets/%s?edit=%s#edit`, fx.assetIn, fx.assetIn)
			if !strings.Contains(assetInBody, wantAssetEdit) {
				t.Errorf("owned asset page missing its Edit link %q", wantAssetEdit)
			}
			assetOutBody := body(t, h.get("/assets/"+fx.assetOut, false))
			dontWantAssetEdit := fmt.Sprintf(`/assets/%s?edit=%s#edit`, fx.assetOut, fx.assetOut)
			if strings.Contains(assetOutBody, dontWantAssetEdit) {
				t.Errorf("out-of-scope asset page unexpectedly carries the Edit link %q", dontWantAssetEdit)
			}

			// Services: same shape.
			serviceInBody := body(t, h.get("/services/"+fx.serviceIn, false))
			wantServiceEdit := fmt.Sprintf(`/services/%s?edit=%s#edit`, fx.serviceIn, fx.serviceIn)
			if !strings.Contains(serviceInBody, wantServiceEdit) {
				t.Errorf("owned service page missing its Edit link %q", wantServiceEdit)
			}
			serviceOutBody := body(t, h.get("/services/"+fx.serviceOut, false))
			dontWantServiceEdit := fmt.Sprintf(`/services/%s?edit=%s#edit`, fx.serviceOut, fx.serviceOut)
			if strings.Contains(serviceOutBody, dontWantServiceEdit) {
				t.Errorf("out-of-scope service page unexpectedly carries the Edit link %q", dontWantServiceEdit)
			}

			// Circuits: ONE page, both rows -- circuit_list.html's Cease
			// button is the only entity-specific control this suite has that
			// lives on a list rather than a detail page.
			circuitListBody := body(t, h.get("/circuits", false))
			wantCease := fmt.Sprintf(`/circuits/%s/retire`, fx.circuitIn)
			if !strings.Contains(circuitListBody, wantCease) {
				t.Errorf("circuit list missing the owned circuit's Cease form (action %q)", wantCease)
			}
			dontWantCease := fmt.Sprintf(`/circuits/%s/retire`, fx.circuitOut)
			if strings.Contains(circuitListBody, dontWantCease) {
				t.Errorf("circuit list unexpectedly carries the out-of-scope circuit's Cease form (action %q)", dontWantCease)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Step 2, test 2: estate-wide configuration -- FLIPPED BACK, task 6 (the
// `.CanWrite` template sweep). This test used to pin the deferred UX gap
// this comment used to describe: teams.html/tags.html gated their creation
// forms on page-wide `.CanWrite`, which WP-G1 Task 13 made true for a
// project owner even though `team`/`tag` are ScopeEstateConfig
// (internal/domain/role.go) and every write was still refused server-side
// (TestAProjectOwnerIsRefusedOnEveryNonProjectLinkableWriteRoute in
// rbac_boundary_test.go proved that independently, driving POST /teams and
// POST /tags directly). Task 6's census found both pages among the 25 files
// still gated on the page-wide flag and switched them to `.IsAdmin` -- the
// same answer CanWriteEntity gives an asset/service/circuit page, just with
// no entity to scope one to (a team or a tag is estate-wide, not owned by
// any one project -- docs/rbac-design.md §4). This test now pins the
// controls being ABSENT again, matching what a project owner could
// legitimately do all along.
//
// Item 2 (2026-09-02 group-a-1-1 round): this pinned exactly two of the 24
// files task 6's census flipped -- team_list.html and tag_list.html.
// Reverting every OTHER flip left the whole internal/web suite green, which
// means a project owner was re-offered a creation form on roughly 15 other
// pages, each one 403ing on submit, and nothing here would have said so.
// Now a table over every swept page task 6's own commit (a3f0d61) touched,
// checked two ways per row the same way the money-visibility census does:
// the marker must be ABSENT for the project owner (the regression this
// guards) and PRESENT for an Administrator on the very same fetch (the
// positive control -- without it a page that happened to 403 outright, or
// render nothing for an unrelated reason, would pass having proven nothing).
//
// fhrp_detail.html and l2vpn_detail.html need a real group/overlay row to
// reach their write form at all -- the seed fixture carries none of either
// -- so their path funcs create one through the real handler first, the
// same way money_visibility_test.go's circuit case adds a fixture cost line
// before either viewer checks the page. ownership_report.html's admin-only
// panel is reachable only when the estate has an unowned entity, which nothing
// here guarantees, so it is left out rather than risk an inert positive
// control; flagged as a follow-up, not silently dropped.
var sweptEstateConfigPages = []struct {
	name string
	// path does any admin-only setup a route needs (creating a fixture row
	// through the real handler) and returns the URL to fetch. Runs already
	// logged in as boundaryAdminUser.
	path func(t *testing.T, h *harness) string
	// marker is the control -- a creation form's action, or its heading --
	// that only .IsAdmin renders.
	marker string
}{
	{"teams", func(t *testing.T, h *harness) string { return "/teams" }, `action="/teams"`},
	{"tags", func(t *testing.T, h *harness) string { return "/tags" }, `action="/tags"`},
	{"catalogue", func(t *testing.T, h *harness) string { return "/catalogue" }, `action="/catalogue/types"`},
	{"certificates", func(t *testing.T, h *harness) string { return "/certificates" }, `action="/certificates"`},
	{"clusters", func(t *testing.T, h *harness) string { return "/clusters" }, `action="/clusters"`},
	{"custom fields", func(t *testing.T, h *harness) string { return "/custom-fields" }, `action="/custom-fields"`},
	{"environments", func(t *testing.T, h *harness) string { return "/environments" }, `action="/environments"`},
	{"redundancy list", func(t *testing.T, h *harness) string { return "/redundancy" }, `action="/redundancy"`},
	{"inflation", func(t *testing.T, h *harness) string { return "/inflation" }, `action="/inflation"`},
	{"overlays list", func(t *testing.T, h *harness) string { return "/overlays" }, `action="/overlays"`},
	{"networks", func(t *testing.T, h *harness) string { return "/network" }, `action="/network/derive"`},
	{"power", func(t *testing.T, h *harness) string { return "/power" }, `action="/power/feeds"`},
	{"prefixes", func(t *testing.T, h *harness) string { return "/prefixes" }, `action="/ip-ranges"`},
	{"registry", func(t *testing.T, h *harness) string { return "/allocations" }, `action="/allocations"`},
	{"vlans", func(t *testing.T, h *harness) string { return "/vlans" }, `action="/vlans"`},
	{
		"redundancy detail", func(t *testing.T, h *harness) string {
			resp := h.post("/redundancy", url.Values{
				"csrf_token": {h.csrfToken("/redundancy")},
				"name":       {"sweep-fixture-group"}, "protocol": {"vrrp2"}, "group_number": {"77"},
			}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("creating the fhrp group fixture returned %d, want a redirect", resp.StatusCode)
			}
			id := h.lookup(`SELECT id FROM fhrp_group WHERE name = ?`, "sweep-fixture-group")
			if id == "" {
				t.Fatal("fhrp group fixture was not created")
			}
			return "/redundancy/" + id
		},
		`action="/redundancy/`, // the member-add form's action is /redundancy/{id}/members
	},
	{
		"overlay detail", func(t *testing.T, h *harness) string {
			resp := h.post("/overlays", url.Values{
				"csrf_token": {h.csrfToken("/overlays")},
				"name":       {"sweep-fixture-overlay"}, "kind": {"vxlan"}, "identifier": {"10099"},
			}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("creating the l2vpn fixture returned %d, want a redirect", resp.StatusCode)
			}
			id := h.lookup(`SELECT id FROM l2vpn WHERE name = ?`, "sweep-fixture-overlay")
			if id == "" {
				t.Fatal("l2vpn fixture was not created")
			}
			return "/overlays/" + id
		},
		`action="/overlays/`, // the termination-add form's action is /overlays/{id}/terminations
	},
}

func TestAProjectOwnerSeesNoEditControlOnAnyTeamOrTagPage(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			for _, tc := range sweptEstateConfigPages {
				t.Run(tc.name, func(t *testing.T) {
					h, _ := setupBoundary(t, eng)
					h.login(boundaryAdminUser, boundaryAdminPassword)
					path := tc.path(t, h)

					adminBody := body(t, h.get(path, false))
					if !strings.Contains(adminBody, tc.marker) {
						t.Fatalf("GET %s as Administrator does not carry %q -- this route is "+
							"not proven to render the control at all, so the project-owner "+
							"check below would prove nothing", path, tc.marker)
					}

					h.logout()
					h.login(boundaryOwnerUser, boundaryOwnerPassword)
					resp := h.get(path, false)
					if resp.StatusCode != http.StatusOK {
						resp.Body.Close()
						t.Fatalf("GET %s as a project owner returned %d, want 200 with the "+
							"control withheld, not a hard refusal", path, resp.StatusCode)
					}
					ownerBody := body(t, resp)
					if strings.Contains(ownerBody, tc.marker) {
						t.Errorf("GET %s unexpectedly carries %q for a project owner -- %s is "+
							"ScopeEstateConfig, Administrator-only, and a control offered here "+
							"403s on submit", path, tc.marker, tc.name)
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Step 2, test 3, and the one that matters: hiding a control is not the
// enforcement. For every control this task hid behind CanWriteEntity, POST
// the write directly against the OUT-OF-SCOPE row anyway -- the exact
// request a browser would never offer, because the button is not there --
// and it must still be refused. A courtesy in the template proves nothing
// about the server; only this does.
func TestHidingAControlIsNotTheEnforcement(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)

			// AssetUpdate and ServiceUpdate re-validate EVERY required field
			// on the submitted form (name, kind, lifecycle for an asset;
			// code, name, kind, environment_id, availability, tier,
			// lifecycle for a service -- neither treats an absent field as
			// "leave unchanged" the way vendor/model/team_id do via
			// submittedString). A bare csrf_token payload fails THAT
			// validation before the write ever reaches the permit, which is
			// a true 403-shaped expectation failing for the wrong reason --
			// the exact trap the sibling E2E spec's own comment describes
			// (tests/e2e/specs/rbac-project-owner-edit-boundary.spec.js).
			// The project owner cannot legitimately read either row's
			// current values (correctly -- neither is in their scope), so
			// this fetches them directly against the store, with no
			// permit involved, purely as fixture setup: it asserts nothing
			// and is not the boundary this test exists to prove.
			assetOut, err := h.store.GetAsset(t.Context(), fx.assetOut)
			if err != nil {
				t.Fatalf("fetching the out-of-scope asset for fixture setup: %v", err)
			}
			serviceOut, err := h.store.GetService(t.Context(), fx.serviceOut)
			if err != nil {
				t.Fatalf("fetching the out-of-scope service for fixture setup: %v", err)
			}

			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			type attempt struct {
				name string
				path string
				form url.Values
			}
			attempts := []attempt{
				{"asset edit", "/assets/" + fx.assetOut, url.Values{
					"name": {assetOut.Name}, "kind": {assetOut.Kind}, "lifecycle": {assetOut.Lifecycle},
				}},
				{"asset retire", "/assets/" + fx.assetOut + "/retire", nil},
				{"asset custom fields", "/assets/" + fx.assetOut + "/custom-fields", nil},
				{"asset tags", "/assets/" + fx.assetOut + "/tags", nil},
				{"service edit", "/services/" + fx.serviceOut, url.Values{
					"code": {serviceOut.Code}, "name": {serviceOut.Name}, "kind": {serviceOut.Kind},
					"environment_id": {serviceOut.EnvironmentID}, "availability": {serviceOut.Availability},
					"tier": {strconv.Itoa(serviceOut.Tier)}, "lifecycle": {serviceOut.Lifecycle},
				}},
				{"service custom fields", "/services/" + fx.serviceOut + "/custom-fields", nil},
				{"service tags", "/services/" + fx.serviceOut + "/tags", nil},
				{"circuit retire", "/circuits/" + fx.circuitOut + "/retire", nil},
			}

			for _, a := range attempts {
				token := boundaryCSRFToken(t, h)
				form := a.form
				if form == nil {
					form = url.Values{}
				}
				form.Set("csrf_token", token)
				resp := h.post(a.path, form, false)
				got := body(t, resp)
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("%s (%s) as a project owner returned %d, want 403 -- body: %s",
						a.name, a.path, resp.StatusCode, truncate(got))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 1.0 item 2: the targeted .CanWrite sweep on the asset/service/circuit pages
// and their topology sub-panels. Since WP-G1 Task 13, .CanWrite means "may
// write SOMETHING" and is true for a project owner too, so a template that
// gated a per-entity control on it (rather than on Base.CanWriteEntity, or
// on .IsAdmin for a type this codebase still refuses a project owner) shows
// a button the server refuses. TestHidingAControlIsNotTheEnforcement above
// already proves the server side; these prove the template side actually
// reads the row it is drawing, not just the session.

// TestAProjectOwnerSeesTheAssetScopedTopologyControlsOnTheirOwnAssetOnly is
// the case a naive fix gets wrong in the OTHER direction from
// TestHidingAControlIsNotTheEnforcement: interface, ip_address and
// journal_entry are ScopeSubjectDerived (1.0 item 1), so a project owner who
// owns the asset may write them -- gating the "add an interface" and "add a
// note" panels on `{{if $.CanWriteEntity "interface" .ID}}` (an entity type
// CanWriteEntity does not recognise, see its own doc comment) would be
// always false and hide a control the server WOULD accept. The correct gate
// is the owning asset's id, and this asserts both halves: present on the
// asset the owner holds, absent on the one they do not.
func TestAProjectOwnerSeesTheAssetScopedTopologyControlsOnTheirOwnAssetOnly(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			inBody := body(t, h.get("/assets/"+fx.assetIn, false))
			if !strings.Contains(inBody, `id="interface-form"`) {
				t.Error("owned asset page missing the interface-creation form " +
					"(interface is asset-scoped, ScopeSubjectDerived -- see domain/role.go)")
			}
			wantNote := fmt.Sprintf(`action="/assets/%s/journal"`, fx.assetIn)
			if !strings.Contains(inBody, wantNote) {
				t.Errorf("owned asset page missing the add-a-note form (action %q) -- "+
					"journal_entry is asset-scoped, ScopeSubjectDerived", wantNote)
			}

			outBody := body(t, h.get("/assets/"+fx.assetOut, false))
			if strings.Contains(outBody, `id="interface-form"`) {
				t.Error("out-of-scope asset page unexpectedly offers the interface-creation form")
			}
			dontWantNote := fmt.Sprintf(`action="/assets/%s/journal"`, fx.assetOut)
			if strings.Contains(outBody, dontWantNote) {
				t.Errorf("out-of-scope asset page unexpectedly carries the add-a-note form (action %q)", dontWantNote)
			}
		})
	}
}

// TestNeitherAssetPageOffersTheCostLineControlToAProjectOwner is the mirror
// case, and its own class claim went stale once WP-1.1 item 3 landed:
// asset_cost is domain.ScopeSubjectDerived now (authorizeCostSubject,
// internal/store/costs.go), not ScopeTopology, so the STORE will accept a
// project owner writing a cost line on their own asset. This UI gate did
// NOT move with it, and that is deliberate, the same reason depRowData's
// own comment gives for dependency (internal/web/handlers/forms.go): an
// honest per-row CanWrite here would have to ask whether the caller's
// permit covers THIS asset, which is exactly authorizeCostSubject's
// derivation, and duplicating a store-layer authorization check in a
// handler-side view model is a second place for the two to drift. So
// cost_panel's dict is still built from .IsAdmin, not from
// Base.CanWriteEntity or Base.CanWrite, and a project owner gets it on
// neither page, including their own asset -- the one case a same-entity-type
// intuition (interface behaves like this, so cost should too) gets wrong.
// TestAnAdministratorGetsTheCostLineControlOnBothAssetPages is the other
// half: an Administrator gets it regardless of project ownership.
//
// Two personas, two tests -- each `h` in this suite carries one session's
// cookie jar, and setupBoundary is the fixture, not a login switch; see
// user_project_assignment_test.go for the same one-persona-per-test shape.
func TestNeitherAssetPageOffersTheCostLineControlToAProjectOwner(t *testing.T) {
	const costForm = `id="cost-kind-asset"`
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			inBody := body(t, h.get("/assets/"+fx.assetIn, false))
			if strings.Contains(inBody, costForm) {
				t.Error("a project owner's OWN asset page offers the add-a-cost form; " +
					"asset_cost is ScopeSubjectDerived at the store layer now, but this UI gate " +
					"deliberately stays on .IsAdmin -- see this test's own doc comment")
			}
			outBody := body(t, h.get("/assets/"+fx.assetOut, false))
			if strings.Contains(outBody, costForm) {
				t.Error("an out-of-scope asset page offers a project owner the add-a-cost form")
			}
		})
	}
}

func TestAnAdministratorGetsTheCostLineControlOnBothAssetPages(t *testing.T) {
	const costForm = `id="cost-kind-asset"`
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryAdminUser, boundaryAdminPassword)

			inBody := body(t, h.get("/assets/"+fx.assetIn, false))
			if !strings.Contains(inBody, costForm) {
				t.Error("an Administrator is missing the add-a-cost form on an asset page")
			}
			outBody := body(t, h.get("/assets/"+fx.assetOut, false))
			if !strings.Contains(outBody, costForm) {
				t.Error("an Administrator is missing the add-a-cost form on a second asset page")
			}
		})
	}
}
