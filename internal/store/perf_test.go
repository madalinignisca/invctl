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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
)

// WP-I3. Measure first, and only then decide whether anything is wrong.
//
// THE POINT IS THE SIZE. Every other test in this package runs against a
// fixture of tens of rows, where an O(n²) walk and a well-indexed query are
// indistinguishable. The failures this work package exists to find only appear
// at a scale nobody has run this against: a few thousand assets, ten thousand
// prefixes, fifty thousand addresses -- which is the size at which an IPAM
// stops being pleasant in the tools people are migrating from.
//
// ROWS GO IN THROUGH BULK INSERTS, NOT THROUGH THE STORE'S OWN WRITE PATH.
// CreateAsset maintains the closure table, writes a change_log row and updates
// the search index in one transaction, which is exactly right for an operator
// typing into a form and is minutes of wall clock for sixty thousand rows. What
// is being measured here is the READ side, so the estate is assembled directly
// and the closure is built in one statement afterwards.
//
// That is a deliberate trade with one real cost: this fixture proves nothing
// about write performance. Said out loud rather than left to be assumed.

// perfScale is the estate these benchmarks build.
//
// Chosen to be uncomfortable rather than round: 4,000 assets across 120 racks
// is a mid-sized estate, and 10,000 prefixes with 50,000 addresses is an IPAM
// somebody has been filling in for years.
type perfScale struct {
	Racks     int
	PerRack   int
	Prefixes  int
	Addresses int
	Services  int
}

var defaultScale = perfScale{Racks: 120, PerRack: 32, Prefixes: 10000, Addresses: 50000, Services: 400}

// openBenchSQLite is openTestSQLite for a testing.TB.
//
// The Engine helpers take *testing.T, which a benchmark does not have. Rather
// than widen every signature in engines_test.go for one caller, this mirrors
// the same two steps -- a file-backed database, then migrate -- and says so.
func openBenchSQLite(tb testing.TB) *DB {
	tb.Helper()
	// A file rather than :memory:, for the reason openTestSQLiteRaw gives: the
	// reader and writer are distinct connections and shared-cache in-memory
	// databases interact badly with WAL.
	dsn := "file:" + filepath.Join(tb.TempDir(), "perf.db")
	db, err := Open(DriverSQLite, dsn)
	if err != nil {
		tb.Fatalf("opening sqlite: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		tb.Fatalf("migrating sqlite: %v", err)
	}
	return db
}

// buildLargeEstate populates a database directly and returns it.
func buildLargeEstate(tb testing.TB, open func(testing.TB) *DB, scale perfScale) (*SQLStore, context.Context) {
	tb.Helper()
	return seedInto(tb, New(open(tb)), scale)
}

// seedInto fills a store the caller owns.
func seedInto(tb testing.TB, s *SQLStore, scale perfScale) (*SQLStore, context.Context) {
	tb.Helper()
	ctx := context.Background()
	now := domain.FormatTime(s.Now())

	w := s.DB().Writer
	exec := func(query string, args ...any) {
		if _, err := w.Exec(w.Rebind(query), args...); err != nil {
			tb.Fatalf("seeding: %v\n%s", err, query)
		}
	}

	// One environment and one team, because neither is what is being measured
	// and a hundred of each would only make the fixture slower to build.
	envID := NewID()
	exec(`INSERT INTO environment (id, code, name, role, in_scope, criticality, created_at, updated_at)
	      VALUES (?, ?, ?, ?, TRUE, 3, ?, ?)`, envID, "prod", "Production", "production", now, now)

	// Assets: a site, racks under it, boxes in the racks.
	siteID := NewID()
	exec(`INSERT INTO asset (id, kind, name, lifecycle, attrs, created_at, updated_at)
	      VALUES (?, ?, ?, ?, '{}', ?, ?)`, siteID, domain.KindSite, "dc-perf", domain.LifecycleActive, now, now)

	assetIDs := make([]string, 0, scale.Racks*scale.PerRack)
	for r := 0; r < scale.Racks; r++ {
		rackID := NewID()
		exec(`INSERT INTO asset (id, kind, name, parent_id, u_height, width_mm, usable_depth_mm,
		                         lifecycle, attrs, created_at, updated_at)
		      VALUES (?, ?, ?, ?, 42, 600, 900, ?, '{}', ?, ?)`,
			rackID, domain.KindRack, fmt.Sprintf("rack-%03d", r), siteID, domain.LifecycleActive, now, now)

		for i := 0; i < scale.PerRack; i++ {
			id := NewID()
			assetIDs = append(assetIDs, id)
			exec(`INSERT INTO asset (id, kind, name, parent_id, rack_position, rack_face,
			                         lifecycle, attrs, created_at, updated_at)
			      VALUES (?, ?, ?, ?, ?, 'front', ?, '{}', ?, ?)`,
				id, domain.KindServer, fmt.Sprintf("srv-%03d-%02d", r, i), rackID, (i%42)+1,
				domain.LifecycleActive, now, now)
			exec(`INSERT INTO asset_environment (asset_id, environment_id) VALUES (?, ?)`, id, envID)
		}
	}

	// The closure, in three statements rather than sixty thousand round trips.
	// Depth 0 is every node against itself; depth 1 is every child; depth 2 is
	// every grandchild. The tree here is exactly three deep, so that is all of
	// it -- a general recursive fill would be slower to write and would prove
	// nothing extra.
	exec(`INSERT INTO asset_closure (ancestor_id, descendant_id, depth) SELECT id, id, 0 FROM asset`)
	exec(`INSERT INTO asset_closure (ancestor_id, descendant_id, depth)
	      SELECT parent_id, id, 1 FROM asset WHERE parent_id IS NOT NULL`)
	exec(`INSERT INTO asset_closure (ancestor_id, descendant_id, depth)
	      SELECT p.parent_id, c.id, 2
	      FROM asset c JOIN asset p ON p.id = c.parent_id
	      WHERE p.parent_id IS NOT NULL`)

	// Prefixes: a /8 carved into /16s carved into /24s, which is the shape that
	// makes a containment tree do work.
	for i := 0; i < scale.Prefixes; i++ {
		var cidr string
		switch {
		case i == 0:
			cidr = "10.0.0.0/8"
		case i <= 200:
			cidr = fmt.Sprintf("10.%d.0.0/16", i-1)
		default:
			cidr = fmt.Sprintf("10.%d.%d.0/24", (i-201)/250, (i-201)%250)
		}
		p, err := domain.ParsePrefix(cidr)
		if err != nil {
			tb.Fatalf("normalising %s: %v", cidr, err)
		}
		exec(`INSERT INTO prefix (id, cidr_text, addr_family, addr_start, addr_end,
		                          environment_id, created_at, updated_at)
		      VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			NewID(), p.Text, p.Family, p.Start, p.End, envID, now, now)
	}

	// Addresses spread across the /24s.
	for i := 0; i < scale.Addresses; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", i/60000, (i/250)%250, (i%250)+1)
		a, err := domain.ParseAddr(ip)
		if err != nil {
			tb.Fatalf("normalising %s: %v", ip, err)
		}
		// A host address is a single point, so the table carries addr_start and
		// no end -- the four-column pattern is for prefixes and ranges. `role`
		// is a foreign key into the ip_address_role vocabulary, not free text.
		exec(`INSERT INTO ip_address (id, addr_text, addr_family, addr_start, role)
		      VALUES (?, ?, ?, ?, 'primary')`,
			NewID(), a.Text, a.Family, a.Start)
	}

	// Services, so the impact engine has a graph rather than an empty one.
	for i := 0; i < scale.Services; i++ {
		id := NewID()
		// `kind` is a foreign key into the service_kind vocabulary and is NOT
		// NULL, which the schema is right about and a hand-written insert is
		// easy to forget.
		exec(`INSERT INTO service (id, code, name, kind, tier, availability, environment_id,
		                           lifecycle, created_at, updated_at)
		      VALUES (?, ?, ?, 'api', ?, 'standalone', ?, ?, ?, ?)`,
			id, fmt.Sprintf("svc-%04d", i), fmt.Sprintf("Service %d", i), (i%3)+1,
			envID, domain.LifecycleActive, now, now)
		// One instance per service, on a real host, so DownInstances has work.
		exec(`INSERT INTO service_instance (id, service_id, host_asset_id, runtime_type,
		                                    role, lifecycle, created_at, updated_at)
		      VALUES (?, ?, ?, 'systemd', 'primary', ?, ?, ?)`,
			NewID(), id, assetIDs[i%len(assetIDs)], domain.LifecycleActive, now, now)
	}

	return s, ctx
}

// TestLargeEstateBuildsTheShapeItClaims.
//
// A BENCHMARK AGAINST A FIXTURE THAT DID NOT BUILD IS A FAST NUMBER AND A LIE.
// The inserts above bypass the store, so nothing else would notice if one of
// them silently wrote no rows -- and every measurement below would then be
// timing an empty database.
func TestLargeEstateBuildsTheShapeItClaims(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a large estate")
	}
	small := perfScale{Racks: 4, PerRack: 4, Prefixes: 50, Addresses: 200, Services: 10}
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			open := func(tb testing.TB) *DB { return e.Open(tb.(*testing.T)) }
			s, ctx := buildLargeEstate(t, open, small)
			for _, tc := range []struct {
				what  string
				query string
				want  int
			}{
				{"assets", `SELECT COUNT(*) FROM asset`, 1 + small.Racks + small.Racks*small.PerRack},
				{"prefixes", `SELECT COUNT(*) FROM prefix`, small.Prefixes},
				{"addresses", `SELECT COUNT(*) FROM ip_address`, small.Addresses},
				{"services", `SELECT COUNT(*) FROM service`, small.Services},
				{"closure", `SELECT COUNT(*) FROM asset_closure WHERE depth = 2`, small.Racks * small.PerRack},
			} {
				var got int
				if err := s.readOne(ctx, &got, tc.query); err != nil {
					t.Fatalf("counting %s: %v", tc.what, err)
				}
				if got != tc.want {
					t.Errorf("%s = %d, want %d", tc.what, got, tc.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The measurements
// ---------------------------------------------------------------------------

// The estate is built once per process and outlives every benchmark in it.
//
// NOT THROUGH tb.Cleanup, WHICH IS WHY THIS IS FIDDLY. A database opened under
// the first benchmark's TB is closed when that benchmark ends, and every
// benchmark after it then measures `sql: database is closed` -- which is fast,
// green-looking in the wrong light, and completely meaningless. It is torn down
// in TestMain instead, after every test and benchmark in the package has run.
var (
	sharedStore *SQLStore
	sharedCtx   context.Context
	sharedDB    *DB
	sharedDir   string
)

// TestMain owns the shared estate's lifetime.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedDB != nil {
		_ = sharedDB.Close()
	}
	if sharedDir != "" {
		_ = os.RemoveAll(sharedDir)
	}
	os.Exit(code)
}

func estate(b *testing.B) (*SQLStore, context.Context) {
	b.Helper()
	if sharedStore == nil {
		start := time.Now()
		dir, err := os.MkdirTemp("", "invctl-perf")
		if err != nil {
			b.Fatalf("temp dir: %v", err)
		}
		sharedDir = dir
		db, err := Open(DriverSQLite, "file:"+filepath.Join(dir, "perf.db"))
		if err != nil {
			b.Fatalf("opening sqlite: %v", err)
		}
		if err := Migrate(context.Background(), db); err != nil {
			b.Fatalf("migrating: %v", err)
		}
		sharedDB = db
		sharedStore, sharedCtx = seedInto(b, New(db), defaultScale)
		b.Logf("built the estate in %s", time.Since(start).Round(time.Millisecond))
	}
	return sharedStore, sharedCtx
}

// BenchmarkPrefixTree is the IPAM page: ten thousand prefixes resolved into a
// containment tree with utilisation, in one request.
func BenchmarkPrefixTree(b *testing.B) {
	s, ctx := estate(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := s.ListPrefixTree(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) == 0 {
			b.Fatal("no prefixes; the benchmark is measuring nothing")
		}
	}
}

// BenchmarkEstateFindings is the overview, which every session opens first and
// which fans out across every report in the system.
func BenchmarkEstateFindings(b *testing.B) {
	s, ctx := estate(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.EstateFindings(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEstateFit is the physical sweep added in C2/C4 and extended in C3.
// It is separated from the findings above because it is the newest code here
// and the most likely to be doing something per-rack that should be done once.
func BenchmarkEstateFit(b *testing.B) {
	s, ctx := estate(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.EstateFit(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSimulateRackLoss is the question the software exists to answer, on
// the largest outage somebody would actually simulate.
func BenchmarkSimulateRackLoss(b *testing.B) {
	s, ctx := estate(b)
	var rackID string
	if err := s.readOne(ctx, &rackID,
		`SELECT id FROM asset WHERE kind = ? ORDER BY name LIMIT 1`, domain.KindRack); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{rackID}, WindowSeconds: 180}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAssetList is the page people leave open.
func BenchmarkAssetList(b *testing.B) {
	s, ctx := estate(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := s.ListAssets(ctx, AssetFilter{Limit: 100})
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) == 0 {
			b.Fatal("no assets")
		}
	}
}

// BenchmarkSearch is the box at the top of every page, which an operator
// reaches for with an address from an alert.
func BenchmarkSearch(b *testing.B) {
	s, ctx := estate(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search(ctx, "10.5.3.7", 25); err != nil {
			b.Fatal(err)
		}
	}
}

// The prefix page's two halves, measured separately.
//
// OPTIMISING BEFORE MEASURING IS HOW A CORRELATED SUBQUERY GETS REWRITTEN INTO
// A MEMORY HOG THAT SAVES NOTHING. These two exist so the split between "count
// the addresses in each prefix" and "read every reservation and assignment" is
// evidence rather than a guess.
func BenchmarkPrefixListOnly(b *testing.B) {
	s, ctx := estate(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ListPrefixes(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAllocationSpansOnly(b *testing.B) {
	s, ctx := estate(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.allocationSpans(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// TestPrefixTreeDoesNotGoQuadratic.
//
// THE REGRESSION GUARD FOR A BUG THAT SHIPPED. ListPrefixTree rescanned every
// node for every node to find its children -- a hundred million UUID
// comparisons at ten thousand prefixes -- and no test noticed, because every
// other fixture in this package is small enough that quadratic and linear are
// the same number. It was found by building something large and measuring it.
//
// A SHAPE TEST, NOT A STOPWATCH. Asserting "under 200ms" would fail on a busy
// machine and pass on a fast one with the quadratic restored at half the size.
// Instead it builds two estates a factor of four apart and compares the ratio.
//
// THE SIZES AND THE THRESHOLD ARE MEASURED, NOT GUESSED. The first version used
// 1000 and 4000 with a threshold of 9x, and the quadratic -- deliberately
// reinstated to check -- slipped through at 8.5x, because at that size the
// linear SQL work still dominates and dilutes the ratio. Measured both ways:
//
//	1000 -> 4000    8.5x with the bug, 4.0x without
//	2000 -> 8000   11.2x with the bug, 4.3x without
//
// So the larger pair, and a threshold of 7 that sits clear of both.
func TestPrefixTreeDoesNotGoQuadratic(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two large estates")
	}
	measure := func(prefixes int) time.Duration {
		s, ctx := buildLargeEstate(t, openBenchSQLite, perfScale{
			Racks: 2, PerRack: 2, Prefixes: prefixes, Addresses: 500, Services: 5,
		})
		// One untimed pass first, so page cache and prepared statements are
		// warm for both sizes equally.
		if _, err := s.ListPrefixTree(ctx); err != nil {
			t.Fatalf("warming: %v", err)
		}
		start := time.Now()
		rows, err := s.ListPrefixTree(ctx)
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(rows) != prefixes {
			t.Fatalf("got %d rows for %d prefixes; the fixture is not the size this "+
				"test thinks it is", len(rows), prefixes)
		}
		return time.Since(start)
	}

	small := measure(2000)
	large := measure(8000)

	// Below a millisecond the clock is the dominant term and any ratio is
	// noise. Rather than assert something meaningless, say so.
	if small < time.Millisecond {
		t.Skipf("the small case ran in %s, which is too fast to compare against", small)
	}
	ratio := float64(large) / float64(small)
	t.Logf("2000 prefixes: %s, 8000 prefixes: %s, ratio %.1fx", small, large, ratio)
	if ratio > 7 {
		t.Errorf("four times the prefixes took %.1fx the time (%s -> %s). Measured, "+
			"this ratio is about 4.3x with the child lookup indexed and about 11x "+
			"with it rescanning every node -- so this is the quadratic back",
			ratio, small, large)
	}
}
