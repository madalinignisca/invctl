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
	"regexp"
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

// TestStaticAssetsAreFingerprinted.
//
// The static handler sets a four-hour Cache-Control, and a CDN in front of it
// honours that literally: measured on the public demo, Cloudflare served a
// four-hour-old app.js from its edge while the origin already had the fix. A
// correct change looked broken, and would have kept looking broken for hours.
//
// Fingerprinting is what makes a deploy visible. The URL must carry a version
// that changes with the file's content -- so this asserts the query is there
// and that it is the same on two renders (a per-request random value would
// bust the cache on every page load, which is the opposite failure).
func TestStaticAssetsAreFingerprinted(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/", false))
	re := regexp.MustCompile(`/static/(app\.js|app\.css|alpine\.min\.js|htmx\.min\.js)\?v=([0-9a-f]{6,})`)
	found := re.FindAllStringSubmatch(page, -1)
	if len(found) < 4 {
		t.Fatalf("only %d of the four static assets are fingerprinted; an unversioned "+
			"asset is one a CDN will keep serving after a deploy", len(found))
	}

	// The login page has its own standalone layout rather than using base.html,
	// and it was missed the first time precisely because this test only looked
	// at an authenticated page. Any page carrying a static asset must version it.
	login := body(t, h.get("/login", false))
	if strings.Contains(login, `"/static/app.css"`) {
		t.Error("the login page references an unversioned stylesheet; it has its own " +
			"layout, so fixing base.html did not fix it")
	}

	second := body(t, h.get("/", false))
	for _, m := range found {
		if !strings.Contains(second, m[0]) {
			t.Errorf("%s changed between two renders; the version must follow the file's "+
				"content, not the request, or every page load misses the cache", m[1])
		}
	}
}
