package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Vocabulary administration: admin-only, audited, and the thing that makes the
// help panel's descriptions genuinely the operator's.

func TestVocabularyPageListsTermsAndDescriptions(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/vocabularies?table=service_kind", false))
	for _, want := range []string{"Load balancer", "Secret manager", "FORWARD proxy"} {
		if !strings.Contains(page, want) {
			t.Errorf("the vocabulary page is missing %q", want)
		}
	}
}

func TestVocabularyAddAndEditAreAudited(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// The change log page is the audit trail a person actually reads, so
	// assert against that rather than reaching into the table.
	before := strings.Count(body(t, h.get("/changes", false)), "service_kind")

	token := h.csrfToken("/vocabularies")
	form := url.Values{
		"csrf_token": {token},
		"table":      {"service_kind"}, "code": {"vpn"}, "label": {"VPN"},
		"sort_order": {"130"}, "description": {"A tunnel endpoint."},
	}
	resp := h.post("/vocabularies", form, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusOK {
		t.Fatalf("adding a term returned %d", resp.StatusCode)
	}

	// It is offered by the form that consumes it -- the whole point of the
	// vocabulary being data.
	svcForm := body(t, h.get("/services", false))
	if !strings.Contains(svcForm, `value="vpn"`) {
		t.Error("the new kind is not offered on the service form; a term nobody can pick " +
			"is a row, not a vocabulary")
	}
	// And in the help panel.
	if !strings.Contains(body(t, h.get("/help/service_kind", false)), "A tunnel endpoint.") {
		t.Error("the new term's description does not reach the help panel")
	}
	if after := strings.Count(body(t, h.get("/changes", false)), "service_kind"); after <= before {
		t.Errorf("change_log rows went %d -> %d; a vocabulary term is declared state "+
			"and every mutation of it must be recorded", before, after)
	}

	// Editing records an update rather than a second create.
	edit := url.Values{
		"csrf_token": {h.csrfToken("/vocabularies")},
		"table":      {"service_kind"}, "code": {"vpn"}, "label": {"VPN gateway"},
		"sort_order": {"130"}, "description": {"A tunnel endpoint, reworded."},
	}
	h.post("/vocabularies", edit, false).Body.Close()
	if !strings.Contains(body(t, h.get("/help/service_kind", false)), "reworded") {
		t.Error("the edit did not take")
	}
}

func TestVocabularyRejectsWhatItShould(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// A table name is never taken from the request without checking it against
	// the allow-list -- it is interpolated into SQL. `asset` is a real table
	// and not a vocabulary, which is exactly the input that would matter.
	bad := h.get("/vocabularies?table=asset", false)
	bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown table returned %d, want 404", bad.StatusCode)
	}

	missing := url.Values{
		"csrf_token": {h.csrfToken("/vocabularies")},
		"table":      {"service_kind"}, "code": {""}, "label": {"No code"},
	}
	resp := h.post("/vocabularies", missing, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a term with no code returned %d, want 422", resp.StatusCode)
	}
}

func TestVocabularyIsAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	form := url.Values{
		"csrf_token": {h.csrfToken("/vocabularies")},
		"table":      {"service_kind"}, "code": {"sneaky"}, "label": {"Sneaky"},
	}
	resp := h.post("/vocabularies", form, false)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
		t.Errorf("a read-only user added a vocabulary term (%d); these are foreign keys, "+
			"so adding one changes what the estate can express", resp.StatusCode)
	}
}
