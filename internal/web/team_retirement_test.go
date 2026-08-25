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

// The retirement confirmation flow, through the real router (WP-G7 piece 2).
// docs/ownership-report-design.md §5.

// mustTeamWeb creates a team through the store, bypassing the form.
func mustTeamWeb(t *testing.T, h *harness, code string) string {
	t.Helper()
	team, err := domain.NewTeam(store.NewID(), domain.TeamSpec{Code: code, Name: code}, h.store.Now())
	if err != nil {
		t.Fatalf("building team %s: %v", code, err)
	}
	if err := h.store.CreateTeam(context.Background(), domain.SystemActor, team); err != nil {
		t.Fatalf("creating team %s: %v", code, err)
	}
	return team.ID
}

// TestTeamRetireConfirmSkipsTheWarningWhenEmpty: a team owning nothing gets a
// plain confirmation, not an empty warning panel or a picker with nothing to
// choose for.
func TestTeamRetireConfirmSkipsTheWarningWhenEmpty(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	teamID := mustTeamWeb(t, h, "empty-team")
	page := body(t, h.get("/teams/"+teamID+"/retire", false))

	if !strings.Contains(page, "Nothing is currently assigned to this team") {
		t.Error("an empty team did not get the plain confirmation")
	}
	if strings.Contains(page, "target_team_id") {
		t.Error("an empty team's confirmation screen still renders the reassignment picker")
	}
	if !strings.Contains(page, `action="/teams/`+teamID+`/retire"`) {
		t.Error("the plain confirmation does not post to the existing retire route")
	}
}

// TestTeamRetireConfirmShowsCountsAndPicker: a team that owns something shows
// what it owns and offers a target picker that excludes the team itself.
func TestTeamRetireConfirmShowsCountsAndPicker(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	teamID := mustTeamWeb(t, h, "busy-team")
	otherID := mustTeamWeb(t, h, "other-team")
	mustAssetOwnedByWeb(t, h, "confirm-screen-asset", teamID)

	page := body(t, h.get("/teams/"+teamID+"/retire", false))

	if !strings.Contains(page, `name="target_team_id"`) {
		t.Error("a team that owns something does not offer a reassignment picker")
	}
	if strings.Contains(page, `value="`+teamID+`"`) {
		t.Error("the retiring team appears as a choice in its own reassignment picker")
	}
	if !strings.Contains(page, otherID) {
		t.Error("another active team is missing from the picker")
	}
	if !strings.Contains(page, "Retire anyway") {
		t.Error("\"retire anyway\" must stay available, and visibly so, alongside the reassignment offer")
	}
}

// TestTeamRetireConfirmRequiresAdmin: a read-only user cannot reach the
// confirmation screen at all -- it exists purely to feed a mutation.
func TestTeamRetireConfirmRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	teamID := mustTeamWeb(t, h, "admin-only-team")
	resp := h.get("/teams/"+teamID+"/retire", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestTeamReassignAndRetireMovesEverythingAndRetires drives the whole flow
// through HTTP: reassignment happens, the outcome is reported, and the team
// ends up retired.
func TestTeamReassignAndRetireMovesEverythingAndRetires(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	fromID := mustTeamWeb(t, h, "moving-team")
	toID := mustTeamWeb(t, h, "receiving-team")
	assetID := mustAssetOwnedByWeb(t, h, "movable-asset", fromID)

	token := h.csrfToken("/teams/" + fromID + "/retire")
	resp := h.post("/teams/"+fromID+"/reassign-retire", url.Values{
		"csrf_token":     {token},
		"target_team_id": {toID},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", resp.StatusCode, page)
	}
	if !strings.Contains(page, "movable-asset") {
		t.Error("the outcome page does not name the reassigned asset")
	}
	if !strings.Contains(page, "reassigned") {
		t.Error("the outcome page does not report the reassignment")
	}

	ctx := context.Background()
	asset, err := h.store.GetAsset(ctx, assetID)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if asset.TeamID == nil || *asset.TeamID != toID {
		t.Errorf("asset team_id = %v, want %s", asset.TeamID, toID)
	}

	team, err := h.store.GetTeam(ctx, fromID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if team.Lifecycle != domain.LifecycleRetired {
		t.Errorf("team lifecycle = %s, want retired", team.Lifecycle)
	}
}

// TestTeamReassignAndRetireRequiresATarget: submitting without choosing a
// team is a validation failure, not a silent no-op or a bare 500.
func TestTeamReassignAndRetireRequiresATarget(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	teamID := mustTeamWeb(t, h, "no-target-team")
	mustAssetOwnedByWeb(t, h, "still-here-asset", teamID)

	token := h.csrfToken("/teams/" + teamID + "/retire")
	resp := h.post("/teams/"+teamID+"/reassign-retire", url.Values{
		"csrf_token":     {token},
		"target_team_id": {""},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(page, "Choose a team") {
		t.Error("the re-rendered form does not explain the missing target")
	}

	team, err := h.store.GetTeam(context.Background(), teamID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if team.Lifecycle == domain.LifecycleRetired {
		t.Error("the team was retired despite the reassignment failing validation")
	}
}

// TestTeamReassignAndRetireRequiresAdmin: the mutation is behind the same
// admin gate as every other write.
func TestTeamReassignAndRetireRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	fromID := mustTeamWeb(t, h, "viewer-cannot-move")
	toID := mustTeamWeb(t, h, "viewer-cannot-receive")

	// A real token, so the request is refused for lacking admin rights
	// specifically -- not merely for lacking a valid CSRF token, which
	// TestCSRFIsEnforced already covers generically.
	token := h.csrfToken("/teams")
	resp := h.post("/teams/"+fromID+"/reassign-retire", url.Values{
		"csrf_token":     {token},
		"target_team_id": {toID},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// mustAssetOwnedByWeb creates an asset owned by teamID, through the store.
func mustAssetOwnedByWeb(t *testing.T, h *harness, name, teamID string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), domain.KindVM, name, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	a.TeamID = &teamID
	if err := h.store.CreateAsset(context.Background(), domain.SystemActor, a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}
