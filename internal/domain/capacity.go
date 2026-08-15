// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"fmt"
	"strconv"
)

// What a cluster can carry, and what has been claimed against it (WP-J3).
//
// SEPARATE FROM cluster.go BECAUSE IT ANSWERS A DIFFERENT QUESTION. cluster.go
// decides what happens when a host is lost -- HA policy, whether the guests
// relocate. This decides how much there is, which is arithmetic over declared
// numbers and has no opinion about failure at all.
//
// EVERY FIGURE CARRIES WHAT IT COULD NOT SEE. A cluster of five hosts where two
// have no recorded size is not a cluster with three hosts' capacity; it is a
// cluster nobody has finished measuring, and a total that quietly omits them
// under-reports capacity while looking exact. Same argument as RackLoad's
// "TotalGrams is a LOWER BOUND" in fit.go, arrived at independently and for the
// same reason.

// DefaultCPUOvercommit is the ratio assumed when a cluster declares none.
//
// 1:1, DELIBERATELY THE PESSIMISTIC ANSWER. Assuming an estate overcommits when
// it has not said so would invent headroom nobody agreed to and make an
// oversubscribed cluster look fine. An operator who overcommits knows they do
// and can say so; one who has not thought about it gets the conservative
// reading, which is the safe direction to be wrong in.
const DefaultCPUOvercommit = 100

// RatioText renders an overcommit stored in hundredths the way an operator
// writes it: 300 is "3", 150 is "1.5", 205 is "2.05".
//
// HERE RATHER THAN IN A TEMPLATE HELPER because three places need it and they
// were disagreeing: the capacity panel printed "250:100", the findings printed
// "2.50:1", and the form field has to offer back exactly what it accepts or
// saving a cluster without touching the ratio is refused. One unit, one
// rendering.
func RatioText(hundredths int) string {
	whole, frac := hundredths/100, hundredths%100
	switch {
	case frac == 0:
		return strconv.Itoa(whole)
	case frac%10 == 0:
		return fmt.Sprintf("%d.%d", whole, frac/10)
	default:
		return fmt.Sprintf("%d.%02d", whole, frac)
	}
}

// OvercommitRatio is the ratio as prose: "3:1".
func (c ClusterCapacity) OvercommitRatio() string { return RatioText(c.Overcommit) + ":1" }

// HostCapacity is one machine's contribution.
type HostCapacity struct {
	AssetID string
	Name    string
	// Nil means nobody has measured this host, which is counted rather than
	// treated as zero.
	CPUCores *int
	MemoryMB *int
}

// Measured reports whether both dimensions are known.
//
// BOTH, NOT EITHER. A host with cores but no memory cannot be divided into
// either total honestly: counting it towards CPU and not memory produces two
// figures with different denominators on one page, which is the sort of thing
// nobody notices and everybody misreads.
func (h HostCapacity) Measured() bool { return h.CPUCores != nil && h.MemoryMB != nil }

// WorkloadClaim is what one guest has been given.
type WorkloadClaim struct {
	AssetID string
	Name    string
	// Provisioned is the hard limit; Allocated is what money is computed on.
	// Either may be absent, and the pair is what makes generosity visible.
	VCPUProvisioned     *int
	VCPUAllocated       *int
	MemoryProvisionedMB *int
	MemoryAllocatedMB   *int
}

// ClusterCapacity is the whole picture for one cluster.
type ClusterCapacity struct {
	// Hosts and Survivors: what exists, and what is left after losing the
	// hosts HA is allowed to lose. Usable capacity is measured on the
	// survivors, because capacity that disappears with a node was never
	// capacity you could sell.
	Hosts     int
	Survivors int
	// Unmeasured hosts are counted, never guessed. Every total below is a
	// LOWER BOUND while this is above zero.
	UnmeasuredHosts int

	// Raw totals over measured hosts.
	RawCores    int
	RawMemoryMB int

	// Usable is what survives a permitted loss, with CPU multiplied by the
	// declared overcommit ratio and memory not.
	UsableVCPU     int
	UsableMemoryMB int

	// Claimed, from the workloads placed on this cluster.
	AllocatedVCPU       int
	AllocatedMemoryMB   int
	ProvisionedVCPU     int
	ProvisionedMemoryMB int
	// Workloads with no allocation recorded. Their cost cannot be attributed
	// and their claim cannot be counted, so both facts are reported.
	UnallocatedWorkloads int

	// Overcommit is the ratio used, in hundredths, and whether it was declared
	// or fell back to DefaultCPUOvercommit.
	Overcommit         int
	OvercommitDeclared bool
	// SurvivorsDeclared says whether cluster.MinHosts was set.
	//
	// IT MATTERS BECAUSE NIL MEANS TWO DIFFERENT THINGS. To the impact engine a
	// nil floor is optimistic -- any single survivor is assumed to carry the
	// guests -- and that reading is right for "what breaks". For capacity it
	// would be optimistic in the other direction: assuming every host's
	// capacity is sellable ignores the redundancy premium entirely and makes a
	// cluster look cheaper per unit than it is.
	//
	// So capacity neither guesses a floor nor silently applies none: it uses
	// every host, and says the premium is not knowable until somebody declares
	// how many must survive.
	SurvivorsDeclared bool
}

// Complete reports whether every host and workload carries its numbers. Only
// then are the totals totals rather than floors.
func (c ClusterCapacity) Complete() bool {
	return c.UnmeasuredHosts == 0 && c.UnallocatedWorkloads == 0 && c.Hosts > 0 &&
		c.SurvivorsDeclared && c.OvercommitDeclared
}

// RedundancyPremium is how much more each usable unit costs because capacity is
// held back for a failure, as a percentage. Zero when nothing is held back, and
// meaningless unless SurvivorsDeclared.
//
// Three hosts that must survive as two is 50%: the same money buys two thirds
// of the capacity. It is reported rather than applied, because a number a
// manager can see is worth more than one folded silently into a rate.
func (c ClusterCapacity) RedundancyPremium() int {
	if !c.SurvivorsDeclared || c.Survivors <= 0 || c.Survivors >= c.Hosts {
		return 0
	}
	return (c.Hosts - c.Survivors) * 100 / c.Survivors
}

// CPUOversubscribed reports whether more vCPU has been provisioned than the
// declared ratio permits.
//
// PROVISIONED, NOT ALLOCATED, and the choice is the point. Allocation is a
// billing figure somebody agreed; provisioning is what the hypervisor will
// actually hand out under contention. A cluster is oversubscribed by what it
// promised the guests, not by what it charged for them.
func (c ClusterCapacity) CPUOversubscribed() bool {
	return c.UsableVCPU > 0 && c.ProvisionedVCPU > c.UsableVCPU
}

// MemoryOversubscribed is the same for memory, which is not overcommitted.
func (c ClusterCapacity) MemoryOversubscribed() bool {
	return c.UsableMemoryMB > 0 && c.ProvisionedMemoryMB > c.UsableMemoryMB
}

// UnfundedVCPU is capacity handed out beyond what anybody is paying for.
//
// It is not waste and it is not headroom: it is a decision somebody made
// without pricing it. Negative is impossible in practice and clamped anyway,
// because a workload allocated more than it is provisioned is a data error
// rather than a negative amount of generosity.
func (c ClusterCapacity) UnfundedVCPU() int {
	if c.ProvisionedVCPU <= c.AllocatedVCPU {
		return 0
	}
	return c.ProvisionedVCPU - c.AllocatedVCPU
}

// UnfundedMemoryMB is the same for memory.
func (c ClusterCapacity) UnfundedMemoryMB() int {
	if c.ProvisionedMemoryMB <= c.AllocatedMemoryMB {
		return 0
	}
	return c.ProvisionedMemoryMB - c.AllocatedMemoryMB
}

// ComputeCapacity totals a cluster's hosts and the claims against it.
//
// survivors is how many hosts must remain -- cluster.MinHosts when it is set,
// and every host when it is not. Usable capacity is measured on THAT many
// hosts rather than on all of them, which is how the availability premium
// falls out of a division instead of a hand-kept multiplier: three hosts that
// must survive as two means everything costs 1.5x per unit, and nobody has to
// remember to apply it.
func ComputeCapacity(hosts []HostCapacity, claims []WorkloadClaim,
	survivors int, overcommit *int) ClusterCapacity {

	c := ClusterCapacity{
		Hosts:              len(hosts),
		Overcommit:         DefaultCPUOvercommit,
		OvercommitDeclared: overcommit != nil,
		SurvivorsDeclared:  survivors > 0 && survivors <= len(hosts),
	}
	if overcommit != nil {
		c.Overcommit = *overcommit
	}

	measured := 0
	for _, h := range hosts {
		if !h.Measured() {
			c.UnmeasuredHosts++
			continue
		}
		measured++
		c.RawCores += *h.CPUCores
		c.RawMemoryMB += *h.MemoryMB
	}

	// Survivors cannot exceed what exists, and a cluster that must keep every
	// host has no spare capacity to lose -- which is the "not survivable" case
	// the estate already reports elsewhere.
	if survivors <= 0 || survivors > len(hosts) {
		survivors = len(hosts)
	}
	c.Survivors = survivors

	// The average measured host, times the survivors. Averaging rather than
	// summing the smallest is deliberate: a cluster is built from like
	// machines, and picking a specific host to lose would make the answer
	// depend on which one -- a question nobody asked and nothing records.
	if measured > 0 && len(hosts) > 0 {
		c.UsableVCPU = c.RawCores * survivors / len(hosts) * c.Overcommit / 100
		c.UsableMemoryMB = c.RawMemoryMB * survivors / len(hosts)
	}

	for _, w := range claims {
		if w.VCPUAllocated == nil && w.MemoryAllocatedMB == nil {
			c.UnallocatedWorkloads++
		}
		if w.VCPUAllocated != nil {
			c.AllocatedVCPU += *w.VCPUAllocated
		}
		if w.MemoryAllocatedMB != nil {
			c.AllocatedMemoryMB += *w.MemoryAllocatedMB
		}
		// Provisioning falls back to the allocation when it is not recorded:
		// a workload nobody capped is one taking what it was given, and
		// counting it as zero would report a cluster as roomier than it is.
		switch {
		case w.VCPUProvisioned != nil:
			c.ProvisionedVCPU += *w.VCPUProvisioned
		case w.VCPUAllocated != nil:
			c.ProvisionedVCPU += *w.VCPUAllocated
		}
		switch {
		case w.MemoryProvisionedMB != nil:
			c.ProvisionedMemoryMB += *w.MemoryProvisionedMB
		case w.MemoryAllocatedMB != nil:
			c.ProvisionedMemoryMB += *w.MemoryAllocatedMB
		}
	}
	return c
}
