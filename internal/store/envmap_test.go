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

// TestEnvironmentMapIsMembershipNotAWalk: the map is asset_environment rows,
// not reachability -- an asset in the environment with no cable to anything
// is still in the picture, and an asset outside the environment is not, even
// when a cable runs to it. The neighbourhood is the page that crosses
// boundaries; this one draws exactly what the environment claims.
func TestEnvironmentMapIsMembershipNotAWalk(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			prod := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			dev := mustEnvironment(t, s, ctx, "dev", domain.EnvRoleDev)

			inA := mustAsset(t, s, ctx, domain.KindServer, "in-a", nil, prod)
			mustAsset(t, s, ctx, domain.KindServer, "in-b", nil, prod) // uncabled, still drawn
			outC := mustAsset(t, s, ctx, domain.KindServer, "out-c", nil, dev)

			pa := mustInterface(t, s, ctx, inA, "eth0")
			pc := mustInterface(t, s, ctx, outC, "eth0")
			link, err := domain.NewLink(NewID(), pa, pc)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testPermit, link); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			g, err := s.EnvironmentMap(ctx, prod, 0)
			if err != nil {
				t.Fatalf("EnvironmentMap: %v", err)
			}
			names := map[string]bool{}
			for _, a := range g.Assets {
				names[a.Name] = true
			}
			if !names["in-a"] || !names["in-b"] || names["out-c"] {
				t.Errorf("assets = %v, want both members including the uncabled one, "+
					"and never the outsider", names)
			}
			// The cable to out-c has one end off-map; drawing it would be a
			// stub going nowhere.
			if len(g.Links) != 0 {
				t.Errorf("%d links drawn, want none: the only cable leaves the environment", len(g.Links))
			}
		})
	}
}

// TestEnvironmentMapReportsItsBudgetCut: a cut nobody is told about is
// indistinguishable from a fact that does not exist.
func TestEnvironmentMapReportsItsBudgetCut(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			prod := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			for _, n := range []string{"a-1", "a-2", "a-3", "a-4"} {
				mustAsset(t, s, ctx, domain.KindServer, n, nil, prod)
			}
			g, err := s.EnvironmentMap(ctx, prod, 3)
			if err != nil {
				t.Fatalf("EnvironmentMap: %v", err)
			}
			if len(g.Assets) != 3 || g.AssetsElided != 1 {
				t.Errorf("kept %d elided %d, want 3 and 1 -- and the cut falls on the "+
					"end of the name ordering", len(g.Assets), g.AssetsElided)
			}
		})
	}
}
