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

// Withdrawing an environment, through the screen an operator actually uses.
//
// `environment` is referenced by nearly everything -- the write-surface census
// carried it as a decision deferred rather than made. Migration 00065 and
// RetireEnvironment (internal/store/assets.go) are the store half; this is
// the half that says an operator can reach it, and the half that proves the
// asset form does not drop a retired-but-assigned membership out from under
// an unrelated correction (THE TRAP: asset_environment is replaced wholesale
// on every save, and a checkbox the picker stops offering is a checkbox the
// browser stops submitting -- unless it is still rendered, ticked and
// enabled, for the one asset that already carries it).

// mustEnvironmentWeb creates an environment through the store, bypassing the
// form.
func mustEnvironmentWeb(t *testing.T, h *harness, code string) string {
	t.Helper()
	env, err := domain.NewEnvironment(store.NewID(), code, code, domain.EnvRoleProduction,
		true, 3, h.store.Now())
	if err != nil {
		t.Fatalf("building environment %s: %v", code, err)
	}
	if err := h.store.CreateEnvironment(context.Background(),
		domain.AdministratorPermit(domain.SystemActor), env); err != nil {
		t.Fatalf("creating environment %s: %v", code, err)
	}
	return env.ID
}

// mustAssetInEnvironmentWeb creates an asset already carrying envID, through
// the store.
func mustAssetInEnvironmentWeb(t *testing.T, h *harness, name, envID string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), domain.KindServer, name, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := h.store.CreateAsset(context.Background(),
		domain.AdministratorPermit(domain.SystemActor), a, []string{envID}); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

// TestAnEnvironmentCanBeWithdrawnFromTheEnvironmentsPage.
func TestAnEnvironmentCanBeWithdrawnFromTheEnvironmentsPage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := mustEnvironmentWeb(t, h, "withdraw-me")

	// Offered by the page, not just by the router -- this project has shipped
	// a 404 on a button with every handler test green.
	page := body(t, h.get("/environments", false))
	if !strings.Contains(page, "/environments/"+envID+"/retire") {
		t.Fatal("the environments page offers no Withdraw control for a live environment")
	}

	resp := h.post("/environments/"+envID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/environments")},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdrawing returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM environment WHERE id = ?`, envID); got != "retired" {
		t.Errorf("lifecycle = %q after withdrawing, want retired", got)
	}
}

// TestARetiredEnvironmentStaysOnTheListMarkedRetired: what is STORED must
// keep displaying (docs/AUDIT.md) -- the same rule already applied to a
// retired team and a retired custom_field_option.
func TestARetiredEnvironmentStaysOnTheListMarkedRetired(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := mustEnvironmentWeb(t, h, "still-shown")
	resp := h.post("/environments/"+envID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/environments")},
	}, false)
	resp.Body.Close()

	page := body(t, h.get("/environments", false))
	if !strings.Contains(page, "still-shown") {
		t.Error("a retired environment vanished from the list instead of staying, marked")
	}
	if !strings.Contains(page, "retired") {
		t.Error("the list does not mark the retired environment as retired")
	}
	// And it is no longer offered a Withdraw control -- it is already gone.
	if strings.Contains(page, "/environments/"+envID+"/retire") {
		t.Error("a retired environment still offers a Withdraw control")
	}
}

// TestARetiredEnvironmentIsNotOfferedToAnAssetThatNeverCarriedIt: the picker
// half of "excludes retired, but the display keeps showing what is stored".
func TestARetiredEnvironmentIsNotOfferedToAnAssetThatNeverCarriedIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := mustEnvironmentWeb(t, h, "never-carried")
	resp := h.post("/environments/"+envID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/environments")},
	}, false)
	resp.Body.Close()

	freeAsset := mustAssetInEnvironmentWeb(t, h, "unrelated-asset", mustEnvironmentWeb(t, h, "other-live-env"))
	// ?edit= opens the inline correction form -- without it the checkbox
	// group is not rendered at all, and the assertion below would pass
	// vacuously regardless of what the picker offers.
	page := body(t, h.get("/assets/"+freeAsset+"?edit="+freeAsset, false))
	if !strings.Contains(page, `name="environments"`) {
		t.Fatal("the edit form did not render at all, so this test is checking nothing")
	}
	if strings.Contains(page, `value="`+envID+`"`) {
		t.Error("a retired environment the asset never carried is offered as a checkbox option")
	}
}

// TestARetiredEnvironmentAssignmentSurvivesAnUnrelatedAssetSave is THE TRAP
// this task exists to close, driven through the real router and the real
// template, not just the store.
//
// asset_environment is a set replaced wholesale on every save
// (setAssetEnvironments). If the retired environment's checkbox were absent
// or disabled, the browser would submit nothing for it and a save that
// touches only the asset's name would silently drop the assignment -- the
// identical shape as the setDataClasses bug already fixed in this repo
// (nil read as "leave alone", unticking every box sent no key). This test
// posts exactly what a real browser posts: the retired environment's id
// still present in the "environments" field, because the rendered checkbox
// is ticked and enabled.
func TestARetiredEnvironmentAssignmentSurvivesAnUnrelatedAssetSave(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := mustEnvironmentWeb(t, h, "trap-web")
	assetID := mustAssetInEnvironmentWeb(t, h, "trap-web-asset", envID)

	retireResp := h.post("/environments/"+envID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/environments")},
	}, false)
	retireResp.Body.Close()

	// The rendered edit form still offers the retired environment as a
	// checked, ENABLED box -- proving the fix, not assuming it. ?edit= opens
	// the inline correction form on the asset detail page.
	page := body(t, h.get("/assets/"+assetID+"?edit="+assetID, false))
	if !strings.Contains(page, `name="environments" value="`+envID+`" checked`) {
		t.Fatalf("the asset edit form does not render the retired-but-assigned "+
			"environment %s as a checked checkbox, so a real browser would submit "+
			"nothing for it on save", envID)
	}

	token := h.csrfToken("/assets/" + assetID)
	resp := h.post("/assets/"+assetID, url.Values{
		"csrf_token":   {token},
		"name":         {"trap-web-asset-renamed"},
		"kind":         {domain.KindServer},
		"lifecycle":    {domain.LifecycleActive},
		"row_version":  {"1"},
		"environments": {envID}, // what the checked, enabled checkbox submits
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body := body(t, resp)
		t.Fatalf("saving an unrelated field returned %d, want 303: %s", resp.StatusCode, body)
	}

	if got := h.lookup(`SELECT name FROM asset WHERE id = ?`, assetID); got != "trap-web-asset-renamed" {
		t.Fatalf("the unrelated field did not save: name = %q", got)
	}
	var n int
	if err := h.store.DB().Reader.Get(&n, h.store.DB().Reader.Rebind(
		`SELECT COUNT(*) FROM asset_environment WHERE asset_id = ? AND environment_id = ?`),
		assetID, envID); err != nil {
		t.Fatalf("checking membership: %v", err)
	}
	if n != 1 {
		t.Error("the retired environment's assignment was dropped by an unrelated field's " +
			"save -- the setDataClasses trap, shipped again")
	}
}

// TestOnlyAnAdministratorCanWithdrawAnEnvironment. environment is
// ScopeEstateConfig, so a project owner's CanWrite does not reach it -- only
// Administrator does.
func TestOnlyAnAdministratorCanWithdrawAnEnvironment(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	envID := mustEnvironmentWeb(t, h, "admin-only-env")

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/environments/"+envID+"/retire", url.Values{
		"csrf_token": {viewer.csrfToken("/environments")},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer got %d withdrawing an environment, want 403", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM environment WHERE id = ?`, envID); got != "active" {
		t.Error("a viewer's withdrawal reached the database")
	}
}
