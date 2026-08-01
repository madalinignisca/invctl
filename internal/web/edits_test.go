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

// ---------- ports, addresses and networks ----------
//
// The same line as everywhere else in this slice: a port keeps its chassis and
// its bond, an address keeps its port. What changes is what the thing IS.

func TestCorrectingAPort(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	ifaceID := firstPortEditID(t, body(t, h.get("/assets/"+assetID, false)))

	row := editingRow(t, body(t, h.get("/assets/"+assetID+"?edit="+ifaceID, false)))
	// The attachment is not on offer: neither the chassis nor the bond.
	for _, forbidden := range []string{`name="asset_id"`, `name="lag_parent_id"`} {
		if strings.Contains(row, forbidden) {
			t.Errorf("the port editor offers %s, which moves the topology", forbidden)
		}
	}

	resp := h.post("/interfaces/"+ifaceID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
		"name":       {"eth9"}, "form_factor": {"sfp28"},
		"speed_mbps": {"25000"}, "mac": {"AA-BB-CC-DD-EE-FF"},
		"mtu": {"9000"}, "is_mgmt": {"true"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a port returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(after, "eth9") {
		t.Error("the port's new name is not shown")
	}
	// A MAC is normalised whichever way it was pasted, so a lookup matches.
	if !strings.Contains(after, "aa:bb:cc:dd:ee:ff") {
		t.Error("a MAC pasted in dashed uppercase was not normalised")
	}
	// `enabled` was not submitted, and an unticked box submits nothing: the
	// port is now administratively down and the page must say so rather than
	// leaving it looking live.
	if !strings.Contains(after, "disabled") {
		t.Error("unticking enabled did not take, or the page does not show it")
	}
}

// The address value is editable and its derived columns move with it. addr_text
// is what a person reads; addr_start is what every containment query scans. A
// row where they disagree resolves to the wrong network and nothing on screen
// would ever show it.
func TestCorrectingAnAddressMovesItsDerivedColumns(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	page := body(t, h.get("/assets/"+assetID, false))
	addrID, oldAddr := firstAddressEditID(t, page)

	resp := h.post("/addresses/"+addrID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
		"addr_text":  {"10.42.42.42"}, "role": {"primary"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting an address returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(after, "10.42.42.42") || strings.Contains(after, oldAddr) {
		t.Errorf("the address still reads %s, not the corrected one", oldAddr)
	}
	// The proof that addr_start moved too: search resolves an address by a
	// bytewise range scan over the derived column, never over the text.
	hits := body(t, h.get("/search?q=10.42.42.42", false))
	if !strings.Contains(hits, "hv-01") {
		t.Error("the corrected address does not resolve to its asset: addr_start did not move with addr_text")
	}
}

func TestCorrectingANetwork(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/prefixes", false))
	prefixID := firstEditID(t, page)

	resp := h.post("/prefixes/"+prefixID, url.Values{
		"csrf_token": {h.csrfToken("/prefixes")},
		"cidr_text":  {"10.99.0.0/16"}, "vlan_id": {"99"}, "role": {"quarantined"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a network returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/prefixes", false))
	if !strings.Contains(after, "10.99.0.0/16") {
		t.Error("the network's new CIDR is not shown")
	}
	// THE REINDEX, exercised through FREE TEXT. A prefix is the one thing in
	// this slice that lives in the search index, and an update that skipped
	// indexEntity would leave search answering with the old row.
	//
	// Searching the CIDR itself proves nothing: an address or a network in the
	// query box is resolved STRUCTURALLY, by a range scan over addr_start,
	// which never touches the index. The first version of this assertion did
	// exactly that and passed with the reindex deleted. The role is plain text
	// and only reachable through the index.
	hits := body(t, h.get("/search?q=quarantined", false))
	if !strings.Contains(hits, "10.99.0.0/16") {
		t.Error("search does not know the corrected network: the reindex was skipped")
	}
}

// A refused correction comes back as 422 with the row reopened on what was
// typed, on every page in this slice. Both reviews of the previous slice
// flagged handlers that redirected instead, discarding the operator's input.
func TestARefusedNetworkEditKeepsWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	prefixID := firstEditID(t, body(t, h.get("/prefixes", false)))
	resp := h.post("/prefixes/"+prefixID, url.Values{
		"csrf_token": {h.csrfToken("/prefixes")},
		"cidr_text":  {"10.1.0.0/16"}, "vlan_id": {"9999"}, "role": {"kept"},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a refused network edit returned %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(page, "field-error") {
		t.Error("the refusal did not say which field was wrong")
	}
	row := editingRow(t, page)
	if !strings.Contains(row, `value="9999"`) {
		t.Errorf("the rejected row lost the VLAN that was typed: %s", row)
	}
	if !strings.Contains(row, `value="kept"`) {
		t.Errorf("the rejected row lost the role that was typed: %s", row)
	}
}

func TestReadOnlyUsersGetNoNetworkEditors(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]
	ifaceID := firstPortEditID(t, body(t, h.get("/assets/"+assetID, false)))
	prefixID := firstEditID(t, body(t, h.get("/prefixes", false)))

	h.logout()
	h.login("viewer", "viewer-password")
	for _, c := range []struct{ name, path, marker string }{
		{"port", "/assets/" + assetID + "?edit=" + ifaceID, `name="form_factor"`},
		{"network", "/prefixes?edit=" + prefixID, `name="cidr_text"`},
	} {
		if strings.Contains(body(t, h.get(c.path, false)), c.marker) {
			t.Errorf("a read-only user asking for the %s editor by id was given one", c.name)
		}
	}
}

// firstPortEditID returns the first PORT offered for editing.
//
// Matched on the actions-column button specifically. Scoping to the panel is
// not enough: an address's edit link sits inside the port's own row, earlier in
// the markup, so "the first ?edit= in the interfaces panel" is an address id
// and the port row never opens.
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

// Every editor in the application emits its version. The token is only worth
// anything if the form carries it, and submittedVersion falls back to the
// stored value when it is missing -- which is safe ONLY because this test fails
// when a form stops emitting one.
func TestEveryEditFormCarriesItsVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	serviceID := h.refs.Services["orders-api"]
	rabbit := h.refs.Services["rabbitmq"]
	assetPage := body(t, h.get("/assets/"+assetID, false))

	editors := []struct{ name, path string }{
		{"environment", "/environments?edit=" + h.refs.Environments["prod"]},
		{"network", "/prefixes?edit=" + firstEditID(t, body(t, h.get("/prefixes", false)))},
		{"port", "/assets/" + assetID + "?edit=" + firstPortEditID(t, assetPage)},
		{"cost line", "/assets/" + assetID + "?edit=" + firstCostFormID(t, assetPage)},
		{"placement", "/services/" + serviceID + "?edit=" +
			firstInstanceEditID(t, body(t, h.get("/services/"+serviceID, false)))},
		{"endpoint", "/services/" + rabbit + "?edit=" + h.refs.Endpoints["rabbitmq/amqp"]},
		{"team", "/teams/" + h.refs.Teams["platform"]},
	}
	for _, e := range editors {
		page := body(t, h.get(e.path, false))
		if !strings.Contains(page, `name="row_version"`) {
			t.Errorf("the %s editor renders no version, so its guard is inert", e.name)
			continue
		}
		// And not a zero: a zero matches no row and would refuse every save.
		if strings.Contains(page, `name="row_version" value="0"`) {
			t.Errorf("the %s editor renders version 0 — the column is missing from its query", e.name)
		}
	}
	addr, _ := firstAddressEditID(t, assetPage)
	if page := body(t, h.get("/assets/"+assetID+"?edit="+addr, false)); !strings.Contains(page, `name="row_version"`) {
		t.Error("the address editor renders no version, so its guard is inert")
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

	page := body(t, save(stale, "10.32.0.0/16"))
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
