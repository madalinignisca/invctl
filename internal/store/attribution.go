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

// Who holds what share of a shared platform (WP-J4).
//
// THE SHARES, NOT YET THE MONEY. docs/COST-ATTRIBUTION.md §5.7's table is four
// percentages and this produces them; dividing actual spend across them needs
// §5.6's conditional costs -- a per-core OS licence benefits only the guests
// running it, so a cost line has to declare which consumers it applies to, and
// that is a schema change of its own. Shipping the shares first is not a
// half-measure: "who is 6% of this cluster" is the question people ask out
// loud, and it is answerable with no money in the system at all.
//
// OWNERSHIP RESOLVES UPWARDS THROUGH CONTAINMENT, never through `uses`. A guest
// belongs to whoever owns the nearest ancestor that anybody owns, which is how
// a project that owns a hypervisor is charged for the guests on it. Deriving
// through `uses` is how one team's estate gets attributed to whoever declared a
// link first -- the asymmetry project.go argues for at length.

// Attribution is one cluster's capacity divided between its claimants.
type Attribution struct {
	ClusterID string
	Cluster   string
	// Capacity is what the divisions were computed from, carried so a page can
	// say what it could not see rather than printing confident percentages
	// over a cluster half of which is unmeasured.
	Capacity *domain.ClusterCapacity
	// Divisions is one per dimension: CPU, memory, and one per storage pool.
	Divisions []domain.Division
}

// Complete reports whether every figure behind these shares was recorded.
func (a Attribution) Complete() bool {
	return a.Capacity != nil && a.Capacity.Complete()
}

// AttributionFor divides a cluster between the projects standing on it.
func (s *SQLStore) AttributionFor(ctx context.Context, clusterID string) (*Attribution, error) {
	cluster, err := s.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	capacity, err := s.ClusterCapacityFor(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	out := &Attribution{ClusterID: clusterID, Cluster: cluster.Name, Capacity: capacity}

	claims, err := s.clusterWorkloadClaims(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	cpu := make([]domain.Claim, 0, len(claims))
	memory := make([]domain.Claim, 0, len(claims))
	for _, c := range claims {
		if c.VCPU > 0 {
			cpu = append(cpu, domain.Claim{
				SubjectID: c.ProjectID, Subject: c.Subject, Amount: c.VCPU,
			})
		}
		if c.MemoryMB > 0 {
			memory = append(memory, domain.Claim{
				SubjectID: c.ProjectID, Subject: c.Subject, Amount: c.MemoryMB,
			})
		}
	}
	out.Divisions = append(out.Divisions,
		domain.Divide("CPU", "vCPU", capacity.UsableVCPU, cpu),
		domain.Divide("Memory", "MB", capacity.UsableMemoryMB, memory))
	return out, nil
}

// PoolAttribution divides one storage pool between the projects holding in it.
//
// SEPARATE FROM THE CLUSTER, because a pool is not owned by one. Block storage
// commonly serves several clusters and bulk storage usually serves everything,
// so folding pools into a cluster's report would attribute one pool's capacity
// to whichever cluster happened to be looked at first.
func (s *SQLStore) PoolAttribution(ctx context.Context, poolID string) (*domain.Division, error) {
	o, err := s.StorageOccupancyFor(ctx, poolID)
	if err != nil {
		return nil, err
	}
	owners, names, err := s.ownersOf(ctx)
	if err != nil {
		return nil, err
	}
	claims := domain.StorageClaims(o.Claims, owners, names)
	d := domain.Divide(o.Pool.Name, "GB", o.Pool.UsableGB(), claims)
	return &d, nil
}

// workloadClaim is one project's draw on one cluster.
type workloadClaim struct {
	ProjectID string
	Subject   string
	VCPU      int
	MemoryMB  int
}

// clusterWorkloadClaims sums what each project has been allocated on a cluster.
func (s *SQLStore) clusterWorkloadClaims(ctx context.Context, clusterID string) ([]workloadClaim, error) {
	// Every guest of every member host, at any depth, excluding the hosts
	// themselves -- the same set ClusterCapacityFor counts, so the shares
	// divide the capacity that page reports rather than a different total.
	var rows []struct {
		AssetID  string `db:"asset_id"`
		VCPU     *int   `db:"vcpu_allocated"`
		MemoryMB *int   `db:"memory_allocated_mb"`
	}
	if err := s.read(ctx, &rows, `
		SELECT DISTINCT g.id AS asset_id, g.vcpu_allocated, g.memory_allocated_mb
		FROM cluster_member m
		JOIN asset_closure cl ON cl.ancestor_id = m.asset_id
		JOIN asset g ON g.id = cl.descendant_id
		JOIN asset_kind k ON k.code = g.kind
		WHERE m.cluster_id = ? AND cl.ancestor_id <> cl.descendant_id
		  AND g.lifecycle <> ? AND k.can_host_instances = TRUE`,
		clusterID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading workloads of cluster %s: %w", clusterID, err)
	}

	owners, names, err := s.ownersOf(ctx)
	if err != nil {
		return nil, err
	}
	byProject := map[string]*workloadClaim{}
	order := []string{}
	for _, r := range rows {
		id := owners[r.AssetID]
		claim, seen := byProject[id]
		if !seen {
			subject := names[id]
			if id == "" {
				subject = domain.UnattributedSubject
			}
			claim = &workloadClaim{ProjectID: id, Subject: subject}
			byProject[id], order = claim, append(order, id)
		}
		if r.VCPU != nil {
			claim.VCPU += *r.VCPU
		}
		if r.MemoryMB != nil {
			claim.MemoryMB += *r.MemoryMB
		}
	}
	out := make([]workloadClaim, 0, len(order))
	for _, id := range order {
		out = append(out, *byProject[id])
	}
	return out, nil
}

// ownersOf maps every asset to the project that owns it or owns something
// containing it, and every project id to its code.
//
// THE NEAREST OWNING ANCESTOR WINS, which is what the depth ordering buys: a
// project owning a specific VM inside a hypervisor another project owns keeps
// that VM, because a more specific declaration is a later decision about a
// smaller thing. Without the ordering the answer would depend on row order,
// which is the kind of bug that shows up as a cost report that changes between
// two identical requests.
func (s *SQLStore) ownersOf(ctx context.Context) (map[string]string, map[string]string, error) {
	var rows []struct {
		AssetID   string `db:"asset_id"`
		ProjectID string `db:"project_id"`
		Depth     int    `db:"depth"`
	}
	if err := s.read(ctx, &rows, `
		SELECT cl.descendant_id AS asset_id, pa.project_id, cl.depth
		FROM project_asset pa
		JOIN asset_closure cl ON cl.ancestor_id = pa.asset_id
		JOIN project p ON p.id = pa.project_id
		WHERE pa.relation = ? AND pa.lifecycle = ? AND p.lifecycle <> ?
		ORDER BY cl.descendant_id, cl.depth`,
		domain.ProjectOwns, domain.LifecycleActive, domain.LifecycleRetired); err != nil {
		return nil, nil, fmt.Errorf("resolving asset ownership: %w", err)
	}
	owners := make(map[string]string, len(rows))
	for _, r := range rows {
		// First row per asset is the shallowest, so the nearest owner sticks.
		if _, taken := owners[r.AssetID]; !taken {
			owners[r.AssetID] = r.ProjectID
		}
	}

	var projects []struct {
		ID   string `db:"id"`
		Code string `db:"code"`
	}
	if err := s.read(ctx, &projects,
		`SELECT id, code FROM project WHERE lifecycle <> ?`, domain.LifecycleRetired); err != nil {
		return nil, nil, fmt.Errorf("resolving project codes: %w", err)
	}
	names := make(map[string]string, len(projects))
	for _, p := range projects {
		names[p.ID] = p.Code
	}
	return owners, names, nil
}
