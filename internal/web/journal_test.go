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

// Journal entries (WP-G3), through the real router.
//
// The properties worth protecting here are all properties of the whole stack:
// that the author comes from the session rather than the form, that a note is
// audited like any other declared change, and that a reader can tell a note
// from an audit entry on the timeline they share.

// note writes one through the form and returns the response.
func (h *harness) note(t *testing.T, resource, id, kind, body string) *http.Response {
	t.Helper()
	path := "/" + resource + "/" + id
	return h.post(path+"/journal", url.Values{
		"csrf_token": {h.csrfToken(path)},
		"kind":       {kind},
		"body":       {body},
	}, false)
}

// TestANoteIsAttributedToTheSessionNotTheForm.
//
// docs/AUDIT.md rule 5. An actor read from a request payload is an actor
// anybody can claim to be, and a note is a statement attributed to a person --
// which is precisely the thing worth forging.
func TestANoteIsAttributedToTheSessionNotTheForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	asset := h.asset("hv-01")

	// The form carries an author field naming somebody else. It must be ignored
	// rather than honoured, and the note must belong to the signed-in user.
	path := "/assets/" + asset
	resp := h.post(path+"/journal", url.Values{
		"csrf_token": {h.csrfToken(path)},
		"kind":       {"note"},
		"body":       {"firmware held back pending vendor case 41182"},
		"author":     {"somebody-else"},
		"actor":      {"somebody-else"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("writing a note returned %d, want 303", resp.StatusCode)
	}

	stored := h.lookup(`SELECT author FROM journal_entry WHERE entity_id = ?`, asset)
	admin := h.lookup(`SELECT id FROM app_user WHERE username = 'admin'`)
	if stored != admin {
		t.Errorf("the note is attributed to %q, want the signed-in admin %q — "+
			"attribution must come from the credential, never from the payload",
			stored, admin)
	}
	if stored == "somebody-else" {
		t.Error("the form's author field was honoured; anybody could sign a note " +
			"as anybody")
	}
}

// TestWritingANoteIsAudited. A note is declared state, so it takes a
// change_log row like every other mutation here.
func TestWritingANoteIsAudited(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	asset := h.asset("hv-01")

	before := h.lookup(`SELECT COUNT(*) FROM change_log WHERE entity_type = 'journal_entry'`)
	h.note(t, "assets", asset, "incident", "port flapping under load").Body.Close()
	after := h.lookup(`SELECT COUNT(*) FROM change_log WHERE entity_type = 'journal_entry'`)

	if before == after {
		t.Errorf("writing a note produced no change_log row (%s -> %s). A note is "+
			"declared state; withdrawing an inconvenient one without trace is what "+
			"the audit trail exists to prevent", before, after)
	}
}

// TestWithdrawingANoteIsSoftAndAudited.
//
// The row has to survive, because the change_log entry recording the withdrawal
// refers to it -- an audit trail pointing at a deleted row says nothing.
func TestWithdrawingANoteIsSoftAndAudited(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	asset := h.asset("hv-01")
	h.note(t, "assets", asset, "note", "replaced PSU B in March").Body.Close()

	id := h.lookup(`SELECT id FROM journal_entry WHERE entity_id = ?`, asset)
	path := "/assets/" + asset
	resp := h.post(path+"/journal/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken(path)},
	}, false)
	resp.Body.Close()

	if got := h.lookup(`SELECT COUNT(*) FROM journal_entry WHERE id = ?`, id); got != "1" {
		t.Errorf("the note row count is %s after withdrawal, want 1 — withdrawal is "+
			"soft, and the change_log entry for it refers to a row that must exist", got)
	}
	if got := h.lookup(`SELECT lifecycle FROM journal_entry WHERE id = ?`, id); got != "retired" {
		t.Errorf("lifecycle = %q after withdrawal, want retired", got)
	}
	// And it leaves the panel.
	page := body(t, h.get(path, false))
	if strings.Contains(page, "replaced PSU B in March") {
		t.Error("a withdrawn note is still on the page")
	}
}

// TestANoteAppearsOnTheTimelineLabelledAsANote.
//
// The whole reason for folding three ledgers into one ordering is that a reader
// can tell them apart. A note rendered as an audit entry would be the
// laundering rule 7 forbids, in the other direction.
func TestANoteAppearsOnTheTimelineLabelledAsANote(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	asset := h.asset("hv-01")
	h.note(t, "assets", asset, "decision", "kept on 7.4 deliberately, see case 41182").Body.Close()

	page := body(t, h.get("/assets/"+asset, false))
	if !strings.Contains(page, "kept on 7.4 deliberately") {
		t.Fatal("the note is not on the asset page at all")
	}
	// The timeline row carries the journal marker. Asserted on the title text
	// rather than a class, because the class could be renamed while the
	// distinction survives, and the title is what a reader actually gets.
	if !strings.Contains(page, "Somebody wrote this down.") {
		t.Error("the timeline does not mark the note as something a person wrote; " +
			"a note indistinguishable from an audit entry is worse than no note")
	}
}

// TestAnEmptyNoteIsRefused. The body is the whole content; an empty one is a
// row that says nothing and clutters a timeline somebody reads under pressure.
func TestAnEmptyNoteIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	asset := h.asset("hv-01")

	h.note(t, "assets", asset, "note", "   ").Body.Close()
	if got := h.lookup(`SELECT COUNT(*) FROM journal_entry WHERE entity_id = ?`, asset); got != "0" {
		t.Errorf("an empty note was stored (%s rows)", got)
	}
}

// TestAReaderCannotWriteANote. Write access is INV_ADMIN_USERS and a note is a
// write like any other.
func TestAReaderCannotWriteANote(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	asset := h.asset("hv-01")

	resp := h.note(t, "assets", asset, "note", "should never be stored")
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("a read-only user wrote a note and was redirected as if it worked")
	}
	if got := h.lookup(`SELECT COUNT(*) FROM journal_entry WHERE entity_id = ?`, asset); got != "0" {
		t.Errorf("a read-only user's note was stored (%s rows)", got)
	}
}

// TestANoteCannotBeEditedThroughAnotherEntitysURL.
//
// Not an authorization hole today -- write access is estate-wide -- and a
// correctness one: the redirect and the audit trail would name the wrong thing,
// and a note would appear to move between assets.
func TestANoteCannotBeEditedThroughAnotherEntitysURL(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	first, second := h.asset("hv-01"), h.asset("hv-02")
	h.note(t, "assets", first, "note", "belongs to hv-01").Body.Close()
	id := h.lookup(`SELECT id FROM journal_entry WHERE entity_id = ?`, first)

	path := "/assets/" + second
	resp := h.post(path+"/journal/"+id, url.Values{
		"csrf_token": {h.csrfToken(path)},
		"kind":       {"note"},
		"body":       {"rewritten through the wrong asset"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("editing hv-01's note through hv-02's URL returned %d, want 404",
			resp.StatusCode)
	}
	if got := h.lookup(`SELECT body FROM journal_entry WHERE id = ?`, id); got != "belongs to hv-01" {
		t.Errorf("the note body is now %q; it was editable through the wrong entity", got)
	}
}
