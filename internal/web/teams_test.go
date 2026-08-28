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
	"strconv"
	"strings"
	"testing"
)

// Teams through the real router.

// TestTeamsShowWhatTheyAreAnswerableFor.
//
// EVERY POSITIVE HAS A NEGATIVE, and that is not decoration. The first version
// of this test asserted only that hv-01 and vault appeared on the platform
// team's page — and it passed with BOTH TeamID filters deleted from the store,
// because an unfiltered list contains them too. It proved the page rendered and
// nothing whatsoever about filtering. A review caught it; a mutation confirmed
// it. The absences below are the assertions that matter.
//
// The pairs run in both directions, so a filter that hardcoded one team, or one
// that filtered assets but not services, is caught either way.
func TestTeamsShowWhatTheyAreAnswerableFor(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	list := body(t, h.get("/teams", false))
	for _, code := range []string{"platform", "commerce", "network", "observability"} {
		if !strings.Contains(list, code) {
			t.Errorf("the team list is missing %s", code)
		}
	}

	platform := body(t, h.get("/teams/"+h.refs.Teams["platform"], false))
	if !strings.Contains(platform, "hv-01") {
		t.Error("the platform page does not list the hypervisor it looks after")
	}
	if !strings.Contains(platform, "vault") {
		t.Error("the platform page does not list the service it looks after")
	}
	// A capacity, not just a name.
	if !strings.Contains(platform, "Operator") {
		t.Error("the team page does not say in what capacity it looks after them")
	}
	// The negatives: the network team's switch and the commerce team's API.
	if strings.Contains(platform, "sw-core-1") {
		t.Error("the network team's switch is on the platform page — assets are not filtered")
	}
	if strings.Contains(platform, "orders-api") {
		t.Error("the commerce team's service is on the platform page — services are not filtered")
	}
	// And a service nobody looks after belongs on no team page at all.
	if strings.Contains(platform, "haproxy-edge") {
		t.Error("a service with no team appears under one")
	}

	// The other direction.
	network := body(t, h.get("/teams/"+h.refs.Teams["network"], false))
	if !strings.Contains(network, "sw-core-1") {
		t.Error("the network team is missing the switch it looks after")
	}
	if strings.Contains(network, "hv-01") {
		t.Error("the platform team's hypervisor is on the network page")
	}
}

// TestTeamCountsAgreeWithWhatTheTeamPageLists.
//
// The three correlated subqueries in teamSelect had no direct test: only
// AssetCount was ever read, and only incidentally. A count that double-counts,
// forgets to exclude retired rows, or references the wrong column would have
// gone unnoticed — and the number is the whole reason a row is worth clicking.
//
// Asserted against the DETAIL PAGE rather than against figures copied from the
// fixture, so the two can never drift and an unrelated seed change cannot make
// this fail spuriously. The count and the list are two paths to one answer; if
// they disagree, one of them is wrong.
func TestTeamCountsAgreeWithWhatTheTeamPageLists(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	list := body(t, h.get("/teams", false))

	for _, code := range []string{"platform", "network", "observability", "commerce"} {
		t.Run(code, func(t *testing.T) {
			counts := teamCounts(t, list, code)
			page := body(t, h.get("/teams/"+h.refs.Teams[code], false))

			for i, section := range []string{"<h2>Assets</h2>", "<h2>Services</h2>",
				"<h2>Projects</h2>", "<h2>Certificates</h2>"} {
				if got := rowsInSection(page, section); got != counts[i] {
					t.Errorf("the list says %d for %s, the page lists %d",
						counts[i], section, got)
				}
			}
		})
	}

	// The control: at least one team must have a non-zero count of each kind,
	// or every assertion above is satisfied by a count query that returns zero.
	counts := teamCounts(t, list, "platform")
	for i, kind := range []string{"assets", "services", "projects", "certificates"} {
		if counts[i] == 0 {
			t.Errorf("platform reports zero %s; a count stuck at zero would pass every "+
				"comparison above", kind)
		}
	}
}

// teamCounts reads the numeric cells from a team's row in the list, in column
// order: assets, services, projects, certificates.
func teamCounts(t *testing.T, page, code string) [4]int {
	t.Helper()
	i := strings.Index(page, ">"+code+"</a>")
	if i < 0 {
		t.Fatalf("no row for %s", code)
	}
	end := strings.Index(page[i:], "</tr>")
	if end < 0 {
		t.Fatalf("could not isolate the row for %s", code)
	}
	nums := regexp.MustCompile(`<td class="num">(\d+)</td>`).FindAllStringSubmatch(page[i:i+end], -1)
	if len(nums) != 4 {
		t.Fatalf("expected four counts in the %s row, found %d", code, len(nums))
	}
	var out [4]int
	for k, m := range nums {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unreadable count %q: %v", m[1], err)
		}
		out[k] = n
	}
	return out
}

// rowsInSection counts the table rows under a heading on a team detail page.
//
// Bounded at the NEXT heading before anything else. An empty section renders an
// "empty" block rather than a table, so a search that only stopped at </table>
// ran on into the following panel and counted its rows -- which is how the first
// version of this reported two assets for a team that has none.
func rowsInSection(page, heading string) int {
	i := strings.Index(page, heading)
	if i < 0 {
		return 0
	}
	rest := page[i+len(heading):]
	if next := strings.Index(rest, "<h2>"); next >= 0 {
		rest = rest[:next]
	}
	body := strings.Index(rest, "<tbody>")
	if body < 0 {
		return 0 // an empty section: no table at all
	}
	return strings.Count(rest[body:], "<tr>")
}

// The rule the schema cannot enforce has to be visible where somebody is about
// to type. If the hint ever disappears, the only thing keeping personal data out
// of a database that is kept forever has disappeared with it.
func TestTheContactFieldWarnsAgainstNamingAPerson(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/teams", false))
	if !strings.Contains(page, "never a person") {
		t.Error("the contact field does not warn against naming an individual")
	}

	detail := body(t, h.get("/teams/"+h.refs.Teams["platform"], false))
	if !strings.Contains(detail, "never a person") {
		t.Error("the edit form drops the warning the create form carries")
	}
}

// Assets and services say who looks after them, and say so plainly when nobody
// does — "nobody recorded" rather than a blank cell that reads like a bug.
func TestAssetsAndServicesShowWhoLooksAfterThem(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	asset := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	if !strings.Contains(asset, "Looked after by") {
		t.Error("the asset detail page does not show a team")
	}
	if !strings.Contains(asset, "Platform") {
		t.Error("the asset's team is not named")
	}

	svc := body(t, h.get("/services/"+h.refs.Services["vault"], false))
	if !strings.Contains(svc, "Looked after by") {
		t.Error("the service detail page does not show a team")
	}

	// haproxy-edge has no team in the fixture, and the page must say so.
	edge := body(t, h.get("/services/"+h.refs.Services["haproxy-edge"], false))
	if !strings.Contains(edge, "nobody recorded") {
		t.Error("a service with no team renders as blank rather than as unrecorded")
	}
}

func TestCreatingATeamAndTheValidationPath(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/teams", url.Values{
		"csrf_token": {h.csrfToken("/teams")},
		"code":       {"security"}, "name": {"Security Engineering"},
		"contact_ref": {"secops@example.com"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating a team returned %d, want a redirect", resp.StatusCode)
	}

	// A missing name re-renders the form at 422 rather than losing what was
	// typed -- the rule a review found broken on the vocabulary screen.
	bad := h.post("/teams", url.Values{
		"csrf_token": {h.csrfToken("/teams")},
		"code":       {"netsec"}, "name": {""},
	}, false)
	page := body(t, bad)
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a team with no name returned %d, want 422", bad.StatusCode)
	}
	if !strings.Contains(page, `class="field-error"`) {
		t.Error("the re-rendered form carries no field error")
	}
	if !strings.Contains(page, "netsec") {
		t.Error("the re-rendered form lost the code that was typed")
	}
}

// Read-only users see who looks after what; only admins can change it.
func TestTeamWritesRefuseAReadOnlyUser(t *testing.T) {
	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")

	if resp := viewer.get("/teams", false); resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Errorf("a read-only user got %d on /teams", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	resp := viewer.post("/teams", url.Values{
		"csrf_token": {viewer.csrfToken("/teams")},
		"code":       {"rogue"}, "name": {"Rogue"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusOK {
		t.Errorf("a read-only user created a team (%d)", resp.StatusCode)
	}
}

// TestAnAbsentPickerDoesNotClearTheTeam.
//
// From a security review. responsibilityOptions degrades to EMPTY pickers when
// its store read fails, and the update handlers used to assign team_id and
// manager_role unconditionally from the form — so a form rendered without those
// fields turned a save of some unrelated field into "this asset no longer has a
// team", written to change_log under the name of whoever pressed the button.
//
// The post below omits both fields entirely, which is exactly what a degraded
// form submits.
func TestAnAbsentPickerDoesNotClearTheTeam(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	before := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(before, "Platform") {
		t.Fatal("the fixture asset has no team; this test would prove nothing")
	}

	resp := h.post("/assets/"+id, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"name":       {"hv-01"}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		// team_id and manager_role deliberately absent.
	}, false)
	resp.Body.Close()

	after := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(after, "Platform") {
		t.Error("a form that omitted the team picker silently cleared the team")
	}

	// The control: a form that DOES carry the field, blank, is an operator
	// choosing the empty option and must still clear it.
	cleared := h.post("/assets/"+id, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"name":       {"hv-01"}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		"team_id": {""}, "manager_role": {""},
	}, false)
	cleared.Body.Close()

	final := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(final, "nobody recorded") {
		t.Error("an explicitly blank picker did not clear the team; clearing must stay possible")
	}
}

// TestTheTeamPickersAreActuallyPopulated.
//
// From a database review, and it is the test this feature most needed. The list
// pages build their form context with an explicit dict, and both omitted Teams
// and Roles — html/template resolves a missing key to the zero value without
// erroring, so {{range .Teams}} iterated nothing and every picker rendered with
// only its "—" placeholder.
//
// The effect was that no human could set a team on an asset or a service at
// all: only the seeder wrote those columns. Every other test passed, because
// they all read seeded data or the 422 path, which passes a real struct.
//
// Counting the options is the assertion. "The page contains a team code"
// would also pass on a page that merely lists teams somewhere else.
func TestTheTeamPickersAreActuallyPopulated(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	for _, page := range []string{"/assets", "/services", "/projects"} {
		t.Run(page, func(t *testing.T) {
			html := body(t, h.get(page, false))
			if got := optionCount(t, html, "team_id"); got < 2 {
				t.Errorf("the team picker on %s has %d options — only the placeholder, "+
					"so nobody can assign a team here", page, got)
			}
		})
	}

	// The role picker exists only where a role can be set.
	for _, page := range []string{"/assets", "/services"} {
		t.Run(page+" role", func(t *testing.T) {
			html := body(t, h.get(page, false))
			if got := optionCount(t, html, "manager_role"); got < 2 {
				t.Errorf("the role picker on %s has %d options", page, got)
			}
		})
	}
}

// optionCount counts the <option> elements in the named select.
func optionCount(t *testing.T, page, name string) int {
	t.Helper()
	i := strings.Index(page, `name="`+name+`"`)
	if i < 0 {
		t.Fatalf("no select named %q on the page", name)
	}
	rest := page[i:]
	end := strings.Index(rest, "</select>")
	if end < 0 {
		t.Fatalf("unterminated select %q", name)
	}
	return strings.Count(rest[:end], "<option")
}
