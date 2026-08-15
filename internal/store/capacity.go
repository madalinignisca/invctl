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

// Reading a cluster's capacity out of the estate (WP-J3).
//
// THE WORKLOADS ARE THE GUESTS OF THE MEMBER HOSTS, found through asset_closure
// rather than by walking parent_id in Go -- which is the rule CLAUDE.md states
// for every containment question here. A guest three levels down (a container
// inside a VM inside a host) is still carried by that host and still claims its
// memory.

// ClusterCapacityFor totals what a cluster has and what has been claimed.
func (s *SQLStore) ClusterCapacityFor(ctx context.Context, clusterID string) (*domain.ClusterCapacity, error) {
	cluster, err := s.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	var hostRows []struct {
		AssetID  string `db:"asset_id"`
		Name     string `db:"name"`
		CPUCores *int   `db:"cpu_cores"`
		MemoryMB *int   `db:"memory_mb"`
	}
	if err := s.read(ctx, &hostRows, `
		SELECT a.id AS asset_id, a.name, a.cpu_cores, a.memory_mb
		FROM cluster_member m
		JOIN asset a ON a.id = m.asset_id
		WHERE m.cluster_id = ? AND a.lifecycle <> ?
		ORDER BY a.name`, clusterID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading hosts of %s: %w", clusterID, err)
	}

	// Everything inside a member host, at any depth, excluding the hosts
	// themselves. DISTINCT because a guest reachable through two ancestors is
	// one guest -- the same collapse impliedAssets makes, and for the same
	// reason: counting it twice would double its claim on the cluster.
	//
	// FILTERED BY can_host_instances, WHICH IS A DATA COLUMN AND NOT A GO
	// SWITCH. A bridge is inside a hypervisor and consumes no CPU; counting it
	// reported nine unallocated workloads on a cluster with six, which reads as
	// a broken page rather than a finding. The kind lookup already answers
	// "can this carry a workload", and asset.go says why that answer lives in
	// the table: a switch in Go could only ever speak for the kinds it was
	// compiled with, so a kind added by INSERT would be silently unusable.
	var claimRows []struct {
		AssetID             string `db:"asset_id"`
		Name                string `db:"name"`
		VCPUProvisioned     *int   `db:"vcpu_provisioned"`
		VCPUAllocated       *int   `db:"vcpu_allocated"`
		MemoryProvisionedMB *int   `db:"memory_provisioned_mb"`
		MemoryAllocatedMB   *int   `db:"memory_allocated_mb"`
	}
	if err := s.read(ctx, &claimRows, `
		SELECT DISTINCT g.id AS asset_id, g.name, g.vcpu_provisioned, g.vcpu_allocated,
		       g.memory_provisioned_mb, g.memory_allocated_mb
		FROM cluster_member m
		JOIN asset_closure cl ON cl.ancestor_id = m.asset_id
		JOIN asset g ON g.id = cl.descendant_id
		JOIN asset_kind k ON k.code = g.kind
		WHERE m.cluster_id = ? AND cl.ancestor_id <> cl.descendant_id
		  AND g.lifecycle <> ? AND k.can_host_instances = TRUE
		ORDER BY g.name`, clusterID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading workloads of %s: %w", clusterID, err)
	}

	hosts := make([]domain.HostCapacity, 0, len(hostRows))
	for _, h := range hostRows {
		hosts = append(hosts, domain.HostCapacity{
			AssetID: h.AssetID, Name: h.Name,
			CPUCores: h.CPUCores, MemoryMB: h.MemoryMB,
		})
	}
	claims := make([]domain.WorkloadClaim, 0, len(claimRows))
	for _, w := range claimRows {
		claims = append(claims, domain.WorkloadClaim{
			AssetID: w.AssetID, Name: w.Name,
			VCPUProvisioned: w.VCPUProvisioned, VCPUAllocated: w.VCPUAllocated,
			MemoryProvisionedMB: w.MemoryProvisionedMB, MemoryAllocatedMB: w.MemoryAllocatedMB,
		})
	}

	// Zero signals "not declared" to ComputeCapacity, which then uses every
	// host and says the redundancy premium is not knowable. Defaulting to
	// len(hosts) here would have been indistinguishable from a cluster that
	// genuinely needs all of them.
	survivors := 0
	if cluster.MinHosts != nil {
		survivors = *cluster.MinHosts
	}
	out := domain.ComputeCapacity(hosts, claims, survivors, cluster.CPUOvercommit)
	return &out, nil
}
