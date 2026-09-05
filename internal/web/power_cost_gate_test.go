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
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jmoiron/sqlx"
)

// §4b.13 (auth-reviewer A1): CostReport reads
//
//	if base.CanSeeCosts {
//	    ... a.Store.DeclaredPowerDraw(r.Context()) ...
//	}
//
// and the comment above it (internal/web/handlers/cost_report.go) presents
// this as a control: "the draw is only queried when there is a rate to apply
// ... scanning the estate for a draw nobody can price is wasted work". But
// nothing asserted that the query is ACTUALLY skipped -- mutating the
// condition to `if true` leaves every existing test green, because they only
// check what an ungranted viewer is SHOWN, never what ran to produce it. The
// template ends up being the only PROVEN gate, while the comment claims two.
//
// This file makes the skip itself a tested control, by counting real SQL
// statements at the driver boundary that match a substring unique to
// DeclaredPowerDraw's own query (power_cost.go's "per_asset.max_draw" derived
// table), the same technique customfieldvalues_query_count_test.go uses for
// its own N+1 guard -- deliberately a SEPARATE, smaller implementation rather
// than a shared one, because this one filters by query text and that one
// does not, and forcing them through one abstraction would make either
// harder to read for the other's sake.

// matchingCountingConn wraps one driver.Conn and atomically counts every
// query or exec whose SQL text contains match.
type matchingCountingConn struct {
	driver.Conn
	match string
	n     *int64
}

func (c *matchingCountingConn) count(query string) {
	if strings.Contains(query, c.match) {
		atomic.AddInt64(c.n, 1)
	}
}

func (c *matchingCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.count(query)
	return q.QueryContext(ctx, query, args)
}

func (c *matchingCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.count(query)
	return e.ExecContext(ctx, query, args)
}

func (c *matchingCountingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
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
	c.count(query)
	return stmt, nil
}

func (c *matchingCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Begin() //nolint:staticcheck // SA1019: intentional fallback, not the primary path
}

type matchingCountingDriver struct {
	base  driver.Driver
	match string
	n     *int64
}

func (d *matchingCountingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &matchingCountingConn{Conn: conn, match: d.match, n: d.n}, nil
}

var matchingCountingDriverSeq int64

// swapInMatchingCountingReader replaces h's store's Reader pool with a
// counting connection to the SAME on-disk file, counting only statements
// whose text contains match. See swapInCountingReader
// (customfieldvalues_query_count_test.go) for the shared reasoning about
// why a second connection to the same file is how this is done at all.
func swapInMatchingCountingReader(t *testing.T, h *harness, match string) (count func() int64) {
	t.Helper()
	probe, err := sql.Open("sqlite", "file:"+h.dsn)
	if err != nil {
		t.Fatalf("resolving the sqlite driver: %v", err)
	}
	base := probe.Driver()
	if err := probe.Close(); err != nil {
		t.Fatalf("closing the driver probe: %v", err)
	}

	var n int64
	name := fmt.Sprintf("sqlite-matching-counting-%d", atomic.AddInt64(&matchingCountingDriverSeq, 1))
	sql.Register(name, &matchingCountingDriver{base: base, match: match, n: &n})

	pool, err := sqlx.Open(name, "file:"+h.dsn+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening the matching counting reader pool: %v", err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })

	h.store.DB().Reader = pool
	return func() int64 { return atomic.LoadInt64(&n) }
}

// TestDeclaredPowerDrawNeverRunsForAnUngrantedViewer is §4b.13. "per_asset"
// is a table alias unique to DeclaredPowerDraw's own query
// (internal/store/power_cost.go) -- grepped against the whole tree before
// relying on it -- so a non-zero count here can only mean that query ran.
//
// PROVING THE GATE CAN FAIL: mutate cost_report.go's `if base.CanSeeCosts`
// to `if true` and this test goes red, because the ungranted "viewer" fetch
// then issues the query it must never issue. Verified by hand as part of
// this change's evidence and reverted -- see the work-package report, not
// checked into CI as a standing mutation.
func TestDeclaredPowerDrawNeverRunsForAnUngrantedViewer(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("viewer", "viewer-password")

	count := swapInMatchingCountingReader(t, h, "per_asset")

	resp := h.get("/reports/cost", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /reports/cost as an ungranted viewer returned %d, want 200", resp.StatusCode)
	}

	if got := count(); got != 0 {
		t.Errorf("DeclaredPowerDraw's own query ran %d time(s) for an ungranted viewer; "+
			"the comment above the CanSeeCosts check in cost_report.go claims this is skipped "+
			"and nothing proved it until now", got)
	}
}

// TestDeclaredPowerDrawDoesRunForAGrantedViewer is the control for the test
// above: it proves swapInMatchingCountingReader and the "per_asset" marker
// actually detect the query at all, rather than a substring typo making the
// previous test pass for a reason that has nothing to do with the gate.
func TestDeclaredPowerDrawDoesRunForAGrantedViewer(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("admin", "admin-password")

	count := swapInMatchingCountingReader(t, h, "per_asset")

	resp := h.get("/reports/cost", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /reports/cost as an admin returned %d, want 200", resp.StatusCode)
	}

	if got := count(); got == 0 {
		t.Fatal("DeclaredPowerDraw's own query never ran for a granted admin; the marker " +
			"or the counting plumbing is broken, and the viewer test above would prove nothing")
	}
}
