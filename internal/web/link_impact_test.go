// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// WP-B3's engine half, on the screen.
//
// THE WORK PACKAGE READ DONE FOR WEEKS WITH THIS UNBUILT. The tracer walked a
// cable end to end and nothing could ask what happens when one is severed;
// `impact.Request` accepted DownAssetIDs and CutCircuitIDs and nothing else, so
// an operator's only recourse was to down an asset -- a different and blunter
// question. A switch losing power is not the same event as one of its uplinks
// being unplugged, and answering the second with the first overstates the blast
// radius every time.
//
// THIS FILE EXISTS BECAUSE THE CIRCUIT PAGE SHIPPED A 500. impact_result is a
// shared partial with a contract -- HasImpact and HasNetworkFinding -- and
// html/template errors on a field a struct does not have. The circuit page
// omitted them and returned 500 on every fetch, and no test caught it because
// none of them fetched the page. Handler tests call handlers directly and never
// execute the template against the real page struct.

// TestCuttingACableReachesTheScreen. Rendered, not merely routed.
func TestCuttingACableReachesTheScreen(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	linkID := h.lookup(`SELECT id FROM link WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	resp := h.get("/links/"+linkID+"/impact", false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /links/%s/impact returned %d, want 200. The shared "+
			"impact_result partial carries a contract, and a page missing one of "+
			"its fields 500s on every fetch", linkID, resp.StatusCode)
	}

	page := body(t, resp)
	// One of the three sentences, never an empty result. store.CircuitCut's
	// doc comment is explicit that conflating any two of its outcomes is a lie
	// the page would tell.
	sentences := []string{
		"joins nothing that is modelled",
		"separates the estate",
		"Another path survives this",
	}
	found := 0
	for _, s := range sentences {
		if strings.Contains(page, s) {
			found++
		}
	}
	if found != 1 {
		t.Errorf("the page renders %d of the three cut outcomes, want exactly 1. "+
			"A cable that joins nothing and a cable whose loss changes nothing "+
			"read identically in a Result and need opposite sentences", found)
	}
}

// TestTheCutLinkIsReachedByClicking, not by constructing the URL.
//
// This project has shipped a 404 on a button with every handler test green,
// because a handler test injects router params by hand and never asks the
// router whether anything can reach it. The control lives in the patching table
// on the asset page.
func TestTheCutLinkIsReachedByClicking(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	linkID := h.lookup(`SELECT id FROM link WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
	assetID := h.lookup(`SELECT ia.asset_id FROM link l
	                     JOIN interface ia ON ia.id = l.a_interface_id
	                     WHERE l.id = ?`, linkID)

	page := body(t, h.get("/assets/"+assetID, false))
	href := "/links/" + linkID + "/impact"
	if !strings.Contains(page, href) {
		t.Fatalf("the asset page offers no link to %s, so the simulation is "+
			"reachable only by typing a URL nobody would guess", href)
	}

	// And following it works, which is the half a template scan cannot see.
	resp := h.get(href, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("following the page's own link returned %d, want 200", resp.StatusCode)
	}
}

// TestCuttingAnUnknownCableIs404. "Nothing breaks" about a scenario nobody ran
// is the most dangerous answer this tool can produce, so a missing cable is a
// refusal rather than an empty simulation.
func TestCuttingAnUnknownCableIs404(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.get("/links/01a00000-0000-7000-8000-000000000000/impact", false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown cable returned %d, want 404 -- an empty simulation "+
			"reads as 'cutting this is harmless'", resp.StatusCode)
	}
}

// TestTheCutSimulationIsReadableByAnyone. Asking what a cut would do is a READ,
// and the person who most needs it at three in the morning is often not the
// person who may unplug it.
func TestTheCutSimulationIsReadableByAnyone(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	linkID := h.lookup(`SELECT id FROM link WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.get("/links/"+linkID+"/impact", false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("an Observer got %d simulating a cable cut, want 200", resp.StatusCode)
	}
}
