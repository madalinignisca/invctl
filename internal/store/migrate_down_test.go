package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

// newestReversible is how many of the most recent dialect migrations this test
// takes back down and re-applies.
//
// Not the whole chain, and that is a finding rather than a preference: going
// all the way down and back up fails on PostgreSQL at version 4, whose down
// path drops `environment_role_check` and whose up path then cannot recreate
// it ("constraint does not exist", SQLSTATE 42704). That is a defect in a
// migration written long before this test, it is only reachable by unwinding
// the entire schema, and fixing it is a separate change from proving that the
// newest migrations reverse.
//
// Two is the number of migrations most likely to be wrong: the ones somebody
// just wrote. Raise it when the older ones are fixed.
const newestReversible = 2

// A migration nobody has ever run backwards is a migration that does not
// reverse. 00019 drops a column from fifteen tables and 00020 drops a named
// CHECK — both things SQLite has historically been fussy about, and neither
// had ever been executed until this test existed.
func TestTheNewestMigrationsReverse(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := e.Open(t)
			ctx := context.Background()
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("initial up: %v", err)
			}

			sub, err := fs.Sub(migrationFS, "migrations/"+db.Driver)
			if err != nil {
				t.Fatalf("opening migrations: %v", err)
			}
			p, err := goose.NewProvider(gooseDialect(db.Driver), db.SQLDB(), sub,
				goose.WithTableName(dialectVersionTable),
				goose.WithDisableGlobalRegistry(true))
			if err != nil {
				t.Fatalf("building a goose provider: %v", err)
			}

			for i := 0; i < newestReversible; i++ {
				r, err := p.Down(ctx)
				if err != nil {
					t.Fatalf("down step %d: %v", i+1, err)
				}
				t.Logf("reversed %s", r.Source.Path)
			}
			if _, err := p.Up(ctx); err != nil {
				t.Fatalf("re-applying after down: %v", err)
			}

			// And the schema really is back: the column the newest migration
			// adds is present again, so "up" did more than update a version
			// number.
			if !columnExists(t, db, "endpoint", "lifecycle") {
				t.Error("endpoint.lifecycle is missing after down-then-up")
			}
			if !columnExists(t, db, "asset", "row_version") {
				t.Error("asset.row_version is missing after down-then-up")
			}
		})
	}
}
