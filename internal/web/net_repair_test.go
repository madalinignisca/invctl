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

// THE REACHABILITY LAYER WAS CREATE-ONLY IN BOTH DIRECTIONS.
//
// Five create routes, no retire routes, and five complete Retire* methods in
// internal/store/reach.go that nothing called. An estate could declare a
// forwarder group, put assets in it, draw uplinks, attach hosts and place
// anchors -- and take none of it back.
//
// The store half was written and argued long ago; this is the wiring, the same
// shape WP-1.2 took for six correction paths. What kept it visible in the
// meantime was unreachableRepairPaths, which this work empties.

func firstNetGroup(t *testing.T, h *harness) string {
	t.Helper()
	return h.lookup(`SELECT id FROM net_group WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
}

// TestAForwarderGroupHasAPageListingWhatItHolds. /network shows counts, and you
// cannot remove one member from a number -- which is why three of the five
// retires had nowhere to live until this page existed.
func TestAForwarderGroupHasAPageListingWhatItHolds(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstNetGroup(t, h)

	// Reached by clicking from the list, not by a URL nobody would guess.
	list := body(t, h.get("/network", false))
	if !strings.Contains(list, "/network/groups/"+id) {
		t.Fatalf("/network offers no link to group %s, so its members and uplinks "+
			"are reachable only by typing a URL", id)
	}

	resp := h.get("/network/groups/"+id, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /network/groups/%s returned %d, want 200", id, resp.StatusCode)
	}
	page := body(t, resp)
	for _, section := range []string{"member", "uplink", "attachment"} {
		if !strings.Contains(page, section) {
			t.Errorf("the group page does not mention %ss at all", section)
		}
	}
}

// TestAnAssetCanBeTakenOutOfAForwarderGroup.
func TestAnAssetCanBeTakenOutOfAForwarderGroup(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	groupID := h.lookup(`SELECT group_id FROM net_group_member WHERE lifecycle = 'active'
	                     ORDER BY group_id LIMIT 1`)
	assetID := h.lookup(`SELECT asset_id FROM net_group_member
	                     WHERE group_id = ? AND lifecycle = 'active' ORDER BY asset_id LIMIT 1`, groupID)

	resp := h.post("/network/groups/"+groupID+"/members/"+assetID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/network/groups/" + groupID)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("removing a member returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM net_group_member
	                    WHERE group_id = ? AND asset_id = ?`, groupID, assetID); got != "retired" {
		t.Errorf("membership lifecycle = %q, want retired", got)
	}
}

// TestAnAnchorCanBeWithdrawn.
//
// THE HIGHEST-LEVERAGE ROW IN THE MODEL by internal/domain's own account: an
// anchor decides external reachability for everything behind it, so one placed
// on the wrong group silently changes every verdict in the estate. It could be
// placed and never removed.
func TestAnAnchorCanBeWithdrawn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.lookup(`SELECT id FROM net_anchor WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	if !strings.Contains(body(t, h.get("/network", false)), "/network/anchors/"+id+"/retire") {
		t.Fatal("the topology page offers no way to withdraw an anchor")
	}

	resp := h.post("/network/anchors/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/network")},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdrawing an anchor returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM net_anchor WHERE id = ?`, id); got != "retired" {
		t.Errorf("anchor lifecycle = %q, want retired", got)
	}
}

// TestWithdrawingAGroupCascadesAndSaysSoFirst.
//
// The cascade is structural, not a convenience: the store's comment records
// that retiring the vertex without it "would strand its members forever (the
// partial unique index gives a retired-group membership no route back to any
// live group)". That is why this differs from withdrawing a PORT, which refuses
// while anything is attached -- a cable on a port survives on its own, a group
// membership does not.
//
// So the page must say what goes with it BEFORE the click, not after.
func TestWithdrawingAGroupCascadesAndSaysSoFirst(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	groupID := h.lookup(`SELECT group_id FROM net_group_member WHERE lifecycle = 'active'
	                     ORDER BY group_id LIMIT 1`)
	members := h.lookup(`SELECT COUNT(*) FROM net_group_member
	                     WHERE group_id = ? AND lifecycle = 'active'`, groupID)
	if members == "0" {
		t.Fatal("that group has no members, so the cascade would prove nothing")
	}

	page := body(t, h.get("/network/groups/"+groupID, false))
	if !strings.Contains(page, "would go with it") {
		t.Error("the group page does not say what withdrawing it takes along. The " +
			"cascade is irreversible for a membership -- the partial unique index " +
			"gives it no route back to a live group -- so an operator must be told " +
			"before the click, not after")
	}

	resp := h.post("/network/groups/"+groupID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/network/groups/" + groupID)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdrawing a group returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM net_group WHERE id = ?`, groupID); got != "retired" {
		t.Errorf("group lifecycle = %q, want retired", got)
	}
	// And the cascade actually happened, which is the half a status code
	// cannot show.
	if got := h.lookup(`SELECT COUNT(*) FROM net_group_member
	                    WHERE group_id = ? AND lifecycle = 'active'`, groupID); got != "0" {
		t.Errorf("%s memberships are still active after the group was withdrawn; "+
			"each is now stranded, with no route back to any live group", got)
	}
}

// TestOnlyAnAdministratorCanUnpickTheReachabilityLayer, at both layers.
func TestOnlyAnAdministratorCanUnpickTheReachabilityLayer(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	groupID := firstNetGroup(t, h)
	project := h.lookup(`SELECT id FROM project WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	t.Run("observer", func(t *testing.T) {
		viewer := newHarness(t)
		viewer.login("viewer", "viewer-password")
		resp := viewer.post("/network/groups/"+groupID+"/retire", url.Values{
			"csrf_token": {viewer.csrfToken("/network")},
		}, false)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("an Observer got %d withdrawing a group, want 403", resp.StatusCode)
		}
	})

	t.Run("project owner", func(t *testing.T) {
		owner := newHarness(t)
		mustWebProjectOwner(t, owner, "po-net", "po-net-password", project)
		owner.login("po-net", "po-net-password")
		// Through the middleware, refused by permit.Covers: net_group is
		// ScopeTopology, which no project scope can cover.
		resp := owner.post("/network/groups/"+groupID+"/retire", url.Values{
			"csrf_token": {owner.csrfToken("/network")},
		}, false)
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusSeeOther {
			t.Error("a project owner withdrew a forwarder group; net_group is ScopeTopology")
		}
	})

	if got := h.lookup(`SELECT lifecycle FROM net_group WHERE id = ?`, groupID); got != "active" {
		t.Errorf("the group is %q after two refused attempts, want active", got)
	}
}

// TestAForwarderGroupsImpactSemanticsCanBeCorrected.
//
// availability, min_healthy and failover_mode are what HANDOVER §3.3 calls the
// semantics that make impact analysis mean anything: they decide whether losing
// a member degrades a group or kills it. Declared wrong, every verdict computed
// through that group is wrong -- and the only previous fix was to retire the
// group, which cascades away every member, uplink, attachment and anchor, then
// redraw all of them to correct one number.
//
// The same class as dependency.nature: a wrong ANSWER, not a wrong label.
func TestAForwarderGroupsImpactSemanticsCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstNetGroup(t, h)

	form := url.Values{
		"csrf_token":   {h.csrfToken("/network/groups/" + id)},
		"code":         {h.lookup(`SELECT code FROM net_group WHERE id = ?`, id)},
		"name":         {"Corrected core"},
		"kind":         {h.lookup(`SELECT kind FROM net_group WHERE id = ?`, id)},
		"role":         {h.lookup(`SELECT role FROM net_group WHERE id = ?`, id)},
		"availability": {"active_active"},
		"min_healthy":  {"2"},
		"row_version":  {h.lookup(`SELECT row_version FROM net_group WHERE id = ?`, id)},
	}
	resp := h.post("/network/groups/"+id, form, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a group returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT min_healthy FROM net_group WHERE id = ?`, id); got != "2" {
		t.Errorf("min_healthy = %s after the correction, want 2 -- this is what "+
			"decides whether losing a member degrades this group or kills it", got)
	}
	if got := h.lookup(`SELECT name FROM net_group WHERE id = ?`, id); got != "Corrected core" {
		t.Errorf("name = %q after the correction", got)
	}
	// The members are untouched: correcting is not retire-and-redraw, which is
	// the entire reason this path is worth having.
	if got := h.lookup(`SELECT COUNT(*) FROM net_group_member
	                    WHERE group_id = ? AND lifecycle = 'active'`, id); got == "0" {
		t.Error("the group's members went away when it was corrected; a correction " +
			"must not cascade the way a withdrawal does")
	}
}

// TestAnAnchorCanBeMovedToTheRightGroup.
//
// group_id is the field this exists for. internal/domain calls a misplaced
// anchor "the single highest-leverage wrong row in this model", and the only
// previous fix was to withdraw it and place another -- which records a
// withdrawal and a fresh declaration in the audit trail where what actually
// happened was a correction.
func TestAnAnchorCanBeMovedToTheRightGroup(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.lookup(`SELECT id FROM net_anchor WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
	from := h.lookup(`SELECT group_id FROM net_anchor WHERE id = ?`, id)
	to := h.lookup(`SELECT id FROM net_group WHERE lifecycle = 'active' AND id <> ?
	                ORDER BY id LIMIT 1`, from)
	if to == "" {
		t.Skip("the estate has only one forwarder group, so there is nowhere to move it")
	}

	// Offered by the page, not just the router.
	if !strings.Contains(body(t, h.get("/network", false)), "/network?edit="+id) {
		t.Fatal("the topology page offers no Edit control for an anchor")
	}

	resp := h.post("/network/anchors/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/network")},
		"code":        {h.lookup(`SELECT code FROM net_anchor WHERE id = ?`, id)},
		"name":        {h.lookup(`SELECT name FROM net_anchor WHERE id = ?`, id)},
		"scope":       {h.lookup(`SELECT scope FROM net_anchor WHERE id = ?`, id)},
		"plane":       {h.lookup(`SELECT plane FROM net_anchor WHERE id = ?`, id)},
		"group_id":    {to},
		"row_version": {h.lookup(`SELECT row_version FROM net_anchor WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("moving an anchor returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT group_id FROM net_anchor WHERE id = ?`, id); got != to {
		t.Errorf("group_id = %q, want %q -- the anchor did not move", got, to)
	}
	// Corrected, not replaced: same row, so its history stays in one place.
	if got := h.lookup(`SELECT lifecycle FROM net_anchor WHERE id = ?`, id); got != "active" {
		t.Errorf("the anchor is %q after a correction, want active", got)
	}
}

// TestAnAnchorCannotBeMovedOntoAWithdrawnGroup. An anchor pointing at a retired
// group is invisible to the M3 loader, so it would silently stop anchoring
// anything while still reading as declared.
func TestAnAnchorCannotBeMovedOntoAWithdrawnGroup(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.lookup(`SELECT id FROM net_anchor WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
	from := h.lookup(`SELECT group_id FROM net_anchor WHERE id = ?`, id)
	dead := h.lookup(`SELECT id FROM net_group WHERE lifecycle = 'active' AND id <> ?
	                  ORDER BY id LIMIT 1`, from)
	if dead == "" {
		t.Skip("only one group in the estate")
	}
	h.exec(`UPDATE net_group SET lifecycle = 'retired' WHERE id = ?`, dead)

	resp := h.post("/network/anchors/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/network")},
		"code":        {h.lookup(`SELECT code FROM net_anchor WHERE id = ?`, id)},
		"name":        {h.lookup(`SELECT name FROM net_anchor WHERE id = ?`, id)},
		"scope":       {h.lookup(`SELECT scope FROM net_anchor WHERE id = ?`, id)},
		"plane":       {h.lookup(`SELECT plane FROM net_anchor WHERE id = ?`, id)},
		"group_id":    {dead},
		"row_version": {h.lookup(`SELECT row_version FROM net_anchor WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT group_id FROM net_anchor WHERE id = ?`, id); got == dead {
		t.Error("an anchor was moved onto a withdrawn group. The M3 loader excludes " +
			"that group, so the anchor silently stops anchoring anything while " +
			"still reading as declared")
	}
}
