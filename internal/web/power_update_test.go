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
	"strings"
	"testing"
)

// The three power correction paths, driven through the forms.
//
// UpdatePowerFeed, UpdatePowerSource and UpdatePowerInput were all complete in
// the store and all unreachable: each validated, guarded on row_version, wrote
// its change_log entry, and had no route, no handler and no form. The power
// chain could be declared and withdrawn and never fixed.
//
// WHAT THAT COST, precisely, is why these are three tests and not one. A feed's
// rating is the denominator of every capacity finding on its board. A supply's
// parent is what decides whether two boards are independent -- the single
// question this whole subsystem exists to answer. And a draw figure is
// multiplied into money by the cost report.

// TestAFeedsRatingCanBeCorrected is the headline gap: a mistyped amperage.
//
// Before this route, the only fix was to retire the feed -- and the inputs hang
// off the feed, so correcting one digit meant disconnecting every asset on it.
func TestAFeedsRatingCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "correction-board")
	feed := h.addFeed(t, panel, "correction-feed") // 230 V, 32 A

	resp := h.post("/power/feeds/"+feed, url.Values{
		"csrf_token":      {h.csrfToken("/power")},
		"name":            {"correction-feed"},
		"voltage":         {"230"},
		"amperage":        {"20"},
		"max_utilisation": {"80"},
		"row_version":     {h.lookup(`SELECT row_version FROM power_feed WHERE id = ?`, feed)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a feed's amperage returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT amperage FROM power_feed WHERE id = ?`, feed); got != "20" {
		t.Errorf("amperage = %s after the correction, want 20 -- the rating is the "+
			"denominator of every capacity finding on this board", got)
	}
	// Through the page as well as the column: a store write nothing renders is
	// the half-built state this whole exercise is about.
	if !strings.Contains(body(t, h.get("/power", false)), "20 A") {
		t.Error("the corrected rating does not appear on /power")
	}
}

// TestCorrectingAFeedKeepsItsNotes is the trap the hidden inputs exist for.
//
// UpdatePowerFeed writes EVERY column, so a field the form does not carry
// arrives nil and is cleared. Correcting an amperage must not silently erase
// the note explaining what the feed is for. The same shape as
// TestCorrectingAWirelessLANKeepsItsPSKReference, and found the same way.
func TestCorrectingAFeedKeepsItsNotes(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "notes-board")
	const note = "fed from the roof plant, do not reroute"
	resp := h.post("/power/feeds", url.Values{
		"csrf_token": {h.csrfToken("/power")}, "panel_id": {panel},
		"name": {"noted-feed"}, "voltage": {"230"}, "amperage": {"32"},
		"max_utilisation": {"80"}, "notes": {note},
	}, false)
	resp.Body.Close()
	feed := h.lookup(`SELECT id FROM power_feed WHERE panel_id = ? AND name = ?`, panel, "noted-feed")

	page := body(t, h.get("/power?edit="+feed, false))
	carried, ok := notesInEditForm(page, "feed-"+feed)
	if !ok {
		t.Fatalf("the feed's edit row carries no notes input, so this test would "+
			"prove nothing -- and the first save would blank the note. Page: %d bytes", len(page))
	}

	resp = h.post("/power/feeds/"+feed, url.Values{
		"csrf_token":      {h.csrfToken("/power")},
		"name":            {"noted-feed"},
		"voltage":         {"230"},
		"amperage":        {"16"},
		"max_utilisation": {"80"},
		"notes":           {carried},
		"row_version":     {h.lookup(`SELECT row_version FROM power_feed WHERE id = ?`, feed)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT COALESCE(notes, '') FROM power_feed WHERE id = ?`, feed); got != note {
		t.Errorf("the note went %q -> %q when the amperage was corrected. The edit row "+
			"must carry every column UpdatePowerFeed writes", note, got)
	}
}

// TestASupplyCanGainTheParentThatMakesTwoBoardsDependent is the correction that
// changes an answer rather than a label.
//
// A supply recorded with no parent reads as the top of its own chain, so two
// boards fed from it and from its sibling look independent. Attaching the
// parent is what turns that into the false-redundancy finding the power report
// exists to raise -- and it was the one field nobody could set after the fact.
func TestASupplyCanGainTheParentThatMakesTwoBoardsDependent(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	mains := h.addSource(t, site, "corrections-utility", "utility")
	ups := h.addSource(t, site, "corrections-ups", "ups")

	if got := h.lookup(`SELECT COALESCE(parent_id, '') FROM power_source WHERE id = ?`, ups); got != "" {
		t.Fatalf("the UPS was created with a parent (%s); this test needs one without", got)
	}

	resp := h.post("/power/sources/"+ups, url.Values{
		"csrf_token":  {h.csrfToken("/power")},
		"name":        {"corrections-ups"},
		"kind":        {"ups"},
		"parent_id":   {mains},
		"row_version": {h.lookup(`SELECT row_version FROM power_source WHERE id = ?`, ups)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("attaching a supply's parent returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT COALESCE(parent_id, '') FROM power_source WHERE id = ?`, ups); got != mains {
		t.Errorf("parent_id = %q after the correction, want %q -- without it two boards "+
			"off this UPS keep reading as independent", got, mains)
	}
}

// TestASupplyCannotBeMadeItsOwnParent. The store refuses a cycle
// (requireNoSupplyCycle); this asserts the handler actually reaches that
// refusal rather than writing a chain that feeds itself.
func TestASupplyCannotBeMadeItsOwnParent(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	ups := h.addSource(t, site, "cycle-ups", "ups")

	resp := h.post("/power/sources/"+ups, url.Values{
		"csrf_token":  {h.csrfToken("/power")},
		"name":        {"cycle-ups"},
		"kind":        {"ups"},
		"parent_id":   {ups},
		"row_version": {h.lookup(`SELECT row_version FROM power_source WHERE id = ?`, ups)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT COALESCE(parent_id, '') FROM power_source WHERE id = ?`, ups); got == ups {
		t.Error("a supply was made its own parent, so the chain now feeds itself and " +
			"any walk of it either loops or stops early")
	}
}

// TestADeclaredDrawCanBeCorrected closes what WP-I2's own review recorded and
// left open: "correcting a number means Disconnect-and-re-add".
//
// That made D7's convergence claim -- that a declared draw improves as
// operators refine it -- rest on a UI with no way to refine anything. The cost
// report multiplies this column, so a mistyped nameplate became money.
func TestADeclaredDrawCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "draw-board")
	feed := h.addFeed(t, panel, "draw-feed")
	asset := h.lookup(`SELECT id FROM asset WHERE kind <> 'site' AND lifecycle = 'active' LIMIT 1`)

	resp := h.post("/assets/"+asset+"/power", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + asset)},
		"feed_id":    {feed}, "name": {"PSU-A"}, "draw_va": {"4500"},
	}, false)
	resp.Body.Close()
	input := h.lookup(`SELECT id FROM power_input WHERE asset_id = ? AND name = ?`, asset, "PSU-A")

	// 4500 was a typo for 450 -- the digit that turns a server into a rack.
	resp = h.post("/assets/"+asset+"/power/"+input, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + asset)},
		"name":        {"PSU-A"},
		"feed_id":     {feed},
		"draw_va":     {"450"},
		"row_version": {h.lookup(`SELECT row_version FROM power_input WHERE id = ?`, input)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a declared draw returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT draw_va FROM power_input WHERE id = ?`, input); got != "450" {
		t.Errorf("draw_va = %s after the correction, want 450", got)
	}
}

// TestCorrectingAPowerInputIsAudited. Declared state, so the correction is a
// permanent record and not a quiet overwrite -- CLAUDE.md's rule, and the
// reason a correction path is better than retire-and-recreate in the first
// place: the history stays attached to the same row.
func TestCorrectingAPowerInputIsAudited(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "audit-board")
	feed := h.addFeed(t, panel, "audit-feed")
	asset := h.lookup(`SELECT id FROM asset WHERE kind <> 'site' AND lifecycle = 'active' LIMIT 1`)

	resp := h.post("/assets/"+asset+"/power", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + asset)},
		"feed_id":    {feed}, "name": {"PSU-B"}, "draw_va": {"300"},
	}, false)
	resp.Body.Close()
	input := h.lookup(`SELECT id FROM power_input WHERE asset_id = ? AND name = ?`, asset, "PSU-B")
	before := h.lookup(`SELECT COUNT(*) FROM change_log WHERE entity_id = ?`, input)

	resp = h.post("/assets/"+asset+"/power/"+input, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + asset)},
		"name":        {"PSU-B"},
		"feed_id":     {feed},
		"draw_va":     {"325"},
		"row_version": {h.lookup(`SELECT row_version FROM power_input WHERE id = ?`, input)},
	}, false)
	resp.Body.Close()

	after := h.lookup(`SELECT COUNT(*) FROM change_log WHERE entity_id = ?`, input)
	if after == before {
		t.Errorf("correcting a declared draw wrote no change_log row (still %s). Every "+
			"mutation of declared state is audited in the same transaction", before)
	}
}

// TestOnlyAnAdministratorCanCorrectThePowerChain. Every power table is
// ScopeTopology -- the same grant Withdraw on these rows already asks for, and
// the reason a project owner sees no Edit link on them.
//
// A PROJECT OWNER IS THE CASE THAT ACTUALLY PROVES THIS, and the first version
// of this test used only an Observer, which proved something much weaker.
// These routes are registered under the generic `write` registrar, so
// RequireWrite refuses an Observer on CanWrite before the handler -- let alone
// the store's scope check -- is ever reached. The identical refusal would
// happen if power_feed were ScopeProjectLinked, so an Observer-only test says
// "you must be a writer", not "you must be an Administrator", however it is
// named. A project owner has CanWrite = true and IsAdmin = false: they pass
// the middleware, reach the store, and are refused by permit.Covers against
// entityScope. That is the ScopeTopology claim, and nothing tested it.
func TestOnlyAnAdministratorCanCorrectThePowerChain(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "rbac-board")
	feed := h.addFeed(t, panel, "rbac-feed")
	version := h.lookup(`SELECT row_version FROM power_feed WHERE id = ?`, feed)
	project := h.lookup(`SELECT id FROM project WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	// wantStatus pins WHICH LAYER refused, not merely that something did.
	// The two are different numbers and that is the whole point: 403 is
	// middleware.RequireWrite answering CanWrite, 404 is the store's
	// permit.Covers refusing a row the caller may not see. A test that
	// accepted "any non-303" could not tell them apart, and would pass
	// unchanged if the store's scope check were deleted outright.
	attempt := func(t *testing.T, who *harness, name string, wantStatus int) {
		t.Helper()
		resp := who.post("/power/feeds/"+feed, url.Values{
			"csrf_token":      {who.csrfToken("/power")},
			"name":            {name},
			"voltage":         {"230"},
			"amperage":        {"63"},
			"max_utilisation": {"80"},
			"row_version":     {version},
		}, false)
		defer resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Errorf("%s got %d, want %d -- the refusal came from a different layer "+
				"than this test is asserting", name, resp.StatusCode, wantStatus)
		}
		if got := h.lookup(`SELECT name FROM power_feed WHERE id = ?`, feed); got == name {
			t.Errorf("%s's correction reached the database despite the refusal", name)
		}
	}

	t.Run("observer", func(t *testing.T) {
		viewer := newHarness(t)
		viewer.login("viewer", "viewer-password")
		// Stopped at the door: CanWrite is false, so the handler never runs.
		attempt(t, viewer, "observer-took-over", http.StatusForbidden)
	})

	// The one that reaches the store. Refused by permit.Covers, not by
	// middleware -- which is the whole difference this test exists to state.
	t.Run("project owner", func(t *testing.T) {
		owner := newHarness(t)
		mustWebProjectOwner(t, owner, "po-power", "po-power-password", project)
		owner.login("po-power", "po-power-password")
		// Through the door and refused inside: CanWrite is TRUE for a project
		// owner, so RequireWrite lets them past and permit.Covers is what
		// says no -- 404 rather than 403, because a row outside your scope
		// does not exist for you.
		attempt(t, owner, "owner-took-over", http.StatusNotFound)
	})
}

func (h *harness) addSource(t *testing.T, siteID, name, kind string) string {
	t.Helper()
	resp := h.post("/power/sources", url.Values{
		"csrf_token": {h.csrfToken("/power")}, "site_id": {siteID},
		"name": {name}, "kind": {kind},
	}, false)
	resp.Body.Close()
	return h.lookup(`SELECT id FROM power_source WHERE site_id = ? AND name = ?`, siteID, name)
}

// notesInEditForm reads the hidden notes input belonging to one edit form.
//
// Scoped by the form's id rather than taking the first `name="notes"` on the
// page, because /power also renders the "add a feed" form with a notes field
// of its own -- a page-wide search would read that empty one and the assertion
// would pass whatever the edit row carried.
func notesInEditForm(page, formID string) (string, bool) {
	i := strings.Index(page, `<form id="`+formID+`"`)
	if i < 0 {
		return "", false
	}
	seg := page[i:]
	if end := strings.Index(seg, "</form>"); end >= 0 {
		seg = seg[:end]
	}
	j := strings.Index(seg, `name="notes" value="`)
	if j < 0 {
		return "", false
	}
	return attrAfter(seg[j:], `name="notes" value="`, `"`)
}
