package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

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
	return nil
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
