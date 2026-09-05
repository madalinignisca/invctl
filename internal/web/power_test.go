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
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The power screens, driven through the real forms, ending at the finding.
//
// EVERY TEST OWNS ITS OWN TOPOLOGY. The seeded fixture now carries a 2N estate
// of its own -- deliberately, so a fresh install shows what the feature is for
// -- which means an assertion like "the report mentions a false redundancy" is
// true before these tests do anything. So each one builds its own panels and
// plugs in assets the fixture leaves unpowered, and asserts on those.

func (h *harness) addPanel(t *testing.T, siteID, name string) string {
	t.Helper()
	resp := h.post("/power/panels", url.Values{
		"csrf_token": {h.csrfToken("/power")}, "site_id": {siteID}, "name": {name},
	}, false)
	resp.Body.Close()
	return h.lookup(`SELECT id FROM power_panel WHERE name = ?`, name)
}

func (h *harness) addFeed(t *testing.T, panelID, name string) string {
	t.Helper()
	resp := h.post("/power/feeds", url.Values{
		"csrf_token": {h.csrfToken("/power")}, "panel_id": {panelID}, "name": {name},
		"voltage": {"230"}, "amperage": {"32"}, "max_utilisation": {"80"},
	}, false)
	resp.Body.Close()
	return h.lookup(`SELECT id FROM power_feed WHERE panel_id = ? AND name = ?`, panelID, name)
}

// follow performs the redirect the harness client deliberately does not.
//
// Redirects are part of what this suite tests, so the client returns them
// rather than chasing them -- which means a test asserting on the CONTENT
// behind one has to ask for it. Both tests below were written without this and
// asserted against `<a href="...">See Other</a>`, where the positive check
// failed loudly and the negative one would have passed for ever.
func (h *harness) follow(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want a redirect", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("a redirect with no Location header")
	}
	return body(t, h.get(loc, false))
}

func (h *harness) plug(t *testing.T, assetID, feedID, name string) *http.Response {
	t.Helper()
	return h.post("/assets/"+assetID+"/power", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
		"feed_id":    {feedID}, "name": {name},
	}, false)
}

// TestTheFalseRedundancyFindingReachesTheScreen is the whole work package, end
// to end through the forms an operator actually uses.
func TestTheFalseRedundancyFindingReachesTheScreen(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	p1 := h.addPanel(t, site, "t-panel-1")
	p2 := h.addPanel(t, site, "t-panel-2")
	f1a := h.addFeed(t, p1, "F1")
	f1b := h.addFeed(t, p1, "F2")
	f2a := h.addFeed(t, p2, "F1")
	if p1 == "" || f1a == "" || f2a == "" {
		t.Fatal("the panels or feeds were not created through the forms")
	}

	// Assets the fixture leaves unpowered, so this test owns their inputs.
	trap := h.asset("sw-core-2")
	resp := h.plug(t, trap, f1a, "A")
	resp.Body.Close()
	resp = h.plug(t, trap, f1b, "B")
	resp.Body.Close()

	// The control: genuinely redundant, and it must not be reported.
	ok := h.asset("fw-edge-1")
	resp = h.plug(t, ok, f1a, "A")
	resp.Body.Close()
	resp = h.plug(t, ok, f2a, "B")
	resp.Body.Close()

	page := body(t, h.get("/reports/power", false))
	if !strings.Contains(page, "false redundancy") {
		t.Fatalf("the report does not mention the finding:\n%s", page)
	}
	if !strings.Contains(page, `href="/assets/`+trap+`"`) {
		t.Error("the report does not link to the asset with two inputs on one panel")
	}
	if strings.Contains(page, `href="/assets/`+ok+`"`) {
		t.Error("the report flags an asset whose inputs are on DIFFERENT panels, so it " +
			"is reporting 'has two inputs' rather than 'has two inputs on one panel'")
	}
	if !strings.Contains(page, "t-panel-1") {
		t.Error("the finding does not name the shared panel, so somebody has to trace it by hand")
	}

	// And the asset's own page shows where it draws from, panel included --
	// which is where somebody would go to fix it.
	assetPage := body(t, h.get("/assets/"+trap, false))
	if !strings.Contains(assetPage, "t-panel-1") {
		t.Error("the asset page does not show the panel behind its inputs")
	}
}

func TestLosingAFeedSimulatesOnlyWhatActuallyGoesDark(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	p1 := h.addPanel(t, site, "t-panel-1")
	p2 := h.addPanel(t, site, "t-panel-2")
	fa := h.addFeed(t, p1, "F1")
	fb := h.addFeed(t, p2, "F1")

	single := h.asset("fw-edge-1")
	resp := h.plug(t, single, fa, "A")
	resp.Body.Close()

	redundant := h.asset("sw-core-2")
	resp = h.plug(t, redundant, fa, "A")
	resp.Body.Close()
	resp = h.plug(t, redundant, fb, "B")
	resp.Body.Close()

	// Asserted on the REDIRECT TARGET, which is literally the resolver's answer.
	// The impact page it lands on also renders a picker of every other asset to
	// add to the simulation, so searching the rendered page for a name finds it
	// whether or not it is in the outage set -- the same trap the device-type
	// filter test fell into.
	resp = h.get("/power/feeds/"+fa+"/impact", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect into the impact page", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, single) {
		t.Errorf("the simulation %q does not include the single-fed asset", loc)
	}
	if strings.Contains(loc, redundant) {
		t.Error("the simulation includes an asset that still has a live input on another " +
			"feed. A resolver that returns everything on the feed models redundancy and " +
			"then ignores it.")
	}

	// And it does land on a real impact page rather than a broken URL.
	page := body(t, h.get(loc, false))
	if !strings.Contains(page, "fw-edge-1") {
		t.Errorf("the impact page does not name the asset that goes dark:\n%s", page)
	}
}

// TestAFeedThatTakesNothingDownSaysSo covers the answer that is easy to render
// as its opposite.
func TestAFeedThatTakesNothingDownSaysSo(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	p1 := h.addPanel(t, site, "t-panel-1")
	p2 := h.addPanel(t, site, "t-panel-2")
	fa := h.addFeed(t, p1, "F1")
	fb := h.addFeed(t, p2, "F1")

	box := h.asset("sw-core-2")
	resp := h.plug(t, box, fa, "A")
	resp.Body.Close()
	resp = h.plug(t, box, fb, "B")
	resp.Body.Close()

	page := h.follow(t, h.get("/power/feeds/"+fa+"/impact", false))
	// "Nothing breaks" and "nothing loses power in the first place" are
	// different answers, and rendering an empty impact page for the second is
	// the most dangerous thing this tool can say.
	if !strings.Contains(page, "Nothing loses power") {
		t.Errorf("a feed whose assets are all redundant did not say so plainly:\n%s", page)
	}
}

// TestTheDrawHintBoundsWholeLoadToThisAssetsOwnConsumption is R5/item 19.
// §2.2's PDU mitigation claim depended on the form's wording alone -- a rack
// PDU is a containment SIBLING of the servers it powers, which asset_closure
// structurally cannot exclude, so an unqualified "whole load" instruction
// invites exactly the double-count §2.2 admits it cannot catch: the most
// natural reading for a PDU is "the sum of everything downstream", not "my
// own overhead". The hint must bound it to the asset's OWN consumption.
func TestTheDrawHintBoundsWholeLoadToThisAssetsOwnConsumption(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	asset := h.asset("sw-core-2")
	page := body(t, h.get("/assets/"+asset, false))

	if !strings.Contains(page, "the whole load of THIS asset") {
		t.Error("the draw hint does not bound \"whole load\" to the asset's own consumption")
	}
	if !strings.Contains(page, "not what it passes on to something downstream") {
		t.Error("the draw hint does not rule out entering what a PDU passes through to " +
			"the servers behind it -- the exact reading §2.2 cannot catch structurally")
	}
	// "Nameplate" was dropped from round 1's fix while the report still says
	// "declared nameplate draw" -- the form must still say where the number
	// comes from.
	if !strings.Contains(page, "Nameplate") {
		t.Error("the draw hint no longer tells the operator where the number comes from")
	}
}

func TestPowerIsReadableByAnyoneAndWritableByAdminsOnly(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "t-panel-1")
	before := h.count(`SELECT COUNT(*) FROM power_feed`)

	h.logout()
	h.login("viewer", "viewer-password")

	resp := h.get("/power", false)
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /power as a read-only user returned %d, want 200 -- an operator "+
			"tracing power during an incident is exactly this audience", resp.StatusCode)
	}
	if strings.Contains(page, "Add a feed") {
		t.Error("a read-only user is offered the add-a-feed form, whose only outcome is a 403")
	}

	// A submission that WOULD have worked, so the refusal is authorization and
	// not validation.
	resp = h.post("/power/feeds", url.Values{
		"csrf_token": {h.csrfToken("/power")}, "panel_id": {panel}, "name": {"sneaky"},
		"voltage": {"230"}, "amperage": {"32"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /power/feeds as a read-only user returned %d, want 403", resp.StatusCode)
	}
	if got := h.count(`SELECT COUNT(*) FROM power_feed`); got != before {
		t.Errorf("feeds went from %d to %d under a read-only session", before, got)
	}
}

// TestTracingARunThroughTwoPanelsOnScreen is B3 end to end through the forms.
func TestTracingARunThroughTwoPanelsOnScreen(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	mk := func(kind, name string) string {
		resp := h.post("/assets", url.Values{
			"csrf_token": {h.csrfToken("/assets")}, "name": {name},
			"kind": {kind}, "parent_id": {site},
		}, false)
		resp.Body.Close()
		return h.lookup(`SELECT id FROM asset WHERE name = ?`, name)
	}
	port := func(assetID, name string) string {
		resp := h.post("/assets/"+assetID+"/interfaces", url.Values{
			"csrf_token": {h.csrfToken("/assets/" + assetID)},
			"name":       {name}, "form_factor": {"rj45"},
		}, false)
		resp.Body.Close()
		return h.lookup(`SELECT id FROM interface WHERE asset_id = ? AND name = ?`, assetID, name)
	}
	patch := func(assetID, front, rear string) {
		resp := h.post("/assets/"+assetID+"/patch", url.Values{
			"csrf_token":         {h.csrfToken("/assets/" + assetID)},
			"front_interface_id": {front}, "rear_interface_id": {rear},
		}, false)
		resp.Body.Close()
	}

	sw := mk("switch", "trace-sw")
	pa := mk("patch_panel", "trace-panel-a")
	pb := mk("patch_panel", "trace-panel-b")
	srv := mk("server", "trace-srv")

	swPort := port(sw, "eth1")
	aF, aR := port(pa, "a-front"), port(pa, "a-rear")
	bR, bF := port(pb, "b-rear"), port(pb, "b-front")
	srvPort := port(srv, "eth0")
	if swPort == "" || aF == "" || srvPort == "" {
		t.Fatal("the ports were not created through the forms")
	}
	patch(pa, aF, aR)
	patch(pb, bF, bR)
	// Assert the SETUP, not just the outcome. A test whose fixture silently
	// failed to build reports the feature as broken, which is a slower way to
	// find a wrong route name than saying so here.
	if n := h.count(`SELECT COUNT(*) FROM port_pass_through`); n != 2 {
		t.Fatalf("created %d pass-throughs, want 2", n)
	}

	// The REAL route and field names: POST /links, with the far end called
	// target_interface_id and the asset carried alongside. Guessed both wrong
	// first, and then three attempts to correct it silently did nothing because
	// gofmt had realigned the literal and the patch no longer matched. The
	// status is asserted here so a wrong route cannot look like a working one.
	link := func(a, b string) {
		t.Helper()
		resp := h.post("/links", url.Values{
			"csrf_token":          {h.csrfToken("/assets/" + sw)},
			"asset_id":            {sw},
			"a_interface_id":      {a},
			"target_interface_id": {b},
		}, false)
		code := resp.StatusCode
		resp.Body.Close()
		if code != http.StatusSeeOther {
			t.Fatalf("cabling returned %d, want 303", code)
		}
	}
	link(swPort, aF)
	link(aR, bR)
	link(bF, srvPort)
	if n := h.count(`SELECT COUNT(*) FROM link`); n < 3 {
		t.Fatalf("created %d cables in total, want at least 3", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM port_pass_through WHERE lifecycle = 'active'`); n != 2 {
		t.Fatalf("%d live pass-throughs, want 2", n)
	}

	page := body(t, h.get("/interfaces/"+swPort+"/trace", false))
	for _, want := range []string{"trace-panel-a", "a-rear", "trace-panel-b", "trace-srv", "eth0"} {
		if !strings.Contains(page, want) {
			t.Errorf("the trace does not name %q; a path that stops at the first cable "+
				"answers \"a patch panel\", which is true and useless:\n%s", want, page)
		}
	}
	// The PILL, not the word. "incomplete" contains "complete", so asserting on
	// the substring could never fail -- mutation testing caught that by removing
	// the completeness flag entirely and watching this stay green.
	if !strings.Contains(page, `pill-ok">complete`) {
		t.Errorf("the trace does not report itself as complete:\n%s", page)
	}
	if strings.Contains(page, "pill-degraded\">incomplete") {
		t.Error("a run that reaches the far end is reported as incomplete")
	}
	if !strings.Contains(page, "through the panel") {
		t.Error("the trace does not distinguish a panel hop from a cable hop")
	}
}

// TestTracingABreakoutOnScreen is WP-B4 end to end through the forms: a rear
// port breaking out to two front ports renders both strands, each labelled
// with its declared position.
//
// DRIVEN THROUGH THE FORM PARAMETER, NOT THE FORM FIELD: the patch form has no
// position input yet (docs/panel-breakout-design.md's Decision C, Task 8 of
// the plan), so this posts "position" directly, the way the handler already
// accepts it. If Task 8 lands, this should be rewritten to drive the real
// input -- a functional test that posts a field the UI cannot send proves the
// handler works and nothing else.
func TestTracingABreakoutOnScreen(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	mk := func(kind, name string) string {
		resp := h.post("/assets", url.Values{
			"csrf_token": {h.csrfToken("/assets")}, "name": {name},
			"kind": {kind}, "parent_id": {site},
		}, false)
		resp.Body.Close()
		return h.lookup(`SELECT id FROM asset WHERE name = ?`, name)
	}
	port := func(assetID, name string) string {
		resp := h.post("/assets/"+assetID+"/interfaces", url.Values{
			"csrf_token": {h.csrfToken("/assets/" + assetID)},
			"name":       {name}, "form_factor": {"rj45"},
		}, false)
		resp.Body.Close()
		return h.lookup(`SELECT id FROM interface WHERE asset_id = ? AND name = ?`, assetID, name)
	}
	patchAt := func(assetID, front, rear string, position int) {
		resp := h.post("/assets/"+assetID+"/patch", url.Values{
			"csrf_token":         {h.csrfToken("/assets/" + assetID)},
			"front_interface_id": {front}, "rear_interface_id": {rear},
			"position": {strconv.Itoa(position)},
		}, false)
		code := resp.StatusCode
		resp.Body.Close()
		if code != http.StatusSeeOther {
			t.Fatalf("patching at position %d returned %d, want 303", position, code)
		}
	}

	pa := mk("patch_panel", "breakout-panel-a")
	rear := port(pa, "rear-1")
	f1 := port(pa, "f-1")
	f2 := port(pa, "f-2")
	if rear == "" || f1 == "" || f2 == "" {
		t.Fatal("the ports were not created through the forms")
	}
	patchAt(pa, f1, rear, 1)
	patchAt(pa, f2, rear, 2)
	if n := h.count(`SELECT COUNT(*) FROM port_pass_through WHERE lifecycle = 'active'`); n != 2 {
		t.Fatalf("%d live pass-throughs, want 2", n)
	}

	page := body(t, h.get("/interfaces/"+rear+"/trace", false))
	for _, want := range []string{"f-1", "f-2", "strand 1", "strand 2"} {
		if !strings.Contains(page, want) {
			t.Errorf("the trace does not show %q; a rear port that breaks out to two "+
				"front ports must render BOTH strands, each labelled with its declared "+
				"position:\n%s", want, page)
		}
	}
	// Two ends, both unpatched at the front (neither f-1 nor f-2 leads anywhere
	// further) -- the count line says so without implying a total.
	if !strings.Contains(page, "2 ends") {
		t.Errorf("the trace does not report 2 ends for a two-strand breakout:\n%s", page)
	}
}
