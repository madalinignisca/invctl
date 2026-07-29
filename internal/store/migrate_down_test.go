package store

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

// TestDownMigrationsRun exercises every down migration on both engines, which
// nothing else did.
//
// Worth having from now on rather than as a one-off check. DECISIONS.md records
// that POC sign-off changes the rules: migrations become additive AND
// REVERSIBLE, and every release must upgrade an existing database in place. A
// down migration nobody has run is a claim, not a property -- and the three
// migrations added just before sign-off (00009 shared, 00002 and 00003 dialect)
// are the first in this project to rebuild a table, which is exactly where a
// down migration goes wrong.
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
			// Down in reverse order of Migrate: dialect first, then shared.
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
					r, err := p.Down(ctx)
					if err != nil {
						if errors.Is(err, goose.ErrNoNextVersion) {
							break
						}
						t.Fatalf("down %s: %v", d.dir, err)
					}
					t.Logf("  down %-22s %s", d.dir, r.Source.Path)
				}
			}
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("re-up after full down: %v", err)
			}
			t.Log("  full down-then-up cycle clean")
		})
	}
}
