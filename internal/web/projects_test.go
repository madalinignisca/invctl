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
	"regexp"
	"strings"
	"testing"
)

// The Projects UI, through the real router.
//
// The fixture now ships three projects of its own (see seed_projects.go), so
// these tests mostly ASSERT AGAINST THE SEEDED ONES rather than building a
// throwaway estate. That is deliberate: the seeded links are what a demo shows
// and what a reader will believe, so they are worth a guard. Where a test
// genuinely needs a fresh project it uses a `t-` code, which the fixture never
// takes.

// makeProject creates one and returns its id, from the redirect the create
// handler issues.
func makeProject(t *testing.T, h *harness, code, name string) string {
	t.Helper()
	form := url.Values{
		"csrf_token": {h.csrfToken("/projects")},
		"code":       {code}, "name": {name},
	}
	resp := h.post("/projects", form, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating project %s returned %d, want a redirect", code, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	id := strings.TrimPrefix(loc, "/projects/")
	if id == "" || id == loc {
		t.Fatalf("no project id in the redirect %q", loc)
	}
	return id
}

func linkAsset(t *testing.T, h *harness, projectID, assetID, relation string) *http.Response {
	t.Helper()
	return h.post("/projects/"+projectID+"/assets", url.Values{
		"csrf_token": {h.csrfToken("/projects/" + projectID)},
		"asset_id":   {assetID}, "relation": {relation},
	}, false)
}

func TestProjectCreateAndOverview(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// A rack, because the fixture's platform project already owns all three
	// hypervisors and an asset has at most one owner. Containment derives the
	// same way one level up, and it is the more interesting case: what is
	// implied by owning a rack is most of the estate.
	id := makeProject(t, h, "t-estate", "Estate Test")
	resp := linkAsset(t, h, id, h.refs.Assets["rack-a1"], "owns")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("linking returned %d", resp.StatusCode)
	}

	page := body(t, h.get("/projects/"+id, false))
	if !strings.Contains(page, "rack-a1") {
		t.Error("the linked asset is not on the overview")
	}
	// The derived section must exist AND be labelled, since that labelling is
	// the actual requirement -- a guest appearing somewhere on the page is
	// true whether it reads as declared or as derived.
	derived := sectionAfter(page, "What that implies")
	if derived == "" {
		t.Fatal("the overview has no derived section")
	}
	if !strings.Contains(derived, "derived") || !strings.Contains(derived, "nobody declared") {
		t.Error("the derived section does not say it is derived")
	}
	for _, guest := range []string{"hv-01", "vm-app-1", "vm-db-1"} {
		if !strings.Contains(derived, guest) {
			t.Errorf("%s sits inside the owned rack and is not in the derived section", guest)
		}
	}
	// And the guests must NOT appear in the declared assets table.
	declared := sectionBetween(page, "<h2>Assets</h2>", "What that implies")
	if strings.Contains(declared, "vm-app-1") {
		t.Error("a derived guest is listed among the declared assets; a manager reading " +
			"that table would think somebody linked it")
	}
}

// sectionAfter returns the page from a heading to the end of its panel.
func sectionAfter(page, heading string) string {
	i := strings.Index(page, heading)
	if i < 0 {
		return ""
	}
	rest := page[i:]
	if j := strings.Index(rest, "</div>\n  </div>"); j > 0 {
		return rest[:j]
	}
	return rest
}

func sectionBetween(page, from, to string) string {
	i := strings.Index(page, from)
	j := strings.Index(page, to)
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return page[i:j]
}

// TestProjectSecondOwnerIs422NamingTheFirst. The operator picked a valid asset
// and a valid relation; what is wrong is the combination, which is a
// field-level problem they can fix on the form in front of them.
func TestProjectSecondOwnerIs422NamingTheFirst(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// The fixture's platform project already owns hv-01, so the conflict is a
	// real one rather than one this test staged for itself.
	second := makeProject(t, h, "t-second", "Second Claim")

	resp := linkAsset(t, h, second, h.refs.Assets["hv-01"], "owns")
	page := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a second owner returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(page, "Platform &amp; Core Services") {
		t.Error("the error does not name the project that already owns it")
	}

	// `uses` from the same project is accepted -- the asymmetry, through the UI.
	ok := linkAsset(t, h, second, h.refs.Assets["hv-01"], "uses")
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Errorf("a second project could not USE the asset: %d", ok.StatusCode)
	}
}

// TestProjectOverviewNamesWhatItDoesNotOwn reads the fixture's own orders
// project, which owns the storefront services and declares that it uses the
// database and SSO but not the queue.
//
// Three outcomes have to be distinguishable on one page, and each is a
// different conversation: somebody else owns it, NOBODY owns it, and you
// already said you use it.
func TestProjectOverviewNamesWhatItDoesNotOwn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/projects/"+h.refs.Projects["orders"], false))
	finding := sectionAfter(page, "What it depends on that it does not own")
	if finding == "" {
		t.Fatal("the overview has no dependency finding section")
	}

	// External: the queue, and the team to escalate to.
	if !strings.Contains(finding, "rabbitmq") {
		t.Error("the finding does not name the queue this project depends on")
	}
	if !strings.Contains(finding, "Platform &amp; Core Services") {
		t.Error("the finding does not name the project that owns it — which is the " +
			"actionable half of the whole panel")
	}
	// Unowned, and it must read differently: there is nobody to ask.
	if !strings.Contains(finding, "haproxy-edge") || !strings.Contains(finding, "no project owns") {
		t.Error("the edge nobody owns is not reported as unowned")
	}
	// Shared: declared, so counted but not raised as a finding.
	if strings.Contains(finding, "pgsql-core") {
		t.Error("a dependency the project already declared it uses is listed as a finding")
	}
	if !strings.Contains(finding, "already declared it uses") {
		t.Error("the shared dependencies are not accounted for at all")
	}
}

func TestProjectMapDrawsAndToggles(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Projects["platform"]

	svg := mainSVG(t, body(t, h.get("/projects/"+id+"/map", false)))
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}
	if !strings.Contains(svg, "hv-01") {
		t.Error("the owned asset is not on the map")
	}
	// The layer toggle is a server round trip, same as the other two maps.
	physical := mainSVG(t, body(t, h.get("/projects/"+id+"/map?filter=1&layer=physical", true)))
	if strings.Contains(physical, "hv-01-br0") {
		t.Error("the virtual layer is still drawn with only the physical layer selected")
	}
	if !strings.Contains(physical, "hv-01") {
		t.Error("the physical layer went missing along with the others")
	}
}

func TestProjectAccessAndFindability(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.refs.Projects["orders"]

	if !strings.Contains(body(t, h.get("/projects", false)), `href="/projects"`) {
		t.Error("the navigation rail does not carry Projects")
	}
	if !strings.Contains(body(t, h.get("/help", false)), "Project relation") {
		t.Error("the help drawer does not explain the two relations")
	}

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	read := viewer.get("/projects/"+id, false)
	read.Body.Close()
	if read.StatusCode == http.StatusOK {
		// A read-only user may look; they may not link.
		resp := viewer.post("/projects/"+id+"/assets", url.Values{
			"csrf_token": {viewer.csrfToken("/projects/" + id)},
			"asset_id":   {viewer.refs.Assets["hv-01"]}, "relation": {"owns"},
		}, false)
		resp.Body.Close()
		if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusOK {
			t.Errorf("a read-only user linked an asset to a project (%d)", resp.StatusCode)
		}
	}

	anon := newHarness(t).get("/projects", false)
	anon.Body.Close()
	if anon.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous got %d, want a redirect to login", anon.StatusCode)
	}
}

// TestProjectCreateRejectsWithoutCode re-renders the form at 422 rather than
// losing what was typed.
func TestProjectCreateRejectsWithoutCode(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/projects", url.Values{
		"csrf_token": {h.csrfToken("/projects")},
		"code":       {""}, "name": {"No code"},
	}, false)
	page := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a project with no code returned %d, want 422", resp.StatusCode)
	}
	if !regexp.MustCompile(`class="field-error"`).MatchString(page) {
		t.Error("the re-rendered form carries no field error")
	}
	if !strings.Contains(page, "No code") {
		t.Error("the re-rendered form lost what was typed")
	}
}
