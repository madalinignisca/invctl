// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Three findings from the three declared numbers (WP-J7).
//
// THREE AUDIENCES, WHICH IS WHY ONE NUMBER COULD NEVER HAVE ANSWERED THEM.
//
//	a project allocated above what it was priced for  -> commercial
//	provisioning above the safe overcommit ratio      -> operational
//	priced-for totals above physical capacity         -> planning
//
// NONE OF THIS NEEDS MONEY, which is why it ships before attribution. It is
// arithmetic over capacity that has already been declared.
//
// THE LAST ONE IS INVISIBLE ON EVERY UTILISATION DASHBOARD. Clusters sit at
// 30-40% CPU and 60-70% memory, look entirely healthy, and are abusively
// oversubscribed underneath -- because utilisation measures what is being
// TAKEN, never what could be CLAIMED. Whether every engagement could be served
// at once is a planning question, and declared figures are the only thing that
// can answer it.

// CapacityFinding is one thing worth somebody's attention.
type CapacityFinding struct {
	Kind     string
	Severity string
	// Subject is the cluster or project it is about.
	Subject string
	// SubjectID and Href send a reader to the page that explains it.
	SubjectID string
	Href      string
	// Detail carries the arithmetic, because a verdict nobody can check is a
	// verdict nobody acts on. Same rule the physical-fit findings follow.
	Detail string
}

// The kinds, so a caller can count them without matching on prose.
const (
	// CapacityOverPriced: an engagement has grown past its own quote.
	CapacityOverPriced = "over_priced_for"
	// CapacityOversubscribed: more promised to guests than the ratio permits.
	CapacityOversubscribed = "oversubscribed"
	// CapacitySoldBeyondEstate: more priced-for than the estate can host.
	CapacitySoldBeyondEstate = "sold_beyond_estate"
	// The gaps, which are findings about the inventory rather than the estate.
	CapacityUnmeasured  = "unmeasured_hosts"
	CapacityUnallocated = "unallocated_workloads"
	// CapacityUnbalancedOccupancy: shares of a machine that do not total 100.
	CapacityUnbalancedOccupancy = "unbalanced_occupancy"
)

// CapacityFindings gathers all of them.
func (s *SQLStore) CapacityFindings(ctx context.Context) ([]CapacityFinding, error) {
	var out []CapacityFinding

	clusters, err := s.ListClusters(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing clusters for capacity findings: %w", err)
	}

	// Estate totals, for the third finding. Only measured hosts contribute, so
	// this is a LOWER BOUND on what the estate can host -- which is the safe
	// direction: it can report having sold too much and cannot report having
	// room it does not have.
	estateVCPU, estateMemory := 0, 0
	for _, c := range clusters {
		cap, err := s.ClusterCapacityFor(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		if cap.Hosts == 0 {
			continue
		}
		estateVCPU += cap.UsableVCPU
		estateMemory += cap.UsableMemoryMB

		href := "/clusters/" + c.ID
		if cap.CPUOversubscribed() {
			out = append(out, CapacityFinding{
				Kind: CapacityOversubscribed, Severity: domain.FindingRiskSeverity,
				Subject: c.Name, SubjectID: c.ID, Href: href,
				Detail: fmt.Sprintf("%d vCPU promised to guests against %d usable "+
					"at %s overcommit — judged on what is provisioned, not what is billed",
					cap.ProvisionedVCPU, cap.UsableVCPU, cap.OvercommitRatio()),
			})
		}
		if cap.MemoryOversubscribed() {
			out = append(out, CapacityFinding{
				Kind: CapacityOversubscribed, Severity: domain.FindingFaultSeverity,
				Subject: c.Name, SubjectID: c.ID, Href: href,
				Detail: fmt.Sprintf("%d MB promised against %d MB usable, and memory "+
					"is not overcommitted", cap.ProvisionedMemoryMB, cap.UsableMemoryMB),
			})
		}
		if cap.UnmeasuredHosts > 0 {
			out = append(out, CapacityFinding{
				Kind: CapacityUnmeasured, Severity: domain.FindingGapSeverity,
				Subject: c.Name, SubjectID: c.ID, Href: href,
				Detail: fmt.Sprintf("%d of %d hosts have no recorded size, so every "+
					"capacity figure for this cluster is a floor",
					cap.UnmeasuredHosts, cap.Hosts),
			})
		}
		if cap.UnallocatedWorkloads > 0 {
			out = append(out, CapacityFinding{
				Kind: CapacityUnallocated, Severity: domain.FindingGapSeverity,
				Subject: c.Name, SubjectID: c.ID, Href: href,
				Detail: fmt.Sprintf("%d workload(s) have no allocation recorded, so "+
					"their cost cannot be attributed to anything",
					cap.UnallocatedWorkloads),
			})
		}
	}

	// The commercial one. A project's allocation against what it was priced on.
	priced, err := s.projectAllocations(ctx)
	if err != nil {
		return nil, err
	}
	soldVCPU, soldMemory := 0, 0
	for _, p := range priced {
		if p.PricedForVCPU != nil {
			soldVCPU += *p.PricedForVCPU
		}
		if p.PricedForMemoryMB != nil {
			soldMemory += *p.PricedForMemoryMB
		}
		href := "/projects/" + p.ID
		if p.PricedForVCPU != nil && p.AllocatedVCPU > *p.PricedForVCPU {
			out = append(out, CapacityFinding{
				Kind: CapacityOverPriced, Severity: domain.FindingRiskSeverity,
				Subject: p.Code, SubjectID: p.ID, Href: href,
				Detail: fmt.Sprintf("using %d vCPU against the %d it was priced for "+
					"— nobody is in breach, the margin is eroding",
					p.AllocatedVCPU, *p.PricedForVCPU),
			})
		}
		if p.PricedForMemoryMB != nil && p.AllocatedMemoryMB > *p.PricedForMemoryMB {
			out = append(out, CapacityFinding{
				Kind: CapacityOverPriced, Severity: domain.FindingRiskSeverity,
				Subject: p.Code, SubjectID: p.ID, Href: href,
				Detail: fmt.Sprintf("using %d MB against the %d MB it was priced for",
					p.AllocatedMemoryMB, *p.PricedForMemoryMB),
			})
		}
	}

	// Shares of a shared machine that do not total 100 (WP-J5, §5.4). A
	// FINDING RATHER THAN A SILENT ROUNDING: normalising 90 up to 100 would
	// inflate every declared share by a ninth, and there would be nothing on
	// any page to notice.
	shared, err := s.AllOccupancy(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering occupancy findings: %w", err)
	}
	for assetID, occ := range shared {
		if occ.Balanced() {
			continue
		}
		declared := occ.DeclaredPercent()
		detail := fmt.Sprintf("%d%% of it is spoken for, so the remaining %d%% is "+
			"attributed to nobody", declared, 100-declared)
		severity := domain.FindingGapSeverity
		if declared > 100 {
			// Over-declaring is a different mistake from not finishing: two
			// people have each been told they have most of the machine.
			detail = fmt.Sprintf("its occupants claim %d%% between them, which is "+
				"more machine than exists", declared)
			severity = domain.FindingRiskSeverity
		}
		name := assetID
		if a, err := s.GetAsset(ctx, assetID); err == nil {
			name = a.Name
		}
		out = append(out, CapacityFinding{
			Kind: CapacityUnbalancedOccupancy, Severity: severity,
			Subject: name, SubjectID: assetID, Href: "/assets/" + assetID,
			Detail: detail,
		})
	}

	// The planning one, and the reason this package exists.
	if estateVCPU > 0 && soldVCPU > estateVCPU {
		out = append(out, CapacityFinding{
			Kind: CapacitySoldBeyondEstate, Severity: domain.FindingFaultSeverity,
			Subject: "the estate", Href: "/clusters",
			Detail: fmt.Sprintf("%d vCPU priced across all engagements against %d "+
				"usable — every one could not be served at once, whatever today's "+
				"utilisation looks like", soldVCPU, estateVCPU),
		})
	}
	if estateMemory > 0 && soldMemory > estateMemory {
		out = append(out, CapacityFinding{
			Kind: CapacitySoldBeyondEstate, Severity: domain.FindingFaultSeverity,
			Subject: "the estate", Href: "/clusters",
			Detail: fmt.Sprintf("%d MB priced across all engagements against %d MB usable",
				soldMemory, estateMemory),
		})
	}
	return out, nil
}

// projectAllocation is what one project has been priced for and what it uses.
type projectAllocation struct {
	ID                string `db:"id"`
	Code              string `db:"code"`
	PricedForVCPU     *int   `db:"priced_for_vcpu"`
	PricedForMemoryMB *int   `db:"priced_for_memory_mb"`
	AllocatedVCPU     int
	AllocatedMemoryMB int
}

// projectAllocations sums what each project's OWNED assets are allocated.
//
// OWNED, NEVER USED, and the asymmetry is the same one the cost rollup follows:
// what is inside somebody else's hypervisor is their footprint. A project that
// merely uses a shared box has not been sold that box's capacity, and counting
// it would report every tenant of a shared cluster as over its quote.
func (s *SQLStore) projectAllocations(ctx context.Context) ([]projectAllocation, error) {
	var projects []projectAllocation
	if err := s.read(ctx, &projects, `
		SELECT id, code, priced_for_vcpu, priced_for_memory_mb
		FROM project WHERE lifecycle <> ?`, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing projects for capacity findings: %w", err)
	}

	for i := range projects {
		var sums struct {
			VCPU   *int `db:"vcpu"`
			Memory *int `db:"memory"`
		}
		// Owned assets AND everything inside them, which is how a project that
		// owns a hypervisor is charged for its guests. asset_closure rather
		// than a parent_id walk, per CLAUDE.md.
		if err := s.readOne(ctx, &sums, `
			SELECT SUM(g.vcpu_allocated) AS vcpu, SUM(g.memory_allocated_mb) AS memory
			FROM project_asset pa
			JOIN asset_closure cl ON cl.ancestor_id = pa.asset_id
			JOIN asset g ON g.id = cl.descendant_id
			JOIN asset_kind k ON k.code = g.kind
			WHERE pa.project_id = ? AND pa.relation = ? AND pa.lifecycle = ?
			  AND g.lifecycle <> ? AND k.can_host_instances = TRUE`,
			projects[i].ID, domain.ProjectOwns, domain.LifecycleActive,
			domain.LifecycleRetired); err != nil {
			return nil, fmt.Errorf("summing allocations for %s: %w", projects[i].Code, err)
		}
		if sums.VCPU != nil {
			projects[i].AllocatedVCPU = *sums.VCPU
		}
		if sums.Memory != nil {
			projects[i].AllocatedMemoryMB = *sums.Memory
		}
	}
	return projects, nil
}
