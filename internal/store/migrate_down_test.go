package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/gabriel/invctl/internal/domain"
)

// downAll rolls every migration back, dialect first then shared, mirroring the
// order Migrate applies them in.
func downAll(t *testing.T, ctx context.Context, db *DB) {
	t.Helper()
	for _, d := range []struct{ dir, table string }{
		{"migrations/" + db.Driver, dialectVersionTable},
		{"migrations/shared", sharedVersionTable},
	} {
		sub, err := fs.Sub(migrationFS, d.dir)
		if err != nil {
			t.Fatalf("sub %s: %v", d.dir, err)
		}
		p, err := goose.NewProvider(gooseDialect(db.Driver), db.SQLDB(), sub,
			goose.WithTableName(d.table), goose.WithDisableGlobalRegistry(true))
		if err != nil {
			t.Fatalf("provider %s: %v", d.dir, err)
		}
		for {
			if _, err := p.Down(ctx); err != nil {
				if errors.Is(err, goose.ErrNoNextVersion) {
					break
				}
				t.Fatalf("down %s: %v", d.dir, err)
			}
		}
	}
}

// TestDownMigrationsRun exercises every down migration on both engines, which
// nothing did before.
//
// Worth having permanently rather than as a one-off check. DECISIONS.md records
// that POC sign-off changes the rules: migrations become additive AND
// REVERSIBLE, and every release must upgrade an existing database in place. A
// down migration nobody has run is a claim, not a property.
//
// It runs the full cycle to zero and back rather than one step, because the
// interesting failures are ordering ones: a down that leaves an index behind,
// or drops a table another migration's down still expects.
func TestDownMigrationsRun(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := e.Open(t)
			ctx := context.Background()
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("initial up: %v", err)
			}
			downAll(t, ctx, db)
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("re-up after full down: %v", err)
			}
		})
	}
}

// TestDownSurvivesAVocabularyValueAddedAsData.
//
// The down path used to be exercised only against a schema nobody had used, and
// that hid a wedge. Restoring the original vocabulary CHECK failed the moment
// anyone had added a value -- which is the first thing the up migration invites
// them to do -- and because those files are NO TRANSACTION the failure left a
// half-built table behind while goose still recorded the version as applied: a
// database that could be migrated neither forward nor back.
//
// So the down path is now tested against a database that used the feature.
func TestDownSurvivesAVocabularyValueAddedAsData(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := e.Open(t)
			ctx := context.Background()
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("up: %v", err)
			}
			s := New(db)
			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			w := db.Writer
			if _, err := w.Exec(w.Rebind(
				`INSERT INTO asset_kind (code, label, sort_order, can_host_instances, is_attachable)
				 VALUES (?, ?, ?, TRUE, TRUE)`), "bridge", "Virtual bridge", 130); err != nil {
				t.Fatalf("adding the kind: %v", err)
			}
			br, err := domain.NewAsset(NewID(), "bridge", "br0", nil, s.Now())
			if err != nil {
				t.Fatalf("building the asset: %v", err)
			}
			if err := s.CreateAsset(ctx, testActor, br, []string{envID}); err != nil {
				t.Fatalf("creating an asset of the added kind: %v", err)
			}

			downAll(t, ctx, db)
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("re-up after a down over data the feature enabled: %v", err)
			}
		})
	}
}

// TestMigrationsPreserveDataOnUpgrade.
//
// Every other migration test builds an empty database and migrates from
// scratch, so the INSERT ... SELECT copy at the heart of a table rebuild runs
// against zero rows. Removing a column from a copy list was measured to be
// entirely invisible: twenty assets lost their owner_team and the suite stayed
// green.
//
// owner_team is the subject because it is nullable and nothing else asserts on
// it -- exactly the shape a rebuild drops without anyone noticing.
func TestMigrationsPreserveDataOnUpgrade(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := e.Open(t)
			ctx := context.Background()
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("up: %v", err)
			}
			s := New(db)
			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			const team = "platform-team"
			ids := make([]string, 0, 8)
			for i := range 8 {
				id := mustAsset(t, s, ctx, domain.KindServer, fmt.Sprintf("srv-%02d", i), nil, envID)
				a, err := s.GetAsset(ctx, id)
				if err != nil {
					t.Fatalf("reading back: %v", err)
				}
				owner := team
				a.OwnerTeam = &owner
				if err := s.UpdateAsset(ctx, testActor, &a.Asset, []string{envID}); err != nil {
					t.Fatalf("setting owner_team: %v", err)
				}
				ids = append(ids, id)
			}

			// Re-running Migrate over a populated database is the upgrade path
			// the post-sign-off rule requires, and the one a rebuild breaks.
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("re-running migrate over populated data: %v", err)
			}

			survived, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM asset WHERE owner_team = ?`, team)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if survived != int64(len(ids)) {
				t.Errorf("%d of %d assets kept their owner_team across migration -- a rebuild "+
					"dropped a column from its copy list, and no other test would notice",
					survived, len(ids))
			}
		})
	}
}
