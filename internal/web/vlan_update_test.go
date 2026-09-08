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

// A VLAN could not be renamed. `UpdateVLAN` has been in the store since VLANs
// arrived -- validating, guarding on `row_version`, writing its `change_log`
// entry, reindexing for search -- and nothing anywhere called it. No route, no
// handler, no form. So a VLAN could be declared, have ports added and removed,
// and be withdrawn, and a mistyped name could only be fixed by withdrawing it
// and declaring another, losing its port membership and its history.
//
// It surfaced on the live demo, whose VLANs are named "VLAN 10" and "VLAN 30"
// by a seed predating the current naming, so a wireless top-up could not
// resolve `production-workloads` and left the guest SSID with no VLAN. The data
// was correctable and the product had no way to correct it.

// TestAVLANCanBeRenamed is the gap itself, closed.
func TestAVLANCanBeRenamed(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id, before := firstVLAN(t, h)
	resp := h.post("/vlans/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/vlans")},
		"vid":         {before.vid},
		"name":        {"renamed-by-test"},
		"row_version": {before.rowVersion},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("renaming a VLAN returned %d, want 303", resp.StatusCode)
	}

	after := body(t, h.get("/vlans", false))
	if !strings.Contains(after, "renamed-by-test") {
		t.Error("the VLAN list does not show the new name, so the correction did not take")
	}
	if strings.Contains(after, before.name) && before.name != "renamed-by-test" {
		t.Errorf("the old name %q is still listed; a rename that leaves both is a "+
			"duplicate, not a correction", before.name)
	}
}

// TestRenamingAVLANThroughTheFormKeepsWhatTheFormDoesNotShow is the trap this
// edit row exists to avoid.
//
// UpdateVLAN writes EVERY column, and an omitted field arrives as nil -- so a
// form that forgets to carry a value CLEARS it. That is correct for a select
// whose empty option means "none", and it is a silent data loss for a field the
// row does not display: `description` is not a visible column here, so it rides
// as a hidden input, and if that input were ever dropped then renaming a VLAN
// would quietly erase its description. A save that looks like it worked and
// destroyed a fact.
//
// So this round-trips through the RENDERED form rather than a hand-built one:
// read back exactly what the page offers, change only the name, and post that.
// Remove the hidden input from the template and this goes red.
func TestRenamingAVLANThroughTheFormKeepsWhatTheFormDoesNotShow(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id, before := firstVLAN(t, h)

	// Give it a description, which the edit row never displays.
	const note = "carried-in-a-hidden-input"
	first := h.post("/vlans/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/vlans")},
		"vid":         {before.vid},
		"name":        {before.name},
		"description": {note},
		"row_version": {before.rowVersion},
	}, false)
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("setting a description returned %d, want 303", first.StatusCode)
	}

	// Now rename, sending back only what the form itself renders.
	reopened := vlanEditFields(t, h, id)
	page := body(t, h.get("/vlans?edit="+id, false))
	carried, ok := attrAfter(page, `name="description" value="`, `"`)
	if !ok {
		t.Fatal("the edit row renders no description field at all, so renaming a VLAN " +
			"clears its description -- the exact silent loss this test exists for")
	}
	resp := h.post("/vlans/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/vlans")},
		"vid":         {reopened.vid},
		"name":        {"renamed-and-kept-its-note"},
		"description": {carried},
		"row_version": {reopened.rowVersion},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("renaming returned %d, want 303", resp.StatusCode)
	}

	detail := body(t, h.get("/vlans/"+id, false))
	if !strings.Contains(detail, note) {
		t.Errorf("the description %q vanished when the VLAN was renamed; the form did not "+
			"carry it back and UpdateVLAN wrote nil over it", note)
	}
}

// TestOnlyAnAdministratorCanRenameAVLAN. `vlan` is ScopeTopology, the same
// grant the Withdraw button already asks for, so the correction path must not
// be a wider door into the same table.
//
// WHERE THE REFUSAL COMES FROM, measured rather than assumed: an Observer gets
// 403, and still gets 403 with this route moved OUT of the write bucket. So the
// gate here is the store's permit check -- tx.log and Covers, the chokepoint
// CLAUDE.md names -- and not the middleware. That is the stronger of the two
// and the right one to be relying on.
//
// The consequence for this test is worth stating: it does NOT pin the route's
// bucket, because it passes either way. `write_routes.txt` pins that
// separately, listing this route as `write | true`. A mutation moving the route
// to read() therefore does not turn this red, and that is not a hole in the
// test -- it is two different guards each holding their own end.
func TestOnlyAnAdministratorCanRenameAVLAN(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id, before := firstVLAN(t, h)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/vlans/"+id, url.Values{
		"csrf_token":  {viewer.csrfToken("/vlans")},
		"vid":         {before.vid},
		"name":        {"renamed-by-an-observer"},
		"row_version": {before.rowVersion},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("an Observer renamed a VLAN. vlan is ScopeTopology and this is a write to it")
	}

	after := body(t, h.get("/vlans", false))
	if strings.Contains(after, "renamed-by-an-observer") {
		t.Error("an Observer's rename reached the database even though the response refused it")
	}
}

// vlanFields is what the list page shows about one VLAN, read back out of it.
type vlanFields struct{ vid, name, rowVersion, role string }

// firstVLAN returns any VLAN and the fields needed to submit a correction.
//
// Read from the rendered edit form rather than constructed, so the test posts
// what a browser would post -- including the row_version, which UpdateVLAN
// refuses without.
func firstVLAN(t *testing.T, h *harness) (string, vlanFields) {
	t.Helper()
	list := body(t, h.get("/vlans", false))
	id, ok := attrAfter(list, `href="/vlans?edit=`, `"`)
	if !ok {
		t.Fatal("no editable VLAN on /vlans, so this test would pass by doing nothing")
	}
	return id, vlanEditFields(t, h, id)
}

// vlanEditFields opens the edit row and reads the values out of it.
func vlanEditFields(t *testing.T, h *harness, id string) vlanFields {
	t.Helper()
	page := body(t, h.get("/vlans?edit="+id, false))
	f := vlanFields{}
	f.vid, _ = attrAfter(page, `name="vid" value="`, `"`)
	f.name, _ = attrAfter(page, `name="name" value="`, `"`)
	f.rowVersion, _ = attrAfter(page, `name="row_version" value="`, `"`)
	f.role, _ = attrAfter(page, `name="role" value="`, `"`)
	if f.vid == "" || f.rowVersion == "" {
		t.Fatalf("the edit row for %s did not render a vid and a row_version; without "+
			"the version UpdateVLAN refuses the write and every assertion here is moot", id)
	}
	return f
}

// attrAfter returns the text between prefix and the next occurrence of end.
func attrAfter(page, prefix, end string) (string, bool) {
	i := strings.Index(page, prefix)
	if i < 0 {
		return "", false
	}
	rest := page[i+len(prefix):]
	j := strings.Index(rest, end)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
