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

func hosts(n, cores, memMB int) []domain.HostCapacity {
	out := make([]domain.HostCapacity, n)
	for i := range out {
		c, m := cores, memMB
		out[i] = domain.HostCapacity{CPUCores: &c, MemoryMB: &m}
	}
	return out
}

func ptr(v int) *int { return &v }

// TestUsableCapacityIsMeasuredOnTheSurvivors.
//
// This is the arithmetic the whole group rests on, and it is the worked example
// from docs/COST-ATTRIBUTION.md §5.7: three nodes of 32 cores and 256 GB that
// must survive losing one, CPU overcommitted 3:1. The availability premium
// falls out of dividing by what SURVIVES rather than being applied as a
// remembered multiplier.
func TestUsableCapacityIsMeasuredOnTheSurvivors(t *testing.T) {
	got := domain.ComputeCapacity(hosts(3, 32, 262144), nil, 2, ptr(300))

	if got.RawCores != 96 {
		t.Errorf("raw cores = %d, want 96", got.RawCores)
	}
	// 96 cores * 2/3 survivors = 64, * 3.0 overcommit = 192 sellable vCPU.
	if got.UsableVCPU != 192 {
		t.Errorf("usable vCPU = %d, want 192 (64 surviving cores at 3:1)", got.UsableVCPU)
	}
	// Memory is NOT overcommitted: 768 GB * 2/3 = 512 GB.
	if got.UsableMemoryMB != 524288 {
		t.Errorf("usable memory = %d MB, want 524288 (512 GB); memory must not "+
			"take the CPU overcommit ratio", got.UsableMemoryMB)
	}
}

// TestAClusterThatMustKeepEveryHostHasNoSpare. The "not survivable" case the
// estate already reports: HA is configured and cannot help.
func TestAClusterThatMustKeepEveryHostHasNoSpare(t *testing.T) {
	got := domain.ComputeCapacity(hosts(3, 32, 262144), nil, 3, ptr(100))
	if got.UsableVCPU != 96 {
		t.Errorf("usable vCPU = %d, want 96: needing all three hosts means no "+
			"capacity is lost to redundancy", got.UsableVCPU)
	}
}

// TestAnUndeclaredOvercommitIsOneToOne.
//
// The pessimistic default is deliberate: assuming an estate overcommits when it
// has not said so invents headroom nobody agreed to and makes an oversubscribed
// cluster look fine.
func TestAnUndeclaredOvercommitIsOneToOne(t *testing.T) {
	got := domain.ComputeCapacity(hosts(2, 16, 65536), nil, 2, nil)
	if got.OvercommitDeclared {
		t.Error("an absent ratio reports as declared")
	}
	if got.UsableVCPU != 32 {
		t.Errorf("usable vCPU = %d, want 32 at the pessimistic 1:1 default", got.UsableVCPU)
	}
}

// TestAnUnmeasuredHostIsCountedNotGuessed.
//
// A cluster where two of five hosts have no recorded size is not a cluster with
// three hosts' capacity -- it is one nobody has finished measuring, and a total
// that quietly omits them under-reports while looking exact.
func TestAnUnmeasuredHostIsCountedNotGuessed(t *testing.T) {
	hs := hosts(2, 16, 65536)
	hs = append(hs, domain.HostCapacity{Name: "unmeasured"})
	got := domain.ComputeCapacity(hs, nil, 3, ptr(100))

	if got.UnmeasuredHosts != 1 {
		t.Errorf("unmeasured = %d, want 1", got.UnmeasuredHosts)
	}
	if got.Complete() {
		t.Error("a cluster with an unmeasured host reports as complete")
	}
	if got.RawCores != 32 {
		t.Errorf("raw cores = %d, want 32: the unmeasured host contributes "+
			"nothing rather than an invented figure", got.RawCores)
	}
}

// TestOversubscriptionIsJudgedOnProvisioningNotAllocation.
//
// Allocation is a billing figure somebody agreed; provisioning is what the
// hypervisor hands out under contention. A cluster is oversubscribed by what it
// promised its guests, not by what it charged for them.
func TestOversubscriptionIsJudgedOnProvisioningNotAllocation(t *testing.T) {
	// 2 hosts x 8 cores, both must survive, no overcommit: 16 usable vCPU.
	claims := []domain.WorkloadClaim{
		{VCPUAllocated: ptr(4), VCPUProvisioned: ptr(12)},
		{VCPUAllocated: ptr(4), VCPUProvisioned: ptr(12)},
	}
	got := domain.ComputeCapacity(hosts(2, 8, 32768), claims, 2, ptr(100))

	if got.AllocatedVCPU != 8 {
		t.Errorf("allocated = %d, want 8", got.AllocatedVCPU)
	}
	if got.ProvisionedVCPU != 24 {
		t.Errorf("provisioned = %d, want 24", got.ProvisionedVCPU)
	}
	if !got.CPUOversubscribed() {
		t.Error("24 vCPU provisioned against 16 usable is not reported as " +
			"oversubscribed; judging on the 8 allocated would have hidden it")
	}
	// And the gap between the two is capacity nobody is paying for.
	if got.UnfundedVCPU() != 16 {
		t.Errorf("unfunded = %d, want 16", got.UnfundedVCPU())
	}
}

// TestAWorkloadWithNoCapProvisionsWhatItWasAllocated. Counting it as zero would
// report the cluster as roomier than it is.
func TestAWorkloadWithNoCapProvisionsWhatItWasAllocated(t *testing.T) {
	claims := []domain.WorkloadClaim{{VCPUAllocated: ptr(6), MemoryAllocatedMB: ptr(8192)}}
	got := domain.ComputeCapacity(hosts(1, 8, 32768), claims, 1, ptr(100))
	if got.ProvisionedVCPU != 6 {
		t.Errorf("provisioned = %d, want 6 (falls back to the allocation)", got.ProvisionedVCPU)
	}
	if got.UnfundedVCPU() != 0 {
		t.Errorf("unfunded = %d, want 0: nothing was handed out beyond what is paid for",
			got.UnfundedVCPU())
	}
}

// TestAnUnallocatedWorkloadIsReported. Its cost cannot be attributed and its
// claim cannot be counted, and both facts belong on the page.
func TestAnUnallocatedWorkloadIsReported(t *testing.T) {
	claims := []domain.WorkloadClaim{{Name: "vm-mystery"}}
	got := domain.ComputeCapacity(hosts(1, 8, 32768), claims, 1, ptr(100))
	if got.UnallocatedWorkloads != 1 {
		t.Errorf("unallocated = %d, want 1", got.UnallocatedWorkloads)
	}
	if got.Complete() {
		t.Error("a cluster with an unallocated workload reports as complete")
	}
}

// TestAnUndeclaredSurvivorFloorIsSaidRatherThanAssumed.
//
// A nil MinHosts means two different things. The impact engine reads it
// optimistically -- any single survivor carries the guests -- and that is right
// for "what breaks". Reading it the same way for capacity would apply no
// redundancy premium at all and make the cluster look cheaper per unit than it
// is. So capacity uses every host and says the premium is not knowable.
func TestAnUndeclaredSurvivorFloorIsSaidRatherThanAssumed(t *testing.T) {
	// survivors <= 0 is how the store signals "MinHosts was nil".
	got := domain.ComputeCapacity(hosts(3, 32, 262144), nil, 0, ptr(100))

	if got.SurvivorsDeclared {
		t.Error("an undeclared floor reports as declared")
	}
	if got.RedundancyPremium() != 0 {
		t.Errorf("premium = %d%% with no declared floor; it must not be invented",
			got.RedundancyPremium())
	}
	if got.Complete() {
		t.Error("a cluster with no declared floor reports as fully known")
	}
	// And capacity still totals, so a caller gets a usable figure with a caveat
	// rather than nothing at all.
	if got.UsableVCPU != 96 {
		t.Errorf("usable = %d, want 96 over all three hosts", got.UsableVCPU)
	}
}

// TestTheRedundancyPremiumIsReportedNotFolded. Three hosts surviving as two is
// 50%: the same money buys two thirds of the capacity.
func TestTheRedundancyPremiumIsReportedNotFolded(t *testing.T) {
	got := domain.ComputeCapacity(hosts(3, 32, 262144), nil, 2, ptr(100))
	if !got.SurvivorsDeclared {
		t.Fatal("a declared floor reports as undeclared")
	}
	if got.RedundancyPremium() != 50 {
		t.Errorf("premium = %d%%, want 50%%", got.RedundancyPremium())
	}
}
