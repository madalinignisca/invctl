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

// The tag registry, piece 1 of WP-G4a (docs/tags-design.md): the tag itself
// and its registry, reached over the router rather than called as a Go
// function -- so a handler that is wired to the wrong route, or a template
// that fails to parse, shows up here rather than passing a handler test that
// never asked the router at all.

func TestTheTagRegistryDefinesAndListsATag(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	form := url.Values{
		"csrf_token":  {h.csrfToken("/tags")},
		"code":        {"dr"},
		"label":       {"DR"},
		"description": {"in scope for the annual disaster-recovery exercise"},
	}
	if code := h.post("/tags", form, false).StatusCode; code != http.StatusSeeOther {
		t.Fatalf("creating a tag: got %d", code)
	}

	page := body(t, h.get("/tags", false))
	for _, want := range []string{"dr", "DR", "in scope for the annual disaster-recovery exercise", "admin"} {
		if !strings.Contains(page, want) {
			t.Errorf("the registry must show %q", want)
		}
	}
}

// TestTagRegistryIsReadableByAnyAuthenticatedUser: read() on GET /tags,
// exactly as the custom field registry ships -- consistent with "who may
// create one" in docs/tags-design.md §4a (creation and application are
// write(), the registry itself is not).
func TestTagRegistryIsReadableByAnyAuthenticatedUser(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	if code := h.get("/tags", false).StatusCode; code != http.StatusOK {
		t.Fatalf("a read-only user must be able to see the tag registry: got %d", code)
	}
}

// TestDefiningATagIsAdminOnly: the mutation sits behind RequireWrite, the
// same as CustomFieldCreate.
func TestDefiningATagIsAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	form := url.Values{
		"csrf_token":  {h.csrfToken("/tags")},
		"code":        {"pci"},
		"label":       {"PCI"},
		"description": {"in scope for PCI-DSS"},
	}
	if code := h.post("/tags", form, false).StatusCode; code != http.StatusForbidden {
		t.Fatalf("a read-only user defining a tag: got %d, want 403", code)
	}
}

// TestATagWithNoDescriptionIsRefusedWith422: validation failure re-renders
// the form partial with error state -- 422, never a 200 with the message
// buried in the body.
func TestATagWithNoDescriptionIsRefusedWith422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	form := url.Values{
		"csrf_token": {h.csrfToken("/tags")},
		"code":       {"pci"},
		"label":      {"PCI"},
		// description deliberately absent
	}
	resp := h.post("/tags", form, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", resp.StatusCode)
	}
}

// TestRetiringATagKeepsItInTheRegistry: a retired tag is not deleted -- it
// moves to its own section.
func TestRetiringATagKeepsItInTheRegistry(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	form := url.Values{
		"csrf_token": {h.csrfToken("/tags")}, "code": {"dr"}, "label": {"DR"},
		"description": {"in scope for DR"},
	}
	h.post("/tags", form, false)

	page := body(t, h.get("/tags", false))
	id := tagIDFromRegistry(t, page, "dr")

	if code := h.post("/tags/"+id+"/retire",
		url.Values{"csrf_token": {h.csrfToken("/tags")}}, false).StatusCode; code != http.StatusSeeOther {
		t.Fatalf("retiring: got %d", code)
	}

	after := body(t, h.get("/tags", false))
	if !strings.Contains(after, "dr") {
		t.Fatal("a retired tag must keep displaying, in its own section")
	}
}

// tagIDFromRegistry pulls the id out of a rendered edit link for one code,
// the cheapest way this suite has of learning an id it did not itself mint.
func tagIDFromRegistry(t *testing.T, page, code string) string {
	t.Helper()
	marker := `href="/tags?edit=`
	i := strings.Index(page, marker)
	if i == -1 {
		t.Fatalf("no edit link found in the registry for %q", code)
	}
	rest := page[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j == -1 {
		t.Fatalf("malformed edit link in the registry for %q", code)
	}
	return rest[:j]
}

// TestRetiringATagOverHTMXRedirectsAndRetires is task-10's mechanism check:
// converting the retire control from a native form POST to hx-post changes
// HOW the request reaches the server, not just what the button attribute
// says, so a passing hx-confirm assertion alone would not prove retiring
// still works. This drives POST /tags/{id}/retire exactly the way the
// button now does -- HX-Request set -- and requires both that the tag is
// actually retired in the store and that render.Redirect's HTMX branch
// fires (HX-Redirect header, not a 303 HTMX would not follow the way a
// plain form expects).
func TestRetiringATagOverHTMXRedirectsAndRetires(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	h.post("/tags", url.Values{
		"csrf_token": {h.csrfToken("/tags")}, "code": {"htmx-retire"}, "label": {"HTMX Retire"},
		"description": {"retired over HTMX to prove the mechanism still works"},
	}, false).Body.Close()

	page := body(t, h.get("/tags", false))
	id := tagIDFromRegistry(t, page, "htmx-retire")

	resp := h.post("/tags/"+id+"/retire",
		url.Values{"csrf_token": {h.csrfToken("/tags")}}, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("retiring over HTMX returned %d, want 204 (render.Redirect's HTMX branch)", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got != "/tags" {
		t.Fatalf("HX-Redirect header = %q, want /tags", got)
	}
	if got := h.lookup(`SELECT COUNT(*) FROM tag WHERE id = ? AND retired_at IS NOT NULL`, id); got != "1" {
		t.Fatalf("tag %s was not retired in the store (matching rows = %q)", id, got)
	}
}
