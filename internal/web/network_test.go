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

// ---------------------------------------------------------------------------
// M2: reachability data model -- forwarder groups, uplinks, attachments,
// anchors, and propose-only derivation.

// mustNetGroupWeb declares a forwarder group directly through the store, for
// tests whose focus is the web layer rather than group creation itself.
func mustNetGroupWeb(t *testing.T, h *harness, code, role, availability string) string {
	t.Helper()
	spec := domain.NetGroupSpec{Code: code, Name: code, Kind: domain.NetGroupStandalone, Role: role, Availability: availability}
	g, err := domain.NewNetGroup(store.NewID(), spec, h.store.Now())
	if err != nil {
		t.Fatalf("building net group: %v", err)
	}
	if err := h.store.CreateNetGroup(context.Background(), domain.AdministratorPermit(domain.SystemActor), g); err != nil {
		t.Fatalf("creating net group: %v", err)
	}
	return g.ID
}

// TestReadOnlyUserCannotWriteReachabilityTopology extends the RBAC model to every
// new M2 write route.
func TestReadOnlyUserCannotWriteReachabilityTopology(t *testing.T) {
	h := newHarness(t)
	// A test-scoped code: the seed now declares sw-core/fw-edge/sw-oob and
	// net_group.code is UNIQUE. This test is about RBAC on the routes, not
	// about the group's identity, so any code will do.
	groupID := mustNetGroupWeb(t, h, "rbac-test-group", domain.NetRoleCore, domain.AvailStandalone)
	h.login("viewer", "viewer-password")

	cases := []struct{ name, path string }{
		{"declare group", "/network/groups"},
		{"add member", "/network/groups/" + groupID + "/members"},
		{"declare uplink", "/network/groups/" + groupID + "/uplinks"},
		{"declare attachment", "/network/attachments"},
		{"declare anchor", "/network/anchors"},
		{"derive", "/network/derive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := h.csrfToken("/network")
			resp := h.post(tc.path, url.Values{"csrf_token": {token}}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s as viewer returned %d, want 403", tc.path, resp.StatusCode)
			}
		})
	}
}

// TestCSRFIsEnforcedOnReachabilityRoutes: a route added without a form referencing
// the shared token is exactly the kind of gap that goes unnoticed.
func TestCSRFIsEnforcedOnReachabilityRoutes(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/network/groups", url.Values{
		"csrf_token":   {"not-a-real-token"},
		"code":         {"csrf-net"},
		"name":         {"CSRF net"},
		"kind":         {domain.NetGroupStandalone},
		"role":         {domain.NetRoleCore},
		"availability": {domain.AvailStandalone},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if _, err := h.store.GetNetGroup(context.Background(), "csrf-net"); err == nil {
		t.Error("a group was created despite the CSRF failure")
	}
}

// TestNetworkGroupValidationIs422 drives the availability/min_healthy pairing
// through the handler: CLAUDE.md's rule that a validation failure re-renders
// the form partial at 422, never a 200 with the error buried in the body.
func TestNetworkGroupValidationIs422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/network")

	resp := h.post("/network/groups", url.Values{
		"csrf_token":   {token},
		"code":         {"sw-bad"},
		"name":         {"Bad group"},
		"kind":         {domain.NetGroupMCLAG},
		"role":         {domain.NetRoleCore},
		"availability": {domain.AvailActiveActive},
		// min_healthy deliberately omitted -- required for active_active.
	}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(text, `id="net-group-form"`) {
		t.Error("the response is not the group form partial")
	}
	if !strings.Contains(text, "required for active_active") {
		t.Error("the response does not explain the min_healthy requirement")
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}

	groups, err := h.store.ListNetGroups(context.Background())
	if err != nil {
		t.Fatalf("listing groups: %v", err)
	}
	for _, g := range groups {
		if g.Code == "sw-bad" {
			t.Error("an invalid group was created despite failing validation")
		}
	}
}

// TestNetworkDeriveProposesAndDoesNotWriteThroughUI drives the propose-only
// contract through the real router: posting to /network/derive must render a
// proposal and must not create any row anywhere in the topology tables.
//
// WP-G1 Task 15: this is the test the brief names as
// TestNetworkDeriveProposesAndDoesNotWriteAnything and asks to be linked to
// the task -- it already existed (network topology's own M5 propose-only
// guarantee), and the brief's own reasoning for exempting POST
// /network/derive from a per-row scope check is exactly what this test
// pins: no write means no change_log row means nothing for Covers to ever
// check. The change_log count assertion below is the part Task 15 adds --
// the attachment-count assertion above proves no TOPOLOGY row appeared;
// this proves no AUDIT row did either, which is the more direct statement
// of "there is nothing here for the seam to authorize". If derive ever
// starts writing, this is the assertion that fails loudly, not silently
// passing because nobody thought to look at change_log specifically.
//
// Since M5 the seed declares an attachment for every cable it lays, so
// derivation against an untouched fixture correctly proposes nothing -- that
// is the fixture agreeing with itself, and it would make this test assert
// nothing. So the test patches in one cable derivation has never seen: a new
// server into a spare port on sw-core-1, whose group the seed already
// declares.
//
// The propose-only half is asserted as "the count did not change", not as
// "the count is zero". Repointing it at the seeded group and changing 0 to 3
// would destroy the guarantee: derivation could then write one extra row and
// the test would still pass.
func TestNetworkDeriveProposesAndDoesNotWriteThroughUI(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	swCoreAssetID := h.refs.Assets["sw-core-1"]
	groupID := h.refs.NetGroups["sw-core"]
	if swCoreAssetID == "" || groupID == "" {
		t.Fatal("fixture does not have sw-core-1 in a declared sw-core group")
	}

	// An undeclared host on an undeclared port. is_mgmt is false on both ends,
	// so the proposal must come out on the data plane.
	host := mustServerAssetWeb(t, h, "test-derive-host")
	hostPort := mustInterfaceWeb(t, h, host, "eno1")
	switchPort := mustInterfaceWeb(t, h, swCoreAssetID, "Ethernet40")
	link, err := domain.NewLink(store.NewID(), hostPort, switchPort)
	if err != nil {
		t.Fatalf("building link: %v", err)
	}
	if err := h.store.CreateLink(ctx, domain.AdministratorPermit(domain.SystemActor), link); err != nil {
		t.Fatalf("creating link: %v", err)
	}

	before, err := h.store.ListNetAttachments(ctx, groupID)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)

	token := h.csrfToken("/network")
	resp := h.post("/network/derive", url.Values{"csrf_token": {token}}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (derivation renders, it does not redirect)", resp.StatusCode)
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}
	if !strings.Contains(text, "Derivation proposal") {
		t.Error("the response is not the derivation result partial")
	}
	if !strings.Contains(text, "test-derive-host") {
		t.Error("the proposal does not name test-derive-host, which this test cables to sw-core-1")
	}

	after, err := h.store.ListNetAttachments(ctx, groupID)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("derivation changed the attachment count from %d to %d through the UI; "+
			"it must be propose-only", len(before), len(after))
	}
	if changeLogAfter := h.count(`SELECT COUNT(*) FROM change_log`); changeLogAfter != changeLogBefore {
		t.Errorf("change_log grew from %d to %d rows; derivation must write nothing at all, "+
			"which is the whole reason it needs no per-row scope check",
			changeLogBefore, changeLogAfter)
	}
}

func mustServerAssetWeb(t *testing.T, h *harness, name string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), domain.KindServer, name, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := h.store.CreateAsset(context.Background(), domain.AdministratorPermit(domain.SystemActor), a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

func mustInterfaceWeb(t *testing.T, h *harness, assetID, name string) string {
	t.Helper()
	i, err := domain.NewInterface(store.NewID(), assetID, name, domain.FFSFP28)
	if err != nil {
		t.Fatalf("building interface %s: %v", name, err)
	}
	if err := h.store.CreateInterface(context.Background(), domain.AdministratorPermit(domain.SystemActor), i); err != nil {
		t.Fatalf("creating interface %s: %v", name, err)
	}
	return i.ID
}

// TestVocabularyValuesRoundTripThroughAForm covers the two ways migration
// 00004's lookup tables meet the HTTP layer, both of which are new.
//
// The `+` case is the one worth having. `sfp+` and `qsfp+` are the only
// vocabulary codes containing a character that means something else in a URL:
// html/template escapes it to `&#43;` in the option's value attribute and
// form-urlencoding turns a raw `+` into a space, so a code that survives
// neither would silently become "sfp " and fail the foreign key -- with the
// dropdown looking perfectly correct. The harness already un-escapes `&#43;`
// for the CSRF token for the same reason.
//
// The unknown-value case asserts the contract the vocabulary change must not
// cost: the form comes back at 422 with the field named, not a bare "that
// request was not valid". The list in the message is read from the lookup
// table, so it is the values that exist now rather than the ones that existed
// when the binary was built.
func TestVocabularyValuesRoundTripThroughAForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := mustServerAssetWeb(t, h, "vocab-probe")
	path := "/assets/" + assetID

	t.Run("a code containing a plus survives the round trip", func(t *testing.T) {
		resp := h.post(path+"/interfaces", url.Values{
			"csrf_token":  {h.csrfToken(path)},
			"name":        {"probe-sfpplus"},
			"form_factor": {domain.FFSFPPlus},
		}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", resp.StatusCode)
		}

		interfaces, err := h.store.ListInterfaces(context.Background(), assetID)
		if err != nil {
			t.Fatalf("listing interfaces: %v", err)
		}
		for _, i := range interfaces {
			if i.Name != "probe-sfpplus" {
				continue
			}
			if i.FormFactor != domain.FFSFPPlus {
				t.Fatalf("form_factor = %q, want %q", i.FormFactor, domain.FFSFPPlus)
			}
			return
		}
		t.Fatal("the interface was not stored")
	})

	t.Run("a form factor that is not in the lookup table", func(t *testing.T) {
		resp := h.post(path+"/interfaces", url.Values{
			"csrf_token":  {h.csrfToken(path)},
			"name":        {"probe-unknown"},
			"form_factor": {"osfp800"},
		}, true)
		text := body(t, resp)

		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", resp.StatusCode)
		}
		if !strings.Contains(text, `id="interface-form"`) {
			t.Error("the response is not the interface form partial")
		}
		if !strings.Contains(text, "must be one of") {
			t.Error("the response does not name the vocabulary the value is missing from")
		}
	})

	t.Run("the form offers what the table holds, not what Go was built with", func(t *testing.T) {
		if _, err := h.store.DB().Writer.Exec(h.store.DB().Rebind(
			`INSERT INTO interface_form_factor (code, label, sort_order) VALUES (?, ?, ?)`),
			"osfp800", "OSFP 800G", 65); err != nil {
			t.Fatalf("inserting the new form factor: %v", err)
		}

		page := body(t, h.get(path, false))
		if !strings.Contains(page, `value="osfp800"`) {
			t.Error("the form does not offer a form factor added as data; the lookup table " +
				"is doing nothing an unfrozen CHECK would not have done")
		}

		resp := h.post(path+"/interfaces", url.Values{
			"csrf_token":  {h.csrfToken(path)},
			"name":        {"probe-osfp"},
			"form_factor": {"osfp800"},
		}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303 -- the value was added and is still unusable",
				resp.StatusCode)
		}
	})
}

// TestNetworkGroupMemberRefusesGroupIDMismatch is task-11's server-side half:
// the group id in the path and the group_id the form actually submitted must
// agree, or the write is refused rather than silently landing in the group
// named by the path. That path/body split is exactly what the CSP-dead
// inline onchange= used to hide -- the path stayed on whichever group
// rendered first while the operator's actual pick sat unused in the body --
// so this drives the mismatch directly, without relying on app.js having run
// (it hasn't: this is a raw POST), which is the point.
func TestNetworkGroupMemberRefusesGroupIDMismatch(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	groupA := mustNetGroupWeb(t, h, "mismatch-a", domain.NetRoleCore, domain.AvailStandalone)
	groupB := mustNetGroupWeb(t, h, "mismatch-b", domain.NetRoleCore, domain.AvailStandalone)
	assetID := mustServerAssetWeb(t, h, "mismatch-member-asset")

	memberCountBefore := h.count(`SELECT COUNT(*) FROM net_group_member WHERE group_id IN (?, ?)`, groupA, groupB)
	changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)

	token := h.csrfToken("/network")
	resp := h.post("/network/groups/"+groupA+"/members", url.Values{
		"csrf_token": {token},
		"group_id":   {groupB}, // disagrees with the path -- the case app.js's rewrite exists to prevent
		"asset_id":   {assetID},
		"role":       {domain.RolePrimary},
	}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(text, `id="net-member-form"`) {
		t.Error("the response is not the member form partial")
	}
	if !strings.Contains(text, "does not match this form") {
		t.Error("the response does not explain the group_id/path disagreement")
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}

	if got := h.count(`SELECT COUNT(*) FROM net_group_member WHERE group_id IN (?, ?)`, groupA, groupB); got != memberCountBefore {
		t.Errorf("net_group_member row count changed from %d to %d despite the refusal -- "+
			"a mismatched group_id must write nothing, into either group", memberCountBefore, got)
	}
	if got := h.count(`SELECT COUNT(*) FROM change_log`); got != changeLogBefore {
		t.Errorf("change_log grew from %d to %d rows despite the refusal", changeLogBefore, got)
	}

	// The matching case -- group_id agrees with the path -- must still
	// succeed. Without this half, a bug that refused EVERY member write
	// (not just a mismatched one) would pass the assertions above too.
	resp2 := h.post("/network/groups/"+groupA+"/members", url.Values{
		"csrf_token": {h.csrfToken("/network")},
		"group_id":   {groupA},
		"asset_id":   {assetID},
		"role":       {domain.RolePrimary},
	}, false)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 for a matching group_id", resp2.StatusCode)
	}
	members, err := h.store.ListNetGroupMembers(ctx, groupA)
	if err != nil {
		t.Fatalf("listing members: %v", err)
	}
	found := false
	for _, m := range members {
		if m.AssetID == assetID {
			found = true
		}
	}
	if !found {
		t.Error("a matching group_id was refused, or the member was not written into groupA")
	}
}

// TestNetworkUplinkRefusesGroupIDMismatch mirrors the member-form guard above
// for the uplink form's identical shape (network.html:123).
func TestNetworkUplinkRefusesGroupIDMismatch(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	groupA := mustNetGroupWeb(t, h, "uplink-mismatch-a", domain.NetRoleCore, domain.AvailStandalone)
	groupB := mustNetGroupWeb(t, h, "uplink-mismatch-b", domain.NetRoleCore, domain.AvailStandalone)
	upstream := mustNetGroupWeb(t, h, "uplink-mismatch-upstream", domain.NetRoleDistribution, domain.AvailStandalone)

	uplinkCountBefore := h.count(`SELECT COUNT(*) FROM net_uplink WHERE group_id IN (?, ?)`, groupA, groupB)
	changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)

	token := h.csrfToken("/network")
	resp := h.post("/network/groups/"+groupA+"/uplinks", url.Values{
		"csrf_token":        {token},
		"group_id":          {groupB}, // disagrees with the path
		"upstream_group_id": {upstream},
		"plane":             {"data"},
	}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(text, `id="net-uplink-form"`) {
		t.Error("the response is not the uplink form partial")
	}
	if !strings.Contains(text, "does not match this form") {
		t.Error("the response does not explain the group_id/path disagreement")
	}

	if got := h.count(`SELECT COUNT(*) FROM net_uplink WHERE group_id IN (?, ?)`, groupA, groupB); got != uplinkCountBefore {
		t.Errorf("net_uplink row count changed from %d to %d despite the refusal", uplinkCountBefore, got)
	}
	if got := h.count(`SELECT COUNT(*) FROM change_log`); got != changeLogBefore {
		t.Errorf("change_log grew from %d to %d rows despite the refusal", changeLogBefore, got)
	}

	resp2 := h.post("/network/groups/"+groupA+"/uplinks", url.Values{
		"csrf_token":        {h.csrfToken("/network")},
		"group_id":          {groupA},
		"upstream_group_id": {upstream},
		"plane":             {"data"},
	}, false)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 for a matching group_id", resp2.StatusCode)
	}
	uplinks, err := h.store.ListNetUplinks(ctx, groupA)
	if err != nil {
		t.Fatalf("listing uplinks: %v", err)
	}
	if len(uplinks) == 0 {
		t.Error("a matching group_id was refused, or the uplink was not written into groupA")
	}
}
