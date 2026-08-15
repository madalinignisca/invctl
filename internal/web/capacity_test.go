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
