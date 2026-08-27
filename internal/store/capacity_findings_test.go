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

// The two alarming findings are asserted HERE rather than seeded.
//
// An estate that trips every alarm is as unrepresentative as one that trips
// none, and making the demo genuinely oversold would have required numbers
// nobody would recognise. seed_engine.go set this precedent for cluster
// relocation -- "demonstrated by the unit tests, which build their own clusters
// and assert both outcomes" -- and it applies for the same reason.

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// sizeAsset records cores and memory on a host, or the claim on a guest.
func (f *projectFixture) sizeAsset(t *testing.T, name string, apply func(*domain.Asset)) {
	t.Helper()
	row, err := f.s.GetAsset(f.ctx, f.assets[name])
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	a := row.Asset
	apply(&a)
	envIDs := make([]string, len(row.Environments))
	for i, env := range row.Environments {
		envIDs[i] = env.ID
	}
	if err := f.s.UpdateAsset(f.ctx, testPermit, &a, envIDs); err != nil {
		t.Fatalf("sizing %s: %v", name, err)
	}
}

// measureCluster puts hv-01 in a cluster of its own and gives it a size.
func (f *projectFixture) measureCluster(t *testing.T, cores, memoryMB int) string {
	t.Helper()
	f.sizeAsset(t, "hv-01", func(a *domain.Asset) {
		a.CPUCores, a.MemoryMB = &cores, &memoryMB
	})
	one := 1
	return mustCluster(t, f.s, f.ctx, "cl-one", domain.HANone, &one, f.assets["hv-01"])
}

// allocate records what a guest has been given.
func (f *projectFixture) allocate(t *testing.T, name string, vcpu, memoryMB int) {
	t.Helper()
	f.sizeAsset(t, name, func(a *domain.Asset) {
		a.VCPUAllocated, a.MemoryAllocatedMB = &vcpu, &memoryMB
	})
}

// provision records the hard limit, which is what oversubscription is judged
// on. Separate from allocate so a test can make them disagree, because that
// disagreement is the whole subject of ClusterCapacity.
func (f *projectFixture) provision(t *testing.T, name string, vcpu, memoryMB int) {
	t.Helper()
	f.sizeAsset(t, name, func(a *domain.Asset) {
		a.VCPUProvisioned, a.MemoryProvisionedMB = &vcpu, &memoryMB
	})
}

// oversubscribe builds a cluster promising its guest more than it has.
func (f *projectFixture) oversubscribe(t *testing.T) {
	t.Helper()
	f.measureCluster(t, 4, 8192)
	f.provision(t, "vm-app-1", 16, 4096) // 16 vCPU on a 4-core host at 1:1
}

// priceProjectFor records what an engagement was quoted on.
func (f *projectFixture) priceProjectFor(t *testing.T, code string, vcpu, memoryMB int) {
	t.Helper()
	row, err := f.s.GetProject(f.ctx, f.projects[code])
	if err != nil {
		t.Fatalf("reading project %s: %v", code, err)
	}
	p := row.Project
	p.PricedForVCPU, p.PricedForMemoryMB = &vcpu, &memoryMB
	if err := f.s.UpdateProject(f.ctx, testActor, &p); err != nil {
		t.Fatalf("pricing %s: %v", code, err)
	}
}

// findingsFor runs the gatherer and returns the findings of one kind.
func findingsFor(t *testing.T, f *projectFixture, kind string) []CapacityFinding {
	t.Helper()
	all, err := f.s.CapacityFindings(f.ctx)
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	var out []CapacityFinding
	for _, x := range all {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

// TestACLusterPromisingMoreThanItCanServeIsReported.
//
// Judged on what is PROVISIONED. Allocation is a billing figure somebody
// agreed; provisioning is what the hypervisor hands out under contention, and a
// cluster is oversubscribed by what it promised its guests.
func TestAClusterPromisingMoreThanItCanServeIsReported(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			f.oversubscribe(t)

			got := findingsFor(t, f, CapacityOversubscribed)
			if len(got) == 0 {
				t.Fatal("a cluster promising more vCPU than it can serve is not reported")
			}
			if got[0].Severity != domain.FindingRiskSeverity {
				t.Errorf("severity is %q, want risk", got[0].Severity)
			}
			// The arithmetic is in the sentence, because a verdict nobody can
			// check is a verdict nobody acts on.
			if !strings.Contains(got[0].Detail, "usable") {
				t.Errorf("the finding does not carry its arithmetic: %q", got[0].Detail)
			}
		})
	}
}

// TestPricingMoreThanTheEstateCanHostIsReported.
//
// THE FINDING NO UTILISATION DASHBOARD CAN PRODUCE. A cluster at 35% CPU looks
// healthy and says nothing about whether every engagement could be served at
// once, because utilisation measures what is TAKEN and this measures what could
// be CLAIMED.
//
// ONE DIMENSION OVERSOLD AT A TIME, and that is not tidiness. The first version
// oversold both, asserted a finding of this kind existed, and PASSED with the
// vCPU branch deleted outright -- the memory branch emits the same kind and
// quietly held the test up. A test satisfied by a neighbour of the thing it
// names proves nothing about the thing it names.
func TestPricingMoreThanTheEstateCanHostIsReported(t *testing.T) {
	// The estate: one measured host, 8 usable vCPU at 1:1 and 32 GB.
	const hostVCPU, hostMemoryMB = 8, 32768

	cases := []struct {
		name           string
		vcpu, memoryMB int
		// want is the unit the finding must be about, so a finding produced by
		// the other branch cannot satisfy this case.
		want string
	}{
		{"more vCPU priced than exists", hostVCPU * 8, hostMemoryMB / 2, "vCPU"},
		{"more memory priced than exists", hostVCPU / 2, hostMemoryMB * 8, "MB"},
	}
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newProjectFixture(t, e)
					f.measureCluster(t, hostVCPU, hostMemoryMB)
					f.priceProjectFor(t, "orders", tc.vcpu, tc.memoryMB)

					got := findingsFor(t, f, CapacitySoldBeyondEstate)
					if len(got) != 1 {
						t.Fatalf("got %d findings, want exactly 1 about %s", len(got), tc.want)
					}
					if !strings.Contains(got[0].Detail, tc.want) {
						t.Errorf("the finding is not about %s: %q", tc.want, got[0].Detail)
					}
					if got[0].Severity != domain.FindingFaultSeverity {
						t.Errorf("severity is %q, want fault: this is not a risk, it is "+
							"a promise that cannot be kept", got[0].Severity)
					}
				})
			}
		})
	}
}

// TestAProjectInsideItsQuoteIsSilent. A finding that fires on everything is one
// people switch off.
func TestAProjectInsideItsQuoteIsSilent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			f.measureCluster(t, 32, 262144)
			// Owned and allocated modestly, priced generously.
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.allocate(t, "vm-app-1", 4, 8192)
			f.priceProjectFor(t, "orders", 64, 262144)

			for _, x := range findingsFor(t, f, CapacityOverPriced) {
				t.Errorf("a project inside its quote produced a finding: %s", x.Detail)
			}
		})
	}
}
