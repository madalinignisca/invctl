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

// The FHRPGroup entry in internal/store/write_surface_test.go's
// writeSurfaceGaps claimed three separate things could not be fixed: a
// group's own fields, a member's priority, and its virtual address. These
// tests drive each correction through the real router, the way
// TestAVLANCanBeRenamed drives UpdateVLAN -- a store method with a test
// proves the SQL works; a route with a test proves an operator can reach it.

func mustFHRPWeb(t *testing.T, h *harness, num int, name string) string {
	t.Helper()
	g, err := domain.NewFHRPGroup(store.NewID(), domain.FHRPVRRP3, num, name)
	if err != nil {
		t.Fatalf("building group %s: %v", name, err)
	}
	if err := h.store.CreateFHRPGroup(context.Background(), domain.AdministratorPermit(domain.SystemActor), g); err != nil {
		t.Fatalf("creating group %s: %v", name, err)
	}
	return g.ID
}

// TestAnFHRPGroupCanBeCorrected is the first gap closed: protocol, group
// number, name and description no longer require withdraw-and-redeclare.
func TestAnFHRPGroupCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := mustFHRPWeb(t, h, 41, "gw-web-correct")
	page := body(t, h.get("/redundancy/"+id, false))
	rowVersion, ok := attrAfter(page, `name="row_version" value="`, `"`)
	if !ok {
		t.Fatal("the detail page renders no row_version for the group's correction form")
	}

	resp := h.post("/redundancy/"+id, url.Values{
		"csrf_token":   {h.csrfToken("/redundancy/" + id)},
		"protocol":     {domain.FHRPHSRP},
		"name":         {"gw-web-corrected"},
		"group_number": {"42"},
		"description":  {"the real segment this answers for"},
		"row_version":  {rowVersion},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting the group returned %d, want 303", resp.StatusCode)
	}

	after := body(t, h.get("/redundancy/"+id, false))
	if !strings.Contains(after, "gw-web-corrected") {
		t.Error("the page does not show the corrected name")
	}
	if !strings.Contains(after, "HSRP") {
		t.Error("the page does not show the corrected protocol")
	}
	if !strings.Contains(after, "the real segment this answers for") {
		t.Error("the page does not show the corrected description")
	}
}

// TestAnFHRPGroupCorrectionIsRefusedOnADuplicateName is the 422 half: a
// refusal comes back with the form re-rendered and what was typed intact,
// not a redirect that throws the submission away.
func TestAnFHRPGroupCorrectionIsRefusedOnADuplicateName(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	mustFHRPWeb(t, h, 43, "gw-web-taken")
	id := mustFHRPWeb(t, h, 44, "gw-web-renaming")
	page := body(t, h.get("/redundancy/"+id, false))
	rowVersion, _ := attrAfter(page, `name="row_version" value="`, `"`)

	resp := h.post("/redundancy/"+id, url.Values{
		"csrf_token":   {h.csrfToken("/redundancy/" + id)},
		"protocol":     {domain.FHRPVRRP3},
		"name":         {"gw-web-taken"},
		"group_number": {"44"},
		"row_version":  {rowVersion},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("renaming onto a live name returned %d, want 422", resp.StatusCode)
	}
	text := body(t, resp)
	if !strings.Contains(text, "gw-web-renaming") {
		t.Error("the refused form does not show the typed name field intact -- " +
			"the page should reopen with what the operator typed")
	}
	if !strings.Contains(text, "already exists") {
		t.Error("the refusal does not explain what collided")
	}
}

// TestOnlyAnAdministratorCanCorrectAnFHRPGroup. fhrp_group is written the
// same way vlan and net_group are -- Administrator only, checked at the
// store's permit chokepoint, not the route bucket (write_routes.txt already
// pins this route as `write`, matching TestOnlyAnAdministratorCanRenameAVLAN's
// own reasoning for why that is not a hole).
func TestOnlyAnAdministratorCanCorrectAnFHRPGroup(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustFHRPWeb(t, h, 45, "gw-web-admin-only")
	page := body(t, h.get("/redundancy/"+id, false))
	rowVersion, _ := attrAfter(page, `name="row_version" value="`, `"`)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/redundancy/"+id, url.Values{
		"csrf_token":   {viewer.csrfToken("/redundancy/" + id)},
		"protocol":     {domain.FHRPVRRP3},
		"name":         {"gw-web-hijacked"},
		"group_number": {"45"},
		"row_version":  {rowVersion},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("an Observer corrected a redundancy group")
	}

	after := body(t, h.get("/redundancy/"+id, false))
	if strings.Contains(after, "gw-web-hijacked") {
		t.Error("an Observer's correction reached the database even though the response refused it")
	}
}

// TestAnFHRPMemberPriorityCanBeCorrected is the second gap: a wrong priority
// no longer needs the router removed and re-added.
func TestAnFHRPMemberPriorityCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	gid := mustFHRPWeb(t, h, 46, "gw-web-priority")
	assetID := mustServerAssetWeb(t, h, "fw-web-priority")
	ifaceID := mustInterfaceWeb(t, h, assetID, "eth0")

	add := h.post("/redundancy/"+gid+"/members", url.Values{
		"csrf_token":   {h.csrfToken("/redundancy/" + gid)},
		"interface_id": {ifaceID},
		"priority":     {"100"},
	}, false)
	add.Body.Close()
	if add.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding a member returned %d, want 303", add.StatusCode)
	}

	before, err := h.store.ListChangesForEntity(context.Background(), "fhrp_group", gid, 50)
	if err != nil {
		t.Fatalf("reading the change log: %v", err)
	}

	resp := h.post("/redundancy/"+gid+"/members/"+ifaceID, url.Values{
		"csrf_token": {h.csrfToken("/redundancy/" + gid)},
		"priority":   {"200"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting the priority returned %d, want 303", resp.StatusCode)
	}

	page := body(t, h.get("/redundancy/"+gid, false))
	if !strings.Contains(page, `value="200"`) {
		t.Error("the page does not show the corrected priority")
	}
	if strings.Contains(page, "No routers in this group") {
		t.Error("the router disappeared from the group when only its priority changed")
	}

	after, err := h.store.ListChangesForEntity(context.Background(), "fhrp_group", gid, 50)
	if err != nil {
		t.Fatalf("reading the change log: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Errorf("the priority correction wrote %d change_log entries, want exactly 1 -- "+
			"a departure-and-arrival pair would write two for a router that never left",
			len(after)-len(before))
	}
}

// TestAnFHRPGroupsVirtualAddressCanBeMoved is the third gap: the stuck-row
// bug named in the task itself. An address wrongly declared as a group's VIP
// could never be withdrawn, because RetireIPAddress refuses while
// fhrp_group_id is set and nothing anywhere cleared it.
func TestAnFHRPGroupsVirtualAddressCanBeMoved(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	gid := mustFHRPWeb(t, h, 47, "gw-web-vip")
	assetID := mustServerAssetWeb(t, h, "fw-web-vip")
	ifaceID := mustInterfaceWeb(t, h, assetID, "eth0")

	create := h.post("/addresses", url.Values{
		"csrf_token":   {h.csrfToken("/assets/" + assetID)},
		"asset_id":     {assetID},
		"interface_id": {ifaceID},
		"addr_text":    {"10.94.0.1"},
		"role":         {"vip"},
	}, false)
	create.Body.Close()
	if create.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating the wrong address returned %d, want 303", create.StatusCode)
	}
	wrongID := h.lookup(`SELECT id FROM ip_address WHERE addr_text = ?`, "10.94.0.1")
	if wrongID == "" {
		t.Fatal("the wrong address was not created")
	}

	ifaceID2 := mustInterfaceWeb(t, h, assetID, "eth1")
	create2 := h.post("/addresses", url.Values{
		"csrf_token":   {h.csrfToken("/assets/" + assetID)},
		"asset_id":     {assetID},
		"interface_id": {ifaceID2},
		"addr_text":    {"10.94.0.2"},
		"role":         {"vip"},
	}, false)
	create2.Body.Close()
	if create2.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating the right address returned %d, want 303", create2.StatusCode)
	}
	rightID := h.lookup(`SELECT id FROM ip_address WHERE addr_text = ?`, "10.94.0.2")
	if rightID == "" {
		t.Fatal("the right address was not created")
	}

	assign := h.post("/redundancy/"+gid+"/vip", url.Values{
		"csrf_token": {h.csrfToken("/redundancy/" + gid)},
		"address_id": {wrongID},
	}, false)
	assign.Body.Close()
	if assign.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring the wrong VIP returned %d, want 303", assign.StatusCode)
	}

	// STUCK, before the move: RetireIPAddress refuses a live VIP.
	//
	// IPAddressRetire answers a refusal with 303-plus-flash rather than a
	// non-2xx status -- it has no form to reopen (a retire button carries no
	// fields), so unlike the corrections above there is no status code to
	// assert here. The DB row is the honest check: still live, and still
	// naming its group.
	stuck := h.post("/addresses/"+wrongID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/redundancy/" + gid)},
	}, false)
	stuck.Body.Close()
	if life := h.lookup(`SELECT lifecycle FROM ip_address WHERE id = ?`, wrongID); life != "active" {
		t.Fatalf("the wrong address is %q after a refused retire, want active "+
			"(setup assumption failed)", life)
	}

	move := h.post("/redundancy/"+gid+"/vip", url.Values{
		"csrf_token": {h.csrfToken("/redundancy/" + gid)},
		"address_id": {rightID},
	}, false)
	move.Body.Close()
	if move.StatusCode != http.StatusSeeOther {
		t.Fatalf("moving the VIP returned %d, want 303", move.StatusCode)
	}

	page := body(t, h.get("/redundancy/"+gid, false))
	if !strings.Contains(page, "10.94.0.2") {
		t.Error("the page does not show the new virtual address")
	}
	// The OLD address legitimately reappears elsewhere on this same page: it
	// is released now, so the "move to a different address" picker offers it
	// again as a candidate for the NEXT move -- ListVIPCandidates is right to
	// list it. What must be gone is its row in the VIPs table itself.
	if strings.Contains(page, `<td class="mono">10.94.0.1</td>`) {
		t.Error("the page still lists the old address as a virtual address after the move")
	}
	if got := h.lookup(`SELECT COALESCE(fhrp_group_id, '') FROM ip_address WHERE id = ?`, wrongID); got != "" {
		t.Errorf("the old address's fhrp_group_id = %q after the move, want cleared", got)
	}

	// UNSTUCK: now that fhrp_group_id is clear, the released address can be
	// withdrawn -- the exact repair the task named. Checked in the DB, for
	// the same reason as the "stuck" assertion above: the status code alone
	// cannot distinguish success from a refusal here.
	unstuck := h.post("/addresses/"+wrongID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/redundancy/" + gid)},
	}, false)
	unstuck.Body.Close()
	if life := h.lookup(`SELECT lifecycle FROM ip_address WHERE id = ?`, wrongID); life != "retired" {
		t.Errorf("the released address is %q after withdrawing it, want retired -- it "+
			"should no longer be stuck", life)
	}
}
