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

// A dependency could not be corrected. UpdateDependency was the most complete
// of the six unreachable Update methods -- provenance check, two subject
// authorizations, serializable transaction, set-table replacement folded into
// the audited value -- and nothing called it.
//
// NATURE IS A WRONG ANSWER, NOT A WRONG LABEL. It is what the impact engine
// reasons over: `hard` takes the consumer down with the provider, `soft`
// degrades it, `optional` does neither. An edge entered hard when it is
// optional turns a routine restart into a predicted outage; entered optional
// when it is hard, the engine says a service survives something that kills it.
// Retire-and-redraw was the only fix, and it loses the verification somebody
// attested to along with the record of what the edge used to claim.

// firstWritableDep returns a dependency and its consumer service.
func firstWritableDep(t *testing.T, h *harness) (depID, consumerID string) {
	t.Helper()
	depID = h.lookup(`SELECT id FROM dependency WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
	consumerID = h.lookup(`SELECT consumer_service_id FROM dependency WHERE id = ?`, depID)
	return depID, consumerID
}

// TestADependencysNatureCanBeCorrected is the answer-changing correction.
func TestADependencysNatureCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dep, consumer := firstWritableDep(t, h)
	before := h.lookup(`SELECT nature FROM dependency WHERE id = ?`, dep)
	want := "optional"
	if before == want {
		want = "hard"
	}

	resp := h.post("/dependencies/"+dep, url.Values{
		"csrf_token":   {h.csrfToken("/services/" + consumer)},
		"nature":       {want},
		"failure_mode": {h.lookup(`SELECT failure_mode FROM dependency WHERE id = ?`, dep)},
		"row_version":  {h.lookup(`SELECT row_version FROM dependency WHERE id = ?`, dep)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a dependency's nature returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT nature FROM dependency WHERE id = ?`, dep); got != want {
		t.Errorf("nature = %q after the correction, want %q -- this is what the "+
			"impact engine reasons over", got, want)
	}
}

// TestCorrectingADependencyKeepsItsVerification.
//
// The store carries verified_by, verified_at and lifecycle from the STORED row
// whatever the caller sends -- so a correction must not cost the attestation
// somebody put their name to. That is the concrete thing retire-and-redraw
// destroyed, and the reason a correction path is worth building at all.
func TestCorrectingADependencyKeepsItsVerification(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dep, consumer := firstWritableDep(t, h)
	resp := h.post("/dependencies/"+dep+"/verify", url.Values{
		"csrf_token": {h.csrfToken("/services/" + consumer)},
	}, false)
	resp.Body.Close()
	verifier := h.lookup(`SELECT COALESCE(verified_by, '') FROM dependency WHERE id = ?`, dep)
	if verifier == "" {
		t.Fatal("could not verify the dependency first, so this test would prove nothing")
	}

	resp = h.post("/dependencies/"+dep, url.Values{
		"csrf_token":   {h.csrfToken("/services/" + consumer)},
		"nature":       {"soft"},
		"failure_mode": {"corrected failure mode"},
		"row_version":  {h.lookup(`SELECT row_version FROM dependency WHERE id = ?`, dep)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT COALESCE(verified_by, '') FROM dependency WHERE id = ?`, dep); got != verifier {
		t.Errorf("verified_by went %q -> %q when the edge was corrected. The whole "+
			"point of correcting rather than redrawing is that the attestation "+
			"survives", verifier, got)
	}
	if got := h.lookup(`SELECT lifecycle FROM dependency WHERE id = ?`, dep); got != "active" {
		t.Errorf("lifecycle = %q after a correction, want active", got)
	}
}

// TestADependencyCannotBeRepointedThroughTheCorrectionForm.
//
// Re-pointing an edge is declaring a different edge, and the store guards it
// with two separate subject authorizations because moving one is a seizure
// risk rather than a typo. The handler carries all three subject columns from
// the stored row, so a submitted consumer or provider is not merely refused --
// it is never read. This drives the route with a forged provider and asserts
// the edge did not move.
func TestADependencyCannotBeRepointedThroughTheCorrectionForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dep, consumer := firstWritableDep(t, h)
	beforeEndpoint := h.lookup(`SELECT COALESCE(provider_endpoint_id, '') FROM dependency WHERE id = ?`, dep)
	beforeConsumer := h.lookup(`SELECT consumer_service_id FROM dependency WHERE id = ?`, dep)

	elsewhere := h.lookup(`SELECT id FROM endpoint WHERE id <> ? ORDER BY id LIMIT 1`, beforeEndpoint)
	otherService := h.lookup(`SELECT id FROM service WHERE id <> ? ORDER BY id LIMIT 1`, beforeConsumer)

	resp := h.post("/dependencies/"+dep, url.Values{
		"csrf_token":           {h.csrfToken("/services/" + consumer)},
		"nature":               {"soft"},
		"failure_mode":         {"still here"},
		"provider_endpoint_id": {elsewhere},
		"consumer_service_id":  {otherService},
		"row_version":          {h.lookup(`SELECT row_version FROM dependency WHERE id = ?`, dep)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT COALESCE(provider_endpoint_id, '') FROM dependency WHERE id = ?`, dep); got != beforeEndpoint {
		t.Errorf("provider_endpoint_id went %q -> %q; the correction form re-pointed "+
			"the edge", beforeEndpoint, got)
	}
	if got := h.lookup(`SELECT consumer_service_id FROM dependency WHERE id = ?`, dep); got != beforeConsumer {
		t.Errorf("consumer_service_id went %q -> %q; the correction form moved the "+
			"edge onto another service", beforeConsumer, got)
	}
}

// TestTheCorrectionFormCannotLaunderProvenance.
//
// docs/AUDIT.md rule 7: only a user actor may write source = 'declared', and
// flipping an existing discovered edge is the cheaper of the two attacks
// because the edge already looks established. The handler carries `source`
// from the stored row, so a submitted one is never read -- this proves it by
// sending one.
func TestTheCorrectionFormCannotLaunderProvenance(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dep, consumer := firstWritableDep(t, h)
	h.exec(`UPDATE dependency SET source = 'discovered_netstat' WHERE id = ?`, dep)

	resp := h.post("/dependencies/"+dep, url.Values{
		"csrf_token":   {h.csrfToken("/services/" + consumer)},
		"nature":       {"soft"},
		"failure_mode": {"unchanged"},
		"source":       {"declared"},
		"row_version":  {h.lookup(`SELECT row_version FROM dependency WHERE id = ?`, dep)},
	}, false)
	defer resp.Body.Close()

	if got := h.lookup(`SELECT source FROM dependency WHERE id = ?`, dep); got != "discovered_netstat" {
		t.Errorf("source went discovered_netstat -> %q through the correction form. A "+
			"discovered edge relabelled as declared reads as something a person "+
			"asserted, which is what rule 7 exists to stop", got)
	}
}

// TestCorrectingADependencyReplacesItsDataClassesWholesale, in both directions.
//
// dependency_data_class is a set table replaced wholesale and folded into the
// dependency's audited value. An unticked checkbox submits NOTHING, so a form
// that fell back to the stored set when none arrived would make unticking the
// last class impossible -- silently, which is the failure mode set tables have
// produced three times in this codebase already.
func TestCorrectingADependencyReplacesItsDataClassesWholesale(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dep, consumer := firstWritableDep(t, h)
	class := h.lookup(`SELECT code FROM data_class ORDER BY sort_order, code LIMIT 1`)

	post := func(classes ...string) {
		t.Helper()
		form := url.Values{
			"csrf_token":   {h.csrfToken("/services/" + consumer)},
			"nature":       {"soft"},
			"failure_mode": {"unchanged"},
			"row_version":  {h.lookup(`SELECT row_version FROM dependency WHERE id = ?`, dep)},
		}
		for _, c := range classes {
			form.Add("data_class", c)
		}
		resp := h.post("/dependencies/"+dep, form, false)
		resp.Body.Close()
	}

	post(class)
	if got := h.lookup(`SELECT COUNT(*) FROM dependency_data_class WHERE dependency_id = ?`, dep); got != "1" {
		t.Fatalf("after ticking one class the edge carries %s, want 1", got)
	}

	// Every box unticked. Nothing arrives, and the set must empty.
	post()
	if got := h.lookup(`SELECT COUNT(*) FROM dependency_data_class WHERE dependency_id = ?`, dep); got != "0" {
		t.Errorf("after unticking every class the edge still carries %s. A set table "+
			"is replaced wholesale, so an absent field means empty, never "+
			"'fall back to what is stored'", got)
	}
}

// TestCorrectingOnlyTheDataClassesIsStillAudited.
//
// The set table is the trap CLAUDE.md names explicitly: three separate times a
// set replacement produced no diff on the parent struct and therefore no audit
// entry at all. UpdateDependency reads the classes BEFORE the write so a change
// to them alone still diffs -- this is the test that says so from outside.
func TestCorrectingOnlyTheDataClassesIsStillAudited(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dep, consumer := firstWritableDep(t, h)
	nature := h.lookup(`SELECT nature FROM dependency WHERE id = ?`, dep)
	mode := h.lookup(`SELECT failure_mode FROM dependency WHERE id = ?`, dep)
	class := h.lookup(`SELECT code FROM data_class ORDER BY sort_order, code LIMIT 1`)
	before := h.lookup(`SELECT COUNT(*) FROM change_log WHERE entity_id = ?`, dep)

	// Nothing changes but the set.
	form := url.Values{
		"csrf_token":   {h.csrfToken("/services/" + consumer)},
		"nature":       {nature},
		"failure_mode": {mode},
		"data_class":   {class},
		"row_version":  {h.lookup(`SELECT row_version FROM dependency WHERE id = ?`, dep)},
	}
	resp := h.post("/dependencies/"+dep, form, false)
	resp.Body.Close()

	if h.lookup(`SELECT COUNT(*) FROM change_log WHERE entity_id = ?`, dep) == before {
		t.Errorf("changing only the data classes wrote no change_log row (still %s). "+
			"A set replacement that produces no diff on the parent is how three "+
			"earlier audit gaps happened, twice for the rows that decide audit "+
			"scope", before)
	}
}

// TestOnlyAWriterCanCorrectADependency. The write rule is two-ended -- consumer
// AND the provider's owning service -- so an Observer holding neither is
// refused, and the row offers no Edit link.
func TestOnlyAWriterCanCorrectADependency(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dep, consumer := firstWritableDep(t, h)
	before := h.lookup(`SELECT nature FROM dependency WHERE id = ?`, dep)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/dependencies/"+dep, url.Values{
		"csrf_token":   {viewer.csrfToken("/services/" + consumer)},
		"nature":       {"optional"},
		"failure_mode": {"taken over"},
		"row_version":  {h.lookup(`SELECT row_version FROM dependency WHERE id = ?`, dep)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("an Observer corrected a dependency")
	}
	if got := h.lookup(`SELECT nature FROM dependency WHERE id = ?`, dep); got != before {
		t.Errorf("nature went %q -> %q despite the refusal", before, got)
	}
	if strings.Contains(body(t, viewer.get("/services/"+consumer+"?dep="+dep, false)), "dep-f-"+dep) {
		t.Error("the correction form rendered for an Observer, offering a control " +
			"that can only be refused")
	}
}
