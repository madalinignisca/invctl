package web_test

import (
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
func TestAnEnvironmentUpdateIsValidated(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	envID := h.refs.Environments["prod"]
	resp := h.post("/environments/"+envID, url.Values{
		"csrf_token": {h.csrfToken("/environments")},
		"code":       {"prod"}, "name": {""},
		"role": {"production"}, "criticality": {"9"},
	}, false)
	resp.Body.Close()

	after := body(t, h.get("/environments", false))
	if strings.Contains(after, `<td class="num">9</td>`) {
		t.Error("a criticality of 9 was stored; the range check did not run on update")
	}
	if !strings.Contains(after, "not accepted") {
		t.Error("the operator was not told the environment was refused")
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

// newestChangeDiff returns the Change cell of the newest audit entry. The log
// is newest first, so the top row is whatever the test just did -- and scoping
// to one row matters: a create elsewhere on the page carries a full snapshot,
// which mentions every field including the ones being asserted absent.
func newestChangeDiff(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, "<tbody>")
	if i < 0 {
		t.Fatal("no change log table on the page")
	}
	row := page[i:]
	end := strings.Index(row, "</tr>")
	if end < 0 {
		t.Fatal("the change log has no rows")
	}
	cells := regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`).FindAllStringSubmatch(row[:end], -1)
	if len(cells) < 5 {
		t.Fatalf("the change log row has %d cells, want 5", len(cells))
	}
	return cells[4][1]
}

// A form post without HTMX is answered with a page, not a fragment and not a
// 500. The row editors are plain forms so they work with JavaScript off, and
// this was the one path where that was untrue: the 422 branch handed the
// service page template an endpointFormData, which carries none of the fields
// that page reads.
func TestAnEndpointFormRejectionIsAWholePageWithoutHTMX(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	serviceID := h.refs.Services["rabbitmq"]
	endpointID := h.refs.Endpoints["rabbitmq/amqp"]

	for _, c := range []struct {
		name string
		path string
		form url.Values
	}{
		{"correcting", "/endpoints/" + endpointID, url.Values{
			"name": {""}, "l4_proto": {"tcp"}, "port": {"5672"},
			"bind_scope": {"host"}, "tls_mode": {"none"}, "exposure": {"internal"}}},
		{"adding", "/services/" + serviceID + "/endpoints", url.Values{
			"name": {""}, "l4_proto": {"tcp"}, "port": {"9999"},
			"bind_scope": {"host"}, "tls_mode": {"none"}, "exposure": {"internal"}}},
	} {
		form := c.form
		form.Set("csrf_token", h.csrfToken("/services/"+serviceID))
		resp := h.post(c.path, form, false)
		page := body(t, resp)

		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s without HTMX returned %d, want 422", c.name, resp.StatusCode)
		}
		// A whole page: the layout is there, so there is a way back.
		if !strings.Contains(page, "<nav") {
			t.Errorf("%s without HTMX answered with a fragment, not a page", c.name)
		}
		if !strings.Contains(page, "field-error") {
			t.Errorf("%s without HTMX did not say what was wrong", c.name)
		}
	}
}
