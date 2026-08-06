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
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/shared/*.sql migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFS embed.FS

// Two version tables, because the shared and dialect-specific migration sets
// are numbered independently. Sharing one table would mean shared/00001 and
// sqlite/00001 collide, and goose would treat the second as already applied.
const (
	sharedVersionTable  = "goose_db_version"
	dialectVersionTable = "goose_db_version_dialect"
)

// Migrate applies the shared schema followed by the dialect-specific objects.
//
// Ordering matters: the dialect set (session store, search index) assumes the
// shared tables already exist.
func Migrate(ctx context.Context, db *DB) error {
	if err := migrateDir(ctx, db, "migrations/shared", sharedVersionTable); err != nil {
		return fmt.Errorf("applying shared migrations: %w", err)
	}
	dialectDir := "migrations/" + db.Driver
	if err := migrateDir(ctx, db, dialectDir, dialectVersionTable); err != nil {
		return fmt.Errorf("applying %s migrations: %w", db.Driver, err)
	}
	return verifyForeignKeys(ctx, db)
}

// verifyForeignKeys refuses to report success on a database whose references no
// longer resolve.
//
// It exists because the obvious guard does not work. Several SQLite migrations
// rebuild a table, which means dropping it, which means turning foreign key
// enforcement off first -- six tables cascade off service_instance alone, and a
// DROP with enforcement on silently takes their rows. Those migrations ended
// with `PRAGMA foreign_key_check`, which reads like a safety net and is not
// one: goose executes migration statements with Exec, the pragma returns its
// violations as RESULT ROWS, and Exec discards them. Measured -- on a database
// holding a known dangling reference, Exec returns nil while Query on the
// identical statement returns the violation. Every one of those pragmas was
// decorative, and a rebuild that orphaned half the estate would have reported
// success.
//
// Doing it here rather than in each migration is deliberate: a check a
// migration has to remember is a check the next migration forgets, and this
// covers every migration ever added without anyone thinking about it.
//
// PostgreSQL needs nothing equivalent. It enforces references continuously and
// has no mode in which a migration can switch that off.
func verifyForeignKeys(ctx context.Context, db *DB) error {
	if db.Driver != DriverSQLite {
		return nil
	}
	type violation struct {
		Table  string `db:"table"`
		RowID  *int64 `db:"rowid"`
		Parent string `db:"parent"`
		FKID   int    `db:"fkid"`
	}
	var found []violation
	if err := db.Reader.SelectContext(ctx, &found,
		`SELECT "table", "rowid", "parent", "fkid" FROM pragma_foreign_key_check`); err != nil {
		return fmt.Errorf("checking foreign keys after migration: %w", err)
	}
	if len(found) == 0 {
		return nil
	}
	// Name the first few rather than all of them: a broken rebuild produces
	// thousands, and the table names are what an operator needs.
	var detail strings.Builder
	for i, v := range found {
		if i == 5 {
			fmt.Fprintf(&detail, " (and %d more)", len(found)-5)
			break
		}
		if i > 0 {
			detail.WriteString(", ")
		}
		fmt.Fprintf(&detail, "%s -> %s", v.Table, v.Parent)
	}
	return fmt.Errorf("migrations left %d unresolved foreign key reference(s): %s: "+
		"a table rebuild has orphaned rows, and the database must not be used in this state",
		len(found), detail.String())
}

func migrateDir(ctx context.Context, db *DB, dir, versionTable string) error {
	sub, err := fs.Sub(migrationFS, dir)
	if err != nil {
		return fmt.Errorf("opening migration dir %s: %w", dir, err)
	}
	provider, err := goose.NewProvider(gooseDialect(db.Driver), db.SQLDB(), sub,
		goose.WithTableName(versionTable),
		// Each call builds its own provider; the global registry would leak
		// migrations from one set into the other.
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return fmt.Errorf("creating goose provider for %s: %w", dir, err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("running goose up on %s: %w", dir, err)
	}
	for _, r := range results {
		slog.Debug("migration applied", "source", r.Source.Path, "version", r.Source.Version, "duration", r.Duration)
	}
	return nil
}

func gooseDialect(driver string) goose.Dialect {
	if driver == DriverPostgres {
		return goose.DialectPostgres
	}
	return goose.DialectSQLite3
}
