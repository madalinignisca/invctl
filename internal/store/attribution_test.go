// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// divisionFor picks one dimension out of an attribution.
func divisionFor(t *testing.T, a *Attribution, dimension string) domain.Division {
	t.Helper()
	for _, d := range a.Divisions {
		if d.Dimension == dimension {
			return d
		}
	}
	t.Fatalf("no %s division in the attribution", dimension)
	return domain.Division{}
}

// TestACLusterDividesBetweenTheProjectsStandingOnIt.
//
// The wiring test for §5.7's table: the arithmetic is proved in the domain, and
// this proves the estate reaches it -- that ownership resolves through
// containment, that the shares divide the same capacity the cluster page
// reports, and that a workload nobody owns is carried rather than dropped.
func TestAClusterDividesBetweenTheProjectsStandingOnIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			// One host, 32 cores and 256 GB, no overcommit declared: 32 vCPU.
			f.sizeAsset(t, "hv-01", func(a *domain.Asset) {
				cores, mem := 32, 256*1024
				a.CPUCores, a.MemoryMB = &cores, &mem
			})
			one := 1
			clusterID := mustCluster(t, f.s, f.ctx, "cl", domain.HANone, &one, f.assets["hv-01"])

			// platform owns the hypervisor, so it owns what runs inside it.
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.allocate(t, "vm-app-1", 8, 32*1024)

			a, err := f.s.AttributionFor(f.ctx, clusterID)
			if err != nil {
				t.Fatalf("attributing: %v", err)
			}
			cpu := divisionFor(t, a, "CPU")
			if cpu.Sellable != 32 {
				t.Fatalf("sellable is %d vCPU, want 32", cpu.Sellable)
			}
			if len(cpu.Shares) != 1 || cpu.Shares[0].Subject != "platform" {
				t.Fatalf("the shares are %+v, want one for platform", cpu.Shares)
			}
			// 8 of 32 is a quarter, and the rest is headroom.
			if cpu.Shares[0].BasisPoints != 2500 {
				t.Errorf("platform holds %d basis points of CPU, want 2500",
					cpu.Shares[0].BasisPoints)
			}
			if cpu.Idle.BasisPoints != 7500 {
				t.Errorf("headroom is %d basis points, want 7500", cpu.Idle.BasisPoints)
			}
			// Memory divides differently, which is the entire point of §5.7.
			mem := divisionFor(t, a, "Memory")
			if mem.Shares[0].BasisPoints != 1250 {
				t.Errorf("platform holds %d basis points of memory, want 1250 -- "+
					"a share that matches CPU's means one of them is not being "+
					"divided by its own dimension", mem.Shares[0].BasisPoints)
			}
		})
	}
}

// TestAWorkloadNobodyOwnsIsCarriedNotDropped.
//
// Dropping it would make the remaining shares add up to a whole that was never
// the cluster, which is §5.3 read from the other end. It is also a different
// fact from idle capacity: idle is unclaimed, this is claimed by somebody
// nobody has written down, and the fix for each is a different conversation.
func TestAWorkloadNobodyOwnsIsCarriedNotDropped(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			f.sizeAsset(t, "hv-01", func(a *domain.Asset) {
				cores, mem := 10, 10240
				a.CPUCores, a.MemoryMB = &cores, &mem
			})
			one := 1
			clusterID := mustCluster(t, f.s, f.ctx, "cl", domain.HANone, &one, f.assets["hv-01"])
			// vm-app-1 is allocated, and NO project owns anything here.
			f.allocate(t, "vm-app-1", 4, 4096)

			a, err := f.s.AttributionFor(f.ctx, clusterID)
			if err != nil {
				t.Fatalf("attributing: %v", err)
			}
			cpu := divisionFor(t, a, "CPU")
			if len(cpu.Shares) != 1 {
				t.Fatalf("got %d shares, want the unattributed one", len(cpu.Shares))
			}
			if cpu.Shares[0].Subject != domain.UnattributedSubject {
				t.Errorf("the unowned workload is filed under %q, want %q",
					cpu.Shares[0].Subject, domain.UnattributedSubject)
			}
			if cpu.Shares[0].Amount != 4 {
				t.Errorf("it holds %d vCPU, want the 4 it was allocated", cpu.Shares[0].Amount)
			}
		})
	}
}

// TestTheNearestOwnerWins. A project owning a specific VM inside a hypervisor
// another project owns keeps that VM: a more specific declaration is a later
// decision about a smaller thing. Without the depth ordering the answer depends
// on row order, which is a cost report that changes between two identical
// requests.
func TestTheNearestOwnerWins(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			f.sizeAsset(t, "hv-01", func(a *domain.Asset) {
				cores, mem := 100, 102400
				a.CPUCores, a.MemoryMB = &cores, &mem
			})
			one := 1
			clusterID := mustCluster(t, f.s, f.ctx, "cl", domain.HANone, &one, f.assets["hv-01"])

			// platform owns the host; orders owns one guest inside it.
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking the host: %v", err)
			}
			if err := f.link(t, "orders", "vm-app-1", domain.ProjectOwns); err != nil {
				t.Fatalf("linking the guest: %v", err)
			}
			f.allocate(t, "vm-app-1", 4, 4096)

			a, err := f.s.AttributionFor(f.ctx, clusterID)
			if err != nil {
				t.Fatalf("attributing: %v", err)
			}
			cpu := divisionFor(t, a, "CPU")
			if len(cpu.Shares) != 1 || cpu.Shares[0].Subject != "orders" {
				t.Errorf("the guest is attributed to %+v, want orders -- the "+
					"nearest declaration, not the outermost", cpu.Shares)
			}
		})
	}
}

// TestAPoolDividesSeparatelyFromTheCluster. Block storage commonly serves
// several clusters and bulk usually serves everything, so folding pools into a
// cluster's report would attribute one pool to whichever cluster was looked at
// first.
func TestAPoolDividesSeparatelyFromTheCluster(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			poolID := f.pool(t, "ceph-block", "ceph_3x", 3000) // 1000 GB usable
			if err := f.link(t, "orders", "vm-app-1", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			if err := f.s.SetStorageClaim(f.ctx, testPermit, f.assets["vm-app-1"],
				poolID, 250, nil); err != nil {
				t.Fatalf("claiming: %v", err)
			}

			d, err := f.s.PoolAttribution(f.ctx, poolID)
			if err != nil {
				t.Fatalf("attributing the pool: %v", err)
			}
			if d.Sellable != 1000 {
				t.Fatalf("the pool divides %d GB, want the 1000 usable of 3000 raw",
					d.Sellable)
			}
			if len(d.Shares) != 1 || d.Shares[0].BasisPoints != 2500 {
				t.Errorf("orders holds %+v, want 2500 basis points of the pool", d.Shares)
			}
			if d.Idle.Amount != 750 {
				t.Errorf("free space is %d GB, want 750", d.Idle.Amount)
			}
		})
	}
}
