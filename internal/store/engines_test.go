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
	// OpenRaw gives an empty database with NO migrations applied. Only a test
	// that needs to stop partway through the migration history wants this --
	// see migrateTo.
	OpenRaw func(t *testing.T) *DB
}

// Engines returns every backend available in this environment, each already
// migrated. Use it as the outer loop of a store test.
func Engines(t *testing.T) []Engine {
	t.Helper()
	engines := []Engine{{Name: "sqlite", Open: openTestSQLite, OpenRaw: openTestSQLiteRaw}}
	if os.Getenv(postgresDSNEnv) != "" {
		engines = append(engines, Engine{
			Name: "postgres", Open: openTestPostgres, OpenRaw: openTestPostgresRaw,
		})
	} else {
		t.Logf("%s not set: skipping the PostgreSQL half of this test", postgresDSNEnv)
	}
	return engines
}

// openTestSQLite gives a test its own migrated database.
//
// BY COPYING A TEMPLATE, NOT BY REPLAYING THE MIGRATIONS. Measured: opening and
// migrating one SQLite database costs 295ms, and this package alone has 306
// test functions, most of which run against both engines and several of which
// build more than one store. That is the great majority of a suite which had
// crept to 586s on CI and failed a release tag on Go's ten-minute timeout.
//
// Every test still gets a private file with nobody else writing to it -- the
// isolation is identical. What changes is how the file comes to exist: forty
// migrations replayed, or a byte copy of the same result.
//
// The template is built once per process, under a mutex, and the migrated
// database is CLOSED before it is copied so WAL frames are checkpointed back
// into the main file. Copying an open WAL database is the torn read this
// project has been bitten by in production, and it would be no less torn here.
func openTestSQLite(t *testing.T) *DB {
	t.Helper()
	template := sqliteTemplate(t)

	dsn := filepath.Join(t.TempDir(), "test.db")
	data, err := os.ReadFile(template)
	if err != nil {
		t.Fatalf("reading the sqlite template: %v", err)
	}
	if err := os.WriteFile(dsn, data, 0o600); err != nil {
		t.Fatalf("writing the test database: %v", err)
	}
	db, err := Open(DriverSQLite, "file:"+dsn)
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	// Registered AFTER the drop, and t.Cleanup runs last-in-first-out, so the
	// connection closes before the schema it is using is dropped. Splitting
	// them is what lets the drop be registered early without also closing a
	// db that does not exist yet.
	t.Cleanup(func() { db.Close() })
	return db
}

var (
	sqliteTemplateOnce sync.Once
	sqliteTemplatePath string
	sqliteTemplateErr  error
)

// sqliteTemplate returns the path to a migrated, closed database file.
func sqliteTemplate(t *testing.T) string {
	t.Helper()
	sqliteTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "invctl-sqlite-template")
		if err != nil {
			sqliteTemplateErr = err
			return
		}
		path := filepath.Join(dir, "template.db")
		db, err := Open(DriverSQLite, "file:"+path)
		if err != nil {
			sqliteTemplateErr = err
			return
		}
		if err := Migrate(context.Background(), db); err != nil {
			_ = db.Close()
			sqliteTemplateErr = err
			return
		}
		// CLOSED BEFORE IT IS COPIED. Close checkpoints the WAL into the main
		// file; copying while it is open would hand every test a database
		// missing whatever was still in the log.
		if err := db.Close(); err != nil {
			sqliteTemplateErr = err
			return
		}
		sqliteTemplatePath = path
	})
	if sqliteTemplateErr != nil {
		t.Fatalf("building the sqlite template: %v", sqliteTemplateErr)
	}
	return sqliteTemplatePath
}

func openTestSQLiteRaw(t *testing.T) *DB {
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
	db := openTestPostgresRaw(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating postgres: %v", err)
	}
	return db
}

func openTestPostgresRaw(t *testing.T) *DB {
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

	// DROP REGISTERED HERE, immediately after the CREATE succeeds, and before
	// the Open below that can t.Fatalf.
	//
	// The schema name is t.Name() truncated, so two tests whose names agree
	// that far share it. That is survivable while every run drops what it
	// made, and stops being survivable the moment one leaks: the leftover
	// then fails every later run with "schema already exists", on Postgres
	// only, presenting as a logic bug in whichever test draws the name, and
	// surviving `git stash` because it lives in the database rather than the
	// tree.
	//
	// This is the second copy of this bug. internal/web/rbac_boundary_test.go
	// had the identical window and was fixed first; this one then leaked
	// t_testreassignteamownershiprefusesthesamet_583 and failed the suite for
	// a run that had changed nothing but comments and documentation.
	t.Cleanup(func() {
		cleanup, err := Open(DriverPostgres, baseDSN)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Writer.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	})

	db, err := Open(DriverPostgres, withSearchPath(baseDSN, schema))
	if err != nil {
		t.Fatalf("opening postgres on schema %s: %v", schema, err)
	}
	t.Cleanup(func() { db.Close() })
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
