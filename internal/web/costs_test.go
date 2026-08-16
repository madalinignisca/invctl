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

// CORRECTING A LINE, which is a different act from retiring one and adding
// another. A wrong figure is usually a typo, not a change of commercial reality,
// and the two must not look the same afterwards: retire-and-re-add starts a
// fresh validity window and loses the fact that the old number was never true.
func TestCorrectingACostLine(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	costID := firstCostFormID(t, body(t, h.get("/assets/"+id, false)))

	// The table is read-only until a row is asked for by id, so a reader is
	// never handed a screenful of input boxes.
	if strings.Contains(body(t, h.get("/assets/"+id, false)), `<form id="cost-`) {
		t.Error("a cost row is editable without being asked for")
	}
	page := body(t, h.get("/assets/"+id+"?edit="+costID, false))

	// THE FORM MUST SHOW WHAT IS STORED. Every field is submitted on save, so a
	// select that renders with nothing marked `selected` silently posts its
	// first option: correcting an amount would also rewrite the kind, and the
	// operator would have no way to see it happen. The row for the €8,400
	// acquisition must come back as `acquisition`/`once` and not as whatever
	// happens to be first in the vocabulary.
	row := rowContaining(t, page, costID)
	for _, want := range []string{
		`value="acquisition" selected`,
		`value="once" selected`,
		// Who benefits (§5.6). Third select on an ASSET cost row, and it obeys
		// the same rule for the same reason: an unset one would post
		// `universal` over a licence somebody had scoped to two database
		// guests, moving real money and saying nothing.
		`value="universal" selected`,
		`value="8400.00"`,
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the edit row does not show the stored value %s", want)
		}
	}
	// The negative control: nothing ELSE may be preselected in those three
	// selects, or the browser posts the last one and the assertion above is
	// satisfied by a form that is still wrong.
	if n := strings.Count(row, " selected>"); n != 3 {
		t.Errorf("the row preselects %d options, want exactly 3 (kind, period "+
			"and who benefits)", n)
	}

	resp := h.post("/assets/"+id+"/costs/"+costID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"kind":       {"acquisition"}, "period": {"once"},
		"amount": {"9000"}, "valid_from": {"2024-01-01"},
		"note": {"corrected against the invoice"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a cost returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(after, "€9,000.00") {
		t.Error("the corrected amount is not shown")
	}
	if strings.Contains(after, "€8,400.00") {
		t.Error("the old amount is still on the page: a correction added a line instead of amending one")
	}
	if !strings.Contains(after, "corrected against the invoice") {
		t.Error("the note did not survive the correction")
	}

	// It is a mutation of declared state, so it is in the audit trail.
	log := body(t, h.get("/changes", false))
	if !strings.Contains(log, "asset_cost") {
		t.Error("correcting a cost line wrote no change_log entry")
	}
}

// The URL names the owner as well as the line, and the handler must believe the
// line rather than the URL. Without the check an admin could reach any cost row
// in the estate through an asset they happen to be looking at, and the audit
// entry would name the wrong parent.
func TestACostLineCannotBeEditedThroughAnotherAssetsURL(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	owner := h.refs.Assets["hv-01"]
	other := h.refs.Assets["vm-app-1"]
	costID := firstCostFormID(t, body(t, h.get("/assets/"+owner, false)))

	resp := h.post("/assets/"+other+"/costs/"+costID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + other)},
		"kind":       {"operating"}, "period": {"monthly"},
		"amount": {"1"}, "valid_from": {"2024-01-01"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("editing hv-01's cost line through vm-app-1 returned %d, want 404", resp.StatusCode)
	}
	if page := body(t, h.get("/assets/"+owner, false)); !strings.Contains(page, "€8,400.00") {
		t.Error("the cost line was changed through the wrong asset's URL")
	}
}

// The edit row is asked for by URL, so the guard has to be on the permission
// and not merely on whether a link was drawn. A reader who types the query
// parameter gets the same read-only table.
func TestAReadOnlyUserGetsNoEditableCostRow(t *testing.T) {
	// ONE harness, two logins. Two harnesses seed two databases with two sets
	// of UUIDs, so the admin's cost id names nothing in the viewer's estate and
	// the row could not have rendered whatever the permission said. The test
	// passed with the permission check deleted; found by mutating it.
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.refs.Assets["hv-01"]
	costID := firstCostFormID(t, body(t, h.get("/assets/"+id, false)))

	h.logout()
	h.login("viewer", "viewer-password")
	page := body(t, h.get("/assets/"+id+"?edit="+costID, false))
	if strings.Contains(page, `<form id="cost-`) || strings.Contains(page, `name="amount"`) {
		t.Error("a read-only user asking for the edit row by id was given one")
	}
	if !strings.Contains(page, "€8,400.00") {
		t.Error("a read-only user cannot read the cost at all")
	}
}

// A retired line is history: readable, not amendable. Editing one would rewrite
// a figure that was already withdrawn, and the withdrawal is itself a fact.
func TestARetiredCostLineCannotBeEdited(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	costID := firstCostFormID(t, body(t, h.get("/assets/"+id, false)))
	retire := h.post("/assets/"+id+"/costs/"+costID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
	}, false)
	retire.Body.Close()

	if page := body(t, h.get("/assets/"+id+"?edit="+costID, false)); strings.Contains(page, `<form id="cost-`) {
		t.Error("a retired cost line was offered for editing")
	}
	resp := h.post("/assets/"+id+"/costs/"+costID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"kind":       {"acquisition"}, "period": {"once"},
		"amount": {"1"}, "valid_from": {"2024-01-01"},
	}, false)
	resp.Body.Close()
	after := body(t, h.get("/assets/"+id, false))
	if strings.Contains(after, "€1.00") {
		t.Error("a retired cost line was amended through a direct POST")
	}
}

// firstCostFormID pulls a cost line's id out of the Edit link on the cost
// table, SCOPED TO THAT PANEL. The asset page now carries edit links for ports
// and addresses too, all using the same ?edit= parameter, so an unscoped match
// returned an interface id and every assertion below it was about the wrong
// row -- which showed up as "no row for cost X" rather than as a wrong pass.
func firstCostFormID(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `id="costs"`)
	if i < 0 {
		t.Fatal("no cost panel on the page")
	}
	m := regexp.MustCompile(`\?edit=([0-9a-f-]+)`).FindStringSubmatch(page[i:])
	if m == nil {
		t.Fatal("no cost row offers an edit link")
	}
	return m[1]
}

// rowContaining returns the <tr> holding a given cost id, so an assertion about
// one line cannot be satisfied by markup belonging to another.
func rowContaining(t *testing.T, page, costID string) string {
	t.Helper()
	i := strings.Index(page, `form="cost-`+costID+`"`)
	if i < 0 {
		t.Fatalf("no row for cost %s", costID)
	}
	start := strings.LastIndex(page[:i], "<tr")
	end := strings.Index(page[i:], "</tr>")
	if start < 0 || end < 0 {
		t.Fatal("cost row is not inside a table row")
	}
	return page[start : i+end]
}
