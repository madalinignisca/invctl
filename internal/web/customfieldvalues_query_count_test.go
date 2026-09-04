// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jmoiron/sqlx"
)

// WP-A4 follow-up item 2: loadCustomFieldsPanel used to call
// a.Store.GetCustomField once PER SELECT FIELD, inside its own render loop --
// an N+1 on the hottest page in the product, GET /assets/{id} and
// GET /services/{id}. This file measures the ACTUAL number of SQL round
// trips a real render issues, rather than trusting that the fix in
// internal/store/customfields.go's OptionsForFields is used the way it is
// meant to be: a source-level "does the loop still call GetCustomField"
// check would pass on a fix that batched the query and then called it once
// per field anyway, and would break the moment the loop is refactored into
// a helper with a different name. Only a real query count answers "does
// this scale with the number of select fields", which is the actual bug.
//
// HOW THE COUNT IS TAKEN: a second, read-only SQLite connection to the SAME
// on-disk file the harness already opened (harness.dsn), wrapped in a
// database/sql/driver proxy that atomically counts every query and exec
// issued through it. The harness's store.DB().Reader pool is swapped for
// this counting pool -- store.DB() returns the live *store.DB, not a copy,
// so every read the running application issues from that point on, through
// whatever code path reaches it, is counted. This is package-agnostic on
// purpose: it counts real SQL statements at the driver boundary, not calls
// to a particular Go method, so it keeps working even if the fix is later
// refactored under a different name.

// countingConn wraps one driver.Conn and atomically counts every query and
// exec it is asked to run, forwarding to whichever of the context-aware or
// legacy driver interfaces the wrapped connection actually implements.
type countingConn struct {
	driver.Conn
	n *int64
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	atomic.AddInt64(c.n, 1)
	return q.QueryContext(ctx, query, args)
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	atomic.AddInt64(c.n, 1)
	return e.ExecContext(ctx, query, args)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var stmt driver.Stmt
	var err error
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err = p.PrepareContext(ctx, query)
	} else {
		stmt, err = c.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &countingStmt{Stmt: stmt, n: c.n}, nil
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	// Legacy fallback, deliberately: this reader pool never opens a
	// transaction (every write in this codebase goes through Writer), so
	// this path is unreached in practice, but ConnBeginTx is optional on
	// driver.Conn and dropping the fallback would panic on a driver that
	// somehow lacked it rather than degrading gracefully.
	return c.Begin() //nolint:staticcheck // SA1019: intentional fallback, not the primary path
}

// countingStmt counts a query or exec that reaches the DB through a plain
// driver.Stmt rather than through the connection's own *Context methods --
// the path a driver lacking QueryerContext/ExecerContext falls back to.
type countingStmt struct {
	driver.Stmt
	n *int64
}

func (s *countingStmt) Exec(args []driver.Value) (driver.Result, error) {
	atomic.AddInt64(s.n, 1)
	return s.Stmt.Exec(args) //nolint:staticcheck // legacy driver.Stmt fallback, deliberately kept
}

func (s *countingStmt) Query(args []driver.Value) (driver.Rows, error) {
	atomic.AddInt64(s.n, 1)
	return s.Stmt.Query(args) //nolint:staticcheck // legacy driver.Stmt fallback, deliberately kept
}

// countingDriver opens countingConn wrappers around whatever real driver it
// was built with -- modernc.org/sqlite here, resolved through sql.Open+
// Driver() rather than imported directly, so this file needs no dependency
// beyond what internal/store already carries.
type countingDriver struct {
	base driver.Driver
	n    *int64
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, n: d.n}, nil
}

var countingDriverSeq int64

// swapInCountingReader replaces h's store's Reader pool with a single
// counting connection to the SAME on-disk file, and returns (reset, count):
// reset zeroes the counter, count reports the total queries and execs
// issued since the last reset. A single connection (MaxOpenConns(1)) keeps
// "one render, one number" unambiguous -- this package's requests are
// sequential, so nothing here is racing for a second connection anyway.
//
// The swap is PERMANENT for the rest of h's life -- every read after this
// call goes through the counted pool, including whatever the next login or
// POST issues. Callers that need writes between two measurements (creating
// another fixture field, say) are unaffected: this only touches Reader, and
// every write in this codebase goes through Writer.
func swapInCountingReader(t *testing.T, h *harness) (reset func(), count func() int64) {
	t.Helper()
	// sql.Open does not dial until first use, so this needs no live
	// connection -- it exists only to resolve the driver.Driver instance
	// modernc.org/sqlite registered under the name "sqlite" (already
	// imported, transitively, through internal/store).
	probe, err := sql.Open("sqlite", "file:"+h.dsn)
	if err != nil {
		t.Fatalf("resolving the sqlite driver: %v", err)
	}
	base := probe.Driver()
	if err := probe.Close(); err != nil {
		t.Fatalf("closing the driver probe: %v", err)
	}

	var n int64
	name := fmt.Sprintf("sqlite-counting-%d", atomic.AddInt64(&countingDriverSeq, 1))
	sql.Register(name, &countingDriver{base: base, n: &n})

	pool, err := sqlx.Open(name, "file:"+h.dsn+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening the counting reader pool: %v", err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })

	h.store.DB().Reader = pool
	return func() { atomic.StoreInt64(&n, 0) },
		func() int64 { return atomic.LoadInt64(&n) }
}

// TestCustomFieldsPanelOptionQueriesDoNotScaleWithFieldCount is the
// measured half of WP-A4 follow-up item 2. It renders the same asset detail
// page twice: once with one select custom field holding a value, once with
// five -- and asserts the second render issues NO MORE reader-pool queries
// than the first. A per-field GetCustomField call scales linearly with the
// number of select fields; a single batched OptionsForFields call does not,
// whatever else the rest of this (large) detail page happens to read.
func TestCustomFieldsPanelOptionQueriesDoNotScaleWithFieldCount(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]

	// The smallest render that exercises the options-fetching path at all:
	// one select field, with a value, so it is not skipped as "nothing to
	// resolve".
	oneID := mustCreateFieldViaHTTP(t, h, "asset", "qcount_one", "QCount One", "select")
	mustSetCustomFieldOptions(t, h, oneID, []string{"a", "b"}, []string{"A", "B"})
	mustSetValueViaHTTP(t, h, assetID, oneID, "a")

	resetReads, readCount := swapInCountingReader(t, h)

	resetReads()
	resp := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(resp, "QCount One") {
		t.Fatalf("the fixture field was not drawn; this measurement would prove nothing")
	}
	withOne := readCount()

	// Four MORE select fields, each holding its own value -- the exact
	// shape that used to cost one extra GetCustomField call apiece. Created
	// and populated AFTER the first measurement and BEFORE the reset ahead
	// of the second, so none of this setup work is counted.
	for i := 0; i < 4; i++ {
		code := fmt.Sprintf("qcount_extra_%d", i)
		id := mustCreateFieldViaHTTP(t, h, "asset", code, code, "select")
		mustSetCustomFieldOptions(t, h, id, []string{"a", "b"}, []string{"A", "B"})
		mustSetValueViaHTTP(t, h, assetID, id, "a")
	}

	resetReads()
	resp = body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(resp, "qcount_extra_3") {
		t.Fatalf("the fifth fixture field was not drawn; this measurement would prove nothing")
	}
	withFive := readCount()

	t.Logf("reader-pool queries: one select field = %d, five select fields = %d", withOne, withFive)
	if withFive > withOne {
		t.Errorf("rendering the panel with 5 select fields issued %d reader-pool queries against "+
			"%d for 1 -- the option lookup is scaling with the number of select fields again, "+
			"which is the N+1 this test exists to catch", withFive, withOne)
	}
}
