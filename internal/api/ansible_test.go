// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// TestAnsibleViewPagesInternallyPastASinglePage is the test the controller
// ruling in the task brief demands: buildAnsibleInventory alone cannot prove
// the handler doesn't just fetch "with a large limit" and silently truncate
// an estate bigger than one page, because that bug lives in fetchAllAssets,
// not in the pure function. f.api.PageSize is set to 2 for this fixture's own
// *API instance (New() built it fresh, so no other test is affected) so a
// small, fast fixture (5 hosts plus the fixture's own vm-db-1 = 6 rows) still
// forces FOUR round trips through the real keyset cursor: three full pages
// of 2 (2 + 2 + 2 = 6, and a full page cannot tell it is the last one) plus
// one final fetch that comes back empty and is what actually ends the loop --
// exactly what an external client paginating /api/v1/assets would have to do.
func TestAnsibleViewPagesInternallyPastASinglePage(t *testing.T) {
	f := newAPIHandlerFixture(t)
	f.api.PageSize = 2

	const hostCount = 5
	want := make([]string, 0, hostCount+1) // +1 for vm-db-1 already in the fixture
	want = append(want, "vm-db-1")
	for i := 0; i < hostCount; i++ {
		name := fmt.Sprintf("vm-page-%02d", i)
		id := f.mustAsset("vm", name, nil, "prod")
		f.mustInterfaceAndAddress(id, "eth0", fmt.Sprintf("10.9.0.%d", i+1))
		want = append(want, name)
	}
	sort.Strings(want)

	rec := f.serve(f.api.Ansible, "GET", "/api/v1/ansible", tokProd)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Meta struct {
			HostVars map[string]map[string]string `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	got := make([]string, 0, len(body.Meta.HostVars))
	for h := range body.Meta.HostVars {
		got = append(got, h)
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("got %d hosts, want %d: got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got hosts %v, want %v (each host must appear exactly once)", got, want)
		}
	}
}

// TestAnsibleViewIsScopedLikeEveryOtherRoute pins that the view uses the
// reader's own scope from context, the same AllowsAll predicate every other
// collection uses: a prod-only token must not see dev-box or sw-core-1 (which
// straddles prod and dev).
func TestAnsibleViewIsScopedLikeEveryOtherRoute(t *testing.T) {
	f := newAPIHandlerFixture(t)

	rec := f.serve(f.api.Ansible, "GET", "/api/v1/ansible", tokProd)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Meta struct {
			HostVars map[string]map[string]string `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if _, ok := body.Meta.HostVars["vm-db-1"]; !ok {
		t.Fatalf("missing an in-scope host: got %+v", body.Meta.HostVars)
	}
	if _, ok := body.Meta.HostVars["dev-box"]; ok {
		t.Fatal("a prod-only token saw dev-box, which is out of its scope")
	}
	if _, ok := body.Meta.HostVars["sw-core-1"]; ok {
		t.Fatal("sw-core-1 is a switch (not a host kind) and also straddles dev: it must not appear")
	}
}

// TestAnsibleHandlerWithNoReaderInContextIsA500NotAnEmptyScope mirrors
// TestAHandlerWithNoReaderInContextIsA500NotAnEmptyScope in
// handlers_test.go for this route specifically: a route mounted without
// RequireReader must fail closed, never publish an invented empty-scope
// inventory.
func TestAnsibleHandlerWithNoReaderInContextIsA500NotAnEmptyScope(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ansible", nil)
	f.api.Ansible(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500: a missing reader must never fall back to an empty (or any) scope", rec.Code)
	}
}

// TestAnsibleHandlerRejectsAnUnknownQueryParameter pins the route as
// unpaginated on the wire: it defines no query parameters at all, so
// anything supplied is unknown.
func TestAnsibleHandlerRejectsAnUnknownQueryParameter(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serve(f.api.Ansible, "GET", "/api/v1/ansible?limit=10", tokProd)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: /api/v1/ansible is unpaginated and takes no query parameters", rec.Code)
	}
}

// TestAnsibleViewOmitsANonActiveAsset pins docs/api-design.md §4's narrower
// rule for this view: only lifecycle = 'active' appears. This is stricter
// than the collections' default, which merely excludes 'retired' -- so a
// 'planned' asset must be visible on /api/v1/assets (it is not retired) but
// absent from /api/v1/ansible (it is not yet active, and has nothing to
// connect to).
func TestAnsibleViewOmitsANonActiveAsset(t *testing.T) {
	f := newAPIHandlerFixture(t)

	planned, err := domain.NewAsset(store.NewID(), domain.KindVM, "vm-planned", nil, f.s.Now())
	if err != nil {
		t.Fatalf("building asset: %v", err)
	}
	planned.Lifecycle = domain.LifecyclePlanned
	if err := f.s.CreateAsset(f.ctx, domain.SystemActor, planned, []string{f.envs["prod"]}); err != nil {
		t.Fatalf("creating planned asset: %v", err)
	}
	f.mustInterfaceAndAddress(planned.ID, "eth0", "10.9.9.9")

	// Sanity: the general collection includes it, because it excludes only
	// 'retired', not everything short of 'active'.
	listRec := f.serve(f.api.ListAssets, "GET", "/api/v1/assets", tokProd)
	if listRec.Code != http.StatusOK {
		t.Fatalf("listing assets: got %d, want 200: %s", listRec.Code, listRec.Body.String())
	}
	var page Page[Asset]
	if err := json.Unmarshal(listRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decoding assets page: %v", err)
	}
	foundInList := false
	for _, a := range page.Data {
		if a.Name == "vm-planned" {
			foundInList = true
		}
	}
	if !foundInList {
		t.Fatalf("a planned (non-retired) asset must appear on /api/v1/assets: got %+v", page.Data)
	}

	rec := f.serve(f.api.Ansible, "GET", "/api/v1/ansible", tokProd)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Meta struct {
			HostVars map[string]map[string]string `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if _, ok := body.Meta.HostVars["vm-planned"]; ok {
		t.Fatalf("a planned (not yet active) asset must not appear in the ansible view: got %+v", body.Meta.HostVars)
	}
	if _, ok := body.Meta.HostVars["vm-db-1"]; !ok {
		t.Fatalf("the fixture's active asset must still appear: got %+v", body.Meta.HostVars)
	}
}

// ---------------------------------------------------------------------------
// Ruling AG: TestAGroupNameCollisionIsRefused (api_test.go) calls
// buildAnsibleInventory directly and inspects the Go error value. Nothing
// there drives a real collision through the actual handler, so neither half
// of docs/api-design.md §4's promise -- "the client learns nothing, the
// operator learns everything" -- was proven at the boundary that matters.
// This is the third time on this branch a control justified in prose turned
// out to have no test guarding it (the scope-miss security event went
// unguarded for a whole review round; a uniform fixture silently disarmed a
// security predicate for three more). The two halves are separate claims and
// each can break without the other, so both are asserted here, separately.
// ---------------------------------------------------------------------------

// TestAGroupNameCollisionThroughTheRealHandlerIs500WithNothingLeakedAndBothNamesLogged
// mirrors the pure-function fixture in api_test.go's TestAGroupNameCollisionIsRefused
// (billing-api vs billing_api), but drives it through (*API).Ansible over a
// real store and a real reader, and checks both the client-facing response
// and the operator-facing log line.
func TestAGroupNameCollisionThroughTheRealHandlerIs500WithNothingLeakedAndBothNamesLogged(t *testing.T) {
	f := newAPIHandlerFixture(t)
	buf := captureSecurityLog(t)

	// f.serviceID is already "billing-api" in prod, placed on f.assetProdVM
	// (vm-db-1), which has an address -- so it is a real host in this view.
	// A second, in-scope, addressable host carries a service whose CODE
	// sanitises to the same group name: "billing_api" -> "svc_billing_api",
	// exactly as "billing-api" does.
	collidingHost := f.mustAsset(domain.KindVM, "vm-collide", nil, "prod")
	f.mustInterfaceAndAddress(collidingHost, "eth0", "10.9.5.5")
	f.mustService("billing_api", "prod", collidingHost)

	rec := f.serve(f.api.Ansible, "GET", "/api/v1/ansible", tokProd)

	// --- Client-facing half: a generic 500, and neither source name anywhere
	// in the body a caller can read. ---
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500: a group-name collision must be refused, not silently merged", rec.Code)
	}
	const wantBody = `{"error":"internal error"}`
	if rec.Body.String() != wantBody {
		t.Fatalf("got body %q, want the generic %q: a collision must not leak which two names collided", rec.Body.String(), wantBody)
	}
	if strings.Contains(rec.Body.String(), "billing-api") || strings.Contains(rec.Body.String(), "billing_api") {
		t.Fatalf("a colliding source name leaked into the client-facing response: %s", rec.Body.String())
	}

	// --- Operator-facing half: both source names appear in the server log,
	// which is the entire compensation for the client learning nothing. ---
	logged := buf.String()
	if !strings.Contains(logged, "billing-api") {
		t.Fatalf("the server log does not name the first colliding source (billing-api): %s", logged)
	}
	if !strings.Contains(logged, "billing_api") {
		t.Fatalf("the server log does not name the second colliding source (billing_api): %s", logged)
	}
}

// ---------------------------------------------------------------------------
// Two assets sharing a host name (final review, B2). asset.name is unique only
// among LIVE SIBLINGS, so the same name under two different hypervisors is
// legal and common -- and Ansible keys hostvars by name, so the view used to
// merge them, last writer wins. A playbook targeting env_dev would resolve
// vm-app-1 to the PRODUCTION box's address and connect to it, successfully,
// with nothing anywhere reporting an error.
//
// Asserted the way the group-name collision is asserted, because it is the
// same failure one level down: both halves separately, through the REAL
// handler. The client must learn nothing; the operator must learn everything.
// ---------------------------------------------------------------------------

func TestTwoAssetsSharingAHostNameAre500WithNothingLeakedAndBothIDsLogged(t *testing.T) {
	f := newAPIHandlerFixture(t)
	buf := captureSecurityLog(t)

	// Two hypervisors, so the two VMs below are not siblings and the name is
	// legal. Neither has an address, so neither is itself a host in this view.
	hvA := f.mustAsset(domain.KindHypervisor, "hv-a", nil, "prod")
	hvB := f.mustAsset(domain.KindHypervisor, "hv-b", nil, "prod")

	first := f.mustAsset(domain.KindVM, "vm-app-1", &hvA, "prod")
	f.mustInterfaceAndAddress(first, "eth0", "10.7.0.1")
	second := f.mustAsset(domain.KindVM, "vm-app-1", &hvB, "prod")
	f.mustInterfaceAndAddress(second, "eth0", "10.7.0.2")

	rec := f.serve(f.api.Ansible, "GET", "/api/v1/ansible", tokProd)

	// --- Client-facing half. ---
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500: two assets sharing a host name must be refused, not merged", rec.Code)
	}
	const wantBody = `{"error":"internal error"}`
	if rec.Body.String() != wantBody {
		t.Fatalf("got body %q, want the generic %q", rec.Body.String(), wantBody)
	}
	if strings.Contains(rec.Body.String(), first) || strings.Contains(rec.Body.String(), second) {
		t.Fatalf("an asset id leaked into the client-facing response: %s", rec.Body.String())
	}

	// --- Operator-facing half: BOTH ids, which is the entire compensation for
	// the client learning nothing. Naming one of them would leave an operator
	// unable to tell which two rows to reconcile. ---
	logged := buf.String()
	if !strings.Contains(logged, first) {
		t.Fatalf("the server log does not name the first asset id (%s): %s", first, logged)
	}
	if !strings.Contains(logged, second) {
		t.Fatalf("the server log does not name the second asset id (%s): %s", second, logged)
	}
}

// TestOneAssetPerHostNameStillBuilds is the negative half: the guard must
// refuse a genuine conflict and nothing else. Two DIFFERENTLY named VMs under
// two hypervisors are an ordinary estate and produce an ordinary inventory.
func TestOneAssetPerHostNameStillBuilds(t *testing.T) {
	f := newAPIHandlerFixture(t)

	hvA := f.mustAsset(domain.KindHypervisor, "hv-a", nil, "prod")
	hvB := f.mustAsset(domain.KindHypervisor, "hv-b", nil, "prod")
	a := f.mustAsset(domain.KindVM, "vm-app-1", &hvA, "prod")
	f.mustInterfaceAndAddress(a, "eth0", "10.7.0.1")
	b := f.mustAsset(domain.KindVM, "vm-app-2", &hvB, "prod")
	f.mustInterfaceAndAddress(b, "eth0", "10.7.0.2")

	rec := f.serve(f.api.Ansible, "GET", "/api/v1/ansible", tokProd)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Meta struct {
			HostVars map[string]map[string]string `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	got := make([]string, 0, len(body.Meta.HostVars))
	for h := range body.Meta.HostVars {
		got = append(got, h)
	}
	sort.Strings(got)
	want := []string{"vm-app-1", "vm-app-2", "vm-db-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got hosts %v, want %v", got, want)
	}
}

// TestASharedHostNameIsRefusedByThePureFunctionNamingBothIDs pins the error
// value itself, which the handler test above can only observe through a log
// line. Both ids, and the name, because an operator reconciling this needs to
// know which two rows are involved.
func TestASharedHostNameIsRefusedByThePureFunctionNamingBothIDs(t *testing.T) {
	_, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "01924e5a-0000-7000-8000-00000000000a", Name: "vm-app-1", Kind: "vm",
			Addresses: []string{"10.0.0.1"}, Environments: []string{"prod"}},
		{ID: "01924e5a-0000-7000-8000-00000000000b", Name: "vm-app-1", Kind: "vm",
			Addresses: []string{"10.0.0.2"}, Environments: []string{"dev"}},
	})
	if err == nil {
		t.Fatal("two assets sharing a host name must be refused, not merged")
	}
	for _, want := range []string{"vm-app-1",
		"01924e5a-0000-7000-8000-00000000000a", "01924e5a-0000-7000-8000-00000000000b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// TestANonHostAssetNeverConflictsOverAName is the boundary of the guard: the
// check runs after the host filter, so a switch (not an Ansible-connectable
// kind) sharing a VM's name is not a conflict -- it never appears in this
// document at all, so nothing can be merged with it.
func TestANonHostAssetNeverConflictsOverAName(t *testing.T) {
	inv, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "shared-name", Kind: "switch",
			Addresses: []string{"10.0.0.1"}, Environments: []string{"prod"}},
		{ID: "b", Name: "shared-name", Kind: "vm",
			Addresses: []string{"10.0.0.2"}, Environments: []string{"prod"}},
	})
	if err != nil {
		t.Fatalf("a switch sharing a VM's name is not a host conflict: %v", err)
	}
	want := map[string]string{"invctl_id": "b", "invctl_kind": "vm", "ansible_host": "10.0.0.2"}
	if !reflect.DeepEqual(inv.Meta.HostVars["shared-name"], want) {
		t.Fatalf("got hostvars %+v, want %+v", inv.Meta.HostVars["shared-name"], want)
	}
}

// ---------------------------------------------------------------------------
// API.PageSize (final review, C3). The field is exported and had no floor or
// ceiling, and BOTH ends silently truncated this view -- the exact failure
// this file's banner comment forbids.
// ---------------------------------------------------------------------------

func TestTheAnsibleRoundTripSizeIsClamped(t *testing.T) {
	cases := []struct {
		name     string
		pageSize int
		want     int
		why      string
	}{
		{"the zero value", 0, MaxLimit,
			"&api.API{Store: st} built without New() asked the store for 0; nextCursor(rows, 0) is always nil, so the loop ended after one page"},
		{"negative", -1, MaxLimit, "same shape as the zero value"},
		{"above the ceiling", MaxLimit + 1, MaxLimit,
			"the store clamps to MaxLimit and returns it, which nextCursor then reads as a short page and stops"},
		{"at the ceiling", MaxLimit, MaxLimit, "already legal"},
		{"a small size a test sets", 2, 2, "a fixture forcing several round trips must keep the size it asked for"},
		{"one", 1, 1, "the smallest usable size still pages"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &API{PageSize: c.pageSize}
			if got := a.pageSize(); got != c.want {
				t.Fatalf("PageSize=%d gave %d, want %d -- %s", c.pageSize, got, c.want, c.why)
			}
		})
	}
}

// TestAZeroPageSizeDoesNotTruncateTheEstate is the behavioural half, and the
// one that actually bites: the store's own default page is 100 rows, so an
// unclamped zero value returned exactly the first 100 and stopped, with a 200.
// The fixture therefore has to cross that boundary for the assertion to mean
// anything -- 102 in-scope assets, asserted as an exact name list.
func TestAZeroPageSizeDoesNotTruncateTheEstate(t *testing.T) {
	f := newAPIHandlerFixture(t)

	want := []string{"vm-db-1"} // already in the fixture, prod-only
	for i := 0; i < 101; i++ {
		name := fmt.Sprintf("vm-bulk-%03d", i)
		f.mustAsset(domain.KindVM, name, nil, "prod")
		want = append(want, name)
	}
	sort.Strings(want)

	// Deliberately NOT New(): this is the zero-value construction that
	// truncated, and it must not.
	zero := &API{Store: f.s}
	rows, err := zero.fetchAllAssets(f.ctx, f.readerScope(t, tokProd))
	if err != nil {
		t.Fatalf("paging the ansible view: %v", err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %d assets, want %d; an unclamped zero PageSize stops at the store's own default page.\ngot:  %v\nwant: %v",
			len(got), len(want), got, want)
	}
}
