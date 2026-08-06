// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package store owns all database access. Handlers never see a *sqlx.DB; they
// talk to the Store interface.
//
// Every query in this package must run unmodified on SQLite and PostgreSQL.
// Placeholders are always `?` and are rewritten by sqlx.Rebind before
// execution -- never write $1 here. The only sanctioned dialect split is
// search, which lives behind the Search interface.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Supported drivers. These are the names used in configuration and in the
// Driver field; the database/sql driver actually registered differs for
// PostgreSQL, where pgx registers itself as "pgx".
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// sqlDriver maps a configuration driver name to the registered database/sql
// driver name.
func sqlDriver(driver string) string {
	if driver == DriverPostgres {
		return "pgx"
	}
	return "sqlite"
}

func init() {
	// sqlx picks its placeholder dialect from the driver name. It knows
	// "pgx" and "sqlite3" out of the box but not modernc's "sqlite", where it
	// would fall back to leaving the query untouched. That happens to be
	// correct for SQLite, but relying on an unknown-driver fallback to do the
	// right thing is the kind of thing that breaks quietly, so say it.
	sqlx.BindDriver("sqlite", sqlx.QUESTION)
}

// DB holds the connection pools.
//
// SQLite gets two: a writer capped at a single connection because SQLite
// permits exactly one writer, and a reader pool for everything else. Without
// the cap, concurrent handlers produce intermittent SQLITE_BUSY that looks
// like a random failure and is miserable to diagnose. PostgreSQL needs no such
// split, so both fields point at the same pool.
type DB struct {
	Reader *sqlx.DB
	Writer *sqlx.DB
	Driver string
}

// Open connects to the database and applies engine-appropriate settings.
func Open(driver, dsn string) (*DB, error) {
	switch driver {
	case DriverSQLite:
		return openSQLite(dsn)
	case DriverPostgres:
		return openPostgres(dsn)
	default:
		return nil, fmt.Errorf("opening database: unsupported driver %q", driver)
	}
}

// sqlitePragmas are applied to every connection in both pools.
//
// foreign_keys is the critical one: it is OFF by default in SQLite, and
// without it every REFERENCES clause in the schema silently does nothing.
var sqlitePragmas = []string{
	"foreign_keys(1)",
	"journal_mode(WAL)",
	"busy_timeout(5000)",
	"synchronous(NORMAL)",
}

func openSQLite(dsn string) (*DB, error) {
	writerDSN, err := sqliteDSN(dsn, true)
	if err != nil {
		return nil, err
	}
	readerDSN, err := sqliteDSN(dsn, false)
	if err != nil {
		return nil, err
	}

	// An in-memory database lives inside its connection. Two pools would
	// therefore be two *different* empty databases: migrations would apply to
	// the writer and every read would fail with "no such table", after a
	// startup that logged no error at all. Collapse to a single connection --
	// in-memory is a single-process, test-only mode, so serialising costs
	// nothing and the reader is guaranteed to see the writer's schema.
	if isMemoryDSN(dsn) {
		pool, err := sqlx.Open(sqlDriver(DriverSQLite), writerDSN)
		if err != nil {
			return nil, fmt.Errorf("opening sqlite in-memory database: %w", err)
		}
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		pool.SetConnMaxLifetime(0)

		db := &DB{Reader: pool, Writer: pool, Driver: DriverSQLite}
		if err := db.verify(); err != nil {
			// The verify error is what the caller needs; a Close failure on an
			// already-broken handle adds nothing and must not mask it.
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}

	writer, err := sqlx.Open(sqlDriver(DriverSQLite), writerDSN)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite writer: %w", err)
	}
	// One writer, held open: SQLite serialises writes anyway, and a pool of
	// one turns lock contention into queueing inside the driver.
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	reader, err := sqlx.Open(sqlDriver(DriverSQLite), readerDSN)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("opening sqlite reader: %w", err)
	}
	reader.SetMaxOpenConns(8)
	reader.SetMaxIdleConns(8)

	db := &DB{Reader: reader, Writer: writer, Driver: DriverSQLite}
	if err := db.verify(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteDSN appends the pragma parameters, and for the writer requests an
// immediate transaction lock so that a write transaction takes the lock at
// BEGIN rather than discovering a conflict partway through and rolling back.
func sqliteDSN(dsn string, writer bool) (string, error) {
	base, existing, _ := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(existing)
	if err != nil {
		return "", fmt.Errorf("parsing sqlite dsn: %w", err)
	}
	for _, p := range sqlitePragmas {
		// WAL needs a file to journal into; an in-memory database rejects it.
		if isMemoryDSN(dsn) && strings.HasPrefix(p, "journal_mode") {
			continue
		}
		values.Add("_pragma", p)
	}
	if writer {
		values.Set("_txlock", "immediate")
	} else {
		values.Del("_txlock")
	}
	return base + "?" + values.Encode(), nil
}

func openPostgres(dsn string) (*DB, error) {
	pool, err := sqlx.Open(sqlDriver(DriverPostgres), dsn)
	if err != nil {
		return nil, fmt.Errorf("opening postgres: %w", err)
	}
	pool.SetMaxOpenConns(16)
	pool.SetMaxIdleConns(4)
	pool.SetConnMaxLifetime(time.Hour)

	db := &DB{Reader: pool, Writer: pool, Driver: DriverPostgres}
	if err := db.verify(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// verify proves the connection works and, on SQLite, that foreign keys are
// actually on. A silently-ignored REFERENCES clause is the single most
// expensive way to discover a misconfiguration months later.
func (db *DB) verify() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.Writer.PingContext(ctx); err != nil {
		return fmt.Errorf("pinging database: %w", err)
	}
	if db.Driver != DriverSQLite {
		return nil
	}
	for _, pool := range []*sqlx.DB{db.Writer, db.Reader} {
		var on int
		if err := pool.GetContext(ctx, &on, "PRAGMA foreign_keys"); err != nil {
			return fmt.Errorf("checking foreign_keys pragma: %w", err)
		}
		if on != 1 {
			return fmt.Errorf("checking foreign_keys pragma: expected 1, got %d", on)
		}
	}
	return nil
}

// Close releases both pools.
func (db *DB) Close() error {
	var firstErr error
	if db.Writer != nil {
		if err := db.Writer.Close(); err != nil {
			firstErr = err
		}
	}
	// Guard against the PostgreSQL case where both fields are the same pool.
	if db.Reader != nil && db.Reader != db.Writer {
		if err := db.Reader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Rebind converts `?` placeholders to the driver's dialect. Every query in
// this package goes through it.
func (db *DB) Rebind(query string) string { return db.Writer.Rebind(query) }

// SQLDB exposes the underlying writer for goose, which wants a *sql.DB.
func (db *DB) SQLDB() *sql.DB { return db.Writer.DB }

// isMemoryDSN reports whether a SQLite DSN asks for an in-memory database, in
// either of the two spellings SQLite accepts.
func isMemoryDSN(dsn string) bool {
	base, query, _ := strings.Cut(dsn, "?")
	if strings.Contains(base, ":memory:") {
		return true
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return false
	}
	return values.Get("mode") == "memory"
}
