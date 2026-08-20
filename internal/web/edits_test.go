// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Correcting what a thing IS, for the three resources whose store methods have
// existed since they were written and had no route to reach them.
//
// The shared rule, and the reason each test asserts an absence as well as a
// presence: a form here may change what something is, never what it is attached
// to. Attachment moves the graph -- closure, impact and reachability all read
// it -- and re-pointing has its own flows with their own warnings.

func TestCorrectingAnEndpoint(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["rabbitmq"]
	endpointID := h.refs.Endpoints["rabbitmq/amqp"]

	page := body(t, h.get("/services/"+serviceID+"?edit="+endpointID, false))
	if !strings.Contains(page, `value="amqp"`) {
		t.Error("the edit form does not show the endpoint's stored name")
	}
	// THE ATTACHMENT IS NOT ON OFFER. A service picker here would let a Save
	// move every dependency that resolves through this socket.
	if strings.Contains(page, `name="service_id"`) {
		t.Error("the endpoint edit form offers to move the endpoint to another service")
	}

	resp := h.post("/endpoints/"+endpointID, url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
		"name":       {"amqps"}, "l4_proto": {"tcp"}, "port": {"5671"},
		"bind_scope": {"host"}, "tls_mode": {"tls"}, "exposure": {"environment"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting an endpoint returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/services/"+serviceID, false))
	for _, want := range []string{"amqps", "5671", "tls"} {
		if !strings.Contains(after, want) {
			t.Errorf("the corrected endpoint does not show %s", want)
		}
	}
	if !strings.Contains(body(t, h.get("/changes", false)), "endpoint") {
		t.Error("correcting an endpoint wrote no change_log entry")
	}
}

// The endpoint keeps what the form does not mention. A field absent from a form
// must read as "not stated", never as "cleared" -- the same rule the asset and
// service update handlers follow with submittedString.
//
// ASSERTED THROUGH THE AUDIT DIFF, because no page renders l7_proto or
// certificate_id and a test can only see what something renders. The diff names
// exactly the fields that moved, which makes it the one surface where "this did
// not change" is directly observable. The first version compared the address
// column instead; no seeded endpoint has a bound address, so it compared "" to
// "" and passed with the handler clearing the field.
func TestCorrectingAnEndpointKeepsWhatTheFormDoesNotAsk(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["orders-web"]
	endpointID := firstEndpointEditID(t, body(t, h.get("/services/"+serviceID, false)))

	resp := h.post("/endpoints/"+endpointID, url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
		"name":       {"renamed"}, "l4_proto": {"tcp"}, "port": {"8443"},
		"bind_scope": {"host"}, "tls_mode": {"tls"}, "exposure": {"external"},
	}, false)
	resp.Body.Close()
	if !strings.Contains(body(t, h.get("/services/"+serviceID, false)), "renamed") {
		t.Fatal("the rename did not take")
	}

	diff := newestChangeDiff(t, body(t, h.get("/changes", false)))
	if !strings.Contains(diff, "name") {
		t.Fatalf("the newest audit entry is not the rename: %s", diff)
	}
	for _, untouched := range []string{"l7_proto", "certificate_id", "ip_address_id", "service_id"} {
		if strings.Contains(diff, untouched) {
			t.Errorf("correcting the name also changed %s, which no field asked about: %s", untouched, diff)
		}
	}
}

func TestCorrectingAPlacement(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["orders-api"]
	page := body(t, h.get("/services/"+serviceID, false))
	instanceID := firstInstanceEditID(t, page)

	// Scoped to the row being edited. The page also carries the form for
	// PLACING a service, which offers a host picker precisely because choosing
	// the host is what placing one is.
	edit := editingRow(t, body(t, h.get("/services/"+serviceID+"?edit="+instanceID, false)))
	if !strings.Contains(edit, `name="desired_state"`) {
		t.Fatal("the placement row did not open for editing")
	}
	// The host is the attachment: moving a service to another box is a
	// migration, and both placements are facts worth keeping.
	if strings.Contains(edit, `name="host_asset_id"`) {
		t.Error("the placement editor offers to move the service to another host")
	}
	// Provenance is never read from a request. A form that posts `source` lets
	// the request choose its own authority.
	if strings.Contains(edit, `name="source"`) {
		t.Error("the placement editor offers to set provenance")
	}

	resp := h.post("/instances/"+instanceID, url.Values{
		"csrf_token":   {h.csrfToken("/services/" + serviceID)},
		"runtime_type": {"systemd"}, "role": {"canary"}, "shard": {""},
		"ordinal": {"7"}, "desired_state": {"stopped"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a placement returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/services/"+serviceID, false))
	if !strings.Contains(after, "canary") {
		t.Error("the placement's corrected role is not shown")
	}
	if !strings.Contains(after, "stopped") {
		t.Error("the placement's corrected desired state is not shown")
	}
}

// Desired state is INTENT and lifecycle is EXISTENCE. Correcting the first must
// never touch the second: a placement asked to stop is still deployed, and a
// page that showed it as withdrawn would send somebody to the wrong host.
//
// What actually holds this today is UpdateInstance's column list, which does
// not mention lifecycle -- a handler setting it is a no-op at the database.
// That is worth pinning precisely BECAUSE it is invisible: adding one column to
// that statement, for some unrelated reason, would hand every caller the
// ability to withdraw a placement by describing it.
func TestSettingAPlacementToStoppedDoesNotWithdrawIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["orders-api"]
	instanceID := firstInstanceEditID(t, body(t, h.get("/services/"+serviceID, false)))

	resp := h.post("/instances/"+instanceID, url.Values{
		"csrf_token":   {h.csrfToken("/services/" + serviceID)},
		"runtime_type": {"systemd"}, "ordinal": {"0"}, "desired_state": {"stopped"},
	}, false)
	resp.Body.Close()

	after := body(t, h.get("/services/"+serviceID, false))
	if strings.Contains(after, "withdrawn") {
		t.Error("a placement asked to stop was recorded as withdrawn from the estate")
	}
	if !strings.Contains(after, `href="/services/`+serviceID+`?edit=`+instanceID) {
		t.Error("the placement is no longer editable, so it is no longer live")
	}
}

func TestCorrectingAnEnvironment(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := h.refs.Environments["prod"]
	page := body(t, h.get("/environments?edit="+envID, false))
	if !strings.Contains(page, `value="prod"`) {
		t.Error("the environment editor does not show the stored code")
	}
	// THE PICKER MUST BE FED. A select whose options come from a page field
	// that one render path leaves nil renders empty, html/template says nothing
	// about it, and the browser then posts a blank -- so the first save wipes
	// the field. This has already happened once in this codebase, to the team
	// picker on two list pages, where it meant nobody could assign a team at
	// all. Asserted on the editor's own row rather than the page, because the
	// add form below it has the same options and would answer for it.
	row := editingRow(t, page)
	if !strings.Contains(row, `<option value="production" selected>`) {
		t.Errorf("the role picker does not offer the stored role: %s", row)
	}

	resp := h.post("/environments/"+envID, url.Values{
		"csrf_token": {h.csrfToken("/environments")},
		"code":       {"production"}, "name": {"Production estate"},
		"role": {"production"}, "criticality": {"1"}, "in_scope": {"true"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting an environment returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/environments", false))
	if !strings.Contains(after, "Production estate") {
		t.Error("the environment's new name is not shown")
	}
	// The assets in it moved with it, because they were never attached by code.
	assets := body(t, h.get("/assets", false))
	if !strings.Contains(assets, "production") {
		t.Error("assets no longer resolve their environment after the code changed")
	}
}

// An update runs the same rules as a create. They lived inside NewEnvironment,
// where an update could not reach them, so the table CHECK was the only thing
// between a form and a blank name.
//
// AND IT IS REFUSED THE WAY THE HOUSE RULE SAYS: 422, the form re-rendered in
// error state, with what the operator typed still in the fields. A redirect
// would refill the row from storage, so the value they just corrected reappears
// as the old one and nothing says whether it saved. Both reviews called this
// out; the first version of this test asserted neither the status nor the
// preserved input, so it would have passed either way.
func TestAnEnvironmentUpdateIsValidated(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := h.refs.Environments["prod"]
	resp := h.post("/environments/"+envID, url.Values{
		"csrf_token": {h.csrfToken("/environments")},
		"code":       {"prod"}, "name": {""},
		"role": {"production"}, "criticality": {"9"},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a refused environment returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(page, "field-error") {
		t.Error("the refusal did not say which field was wrong")
	}
	if !strings.Contains(page, "<nav") {
		t.Error("the refusal answered with a fragment, not a page")
	}
	// The typed value survives: 9 is what they entered and 9 is what comes
	// back, not the stored 1.
	row := editingRow(t, page)
	if !strings.Contains(row, `value="9"`) {
		t.Errorf("the rejected row lost what the operator typed: %s", row)
	}
	// And nothing was stored.
	after := body(t, h.get("/environments", false))
	if strings.Contains(after, `<td class="num">9</td>`) {
		t.Error("a criticality of 9 was stored; the range check did not run on update")
	}
}

// A read-only user gets no editor anywhere in this slice, asking by id.
func TestReadOnlyUsersGetNoEditorsInThisSlice(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	serviceID := h.refs.Services["orders-api"]
	instanceID := firstInstanceEditID(t, body(t, h.get("/services/"+serviceID, false)))
	envID := h.refs.Environments["prod"]
	endpointID := h.refs.Endpoints["rabbitmq/amqp"]
	rabbit := h.refs.Services["rabbitmq"]

	h.logout()
	h.login("viewer", "viewer-password")

	for _, c := range []struct{ name, path, marker string }{
		{"placement", "/services/" + serviceID + "?edit=" + instanceID, `name="desired_state"`},
		{"endpoint", "/services/" + rabbit + "?edit=" + endpointID, `name="bind_scope"`},
		{"environment", "/environments?edit=" + envID, `name="criticality"`},
	} {
		if strings.Contains(body(t, h.get(c.path, false)), c.marker) {
			t.Errorf("a read-only user asking for the %s editor by id was given one", c.name)
		}
	}
}

// firstEditID returns the first id offered for editing on a page.
func firstEditID(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`\?edit=([0-9a-f-]+)`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("nothing on the page offers an edit link")
	}
	return m[1]
}

// firstInstanceEditID returns the id of the first placement offered for
// editing, which is not necessarily the first edit link on the page.
func firstInstanceEditID(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `id="instances"`)
	j := strings.Index(page, `id="endpoints"`)
	if i < 0 || j < 0 || j < i {
		t.Fatal("the service page no longer has both panels in the expected order")
	}
	return firstEditID(t, page[i:j])
}

// firstEndpointEditID returns the id of the first endpoint offered for editing.
func firstEndpointEditID(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `id="endpoints"`)
	if i < 0 {
		t.Fatal("the service page has no endpoints panel")
	}
	return firstEditID(t, page[i:])
}

// editingRow returns the one <tr> carrying the inline editor, so an assertion
// about what a form offers cannot be answered by a different form on the page.
func editingRow(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `class="row-editing"`)
	if i < 0 {
		t.Fatal("no row is being edited")
	}
	end := strings.Index(page[i:], "</tr>")
	return page[i : i+end]
}

// newestChangeDiff returns the FIELD NAMES of the newest audit entry.
//
// The log is newest first, so the top row is whatever the test just did, and
// scoping to one row matters: a create elsewhere on the page lists every field
// including the ones being asserted absent.
//
// Reads the rendered field column rather than the raw payload. That is the
// point of the screen — the entry is now a table of fields, so the test asserts
// on what an operator actually sees rather than on serialised JSON.
func newestChangeDiff(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, "<tbody>")
	if i < 0 {
		t.Fatal("no change log table on the page")
	}
	// Scoped to the FIRST change cell, not to the first </tr>: the diff is
	// itself a table now, so the first closing row tag belongs to a nested
	// field row rather than to the entry.
	const cell = `<td style="max-width:640px">`
	body := page[i:]
	first := strings.Index(body, cell)
	if first < 0 {
		t.Fatal("no change entry on the page")
	}
	scope := body[first+len(cell):]
	if next := strings.Index(scope, cell); next >= 0 {
		scope = scope[:next]
	}
	fields := regexp.MustCompile(`<td class="diff-field">([^<]+)</td>`).
		FindAllStringSubmatch(scope, -1)
	if len(fields) == 0 {
		// Either an unparsed diff or a create summary: fall back to the raw
		// cell so the caller still sees something to assert against.
		return scope
	}
	var names []string
	for _, f := range fields {
		names = append(names, f[1])
	}
	return strings.Join(names, " ")
}

func firstPortEditID(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, "<h2>Interfaces</h2>")
	if i < 0 {
		t.Fatal("no interfaces panel on the asset page")
	}
	m := regexp.MustCompile(`class="btn btn-sm" href="[^"]*\?edit=([0-9a-f-]+)#ports"`).
		FindStringSubmatch(page[i:])
	if m == nil {
		t.Fatal("no port offers an edit link")
	}
	return m[1]
}

// firstAddressEditID returns an address offered for editing and the text it
// currently shows, so a test can assert the old value is gone.
func firstAddressEditID(t *testing.T, page string) (string, string) {
	t.Helper()
	i := strings.Index(page, "<h2>Interfaces</h2>")
	if i < 0 {
		t.Fatal("no interfaces panel on the asset page")
	}
	m := regexp.MustCompile(`<div>([0-9a-f.:]+) <span class="pill pill-muted">[^<]*</span>\s*<a class="id" href="[^"]*\?edit=([0-9a-f-]+)`).
		FindStringSubmatch(page[i:])
	if m == nil {
		t.Fatal("no address offers an edit link")
	}
	return m[2], m[1]
}

// The store refuses to move a port even when the form says to. The UI not
// offering a field is not a control: a hand-written POST is one line of curl,
// and moving a port to another chassis rewrites the cable map and every path
// that runs through it.
func TestAPortCannotBeMovedByPostingAnAssetID(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	otherID := h.refs.Assets["hv-02"]
	ifaceID := firstPortEditID(t, body(t, h.get("/assets/"+assetID, false)))
	otherBefore := body(t, h.get("/assets/"+otherID, false))

	resp := h.post("/interfaces/"+ifaceID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
		"name":       {"moved"}, "form_factor": {"sfp28"},
		// The two the handler must ignore.
		"asset_id": {otherID}, "lag_parent_id": {ifaceID},
	}, false)
	resp.Body.Close()

	if !strings.Contains(body(t, h.get("/assets/"+assetID, false)), "moved") {
		t.Fatal("the rename did not take, so the rest of this proves nothing")
	}
	otherAfter := body(t, h.get("/assets/"+otherID, false))
	if strings.Contains(otherAfter, "moved") && !strings.Contains(otherBefore, "moved") {
		t.Error("a posted asset_id moved the port to another chassis")
	}
}

// Clearing the amount and saving must not rewrite a real figure to zero. It
// used to: parseAmountMinor treated blank as 0, so the only record that
// anything happened was the change_log. Found by a security review.
func TestClearingACostAmountIsRefusedRatherThanStoredAsZero(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	costID := firstCostFormID(t, body(t, h.get("/assets/"+id, false)))

	resp := h.post("/assets/"+id+"/costs/"+costID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"kind":       {"acquisition"}, "period": {"once"},
		"amount": {""}, "valid_from": {"2024-01-01"},
	}, false)
	resp.Body.Close()

	after := body(t, h.get("/assets/"+id, false))
	if strings.Contains(after, "€0.00") {
		t.Error("clearing the amount stored zero")
	}
	if !strings.Contains(after, "€8,400.00") {
		t.Error("the stored amount did not survive a blank submission")
	}
}

// ---------- optimistic concurrency ----------

// TWO PEOPLE, ONE ROW. Both open the editor, both save. Without a version the
// second write silently reverts the first and change_log records the revert as
// a deliberate act by whoever was slower -- the misattribution docs/AUDIT.md
// objects to for observed state, arriving through the declared door.
//
// The form carries the version it was rendered with, so the second save is
// compared against a row that has moved and is refused.
func TestASecondSaveFromAStaleFormIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := h.refs.Environments["prod"]
	// Both operators open the editor and read the same version.
	staleVersion := versionInForm(t, body(t, h.get("/environments?edit="+envID, false)))

	first := h.post("/environments/"+envID, url.Values{
		"csrf_token": {h.csrfToken("/environments")}, "row_version": {staleVersion},
		"code": {"prod"}, "name": {"First writer"},
		"role": {"production"}, "criticality": {"1"}, "in_scope": {"true"},
	}, false)
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("the first save returned %d, want a redirect", first.StatusCode)
	}

	second := h.post("/environments/"+envID, url.Values{
		"csrf_token": {h.csrfToken("/environments")}, "row_version": {staleVersion},
		"code": {"prod"}, "name": {"Second writer"},
		"role": {"production"}, "criticality": {"1"}, "in_scope": {"true"},
	}, false)
	second.Body.Close()
	if second.StatusCode == http.StatusSeeOther {
		t.Error("the second save from a stale form was accepted")
	}

	after := body(t, h.get("/environments", false))
	if !strings.Contains(after, "First writer") {
		t.Error("the first writer's change was reverted by the second")
	}
	if strings.Contains(after, "Second writer") {
		t.Error("the stale write landed")
	}
}

// And the version moves, so a fresh read can save. A guard that refused
// everything would pass the test above and make the application unusable.
func TestAFreshFormSavesAfterSomebodyElseWrote(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	envID := h.refs.Environments["prod"]

	save := func(version, name string) int {
		resp := h.post("/environments/"+envID, url.Values{
			"csrf_token": {h.csrfToken("/environments")}, "row_version": {version},
			"code": {"prod"}, "name": {name},
			"role": {"production"}, "criticality": {"1"}, "in_scope": {"true"},
		}, false)
		resp.Body.Close()
		return resp.StatusCode
	}
	v1 := versionInForm(t, body(t, h.get("/environments?edit="+envID, false)))
	if got := save(v1, "One"); got != http.StatusSeeOther {
		t.Fatalf("the first save returned %d", got)
	}
	// Re-open the editor: a new version, and saving works again.
	v2 := versionInForm(t, body(t, h.get("/environments?edit="+envID, false)))
	if v1 == v2 {
		t.Errorf("the version did not move after a write: still %s", v2)
	}
	if got := save(v2, "Two"); got != http.StatusSeeOther {
		t.Errorf("a save from a freshly opened form returned %d, want a redirect", got)
	}
	if !strings.Contains(body(t, h.get("/environments", false)), "Two") {
		t.Error("the second, legitimate save did not land")
	}
}

// Retiring counts. A form opened before something was withdrawn must not save
// over the withdrawal, or an edit in a stale tab quietly brings a retired thing
// back. Every write bumps the version, retirements included -- that is the only
// reason this is caught.
//
// A CERTIFICATE, deliberately. Cost lines and placements are refused by their
// own "history is not amendable" guard whatever the version says, so testing
// with one proves nothing about the bump: the first version of this test used a
// cost line and passed with the retirement's bump deleted. UpdateCertificate
// has no such guard, so the version is the only thing standing here.
func TestAnEditOpenedBeforeARetirementIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	list := body(t, h.get("/certificates", false))
	m := regexp.MustCompile(`href="/certificates/([0-9a-f-]+)"`).FindStringSubmatch(list)
	if m == nil {
		t.Fatal("no certificate to edit")
	}
	certID := m[1]
	stale := versionInForm(t, body(t, h.get("/certificates/"+certID, false)))

	retire := h.post("/certificates/"+certID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/certificates/" + certID)},
	}, false)
	retire.Body.Close()

	resp := h.post("/certificates/"+certID, url.Values{
		"csrf_token": {h.csrfToken("/certificates/" + certID)}, "row_version": {stale},
		"subject_cn": {"stale-write.example.com"}, "lifecycle": {"active"},
	}, false)
	resp.Body.Close()

	after := body(t, h.get("/certificates/"+certID, false))
	if strings.Contains(after, "stale-write.example.com") {
		t.Error("an edit from a form opened before the retirement was applied anyway")
	}
	// And it did not quietly come back to life.
	if !strings.Contains(after, "retired") {
		t.Error("the certificate is no longer retired: the stale write un-retired it")
	}
}

// Every editor in the application emits its version AND ITS HANDLER USES IT.
//
// Rendering the token is half the contract and was the only half this test
// checked. The cost editor rendered it perfectly and its handler never read it
// back — so a second save from a stale form always won, silently, which is the
// exact bug the whole mechanism exists to prevent. A review caught it; this
// test could not have.
//
// Now every editor is submitted TWICE from one token. The second must be
// refused. That is a property of the pair, not of the markup, and it is the
// only assertion that would have failed.
func TestEveryEditorRefusesASecondSaveFromOneToken(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	serviceID := h.refs.Services["orders-api"]
	projectID := h.refs.Projects["platform"]
	envID := h.refs.Environments["prod"]
	assetPage := body(t, h.get("/assets/"+assetID, false))
	servicePage := body(t, h.get("/services/"+serviceID, false))
	addressID, _ := firstAddressEditID(t, assetPage)

	// WP-A4: the definition editor and the options editor are two separate
	// editors on two separate entities (a custom_field's own row_version,
	// docs/custom-fields-design.md §3), so each gets its own entry here.
	customFieldID := mustCreateCustomField(t, h, "asset", "edit-token", "text")
	customFieldSelectID := mustCreateCustomField(t, h, "asset", "options-token", "select")

	editors := []struct {
		name   string
		open   string // where the form is rendered
		action string // where it posts
		form   url.Values
	}{
		{"environment", "/environments?edit=" + envID, "/environments/" + envID,
			url.Values{"code": {"prod"}, "name": {"Env"}, "role": {"production"},
				"criticality": {"1"}, "in_scope": {"true"}}},
		{"cost line", "/assets/" + assetID + "?edit=" + firstCostFormID(t, assetPage),
			"/assets/" + assetID + "/costs/" + firstCostFormID(t, assetPage),
			url.Values{"kind": {"acquisition"}, "period": {"once"},
				"amount": {"4321"}, "valid_from": {"2024-01-01"}}},
		{"port", "/assets/" + assetID + "?edit=" + firstPortEditID(t, assetPage),
			"/interfaces/" + firstPortEditID(t, assetPage),
			url.Values{"name": {"eth-token"}, "form_factor": {"rj45"}}},
		{"placement", "/services/" + serviceID + "?edit=" + firstInstanceEditID(t, servicePage),
			"/instances/" + firstInstanceEditID(t, servicePage),
			url.Values{"runtime_type": {"systemd"}, "ordinal": {"5"},
				"desired_state": {"running"}}},
		{"asset", "/assets/" + assetID + "?edit=" + assetID, "/assets/" + assetID,
			url.Values{"name": {"hv-token"}, "kind": {"hypervisor"}, "lifecycle": {"active"}}},
		{"project", "/projects/" + projectID + "?edit=" + projectID, "/projects/" + projectID,
			url.Values{"code": {"platform"}, "name": {"P"}, "lifecycle": {"active"}}},
		{"service", "/services/" + serviceID + "?edit=" + serviceID, "/services/" + serviceID,
			url.Values{"code": {"orders-api"}, "name": {"S"}, "kind": {"api"},
				"environment_id": {envID}, "availability": {"standalone"},
				"tier": {"2"}, "lifecycle": {"active"}}},
		{"certificate", "/certificates/" + certificateID(t, h), "/certificates/" + certificateID(t, h),
			url.Values{"subject_cn": {"token.example.com"}, "lifecycle": {"active"}}},
		{"address", "/assets/" + assetID + "?edit=" + addressID,
			"/addresses/" + addressID,
			url.Values{"addr_text": {"10.77.77.77"}, "role": {"primary"}}},
		{"endpoint", "/services/" + serviceID + "?edit=" + firstEndpointEditID(t, servicePage),
			"/endpoints/" + firstEndpointEditID(t, servicePage),
			url.Values{"name": {"ep-token"}, "l4_proto": {"tcp"}, "port": {"7001"},
				"bind_scope": {"host"}, "tls_mode": {"none"}, "exposure": {"internal"}}},
		{"custom field", "/custom-fields?edit=" + customFieldID, "/custom-fields/" + customFieldID,
			url.Values{"code": {"edit-token"}, "label": {"Edit Token"}, "kind": {"text"},
				"description": {"redrawn by the token test"}}},
		{"custom field options", "/custom-fields?options=" + customFieldSelectID,
			"/custom-fields/" + customFieldSelectID + "/options",
			url.Values{"option_value": {"gold"}, "option_label": {"Gold"}}},
		// WP-A4 Task 6: the value editor is its own third editor, on the
		// PARENT ASSET's own row_version -- not the custom_field's and not
		// the value's -- design.md §3. It carries no ?edit=, because it is
		// always rendered when a live field is defined, not gated by the
		// shared row editor.
		{"custom field value", "/assets/" + assetID, "/assets/" + assetID + "/custom-fields",
			url.Values{"cf_" + customFieldID: {"value-token"}}},
	}

	for _, e := range editors {
		page := body(t, h.get(e.open, false))
		if !strings.Contains(page, `name="row_version"`) {
			t.Errorf("the %s editor renders no version, so its guard is inert", e.name)
			continue
		}
		token := versionInForm(t, page)
		if token == "0" {
			t.Errorf("the %s editor renders version 0 — the column is missing from its query", e.name)
			continue
		}

		post := func() int {
			form := url.Values{}
			for k, v := range e.form {
				form[k] = v
			}
			form.Set("csrf_token", h.csrfToken(e.open))
			form.Set("row_version", token)
			resp := h.post(e.action, form, false)
			resp.Body.Close()
			return resp.StatusCode
		}
		if first := post(); first != http.StatusSeeOther {
			t.Errorf("the %s editor refused a save from a FRESH token (%d)", e.name, first)
			continue
		}
		if second := post(); second == http.StatusSeeOther {
			t.Errorf("the %s editor accepted a second save from the same token: "+
				"its handler does not read the version back", e.name)
		}
	}
}

// versionInForm reads the token out of a rendered editor.
func versionInForm(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`name="row_version" value="(\d+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the form carries no version")
	}
	return m[1]
}

// A stale write and a duplicate are both conflicts and are NOT the same
// problem. Told "that already exists", an operator goes looking for a name
// clash that is not there; what actually happened is that somebody else saved
// first. The message has to say which.
func TestAStaleWriteIsNotReportedAsADuplicate(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	prefixID := firstEditID(t, body(t, h.get("/prefixes", false)))
	stale := versionInForm(t, body(t, h.get("/prefixes?edit="+prefixID, false)))

	save := func(version, cidr string) *http.Response {
		return h.post("/prefixes/"+prefixID, url.Values{
			"csrf_token": {h.csrfToken("/prefixes")}, "row_version": {version},
			"cidr_text": {cidr}, "role": {"first"},
		}, false)
	}
	first := save(stale, "10.31.0.0/16")
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("the first save returned %d", first.StatusCode)
	}

	lost := save(stale, "10.32.0.0/16")
	page := body(t, lost)
	// 409, not 422: nothing the operator typed was wrong.
	if lost.StatusCode != http.StatusConflict {
		t.Errorf("a stale write returned %d, want 409", lost.StatusCode)
	}
	if !strings.Contains(page, "somebody else changed this") {
		t.Errorf("a stale write was not explained as one:\n%s", firstFieldError(t, page))
	}
	if strings.Contains(page, "already declared") {
		t.Error("a stale write was reported as a duplicate network")
	}
}

func firstFieldError(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<div class="field-error">(.*?)</div>`).FindStringSubmatch(page)
	if m == nil {
		return "(no field error at all)"
	}
	return m[1]
}

// ---------- the three entities whose write routes had no form ----------
//
// AssetUpdate, ServiceUpdate and ProjectUpdate existed and were reachable by
// anybody who could construct a POST, and by nobody using the application.
// The mirror image of the store methods with no route that started this work.

func TestCorrectingAnAsset(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	form := editingRow // silence: not used here, keeps imports honest
	_ = form
	page := body(t, h.get("/assets/"+id+"?edit="+id, false))

	if !strings.Contains(page, `value="hv-01"`) {
		t.Error("the asset editor does not show the stored name")
	}
	// THE PARENT IS NOT ON OFFER. Moving an asset rewrites asset_closure and
	// with it every containment answer and impact simulation.
	if strings.Contains(page, `name="parent_id"`) {
		t.Error("the asset editor offers to re-parent, which moves the graph")
	}
	// Fields that only exist once the thing does.
	for _, want := range []string{`name="lifecycle"`, `name="asset_tag"`, `name="row_version"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the asset editor is missing %s", want)
		}
	}

	version := versionInForm(t, page)
	resp := h.post("/assets/"+id, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)}, "row_version": {version},
		"name": {"hv-01-corrected"}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		"vendor": {"Dell"}, "asset_tag": {"TAG-9"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting an asset returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(after, "hv-01-corrected") {
		t.Error("the corrected name is not shown")
	}
	if !strings.Contains(body(t, h.get("/changes", false)), "asset") {
		t.Error("correcting an asset wrote no change_log entry")
	}
}

// Environments are a set, and a REFUSED save must redraw the boxes the operator
// actually ticked. A single field falls back to what is stored when it is
// missing, which is right for a text input and wrong for a checkbox: an
// unticked box submits nothing, so falling back would silently re-tick it and
// the next save would put the environment back without anybody choosing to.
//
// Asserted on the refusal path deliberately. On a SUCCESSFUL save the page is
// re-read from storage, which shows the cleared state whatever this code does —
// the first version of this test did that and passed with the whole Multi
// lookup deleted.
func TestARefusedAssetEditRedrawsTheBoxesThatWereTicked(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	page := body(t, h.get("/assets/"+id+"?edit="+id, false))
	if !strings.Contains(page, " checked>") {
		t.Fatal("the asset is in no environment, so unticking proves nothing")
	}

	// A save that fails validation (blank name) and ticks NO environment.
	resp := h.post("/assets/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + id)},
		"row_version": {versionInForm(t, page)},
		"name":        {""}, "kind": {"hypervisor"}, "lifecycle": {"active"},
	}, false)
	refused := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("the refused save returned %d, want 422", resp.StatusCode)
	}
	if strings.Contains(refused, " checked>") {
		t.Error("a refused save re-ticked an environment the operator had unticked")
	}
	// And the stored row is untouched: a refusal changes nothing.
	if !strings.Contains(body(t, h.get("/assets/"+id+"?edit="+id, false)), " checked>") {
		t.Error("a refused save cleared the stored environments")
	}
}

func TestCorrectingAService(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Services["orders-api"]
	page := body(t, h.get("/services/"+id+"?edit="+id, false))
	if !strings.Contains(page, `value="orders-api"`) {
		t.Error("the service editor does not show the stored code")
	}
	version := versionInForm(t, page)

	resp := h.post("/services/"+id, url.Values{
		"csrf_token": {h.csrfToken("/services/" + id)}, "row_version": {version},
		"code": {"orders-api"}, "name": {"Orders API, corrected"},
		"kind": {"api"}, "environment_id": {h.refs.Environments["prod"]},
		"availability": {"active_active"}, "min_healthy": {"2"}, "tier": {"1"},
		"lifecycle": {"active"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a service returned %d, want a redirect", resp.StatusCode)
	}
	if !strings.Contains(body(t, h.get("/services/"+id, false)), "Orders API, corrected") {
		t.Error("the corrected service name is not shown")
	}
}

func TestCorrectingAProject(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Projects["platform"]
	page := body(t, h.get("/projects/"+id+"?edit="+id, false))
	if !strings.Contains(page, "Correct this project") {
		t.Fatal("the project editor did not open")
	}
	version := versionInForm(t, page)

	resp := h.post("/projects/"+id, url.Values{
		"csrf_token": {h.csrfToken("/projects/" + id)}, "row_version": {version},
		"code": {"platform"}, "name": {"Platform, corrected"},
		"description": {"one sentence"}, "lifecycle": {"active"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a project returned %d, want a redirect", resp.StatusCode)
	}
	if !strings.Contains(body(t, h.get("/projects/"+id, false)), "Platform, corrected") {
		t.Error("the corrected project name is not shown")
	}
	// The add form on the LIST page is the same partial and must still add.
	if !strings.Contains(body(t, h.get("/projects", false)), "Add a project") {
		t.Error("extracting the shared form broke the add form")
	}
}

// A refused save keeps what was typed, on all three.
func TestARefusedEntityEditKeepsWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]

	page := body(t, h.get("/assets/"+assetID+"?edit="+assetID, false))
	resp := h.post("/assets/"+assetID, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, page)},
		"name":        {""}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		"vendor": {"Typed and kept"},
	}, false)
	refused := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a refused asset edit returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(refused, "field-error") {
		t.Error("the refusal did not say which field was wrong")
	}
	if !strings.Contains(refused, `value="Typed and kept"`) {
		t.Error("the refused form lost the other fields the operator had typed")
	}
	if !strings.Contains(refused, "<nav") {
		t.Error("the refusal answered with a fragment, not a page")
	}
}

func TestReadOnlyUsersGetNoEntityEditors(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]
	serviceID := h.refs.Services["orders-api"]
	projectID := h.refs.Projects["platform"]

	h.logout()
	h.login("viewer", "viewer-password")
	for _, c := range []struct{ name, path, marker string }{
		{"asset", "/assets/" + assetID + "?edit=" + assetID, `name="asset_tag"`},
		{"service", "/services/" + serviceID + "?edit=" + serviceID, `name="availability"`},
		{"project", "/projects/" + projectID + "?edit=" + projectID, "Correct this project"},
	} {
		if strings.Contains(body(t, h.get(c.path, false)), c.marker) {
			t.Errorf("a read-only user asking for the %s editor by id was given one", c.name)
		}
	}
}

// A number that is not a number is refused, not silently replaced.
//
// intValue used to return its fallback for unparseable input, so a POST of
// tier=abc saved the STORED value and answered 303 — the operator is told the
// edit went in and it did not. Blank still means "leave it alone": a field left
// empty, or one that never rendered, is not a lie.
func TestANonNumericFieldIsRefusedRatherThanIgnored(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := h.refs.Environments["prod"]
	page := body(t, h.get("/environments?edit="+envID, false))
	stored := versionInForm(t, page)
	resp := h.post("/environments/"+envID, url.Values{
		"csrf_token":  {h.csrfToken("/environments")},
		"row_version": {versionInForm(t, page)},
		"code":        {"prod"}, "name": {"Production"},
		"role": {"production"}, "criticality": {"not a number"}, "in_scope": {"true"},
	}, false)
	refused := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a non-numeric criticality returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(refused, "must be a whole number") {
		t.Error("the operator was not told what was wrong with it")
	}
	// Nothing was written: the version is the cheapest proof, since every
	// write bumps it. Comparing whole pages would compare CSRF tokens too.
	if now := versionInForm(t, body(t, h.get("/environments?edit="+envID, false))); now != stored {
		t.Errorf("a refused submission wrote anyway: version %s -> %s", stored, now)
	}
}

// And a blank one still means "leave it": the two cases must not collapse.
func TestABlankNumberKeepsWhatIsStored(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["orders-api"]
	page := body(t, h.get("/services/"+serviceID+"?edit="+serviceID, false))
	tier := regexp.MustCompile(`name="tier"[^>]*value="(\d+)"`).FindStringSubmatch(page)
	if tier == nil {
		t.Fatal("the service editor renders no tier")
	}

	resp := h.post("/services/"+serviceID, url.Values{
		"csrf_token":  {h.csrfToken("/services/" + serviceID)},
		"row_version": {versionInForm(t, page)},
		"code":        {"orders-api"}, "name": {"Orders API"}, "kind": {"api"},
		"environment_id": {h.refs.Environments["prod"]},
		"availability":   {"active_active"}, "min_healthy": {"2"},
		"tier": {""}, "lifecycle": {"active"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a blank tier returned %d, want a redirect", resp.StatusCode)
	}
	after := body(t, h.get("/services/"+serviceID+"?edit="+serviceID, false))
	if !strings.Contains(after, `value="`+tier[1]+`"`) {
		t.Errorf("a blank tier did not keep the stored value %s", tier[1])
	}
}

// A retired service's shape is history, and its children are part of that
// shape. An endpoint has no lifecycle of its own — it exists because its
// service declares it — so the fact that makes it unamendable lives one level
// up. Same for a placement: if the service is gone, so is the fact that it ran
// somewhere.
func TestARetiredServicesShapeCannotBeRewritten(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["rabbitmq"]
	endpointID := h.refs.Endpoints["rabbitmq/amqp"]
	page := body(t, h.get("/services/"+serviceID, false))
	instanceID := firstInstanceEditID(t, page)

	retire := h.post("/services/"+serviceID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
	}, false)
	retire.Body.Close()

	// The endpoint.
	resp := h.post("/endpoints/"+endpointID, url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
		"name":       {"rewritten-history"}, "l4_proto": {"tcp"}, "port": {"1234"},
		"bind_scope": {"host"}, "tls_mode": {"none"}, "exposure": {"internal"},
	}, false)
	resp.Body.Close()

	// The placement.
	resp2 := h.post("/instances/"+instanceID, url.Values{
		"csrf_token":   {h.csrfToken("/services/" + serviceID)},
		"runtime_type": {"systemd"}, "role": {"rewritten-history"},
		"ordinal": {"0"}, "desired_state": {"running"},
	}, false)
	resp2.Body.Close()

	after := body(t, h.get("/services/"+serviceID, false))
	if strings.Contains(after, "rewritten-history") {
		t.Error("a retired service's recorded shape was rewritten")
	}
	// The service really is retired, so the assertion above is about the guard
	// and not about a request that failed for some unrelated reason.
	if !strings.Contains(after, "retired") {
		t.Fatal("the service was not retired, so this test proved nothing")
	}
}

// ---------- a withdrawn socket ----------

// An endpoint can stop existing, and what that means differs per query. The
// read audit in migration 00020 records the decision for all fifteen; these
// pin the four that FILTER, because a missing WHERE looks identical whether it
// was considered or forgotten.
func TestAWithdrawnEndpointLeavesTheLiveViewsButKeepsItsRecord(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// A socket nothing depends on, so it can actually be withdrawn.
	serviceID, endpointID, port := withdrawableEndpoint(t, h)

	resp := h.post("/endpoints/"+endpointID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
	}, false)
	resp.Body.Close()

	// Fetched twice: the first GET carries the success flash, whose text also
	// contains the word "withdrawn". Asserting on the page would then pass with
	// the row's own marker deleted -- which it did, until this mutation caught
	// it. The second GET has no flash, so the only "withdrawn" left is the row.
	body(t, h.get("/services/"+serviceID, false))
	page := body(t, h.get("/services/"+serviceID, false))
	// KEPT: the service page still shows it, marked, exactly as a withdrawn
	// placement is. Deleting the row would delete the answer to "what was it
	// listening on when this broke".
	if !strings.Contains(page, "scratch") {
		t.Fatal("the withdrawn endpoint vanished from its service page")
	}
	if !strings.Contains(page, "withdrawn") {
		t.Error("the withdrawn endpoint is not marked as such on its row")
	}
	if strings.Contains(page, `/services/`+serviceID+`?edit=`+endpointID) {
		t.Error("a withdrawn endpoint is still offered for editing")
	}

	// FILTERED: search by port must not send anybody to a socket that is gone.
	if hits := body(t, h.get("/search?q="+port, false)); strings.Contains(hits, "listens on") {
		t.Errorf("port search still answers with the withdrawn socket:\n%s", port)
	}
	// FILTERED: the dependency picker must not offer it as a provider.
	depForm := body(t, h.get("/services/"+serviceID, false))
	if strings.Contains(depForm, `<option value="`+endpointID+`"`) {
		t.Error("the dependency picker still offers the withdrawn socket as a provider")
	}
}

// It is not amendable either: history, like a retired cost line.
func TestAWithdrawnEndpointCannotBeAmended(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	serviceID, endpointID, _ := withdrawableEndpoint(t, h)

	retire := h.post("/endpoints/"+endpointID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
	}, false)
	retire.Body.Close()

	resp := h.post("/endpoints/"+endpointID, url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
		"name":       {"amended-after-death"}, "l4_proto": {"tcp"}, "port": {"9999"},
		"bind_scope": {"host"}, "tls_mode": {"none"}, "exposure": {"internal"},
	}, false)
	resp.Body.Close()
	if strings.Contains(body(t, h.get("/services/"+serviceID, false)), "amended-after-death") {
		t.Error("a withdrawn endpoint was amended")
	}
}

// AND IT CANNOT BE PULLED OUT FROM UNDER A LIVE EDGE. A dependency naming this
// socket describes traffic somebody believes is flowing. Withdrawing it would
// leave an active edge pointing at a thing declared not to exist — and because
// the impact engine now skips retired sockets, that outage would quietly stop
// propagating along an edge still drawn on screen.
func TestAnEndpointWithLiveDependantsCannotBeWithdrawn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// rabbitmq/amqp is a provider in the fixture graph.
	endpointID := h.refs.Endpoints["rabbitmq/amqp"]
	serviceID := h.refs.Services["rabbitmq"]
	before := body(t, h.get("/services/"+serviceID, false))

	resp := h.post("/endpoints/"+endpointID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
	}, false)
	resp.Body.Close()

	after := body(t, h.get("/services/"+serviceID, false))
	if strings.Contains(after, "withdrawn") && !strings.Contains(before, "withdrawn") {
		t.Error("an endpoint with a live dependant was withdrawn")
	}
	if !strings.Contains(after, "still has something depending on it") {
		t.Error("the operator was not told why it was refused")
	}
}

// withdrawableEndpoint adds a fresh socket and returns its service, id and
// port. CREATED rather than found: nothing depends on a socket that did not
// exist a moment ago, which is the only way to be sure the retirement under
// test is the one being exercised. The first version searched the fixture by
// TRYING to retire each candidate, so the helper did the retiring and every
// test's own call was a no-op against an already-withdrawn row.
func withdrawableEndpoint(t *testing.T, h *harness) (string, string, string) {
	t.Helper()
	serviceID := h.refs.Services["orders-api"]
	const port = "19999"

	resp := h.post("/services/"+serviceID+"/endpoints", url.Values{
		"csrf_token": {h.csrfToken("/services/" + serviceID)},
		"name":       {"scratch"}, "l4_proto": {"tcp"}, "port": {port},
		"bind_scope": {"host"}, "tls_mode": {"none"}, "exposure": {"internal"},
	}, false)
	resp.Body.Close()

	page := body(t, h.get("/services/"+serviceID, false))
	i := strings.Index(page, `id="endpoints"`)
	if i < 0 {
		t.Fatal("the service page has no endpoints panel")
	}
	for _, row := range regexp.MustCompile(`(?s)<tr[^>]*>.*?</tr>`).FindAllString(page[i:], -1) {
		if !strings.Contains(row, ">scratch") {
			continue
		}
		m := regexp.MustCompile(`\?edit=([0-9a-f-]+)#endpoints`).FindStringSubmatch(row)
		if m == nil {
			t.Fatal("the new endpoint offers no edit link, so it has no id to return")
		}
		return serviceID, m[1], port
	}
	t.Fatal("the endpoint that was just added is not on the page")
	return "", "", ""
}

// A REFUSED FORM KEEPS ITS STALE TOKEN, so the next click of Save is refused
// again rather than becoming a one-click force-overwrite of the very edit the
// operator was just warned about. The message tells them to go and read that
// edit first; before this, the mechanics made that optional.
func TestARefusedStaleSaveCannotBeForcedByPressingSaveAgain(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := h.refs.Environments["prod"]
	token := versionInForm(t, body(t, h.get("/environments?edit="+envID, false)))
	save := func(version, name string) *http.Response {
		return h.post("/environments/"+envID, url.Values{
			"csrf_token": {h.csrfToken("/environments")}, "row_version": {version},
			"code": {"prod"}, "name": {name},
			"role": {"production"}, "criticality": {"1"}, "in_scope": {"true"},
		}, false)
	}
	first := save(token, "The other operator")
	first.Body.Close()

	refused := save(token, "Mine")
	page := body(t, refused)
	if refused.StatusCode != http.StatusConflict {
		t.Fatalf("the stale save returned %d, want 409", refused.StatusCode)
	}
	// The refused page hands back the token that was refused, not a fresh one.
	if back := versionInForm(t, page); back != token {
		t.Errorf("the refused form re-armed with a fresh token %s (submitted %s): "+
			"pressing Save again would overwrite blindly", back, token)
	}
	// And submitting it again really is refused again.
	again := save(versionInForm(t, page), "Mine, forced")
	again.Body.Close()
	if again.StatusCode == http.StatusSeeOther {
		t.Error("a blind resubmit of the refused form went through")
	}
	if !strings.Contains(body(t, h.get("/environments", false)), "The other operator") {
		t.Error("the other operator's edit was overwritten")
	}
}

// certificateID returns any certificate, for the editor enumeration.
func certificateID(t *testing.T, h *harness) string {
	t.Helper()
	m := regexp.MustCompile(`href="/certificates/([0-9a-f-]+)"`).
		FindStringSubmatch(body(t, h.get("/certificates", false)))
	if m == nil {
		t.Fatal("no certificate in the fixture")
	}
	return m[1]
}

// An optional number that arrives as garbage is refused, not silently dropped.
//
// optionalInt returned nil on a parse error, and nil is a VALID value for every
// field it feeds — so Validate passed, the store wrote NULL, and the response
// said "updated". An operator who types 1000mbps into a port's speed loses the
// speed with no warning. Same defect intValue had; found by a review that
// pointed out fixing one and not the other was arbitrary.
func TestGarbageInAnOptionalNumberDoesNotClearIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	page := body(t, h.get("/assets/"+assetID, false))
	ifaceID := firstPortEditID(t, page)

	// Give it a speed to lose.
	set := h.post("/interfaces/"+ifaceID, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, body(t, h.get("/assets/"+assetID+"?edit="+ifaceID, false)))},
		"name":        {"eth-speed"}, "form_factor": {"sfp28"}, "speed_mbps": {"25000"},
	}, false)
	set.Body.Close()
	if !strings.Contains(body(t, h.get("/assets/"+assetID, false)), "25000") {
		t.Fatal("the speed was not stored, so losing it proves nothing")
	}

	resp := h.post("/interfaces/"+ifaceID, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, body(t, h.get("/assets/"+assetID+"?edit="+ifaceID, false)))},
		"name":        {"eth-speed"}, "form_factor": {"sfp28"}, "speed_mbps": {"1000mbps"},
	}, false)
	refused := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a non-numeric speed returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(refused, "whole number") {
		t.Error("the operator was not told what was wrong")
	}
	if !strings.Contains(body(t, h.get("/assets/"+assetID, false)), "25000") {
		t.Error("the stored speed was cleared by a value that was refused")
	}
}

// EVERY FIELD THAT CAN CARRY AN ERROR HAS SOMEWHERE TO SHOW IT.
//
// A refusal is only useful if the operator can see it. The service form
// returned 422 for a non-numeric rto_minutes and rendered nothing, because the
// message went into a map whose key had no rendering site — an invisible
// refusal, which reads to the operator exactly like a save that did nothing.
func TestARefusalIsVisibleForEveryNumericField(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	serviceID := h.refs.Services["orders-api"]
	page := body(t, h.get("/services/"+serviceID+"?edit="+serviceID, false))
	base := url.Values{
		"csrf_token":  {h.csrfToken("/services/" + serviceID)},
		"row_version": {versionInForm(t, page)},
		"code":        {"orders-api"}, "name": {"S"}, "kind": {"api"},
		"environment_id": {h.refs.Environments["prod"]},
		"availability":   {"standalone"}, "tier": {"2"}, "lifecycle": {"active"},
	}
	for _, field := range []string{"tier", "min_healthy", "rto_minutes", "rpo_minutes"} {
		form := url.Values{}
		for k, v := range base {
			form[k] = v
		}
		form.Set(field, "not a number")
		form.Set("csrf_token", h.csrfToken("/services/"+serviceID))
		form.Set("row_version", versionInForm(t,
			body(t, h.get("/services/"+serviceID+"?edit="+serviceID, false))))

		resp := h.post("/services/"+serviceID, form, false)
		refused := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: returned %d, want 422", field, resp.StatusCode)
			continue
		}
		if !strings.Contains(refused, "field-error") {
			t.Errorf("%s: refused with 422 and rendered no message — an invisible refusal", field)
		}
	}
}

// ---------- the audit screen ----------

// The change log shows FIELDS, not the serialised payload.
//
// It printed the raw JSON, which meant the one screen that demonstrates the
// product's strongest claim was the one screen too unreadable to show anybody:
// an update and a create looked alike, and no human could scan the column.
// Found by an external review of the live demo.
func TestTheChangeLogRendersFieldsRatherThanJSON(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Make one update with a known shape.
	envID := h.refs.Environments["prod"]
	page := body(t, h.get("/environments?edit="+envID, false))
	resp := h.post("/environments/"+envID, url.Values{
		"csrf_token":  {h.csrfToken("/environments")},
		"row_version": {versionInForm(t, page)},
		"code":        {"prod"}, "name": {"Renamed for the audit screen"},
		"role": {"production"}, "criticality": {"1"}, "in_scope": {"true"},
	}, false)
	resp.Body.Close()

	log := body(t, h.get("/changes", false))
	if !strings.Contains(log, `class="diff-field"`) {
		t.Fatal("the change log renders no field rows")
	}
	// The field that moved is named, with both sides of the move.
	if !strings.Contains(log, ">name</td>") {
		t.Error("the changed field is not named")
	}
	if !strings.Contains(log, `class="diff-old"`) || !strings.Contains(log, `class="diff-new"`) {
		t.Error("the entry does not show both sides of the change")
	}
	// The raw payload is gone: no bare JSON object in the change column.
	if strings.Contains(log, `{&#34;new&#34;:{&#34;`) || strings.Contains(log, `{"new":{"`) {
		t.Error("the serialised payload is still being printed")
	}
	// A create collapses rather than burying the updates around it.
	if !strings.Contains(log, "<summary") {
		t.Error("a create entry is not collapsed behind a disclosure")
	}
}

// A page link that will not parse is refused, not silently answered with the
// first page — which looks identical to reaching the first page legitimately,
// so a paginated export comes back short and nothing says so.
func TestAnUnreadablePageCursorIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	for _, bad := range []string{"garbage", "notatime 019fd217-0e49-723d-a187-a97bcb24b4ad", "%%%"} {
		resp := h.get("/changes?before="+url.QueryEscape(bad), false)
		page := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("cursor %q returned %d, want 422", bad, resp.StatusCode)
		}
		if !strings.Contains(page, "not readable") {
			t.Errorf("cursor %q: the operator was not told why", bad)
		}
	}

	// And a real cursor still works, so the refusal has not broken paging.
	first := body(t, h.get("/changes", false))
	m := regexp.MustCompile(`href="(/changes\?[^"]*before=[^"]*)"`).FindStringSubmatch(first)
	if m == nil {
		t.Skip("the fixture has only one page of changes")
	}
	// html.UnescapeString, not a hand-rolled replace: html/template escapes
	// more than &amp; in a URL attribute, and a half-decoded link is a test
	// artefact that looks exactly like a broken cursor.
	next := h.get(html.UnescapeString(m[1]), false)
	next.Body.Close()
	if next.StatusCode != http.StatusOK {
		t.Errorf("following the real page link returned %d", next.StatusCode)
	}
}

// An out-of-range report parameter is CLAMPED, not ignored.
//
// ?months=-5 and ?months=999999 both silently produced the default page, which
// is indistinguishable from asking for the default — so a bookmarked or
// mistyped link answered a different question under the same heading with
// nothing to say so. Clamped rather than refused because a query parameter
// lives in URLs people paste into tickets; 422 on a stale link helps nobody.
// Found by an external review of the live demo.
func TestAnOutOfRangeReportHorizonIsClamped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	horizon := func(query string) string {
		t.Helper()
		page := body(t, h.get("/reports/expiry"+query, false))
		m := regexp.MustCompile(`within (\d+) months`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("%q: the report does not state its horizon", query)
		}
		return m[1]
	}

	def := horizon("")
	if got := horizon("?months=-5"); got != "1" {
		t.Errorf("months=-5 gave a horizon of %s, want the minimum of 1", got)
	}
	if got := horizon("?months=999999"); got != "120" {
		t.Errorf("months=999999 gave a horizon of %s, want the maximum of 120", got)
	}
	// A value inside the range is honoured exactly, so clamping has not become
	// a way of ignoring the parameter after all.
	if got := horizon("?months=9"); got != "9" {
		t.Errorf("months=9 gave a horizon of %s, want 9", got)
	}
	if def == "9" {
		t.Skip("the default horizon happens to be 9; the check above proves nothing")
	}
}

// The vocabulary sort order was the fifth instance of the silent-fallback
// shape: "abc" became 0, the term sorted to the top of every picker in the
// application, and the response said saved. Found by sweeping for the shape
// rather than by anybody hitting it.
func TestANonNumericSortOrderIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/vocabularies", url.Values{
		"csrf_token": {h.csrfToken("/vocabularies")},
		"table":      {"asset_kind"},
		"code":       {"probe-term"}, "label": {"Probe"},
		"sort_order": {"not a number"},
	}, false)
	refused := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a non-numeric sort order returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(refused, "whole number") {
		t.Error("the operator was not told what was wrong")
	}
	if strings.Contains(body(t, h.get("/vocabularies", false)), "probe-term") {
		t.Error("the term was stored anyway, with a sort order of zero")
	}
}

// An audit entry has its own address, so it can be cited.
//
// "The configuration changed at 03:12" is an assertion; a link to the entry is
// evidence. The log is append-only and nothing is deleted, so the link cannot
// rot — which is the property that makes citing it worth anything.
func TestAnAuditEntryCanBeLinkedTo(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Make a change with a known shape so the entry has something to show.
	envID := h.refs.Environments["prod"]
	page := body(t, h.get("/environments?edit="+envID, false))
	save := h.post("/environments/"+envID, url.Values{
		"csrf_token":  {h.csrfToken("/environments")},
		"row_version": {versionInForm(t, page)},
		"code":        {"prod"}, "name": {"Cited in an incident"},
		"role": {"production"}, "criticality": {"1"}, "in_scope": {"true"},
	}, false)
	save.Body.Close()

	log := body(t, h.get("/changes", false))
	m := regexp.MustCompile(`href="/changes/([0-9a-f-]+)"`).FindStringSubmatch(log)
	if m == nil {
		t.Fatal("the change log does not link to its entries")
	}

	resp := h.get("/changes/"+m[1], false)
	entry := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the entry returned %d", resp.StatusCode)
	}
	// Everything needed to quote it: when, what, who, and the field detail.
	for _, want := range []string{"environment", "update", "name"} {
		if !strings.Contains(entry, want) {
			t.Errorf("the entry page does not show %q", want)
		}
	}
	// Actor and actor kind together — "admin" means a different thing
	// depending on whether a person, the system or an agent wrote it.
	if !strings.Contains(entry, "actor-kind") && !strings.Contains(entry, "user") {
		t.Error("the entry does not say what kind of actor wrote it")
	}
	// And it links back to the thing it is about.
	if !strings.Contains(entry, `href="/`) {
		t.Error("the entry does not link to the entity it concerns")
	}

	// A read-only user can cite one too: the audit trail is readable by
	// everyone who can read the estate.
	h.logout()
	h.login("viewer", "viewer-password")
	if got := h.get("/changes/"+m[1], false); got.StatusCode != http.StatusOK {
		t.Errorf("a read-only user cannot open an audit entry (%d)", got.StatusCode)
	}
}

// A entry id that names nothing is a 404, not a blank page pretending an entry
// exists.
func TestAMissingAuditEntryIs404(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	for _, id := range []string{"019fd217-0e49-723d-a187-a97bcb24b4ad", "not-a-uuid"} {
		resp := h.get("/changes/"+id, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("/changes/%s returned %d, want 404", id, resp.StatusCode)
		}
	}
}
