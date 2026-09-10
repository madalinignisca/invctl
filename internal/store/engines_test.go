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
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// ---------------------------------------------------------------------------
// The Postgres template database (WP-I3, second pass)
// ---------------------------------------------------------------------------
//
// WP-I3's first pass found the suite replaying every migration for every test
// and fixed it FOR SQLITE ONLY, by migrating a template file once per process
// and copying it: 98s to 11s. The Postgres half kept migrating per test, and
// the note left in .github/workflows/ci.yml -- "if this number needs raising
// again, that is WP-I3 asking to be done rather than a number to increase" --
// came due when the 30m cap was hit at 63 migrations.
//
// MEASURED on a 12th-gen i5, PostgreSQL 17, before changing anything:
//
//	per-test Migrate           493ms   (63 migrations, 10 runs, tight variance)
//	CREATE DATABASE TEMPLATE    32ms
//	DROP DATABASE                4.6ms
//
// 493ms is not client chatter -- that is 7.8ms per migration against a 0.05ms
// round trip, so it is DDL executing in the server. Batching the migrations
// into one transaction or dropping goose's bookkeeping would have saved about
// a fifth of it and left the cost in place. Copying the finished database
// executes no DDL at all, which is why it is 13x rather than 1.2x.
//
// Four concurrent clones from one template took 52ms, not 4x33ms, so packages
// running side by side under `go test ./...` do not serialise on it.
//
// ONE DATABASE PER TEST RATHER THAN ONE SCHEMA, and that also retires a bug
// class this file already carried a long comment about. Schema names came from
// t.Name() truncated to 40 characters, so two tests agreeing that far shared a
// name and a single leaked schema failed every later run with "already
// exists", on Postgres only. These names carry the pid and a sequence number,
// so a database leaked by a killed run collides with nothing -- it wastes a
// few megabytes until somebody drops it, rather than breaking the suite.
var (
	pgTemplateOnce sync.Once
	pgTemplateName string
	pgTemplateErr  error
	pgDBSeq        = make(chan int, 1)
)

func init() { pgDBSeq <- 0 }

// nextTestDBName is unique per process and per call. Postgres identifiers cap
// at 63 bytes; pid and sequence keep this far below it without needing the
// test's name, which is what made the old schema names collide.
func nextTestDBName() string {
	n := <-pgDBSeq
	n++
	pgDBSeq <- n
	return fmt.Sprintf("invctl_t_%d_%d", os.Getpid(), n)
}

// pgTemplateDB builds the migrated template once per process and returns its
// name. Every later test copies it instead of migrating.
func pgTemplateDB(t *testing.T) string {
	t.Helper()
	pgTemplateOnce.Do(func() {
		baseDSN := os.Getenv(postgresDSNEnv)
		admin, err := Open(DriverPostgres, baseDSN)
		if err != nil {
			pgTemplateErr = fmt.Errorf("opening postgres to build the template: %w", err)
			return
		}
		defer admin.Close()

		name := fmt.Sprintf("invctl_tmpl_%d", os.Getpid())
		sweepDeadTemplates(admin, name)
		if _, err := admin.Writer.Exec(`DROP DATABASE IF EXISTS ` + name); err != nil {
			pgTemplateErr = fmt.Errorf("dropping a stale template: %w", err)
			return
		}
		if _, err := admin.Writer.Exec(`CREATE DATABASE ` + name); err != nil {
			pgTemplateErr = fmt.Errorf("creating the template database: %w", err)
			return
		}

		tdb, err := Open(DriverPostgres, withDatabase(baseDSN, name))
		if err != nil {
			pgTemplateErr = fmt.Errorf("opening the template database: %w", err)
			return
		}
		defer tdb.Close()
		// pg_trgm goes in the TEMPLATE, so every copy inherits it. It used to
		// be created once in the base database's public schema and reached
		// through the search path; a copied database has its own public schema
		// and needs its own extension.
		if _, err := tdb.Writer.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
			pgTemplateErr = fmt.Errorf("creating pg_trgm in the template: %w", err)
			return
		}
		if err := Migrate(context.Background(), tdb); err != nil {
			pgTemplateErr = fmt.Errorf("migrating the template: %w", err)
			return
		}
		pgTemplateName = name
	})
	if pgTemplateErr != nil {
		t.Fatalf("postgres template: %v", pgTemplateErr)
	}
	return pgTemplateName
}

// openTestPostgres gives each test its own database, copied from the migrated
// template, so tests can run against one container without seeing each other's
// rows and without paying for the migrations again.
func openTestPostgres(t *testing.T) *DB {
	t.Helper()
	return openTestPostgresFrom(t, pgTemplateDB(t))
}

// openTestPostgresRaw gives an empty, UNMIGRATED database -- the one caller is
// the test that drives Migrate itself. It carries pg_trgm because the
// migrations expect the extension to exist, not the index that uses it.
func openTestPostgresRaw(t *testing.T) *DB {
	t.Helper()
	db := openTestPostgresFrom(t, "")
	if _, err := db.Writer.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
		t.Fatalf("creating pg_trgm: %v", err)
	}
	return db
}

// openTestPostgresFrom creates one database, optionally copied from template,
// and hands back a pool on it.
func openTestPostgresFrom(t *testing.T, template string) *DB {
	t.Helper()
	baseDSN := os.Getenv(postgresDSNEnv)
	name := nextTestDBName()

	admin, err := Open(DriverPostgres, baseDSN)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer admin.Close()

	create := `CREATE DATABASE ` + name
	if template != "" {
		create += ` TEMPLATE ` + template
	}
	if _, err := admin.Writer.Exec(create); err != nil {
		t.Fatalf("creating database %s: %v", name, err)
	}

	// DROP REGISTERED HERE, immediately after the CREATE succeeds and before
	// the Open below that can t.Fatalf -- the same window this file has had to
	// close twice before, once here and once in internal/web.
	//
	// t.Cleanup runs LIFO, so the pool's Close (registered below) runs FIRST
	// and this drop second. That ordering is now load-bearing in a way it was
	// not for a schema: DROP DATABASE is REFUSED while any session is still
	// connected, where DROP SCHEMA CASCADE simply worked. WITH (FORCE) is the
	// belt to that braces -- it terminates a connection some future test
	// forgot, rather than leaving a database behind that nothing will collect.
	t.Cleanup(func() {
		cleanup, err := Open(DriverPostgres, baseDSN)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Writer.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
	})

	db, err := Open(DriverPostgres, withDatabase(baseDSN, name))
	if err != nil {
		t.Fatalf("opening postgres database %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// withDatabase swaps the database name in a DSN, keeping every other
// parameter. The tests address one server and many databases on it.
func withDatabase(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		// A DSN this malformed fails at Open with a better message than
		// anything invented here.
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

// migrated is shorthand for the common "give me a fresh database" pattern.
func migrated(t *testing.T, e Engine) *DB {
	t.Helper()
	return e.Open(t)
}

// dropPgTemplate is called from TestMain once the suite has finished. The
// per-test databases collect themselves through t.Cleanup; the TEMPLATE has no
// test to hang a cleanup on, and the first run of this change left two 13MB
// databases behind for exactly that reason.
func dropPgTemplate() {
	if pgTemplateName == "" {
		return
	}
	baseDSN := os.Getenv(postgresDSNEnv)
	admin, err := Open(DriverPostgres, baseDSN)
	if err != nil {
		return
	}
	defer admin.Close()
	_, _ = admin.Writer.Exec(`DROP DATABASE IF EXISTS ` + pgTemplateName + ` WITH (FORCE)`)
}

// sweepDeadTemplates collects templates left by a run that was killed before
// TestMain could drop its own -- a SIGKILL, a laptop lid, a CI cancellation.
//
// IT CHECKS THAT THE OWNING PROCESS IS ACTUALLY GONE rather than dropping
// every template it finds, because `go test ./...` runs each package as its
// own process: a blanket sweep here would delete a sibling package's template
// out from under it, mid-run, and present as a random Postgres failure in a
// package this file does not touch.
func sweepDeadTemplates(admin *DB, mine string) {
	var names []string
	if err := admin.Reader.Select(&names,
		`SELECT datname FROM pg_database WHERE datname LIKE 'invctl_tmpl_%'`); err != nil {
		return
	}
	for _, name := range names {
		if name == mine {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(name, "invctl_tmpl_"))
		if err != nil {
			continue
		}
		// Signal 0 tests for existence without delivering anything.
		//
		// EPERM COUNTS AS ALIVE. It means the process exists and belongs to
		// somebody else -- another user sharing this Postgres -- which is the
		// strongest possible reason not to drop its template. Only ESRCH, "no
		// such process", says the owner is gone.
		if proc, err := os.FindProcess(pid); err == nil {
			err := proc.Signal(syscall.Signal(0))
			if err == nil || errors.Is(err, syscall.EPERM) {
				continue // still running: not ours to collect
			}
		}
		_, _ = admin.Writer.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
	}
}
