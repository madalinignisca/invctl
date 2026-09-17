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
)

// docs/breakout-cables-design.md D5, driven through the real router rather
// than pinned only at the view-model level (bundle_impact_group_test.go in
// the handlers package already does that half). This is the behaviour the
// design doc's own example names: "/bundles/{id}/impact would report '4
// cables go dark'" for a duct holding one physical breakout. Rendered, not
// merely routed -- link_impact_test.go's own comment is the reason: a
// handler test that builds the page struct by hand never catches a template
// that silently stopped grouping.
func TestBundleImpactGroupsABreakoutIntoOneCable(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	swAsset := mustServerAssetWeb(t, h, "bundle-breakout-sw")
	swIface := mustInterfaceWeb(t, h, swAsset, "eth1")

	var bIfaces []string
	for i := 0; i < 4; i++ {
		srv := mustServerAssetWeb(t, h, "bundle-breakout-srv-"+string(rune('a'+i)))
		bIfaces = append(bIfaces, mustInterfaceWeb(t, h, srv, "eth0"))
	}

	links, err := h.store.CreateBreakout(context.Background(),
		domain.AdministratorPermit(domain.SystemActor), domain.BreakoutSpec{
			AInterfaceID: swIface, BInterfaceIDs: bIfaces,
		})
	if err != nil {
		t.Fatalf("creating breakout: %v", err)
	}
	if len(links) != 4 {
		t.Fatalf("got %d strands, want 4", len(links))
	}

	bundleResp := h.post("/bundles", url.Values{
		"csrf_token": {h.csrfToken("/bundles")},
		"code":       {"duct-breakout-1"},
		"name":       {"Breakout duct"},
	}, false)
	bundleResp.Body.Close()
	if bundleResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring the bundle returned %d, want 303", bundleResp.StatusCode)
	}
	bundleID := h.lookup(`SELECT id FROM cable_bundle WHERE code = ?`, "duct-breakout-1")
	if bundleID == "" {
		t.Fatal("the bundle was not created")
	}

	linkIDs := make([]string, len(links))
	for i, l := range links {
		linkIDs[i] = l.ID
	}
	memberResp := h.post("/bundles/"+bundleID+"/members", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + bundleID)},
		"link_id":    linkIDs,
	}, false)
	memberResp.Body.Close()
	if memberResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("pulling all four strands into the duct returned %d, want 303",
			memberResp.StatusCode)
	}

	page := body(t, h.get("/bundles/"+bundleID+"/impact", false))

	if !strings.Contains(page, "1 breakout cable") {
		t.Error("the impact page does not say '1 breakout cable' -- a duct holding one " +
			"physical breakout DAC must be reported as one cable, not four")
	}
	if !strings.Contains(page, "4 strands") {
		t.Error("the impact page does not name all 4 strands of the folded breakout")
	}
	if strings.Contains(page, "4 cables") {
		t.Error("the impact page says '4 cables' -- this is the exact overstatement " +
			"docs/breakout-cables-design.md D5 exists to fix: one connector failing takes " +
			"the whole assembly, and the page must not report it as four separate incidents")
	}
	// Every strand is still individually named and still individually
	// reachable -- D5 groups the DISPLAY, it does not hide what actually
	// goes dark.
	for _, id := range linkIDs {
		if !strings.Contains(page, "/links/"+id+"/impact") {
			t.Errorf("strand %s is not linked from the grouped entry -- folding the display "+
				"must not drop a strand from what the page shows", id)
		}
	}
}

// TestBundleImpactDoesNotGroupOrdinaryCables is the converse: a duct with no
// breakout in it must keep reporting one entry per cable, unchanged.
func TestBundleImpactDoesNotGroupOrdinaryCables(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	a1 := mustServerAssetWeb(t, h, "bundle-plain-a")
	a2 := mustServerAssetWeb(t, h, "bundle-plain-b")
	iA := mustInterfaceWeb(t, h, a1, "eth0")
	iB := mustInterfaceWeb(t, h, a2, "eth0")
	cable := mustCableWeb(t, h, iA, iB)

	bundleResp := h.post("/bundles", url.Values{
		"csrf_token": {h.csrfToken("/bundles")},
		"code":       {"duct-plain-1"},
		"name":       {"Plain duct"},
	}, false)
	bundleResp.Body.Close()
	bundleID := h.lookup(`SELECT id FROM cable_bundle WHERE code = ?`, "duct-plain-1")

	memberResp := h.post("/bundles/"+bundleID+"/members", url.Values{
		"csrf_token": {h.csrfToken("/bundles/" + bundleID)},
		"link_id":    {cable},
	}, false)
	memberResp.Body.Close()
	if memberResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("pulling the cable into the duct returned %d, want 303", memberResp.StatusCode)
	}

	page := body(t, h.get("/bundles/"+bundleID+"/impact", false))
	if !strings.Contains(page, "1 cable") {
		t.Error("a duct with one ordinary cable does not report '1 cable'")
	}
	if strings.Contains(page, "breakout cable") {
		t.Error("an ordinary cable is reported as a breakout -- it carries no breakout_id")
	}
}
