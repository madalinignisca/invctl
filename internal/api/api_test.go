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
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"a client mistake is 400", fmt.Errorf("%w: limit", ErrBadRequest), 400},
		{"an absent entity is 404", domain.ErrNotFound, 404},
		{"an out-of-scope entity renders the same 404 as an absent one", store.ErrOutOfScope, 404},
		{"anything else is 500", errors.New("boom"), 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, c.err)
			if rec.Code != c.want {
				t.Fatalf("got %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestAnInternalErrorIsNotEchoedToTheClient(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New(`pq: relation "asset" does not exist`))
	if strings.Contains(rec.Body.String(), "relation") {
		t.Fatal("a driver error must not reach the client; it names the schema")
	}
}

// TestAWrappedBadRequestKeepsItsClientSafeMessage pins finding (c): every
// list handler wraps whatever error it gets back (fmt.Errorf("listing
// assets: %w", err)), so the 400 body must come from the badRequestError's
// own message, not from trimming a known prefix off the wrapped text -- a
// positional trim over "listing assets: bad request: kind is not a
// recognised asset kind" would either fail to strip anything or leak the
// wrapping text itself.
func TestAWrappedBadRequestKeepsItsClientSafeMessage(t *testing.T) {
	err := fmt.Errorf("listing assets: %w", badRequest("kind is not a recognised asset kind"))
	rec := httptest.NewRecorder()
	writeError(rec, err)
	if rec.Code != 400 {
		t.Fatalf("got %d, want 400", rec.Code)
	}
	const want = `{"error":"kind is not a recognised asset kind"}`
	if rec.Body.String() != want {
		t.Fatalf("got %s, want %s", rec.Body.String(), want)
	}
}

// TestAnOutOfScopeErrorAndAFabricatedIDProduceTheSameResponse pins the
// property store.ErrOutOfScope exists to protect at the one place it can be
// undone silently: writeError. Both are domain.ErrNotFound by wrapping, and
// this asserts they render byte-for-byte identical HTTP responses -- status,
// headers and body -- so that a client cannot use the response to tell an
// id that exists but is out of scope apart from one that was never real.
// docs/api-design.md §3: a 403 (or any other divergence) would be an
// existence oracle over the estate.
func TestAnOutOfScopeErrorAndAFabricatedIDProduceTheSameResponse(t *testing.T) {
	recOutOfScope := httptest.NewRecorder()
	writeError(recOutOfScope, fmt.Errorf("getting asset x for the api: %w", store.ErrOutOfScope))

	recAbsent := httptest.NewRecorder()
	writeError(recAbsent, fmt.Errorf("getting asset y for the api: %w", domain.ErrNotFound))

	if recOutOfScope.Code != recAbsent.Code {
		t.Fatalf("status differs: out-of-scope %d, absent %d", recOutOfScope.Code, recAbsent.Code)
	}
	if recOutOfScope.Body.String() != recAbsent.Body.String() {
		t.Fatalf("body differs: out-of-scope %q, absent %q", recOutOfScope.Body.String(), recAbsent.Body.String())
	}
	// The FULL header map, not one named header picked because it seemed
	// relevant: a length or "contains" check is satisfied by many wrong
	// answers, and item 1 is the property this task most had to protect.
	if !reflect.DeepEqual(recOutOfScope.Header(), recAbsent.Header()) {
		t.Fatalf("headers differ: out-of-scope %v, absent %v", recOutOfScope.Header(), recAbsent.Header())
	}
}

// TestPageDataMarshalsAsEmptyArrayNotNull pins Page[T].Data as always built
// with make([]T, 0, ...), never left as a nil slice, so an empty collection
// marshals as `[]` and a consumer doing `for host in group.hosts` never has to
// special-case null.
func TestPageDataMarshalsAsEmptyArrayNotNull(t *testing.T) {
	p := Page[Asset]{Data: make([]Asset, 0), Next: nil}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshalling an empty page: %v", err)
	}
	const want = `{"data":[],"next":null}`
	if string(body) != want {
		t.Fatalf("got %s, want %s", body, want)
	}
}

// TestAnUnknownQueryParameterIsRefused pins item 5: a typo in a query
// parameter name must not silently fall back to a default with a 200 -- that
// is the same silent-fallback shape a discarded parse error would be.
func TestAnUnknownQueryParameterIsRefused(t *testing.T) {
	err := checkKnownParams(url.Values{"limt": {"5"}}, "after", "limit")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v, want ErrBadRequest", err)
	}
	if !strings.Contains(err.Error(), `"limt"`) {
		t.Fatalf("error %q does not name the offending parameter", err.Error())
	}
}

// TestAnUnknownQueryParameterValueIsNeverEchoed pins the second half of item
// 5: the parameter NAME is self-diagnosing, but its value never appears in the
// error, so a malformed query string cannot be used to reflect
// attacker-controlled content into a client-visible response.
func TestAnUnknownQueryParameterValueIsNeverEchoed(t *testing.T) {
	err := checkKnownParams(url.Values{"env": {"<script>secret-value</script>"}}, "after", "limit")
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("the offending value leaked into the error: %q", err.Error())
	}
}

// TestARepeatedQueryParameterIsRefused pins finding (d): url.Values.Get takes
// the first of several values for a key, so ?limit=5&limit=9 would otherwise
// silently use 5 and drop 9 -- a value the caller supplied that the handler
// never even looks at, the same shape as an unknown parameter name.
func TestARepeatedQueryParameterIsRefused(t *testing.T) {
	err := checkKnownParams(url.Values{"limit": {"5", "9"}}, "after", "limit")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v, want ErrBadRequest", err)
	}
	if !strings.Contains(err.Error(), `"limit"`) {
		t.Fatalf("error %q does not name the repeated parameter", err.Error())
	}
}

// TestAnOversizedParameterNameIsNotEchoed pins finding (e): the parameter
// NAME is as caller-controlled as its value, so an oversized one is replaced
// by a generic placeholder rather than echoed verbatim.
func TestAnOversizedParameterNameIsNotEchoed(t *testing.T) {
	long := strings.Repeat("x", maxEchoedParamName+1)
	err := checkKnownParams(url.Values{long: {"1"}}, "after", "limit")
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	if strings.Contains(err.Error(), long) {
		t.Fatalf("an oversized parameter name was echoed verbatim: %q", err.Error())
	}
}

// TestAnUnprintableParameterNameIsNotEchoed is the same guard for a name
// carrying a control character rather than merely being long.
func TestAnUnprintableParameterNameIsNotEchoed(t *testing.T) {
	bad := "a\x00b"
	err := checkKnownParams(url.Values{bad: {"1"}}, "after", "limit")
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	if strings.Contains(err.Error(), bad) {
		t.Fatalf("an unprintable parameter name was echoed verbatim: %q", err.Error())
	}
}

func TestAKnownQueryParameterIsAccepted(t *testing.T) {
	if err := checkKnownParams(url.Values{"after": {"x"}, "limit": {"5"}}, "after", "limit"); err != nil {
		t.Fatalf("a legitimate parameter set was refused: %v", err)
	}
}

func TestNoQueryParametersIsAlwaysAccepted(t *testing.T) {
	if err := checkKnownParams(url.Values{}); err != nil {
		t.Fatalf("an empty query string was refused: %v", err)
	}
}

// TestNextCursorIsNilOnAShortPage pins nextCursor's two cases directly,
// without a store round trip.
func TestNextCursorIsNilOnAShortPage(t *testing.T) {
	rows := []string{"a", "b"}
	got := nextCursor(rows, 5, func(s string) string { return s })
	if got != nil {
		t.Fatalf("got %v, want nil for a page shorter than the limit", got)
	}
}

func TestNextCursorIsTheLastRowIDOnAFullPage(t *testing.T) {
	rows := []string{"a", "b", "c"}
	got := nextCursor(rows, 3, func(s string) string { return s })
	if got == nil || *got != "c" {
		t.Fatalf("got %v, want a cursor naming the last row", got)
	}
}

func TestNextCursorIsNilOnAnEmptyPage(t *testing.T) {
	var rows []string
	got := nextCursor(rows, 5, func(s string) string { return s })
	if got != nil {
		t.Fatalf("got %v, want nil for an empty page", got)
	}
}

// ---------------------------------------------------------------------------
// The Ansible view (Task 8): group naming, sanitisation and the refusal of a
// name collision. buildAnsibleInventory is exercised as the pure function it
// is -- no store, no request -- so these tests assert exact values rather
// than shapes.
// ---------------------------------------------------------------------------

func TestGroupNamesAreSanitisedAndPrefixed(t *testing.T) {
	cases := []struct{ dimension, raw, want string }{
		{"env", "prod", "env_prod"},
		{"kind", "vm", "kind_vm"},
		{"svc", "billing-api", "svc_billing_api"},
		{"svc", "Billing API", "svc_billing_api"},
		{"site", "dc-1", "site_dc_1"},
	}
	for _, c := range cases {
		if got := ansibleGroupName(c.dimension, c.raw); got != c.want {
			t.Errorf("ansibleGroupName(%q, %q) = %q, want %q", c.dimension, c.raw, got, c.want)
		}
	}
}

// TestAServiceCannotCollideWithAnEnvironment pins the reason for the
// dimension prefix: a service literally named "prod" sanitises to
// "svc_prod", not "prod", and cannot silently widen the "env_prod" group.
func TestAServiceCannotCollideWithAnEnvironment(t *testing.T) {
	if ansibleGroupName("svc", "prod") == ansibleGroupName("env", "prod") {
		t.Fatal("a service and an environment of the same name must not produce one group")
	}
}

// TestAGroupNameCollisionIsRefused pins docs/api-design.md §4: two services
// sanitising to the same name -- billing-api and billing_api -- must be
// refused with an error naming both sources, never silently merged into one
// group. A merged group would silently widen the target set of every
// playbook that uses it.
func TestAGroupNameCollisionIsRefused(t *testing.T) {
	_, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "h1", Kind: "vm", Addresses: []string{"10.0.0.1"},
			Environments: []string{"prod"}, Services: []string{"billing-api"}},
		{ID: "b", Name: "h2", Kind: "vm", Addresses: []string{"10.0.0.2"},
			Environments: []string{"prod"}, Services: []string{"billing_api"}},
	})
	if err == nil {
		t.Fatal("two services sanitising to one group name must be refused, not merged")
	}
	if !strings.Contains(err.Error(), "billing-api") || !strings.Contains(err.Error(), "billing_api") {
		t.Fatalf("error %q does not name both colliding sources", err.Error())
	}
}

// TestOnlyAddressableKindsAreHosts asserts the exact host set, not merely
// that the excluded names are absent: a rack is not connectable, a switch
// is out of scope for this view, and an addressless VM has nothing to
// connect to.
func TestOnlyAddressableKindsAreHosts(t *testing.T) {
	inv, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "vm-1", Kind: "vm", Addresses: []string{"10.0.0.1"}, Environments: []string{"prod"}},
		{ID: "b", Name: "rack-14", Kind: "rack", Environments: []string{"prod"}},
		{ID: "c", Name: "sw-1", Kind: "switch", Addresses: []string{"10.0.0.9"}, Environments: []string{"prod"}},
		{ID: "d", Name: "vm-2", Kind: "vm", Environments: []string{"prod"}}, // no address
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	gotHosts := make([]string, 0, len(inv.Meta.HostVars))
	for h := range inv.Meta.HostVars {
		gotHosts = append(gotHosts, h)
	}
	sort.Strings(gotHosts)
	wantHosts := []string{"vm-1"}
	if !reflect.DeepEqual(gotHosts, wantHosts) {
		t.Fatalf("got hosts %v, want %v", gotHosts, wantHosts)
	}
}

// TestAnAssetInTwoEnvironmentsIsInTwoGroups asserts the exact host list of
// every group involved, not merely its length.
func TestAnAssetInTwoEnvironmentsIsInTwoGroups(t *testing.T) {
	inv, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "vm-1", Kind: "vm", Addresses: []string{"10.0.0.1"},
			Environments: []string{"prod", "shared"}},
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	want := []string{"vm-1"}
	for _, g := range []string{"env_prod", "env_shared", "kind_vm"} {
		if !reflect.DeepEqual(inv.Groups[g].Hosts, want) {
			t.Errorf("group %s: got %v, want %v", g, inv.Groups[g].Hosts, want)
		}
	}
	if len(inv.Groups) != 3 {
		t.Fatalf("got %d groups, want exactly 3 (env_prod, env_shared, kind_vm): %+v", len(inv.Groups), inv.Groups)
	}
}

// TestAnAssetInMultipleGroupsProducesTheFullHostVarsRecord pins the exact
// hostvars shape published for a host, including the invctl_site field
// sourced from the asset's closure-derived Site.
func TestAnAssetInMultipleGroupsProducesTheFullHostVarsRecord(t *testing.T) {
	site := "dc-1"
	inv, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "01924e5a-0000-7000-8000-000000000001", Name: "vm-db-2", Kind: "vm",
			Site: &site, Addresses: []string{"10.2.0.14", "10.2.0.15"},
			Environments: []string{"prod"}, Services: []string{"billing-api"}},
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	want := map[string]string{
		"invctl_id":    "01924e5a-0000-7000-8000-000000000001",
		"invctl_kind":  "vm",
		"invctl_site":  "dc-1",
		"ansible_host": "10.2.0.14",
	}
	got, ok := inv.Meta.HostVars["vm-db-2"]
	if !ok {
		t.Fatalf("host vm-db-2 is missing from hostvars: %+v", inv.Meta.HostVars)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got hostvars %+v, want %+v", got, want)
	}
}

// TestAHostWithNoResolvedSiteOmitsInvctlSite pins that a nil Site does not
// publish an "invctl_site": null field into a plain map[string]string.
func TestAHostWithNoResolvedSiteOmitsInvctlSite(t *testing.T) {
	inv, err := buildAnsibleInventory([]store.APIAssetRow{
		{ID: "a", Name: "vm-1", Kind: "vm", Addresses: []string{"10.0.0.1"}, Environments: []string{"prod"}},
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if _, ok := inv.Meta.HostVars["vm-1"]["invctl_site"]; ok {
		t.Fatalf("invctl_site must be absent when the asset has no resolved site: got %+v", inv.Meta.HostVars["vm-1"])
	}
}

// TestGroupHostsMarshalAsEmptyArrayNotNull pins the binding constraint: a
// group with no hosts (which buildAnsibleInventory never produces on its
// own, but a caller assembling Inventory by hand might) must still marshal
// "hosts" as [] rather than null.
func TestGroupHostsMarshalAsEmptyArrayNotNull(t *testing.T) {
	g := InventoryGroup{Hosts: []string{}}
	body, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(body) != `{"hosts":[]}` {
		t.Fatalf("got %s, want {\"hosts\":[]}", body)
	}
}

// TestInventoryMarshalsMetaBesideArbitraryGroupNames pins the exact wire
// shape from docs/api-design.md §4: "_meta" and every group key sit at the
// same top level.
func TestInventoryMarshalsMetaBesideArbitraryGroupNames(t *testing.T) {
	inv := Inventory{
		Meta: InventoryMeta{HostVars: map[string]map[string]string{
			"vm-1": {"invctl_id": "a", "invctl_kind": "vm", "ansible_host": "10.0.0.1"},
		}},
		Groups: map[string]InventoryGroup{
			"env_prod": {Hosts: []string{"vm-1"}},
		},
	}
	body, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	const want = `{"_meta":{"hostvars":{"vm-1":{"ansible_host":"10.0.0.1","invctl_id":"a","invctl_kind":"vm"}}},"env_prod":{"hosts":["vm-1"]}}`
	if string(body) != want {
		t.Fatalf("got %s, want %s", body, want)
	}
}

// TestAGroupNamedMetaIsRefused pins the last line of defence Inventory's own
// MarshalJSON adds beyond buildAnsibleInventory's own collision check: a
// group whose sanitised name happens to be "_meta" must never silently
// overwrite the real one.
func TestAGroupNamedMetaIsRefused(t *testing.T) {
	inv := Inventory{
		Meta:   InventoryMeta{HostVars: map[string]map[string]string{}},
		Groups: map[string]InventoryGroup{"_meta": {Hosts: []string{"vm-1"}}},
	}
	if _, err := json.Marshal(inv); err == nil {
		t.Fatal("a group named _meta must be refused, not silently merged into the reserved key")
	}
}

// TestStoreAndAPILimitsAgree is the cross-package half of the constant
// assertion internal/store's api.go doc comment calls for: store cannot
// import internal/api without a cycle, so the two limits are declared twice,
// and nothing but this test keeps them from drifting apart.
func TestStoreAndAPILimitsAgree(t *testing.T) {
	if store.APIDefaultLimit() != DefaultLimit {
		t.Fatalf("store's default page size (%d) and api.DefaultLimit (%d) disagree",
			store.APIDefaultLimit(), DefaultLimit)
	}
	if store.APIMaxLimit() != MaxLimit {
		t.Fatalf("store's max page size (%d) and api.MaxLimit (%d) disagree",
			store.APIMaxLimit(), MaxLimit)
	}
}
