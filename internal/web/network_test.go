package web_test

import (
	"context"
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
	groupID := mustNetGroupWeb(t, h, "sw-core", domain.NetRoleCore, domain.AvailStandalone)
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
			if resp.StatusCode != 403 {
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
	if resp.StatusCode != 400 {
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

	if resp.StatusCode != 422 {
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
func TestNetworkDeriveProposesAndDoesNotWriteThroughUI(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	// The seed fixture cables hv-01's mgmt NIC to sw-core-1, so a forwarder
	// group over sw-core-1 makes derivation propose one mgmt attachment.
	swCoreAssetID := h.refs.Assets["sw-core-1"]
	if swCoreAssetID == "" {
		t.Skip("fixture does not have sw-core-1")
	}
	groupID := mustNetGroupWeb(t, h, "sw-core", domain.NetRoleCore, domain.AvailStandalone)
	m, err := domain.NewNetGroupMember(groupID, swCoreAssetID, "member", h.store.Now())
	if err != nil {
		t.Fatalf("building member: %v", err)
	}
	if err := h.store.AddNetGroupMember(ctx, domain.SystemActor, m); err != nil {
		t.Fatalf("adding member: %v", err)
	}

	token := h.csrfToken("/network")
	resp := h.post("/network/derive", url.Values{"csrf_token": {token}}, true)
	text := body(t, resp)

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (derivation renders, it does not redirect)", resp.StatusCode)
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}
	if !strings.Contains(text, "Derivation proposal") {
		t.Error("the response is not the derivation result partial")
	}
	if !strings.Contains(text, "hv-01") {
		t.Error("the proposal does not name hv-01, which the fixture cables to sw-core-1")
	}

	rows, err := h.store.ListNetAttachments(ctx, groupID)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("derivation wrote %d attachment rows through the UI; it must be propose-only", len(rows))
	}
}
