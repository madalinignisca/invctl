// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact

import "testing"

// WP-I1. Three work packages shipped models the engine had never heard of, so
// simulating the loss of a switch said nothing about the broadcast domain that
// switch was the only port of. These are the cases that must now be reported.

func structureFixture() []Structure {
	return []Structure{
		// One VLAN across two switches, two ports on the first.
		{Kind: StructureVLAN, ID: "v30", Name: "workloads",
			AssetIDs: []string{"sw-a", "sw-a", "sw-b"}},
		// A VLAN that only ever lived on one switch.
		{Kind: StructureVLAN, ID: "v99", Name: "transit",
			AssetIDs: []string{"sw-b"}},
		// A redundancy group across two firewalls.
		{Kind: StructureFHRP, ID: "g10", Name: "gw-prod",
			AssetIDs: []string{"fw-a", "fw-b"}},
		// An overlay terminating on both switches.
		{Kind: StructureL2VPN, ID: "o1", Name: "prod-stretch",
			AssetIDs: []string{"sw-a", "sw-b"}},
	}
}

func findingFor(t *testing.T, out []StructureFinding, name string) (StructureFinding, bool) {
	t.Helper()
	for _, f := range out {
		if f.Name == name {
			return f, true
		}
	}
	return StructureFinding{}, false
}

// TestLosingEveryPortEmptiesTheStructure.
func TestLosingEveryPortEmptiesTheStructure(t *testing.T) {
	out := analyseStructures(structureFixture(), map[string]bool{"sw-a": true, "sw-b": true})

	for _, name := range []string{"workloads", "transit", "prod-stretch"} {
		f, ok := findingFor(t, out, name)
		if !ok {
			t.Errorf("%s is not reported, but every asset holding a member is down", name)
			continue
		}
		if !f.Emptied() {
			t.Errorf("%s reports %d remaining, want 0", name, f.Remaining)
		}
	}
	// The firewalls are up, so the redundancy group is untouched.
	if _, ok := findingFor(t, out, "gw-prod"); ok {
		t.Error("gw-prod is reported, but neither of its routers is down")
	}
}

// TestFourPortsOnOneSwitchAreOneAsset.
//
// The engine counts DISTINCT assets, because losing a switch takes every port
// on it at once. Counting members would report VLAN 30 as 1-of-3 surviving when
// the truth is 1-of-2 boxes -- and the difference decides whether "it survived"
// is worth saying.
func TestFourPortsOnOneSwitchAreOneAsset(t *testing.T) {
	out := analyseStructures(structureFixture(), map[string]bool{"sw-a": true})

	f, ok := findingFor(t, out, "workloads")
	if !ok {
		t.Fatal("workloads is not reported, but it is down to one switch of two")
	}
	if f.Total != 2 || f.Remaining != 1 {
		t.Errorf("workloads reports %d of %d assets; want 1 of 2 -- sw-a holds two of "+
			"its three ports and is one box", f.Remaining, f.Total)
	}
}

// TestOneLeftOfSeveralIsReportedAndOneOfOneIsNot.
//
// A structure reduced to its last asset will not survive the next failure,
// which is worth saying. A structure that had one asset before the outage and
// still has it was already a single point of failure -- the redundancy page
// says so permanently, and repeating it here answers a question nobody asked.
func TestOneLeftOfSeveralIsReportedAndOneOfOneIsNot(t *testing.T) {
	out := analyseStructures(structureFixture(), map[string]bool{"fw-a": true})

	f, ok := findingFor(t, out, "gw-prod")
	if !ok {
		t.Fatal("gw-prod is not reported, but it is down to one router of two")
	}
	if f.Remaining != 1 || f.Total != 2 {
		t.Errorf("gw-prod reports %d of %d, want 1 of 2", f.Remaining, f.Total)
	}

	// transit lives on sw-b alone and sw-b is up: nothing has changed for it.
	if _, ok := findingFor(t, out, "transit"); ok {
		t.Error("transit is reported, but it had one asset before this outage and " +
			"still has it -- that is a standing finding, not a consequence")
	}
}

// TestNothingDownIsNothingReported.
func TestNothingDownIsNothingReported(t *testing.T) {
	if out := analyseStructures(structureFixture(), map[string]bool{}); len(out) != 0 {
		t.Errorf("an outage of nothing produced %d findings: %+v", len(out), out)
	}
}

// TestEmptiedComesBeforeReduced. Somebody reads the top of the list.
func TestEmptiedComesBeforeReduced(t *testing.T) {
	// sw-a and fw-a down: workloads keeps sw-b (reduced), prod-stretch keeps
	// sw-b (reduced), gw-prod keeps fw-b (reduced). Add a structure wholly on
	// sw-a so something is emptied.
	fixture := append(structureFixture(), Structure{
		Kind: StructureVLAN, ID: "v40", Name: "dev", AssetIDs: []string{"sw-a"},
	})
	out := analyseStructures(fixture, map[string]bool{"sw-a": true, "fw-a": true})

	if len(out) < 2 {
		t.Fatalf("expected several findings, got %d", len(out))
	}
	if !out[0].Emptied() {
		t.Errorf("the first finding is %q with %d remaining; an emptied structure must "+
			"sort above a reduced one", out[0].Name, out[0].Remaining)
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].Remaining > out[i].Remaining {
			t.Errorf("finding %d has %d remaining and %d has %d; worst must come first",
				i-1, out[i-1].Remaining, i, out[i].Remaining)
		}
	}
}

// TestAStructureThatWasAlreadyEmptyIsNotThisOutagesDoing.
//
// A VLAN with no ports was empty before anybody simulated anything -- the
// overview reports it as a standing gap. Reporting it here would blame this
// outage for a state it did not cause, and an operator would go looking for a
// switch that was never in it.
//
// In practice loadStructures builds these from a join and cannot produce one
// with no members, so this guards the function rather than the query. Worth
// keeping: the type is not private and the next caller may not come from SQL.
func TestAStructureThatWasAlreadyEmptyIsNotThisOutagesDoing(t *testing.T) {
	fixture := []Structure{
		{Kind: StructureVLAN, ID: "v0", Name: "declared-and-empty"},
		{Kind: StructureVLAN, ID: "v30", Name: "workloads", AssetIDs: []string{"sw-a"}},
	}
	out := analyseStructures(fixture, map[string]bool{"sw-a": true})

	if _, ok := findingFor(t, out, "declared-and-empty"); ok {
		t.Error("a VLAN with no ports at all is reported as emptied by this outage; " +
			"it was empty before and the overview already says so as a standing gap")
	}
	if _, ok := findingFor(t, out, "workloads"); !ok {
		t.Error("workloads is not reported, so this test proved nothing")
	}
}

// TestAnEstateWithNoStructuresIsInert. The feature must cost nothing on an
// estate that has declared none -- the same promise the reach model makes.
func TestAnEstateWithNoStructuresIsInert(t *testing.T) {
	if out := analyseStructures(nil, map[string]bool{"sw-a": true}); out != nil {
		t.Errorf("an estate with no declared structures produced %+v", out)
	}
}

// TestTheDetailNamesTheKind. A row saying "0 of 2" needs the reader to know
// which is worse; the sentence is assembled here because templates must not
// decide what is wrong.
func TestTheDetailNamesTheKind(t *testing.T) {
	out := analyseStructures(structureFixture(), map[string]bool{"sw-a": true, "sw-b": true, "fw-a": true})
	for _, f := range out {
		if f.Detail == "" {
			t.Errorf("%s (%s) has no detail sentence", f.Name, f.Kind)
		}
		if f.Href == "" {
			t.Errorf("%s (%s) links nowhere, so the finding cannot be opened", f.Name, f.Kind)
		}
	}
}
