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

// A reservation could not be corrected. UpdateIPRange was complete in the
// store -- validating, rewriting all four address columns together, guarding on
// row_version, writing its change_log entry and reindexing for search -- with
// nothing calling it.
//
// WHAT THAT COSTS IS WORSE THAN A TYPO. A reservation is a claim on space the
// allocator will never offer. Withdraw-and-redeclare, the only fix, returns the
// whole span to the allocator in the gap between the two -- which is exactly
// where a fresh allocation lands in the middle of the range being repaired.

func (h *harness) reserve(t *testing.T, start, end, role string) string {
	t.Helper()
	resp := h.post("/ip-ranges", url.Values{
		"csrf_token": {h.csrfToken("/prefixes")},
		"start_text": {start}, "end_text": {end}, "role": {role},
	}, false)
	resp.Body.Close()
	return h.lookup(`SELECT id FROM ip_range WHERE start_text = ? AND end_text = ?`, start, end)
}

// TestAReservationsBoundsCanBeCorrected, and the bytes move with the text.
//
// The four address columns are the point. The text is the label; addr_start and
// addr_end are what every containment question is answered from, so a
// correction that moved one and not the other would leave a reservation whose
// span nobody can see -- withholding the addresses it used to name and offering
// the ones it now claims.
func TestAReservationsBoundsCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.reserve(t, "10.240.5.10", "10.240.5.99", "dhcp")
	beforeEnd := h.lookup(`SELECT HEX(addr_end) FROM ip_range WHERE id = ?`, id)

	resp := h.post("/ip-ranges/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/prefixes")},
		"start_text":  {"10.240.5.10"},
		"end_text":    {"10.240.5.50"},
		"role":        {"dhcp"},
		"row_version": {h.lookup(`SELECT row_version FROM ip_range WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a reservation's bounds returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT end_text FROM ip_range WHERE id = ?`, id); got != "10.240.5.50" {
		t.Errorf("end_text = %s, want 10.240.5.50", got)
	}
	afterEnd := h.lookup(`SELECT HEX(addr_end) FROM ip_range WHERE id = ?`, id)
	if afterEnd == beforeEnd {
		t.Errorf("the label moved but addr_end did not (still %s). Containment is "+
			"answered from the bytes, so the reservation still withholds the old "+
			"span and the allocator will offer addresses inside the new one", beforeEnd)
	}
}

// TestAReservationThatEndsBeforeItBeginsIsRefused, with the row reopened
// showing what was typed.
//
// The house rule is 422 with the form re-rendered in error state. It matters
// more than usual here: a backwards range silently reserves nothing while
// looking like a reservation, so an accepted one is invisible.
func TestAReservationThatEndsBeforeItBeginsIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.reserve(t, "10.240.6.10", "10.240.6.99", "lb")

	resp := h.post("/ip-ranges/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/prefixes")},
		"start_text":  {"10.240.6.99"},
		"end_text":    {"10.240.6.10"},
		"role":        {"lb"},
		"row_version": {h.lookup(`SELECT row_version FROM ip_range WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a backwards reservation returned %d, want 422", resp.StatusCode)
	}
	if got := h.lookup(`SELECT end_text FROM ip_range WHERE id = ?`, id); got != "10.240.6.99" {
		t.Errorf("end_text = %s; the refused correction reached the database", got)
	}
	page := body(t, resp)
	if !strings.Contains(page, `value="10.240.6.99"`) || !strings.Contains(page, `value="10.240.6.10"`) {
		t.Error("the refusal did not redraw the row with what the operator typed, so " +
			"they see the stored bounds where they just typed new ones and cannot " +
			"tell whether it saved")
	}
}

// TestCorrectingAReservationKeepsItsVRF. The form does not carry vrf_id and
// UpdateIPRange writes every column, so a range in a VRF must not fall out of
// it when its note is fixed -- the same blanking trap as the wireless PSK
// reference, reached from the opposite direction: here the field is absent
// from the form by design rather than hidden in it.
func TestCorrectingAReservationKeepsItsVRF(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Inserted directly rather than through a form: this test is about a
	// column the reservation form does not carry, so reaching it through the
	// UI is not possible by construction -- which is the point.
	const vrf = "01a00000-0000-7000-8000-00000000vrf1"
	h.exec(`INSERT INTO vrf (id, name, lifecycle, created_at, updated_at)
	        VALUES (?, 'corrections-vrf', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, vrf)
	id := h.reserve(t, "10.240.7.10", "10.240.7.99", "dhcp")
	h.exec(`UPDATE ip_range SET vrf_id = ? WHERE id = ?`, vrf, id)

	resp := h.post("/ip-ranges/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/prefixes")},
		"start_text":  {"10.240.7.10"},
		"end_text":    {"10.240.7.99"},
		"role":        {"dhcp"},
		"description": {"corrected note"},
		"row_version": {h.lookup(`SELECT row_version FROM ip_range WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT COALESCE(vrf_id, '') FROM ip_range WHERE id = ?`, id); got != vrf {
		t.Errorf("vrf_id went %q -> %q when the note was corrected. UpdateIPRange "+
			"writes every column, so the handler must build from the stored row", vrf, got)
	}
}

// TestOnlyAnAdministratorCanCorrectAReservation, at both layers.
//
// AN OBSERVER-ONLY TEST WOULD PROVE THE WEAKER CLAIM. This route sits under
// the generic `write` registrar, so RequireWrite refuses an Observer on
// CanWrite before the handler runs -- the same refusal an entity of ANY scope
// would produce. A project owner has CanWrite = true, passes the middleware,
// and is refused by the store's permit.Covers against ip_range's ScopeTopology.
// The two refusals carry different statuses, which is what makes the layer
// assertable: 403 at the door, 404 inside.
func TestOnlyAnAdministratorCanCorrectAReservation(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.reserve(t, "10.240.8.10", "10.240.8.99", "dhcp")
	version := h.lookup(`SELECT row_version FROM ip_range WHERE id = ?`, id)
	project := h.lookup(`SELECT id FROM project WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	attempt := func(t *testing.T, who *harness, end string, wantStatus int) {
		t.Helper()
		resp := who.post("/ip-ranges/"+id, url.Values{
			"csrf_token":  {who.csrfToken("/prefixes")},
			"start_text":  {"10.240.8.10"},
			"end_text":    {end},
			"row_version": {version},
		}, false)
		defer resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Errorf("got %d, want %d -- the refusal came from a different layer than "+
				"this test is asserting", resp.StatusCode, wantStatus)
		}
		if got := h.lookup(`SELECT end_text FROM ip_range WHERE id = ?`, id); got == end {
			t.Error("the correction reached the database despite the refusal")
		}
	}

	t.Run("observer", func(t *testing.T) {
		viewer := newHarness(t)
		viewer.login("viewer", "viewer-password")
		attempt(t, viewer, "10.240.8.200", http.StatusForbidden)
	})

	t.Run("project owner", func(t *testing.T) {
		owner := newHarness(t)
		mustWebProjectOwner(t, owner, "po-range", "po-range-password", project)
		owner.login("po-range", "po-range-password")
		attempt(t, owner, "10.240.8.201", http.StatusNotFound)
	})
}
