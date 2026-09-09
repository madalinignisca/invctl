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

// A circuit could not be corrected. UpdateCircuit was complete in the store --
// validating, guarding on row_version, writing its change_log entry and
// reindexing for search -- with nothing calling it.
//
// A CIRCUIT IS A FAILURE TARGET, not a record. Its terminations make "simulate
// cutting this" answer anything at all, and its impact history is what somebody
// reads during the incident. Retire-and-redeclare threw both away to fix a
// typed digit: the replacement has no ends until somebody lands them again, and
// the old circuit's log stops at a cessation that never happened.

func firstCircuit(t *testing.T, h *harness) string {
	t.Helper()
	return h.lookup(`SELECT id FROM circuit WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
}

// TestACircuitIdentifierCanBeCorrectedAndKeepsItsEnds is the whole point: the
// terminations survive, which is what retire-and-redeclare could not do.
func TestACircuitIdentifierCanBeCorrectedAndKeepsItsEnds(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := firstCircuit(t, h)
	ends := h.lookup(`SELECT COUNT(*) FROM circuit_termination WHERE circuit_id = ?`, id)
	if ends == "0" {
		t.Fatalf("circuit %s has no terminations, so this test cannot show that a "+
			"correction preserves them and would pass by checking nothing", id)
	}

	resp := h.post("/circuits/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/circuits/" + id)},
		"cid":         {"CID-CORRECTED-1"},
		"provider_id": {h.lookup(`SELECT provider_id FROM circuit WHERE id = ?`, id)},
		"row_version": {h.lookup(`SELECT row_version FROM circuit WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a circuit identifier returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT cid FROM circuit WHERE id = ?`, id); got != "CID-CORRECTED-1" {
		t.Errorf("cid = %q after the correction, want CID-CORRECTED-1", got)
	}
	if got := h.lookup(`SELECT COUNT(*) FROM circuit_termination WHERE circuit_id = ?`, id); got != ends {
		t.Errorf("terminations went %s -> %s. Correcting a circuit must not disturb "+
			"where it lands -- that is the whole reason this is not a retire and "+
			"redeclare", ends, got)
	}
	if got := h.lookup(`SELECT lifecycle FROM circuit WHERE id = ?`, id); got != "active" {
		t.Errorf("lifecycle = %q after a correction, want active", got)
	}
}

// TestCorrectingACircuitRetitlesItInSearch. UpdateCircuit reindexes, and the
// index titles a circuit by its CID -- so a correction that did not reach the
// index would leave the circuit findable only under the identifier that was
// wrong, which is worse than untidy at three in the morning.
func TestCorrectingACircuitRetitlesItInSearch(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := firstCircuit(t, h)
	resp := h.post("/circuits/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/circuits/" + id)},
		"cid":         {"CID-FINDME-2"},
		"provider_id": {h.lookup(`SELECT provider_id FROM circuit WHERE id = ?`, id)},
		"row_version": {h.lookup(`SELECT row_version FROM circuit WHERE id = ?`, id)},
	}, false)
	resp.Body.Close()

	if got := h.lookup(`SELECT COUNT(*) FROM search_index
	                    WHERE entity_id = ? AND title = ?`, id, "CID-FINDME-2"); got != "1" {
		t.Errorf("the search index still does not title circuit %s by its corrected "+
			"identifier, so it stays findable only under the wrong one", id)
	}
}

// TestCorrectingACircuitKeepsWhatTheFormDidNotSend is the blanking trap.
//
// UpdateCircuit writes every column. contract_end drives the expiry report --
// the thing that catches an auto-renewal at a rate nobody checked -- so a
// correction that dropped it would silence that report without saying so.
func TestCorrectingACircuitKeepsWhatTheFormDidNotSend(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := firstCircuit(t, h)
	h.exec(`UPDATE circuit SET contract_end = '2027-06-30' WHERE id = ?`, id)

	// Read the form back and post what it actually renders, rather than a
	// hand-built payload: a test that invents its own fields proves the store
	// works and says nothing about whether the form carries them.
	page := body(t, h.get("/circuits/"+id+"?edit="+id, false))
	end, ok := attrAfter(page, `name="contract_end"`+"\n"+`               value="`, `"`)
	if !ok {
		end, ok = valueOfInput(page, "contract_end")
	}
	if !ok || end != "2027-06-30" {
		t.Fatalf("the correction form does not render contract_end (got %q, ok=%v); "+
			"saving from it would silence the expiry report for this circuit", end, ok)
	}

	resp := h.post("/circuits/"+id, url.Values{
		"csrf_token":   {h.csrfToken("/circuits/" + id)},
		"cid":          {h.lookup(`SELECT cid FROM circuit WHERE id = ?`, id)},
		"provider_id":  {h.lookup(`SELECT provider_id FROM circuit WHERE id = ?`, id)},
		"contract_end": {end},
		"description":  {"corrected description"},
		"row_version":  {h.lookup(`SELECT row_version FROM circuit WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT COALESCE(contract_end, '') FROM circuit WHERE id = ?`, id); got != "2027-06-30" {
		t.Errorf("contract_end went 2027-06-30 -> %q when the description was "+
			"corrected. The expiry report is built on that column", got)
	}
}

// TestACircuitCannotBeCorrectedOntoAnothersIdentifier. The store refuses a
// duplicate CID within a provider; this asserts the handler reaches that
// refusal and redraws the form with what was typed rather than 500ing.
func TestACircuitCannotBeCorrectedOntoAnothersIdentifier(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	first := firstCircuit(t, h)
	provider := h.lookup(`SELECT provider_id FROM circuit WHERE id = ?`, first)

	// A second circuit at the SAME supplier, declared through the form -- the
	// seed gives each provider one, and the constraint being tested is
	// per-provider.
	const takenCID = "CID-ALREADY-TAKEN"
	resp := h.post("/circuits", url.Values{
		"csrf_token": {h.csrfToken("/circuits")},
		"cid":        {takenCID}, "provider_id": {provider},
	}, false)
	resp.Body.Close()
	if h.lookup(`SELECT COUNT(*) FROM circuit WHERE provider_id = ? AND cid = ?`,
		provider, takenCID) != "1" {
		t.Fatal("could not declare a second circuit at the same supplier, so the " +
			"duplicate this test is about does not exist")
	}

	resp = h.post("/circuits/"+first, url.Values{
		"csrf_token":  {h.csrfToken("/circuits/" + first)},
		"cid":         {takenCID},
		"provider_id": {provider},
		"row_version": {h.lookup(`SELECT row_version FROM circuit WHERE id = ?`, first)},
	}, false)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("a circuit was corrected onto another circuit's identifier at the " +
			"same supplier; the two are now indistinguishable down the phone")
	}
	if got := h.lookup(`SELECT COUNT(*) FROM circuit WHERE provider_id = ? AND cid = ?`,
		provider, takenCID); got != "1" {
		t.Errorf("%s circuits at that supplier now carry %q", got, takenCID)
	}
	if !strings.Contains(body(t, resp), "already has a circuit with that identifier") {
		t.Error("the refusal did not say why, so the operator sees a form that " +
			"simply did not save")
	}
}

// TestOnlyAWriterCanCorrectACircuit. A circuit is project-linked, so the gate
// is CanWriteEntity rather than IsAdmin -- but an Observer holds neither.
func TestOnlyAWriterCanCorrectACircuit(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstCircuit(t, h)
	before := h.lookup(`SELECT cid FROM circuit WHERE id = ?`, id)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/circuits/"+id, url.Values{
		"csrf_token":  {viewer.csrfToken("/circuits/" + id)},
		"cid":         {"TAKEN-OVER"},
		"provider_id": {h.lookup(`SELECT provider_id FROM circuit WHERE id = ?`, id)},
		"row_version": {h.lookup(`SELECT row_version FROM circuit WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("an Observer corrected a circuit")
	}
	if got := h.lookup(`SELECT cid FROM circuit WHERE id = ?`, id); got != before {
		t.Errorf("cid went %q -> %q despite the refusal", before, got)
	}
}

// valueOfInput reads the value attribute of the named input, whatever
// whitespace the template happens to put between the two attributes.
func valueOfInput(page, name string) (string, bool) {
	i := strings.Index(page, `name="`+name+`"`)
	if i < 0 {
		return "", false
	}
	return attrAfter(page[i:], `value="`, `"`)
}
