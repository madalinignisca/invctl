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
// Step 2, test 2: estate-wide configuration stays exactly as CanWrite
// already had it BEFORE WP-G1 Task 13's flip -- a project owner sees no
// write control on a team or a tag page, regardless of which project they
// own. That was true when this test was written because CanWrite(project
// owner) was unconditionally false; Task 13 made it true (it now means "may
// reach a write-gated route", not "may write everything" -- see
// auth.CanWrite's own comment), and teams.html/tags.html gate their
// creation forms on `.CanWrite` PAGE-WIDE, never converted to the
// entity-scoped CanWriteEntity check Step 1 gave the asset/service/circuit
// templates (these pages have no entity to scope one to in the first place
// -- a team or a tag is estate-wide config, docs/rbac-design.md §4). Task 17
// counted 132 `.CanWrite` occurrences across 38 template files with this
// same property; widening all of them is EXPLICITLY DEFERRED (WP-G1 Task
// 13's own brief calls it "a UX defect, not a security one" -- the server
// still refuses every write, proved independently by
// TestAProjectOwnerIsRefusedOnEveryNonProjectLinkableWriteRoute in
// rbac_boundary_test.go, which drives POST /teams and POST /tags directly
// and gets a permit refusal). So this test now pins the controls being
// VISIBLE, not absent -- flip the two Errorf branches back to their
// original "still absent" shape the day that template sweep lands, not
// before.
func TestAProjectOwnerSeesNoEditControlOnAnyTeamOrTagPage(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, _ := setupBoundary(t, eng)
			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			teamsBody := body(t, h.get("/teams", false))
			if !strings.Contains(teamsBody, "Add a team") {
				t.Error("teams page no longer offers \"Add a team\" to a project owner -- " +
					"see this test's own comment: it should today (known, deferred UX gap)")
			}
			if !strings.Contains(teamsBody, `action="/teams"`) {
				t.Error("teams page no longer renders the team-creation form for a project owner -- " +
					"see this test's own comment")
			}

			tagsBody := body(t, h.get("/tags", false))
			if !strings.Contains(tagsBody, "Define a tag") {
				t.Error("tags page no longer offers \"Define a tag\" to a project owner -- " +
					"see this test's own comment: it should today (known, deferred UX gap)")
			}
			if !strings.Contains(tagsBody, `action="/tags"`) {
				t.Error("tags page no longer renders the tag-creation form for a project owner -- " +
					"see this test's own comment")
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
