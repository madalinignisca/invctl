// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// share declares an occupancy on a workload.
func (f *projectFixture) share(t *testing.T, asset string, parts map[string]int) {
	t.Helper()
	occ := make([]domain.Occupant, 0, len(parts))
	for code, percent := range parts {
		occ = append(occ, domain.Occupant{
			ProjectID: f.projects[code], Percent: percent,
		})
	}
	if err := f.s.SetOccupants(f.ctx, testPermit, f.assets[asset], occ); err != nil {
		t.Fatalf("declaring occupancy: %v", err)
	}
}

// sharedCluster is one measured host in a cluster, with the guest allocated.
func (f *projectFixture) sharedCluster(t *testing.T) string {
	t.Helper()
	f.sizeAsset(t, "hv-01", func(a *domain.Asset) {
		cores, mem := 20, 20480
		a.CPUCores, a.MemoryMB = &cores, &mem
	})
	one := 1
	id := mustCluster(t, f.s, f.ctx, "cl", domain.HANone, &one, f.assets["hv-01"])
	f.allocate(t, "vm-app-1", 10, 10240)
	return id
}

// TestASharedMachineDividesBetweenItsTenants.
//
// THE CASE OWNERSHIP CANNOT DESCRIBE. At most one project owns an asset, so
// without this the whole of a shared box's capacity lands on its owner -- not
// an approximation but a wrong answer given confidently, for exactly the
// estates that pack tenants together to save on licensing.
func TestASharedMachineDividesBetweenItsTenants(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.sharedCluster(t)
			// platform owns the host and therefore the guest inside it...
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			// ...but the guest is shared 60/40 with orders.
			f.share(t, "vm-app-1", map[string]int{"platform": 60, "orders": 40})

			a, err := f.s.AttributionFor(f.ctx, id)
			if err != nil {
				t.Fatalf("attributing: %v", err)
			}
			cpu := divisionFor(t, a, "CPU")
			got := map[string]int{}
			for _, sh := range cpu.Shares {
				got[sh.Subject] = sh.Amount
			}
			if got["platform"] != 6 || got["orders"] != 4 {
				t.Errorf("ten vCPU divided %v, want 6 to platform and 4 to orders "+
					"-- the declaration, not the ownership", got)
			}
		})
	}
}

// TestAnUnderDeclaredOccupancyLeavesTheRestVisible.
//
// §5.4: a total that is not 100 is a finding, not a silent rounding. The
// remainder is a real slice of a real machine somebody is paying for, so it is
// carried to nobody rather than dropped -- dropping it would make the shares
// add up to a whole that was never the cluster.
func TestAnUnderDeclaredOccupancyLeavesTheRestVisible(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.sharedCluster(t)
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.share(t, "vm-app-1", map[string]int{"platform": 50, "orders": 30})

			a, err := f.s.AttributionFor(f.ctx, id)
			if err != nil {
				t.Fatalf("attributing: %v", err)
			}
			got := map[string]int{}
			for _, sh := range divisionFor(t, a, "CPU").Shares {
				got[sh.Subject] = sh.Amount
			}
			if got["platform"] != 5 || got["orders"] != 3 {
				t.Errorf("80%% declared divided %v, want 5 and 3", got)
			}
			if got[domain.UnattributedSubject] != 2 {
				t.Errorf("the undeclared fifth is %d vCPU, want 2 held by nobody",
					got[domain.UnattributedSubject])
			}

			// And it is reported rather than left for somebody to notice.
			fs, err := f.s.CapacityFindings(f.ctx)
			if err != nil {
				t.Fatalf("gathering findings: %v", err)
			}
			var found bool
			for _, x := range fs {
				if x.Kind == CapacityUnbalancedOccupancy {
					found = true
					if !strings.Contains(x.Detail, "80%") {
						t.Errorf("the finding does not say how much is spoken for: %s", x.Detail)
					}
				}
			}
			if !found {
				t.Error("an occupancy that does not total 100 produced no finding")
			}
		})
	}
}

// TestOverDeclaredOccupancyIsARiskNotAGap. Two people have each been told they
// have most of the machine, which is a different mistake from not having
// finished declaring it.
func TestOverDeclaredOccupancyIsARiskNotAGap(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			f.sharedCluster(t)
			f.share(t, "vm-app-1", map[string]int{"platform": 70, "orders": 60})

			fs, err := f.s.CapacityFindings(f.ctx)
			if err != nil {
				t.Fatalf("gathering findings: %v", err)
			}
			for _, x := range fs {
				if x.Kind != CapacityUnbalancedOccupancy {
					continue
				}
				if x.Severity != domain.FindingRiskSeverity {
					t.Errorf("over-declared occupancy is %q, want risk", x.Severity)
				}
				if !strings.Contains(x.Detail, "more machine than exists") {
					t.Errorf("the finding does not say what is wrong: %s", x.Detail)
				}
				return
			}
			t.Error("occupants claiming 130% between them produced no finding")
		})
	}
}

// TestDeclaringWhoSharesAMachineIsAudited. Somebody deciding a machine is
// shared moves money, and the codebase has lost this audit to a set-table
// replacement three times.
func TestDeclaringWhoSharesAMachineIsAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			vm := f.assets["vm-app-1"]
			f.share(t, "vm-app-1", map[string]int{"platform": 60, "orders": 40})

			entries, err := f.s.ListChangesForEntity(f.ctx, "asset", vm, 10)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			if len(entries) == 0 {
				t.Fatal("declaring who shares a machine wrote no change_log entry")
			}
			if !strings.Contains(entries[0].Diff, "60%") {
				t.Errorf("the entry does not record the shares: %s", entries[0].Diff)
			}

			// Deciding it is no longer shared is a change too.
			if err := f.s.SetOccupants(f.ctx, testPermit, vm, nil); err != nil {
				t.Fatalf("clearing: %v", err)
			}
			after, err := f.s.ListChangesForEntity(f.ctx, "asset", vm, 10)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			if len(after) <= len(entries) {
				t.Error("a machine ceasing to be shared wrote no entry")
			}
		})
	}
}

// TestOccupancyDoesNotReplaceOwnership. A shared box still has an owner: who is
// answerable for it, and who is called when it breaks. Conflating the two would
// mean a machine four projects share belongs to nobody.
func TestOccupancyDoesNotReplaceOwnership(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.link(t, "platform", "vm-app-1", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.share(t, "vm-app-1", map[string]int{"orders": 100})

			owners, _, err := f.s.ownersOf(f.ctx)
			if err != nil {
				t.Fatalf("resolving ownership: %v", err)
			}
			if owners[f.assets["vm-app-1"]] != f.projects["platform"] {
				t.Error("declaring an occupant changed who owns the machine")
			}
		})
	}
}
