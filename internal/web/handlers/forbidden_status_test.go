// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G1 Task 15: a refused write must answer 403, never 409. Every edit
// handler in this package follows the same shape --
//
//	switch {
//	case isStale(err): ... // 409, "somebody else got there first"
//	case isConflict(err): ...
//	default: a.handleStoreError(w, r, err); return
//	}
//	...renderX(w, r, refusalStatus(err), ...)
//
// -- and the property this test pins is that domain.ErrForbidden can never
// be routed down the isStale/refusalStatus branch, because errors.Is only
// matches the sentinel it names: ErrForbidden is not ErrStale and is not
// ErrConflict, so it always falls to the default branch and
// a.handleStoreError, which maps it to 403. A refusal reaching 409 would
// tell an operator to retry a save they are never allowed to make
// (TestAnOutOfScopeWriteReturns403AndNot409, per the brief).
func TestAnOutOfScopeWriteReturns403AndNot409(t *testing.T) {
	scopeErr := fmt.Errorf("writing change log for asset out-of-scope-asset: %w", domain.ErrForbidden)

	if isStale(scopeErr) {
		t.Fatal("isStale(ErrForbidden) = true, want false -- a scope refusal must never be mistaken for a stale row_version")
	}
	if isConflict(scopeErr) {
		t.Fatal("isConflict(ErrForbidden) = true, want false -- a scope refusal must never be mistaken for a naming conflict")
	}
	if got := refusalStatus(scopeErr); got == http.StatusConflict {
		t.Fatalf("refusalStatus(ErrForbidden) = %d, must never be %d", got, http.StatusConflict)
	}

	a := &App{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/assets/out-of-scope-asset", nil)
	a.handleStoreError(rec, req, scopeErr)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("handleStoreError(ErrForbidden) answered %d, want %d", rec.Code, http.StatusForbidden)
	}
}
