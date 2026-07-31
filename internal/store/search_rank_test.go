package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// Search ranking, on both engines, because the two disagreed and both were
// defensible: SQLite's bm25 favours short documents and put the bridge
// `hv-01-br0` above the hypervisor `hv-01`, since the bridge has no serial,
// vendor or model in its indexed body. PostgreSQL's trigram similarity put the
// exact match first. An operator mid-incident must not get a different first
// result depending on which engine the deployment runs.

// describe gives an asset a long indexed body -- serial, vendor, model -- which
// is precisely what makes bm25 rank it BELOW the short-bodied bridges named
// after it. Without it these tests would pass against an implementation that
// does no ranking at all.
func describe(t *testing.T, s *SQLStore, ctx context.Context, env, assetID string) {
	t.Helper()
	row, err := s.GetAsset(ctx, assetID)
	if err != nil {
		t.Fatalf("reading the asset: %v", err)
	}
	a := row.Asset
	serial, vendor, model := "FCH2033V0YR", "Dell Incorporated", "PowerEdge R640 rack server"
	a.Serial, a.Vendor, a.Model = &serial, &vendor, &model
	if err := s.UpdateAsset(ctx, testActor, &a, []string{env}); err != nil {
		t.Fatalf("describing the asset: %v", err)
	}
}

func titlesOf(results []SearchResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Title
	}
	return out
}

// TestSearchPutsTheExactNameFirst is the whole fix.
func TestSearchPutsTheExactNameFirst(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil, env)
			mustAsset(t, s, ctx, domain.KindBridge, "hv-01-br0", &hv, env)
			mustAsset(t, s, ctx, domain.KindBridge, "hv-01-br1", &hv, env)

			describe(t, s, ctx, env, hv)

			got, err := s.Search(ctx, "hv-01", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("no results at all")
			}
			if got[0].Title != "hv-01" {
				t.Errorf("first result is %q, want hv-01 (order: %v)", got[0].Title, titlesOf(got))
			}
			// The others must still be there -- ranking is not filtering.
			if len(got) < 3 {
				t.Errorf("only %d results; the near matches were dropped: %v", len(got), titlesOf(got))
			}
			// And the exact hit says why it is first.
			if got[0].Why == "" {
				t.Error("the exact match does not explain itself")
			}
		})
	}
}

// A service is found by its code, which is what an operator types, and the code
// ranks as an exact match even though it is the subtitle rather than the title.
func TestSearchFindsAServiceByItsExactCode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			for _, spec := range []struct{ code, name string }{
				{"orders-api", "Orders API"},
				{"orders-api-dev", "Orders API (dev)"},
				{"orders-api-canary", "Orders API (canary)"},
			} {
				svc, err := domain.NewService(NewID(), domain.ServiceSpec{
					Code: spec.code, Name: spec.name, Kind: domain.SvcAPI,
					EnvironmentID: env, Availability: domain.AvailStandalone, Tier: 2,
				}, s.Now())
				if err != nil {
					t.Fatalf("building %s: %v", spec.code, err)
				}
				if err := s.CreateService(ctx, testActor, svc); err != nil {
					t.Fatalf("creating %s: %v", spec.code, err)
				}
			}

			got, err := s.Search(ctx, "orders-api", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if len(got) == 0 || got[0].Subtitle != "orders-api" {
				t.Errorf("first result is %+v, want the service coded orders-api (order: %v)",
					got, titlesOf(got))
			}
		})
	}
}

// TestAnExactNameSurvivesASmallLimit. Ranking after the query cannot recover a
// row the query never returned, which is why an exact name is looked up directly
// as well as sorted for.
//
// The competitor count matters: the text query over-fetches limit*textOverfetch
// rows, so the estate here has more near-matches than that window holds. With a
// limit of 2 the text side fetches 8 and there are 20 bridges ahead of the
// hypervisor -- it is not in the set at all, and no sort can put it first.
func TestAnExactNameSurvivesASmallLimit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil, env)
			for i := 0; i < 20; i++ {
				mustAsset(t, s, ctx, domain.KindBridge,
					fmt.Sprintf("hv-01-br%02d", i), &hv, env)
			}
			describe(t, s, ctx, env, hv)

			got, err := s.Search(ctx, "hv-01", 2)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d results, want 2", len(got))
			}
			if got[0].Title != "hv-01" {
				t.Errorf("first result is %q, want hv-01 (order: %v)", got[0].Title, titlesOf(got))
			}
		})
	}
}

// TestRankingDecidesWhenNothingMatchesExactly.
//
// A partial query fires no exact lookup, so the ordering is the ranker's alone:
// `hv-01` is a shorter and therefore more specific answer to `hv-0` than
// `hv-01-br0` is, whatever bm25 makes of the document lengths.
func TestRankingDecidesWhenNothingMatchesExactly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil, env)
			mustAsset(t, s, ctx, domain.KindBridge, "hv-01-br0", &hv, env)
			mustAsset(t, s, ctx, domain.KindBridge, "hv-01-br1", &hv, env)
			describe(t, s, ctx, env, hv)

			got, err := s.Search(ctx, "hv-0", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if len(got) < 3 {
				t.Fatalf("got %d results, want all three: %v", len(got), titlesOf(got))
			}
			if got[0].Title != "hv-01" {
				t.Errorf("first result is %q, want hv-01 — nothing matched exactly here, "+
					"so this is the ranker's decision alone (order: %v)",
					got[0].Title, titlesOf(got))
			}
		})
	}
}

// Ranking is ordering, not filtering: a hit that matches only in the body still
// appears, after the ones that match by name.
func TestSearchKeepsBodyOnlyMatchesButRanksThemLast(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil, env)

			// Named nothing like the query; matches only through its vendor.
			other := mustAsset(t, s, ctx, domain.KindSwitch, "edge-42", nil, env)
			row, err := s.GetAsset(ctx, other)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			a := row.Asset
			vendor := "sw-core-1 replacement"
			a.Vendor = &vendor
			if err := s.UpdateAsset(ctx, testActor, &a, []string{env}); err != nil {
				t.Fatalf("describing: %v", err)
			}

			got, err := s.Search(ctx, "sw-core-1", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			titles := titlesOf(got)
			if len(got) < 2 {
				t.Fatalf("a body-only match was dropped: %v", titles)
			}
			if titles[0] != "sw-core-1" {
				t.Errorf("order is %v, want the exact name first", titles)
			}
		})
	}
}
