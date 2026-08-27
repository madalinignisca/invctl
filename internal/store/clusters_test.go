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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
)

// The unit tests prove the relocation rule. This proves the WIRING: that a
// declared cluster reaches the engine and changes what a real simulation
// concludes about a real service. Both halves are needed -- correct logic the
// loader never hands anything to is a feature nobody has.

func mustCluster(t *testing.T, s *SQLStore, ctx context.Context, name, policy string,
	minHosts *int, hosts ...string) string {
	t.Helper()
	c, err := domain.NewCluster(NewID(), name, domain.ClusterProxmox)
	if err != nil {
		t.Fatalf("building cluster: %v", err)
	}
	c.HAPolicy = policy
	c.MinHosts = minHosts
	if err := s.CreateCluster(ctx, testPermit, c); err != nil {
		t.Fatalf("creating cluster: %v", err)
	}
	members := make([]domain.ClusterMember, 0, len(hosts))
	for _, h := range hosts {
		members = append(members, domain.ClusterMember{ClusterID: c.ID, AssetID: h})
	}
	if err := s.SetClusterMembers(ctx, testPermit, c.ID, members); err != nil {
		t.Fatalf("setting members: %v", err)
	}
	return c.ID
}

// TestAClusterChangesWhatTheSimulationConcludes.
//
// The same outage, the same estate, twice: once with the cluster's policy at
// none and once at restart. The guest's instances must be down in the first and
// up in the second, because that is the whole claim -- and if the loader is
// wired to nothing, both runs agree and this fails.
func TestAClusterChangesWhatTheSimulationConcludes(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			hv1 := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-1", nil)
			hv2 := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-2", nil)
			vm := mustAsset(t, s, ctx, domain.KindVM, "vm-a", &hv1)

			cid := mustCluster(t, s, ctx, "prod-pve", domain.HANone, nil, hv1, hv2)

			guestsDown := func() bool {
				t.Helper()
				g, err := s.LoadGraph(ctx)
				if err != nil {
					t.Fatalf("loading graph: %v", err)
				}
				subtree, err := s.SubtreeIDs(ctx, []string{hv1})
				if err != nil {
					t.Fatalf("subtree: %v", err)
				}
				down := map[string]bool{}
				for _, id := range subtree {
					down[id] = true
				}
				if !down[vm] {
					t.Fatal("the guest is not in the closure-expanded down set at all, " +
						"so this test cannot tell whether HA revived it")
				}
				res := impact.Analyse(g, impact.Request{DownAssetIDs: []string{hv1}},
					impact.Inputs{DownInstanceIDs: map[string]bool{}, DownAssetIDs: down, Net: g.Net})
				// Analyse mutates its local copy, so ask the findings.
				for _, r := range res.Relocations {
					if r.Relocated() {
						return false
					}
				}
				return true
			}

			if !guestsDown() {
				t.Error("with the policy at none the guest was relocated; that is what " +
					"the engine did before clusters existed and must still do")
			}

			// Same estate, policy flipped.
			c, err := s.GetCluster(ctx, cid)
			if err != nil {
				t.Fatalf("getting cluster: %v", err)
			}
			c.HAPolicy = domain.HARestart
			if err := s.UpdateCluster(ctx, testPermit, c); err != nil {
				t.Fatalf("updating cluster: %v", err)
			}
			if guestsDown() {
				t.Error("with the policy at restart and a surviving host the guest was " +
					"NOT relocated; the cluster never reached the engine")
			}
		})
	}
}

// TestTheGuestsComeFromContainmentNotFromAColumn. A guest nested under a bridge
// under the host must relocate too -- the down set is closure-expanded, so the
// revival has to be, or a nested guest stays down while its sibling comes back.
func TestTheGuestsComeFromContainmentNotFromAColumn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			hv1 := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-1", nil)
			hv2 := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-2", nil)
			br := mustAsset(t, s, ctx, domain.KindBridge, "hv-1-br0", &hv1)
			nested := mustAsset(t, s, ctx, domain.KindVM, "vm-nested", &br)

			mustCluster(t, s, ctx, "prod-pve", domain.HARestart, nil, hv1, hv2)

			g, err := s.LoadGraph(ctx)
			if err != nil {
				t.Fatalf("loading graph: %v", err)
			}
			var found *impact.Cluster
			for i := range g.Clusters {
				if g.Clusters[i].Name == "prod-pve" {
					found = &g.Clusters[i]
				}
			}
			if found == nil {
				t.Fatal("the cluster did not reach the graph")
			}
			guests := found.GuestsByHost[hv1]
			hasNested, hasBridge := false, false
			for _, id := range guests {
				if id == nested {
					hasNested = true
				}
				if id == br {
					hasBridge = true
				}
			}
			if !hasNested {
				t.Error("the VM two levels down was not loaded as a guest, so it would " +
					"stay down while its siblings came back")
			}
			if !hasBridge {
				t.Error("the bridge was not loaded as a guest; the down set contains it " +
					"and an unrevived bridge leaves its children unreachable")
			}
			for _, id := range guests {
				if id == hv1 {
					t.Error("the host is listed as its own guest; reviving it would undo " +
						"the outage")
				}
			}
		})
	}
}
