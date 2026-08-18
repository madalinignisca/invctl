// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/madalinignisca/invctl/internal/store"
)

// syncLogBuffer is a concurrency-safe log sink, the same shape as
// internal/web/web_test.go's syncBuffer. httptest can serve a request on its
// own goroutine and the server logs from inside that handler, so an
// unguarded bytes.Buffer would race with the assertions reading it back --
// not a risk this particular test hits today (the fixture's handlers run
// synchronously under httptest.NewRecorder), but the whole point of this
// test is that it keeps working if that ever changes.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureSecurityLog installs a text-handler logger over a concurrency-safe
// buffer as the default slog logger, and restores the previous default when
// the test ends.
func captureSecurityLog(t *testing.T) *syncLogBuffer {
	t.Helper()
	buf := &syncLogBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

// TestAScopeMissLogsTheSecurityEventGetAsset pins Controller ruling AE: the
// 404 for an out-of-scope asset id is deliberately, byte-for-byte
// indistinguishable from a 404 for a fabricated one (item 1), and the ENTIRE
// compensation for that deliberate opacity is a server-side security event
// naming the credential. If that log line can be deleted with nothing in
// this package noticing, the compensation is a comment, not a guarantee.
//
// Round 1's bounded mutation testing found exactly this gap: removing the
// auth.LogSecurityEvent call (and its now-unused imports) from GetAsset
// passed the entire internal/api suite. This test exists to close it at the
// package level; a later task adds the end-to-end version through the real
// router, but the property belongs to this task's code and the gap is
// demonstrated, not theoretical, so it is pinned here rather than deferred.
func TestAScopeMissLogsTheSecurityEventGetAsset(t *testing.T) {
	f := newAPIHandlerFixture(t)
	buf := captureSecurityLog(t)

	// assetDevOnly is real but carries no `prod` membership: a scope miss,
	// not an absence.
	rec := f.serveByID(f.api.GetAsset, f.assetDevOnly, tokProd)
	if rec.Code != 404 {
		t.Fatalf("got %d, want 404", rec.Code)
	}

	logged := buf.String()
	if !strings.Contains(logged, "event=reader_scope_denied") {
		t.Fatalf("a scope miss on GetAsset did not log the security event: %s", logged)
	}
	if !strings.Contains(logged, "credential=prod-reader") {
		t.Fatalf("the security event does not name the credential: %s", logged)
	}
	if strings.Contains(logged, tokProd) {
		t.Fatalf("the security event leaked the bearer token itself: %s", logged)
	}
}

// TestAFabricatedIDNeverLogsTheSecurityEventGetAsset is the other half of the
// same property: a genuinely absent id is not a scope miss, and must not log
// as one. If both an absent id and an out-of-scope one logged the same
// event, the log would be useless for the one question it exists to answer
// -- "was this a misconfigured scope, or did the caller just get the id
// wrong" -- because every 404 would look identical there too.
func TestAFabricatedIDNeverLogsTheSecurityEventGetAsset(t *testing.T) {
	f := newAPIHandlerFixture(t)
	buf := captureSecurityLog(t)

	rec := f.serveByID(f.api.GetAsset, store.NewID(), tokProd)
	if rec.Code != 404 {
		t.Fatalf("got %d, want 404", rec.Code)
	}

	if logged := buf.String(); strings.Contains(logged, "event=reader_scope_denied") {
		t.Fatalf("a fabricated id logged a scope-denied event, which must be reserved for a genuine scope miss: %s", logged)
	}
}

// TestAScopeMissLogsTheSecurityEventGetService is GetAsset's test mirrored
// for GetService, which carries the same errors.Is(err, store.ErrOutOfScope)
// branch.
func TestAScopeMissLogsTheSecurityEventGetService(t *testing.T) {
	f := newAPIHandlerFixture(t)
	buf := captureSecurityLog(t)

	// billing-api is in prod only: a dev-only token gets a scope miss.
	rec := f.serveByID(f.api.GetService, f.serviceID, tokDev)
	if rec.Code != 404 {
		t.Fatalf("got %d, want 404", rec.Code)
	}

	logged := buf.String()
	if !strings.Contains(logged, "event=reader_scope_denied") {
		t.Fatalf("a scope miss on GetService did not log the security event: %s", logged)
	}
	if !strings.Contains(logged, "credential=dev-reader") {
		t.Fatalf("the security event does not name the credential: %s", logged)
	}
	if strings.Contains(logged, tokDev) {
		t.Fatalf("the security event leaked the bearer token itself: %s", logged)
	}
}

// TestAFabricatedIDNeverLogsTheSecurityEventGetService mirrors
// TestAFabricatedIDNeverLogsTheSecurityEventGetAsset for GetService.
func TestAFabricatedIDNeverLogsTheSecurityEventGetService(t *testing.T) {
	f := newAPIHandlerFixture(t)
	buf := captureSecurityLog(t)

	rec := f.serveByID(f.api.GetService, store.NewID(), tokDev)
	if rec.Code != 404 {
		t.Fatalf("got %d, want 404", rec.Code)
	}

	if logged := buf.String(); strings.Contains(logged, "event=reader_scope_denied") {
		t.Fatalf("a fabricated id logged a scope-denied event, which must be reserved for a genuine scope miss: %s", logged)
	}
}
