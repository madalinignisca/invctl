package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Teams through the real router.

func TestTeamsShowWhatTheyAreAnswerableFor(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	list := body(t, h.get("/teams", false))
	for _, code := range []string{"platform", "commerce", "observability"} {
		if !strings.Contains(list, code) {
			t.Errorf("the team list is missing %s", code)
		}
	}

	id := h.refs.Teams["platform"]
	page := body(t, h.get("/teams/"+id, false))
	// What it looks after, from both directions: the hypervisors it owns and
	// the services it runs.
	if !strings.Contains(page, "hv-01") {
		t.Error("the team page does not list the assets it looks after")
	}
	if !strings.Contains(page, "vault") {
		t.Error("the team page does not list the services it looks after")
	}
	// A capacity, not just a name.
	if !strings.Contains(page, "Operator") {
		t.Error("the team page does not say in what capacity it looks after them")
	}
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
func TestTeamWritesRequireAdmin(t *testing.T) {
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
