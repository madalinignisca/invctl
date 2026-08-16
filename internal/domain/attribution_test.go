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

// TestTheWorkedExampleFromTheSpecification.
//
// COST-ATTRIBUTION.md §5.7 is an acceptance test written as prose, and it says
// so: "if the model cannot produce numbers a person who knows this estate would
// recognise, the model is wrong -- that is what this example is for." It is
// driven through ComputeCapacity rather than by handing Divide the sellable
// figures, because the claim being tested is the whole chain: three nodes, one
// permitted loss and a declared 3:1 ratio produce 192 sellable vCPU, and ten
// allocated vCPU is 5.2% of it.
//
// Storage is two of the four rows and is not modelled yet, so this covers the
// two the estate can currently answer. The rows it cannot are asserted absent
// rather than quietly omitted -- see the sibling test.
func TestTheWorkedExampleFromTheSpecification(t *testing.T) {
	// Three nodes, 32 cores and 256 GB each.
	cores, memory := 32, 256*1024
	hosts := []domain.HostCapacity{
		{AssetID: "n1", Name: "node-1", CPUCores: &cores, MemoryMB: &memory},
		{AssetID: "n2", Name: "node-2", CPUCores: &cores, MemoryMB: &memory},
		{AssetID: "n3", Name: "node-3", CPUCores: &cores, MemoryMB: &memory},
	}
	// Two application servers at 4 vCPU / 8 GB, one database at 2 / 16.
	claim := func(v, m int) domain.WorkloadClaim {
		return domain.WorkloadClaim{VCPUAllocated: &v, MemoryAllocatedMB: &m}
	}
	gb := 1024
	workloads := []domain.WorkloadClaim{
		claim(4, 8*gb), claim(4, 8*gb), claim(2, 16*gb),
	}
	overcommit := 300 // 3:1, declared
	cap := domain.ComputeCapacity(hosts, workloads, 2, &overcommit)

	// The division: surviving one node leaves 64 cores and 512 GB.
	if cap.UsableVCPU != 192 {
		t.Errorf("sellable vCPU is %d, want 192 (64 surviving cores at 3:1)", cap.UsableVCPU)
	}
	if cap.UsableMemoryMB != 512*gb {
		t.Errorf("sellable memory is %d MB, want %d (memory is not overcommitted)",
			cap.UsableMemoryMB, 512*gb)
	}

	cases := []struct {
		dimension string
		unit      string
		sellable  int
		claimed   int
		// wantPoints is the spec's percentage in basis points.
		// wantText is how it renders.
		wantPoints int
		wantText   string
	}{
		// 10/192 is 5.2083%, which the spec quotes at one decimal as 5.2% and
		// this renders at two as 5.21%. The same number, not a disagreement --
		// asserted at the precision the code actually produces so that a
		// change in the rounding rule is caught rather than absorbed.
		{"CPU", "vCPU", cap.UsableVCPU, 10, 521, "5.21"},
		{"Memory", "MB", cap.UsableMemoryMB, 32 * gb, 625, "6.25"},
	}
	for _, tc := range cases {
		t.Run(tc.dimension, func(t *testing.T) {
			d := domain.Divide(tc.dimension, tc.unit, tc.sellable, []domain.Claim{
				{SubjectID: "p1", Subject: "the project", Amount: tc.claimed},
			})
			if len(d.Shares) != 1 {
				t.Fatalf("got %d shares, want 1", len(d.Shares))
			}
			// Asserted exactly rather than within a tolerance: a tolerance
			// here would have accepted the single blended figure this example
			// exists to rule out.
			if d.Shares[0].BasisPoints != tc.wantPoints {
				t.Errorf("share is %d basis points (%s%%), want %d (%s%%)",
					d.Shares[0].BasisPoints, d.Shares[0].Percent(),
					tc.wantPoints, tc.wantText)
			}
			if got := d.Shares[0].Percent(); got != tc.wantText {
				t.Errorf("rendered as %s%%, want %s%%", got, tc.wantText)
			}
		})
	}

	// AND THE POINT OF THE EXAMPLE: memory binds, not CPU. A single blended
	// number would have hidden which dimension runs out first, which is the
	// one thing a capacity conversation is about.
	cpu := domain.Divide("CPU", "vCPU", cap.UsableVCPU,
		[]domain.Claim{{SubjectID: "p1", Amount: 10}})
	mem := domain.Divide("Memory", "MB", cap.UsableMemoryMB,
		[]domain.Claim{{SubjectID: "p1", Amount: 32 * gb}})
	if mem.Shares[0].BasisPoints <= cpu.Shares[0].BasisPoints {
		t.Error("memory does not bind harder than CPU, so the example's whole " +
			"lesson has been lost")
	}
}

// TestEverySliceSumsToTheWhole.
//
// §5.3, and the reason it is a rule rather than a nicety: "a report whose
// slices do not sum to the invoice is worse than no report, because somebody
// will put it in a board pack." Thirds are the case that breaks naive
// rounding -- 33.33% three times is 99.99%.
func TestEverySliceSumsToTheWhole(t *testing.T) {
	cases := []struct {
		name     string
		sellable int
		claims   []int
	}{
		{"three equal thirds", 300, []int{100, 100, 100}},
		{"thirds of an odd pool", 100, []int{33, 33, 33}},
		{"one claimant, most idle", 1000, []int{7}},
		{"nothing claimed at all", 500, nil},
		{"seven awkward claims", 1000, []int{143, 143, 143, 143, 143, 143, 142}},
		{"claimed exactly to the edge", 64, []int{16, 16, 16, 16}},
		{"more claimed than exists", 100, []int{80, 80}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := make([]domain.Claim, len(tc.claims))
			for i, a := range tc.claims {
				claims[i] = domain.Claim{SubjectID: string(rune('a' + i)), Amount: a}
			}
			d := domain.Divide("CPU", "vCPU", tc.sellable, claims)

			total := 0
			for _, s := range d.Total() {
				total += s.BasisPoints
			}
			if total != 10000 {
				t.Errorf("the slices sum to %d basis points, want exactly 10000", total)
			}
			// And the money, which rounds separately and must also be exact.
			const bill = int64(420_000) // €4,200.00 in cents
			var money int64
			for _, part := range d.ApportionMinor(bill) {
				money += part
			}
			if money != bill {
				t.Errorf("the apportioned money sums to %d, want exactly %d", money, bill)
			}
		})
	}
}

// TestIdleCapacityIsASliceAndNeverNegative.
//
// §5.3 again: whatever does not reach a project is headroom and is shown, never
// dropped. The oversubscribed case is the one that tempts an implementation
// into a negative slice -- more claimed than exists is a real state this estate
// reports as a finding, so the arithmetic has to survive it.
func TestIdleCapacityIsASliceAndNeverNegative(t *testing.T) {
	t.Run("headroom is its own slice", func(t *testing.T) {
		d := domain.Divide("CPU", "vCPU", 100, []domain.Claim{
			{SubjectID: "p1", Subject: "orders", Amount: 30},
		})
		if d.Idle.Amount != 70 {
			t.Errorf("idle is %d, want 70", d.Idle.Amount)
		}
		if d.Idle.BasisPoints != 7000 {
			t.Errorf("idle is %d basis points, want 7000", d.Idle.BasisPoints)
		}
		if d.Idle.Subject != domain.IdleSubject {
			t.Errorf("the idle slice is called %q", d.Idle.Subject)
		}
		if len(d.Total()) != 2 {
			t.Errorf("the chart has %d slices, want the project and the headroom",
				len(d.Total()))
		}
	})

	t.Run("oversubscribed has no headroom and no negative", func(t *testing.T) {
		d := domain.Divide("CPU", "vCPU", 100, []domain.Claim{
			{SubjectID: "p1", Subject: "orders", Amount: 80},
			{SubjectID: "p2", Subject: "platform", Amount: 80},
		})
		if d.Idle.Amount != 0 {
			t.Errorf("idle is %d on an oversubscribed cluster, want 0", d.Idle.Amount)
		}
		if !d.Oversubscribed() {
			t.Error("a cluster claimed beyond its capacity does not say so")
		}
		// The cost still lands entirely on the claimants.
		if d.Shares[0].BasisPoints != 5000 || d.Shares[1].BasisPoints != 5000 {
			t.Errorf("two equal claims split %d/%d, want 5000/5000",
				d.Shares[0].BasisPoints, d.Shares[1].BasisPoints)
		}
	})

	t.Run("an unmeasured cluster divides nothing", func(t *testing.T) {
		d := domain.Divide("CPU", "vCPU", 0, nil)
		if len(d.Shares) != 0 || d.Idle.BasisPoints != 0 {
			t.Error("a cluster nobody has measured produced shares anyway")
		}
		for _, part := range d.ApportionMinor(100_000) {
			if part != 0 {
				t.Error("money was apportioned against capacity nobody has measured")
			}
		}
	})
}

// TestTheLargestClaimComesFirst. A reader looking for who dominates a cluster
// should not have to sort it, and the order must not change between two
// requests that returned the same data.
func TestTheLargestClaimComesFirst(t *testing.T) {
	d := domain.Divide("CPU", "vCPU", 1000, []domain.Claim{
		{SubjectID: "a", Subject: "small", Amount: 10},
		{SubjectID: "b", Subject: "largest", Amount: 500},
		{SubjectID: "c", Subject: "middle", Amount: 100},
		{SubjectID: "d", Subject: "also-small", Amount: 10},
	})
	want := []string{"largest", "middle", "also-small", "small"}
	for i, w := range want {
		if d.Shares[i].Subject != w {
			t.Errorf("slice %d is %q, want %q", i, d.Shares[i].Subject, w)
		}
	}
}

// TestEverySliceCarriesItsBasis. §6: without it a later switch of basis gives
// two months of one meaning, a flip, and a discontinuity nobody can explain a
// year later.
func TestEverySliceCarriesItsBasis(t *testing.T) {
	d := domain.Divide("CPU", "vCPU", 100, []domain.Claim{{SubjectID: "p1", Amount: 10}})
	if d.Basis != domain.BasisAllocated {
		t.Errorf("the division is stamped %q, want %q", d.Basis, domain.BasisAllocated)
	}
}

// TestAPerConsumerCostDividesPerHeadNotByCapacity.
//
// §5.6's third shape. A backup licence per virtual machine costs the same for a
// 64 GB machine as for a 2 GB one, so a project owning three of five covered
// machines pays three fifths -- regardless of how large those machines are.
// Dividing this by capacity share reconciles perfectly and is wrong, which is
// the failure mode the whole section exists for.
func TestAPerConsumerCostDividesPerHeadNotByCapacity(t *testing.T) {
	d := domain.DivideEqually("Backup licence", []domain.Claim{
		{SubjectID: "p1", Subject: "orders", Amount: 3},   // three covered machines
		{SubjectID: "p2", Subject: "platform", Amount: 2}, // two, but much larger
	})
	if d.Shares[0].BasisPoints != 6000 || d.Shares[1].BasisPoints != 4000 {
		t.Errorf("three of five machines and two of five split %d/%d, want 6000/4000",
			d.Shares[0].BasisPoints, d.Shares[1].BasisPoints)
	}
	// A licence has no unclaimed portion: every one of them was bought for
	// somebody, so there is no headroom slice to render.
	if d.Idle.Amount != 0 {
		t.Errorf("a per-consumer cost produced %d units of idle capacity, "+
			"which is not a thing a licence has", d.Idle.Amount)
	}
	// And the money still lands exactly.
	var total int64
	for _, part := range d.ApportionMinor(1_000) {
		total += part
	}
	if total != 1_000 {
		t.Errorf("the licence apportions to %d, want exactly 1000", total)
	}
}

// TestAScopeThatNamesNobodyIsAGapNotUniversal.
//
// The fallback would be laundering: a default wearing the clothes of a
// declaration. A conditional line naming nobody is one whose scope somebody
// started declaring and did not finish, and the two states must not render the
// same way.
func TestAScopeThatNamesNobodyIsAGapNotUniversal(t *testing.T) {
	for _, scope := range []string{domain.CostConditional, domain.CostPerConsumer} {
		if !domain.NeedsConsumers(scope) {
			t.Errorf("%s does not require a named set, so a line with none "+
				"would silently divide across everything", scope)
		}
	}
	if domain.NeedsConsumers(domain.CostUniversal) {
		t.Error("a universal cost demands a consumer list, which it cannot have")
	}
}
