package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The store suite runs against both engines. A change that only passes on
// SQLite is not done -- the portability constraint is the reason most of the
// rules in CLAUDE.md exist, and it is only real if it is tested.
//
// PostgreSQL is skipped when INV_TEST_POSTGRES_DSN is unset so that `go test
// ./...` still works without Docker; `make test` sets it and starts the
// container.

const postgresDSNEnv = "INV_TEST_POSTGRES_DSN"

// Engine is one database backend under test.
type Engine struct {
	Name string
	Open func(t *testing.T) *DB
}

// Engines returns every backend available in this environment, each already
// migrated. Use it as the outer loop of a store test.
func Engines(t *testing.T) []Engine {
	t.Helper()
	engines := []Engine{{Name: "sqlite", Open: openTestSQLite}}
	if os.Getenv(postgresDSNEnv) != "" {
		engines = append(engines, Engine{Name: "postgres", Open: openTestPostgres})
	} else {
		t.Logf("%s not set: skipping the PostgreSQL half of this test", postgresDSNEnv)
	}
	return engines
}

func openTestSQLite(t *testing.T) *DB {
	t.Helper()
	// A file rather than :memory: -- the two-pool setup means reader and
	// writer are distinct connections, and shared-cache in-memory databases
	// interact badly with WAL.
	dsn := "file:" + filepath.Join(t.TempDir(), "test.db")
	db, err := Open(DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating sqlite: %v", err)
	}
	return db
}

var (
	pgExtensionOnce sync.Once
	pgExtensionErr  error
	pgSchemaSeq     = make(chan int, 1)
)

func init() { pgSchemaSeq <- 0 }

func nextSchemaName(t *testing.T) string {
	n := <-pgSchemaSeq
	n++
	pgSchemaSeq <- n
	// Test names contain slashes and capitals; neither is welcome in an
	// unquoted identifier.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, strings.ToLower(t.Name()))
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return fmt.Sprintf("t_%s_%d", safe, n)
}

// openTestPostgres gives each test its own schema, so tests can run in
// parallel against one container without seeing each other's rows.
func openTestPostgres(t *testing.T) *DB {
	t.Helper()
	baseDSN := os.Getenv(postgresDSNEnv)

	// pg_trgm is database-wide; create it once in public and keep public on
	// the search path so gin_trgm_ops resolves from the per-test schema.
	pgExtensionOnce.Do(func() {
		admin, err := Open(DriverPostgres, baseDSN)
		if err != nil {
			pgExtensionErr = fmt.Errorf("opening postgres for extension setup: %w", err)
			return
		}
		defer admin.Close()
		if _, err := admin.Writer.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
			pgExtensionErr = fmt.Errorf("creating pg_trgm: %w", err)
		}
	})
	if pgExtensionErr != nil {
		t.Fatalf("postgres setup: %v", pgExtensionErr)
	}

	schema := nextSchemaName(t)
	admin, err := Open(DriverPostgres, baseDSN)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	if _, err := admin.Writer.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("creating schema %s: %v", schema, err)
	}
	admin.Close()

	db, err := Open(DriverPostgres, withSearchPath(baseDSN, schema))
	if err != nil {
		t.Fatalf("opening postgres on schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanup, err := Open(DriverPostgres, baseDSN)
		if err != nil {
			return
		}
		defer cleanup.Close()
		cleanup.Writer.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	})

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating postgres: %v", err)
	}
	return db
}

// withSearchPath appends a search_path runtime parameter. pgx forwards
// unrecognised query parameters to the server as runtime settings.
func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema + ",public"
}

// migrated is shorthand for the common "give me a fresh database" pattern.
func migrated(t *testing.T, e Engine) *DB {
	t.Helper()
	return e.Open(t)
}
