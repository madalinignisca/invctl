package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Costs through the real router, against the seeded fixture.

func TestCostsAppearWhereTheyAreEnteredAndWhereTheyRollUp(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// On the thing the invoice names.
	asset := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	if !strings.Contains(asset, "What it costs") {
		t.Error("the asset detail page has no cost panel")
	}
	if !strings.Contains(asset, "€8,400.00") {
		t.Error("the acquisition price is not shown on the asset that carries it")
	}

	// On a service, which is where a licence lives.
	svc := body(t, h.get("/services/"+h.refs.Services["vault"], false))
	if !strings.Contains(svc, "€14,400.00") {
		t.Error("a service licence is not shown on the service")
	}

	// And rolled up on the project that owns them.
	project := body(t, h.get("/projects/"+h.refs.Projects["platform"], false))
	if !strings.Contains(project, "capital committed") {
		t.Error("the project overview has no cost summary")
	}
	// Capital and run rate are separate figures and must both be present. One
	// number would be the failure mode this whole design avoids.
	for _, label := range []string{"capital committed", "per month", "per year"} {
		if !strings.Contains(project, label) {
			t.Errorf("the summary is missing the %q figure", label)
		}
	}
	if !strings.Contains(project, "nobody typed these totals") {
		t.Error("the summary does not say it is derived")
	}
}

// TestTheSubsidyIsVisible. The estate's structural finding: the platform team
// pays for the hardware the storefront runs on, so orders looks nearly free and
// the page has to say why rather than letting a reader conclude it.
func TestTheSubsidyIsVisible(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	orders := body(t, h.get("/projects/"+h.refs.Projects["orders"], false))

	// Its own run rate is small and includes the SaaS attached to no box.
	if !strings.Contains(orders, "attached to the\n        project itself") &&
		!strings.Contains(orders, "attached to the project itself") {
		t.Error("the project's own direct costs are not called out")
	}
	// And the floor caveat, because most of the footprint has no price.
	if !strings.Contains(orders, "which makes this a floor, not a budget") {
		t.Error("the summary does not admit how much of the footprint is unpriced")
	}

	platform := body(t, h.get("/projects/"+h.refs.Projects["platform"], false))
	// platform owns the hypervisors, so vm-app-1 sits inside its footprint
	// while orders owns it outright: spend that is on its estate and not its
	// own, named with who carries it.
	if !strings.Contains(platform, "not counted above") {
		t.Error("platform's page does not separate spend another project owns")
	}
	if !strings.Contains(platform, "Orders Platform") {
		t.Error("the subsidy line does not name the project that carries it")
	}
}

func TestAddingAndRemovingACostLine(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["vm-app-1"]
	resp := h.post("/assets/"+id+"/costs", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"kind":       {"operating"}, "period": {"monthly"},
		"amount": {"1 234,50"}, "note": {"typed the way a person types"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding a cost returned %d, want a redirect", resp.StatusCode)
	}

	page := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(page, "€1,234.50") {
		t.Error("an amount typed with a space and a comma was not accepted as 1234.50")
	}

	// A bad amount is refused with a message rather than silently stored as
	// something else. Both separators at once is genuinely ambiguous and differs
	// by a factor of a thousand.
	bad := h.post("/assets/"+id+"/costs", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"kind":       {"operating"}, "period": {"monthly"}, "amount": {"1.200,50"},
	}, false)
	bad.Body.Close()
	after := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(after, "not accepted") {
		t.Error("an ambiguous amount was accepted, or the operator was not told")
	}
}

// A read-only user sees the numbers and cannot change them. That is the whole
// authorization model today, stated as a test so that changing it is deliberate.
func TestCostsAreReadableByEveryoneAndWritableByAdminsOnly(t *testing.T) {
	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")

	id := viewer.refs.Assets["hv-01"]
	page := body(t, viewer.get("/assets/"+id, false))
	if !strings.Contains(page, "€8,400.00") {
		t.Error("a read-only user cannot see costs; today they should")
	}
	if strings.Contains(page, "Add cost") {
		t.Error("a read-only user was shown the add-cost form")
	}

	resp := viewer.post("/assets/"+id+"/costs", url.Values{
		"csrf_token": {viewer.csrfToken("/assets/" + id)},
		"kind":       {"operating"}, "period": {"monthly"}, "amount": {"10"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusOK {
		t.Errorf("a read-only user added a cost line (%d)", resp.StatusCode)
	}
}
