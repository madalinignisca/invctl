// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package scaletest measures the shapes that only misbehave at size.
//
// SEPARATE FROM THE SUITE, AND OPT-IN. It builds a five-thousand-asset estate,
// which takes long enough that putting it in `make test` would tax every run
// for a question nobody asks on most commits. It is not a substitute for the
// 28-asset fixture either: that one is for CORRECTNESS and reads well because
// you know what every row means. This one is unreadable by design and exists to
// answer "does anything fall off a cliff".
//
//	INV_SCALE=1 go test ./internal/store/scaletest/ -v -run TestAtScale
package scaletest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
)

// The estate: 20 sites × 10 racks × 25 servers.
const (
	sites          = 20
	racksPerSite   = 10
	serversPerRack = 25
)

// secondFile is a second estate, so the writer-contention probe imports
// something that does not collide with the first.
var secondRows []store.AssetImportRow

func secondFile() []store.AssetImportRow { return secondRows }

func TestAtScale(t *testing.T) {
	if os.Getenv("INV_SCALE") == "" {
		t.Skip("set INV_SCALE=1 to run the scale measurement")
	}

	dsn := "file:" + filepath.Join(t.TempDir(), "scale.db") + "?_txlock=immediate"
	db, err := store.Open(store.DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	s := store.New(db)
	if _, err := seed.Load(ctx, s); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	var csv strings.Builder
	csv.WriteString("parent,name,kind\n")
	for si := 0; si < sites; si++ {
		site := fmt.Sprintf("scale-dc-%02d", si)
		fmt.Fprintf(&csv, ",%s,site\n", site)
		for r := 0; r < racksPerSite; r++ {
			fmt.Fprintf(&csv, "%s,rack-%02d,rack\n", site, r)
			for b := 0; b < serversPerRack; b++ {
				fmt.Fprintf(&csv, "%s/rack-%02d,srv-%02d%02d%02d,server\n", site, r, si, r, b)
			}
		}
	}

	rows, problems := store.ParseAssetCSV(strings.NewReader(csv.String()))
	if len(problems) > 0 {
		t.Fatalf("parsing: %+v", problems[:1])
	}
	t.Logf("%-26s %d", "rows parsed", len(rows))

	var second strings.Builder
	second.WriteString("parent,name,kind\n")
	for si := 0; si < 4; si++ {
		site := fmt.Sprintf("probe-dc-%02d", si)
		fmt.Fprintf(&second, ",%s,site\n", site)
		for b := 0; b < 250; b++ {
			fmt.Fprintf(&second, "%s,probe-%02d%03d,server\n", site, si, b)
		}
	}
	secondRows, _ = store.ParseAssetCSV(strings.NewReader(second.String()))

	actor := domain.Actor{ID: "scale", Name: "scale", Kind: domain.ActorKindUser}
	timed := func(label string, fn func() string) {
		t0 := time.Now()
		note := fn()
		t.Logf("%-26s %-9s %s", label, time.Since(t0).Round(time.Millisecond), note)
	}

	timed("dry run", func() string {
		rep, err := s.ImportAssets(ctx, actor, rows, true)
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		return fmt.Sprintf("%d would be created, %d problems", len(rep.Created), len(rep.Problems))
	})
	timed("real import (batched)", func() string {
		rep, err := s.ImportAssetsBatched(ctx, domain.AdministratorPermit(actor), rows, nil)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if len(rep.Problems) > 0 {
			t.Fatalf("import refused: %+v", rep.Problems[:1])
		}
		return fmt.Sprintf("%d created, in batches of %d", len(rep.Created), store.ImportBatchSize)
	})

	// THE NUMBER THE BATCHING EXISTS FOR: how long another writer waits while an
	// import runs. One transaction meant the whole file; batches mean one batch.
	timed("longest wait for the writer", func() string {
		worst := time.Duration(0)
		stop := make(chan struct{})
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				t0 := time.Now()
				env, err := domain.NewEnvironment(store.NewID(),
					fmt.Sprintf("probe-%d", time.Now().UnixNano()), "probe",
					domain.EnvRoleProduction, false, 3, time.Now().UTC())
				if err == nil {
					_ = s.CreateEnvironment(ctx, actor, env)
				}
				if d := time.Since(t0); d > worst {
					worst = d
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		rep, err := s.ImportAssetsBatched(ctx, domain.AdministratorPermit(actor), secondFile(), nil)
		close(stop)
		if err != nil {
			t.Fatalf("second import: %v", err)
		}
		if len(rep.Problems) > 0 {
			t.Fatalf("second import refused: %+v", rep.Problems[:1])
		}
		return fmt.Sprintf("worst single write waited %s", worst.Round(time.Millisecond))
	})

	var assets, closure, audit int
	_ = db.Reader.Get(&assets, `SELECT COUNT(*) FROM asset`)
	_ = db.Reader.Get(&closure, `SELECT COUNT(*) FROM asset_closure`)
	_ = db.Reader.Get(&audit, `SELECT COUNT(*) FROM change_log`)
	t.Logf("%-26s %d assets, %d closure rows, %d audit rows", "estate", assets, closure, audit)

	var rack, site string
	_ = db.Reader.Get(&rack, `SELECT id FROM asset WHERE kind='rack' AND name='rack-00' LIMIT 1`)
	_ = db.Reader.Get(&site, `SELECT id FROM asset WHERE kind='site' AND name='scale-dc-00' LIMIT 1`)

	// Twice: the first pays for whatever the engine loads once.
	for _, pass := range []string{"cold", "warm"} {
		timed("impact, one rack ("+pass+")", func() string {
			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{rack}, WindowSeconds: 180})
			if err != nil {
				t.Fatalf("simulate: %v", err)
			}
			return fmt.Sprintf("%d services affected", len(res.Services))
		})
	}
	timed("impact, one whole site", func() string {
		_, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{site}, WindowSeconds: 180})
		if err != nil {
			t.Fatalf("simulate: %v", err)
		}
		return ""
	})
	timed("expiry report", func() string {
		_, err := s.Expiring(ctx, time.Now().UTC(), 12)
		if err != nil {
			t.Fatalf("expiry: %v", err)
		}
		return ""
	})
	timed("power findings", func() string {
		_, err := s.PowerFindings(ctx)
		if err != nil {
			t.Fatalf("power: %v", err)
		}
		return ""
	})
	timed("asset list, unfiltered", func() string {
		list, err := s.ListAssets(ctx, store.AssetFilter{})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		return fmt.Sprintf("%d rows", len(list))
	})
	timed("search, exact name", func() string {
		hits, err := s.Search(ctx, "srv-000012", 20)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		return fmt.Sprintf("%d hits", len(hits))
	})
}
