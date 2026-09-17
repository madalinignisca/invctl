// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Task 5 of write-surface-gaps (docs/superpowers/plans/2026-09-17-write-
// surface-gaps.md) wires POST /{resource}/{id} and /{resource}/{id}/retire for
// the seven entities whose store methods (Tasks 3 and 4) shipped with no route
// reaching them: Aggregate, RIR, ASN, L2VPN, VLANGroup, BackendPool and Route.
// unreachableRepairPaths (internal/store/update_reachable_test.go) tracked the
// gap until this landed; these are the behavioural proof that landing is real.
//
// Each entity gets: a correction that succeeds, a correction that is refused
// at 422 with the form re-rendered, a stale row_version refused at 409, and --
// where the store's own guard makes one meaningful -- a withdrawal refused
// with a message the operator can read rather than a 500.

var adminPermit = domain.AdministratorPermit(domain.SystemActor)

// ---------- Aggregate ----------

func TestAggregateCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	agg, err := domain.NewAggregate(store.NewID(), "203.0.113.0/24")
	if err != nil {
		t.Fatalf("building aggregate: %v", err)
	}
	if err := h.store.CreateAggregate(ctx, adminPermit, agg); err != nil {
		t.Fatalf("seeding aggregate: %v", err)
	}

	resp := h.post("/allocations/"+agg.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"cidr_text":   {"203.0.113.0/25"},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting an aggregate returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT cidr_text FROM aggregate WHERE id = ?`, agg.ID); got != "203.0.113.0/25" {
		t.Errorf("cidr_text = %q, want 203.0.113.0/25", got)
	}
}

func TestAggregateCorrectionRefusesAnInvalidCIDR(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	agg, _ := domain.NewAggregate(store.NewID(), "203.0.114.0/24")
	if err := h.store.CreateAggregate(ctx, adminPermit, agg); err != nil {
		t.Fatalf("seeding aggregate: %v", err)
	}

	resp := h.post("/allocations/"+agg.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"cidr_text":   {"not-a-cidr"},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an invalid CIDR returned %d, want 422", resp.StatusCode)
	}
	if got := h.lookup(`SELECT cidr_text FROM aggregate WHERE id = ?`, agg.ID); got != "203.0.114.0/24" {
		t.Errorf("cidr_text changed to %q despite the refusal", got)
	}
	if !strings.Contains(body(t, resp), "allocations") {
		t.Error("the refusal did not re-render the allocations page")
	}
}

func TestAggregateCorrectionRefusesAStaleVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	agg, _ := domain.NewAggregate(store.NewID(), "203.0.115.0/24")
	if err := h.store.CreateAggregate(ctx, adminPermit, agg); err != nil {
		t.Fatalf("seeding aggregate: %v", err)
	}

	resp := h.post("/allocations/"+agg.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"cidr_text":   {"203.0.115.0/25"},
		"row_version": {"999"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stale row_version returned %d, want 409", resp.StatusCode)
	}
	if got := h.lookup(`SELECT cidr_text FROM aggregate WHERE id = ?`, agg.ID); got != "203.0.115.0/24" {
		t.Errorf("cidr_text changed to %q despite the stale token", got)
	}
}

// ---------- ASN ----------

func TestASNCanBeCorrectedIncludingTheNumberItself(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	asn, err := domain.NewASN(store.NewID(), 65500)
	if err != nil {
		t.Fatalf("building ASN: %v", err)
	}
	if err := h.store.CreateASN(ctx, adminPermit, asn); err != nil {
		t.Fatalf("seeding ASN: %v", err)
	}

	resp := h.post("/asn/"+asn.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"number":      {"65501"},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting an AS number returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT number FROM asn WHERE id = ?`, asn.ID); got != "65501" {
		t.Errorf("number = %q, want 65501 -- the roadmap's own gap (\"a mistyped AS "+
			"number is withdraw-and-redeclare\") is supposed to be closed", got)
	}
}

func TestASNCorrectionRefusesAnOutOfRangeNumber(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	asn, _ := domain.NewASN(store.NewID(), 65502)
	if err := h.store.CreateASN(ctx, adminPermit, asn); err != nil {
		t.Fatalf("seeding ASN: %v", err)
	}

	resp := h.post("/asn/"+asn.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"number":      {"0"},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("number 0 returned %d, want 422", resp.StatusCode)
	}
	if got := h.lookup(`SELECT number FROM asn WHERE id = ?`, asn.ID); got != "65502" {
		t.Errorf("number changed to %q despite the refusal", got)
	}
}

func TestASNCorrectionRefusesAStaleVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	asn, _ := domain.NewASN(store.NewID(), 65503)
	if err := h.store.CreateASN(ctx, adminPermit, asn); err != nil {
		t.Fatalf("seeding ASN: %v", err)
	}

	resp := h.post("/asn/"+asn.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"number":      {"65504"},
		"row_version": {"999"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stale row_version returned %d, want 409", resp.StatusCode)
	}
}

// ---------- L2VPN ----------

func firstOverlay(t *testing.T, h *harness) string {
	t.Helper()
	return h.lookup(`SELECT id FROM l2vpn WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
}

func TestL2VPNCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstOverlay(t, h)
	rv := h.lookup(`SELECT row_version FROM l2vpn WHERE id = ?`, id)

	resp := h.post("/overlays/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/overlays")},
		"name":        {"renamed-overlay"},
		"kind":        {h.lookup(`SELECT kind FROM l2vpn WHERE id = ?`, id)},
		"row_version": {rv},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting an overlay returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT name FROM l2vpn WHERE id = ?`, id); got != "renamed-overlay" {
		t.Errorf("name = %q, want renamed-overlay", got)
	}
}

func TestL2VPNCorrectionRefusesAnEmptyName(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstOverlay(t, h)
	rv := h.lookup(`SELECT row_version FROM l2vpn WHERE id = ?`, id)
	before := h.lookup(`SELECT name FROM l2vpn WHERE id = ?`, id)

	resp := h.post("/overlays/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/overlays")},
		"name":        {""},
		"kind":        {h.lookup(`SELECT kind FROM l2vpn WHERE id = ?`, id)},
		"row_version": {rv},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an empty name returned %d, want 422", resp.StatusCode)
	}
	if got := h.lookup(`SELECT name FROM l2vpn WHERE id = ?`, id); got != before {
		t.Errorf("name changed to %q despite the refusal", got)
	}
}

func TestL2VPNCorrectionRefusesAStaleVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstOverlay(t, h)

	resp := h.post("/overlays/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/overlays")},
		"name":        {"stale-attempt"},
		"kind":        {h.lookup(`SELECT kind FROM l2vpn WHERE id = ?`, id)},
		"row_version": {"999"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stale row_version returned %d, want 409", resp.StatusCode)
	}
}

// ---------- RIR ----------

func TestRIRCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	rir, err := domain.NewRIR(store.NewID(), "test-registry", false)
	if err != nil {
		t.Fatalf("building RIR: %v", err)
	}
	if err := h.store.CreateRIR(ctx, adminPermit, rir); err != nil {
		t.Fatalf("seeding RIR: %v", err)
	}

	resp := h.post("/rirs/"+rir.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"name":        {"renamed-registry"},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a registry returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT name FROM rir WHERE id = ?`, rir.ID); got != "renamed-registry" {
		t.Errorf("name = %q, want renamed-registry", got)
	}
}

func TestRIRCorrectionRefusesAnEmptyName(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	rir, _ := domain.NewRIR(store.NewID(), "another-registry", false)
	if err := h.store.CreateRIR(ctx, adminPermit, rir); err != nil {
		t.Fatalf("seeding RIR: %v", err)
	}

	resp := h.post("/rirs/"+rir.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"name":        {""},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an empty name returned %d, want 422", resp.StatusCode)
	}
}

func TestRIRCorrectionRefusesAStaleVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	rir, _ := domain.NewRIR(store.NewID(), "stale-registry", false)
	if err := h.store.CreateRIR(ctx, adminPermit, rir); err != nil {
		t.Fatalf("seeding RIR: %v", err)
	}

	resp := h.post("/rirs/"+rir.ID, url.Values{
		"csrf_token":  {h.csrfToken("/allocations")},
		"name":        {"whatever"},
		"row_version": {"999"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stale row_version returned %d, want 409", resp.StatusCode)
	}
}

// TestRIRWithdrawalRefusesWhileALiveAggregateNamesIt is the evidence gate's
// own example: RetireRIR refuses rather than orphaning the aggregate, and the
// operator has to be told why rather than seeing a 500.
func TestRIRWithdrawalRefusesWhileALiveAggregateNamesIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	rir, _ := domain.NewRIR(store.NewID(), "held-registry", false)
	if err := h.store.CreateRIR(ctx, adminPermit, rir); err != nil {
		t.Fatalf("seeding RIR: %v", err)
	}
	agg, _ := domain.NewAggregate(store.NewID(), "198.51.100.0/24")
	agg.RIRID = &rir.ID
	if err := h.store.CreateAggregate(ctx, adminPermit, agg); err != nil {
		t.Fatalf("seeding aggregate: %v", err)
	}

	resp := h.post("/rirs/"+rir.ID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/allocations")},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatal("withdrawing a registry with a live aggregate 500ed instead of refusing")
	}
	if got := h.lookup(`SELECT lifecycle FROM rir WHERE id = ?`, rir.ID); got != "active" {
		t.Errorf("lifecycle = %q, want active -- the withdrawal should have been refused", got)
	}
	// The refusal is a flash on the next full render, not the response body of
	// a redirect -- read it back off the page it sends the operator to.
	after := body(t, h.get("/allocations", false))
	if !strings.Contains(after, "live allocations") {
		t.Error("the refusal did not explain why, so the operator sees a button that " +
			"simply did not work")
	}
}

// ---------- VLANGroup ----------

func TestVLANGroupCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	grp, err := domain.NewVLANGroup(store.NewID(), "test-scope", nil)
	if err != nil {
		t.Fatalf("building VLAN group: %v", err)
	}
	if err := h.store.CreateVLANGroup(ctx, adminPermit, grp); err != nil {
		t.Fatalf("seeding VLAN group: %v", err)
	}

	resp := h.post("/vlan-groups/"+grp.ID, url.Values{
		"csrf_token":  {h.csrfToken("/vlans")},
		"name":        {"renamed-scope"},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a numbering scope returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT name FROM vlan_group WHERE id = ?`, grp.ID); got != "renamed-scope" {
		t.Errorf("name = %q, want renamed-scope", got)
	}
}

func TestVLANGroupCorrectionRefusesAnEmptyName(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	grp, _ := domain.NewVLANGroup(store.NewID(), "another-scope", nil)
	if err := h.store.CreateVLANGroup(ctx, adminPermit, grp); err != nil {
		t.Fatalf("seeding VLAN group: %v", err)
	}

	resp := h.post("/vlan-groups/"+grp.ID, url.Values{
		"csrf_token":  {h.csrfToken("/vlans")},
		"name":        {""},
		"row_version": {"1"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an empty name returned %d, want 422", resp.StatusCode)
	}
}

func TestVLANGroupCorrectionRefusesAStaleVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	grp, _ := domain.NewVLANGroup(store.NewID(), "stale-scope", nil)
	if err := h.store.CreateVLANGroup(ctx, adminPermit, grp); err != nil {
		t.Fatalf("seeding VLAN group: %v", err)
	}

	resp := h.post("/vlan-groups/"+grp.ID, url.Values{
		"csrf_token":  {h.csrfToken("/vlans")},
		"name":        {"whatever"},
		"row_version": {"999"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stale row_version returned %d, want 409", resp.StatusCode)
	}
}

// TestVLANGroupWithdrawalRefusesWhileALiveVLANNumbersWithinIt.
func TestVLANGroupWithdrawalRefusesWhileALiveVLANNumbersWithinIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	grp, _ := domain.NewVLANGroup(store.NewID(), "held-scope", nil)
	if err := h.store.CreateVLANGroup(ctx, adminPermit, grp); err != nil {
		t.Fatalf("seeding VLAN group: %v", err)
	}
	vlan, err := domain.NewVLAN(store.NewID(), 3900, "held-vlan", &grp.ID)
	if err != nil {
		t.Fatalf("building VLAN: %v", err)
	}
	if err := h.store.CreateVLAN(ctx, adminPermit, vlan); err != nil {
		t.Fatalf("seeding VLAN: %v", err)
	}

	resp := h.post("/vlan-groups/"+grp.ID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/vlans")},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatal("withdrawing a numbering scope with a live VLAN 500ed instead of refusing")
	}
	if got := h.lookup(`SELECT lifecycle FROM vlan_group WHERE id = ?`, grp.ID); got != "active" {
		t.Errorf("lifecycle = %q, want active -- the withdrawal should have been refused", got)
	}
	after := body(t, h.get("/vlans", false))
	if !strings.Contains(after, "live VLANs") {
		t.Error("the refusal did not explain why")
	}
}

// ---------- BackendPool ----------

// haproxyService returns the id of the seeded "haproxy-edge" service, which
// carries the seeded pool "orders-pool" and route "orders.example.com"
// (internal/seed/seed_services.go's routing phase).
func haproxyService(t *testing.T, h *harness) string {
	t.Helper()
	return h.lookup(`SELECT id FROM service WHERE code = 'haproxy-edge'`)
}

func TestBackendPoolCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	pool := h.lookup(`SELECT id FROM backend_pool WHERE service_id = ? AND name = 'orders-pool'`, svc)
	rv := h.lookup(`SELECT row_version FROM backend_pool WHERE id = ?`, pool)

	resp := h.post("/pools/"+pool, url.Values{
		"csrf_token":   {h.csrfToken("/services/" + svc)},
		"name":         {"orders-pool-renamed"},
		"lb_algorithm": {"leastconn"},
		"row_version":  {rv},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a pool returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT name FROM backend_pool WHERE id = ?`, pool); got != "orders-pool-renamed" {
		t.Errorf("name = %q, want orders-pool-renamed", got)
	}
	if got := h.lookup(`SELECT service_id FROM backend_pool WHERE id = ?`, pool); got != svc {
		t.Errorf("service_id changed to %q; UpdateBackendPool must pin it from the stored row", got)
	}
}

func TestBackendPoolCorrectionRefusesAnEmptyName(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	pool := h.lookup(`SELECT id FROM backend_pool WHERE service_id = ? AND name = 'orders-pool'`, svc)
	rv := h.lookup(`SELECT row_version FROM backend_pool WHERE id = ?`, pool)

	resp := h.post("/pools/"+pool, url.Values{
		"csrf_token":  {h.csrfToken("/services/" + svc)},
		"name":        {""},
		"row_version": {rv},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an empty name returned %d, want 422", resp.StatusCode)
	}
}

func TestBackendPoolCorrectionRefusesAStaleVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	pool := h.lookup(`SELECT id FROM backend_pool WHERE service_id = ? AND name = 'orders-pool'`, svc)

	resp := h.post("/pools/"+pool, url.Values{
		"csrf_token":  {h.csrfToken("/services/" + svc)},
		"name":        {"whatever"},
		"row_version": {"999"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stale row_version returned %d, want 409", resp.StatusCode)
	}
}

// TestBackendPoolWithdrawalRefusesWhileALiveRoutePointsAtIt. The seeded pool
// "orders-pool" is fronted by the seeded route "orders.example.com" -- see
// seed_services.go's routing phase.
func TestBackendPoolWithdrawalRefusesWhileALiveRoutePointsAtIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	pool := h.lookup(`SELECT id FROM backend_pool WHERE service_id = ? AND name = 'orders-pool'`, svc)

	resp := h.post("/pools/"+pool+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/services/" + svc)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatal("withdrawing a pool with a live route pointing at it 500ed instead of refusing")
	}
	if got := h.lookup(`SELECT lifecycle FROM backend_pool WHERE id = ?`, pool); got != "active" {
		t.Errorf("lifecycle = %q, want active -- the withdrawal should have been refused", got)
	}
	after := body(t, h.get("/services/"+svc, false))
	if !strings.Contains(after, "live route") {
		t.Error("the refusal did not explain why")
	}
}

// ---------- Route ----------

func TestRouteCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	route := h.lookup(`SELECT id FROM route WHERE match_value = 'orders.example.com'`)
	rv := h.lookup(`SELECT row_version FROM route WHERE id = ?`, route)

	resp := h.post("/routes/"+route, url.Values{
		"csrf_token":      {h.csrfToken("/services/" + svc)},
		"match_type":      {"host_header"},
		"match_value":     {"orders-v2.example.com"},
		"tls_termination": {"terminate"},
		"priority":        {"20"},
		"row_version":     {rv},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a route returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT match_value FROM route WHERE id = ?`, route); got != "orders-v2.example.com" {
		t.Errorf("match_value = %q, want orders-v2.example.com", got)
	}
	if got := h.lookup(`SELECT priority FROM route WHERE id = ?`, route); got != "20" {
		t.Errorf("priority = %q, want 20", got)
	}
}

// TestRouteCorrectionRefusesAnInvalidMatchType. Route.Validate checks
// match_type against domain.MatchTypes; a value outside the enum is caught
// before it reaches the CHECK constraint.
func TestRouteCorrectionRefusesAnInvalidMatchType(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	route := h.lookup(`SELECT id FROM route WHERE match_value = 'orders.example.com'`)
	rv := h.lookup(`SELECT row_version FROM route WHERE id = ?`, route)

	resp := h.post("/routes/"+route, url.Values{
		"csrf_token":  {h.csrfToken("/services/" + svc)},
		"match_type":  {"not-a-real-type"},
		"match_value": {"orders.example.com"},
		"row_version": {rv},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an invalid match_type returned %d, want 422", resp.StatusCode)
	}
	if got := h.lookup(`SELECT match_type FROM route WHERE id = ?`, route); got != "host_header" {
		t.Errorf("match_type changed to %q despite the refusal", got)
	}
}

func TestRouteCorrectionRefusesAStaleVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	route := h.lookup(`SELECT id FROM route WHERE match_value = 'orders.example.com'`)

	resp := h.post("/routes/"+route, url.Values{
		"csrf_token":  {h.csrfToken("/services/" + svc)},
		"match_type":  {"host_header"},
		"match_value": {"whatever.example.com"},
		"row_version": {"999"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a stale row_version returned %d, want 409", resp.StatusCode)
	}
}

// TestRouteWithdrawalRefusesWhileALiveDependencyResolvesThroughIt. The seeded
// dependency from "partner-gateway" resolves through the seeded route
// "orders.example.com" -- see seed_services.go's dependency specs.
func TestRouteWithdrawalRefusesWhileALiveDependencyResolvesThroughIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := haproxyService(t, h)
	route := h.lookup(`SELECT id FROM route WHERE match_value = 'orders.example.com'`)
	deps := h.lookup(`SELECT COUNT(*) FROM dependency WHERE provider_route_id = ? AND lifecycle = 'active'`, route)
	if deps == "0" {
		t.Fatal("no live dependency resolves through this route, so this test would pass by " +
			"checking nothing")
	}

	resp := h.post("/routes/"+route+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/services/" + svc)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatal("withdrawing a route with a live dependency resolving through it 500ed " +
			"instead of refusing")
	}
	if got := h.lookup(`SELECT lifecycle FROM route WHERE id = ?`, route); got != "active" {
		t.Errorf("lifecycle = %q, want active -- the withdrawal should have been refused", got)
	}
	after := body(t, h.get("/services/"+svc, false))
	if !strings.Contains(after, "live dependency") {
		t.Error("the refusal did not explain why")
	}
}

// ---------- observer boundary (one representative; the full sweep is
// TestEveryRegisteredWriteRouteRefusesAnObserver) ----------

func TestOnlyAWriterCanCorrectAnOverlay(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstOverlay(t, h)
	before := h.lookup(`SELECT name FROM l2vpn WHERE id = ?`, id)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/overlays/"+id, url.Values{
		"csrf_token":  {viewer.csrfToken("/overlays")},
		"name":        {"taken-over"},
		"kind":        {h.lookup(`SELECT kind FROM l2vpn WHERE id = ?`, id)},
		"row_version": {h.lookup(`SELECT row_version FROM l2vpn WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("an Observer corrected an overlay")
	}
	if got := h.lookup(`SELECT name FROM l2vpn WHERE id = ?`, id); got != before {
		t.Errorf("name went %q -> %q despite the refusal", before, got)
	}
}
