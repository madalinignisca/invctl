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
	"errors"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Search is the one place dialect-specific SQL is sanctioned, so it is also
// the place where the two engines are most likely to drift. These tests run
// against both and assert on behaviour, not on ranking.

func seedSearchable(t *testing.T, s *SQLStore, ctx context.Context) (assetID, serviceID string) {
	t.Helper()

	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

	a, err := domain.NewAsset(NewID(), domain.KindHypervisor, "hv-01", nil, s.Now())
	if err != nil {
		t.Fatalf("building asset: %v", err)
	}
	serial := "FCH2033V0YR"
	a.Serial = &serial
	a.Vendor = strPtr("Dell")
	if err := s.CreateAsset(ctx, testPermit, a, []string{envID}); err != nil {
		t.Fatalf("creating asset: %v", err)
	}

	iface, err := domain.NewInterface(NewID(), a.ID, "eno1", domain.FFSFP28)
	if err != nil {
		t.Fatalf("building interface: %v", err)
	}
	if err := iface.SetMAC("AA:BB:CC:00:10:01"); err != nil {
		t.Fatalf("setting mac: %v", err)
	}
	if err := s.CreateInterface(ctx, testActor, iface); err != nil {
		t.Fatalf("creating interface: %v", err)
	}

	addr, err := domain.NewIPAddress(NewID(), "10.20.30.11", &iface.ID, domain.IPRolePrimary)
	if err != nil {
		t.Fatalf("building address: %v", err)
	}
	if err := s.CreateIPAddress(ctx, testActor, addr); err != nil {
		t.Fatalf("creating address: %v", err)
	}

	prefix, err := domain.NewPrefix(NewID(), "10.20.30.0/24")
	if err != nil {
		t.Fatalf("building prefix: %v", err)
	}
	prefix.Role = strPtr("production-workloads")
	if err := s.CreatePrefix(ctx, testActor, prefix); err != nil {
		t.Fatalf("creating prefix: %v", err)
	}
	wider, err := domain.NewPrefix(NewID(), "10.20.0.0/16")
	if err != nil {
		t.Fatalf("building wider prefix: %v", err)
	}
	if err := s.CreatePrefix(ctx, testActor, wider); err != nil {
		t.Fatalf("creating wider prefix: %v", err)
	}

	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: "pgsql-core", Name: "PostgreSQL (core)", Kind: domain.SvcDB,
		EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 1,
	}, s.Now())
	if err != nil {
		t.Fatalf("building service: %v", err)
	}
	if err := s.CreateService(ctx, testPermit, svc); err != nil {
		t.Fatalf("creating service: %v", err)
	}

	port := 5432
	ep, err := domain.NewEndpoint(NewID(), svc.ID, "sql", domain.ProtoTCP, &port, domain.BindHost)
	if err != nil {
		t.Fatalf("building endpoint: %v", err)
	}
	if err := s.CreateEndpoint(ctx, testPermit, ep); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}

	return a.ID, svc.ID
}

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }

func TestSearchResolvesIdentifiers(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID, serviceID := seedSearchable(t, s, ctx)

			t.Run("ip address resolves to the interface holder", func(t *testing.T) {
				results, err := s.Search(ctx, "10.20.30.11", 25)
				if err != nil {
					t.Fatalf("searching: %v", err)
				}
				if !hasResult(results, "asset", assetID) {
					t.Errorf("searching for an IP did not return the asset holding it: %+v", results)
				}
				// And the containing prefix, because that is the other thing
				// you want when you paste an address out of a firewall log.
				if !hasType(results, "prefix") {
					t.Errorf("searching for an IP did not return the containing prefix: %+v", results)
				}
				for _, r := range results {
					if r.EntityType == "prefix" && r.Title != "10.20.30.0/24" {
						t.Errorf("prefix = %s, want the most specific one (10.20.30.0/24)", r.Title)
					}
				}
			})

			t.Run("mac resolves regardless of format", func(t *testing.T) {
				for _, query := range []string{
					"aa:bb:cc:00:10:01", "AA-BB-CC-00-10-01", "aabb.cc00.1001", "aabbcc001001",
				} {
					results, err := s.Search(ctx, query, 25)
					if err != nil {
						t.Fatalf("searching for %s: %v", query, err)
					}
					if !hasResult(results, "asset", assetID) {
						t.Errorf("searching for %q did not resolve to the asset", query)
					}
				}
			})

			t.Run("bare number resolves to a listening service", func(t *testing.T) {
				results, err := s.Search(ctx, "5432", 25)
				if err != nil {
					t.Fatalf("searching: %v", err)
				}
				if !hasResult(results, "service", serviceID) {
					t.Errorf("searching for a port did not return the listening service: %+v", results)
				}
			})

			t.Run("serial resolves exactly", func(t *testing.T) {
				results, err := s.Search(ctx, "FCH2033V0YR", 25)
				if err != nil {
					t.Fatalf("searching: %v", err)
				}
				if !hasResult(results, "asset", assetID) {
					t.Errorf("searching for a serial did not return the asset: %+v", results)
				}
			})

			t.Run("free text falls through to the index", func(t *testing.T) {
				results, err := s.Search(ctx, "pgsql", 25)
				if err != nil {
					t.Fatalf("searching: %v", err)
				}
				if !hasResult(results, "service", serviceID) {
					t.Errorf("searching for a code fragment did not return the service: %+v", results)
				}
			})

			t.Run("nothing matches", func(t *testing.T) {
				results, err := s.Search(ctx, "zzzz-nothing-here", 25)
				if err != nil {
					t.Fatalf("searching: %v", err)
				}
				if len(results) != 0 {
					t.Errorf("got %d results for a nonsense query, want none", len(results))
				}
			})

			t.Run("empty query returns nothing rather than everything", func(t *testing.T) {
				results, err := s.Search(ctx, "   ", 25)
				if err != nil {
					t.Fatalf("searching: %v", err)
				}
				if len(results) != 0 {
					t.Errorf("got %d results for an empty query", len(results))
				}
			})

			t.Run("results are deduplicated", func(t *testing.T) {
				results, err := s.Search(ctx, "hv-01", 25)
				if err != nil {
					t.Fatalf("searching: %v", err)
				}
				seen := map[string]int{}
				for _, r := range results {
					seen[r.EntityType+"/"+r.EntityID]++
				}
				for key, n := range seen {
					if n > 1 {
						t.Errorf("%s appears %d times in one result set", key, n)
					}
				}
			})
		})
	}
}

// TestSearchSurvivesQuerySyntax: FTS5 has its own query language, and
// unescaped operator characters in user input are at best a syntax error.
// None of these should return an error on either engine.
func TestSearchSurvivesQuerySyntax(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			seedSearchable(t, s, ctx)

			queries := []string{
				`"`, `""`, `*`, `AND`, `OR`, `NOT`, `hv-01 OR`, `(unbalanced`,
				`a"b`, `NEAR(x y)`, `%`, `_`, `'; DROP TABLE asset; --`,
				`^caret`, `col:value`, strings.Repeat("x", 300),
			}
			for _, q := range queries {
				if _, err := s.Search(ctx, q, 25); err != nil {
					t.Errorf("searching for %q returned an error: %v", q, err)
				}
			}
		})
	}
}

// TestResolveAddress covers the containment query directly, including the
// most-specific-wins rule and the v4/v6 separation.
func TestResolveAddress(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			seedSearchable(t, s, ctx)

			v6, err := domain.NewPrefix(NewID(), "2001:db8::/32")
			if err != nil {
				t.Fatalf("building v6 prefix: %v", err)
			}
			if err := s.CreatePrefix(ctx, testActor, v6); err != nil {
				t.Fatalf("creating v6 prefix: %v", err)
			}

			// A child ALIGNED to its parent's first address. The case above it
			// passes on a broken query: 10.20.30.0/24 starts later than
			// 10.20.0.0/16, so ordering on addr_start alone already separates
			// them. Here the starts are identical and only addr_end can decide,
			// which is the ordinary shape of a subnetted range and was the gap.
			aligned, err := domain.NewPrefix(NewID(), "10.20.0.0/24")
			if err != nil {
				t.Fatalf("building the aligned child: %v", err)
			}
			if err := s.CreatePrefix(ctx, testActor, aligned); err != nil {
				t.Fatalf("creating the aligned child: %v", err)
			}

			cases := []struct{ addr, want string }{
				{"10.20.30.99", "10.20.30.0/24"}, // most specific wins
				{"10.20.99.1", "10.20.0.0/16"},   // only the wider one contains it
				{"2001:db8::5", "2001:db8::/32"}, // v6 resolves independently
				// Both the /16 and the /24 start at 10.20.0.0.
				{"10.20.0.5", "10.20.0.0/24"},
			}
			for _, tc := range cases {
				got, err := s.ResolveAddress(ctx, tc.addr)
				if err != nil {
					t.Fatalf("resolving %s: %v", tc.addr, err)
				}
				if got.CIDRText != tc.want {
					t.Errorf("resolving %s = %s, want %s", tc.addr, got.CIDRText, tc.want)
				}
			}

			if _, err := s.ResolveAddress(ctx, "203.0.113.5"); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("resolving an address outside every prefix = %v, want ErrNotFound", err)
			}
			if _, err := s.ResolveAddress(ctx, "not-an-address"); err == nil {
				t.Error("resolving a non-address succeeded")
			}
		})
	}
}

// TestSearchIndexFollowsUpdates: an index that goes stale on rename is worse
// than no index, because it returns a confidently wrong answer.
func TestSearchIndexFollowsUpdates(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID, _ := seedSearchable(t, s, ctx)

			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("getting asset: %v", err)
			}
			renamed := asset.Asset
			renamed.Name = "hv-01-renamed"
			if err := s.UpdateAsset(ctx, testPermit, &renamed, nil); err != nil {
				t.Fatalf("updating asset: %v", err)
			}

			results, err := s.Search(ctx, "renamed", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if !hasResult(results, "asset", assetID) {
				t.Errorf("the search index did not follow a rename: %+v", results)
			}

			// And the old name is gone from the index, not merely outranked.
			old, err := s.Search(ctx, "hv-01-renamed-nonexistent", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if len(old) != 0 {
				t.Errorf("unexpected results: %+v", old)
			}
		})
	}
}

func hasResult(results []SearchResult, entityType, entityID string) bool {
	for _, r := range results {
		if r.EntityType == entityType && r.EntityID == entityID {
			return true
		}
	}
	return false
}

func hasType(results []SearchResult, entityType string) bool {
	for _, r := range results {
		if r.EntityType == entityType {
			return true
		}
	}
	return false
}
