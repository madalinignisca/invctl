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

// A SUPPLIER WAS THE ONLY ENTITY WITH A LIVE CREATE ROUTE AND NO REPAIR AT ALL.
// Not an unreachable Update method -- there was no Update method, and no Retire
// either, which is exactly why the reachability guard could never see it: that
// guard's population is built from Update* declarations, so an entity with none
// contributes nothing and passes in silence. The write-surface census
// (internal/store/write_surface_test.go) keys on creation instead, and this was
// its first and sharpest entry.
//
// What a wrong one costs: `name` is what somebody reads down the phone during
// an outage, `account_ref` is what they quote when they get through, and
// `portal_url` is where they go first.

func (h *harness) addProvider(t *testing.T, name string) string {
	t.Helper()
	resp := h.post("/providers", url.Values{
		"csrf_token": {h.csrfToken("/circuits")}, "name": {name},
		"account_ref": {"ACC-" + name},
	}, false)
	resp.Body.Close()
	// Scoped to the live row: a withdrawn carrier keeps its name (the unique
	// index is partial), so after a retire this name matches two rows and an
	// unscoped lookup would hand back whichever came first.
	return h.lookup(`SELECT id FROM provider WHERE name = ? AND lifecycle = 'active'`, name)
}

// TestASuppliersNameCanBeCorrected is the headline: a typo in the one string
// somebody reads down the phone.
func TestASuppliersNameCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.addProvider(t, "Colt Technlogy")

	resp := h.post("/providers/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/circuits")},
		"name":        {"Colt Technology"},
		"account_ref": {"ACC-Colt Technlogy"},
		"row_version": {h.lookup(`SELECT row_version FROM provider WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a supplier's name returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT name FROM provider WHERE id = ?`, id); got != "Colt Technology" {
		t.Errorf("name = %q after the correction, want %q", got, "Colt Technology")
	}
	if !strings.Contains(body(t, h.get("/circuits", false)), "Colt Technology") {
		t.Error("the corrected name does not appear on /circuits")
	}
}

// TestCorrectingASupplierKeepsItsCircuits. A provider is what circuits hang
// off, and circuit.provider_id is NOT NULL -- correcting the carrier must not
// disturb them. This is the whole reason a correction beats
// withdraw-and-redeclare here: there is no way to redeclare without moving
// every circuit by hand.
func TestCorrectingASupplierKeepsItsCircuits(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.lookup(`SELECT provider_id FROM circuit WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
	before := h.lookup(`SELECT COUNT(*) FROM circuit WHERE provider_id = ?`, id)
	if before == "0" {
		t.Fatal("that provider carries no circuits, so this test would prove nothing")
	}

	resp := h.post("/providers/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/circuits")},
		"name":        {"Renamed Carrier"},
		"row_version": {h.lookup(`SELECT row_version FROM provider WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting the carrier returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT COUNT(*) FROM circuit WHERE provider_id = ?`, id); got != before {
		t.Errorf("circuits on this provider went %s -> %s", before, got)
	}
}

// TestCorrectingASupplierKeepsItsDescription is the blanking trap. The
// description is not rendered as a column on this table, so it rides as a
// hidden input -- drop that and fixing a typo in the name silently erases it.
func TestCorrectingASupplierKeepsItsDescription(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.addProvider(t, "Noted Carrier")
	const note = "primary transit, 30-day termination clause"
	h.exec(`UPDATE provider SET description = ? WHERE id = ?`, note, id)

	page := body(t, h.get("/circuits?edit="+id, false))
	carried, ok := hiddenInForm(page, "prov-"+id, "description")
	if !ok || carried != note {
		t.Fatalf("the provider edit row carries description %q (ok=%v), want %q. "+
			"Without it the first correction blanks the note", carried, ok, note)
	}

	resp := h.post("/providers/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/circuits")},
		"name":        {"Noted Carrier Ltd"},
		"description": {carried},
		"row_version": {h.lookup(`SELECT row_version FROM provider WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT COALESCE(description, '') FROM provider WHERE id = ?`, id); got != note {
		t.Errorf("the description went %q -> %q when the name was corrected", note, got)
	}
}

// TestASupplierCarryingCircuitsCannotBeWithdrawn. circuit.provider_id is NOT
// NULL, so a withdrawn carrier under a live circuit keeps rendering on every
// one of their pages -- the estate would be saying the supplier is gone while
// still showing it. Refused, the same shape as RetirePowerFeed.
func TestASupplierCarryingCircuitsCannotBeWithdrawn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.lookup(`SELECT provider_id FROM circuit WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	resp := h.post("/providers/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/circuits")},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT lifecycle FROM provider WHERE id = ?`, id); got != "active" {
		t.Errorf("lifecycle = %q; a supplier carrying live circuits was withdrawn, "+
			"and every one of those circuits now names a carrier the estate says "+
			"is gone", got)
	}
	// The Withdraw control is not offered either, so this is never a button
	// that exists only to be refused.
	page := body(t, h.get("/circuits", false))
	if strings.Contains(page, "/providers/"+id+"/retire") {
		t.Error("the Withdraw button renders for a supplier that carries circuits")
	}
}

// TestASupplierWithNoCircuitsCanBeWithdrawn, and the name frees up.
//
// provider_name_key is partial -- UNIQUE(name) WHERE lifecycle <> 'retired' --
// so withdrawing releases the name, which is what somebody re-signing with a
// former supplier actually wants.
func TestASupplierWithNoCircuitsCanBeWithdrawn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.addProvider(t, "Departing Carrier")

	resp := h.post("/providers/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/circuits")},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdrawing an unused supplier returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM provider WHERE id = ?`, id); got != "retired" {
		t.Errorf("lifecycle = %q after withdrawal, want retired", got)
	}

	// The name is free again.
	again := h.addProvider(t, "Departing Carrier")
	if again == "" || again == id {
		t.Error("the withdrawn supplier's name was not released, so nobody can " +
			"re-sign with a former carrier under the name they trade as")
	}
}

// TestOnlyAnAdministratorCanRepairASupplier, at both layers -- provider is
// ScopeEstateConfig, which no project owner's permit can ever cover.
func TestOnlyAnAdministratorCanRepairASupplier(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.addProvider(t, "Guarded Carrier")
	version := h.lookup(`SELECT row_version FROM provider WHERE id = ?`, id)
	project := h.lookup(`SELECT id FROM project WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	attempt := func(t *testing.T, who *harness, name string, wantStatus int) {
		t.Helper()
		resp := who.post("/providers/"+id, url.Values{
			"csrf_token":  {who.csrfToken("/circuits")},
			"name":        {name},
			"row_version": {version},
		}, false)
		defer resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Errorf("%s got %d, want %d -- the refusal came from a different layer "+
				"than this test asserts", name, resp.StatusCode, wantStatus)
		}
		if got := h.lookup(`SELECT name FROM provider WHERE id = ?`, id); got == name {
			t.Errorf("%s's correction reached the database despite the refusal", name)
		}
	}

	t.Run("observer", func(t *testing.T) {
		viewer := newHarness(t)
		viewer.login("viewer", "viewer-password")
		// Stopped at the door: CanWrite is false.
		attempt(t, viewer, "observer-took-over", http.StatusForbidden)
	})

	t.Run("project owner", func(t *testing.T) {
		owner := newHarness(t)
		mustWebProjectOwner(t, owner, "po-prov", "po-prov-password", project)
		owner.login("po-prov", "po-prov-password")
		// Through the door and refused inside: a carrier is estate config, so
		// permit.Covers says no and a row outside your scope does not exist.
		attempt(t, owner, "owner-took-over", http.StatusNotFound)
	})
}

// hiddenInForm reads one hidden input belonging to the form with this id.
func hiddenInForm(page, formID, field string) (string, bool) {
	i := strings.Index(page, `<form id="`+formID+`"`)
	if i < 0 {
		return "", false
	}
	seg := page[i:]
	if end := strings.Index(seg, "</form>"); end >= 0 {
		seg = seg[:end]
	}
	j := strings.Index(seg, `name="`+field+`" value="`)
	if j < 0 {
		return "", false
	}
	return attrAfter(seg[j:], `name="`+field+`" value="`, `"`)
}
