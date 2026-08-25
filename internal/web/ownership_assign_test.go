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
	"regexp"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-G7 piece 3: bulk assignment of the Unowned finding, through the real
// router (docs/ownership-report-design.md §4, §6). team_retirement_test.go's
// helpers (mustTeamWeb, mustAssetOwnedByWeb) are reused; mustUnownedAssetWeb
// below is their unowned counterpart.

func mustUnownedAssetWeb(t *testing.T, h *harness, kind, name string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), kind, name, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := h.store.CreateAsset(context.Background(), domain.SystemActor, a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

var checkboxValuePattern = regexp.MustCompile(`name="ids" value="([^"]+)"`)

// checkboxIDs extracts the ids rendered as candidate checkboxes -- what a
// "select all" click in the browser would toggle.
func checkboxIDs(page string) []string {
	var out []string
	for _, m := range checkboxValuePattern.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestOwnershipReportOffersAssignmentToAnAdmin: the Unowned section renders
// a per-entity-type assignment form for an admin, and stays a plain read-only
// list for a viewer.
func TestOwnershipReportOffersAssignmentToAnAdmin(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	mustUnownedAssetWeb(t, h, domain.KindVM, "report-unowned-vm")

	page := body(t, h.get("/reports/ownership", false))
	if !strings.Contains(page, "report-unowned-vm") {
		t.Fatal("the unowned asset does not appear on the report")
	}
	if !strings.Contains(page, `name="ids"`) {
		t.Error("an admin does not get a checkbox to select the unowned asset")
	}
	if !strings.Contains(page, `name="team_id"`) {
		t.Error("an admin does not get a team picker for the unowned group")
	}

	h2 := newHarness(t)
	h2.login("viewer", "viewer-password")
	viewerPage := body(t, h2.get("/reports/ownership", false))
	if strings.Contains(viewerPage, `name="ids"`) {
		t.Error("a read-only user is offered the bulk-assignment checkboxes")
	}
}

// TestOwnershipAssignMovesExactlyTheSelectedIDs drives the mutation through
// HTTP: only the ids named in the POST move, the rest of the unowned estate
// is untouched, and the result page reports the outcome per item.
func TestOwnershipAssignMovesExactlyTheSelectedIDs(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	target := mustTeamWeb(t, h, "http-bulk-target")
	moveMe := mustUnownedAssetWeb(t, h, domain.KindVM, "http-move-me")
	leaveMe := mustUnownedAssetWeb(t, h, domain.KindVM, "http-leave-me")

	token := h.csrfToken("/reports/ownership")
	resp := h.post("/reports/ownership/assign", url.Values{
		"csrf_token":  {token},
		"entity_type": {"asset"},
		"ids":         {moveMe},
		"team_id":     {target},
	}, true)
	page := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", resp.StatusCode, page)
	}
	if !strings.Contains(page, "http-move-me") {
		t.Error("the result page does not name the assigned asset")
	}
	if !strings.Contains(page, "assigned") {
		t.Error("the result page does not report the assignment")
	}

	ctx := context.Background()
	moved, err := h.store.GetAsset(ctx, moveMe)
	if err != nil {
		t.Fatalf("GetAsset(moved): %v", err)
	}
	if moved.TeamID == nil || *moved.TeamID != target {
		t.Errorf("moved asset team_id = %v, want %s", moved.TeamID, target)
	}

	untouched, err := h.store.GetAsset(ctx, leaveMe)
	if err != nil {
		t.Fatalf("GetAsset(untouched): %v", err)
	}
	if untouched.TeamID != nil {
		t.Errorf("an asset NOT named in ids was moved: team_id = %v", *untouched.TeamID)
	}
}

// TestOwnershipCandidatesRespectsTheFilterOverHTTP: select-all applies to the
// current filtered view, proven end to end -- filter the candidate list by
// kind, take exactly the ids it rendered (what a "select all" click would
// toggle), submit those, and confirm the other kind never moved.
func TestOwnershipCandidatesRespectsTheFilterOverHTTP(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	target := mustTeamWeb(t, h, "http-filter-target")
	switchID := mustUnownedAssetWeb(t, h, domain.KindSwitch, "http-unowned-switch")
	vmID := mustUnownedAssetWeb(t, h, domain.KindVM, "http-unowned-vm")

	filtered := body(t, h.get("/reports/ownership/candidates?entity_type=asset&kind="+domain.KindSwitch, true))
	ids := checkboxIDs(filtered)
	if len(ids) != 1 || ids[0] != switchID {
		t.Fatalf("filtered candidates = %v, want exactly [%s]", ids, switchID)
	}

	token := h.csrfToken("/reports/ownership")
	values := url.Values{
		"csrf_token":  {token},
		"entity_type": {"asset"},
		"team_id":     {target},
	}
	for _, id := range ids {
		values.Add("ids", id)
	}
	resp := h.post("/reports/ownership/assign", values, true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	ctx := context.Background()
	moved, err := h.store.GetAsset(ctx, switchID)
	if err != nil {
		t.Fatalf("GetAsset(switch): %v", err)
	}
	if moved.TeamID == nil || *moved.TeamID != target {
		t.Errorf("the filtered (switch) asset did not move: team_id = %v", moved.TeamID)
	}
	untouched, err := h.store.GetAsset(ctx, vmID)
	if err != nil {
		t.Fatalf("GetAsset(vm): %v", err)
	}
	if untouched.TeamID != nil {
		t.Errorf("select-all moved an asset outside the active filter: vm team_id = %v", *untouched.TeamID)
	}
}

// TestOwnershipAssignSkipsAnEntityClaimedInTheMeantime: an id claimed by
// somebody else between the report render and this submission is skipped and
// reported, never clobbered.
func TestOwnershipAssignSkipsAnEntityClaimedInTheMeantime(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	claimedBy := mustTeamWeb(t, h, "http-quick-claim")
	target := mustTeamWeb(t, h, "http-too-slow")
	assetID := mustUnownedAssetWeb(t, h, domain.KindVM, "http-raced-asset")

	h.exec(`UPDATE asset SET team_id = ? WHERE id = ?`, claimedBy, assetID)

	token := h.csrfToken("/reports/ownership")
	resp := h.post("/reports/ownership/assign", url.Values{
		"csrf_token":  {token},
		"entity_type": {"asset"},
		"ids":         {assetID},
		"team_id":     {target},
	}, true)
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", resp.StatusCode, page)
	}
	if !strings.Contains(page, "no longer unowned") {
		t.Error("the result page does not report the skip")
	}

	after, err := h.store.GetAsset(context.Background(), assetID)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if after.TeamID == nil || *after.TeamID != claimedBy {
		t.Errorf("asset team_id = %v, want it to still be %s", after.TeamID, claimedBy)
	}
}

// TestOwnershipAssignRefusesARetiredTarget: a retired team must not be
// selectable as a bulk-assignment target, exactly as piece 2 already
// enforces for reassignment.
func TestOwnershipAssignRefusesARetiredTarget(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	gone := mustTeamWeb(t, h, "http-gone-bulk-target")
	if err := h.store.RetireTeam(context.Background(), domain.SystemActor, gone); err != nil {
		t.Fatalf("retiring team: %v", err)
	}
	assetID := mustUnownedAssetWeb(t, h, domain.KindVM, "http-untouched-refused-target")

	token := h.csrfToken("/reports/ownership")
	resp := h.post("/reports/ownership/assign", url.Values{
		"csrf_token":  {token},
		"entity_type": {"asset"},
		"ids":         {assetID},
		"team_id":     {gone},
	}, true)
	page := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body: %s", resp.StatusCode, page)
	}

	asset, err := h.store.GetAsset(context.Background(), assetID)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if asset.TeamID != nil {
		t.Error("the asset moved despite the target team being refused")
	}
}

// TestOwnershipAssignRequiresAdmin: a read-only user cannot reach the
// mutation, even with a valid CSRF token.
func TestOwnershipAssignRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	target := mustTeamWeb(t, h, "http-viewer-target")
	assetID := mustUnownedAssetWeb(t, h, domain.KindVM, "http-viewer-cannot-move")

	token := h.csrfToken("/reports/ownership")
	resp := h.post("/reports/ownership/assign", url.Values{
		"csrf_token":  {token},
		"entity_type": {"asset"},
		"ids":         {assetID},
		"team_id":     {target},
	}, true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}

	asset, err := h.store.GetAsset(context.Background(), assetID)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if asset.TeamID != nil {
		t.Error("a read-only user's request moved the asset")
	}
}

// TestOwnershipCandidatesRequiresAdmin: the filtering feed is admin-only too,
// the same reasoning /teams/{id}/retire's own GET already documents.
func TestOwnershipCandidatesRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	resp := h.get("/reports/ownership/candidates?entity_type=asset", true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}
