// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"sort"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestTheDuctHoldsEveryFibreThatLeavesTheRack.
//
// BY NAME, NOT BY COUNT. "Four cables" is satisfied by any four, including
// the three-metre DACs that never leave their cabinet -- and a bundle whose
// membership is approximately right is worse than none, because the whole
// claim a bundle makes is that these specific cables share a fate.
func TestTheDuctHoldsEveryFibreThatLeavesTheRack(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		bundles, err := f.store.ListBundles(f.ctx)
		if err != nil {
			t.Fatalf("listing bundles: %v", err)
		}
		var ductID string
		for _, b := range bundles {
			if b.Code == "duct-a1-b1" {
				ductID = b.ID
			}
		}
		if ductID == "" {
			t.Fatalf("the fixture declares no duct-a1-b1; /bundles is empty and no cable's "+
				"impact page can say what shares its fate. Bundles present: %+v", bundles)
		}

		members, err := f.store.ListBundleMembers(f.ctx, ductID)
		if err != nil {
			t.Fatalf("listing the duct's cables: %v", err)
		}
		got := make([]string, 0, len(members))
		for _, m := range members {
			if m.Lifecycle != domain.LifecycleActive {
				t.Errorf("%s is in the duct but is %s; the fixture has no retired cable "+
					"and should not have grown one here", m.Label(), m.Lifecycle)
			}
			got = append(got, m.Label())
		}
		want := []string{
			"hv-01 eno3 – sw-core-2 Ethernet1",
			"hv-02 eno3 – sw-core-2 Ethernet2",
			"sw-core-1 Ethernet46 – sw-core-2 Ethernet46",
			"sw-core-1 Ethernet47 – sw-core-2 Ethernet47",
		}
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("the duct holds %d cables, want %d: %v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("the duct holds %q where it should hold %q. Every OM4 run in this "+
					"estate leaves the rack and every DAC does not; membership that drifts "+
					"from that is a claim about the plant that is simply false",
					got[i], want[i])
			}
		}
	})
}

// TestCuttingTheDuctJoinsNothingAndSaysSo.
//
// THE COMMENT IN seed_bundles.go IS THE THING UNDER TEST. An earlier draft of
// it claimed the duct was a hidden single point of failure; the engine
// disagreed, and this pins the version that survived. Every one of the duct's
// cables either has both ends inside the sw-core group (the MC-LAG peer bond)
// or ends on a host that forwards nothing, so none of them crosses a
// forwarder-group boundary and the honest answer is "joins nothing".
//
// It is asserted rather than left implicit because the two failure directions
// are opposite and both are silent. If a later seed edit gave the duct a
// boundary-crossing cable, the page would start claiming a partition the
// estate does not have. If bundle membership quietly emptied, the answer would
// be the same "joins nothing" for an entirely different and wrong reason --
// which is why TestTheDuctHoldsEveryFibreThatLeavesTheRack above has to exist
// beside this one rather than instead of it.
func TestCuttingTheDuctJoinsNothingAndSaysSo(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		bundles, err := f.store.ListBundles(f.ctx)
		if err != nil {
			t.Fatalf("listing bundles: %v", err)
		}
		if len(bundles) == 0 {
			t.Fatal("the fixture declares no bundle at all")
		}
		cut, err := f.store.BundleCutEffect(f.ctx, bundles[0].ID)
		if err != nil {
			t.Fatalf("cutting the duct: %v", err)
		}
		if cut.Joins {
			t.Errorf("cutting the duct reports that it joins %+v. Every cable in it is "+
				"either an MC-LAG peer link with both ends in sw-core or a host's uplink, "+
				"so it crosses no group boundary -- and bundle_impact.html's "+
				"\"joins nothing that is modelled\" panel is what the demo needs it to "+
				"reach", cut.JoinedPairs)
		}
		if cut.Separates {
			t.Errorf("cutting the duct claims to separate %+v, which the simulation "+
				"contradicts: hv-01 and hv-02 each keep their first home and the cores "+
				"stay reachable through the edge", cut.SeparatedPairs)
		}
	})
}
