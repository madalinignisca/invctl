package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// The help panel, through the real router.

func TestHelpIndexListsBothKindsOfVocabulary(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/help", false))
	// One from each source, and the distinction the panel exists to draw.
	for _, want := range []string{"Service kinds", "Availability policy", "engine"} {
		if !strings.Contains(page, want) {
			t.Errorf("the help index is missing %q", want)
		}
	}
}

func TestHelpAnswersTheQuestionsThatPromptedIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	kinds := body(t, h.get("/help/service_kind", false))
	// The four terms a systems administrator read cold and found ambiguous.
	for _, want := range []string{
		"FORWARD proxy",      // proxy is not the load balancer
		"For a secret store", // auth is not the secret manager
		"storage SERVICE",    // service storage is not the array
		"Load balancer",      // the kind that was missing
		"Secret manager",     // the other one
	} {
		if !strings.Contains(kinds, want) {
			t.Errorf("the service-kind help does not mention %q", want)
		}
	}

	// And the asset side says which `storage` it is, since both exist.
	assets := body(t, h.get("/help/asset_kind", false))
	if !strings.Contains(assets, "Storage HARDWARE") {
		t.Error("the asset-kind help does not distinguish the array from the service")
	}
}

// TestHelpDescriptionsComeFromTheDatabase is the one that proves the split is
// real: edit a description in the database and the panel must follow, because
// these are an estate's own conventions rather than the tool's rules.
func TestHelpDescriptionsComeFromTheDatabase(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	const reworded = "Whatever this shop decides a queue is."
	w := h.store.DB().Writer
	if _, err := w.Exec(w.Rebind(
		`UPDATE service_kind SET description = ? WHERE code = ?`), reworded, "queue"); err != nil {
		t.Fatalf("rewording the term: %v", err)
	}

	page := body(t, h.get("/help/service_kind", false))
	if !strings.Contains(page, reworded) {
		t.Error("the panel does not show the database's wording; the descriptions are " +
			"supposed to be data an operator owns")
	}
}

// TestEngineHelpIsNotEditable: the enums the impact engine acts on are
// explained from Go and say so, because a description you can edit for a
// behaviour you cannot is a lie waiting to happen.
func TestEngineHelpIsNotEditable(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/help/availability", false))
	if !strings.Contains(page, "defined by the engine") {
		t.Error("engine-defined help does not say so")
	}
	if strings.Contains(page, "yours to edit") {
		t.Error("engine-defined help offers editing")
	}
	// The substance: quorum's second loss being worse than its first is the
	// kind of thing a label cannot carry.
	if !strings.Contains(page, "losing two is total") {
		t.Error("the availability help does not explain what quorum actually does")
	}
}

func TestHelpAccessAndUnknownTopic(t *testing.T) {
	h := newHarness(t)

	anon := h.get("/help", false)
	anon.Body.Close()
	if anon.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous got %d, want a redirect to login", anon.StatusCode)
	}

	h.login("viewer", "viewer-password")
	ok := h.get("/help/service_kind", false)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("a read-only user got %d, want 200: help is a read", ok.StatusCode)
	}
	missing := h.get("/help/no-such-topic", false)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown topic returned %d, want 404", missing.StatusCode)
	}
}
