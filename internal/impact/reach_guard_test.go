package impact_test

import (
	"testing"
)

// TestHv01IsTheRegressionGuard is the assertion docs/reachability-design.md
// says it would write first: "hv-01 -- the regression guard ... Result
// byte-identical to today: 6 services ... the whole design is worthless if it
// perturbs it."
//
// This began life as TestM2TablesDoNotAffectImpact, whose contract was "M2's
// tables are empty; populating them changes nothing". M5 retired that premise
// by moving the demo topology into the seed: there is no empty state left to
// compare against, and declaring a second, redundant topology on top of the
// seeded one would have proved nothing. What survives is the number and the
// claim underneath it, now asserted across the topology boundary.
//
// The 6 is derived, never adjusted. hv-01's subtree is {hv-01, vm-vault-1,
// vm-db-1, vm-app-1}; no net_group_member sits inside it, so no group status
// moves; hv-01's own attachment dies but hv-01 is already in downInstanceIDs,
// so alive = !down && !isolated is unchanged; and every surviving host still
// resolves to sw-core in the same pessimistic component. If this ever comes
// out as anything other than 6, that is the regression the milestone was
// warned about, not fixture arithmetic to be reconciled.
func TestHv01IsTheRegressionGuard(t *testing.T) {
	f := newFixture(t)
	assertHasTopology(t, f)

	withTopology, withByCode := f.simulate(t, 180, "hv-01")
	if len(withTopology.Services) != 6 {
		t.Fatalf("hv-01 affected %d services with the seeded topology, want 6 per the design doc's regression guard: %v",
			len(withTopology.Services), codesOf(withTopology.Services))
	}

	// Losing a hypervisor is a containment question, not a network one, so
	// retiring every net_* row must not change a byte of the answer.
	// clearSeededTopology asserts Inputs.Net really goes back to nil, so this
	// comparison genuinely straddles the boundary rather than comparing two
	// runs of the same graph.
	clearSeededTopology(t, f)
	bare, bareByCode := f.simulate(t, 180, "hv-01")

	if len(bare.Services) != len(withTopology.Services) {
		t.Fatalf("retiring the topology changed the affected service count: %d -> %d",
			len(withTopology.Services), len(bare.Services))
	}
	for code, want := range withByCode {
		got, ok := bareByCode[code]
		if !ok {
			t.Errorf("%s is no longer reported with the topology retired (was %s)", code, want.Status)
			continue
		}
		if got.Status != want.Status {
			t.Errorf("%s status changed from %s to %s when the topology was retired", code, want.Status, got.Status)
		}
	}
	if bare.Iterations != withTopology.Iterations {
		t.Errorf("iteration count changed from %d to %d", withTopology.Iterations, bare.Iterations)
	}
	if len(bare.Cycles) != len(withTopology.Cycles) {
		t.Errorf("cycle count changed from %d to %d", len(withTopology.Cycles), len(bare.Cycles))
	}
}
