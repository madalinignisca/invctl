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

// Task 4 of docs/cable-bundles-design.md: the bundle UI. Tasks 1-3 left the
// entity, the store CRUD and the cut view built and unreachable from a
// browser except the cut page itself -- these drive the real router the way
// TestAnFHRPGroupCanBeCorrected drives redundancy's correction routes.

// mustCableWeb declares a live cable directly through the store, for tests
// whose focus is the bundle web layer rather than cabling itself.
func mustCableWeb(t *testing.T, h *harness, aIfaceID, bIfaceID string) string {
	t.Helper()
	l, err := domain.NewLink(store.NewID(), aIfaceID, bIfaceID)
	if err != nil {
		t.Fatalf("building cable: %v", err)
	}
	if err := h.store.CreateLink(context.Background(), domain.AdministratorPermit(domain.SystemActor), l); err != nil {
		t.Fatalf("creating cable: %v", err)
	}
	return l.ID
}

// TestABundleCanBeDeclaredAndCorrected covers the plain create-then-correct
// path through the real routes.
func TestABundleCanBeDeclaredAndCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	create := h.post("/bundles", url.Values{
		"csrf_token":  {h.csrfToken("/bundles")},
		"code":        {"duct-web-1"},
		"name":        {"Web duct 1"},
		"description": {"under the raised floor"},
	}, false)
	create.Body.Close()
	if create.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring a bundle returned %d, want 303", create.StatusCode)
	}
	id := h.lookup(`SELECT id FROM cable_bundle WHERE code = ?`, "duct-web-1")
	if id == "" {
		t.Fatal("the bundle was not created")
	}

	page := body(t, h.get("/bundles/"+id, false))
	rowVersion, ok := attrAfter(page, `name="row_version" value="`, `"`)
	if !ok {
		t.Fatal("the detail page renders no row_version for the bundle's correction form")
	}

	resp := h.post("/bundles/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/bundles/" + id)},
		"code":        {"duct-web-1"},
		"name":        {"Web duct 1 (corrected)"},
		"description": {"the real place this duct runs"},
		"row_version": {rowVersion},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting the bundle returned %d, want 303", resp.StatusCode)
	}

	after := body(t, h.get("/bundles/"+id, false))
	if !strings.Contains(after, "Web duct 1 (corrected)") {
		t.Error("the page does not show the corrected name")
	}
	if !strings.Contains(after, "the real place this duct runs") {
		t.Error("the page does not show the corrected description")
	}
}

// TestBundleMembershipRefusesACableInAnotherLiveBundle is the conflict half:
// the store's own refusal must reach the operator as 422 with the form
// re-rendered and what they picked still selected, not a 500.
func TestBundleMembershipRefusesACableInAnotherLiveBundle(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	a1 := mustServerAssetWeb(t, h, "bundle-conflict-a")
	a2 := mustServerAssetWeb(t, h, "bundle-conflict-b")
	iA := mustInterfaceWeb(t, h, a1, "eth0")
	iB := mustInterfaceWeb(t, h, a2, "eth0")
	cable := mustCableWeb(t, h, iA, iB)

	holderResp := h.post("/bundles", url.Values{
		"csrf_token": {h.csrfToken("/bundles")},
		"code":       {"duct-holder"},
		"name":       {"Holder duct"},
	}, false)
	holderResp.Body.Close()
	holder := h.lookup(`SELECT id FROM cable_bundle WHERE code = ?`, "duct-holder")

	claimResp := h.post("/bundles/"+holder+"/members", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + holder)},
		"link_id":    {cable},
	}, false)
	claimResp.Body.Close()
	if claimResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("claiming the cable in the holder bundle returned %d, want 303", claimResp.StatusCode)
	}

	otherResp := h.post("/bundles", url.Values{
		"csrf_token": {h.csrfToken("/bundles")},
		"code":       {"duct-claimant"},
		"name":       {"Claimant duct"},
	}, false)
	otherResp.Body.Close()
	other := h.lookup(`SELECT id FROM cable_bundle WHERE code = ?`, "duct-claimant")

	resp := h.post("/bundles/"+other+"/members", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + other)},
		"link_id":    {cable},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("claiming an already-bundled cable returned %d, want 422", resp.StatusCode)
	}
	text := body(t, resp)
	if !strings.Contains(text, cable) {
		t.Error("the refusal does not name the conflicting cable")
	}
	if !strings.Contains(text, "duct-holder") {
		t.Error("the refusal does not name the bundle already holding the cable")
	}
	if !strings.Contains(text, `value="`+cable+`" selected`) {
		t.Error("the refused form does not still show the operator's picked cable selected -- " +
			"the typed input must survive the round trip")
	}

	members, err := h.store.ListBundleMembers(context.Background(), other)
	if err != nil {
		t.Fatalf("listing the claimant bundle's members: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("the claimant bundle has %d members after a refused add, want 0", len(members))
	}
}

// TestARetiredCableStaysInItsBundleAcrossAnUnrelatedSave is the trap
// CLAUDE.md names three times over (setDataClasses): a wholesale set
// replacement must not silently drop a row the form never offered a way to
// keep. A retired cable is not even a candidate in the picker, so an
// unrelated membership save must not remove it.
func TestARetiredCableStaysInItsBundleAcrossAnUnrelatedSave(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	a1 := mustServerAssetWeb(t, h, "bundle-retired-a")
	a2 := mustServerAssetWeb(t, h, "bundle-retired-b")
	a3 := mustServerAssetWeb(t, h, "bundle-retired-c")
	iA := mustInterfaceWeb(t, h, a1, "eth0")
	iB := mustInterfaceWeb(t, h, a2, "eth0")
	iC1 := mustInterfaceWeb(t, h, a1, "eth1")
	iC2 := mustInterfaceWeb(t, h, a3, "eth0")

	retiredCable := mustCableWeb(t, h, iA, iB)
	liveCable := mustCableWeb(t, h, iC1, iC2)

	createResp := h.post("/bundles", url.Values{
		"csrf_token": {h.csrfToken("/bundles")},
		"code":       {"duct-retiree"},
		"name":       {"Duct with a retiree"},
	}, false)
	createResp.Body.Close()
	bundleID := h.lookup(`SELECT id FROM cable_bundle WHERE code = ?`, "duct-retiree")

	seed := h.post("/bundles/"+bundleID+"/members", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + bundleID)},
		"link_id":    {retiredCable},
	}, false)
	seed.Body.Close()
	if seed.StatusCode != http.StatusSeeOther {
		t.Fatalf("seeding the bundle returned %d, want 303", seed.StatusCode)
	}

	retire := h.post("/links/"+retiredCable+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + bundleID)},
	}, false)
	retire.Body.Close()
	if life := h.lookup(`SELECT lifecycle FROM link WHERE id = ?`, retiredCable); life != "retired" {
		t.Fatalf("cable lifecycle after retiring it = %q, want retired (setup assumption failed)", life)
	}

	// An UNRELATED save: adding the other, live cable. The retired one is not
	// among the candidates offered, so nothing in this form's payload names
	// it at all.
	unrelated := h.post("/bundles/"+bundleID+"/members", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + bundleID)},
		"link_id":    {liveCable},
	}, false)
	unrelated.Body.Close()
	if unrelated.StatusCode != http.StatusSeeOther {
		t.Fatalf("the unrelated membership save returned %d, want 303", unrelated.StatusCode)
	}

	members, err := h.store.ListBundleMembers(context.Background(), bundleID)
	if err != nil {
		t.Fatalf("listing bundle members: %v", err)
	}
	found := map[string]bool{}
	for _, m := range members {
		found[m.LinkID] = true
	}
	if !found[retiredCable] {
		t.Error("the retired cable was dropped from the bundle by an unrelated save -- " +
			"membership is supposed to be history, not silently rewritten")
	}
	if !found[liveCable] {
		t.Error("the live cable the operator actually added is missing")
	}
}

// TestABundleCanBeWithdrawn covers the retire route, and that a withdrawn
// bundle's membership editor stops being offered.
func TestABundleCanBeWithdrawn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	createResp := h.post("/bundles", url.Values{
		"csrf_token": {h.csrfToken("/bundles")},
		"code":       {"duct-withdraw"},
		"name":       {"Duct to withdraw"},
	}, false)
	createResp.Body.Close()
	id := h.lookup(`SELECT id FROM cable_bundle WHERE code = ?`, "duct-withdraw")

	resp := h.post("/bundles/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdrawing the bundle returned %d, want 303", resp.StatusCode)
	}
	if life := h.lookup(`SELECT lifecycle FROM cable_bundle WHERE id = ?`, id); life != "retired" {
		t.Fatalf("bundle lifecycle after withdrawing it = %q, want retired", life)
	}

	page := body(t, h.get("/bundles/"+id, false))
	if strings.Contains(page, `name="link_id"`) {
		t.Error("a withdrawn bundle still offers the membership editor -- " +
			"SetBundleMembers refuses to edit a retired bundle, so this control has nothing behind it")
	}
	if strings.Contains(page, "Withdraw this bundle") {
		t.Error("a withdrawn bundle still offers the withdraw button")
	}

	// Editing membership directly against the route must still be refused,
	// not merely hidden in the UI.
	membersResp := h.post("/bundles/"+id+"/members", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + id)},
	}, false)
	defer membersResp.Body.Close()
	if membersResp.StatusCode == http.StatusSeeOther {
		t.Error("editing a withdrawn bundle's membership was accepted")
	}
}
