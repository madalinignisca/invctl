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

// An SSID could not be corrected. `UpdateWirelessLAN` shipped with WP-F1 --
// validating, checking the security vocabulary, guarding on `row_version`,
// writing its `change_log` entry, reindexing for search -- with no route, no
// handler and no form. Declare, add radios, withdraw: no correction.
//
// THE SAME GAP WP-F1 FOUND IN VLANs AND FIXED HOURS EARLIER, reproduced in the
// feature that found it. The create/retire pair is what gets built; the
// correction path is what gets forgotten.
//
// It surfaced on the demo: a top-up creates an SSID once and then skips it,
// deliberately, so it never clobbers what somebody set -- which means an SSID
// declared before its VLAN existed can never gain one from a later top-up.

// TestAWirelessLANCanGainItsVLAN is the demo's actual problem, as a test.
func TestAWirelessLANCanGainItsVLAN(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id, f := firstWLAN(t, h)
	vlanID, vlanLabel := someVLAN(t, h)

	resp := h.post("/wireless/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/wireless")},
		"ssid":        {f["ssid"]},
		"name":        {f["name"]},
		"security":    {f["security"]},
		"vlan_id":     {vlanID},
		"row_version": {f["row_version"]},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("attaching a VLAN returned %d, want 303", resp.StatusCode)
	}

	list := body(t, h.get("/wireless", false))
	if !strings.Contains(list, vlanLabel) {
		t.Errorf("the wireless list does not show VLAN %q against any SSID, so the "+
			"correction did not take -- which is the demo's exact complaint", vlanLabel)
	}
}

// TestCorrectingAWirelessLANKeepsItsPSKReference is the trap this row's hidden
// inputs exist for, and the one with a security edge.
//
// UpdateWirelessLAN writes EVERY column, so a field the form does not carry
// arrives as nil and is cleared. `psk_ref` is never displayed as a value
// anywhere -- a map of credential paths readable by every session is the
// disclosure rbac-design.md draws a line at -- so it rides as a hidden input.
// Drop that input and correcting an SSID's VLAN silently erases the reference
// to where its key lives.
func TestCorrectingAWirelessLANKeepsItsPSKReference(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id, f := wlanWithAPSKRef(t, h)
	before := f["psk_ref"]

	resp := h.post("/wireless/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/wireless")},
		"ssid":        {f["ssid"]},
		"name":        {"corrected-name"},
		"security":    {f["security"]},
		"psk_ref":     {before},
		"row_version": {f["row_version"]},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting returned %d, want 303", resp.StatusCode)
	}

	after := wlanEditFields(t, h, id)
	if after["psk_ref"] != before {
		t.Errorf("the PSK reference went %q -> %q when the SSID was renamed. The form "+
			"must carry every column UpdateWirelessLAN writes, or a correction blanks "+
			"what it did not send", before, after["psk_ref"])
	}
}

// TestOnlyAnAdministratorCanCorrectAWirelessLAN. wireless_lan is
// ScopeTopology, the same grant Withdraw already asks for.
func TestOnlyAnAdministratorCanCorrectAWirelessLAN(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id, f := firstWLAN(t, h)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/wireless/"+id, url.Values{
		"csrf_token":  {viewer.csrfToken("/wireless")},
		"ssid":        {"taken-over"},
		"name":        {f["name"]},
		"security":    {f["security"]},
		"row_version": {f["row_version"]},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("an Observer corrected a wireless LAN; wireless_lan is ScopeTopology")
	}
	if strings.Contains(body(t, h.get("/wireless", false)), "taken-over") {
		t.Error("an Observer's correction reached the database despite the refusal")
	}
}

// wlanEditFields opens an SSID's edit row and reads its inputs back.
func wlanEditFields(t *testing.T, h *harness, id string) map[string]string {
	t.Helper()
	page := body(t, h.get("/wireless?edit="+id, false))
	out := map[string]string{}
	for _, f := range []string{"ssid", "name", "row_version", "psk_ref", "notes"} {
		out[f], _ = attrAfter(page, `name="`+f+`" value="`, `"`)
	}
	// security is a select, so its value is the selected option.
	if i := strings.Index(page, `name="security"`); i >= 0 {
		if v, ok := attrAfter(page[i:], `<option value="`, `"`); ok {
			out["security"] = v
		}
		if j := strings.Index(page[i:], "selected"); j >= 0 {
			seg := page[i : i+j]
			if k := strings.LastIndex(seg, `<option value="`); k >= 0 {
				if v, ok := attrAfter(seg[k:], `<option value="`, `"`); ok {
					out["security"] = v
				}
			}
		}
	}
	if out["row_version"] == "" {
		t.Fatalf("no row_version in the edit row for %s; UpdateWirelessLAN refuses "+
			"without it and every assertion here would be moot", id)
	}
	return out
}

func firstWLAN(t *testing.T, h *harness) (string, map[string]string) {
	t.Helper()
	id, ok := attrAfter(body(t, h.get("/wireless", false)), `href="/wireless?edit=`, `"`)
	if !ok {
		t.Fatal("no editable SSID on /wireless, so this test would pass by doing nothing")
	}
	return id, wlanEditFields(t, h, id)
}

// wlanWithAPSKRef finds an SSID that actually has a reference recorded, so the
// preservation test cannot pass by comparing two empty strings.
func wlanWithAPSKRef(t *testing.T, h *harness) (string, map[string]string) {
	t.Helper()
	rest := body(t, h.get("/wireless", false))
	for {
		id, ok := attrAfter(rest, `href="/wireless?edit=`, `"`)
		if !ok {
			t.Fatal("no seeded SSID carries a psk_ref; this test cannot tell a preserved " +
				"reference from an absent one and would pass vacuously")
		}
		f := wlanEditFields(t, h, id)
		if f["psk_ref"] != "" {
			return id, f
		}
		i := strings.Index(rest, `href="/wireless?edit=`)
		rest = rest[i+len(`href="/wireless?edit=`):]
	}
}

// someVLAN returns a VLAN to attach, and the label the list renders for it.
func someVLAN(t *testing.T, h *harness) (string, string) {
	t.Helper()
	page := body(t, h.get("/vlans", false))
	id, ok := attrAfter(page, `href="/vlans?edit=`, `"`)
	if !ok {
		t.Fatal("no VLAN in the fixture to attach")
	}
	name, _ := attrAfter(body(t, h.get("/vlans?edit="+id, false)), `name="name" value="`, `"`)
	return id, name
}
