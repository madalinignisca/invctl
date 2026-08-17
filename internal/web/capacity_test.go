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

// TestTheCapacityPanelRendersAndSaysWhatItCannotSee.
//
// The panel is fetched rather than trusted to compile, for the reason the
// circuit impact page taught this codebase: a template that references a field
// the page struct does not carry returns 500 for every request while every
// other test stays green.
func TestTheCapacityPanelRendersAndSaysWhatItCannotSee(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	clusters := body(t, h.get("/clusters", false))
	id := firstClusterID(t, clusters)
	page := body(t, h.get("/clusters/"+id, false))

	if !strings.Contains(page, "Capacity") {
		t.Fatal("the cluster page has no capacity panel")
	}
	if !strings.Contains(page, "Usable vCPU") {
		t.Error("no usable vCPU figure")
	}
	// The base fixture declares no sizes at all, so the panel must lead with
	// what it could not see rather than reporting a confident zero.
	if !strings.Contains(page, "floor, not a total") {
		t.Error("an unmeasured cluster does not warn that its figures are a floor")
	}
}

// firstClusterID pulls a cluster id off the list page.
func firstClusterID(t *testing.T, page string) string {
	t.Helper()
	const marker = `href="/clusters/`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatal("no cluster on the list page")
	}
	rest := page[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatal("malformed cluster link")
	}
	return rest[:j]
}

// TestAnOperatorCanRecordHowBigAHostIs.
//
// THE HALF THAT WAS MISSING. J3 shipped the columns, the arithmetic and the
// panel that renders them, and no form anywhere set a single one of the six
// numbers -- every capacity figure in the product could only come from the
// seed. A feature whose inputs cannot be entered is a demo, so this asserts the
// round trip rather than the field: typed into the form, out of the panel.
func TestAnOperatorCanRecordHowBigAHostIs(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.refs.Assets["hv-01"]

	form := body(t, h.get("/assets/"+id+"?edit="+id, false))
	if !strings.Contains(form, `name="cpu_cores"`) {
		t.Fatal("the asset form cannot record how many cores a hypervisor has")
	}
	if !strings.Contains(form, `name="vcpu_allocated"`) {
		t.Error("the asset form cannot record what a workload has been allocated")
	}

	resp := h.post("/assets/"+id, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"name":       {"hv-01"}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		"cpu_cores": {"48"}, "memory_mb": {"393216"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recording a size returned %d, want a redirect", resp.StatusCode)
	}

	if page := body(t, h.get("/assets/"+id+"?edit="+id, false)); !strings.Contains(page, `value="48"`) {
		t.Error("the recorded core count does not come back on the form")
	}
	// Declared state, so it owes an audit entry like everything else here.
	if !strings.Contains(body(t, h.get("/changes", false)), "cpu_cores") {
		t.Error("sizing a host wrote no change_log entry naming the field")
	}
}

// TestAFieldTheFormDidNotCarryIsNotCleared.
//
// The capacity inputs are conditional on the kind, so the ordinary asset form
// for a switch carries none of them. If that read as "cleared", editing a
// hypervisor through any variant without the section would silently drop its
// size -- and the loss would surface as an unmeasured host, which looks like
// missing data entry rather than a bug. numbers.sub exists for this.
func TestAFieldTheFormDidNotCarryIsNotCleared(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.refs.Assets["hv-01"]

	sized := url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"name":       {"hv-01"}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		"cpu_cores": {"48"},
	}
	h.post("/assets/"+id, sized, false).Body.Close()

	// The same asset corrected by a form with no capacity section at all.
	h.post("/assets/"+id, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"name":       {"hv-01"}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		"vendor": {"Supermicro"},
	}, false).Body.Close()

	page := body(t, h.get("/assets/"+id+"?edit="+id, false))
	if !strings.Contains(page, `value="48"`) {
		t.Error("an edit that never mentioned the core count erased it")
	}
}

// TestAnOperatorCanRecordWhatAnEngagementWasPricedOn.
//
// The CEO's alert is unusable until somebody can enter the number it compares
// against, and this is also the regression guard for the way the project
// handler is built: ProjectUpdate rebuilds the row through NewProject, so any
// field the SPEC does not carry is nil by the time the store sees it. On the
// day these columns landed, correcting a project's name silently erased what
// it was priced for.
func TestAnOperatorCanRecordWhatAnEngagementWasPricedOn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.refs.Projects["platform"]

	form := body(t, h.get("/projects/"+id+"?edit="+id, false))
	if !strings.Contains(form, `name="priced_for_vcpu"`) {
		t.Fatal("the project form cannot record what the engagement was priced on")
	}

	save := func(extra url.Values) {
		t.Helper()
		v := url.Values{
			"csrf_token": {h.csrfToken("/projects/" + id)},
			"code":       {"platform"}, "name": {"Platform"}, "lifecycle": {"active"},
		}
		for k, vals := range extra {
			v[k] = vals
		}
		resp := h.post("/projects/"+id, v, false)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			t.Fatalf("saving the project returned %d", resp.StatusCode)
		}
	}

	save(url.Values{"priced_for_vcpu": {"24"}, "priced_for_memory_mb": {"81920"}})
	if page := body(t, h.get("/projects/"+id+"?edit="+id, false)); !strings.Contains(page, `value="24"`) {
		t.Fatal("the priced-for figure does not come back on the form")
	}

	// A correction that never mentions the figure must not erase it.
	save(url.Values{"name": {"Platform services"}})
	if page := body(t, h.get("/projects/"+id+"?edit="+id, false)); !strings.Contains(page, `value="24"`) {
		t.Error("renaming a project erased what it was priced for")
	}
}

// TestAnOperatorCanDeclareTheOvercommitRatio.
//
// The panel already reported the ratio and said "undeclared, so 1:1" for every
// cluster in the product, because nothing could declare one. It is also the
// one capacity number written differently from how it is stored -- 3:1 is 300
// hundredths -- so the round trip is what matters: what the field offers back
// must be what it accepts, or saving a cluster without touching the ratio
// would refuse.
func TestAnOperatorCanDeclareTheOvercommitRatio(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstClusterID(t, body(t, h.get("/clusters", false)))

	save := func(ratio string) *http.Response {
		t.Helper()
		return h.post("/clusters/"+id, url.Values{
			"csrf_token": {h.csrfToken("/clusters/" + id)},
			"name":       {"prod-virt"}, "kind": {"proxmox"}, "ha_policy": {"restart"},
			"min_hosts": {"2"}, "cpu_overcommit": {ratio},
		}, false)
	}

	resp := save("2.5")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring an overcommit ratio returned %d", resp.StatusCode)
	}

	page := body(t, h.get("/clusters/"+id, false))
	if !strings.Contains(page, `value="2.5"`) {
		t.Error("the declared ratio does not come back on the form as it was typed")
	}
	// Stored as hundredths and rendered as prose: 250 reads as 2.5:1, never as
	// "250:100", which is what the panel printed before anything could set it.
	if !strings.Contains(page, "2.5:1") {
		t.Error("the capacity panel does not report the declared ratio")
	}

	// Refused, never silently dropped: a ratio that quietly became nothing
	// would re-read the whole cluster at a conservative 1:1 and report it as
	// healthier than it is.
	bad := save("three")
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a ratio that is not a number returned %d, want 422", bad.StatusCode)
	}
}

// TestTheClusterPageSaysWhoHoldsIt.
//
// The panel is fetched rather than trusted to compile, for the reason the
// circuit impact page taught this codebase: a template referencing a field the
// page struct does not carry returns 500 for every request while every other
// test stays green.
func TestTheClusterPageSaysWhoHoldsIt(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	id := firstClusterID(t, body(t, h.get("/clusters", false)))
	page := body(t, h.get("/clusters/"+id, false))

	if !strings.Contains(page, "Who holds this cluster") {
		t.Fatal("the cluster page does not divide the cluster between its claimants")
	}
	// Both dimensions, because the whole point is that they differ.
	for _, want := range []string{">CPU<", ">Memory<"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %s division on the page", want)
		}
	}
	// The basis is on the page, not only in the database. A percentage whose
	// meaning is not stated is one somebody will read as usage.
	if !strings.Contains(page, "allocated") {
		t.Error("the page does not say the shares are computed on allocation")
	}
	// The idle slice is asserted where this test can create the data for it:
	// see the pool page below. The base fixture allocates nothing, so this
	// cluster correctly reports that there is nothing to divide, and asserting
	// headroom here would only prove the fixture was seeded.
	if !strings.Contains(page, "Nothing to divide") && !strings.Contains(page, "idle capacity") {
		t.Error("the panel neither divides the cluster nor says why it cannot")
	}
}

// TestAnOperatorCanRecordWhatAWorkloadHolds. J3's storage half, which shipped
// as arithmetic with no way to enter a number -- the same gap J7 closed for
// compute, closed here before the feature could repeat it.
func TestAnOperatorCanRecordWhatAWorkloadHolds(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	vm := h.refs.Assets["vm-app-1"]
	pool := h.refs.Assets["ceph-block"]
	if pool == "" {
		t.Skip("the base fixture has no storage pool")
	}

	page := body(t, h.get("/assets/"+vm, false))
	if !strings.Contains(page, `name="allocated_gb"`) {
		t.Fatal("the asset page cannot record what a workload holds")
	}

	resp := h.post("/assets/"+vm+"/storage", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + vm)},
		"pool_id":    {pool}, "allocated_gb": {"275"}, "note": {"test claim"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recording a claim returned %d, want a redirect", resp.StatusCode)
	}
	if after := body(t, h.get("/assets/"+vm, false)); !strings.Contains(after, "275 GB") {
		t.Error("the recorded claim does not appear on the workload's page")
	}
	// Declared state, so it owes an audit entry.
	if !strings.Contains(body(t, h.get("/changes", false)), "275 GB") {
		t.Error("recording what a workload holds wrote no change_log entry")
	}
	// And the pool's own page divides it.
	poolPage := body(t, h.get("/assets/"+pool, false))
	if !strings.Contains(poolPage, "This pool") {
		t.Fatal("a storage pool's page does not report its capacity")
	}
	if !strings.Contains(poolPage, "Lost to redundancy") {
		t.Error("the pool does not say what replication costs it")
	}
	// §5.3 made visible: headroom is a slice, so a reader can see the shares
	// sum to the whole pool rather than to whatever was claimed.
	if !strings.Contains(poolPage, "idle capacity") {
		t.Error("free space is not shown as its own slice, so the slices do not " +
			"visibly account for the pool")
	}
}

// TestTheClusterPageSaysWhatTheShareCosts.
//
// The money panel, fetched rather than trusted to compile. It also asserts the
// two figures stay APART: cost.go is emphatic that folding a one-off into a
// monthly run rate is a lie, and a panel that added them into one number would
// undo that quietly.
func TestTheClusterPageSaysWhatTheShareCosts(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstClusterID(t, body(t, h.get("/clusters", false)))

	// Declare the split, which is what makes the money divisible at all.
	resp := h.post("/clusters/"+id, url.Values{
		"csrf_token": {h.csrfToken("/clusters/" + id)},
		"name":       {"prod-virt"}, "kind": {"proxmox"}, "ha_policy": {"restart"},
		"min_hosts": {"2"}, "cost_split_cpu": {"60"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring the cost split returned %d", resp.StatusCode)
	}

	page := body(t, h.get("/clusters/"+id, false))
	if !strings.Contains(page, "What that share costs") {
		t.Fatal("the cluster page does not divide its cost")
	}
	for _, want := range []string{"Run rate", "Capital, spread"} {
		if !strings.Contains(page, want) {
			t.Errorf("%q is missing: run rate and capital must stay apart", want)
		}
	}
	if strings.Contains(page, "Not divided") {
		t.Error("the split was declared and the page still refuses to divide")
	}
	if !strings.Contains(page, `value="60"`) {
		t.Error("the declared split does not come back on the form")
	}
	// THE SHARES ARE RENDERED AS PERCENTAGES, not as the basis points they are
	// stored in. The first version printed 1940 under a column headed "blended"
	// on a page saying 19.4% a few inches above -- a reader takes that at face
	// value and is wrong by a hundred times.
	//
	// Asserted on the CELL rather than on the absence of a particular number: a
	// negative assertion passes whenever the fixture's figures happen to differ,
	// which is exactly what it did when it was first written.
	panel := page[strings.Index(page, "What that share costs"):]
	if end := strings.Index(panel, "</table>"); end > 0 {
		panel = panel[:end]
	}
	// All THREE share columns -- CPU, memory and the blend they produce. Testing
	// that "a percentage appears somewhere" passes while two of the three have
	// regressed, which is what the previous version of this assertion did.
	if n := strings.Count(panel, `class="num">0%<`); n != 3 {
		t.Errorf("%d of the 3 share columns render as percentages; a basis-point "+
			"integer under a column headed Blended reads as a percentage and is "+
			"wrong by a hundred times:\n%s", n, panel)
	}
}

// TestWithoutASplitThePageSaysSoRatherThanGuessing. Half and half is not
// cautious, it is arbitrary — so the page says what is missing.
func TestWithoutASplitThePageSaysSoRatherThanGuessing(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstClusterID(t, body(t, h.get("/clusters", false)))

	page := body(t, h.get("/clusters/"+id, false))
	if !strings.Contains(page, "Not divided") {
		t.Error("a cluster with no declared split does not say its cost cannot " +
			"be divided")
	}
	// And it offers the field that fixes it.
	if !strings.Contains(page, `name="cost_split_cpu"`) {
		t.Error("the page reports the gap and offers no way to close it")
	}
}

// TestAnOperatorCanDeclareWhoSharesAMachine.
//
// J5's whole input surface. The shares are set together in one form because
// they only mean anything together: editing one without seeing the others is
// how a total stops being 100 without anybody deciding it should.
func TestAnOperatorCanDeclareWhoSharesAMachine(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	vm := h.refs.Assets["vm-app-1"]
	platform, orders := h.refs.Projects["platform"], h.refs.Projects["orders"]

	page := body(t, h.get("/assets/"+vm, false))
	if !strings.Contains(page, "Shared with") {
		t.Fatal("the asset page cannot record who shares a machine")
	}

	resp := h.post("/assets/"+vm+"/occupants", url.Values{
		"csrf_token":          {h.csrfToken("/assets/" + vm)},
		"project_id":          {platform, orders},
		"percent_" + platform: {"60"},
		"percent_" + orders:   {"40"},
		"note_" + platform:    {"agreed at the March review"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring occupancy returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/assets/"+vm, false))
	for _, want := range []string{"60%", "40%", "agreed at the March review"} {
		if !strings.Contains(after, want) {
			t.Errorf("the declared occupancy does not show %q", want)
		}
	}
	// Declared state moves money, so it owes an audit entry.
	if !strings.Contains(body(t, h.get("/changes", false)), "60%") {
		t.Error("declaring who shares a machine wrote no change_log entry")
	}
}

// TestAnUnbalancedOccupancySaysSoOnThePage. §5.4: a finding, not a silent
// rounding — and the page is where somebody sees it.
func TestAnUnbalancedOccupancySaysSoOnThePage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	vm := h.refs.Assets["vm-app-1"]
	platform := h.refs.Projects["platform"]

	resp := h.post("/assets/"+vm+"/occupants", url.Values{
		"csrf_token":          {h.csrfToken("/assets/" + vm)},
		"project_id":          {platform},
		"percent_" + platform: {"70"},
	}, false)
	resp.Body.Close()

	page := body(t, h.get("/assets/"+vm, false))
	if !strings.Contains(page, "attributed to nobody") {
		t.Error("a 70% occupancy does not say the rest reaches nobody")
	}
	// And the dashboard carries it, so it is not only visible to whoever
	// happens to open this one asset.
	if !strings.Contains(body(t, h.get("/", false)),
		"occupants do not total 100%") {
		t.Error("the estate findings do not mention the unbalanced occupancy")
	}
}

// TestAConsumerPickerOffersOnlyThingsThatRunSoftware.
//
// A bridge is a child of its hypervisor and cannot run the software a licence
// covers. Offering it as a consumer is an invitation to a wrong answer, and the
// kind lookup already answers this question everywhere capacity is counted.
//
// The scoped line is CREATED HERE rather than assumed: the base fixture carries
// none, so the first version of this test found no picker, returned early and
// passed against the very bug it was written for.
func TestAConsumerPickerOffersOnlyThingsThatRunSoftware(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	hv := h.refs.Assets["hv-01"]

	page := body(t, h.get("/assets/"+hv, false))
	if !strings.Contains(page, "hv-01-br0") {
		t.Fatal("the fixture has no bridge under hv-01, so this proves nothing")
	}

	resp := h.post("/assets/"+hv+"/costs", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + hv)},
		"kind":       {"licence"}, "period": {"yearly"}, "amount": {"7800"},
		"applies_to": {"conditional"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("adding a scoped cost returned %d", resp.StatusCode)
	}

	page = body(t, h.get("/assets/"+hv, false))
	i := strings.Index(page, "Set who it covers")
	if i < 0 {
		t.Fatal("a conditional cost line offers no way to say who it covers")
	}
	form := page[:i]
	if j := strings.LastIndex(form, "<form"); j >= 0 {
		form = form[j:]
	}
	if !strings.Contains(form, "vm-") {
		t.Error("the consumer picker offers no workloads at all")
	}
	if strings.Contains(form, "hv-01-br0") {
		t.Error("the consumer picker offers a bridge, which runs no software " +
			"and can hold no licence")
	}
}
