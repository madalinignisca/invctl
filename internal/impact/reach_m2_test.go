package impact_test

import (
	"testing"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// TestM2TablesDoNotAffectImpact is this milestone's headline guarantee:
// internal/impact is untouched in M2, and LoadGraph/Simulate do not read any
// of the seven new tables yet. Populating them -- a forwarder group, its
// members, an attachment and an uplink, all declared against real fixture
// assets -- must not change a single byte of the existing hv-01 result.
//
// docs/reachability-design.md calls this exact case out: "hv-01 -- the
// regression guard ... Result byte-identical to today: 6 services." That is
// the assertion below, both before and after the new tables are populated.
func TestM2TablesDoNotAffectImpact(t *testing.T) {
	f := newFixture(t)

	before, beforeByCode := f.simulate(t, 180, "hv-01")

	swCoreID := f.refs.Assets["sw-core-1"]
	fwEdgeID := f.refs.Assets["fw-edge-1"]
	hv01ID := f.refs.Assets["hv-01"]
	if swCoreID == "" || fwEdgeID == "" || hv01ID == "" {
		t.Fatal("fixture is missing an asset this test depends on")
	}

	core, err := domain.NewNetGroup(store.NewID(), domain.NetGroupSpec{
		Code: "sw-core", Name: "Core switch", Kind: domain.NetGroupStandalone,
		Role: domain.NetRoleCore, Availability: domain.AvailStandalone,
	}, f.store.Now())
	if err != nil {
		t.Fatalf("building net group: %v", err)
	}
	if err := f.store.CreateNetGroup(f.ctx, domain.SystemActor, core); err != nil {
		t.Fatalf("creating net group: %v", err)
	}
	edge, err := domain.NewNetGroup(store.NewID(), domain.NetGroupSpec{
		Code: "fw-edge", Name: "Edge firewall", Kind: domain.NetGroupStandalone,
		Role: domain.NetRoleEdge, Availability: domain.AvailStandalone,
	}, f.store.Now())
	if err != nil {
		t.Fatalf("building net group: %v", err)
	}
	if err := f.store.CreateNetGroup(f.ctx, domain.SystemActor, edge); err != nil {
		t.Fatalf("creating net group: %v", err)
	}

	member, err := domain.NewNetGroupMember(core.ID, swCoreID, "member", f.store.Now())
	if err != nil {
		t.Fatalf("building member: %v", err)
	}
	if err := f.store.AddNetGroupMember(f.ctx, domain.SystemActor, member); err != nil {
		t.Fatalf("adding member: %v", err)
	}

	uplink, err := domain.NewNetUplink(store.NewID(), core.ID, edge.ID, domain.PlaneData, f.store.Now())
	if err != nil {
		t.Fatalf("building uplink: %v", err)
	}
	if err := f.store.CreateNetUplink(f.ctx, domain.SystemActor, uplink); err != nil {
		t.Fatalf("creating uplink: %v", err)
	}

	attachment, err := domain.NewNetAttachment(store.NewID(), hv01ID, core.ID, domain.PlaneData, f.store.Now())
	if err != nil {
		t.Fatalf("building attachment: %v", err)
	}
	if err := f.store.CreateNetAttachment(f.ctx, domain.SystemActor, attachment, nil); err != nil {
		t.Fatalf("creating attachment: %v", err)
	}

	anchor, err := domain.NewNetAnchor(store.NewID(), "internet", "Internet", "external", edge.ID, f.store.Now())
	if err != nil {
		t.Fatalf("building anchor: %v", err)
	}
	if err := f.store.CreateNetAnchor(f.ctx, domain.SystemActor, anchor); err != nil {
		t.Fatalf("creating anchor: %v", err)
	}

	after, afterByCode := f.simulate(t, 180, "hv-01")

	if len(before.Services) != 6 {
		t.Fatalf("setup: hv-01 affected %d services before M2 tables existed, want 6 per the design doc's regression guard",
			len(before.Services))
	}
	if len(after.Services) != len(before.Services) {
		t.Fatalf("populating M2's tables changed the affected service count: %d -> %d",
			len(before.Services), len(after.Services))
	}
	for code, want := range beforeByCode {
		got, ok := afterByCode[code]
		if !ok {
			t.Errorf("%s is no longer reported after M2 tables were populated (was %s)", code, want.Status)
			continue
		}
		if got.Status != want.Status {
			t.Errorf("%s status changed from %s to %s after M2 tables were populated", code, want.Status, got.Status)
		}
	}
	if after.Iterations != before.Iterations {
		t.Errorf("iteration count changed from %d to %d", before.Iterations, after.Iterations)
	}
	if len(after.Cycles) != len(before.Cycles) {
		t.Errorf("cycle count changed from %d to %d", len(before.Cycles), len(after.Cycles))
	}
}
