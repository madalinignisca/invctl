package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// ---------------------------------------------------------------------------
// M2: reachability data model -- forwarder groups, uplinks, attachments,
// anchors, and propose-only derivation.

// mustNetGroupWeb declares a forwarder group directly through the store, for
// tests whose focus is the web layer rather than group creation itself.
func mustNetGroupWeb(t *testing.T, h *harness, code, role, availability string) string {
	t.Helper()
	spec := domain.NetGroupSpec{Code: code, Name: code, Kind: domain.NetGroupStandalone, Role: role, Availability: availability}
	g, err := domain.NewNetGroup(store.NewID(), spec, h.store.Now())
	if err != nil {
		t.Fatalf("building net group: %v", err)
	}
	if err := h.store.CreateNetGroup(context.Background(), domain.SystemActor, g); err != nil {
		t.Fatalf("creating net group: %v", err)
	}
	return g.ID
}

// TestReadOnlyUserCannotWriteReachabilityTopology extends the RBAC model to every
// new M2 write route.
func TestReadOnlyUserCannotWriteReachabilityTopology(t *testing.T) {
	h := newHarness(t)
	// A test-scoped code: the seed now declares sw-core/fw-edge/sw-oob and
	// net_group.code is UNIQUE. This test is about RBAC on the routes, not
	// about the group's identity, so any code will do.
	groupID := mustNetGroupWeb(t, h, "rbac-test-group", domain.NetRoleCore, domain.AvailStandalone)
	h.login("viewer", "viewer-password")

	cases := []struct{ name, path string }{
		{"declare group", "/network/groups"},
		{"add member", "/network/groups/" + groupID + "/members"},
		{"declare uplink", "/network/groups/" + groupID + "/uplinks"},
		{"declare attachment", "/network/attachments"},
		{"declare anchor", "/network/anchors"},
		{"derive", "/network/derive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := h.csrfToken("/network")
			resp := h.post(tc.path, url.Values{"csrf_token": {token}}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s as viewer returned %d, want 403", tc.path, resp.StatusCode)
			}
		})
	}
}

// TestCSRFIsEnforcedOnReachabilityRoutes: a route added without a form referencing
// the shared token is exactly the kind of gap that goes unnoticed.
func TestCSRFIsEnforcedOnReachabilityRoutes(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/network/groups", url.Values{
		"csrf_token":   {"not-a-real-token"},
		"code":         {"csrf-net"},
		"name":         {"CSRF net"},
		"kind":         {domain.NetGroupStandalone},
		"role":         {domain.NetRoleCore},
		"availability": {domain.AvailStandalone},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if _, err := h.store.GetNetGroup(context.Background(), "csrf-net"); err == nil {
		t.Error("a group was created despite the CSRF failure")
	}
}

// TestNetworkGroupValidationIs422 drives the availability/min_healthy pairing
// through the handler: CLAUDE.md's rule that a validation failure re-renders
// the form partial at 422, never a 200 with the error buried in the body.
func TestNetworkGroupValidationIs422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/network")

	resp := h.post("/network/groups", url.Values{
		"csrf_token":   {token},
		"code":         {"sw-bad"},
		"name":         {"Bad group"},
		"kind":         {domain.NetGroupMCLAG},
		"role":         {domain.NetRoleCore},
		"availability": {domain.AvailActiveActive},
		// min_healthy deliberately omitted -- required for active_active.
	}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(text, `id="net-group-form"`) {
		t.Error("the response is not the group form partial")
	}
	if !strings.Contains(text, "required for active_active") {
		t.Error("the response does not explain the min_healthy requirement")
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}

	groups, err := h.store.ListNetGroups(context.Background())
	if err != nil {
		t.Fatalf("listing groups: %v", err)
	}
	for _, g := range groups {
		if g.Code == "sw-bad" {
			t.Error("an invalid group was created despite failing validation")
		}
	}
}

// TestNetworkDeriveProposesAndDoesNotWriteThroughUI drives the propose-only
// contract through the real router: posting to /network/derive must render a
// proposal and must not create any row anywhere in the topology tables.
//
// Since M5 the seed declares an attachment for every cable it lays, so
// derivation against an untouched fixture correctly proposes nothing -- that
// is the fixture agreeing with itself, and it would make this test assert
// nothing. So the test patches in one cable derivation has never seen: a new
// server into a spare port on sw-core-1, whose group the seed already
// declares.
//
// The propose-only half is asserted as "the count did not change", not as
// "the count is zero". Repointing it at the seeded group and changing 0 to 3
// would destroy the guarantee: derivation could then write one extra row and
// the test would still pass.
func TestNetworkDeriveProposesAndDoesNotWriteThroughUI(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	swCoreAssetID := h.refs.Assets["sw-core-1"]
	groupID := h.refs.NetGroups["sw-core"]
	if swCoreAssetID == "" || groupID == "" {
		t.Fatal("fixture does not have sw-core-1 in a declared sw-core group")
	}

	// An undeclared host on an undeclared port. is_mgmt is false on both ends,
	// so the proposal must come out on the data plane.
	host := mustServerAssetWeb(t, h, "test-derive-host")
	hostPort := mustInterfaceWeb(t, h, host, "eno1")
	switchPort := mustInterfaceWeb(t, h, swCoreAssetID, "Ethernet40")
	link, err := domain.NewLink(store.NewID(), hostPort, switchPort)
	if err != nil {
		t.Fatalf("building link: %v", err)
	}
	if err := h.store.CreateLink(ctx, domain.SystemActor, link); err != nil {
		t.Fatalf("creating link: %v", err)
	}

	before, err := h.store.ListNetAttachments(ctx, groupID)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}

	token := h.csrfToken("/network")
	resp := h.post("/network/derive", url.Values{"csrf_token": {token}}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (derivation renders, it does not redirect)", resp.StatusCode)
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}
	if !strings.Contains(text, "Derivation proposal") {
		t.Error("the response is not the derivation result partial")
	}
	if !strings.Contains(text, "test-derive-host") {
		t.Error("the proposal does not name test-derive-host, which this test cables to sw-core-1")
	}

	after, err := h.store.ListNetAttachments(ctx, groupID)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("derivation changed the attachment count from %d to %d through the UI; "+
			"it must be propose-only", len(before), len(after))
	}
}

func mustServerAssetWeb(t *testing.T, h *harness, name string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), domain.KindServer, name, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := h.store.CreateAsset(context.Background(), domain.SystemActor, a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

func mustInterfaceWeb(t *testing.T, h *harness, assetID, name string) string {
	t.Helper()
	i, err := domain.NewInterface(store.NewID(), assetID, name, domain.FFSFP28)
	if err != nil {
		t.Fatalf("building interface %s: %v", name, err)
	}
	if err := h.store.CreateInterface(context.Background(), domain.SystemActor, i); err != nil {
		t.Fatalf("creating interface %s: %v", name, err)
	}
	return i.ID
}
