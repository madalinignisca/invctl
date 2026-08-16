// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain_test

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

func occupancy(parts ...int) domain.Occupancy {
	o := domain.Occupancy{AssetID: "vm-shared", AssetName: "vm-shared"}
	for i, p := range parts {
		o.Occupants = append(o.Occupants, domain.Occupant{
			ProjectID: string(rune('a' + i)), Percent: p,
		})
	}
	return o
}

// TestASharedMachineDividesByDeclaredShare. The case ownership cannot describe:
// four clients in one VM, packed together to save on licensing.
func TestASharedMachineDividesByDeclaredShare(t *testing.T) {
	o := occupancy(40, 30, 30)
	got := o.Split(10)
	if got["a"] != 4 || got["b"] != 3 || got["c"] != 3 {
		t.Errorf("10 vCPU split %v, want 4/3/3", got)
	}
	if !o.Balanced() {
		t.Error("40 + 30 + 30 is not reported as balanced")
	}
}

// TestAnUnderDeclaredOccupancyLeavesTheRestToNobody.
//
// §5.4: a total that is not 100 is a finding, not a silent rounding.
// Normalising 90 up to 100 would inflate every declared share by a ninth and
// leave nothing on any page to notice.
func TestAnUnderDeclaredOccupancyLeavesTheRestToNobody(t *testing.T) {
	o := occupancy(50, 40) // 90%
	if o.Balanced() {
		t.Error("a 90% occupancy reports itself balanced")
	}
	got := o.Split(100)
	if got["a"] != 50 || got["b"] != 40 {
		t.Errorf("90%% declared split %v, want 50/40 with ten unattributed", got)
	}
	if total := got["a"] + got["b"]; total != 90 {
		t.Errorf("the parts total %d, want 90 -- the rest belongs to nobody and "+
			"must stay visible", total)
	}
}

// TestAnOverDeclaredOccupancyNeverExceedsTheMachine. The arithmetic has to
// survive a state the estate can really be in; the finding is what tells
// somebody to fix it.
func TestAnOverDeclaredOccupancyNeverExceedsTheMachine(t *testing.T) {
	o := occupancy(60, 60) // 120%
	if o.Balanced() {
		t.Error("a 120% occupancy reports itself balanced")
	}
	got := o.Split(100)
	if total := got["a"] + got["b"]; total > 100 {
		t.Errorf("the parts total %d, which is more machine than exists", total)
	}
}

// TestTheSplitNeverLosesAUnit. Thirds are the case that breaks naive division,
// and money is the case where losing one is noticed.
func TestTheSplitNeverLosesAUnit(t *testing.T) {
	cases := [][]int{{34, 33, 33}, {50, 50}, {99, 1}, {25, 25, 25, 25}}
	for _, parts := range cases {
		o := occupancy(parts...)
		for _, amount := range []int{7, 10, 100, 1} {
			got, total := o.Split(amount), 0
			for _, v := range got {
				total += v
			}
			if total != amount {
				t.Errorf("%v splitting %d totals %d, want exactly %d",
					parts, amount, total, amount)
			}
		}
		var money int64
		for _, v := range o.SplitMinor(99_999) {
			money += v
		}
		if money != 99_999 {
			t.Errorf("%v splitting money totals %d, want exactly 99999", parts, money)
		}
	}
}

// TestAnOccupantListIsValidatedBeforeItIsWritten. A total that is not 100 is
// deliberately NOT an error: refusing it would stop somebody recording the two
// occupants they know about while they chase the third.
func TestAnOccupantListIsValidatedBeforeItIsWritten(t *testing.T) {
	if err := domain.ValidateOccupants([]domain.Occupant{
		{ProjectID: "a", Percent: 50}, {ProjectID: "b", Percent: 20},
	}); err != nil {
		t.Errorf("an incomplete but honest occupancy was refused: %v", err)
	}
	for _, bad := range [][]domain.Occupant{
		{{ProjectID: "a", Percent: 0}},
		{{ProjectID: "a", Percent: 101}},
		{{ProjectID: "a", Percent: 50}, {ProjectID: "a", Percent: 50}},
		{{ProjectID: "", Percent: 50}},
	} {
		if err := domain.ValidateOccupants(bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}
