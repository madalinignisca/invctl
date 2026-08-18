// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"a client mistake is 400", fmt.Errorf("%w: limit", ErrBadRequest), 400},
		{"an absent entity is 404", domain.ErrNotFound, 404},
		{"an out-of-scope entity renders the same 404 as an absent one", store.ErrOutOfScope, 404},
		{"anything else is 500", errors.New("boom"), 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, c.err)
			if rec.Code != c.want {
				t.Fatalf("got %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestAnInternalErrorIsNotEchoedToTheClient(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New(`pq: relation "asset" does not exist`))
	if strings.Contains(rec.Body.String(), "relation") {
		t.Fatal("a driver error must not reach the client; it names the schema")
	}
}

// TestAWrappedBadRequestKeepsItsClientSafeMessage pins finding (c): every
// list handler wraps whatever error it gets back (fmt.Errorf("listing
// assets: %w", err)), so the 400 body must come from the badRequestError's
// own message, not from trimming a known prefix off the wrapped text -- a
// positional trim over "listing assets: bad request: kind is not a
// recognised asset kind" would either fail to strip anything or leak the
// wrapping text itself.
func TestAWrappedBadRequestKeepsItsClientSafeMessage(t *testing.T) {
	err := fmt.Errorf("listing assets: %w", badRequest("kind is not a recognised asset kind"))
	rec := httptest.NewRecorder()
	writeError(rec, err)
	if rec.Code != 400 {
		t.Fatalf("got %d, want 400", rec.Code)
	}
	const want = `{"error":"kind is not a recognised asset kind"}`
	if rec.Body.String() != want {
		t.Fatalf("got %s, want %s", rec.Body.String(), want)
	}
}

// TestAnOutOfScopeErrorAndAFabricatedIDProduceTheSameResponse pins the
// property store.ErrOutOfScope exists to protect at the one place it can be
// undone silently: writeError. Both are domain.ErrNotFound by wrapping, and
// this asserts they render byte-for-byte identical HTTP responses -- status,
// headers and body -- so that a client cannot use the response to tell an
// id that exists but is out of scope apart from one that was never real.
// docs/api-design.md §3: a 403 (or any other divergence) would be an
// existence oracle over the estate.
func TestAnOutOfScopeErrorAndAFabricatedIDProduceTheSameResponse(t *testing.T) {
	recOutOfScope := httptest.NewRecorder()
	writeError(recOutOfScope, fmt.Errorf("getting asset x for the api: %w", store.ErrOutOfScope))

	recAbsent := httptest.NewRecorder()
	writeError(recAbsent, fmt.Errorf("getting asset y for the api: %w", domain.ErrNotFound))

	if recOutOfScope.Code != recAbsent.Code {
		t.Fatalf("status differs: out-of-scope %d, absent %d", recOutOfScope.Code, recAbsent.Code)
	}
	if recOutOfScope.Body.String() != recAbsent.Body.String() {
		t.Fatalf("body differs: out-of-scope %q, absent %q", recOutOfScope.Body.String(), recAbsent.Body.String())
	}
	// The FULL header map, not one named header picked because it seemed
	// relevant: a length or "contains" check is satisfied by many wrong
	// answers, and item 1 is the property this task most had to protect.
	if !reflect.DeepEqual(recOutOfScope.Header(), recAbsent.Header()) {
		t.Fatalf("headers differ: out-of-scope %v, absent %v", recOutOfScope.Header(), recAbsent.Header())
	}
}

// TestPageDataMarshalsAsEmptyArrayNotNull pins Page[T].Data as always built
// with make([]T, 0, ...), never left as a nil slice, so an empty collection
// marshals as `[]` and a consumer doing `for host in group.hosts` never has to
// special-case null.
func TestPageDataMarshalsAsEmptyArrayNotNull(t *testing.T) {
	p := Page[Asset]{Data: make([]Asset, 0), Next: nil}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshalling an empty page: %v", err)
	}
	const want = `{"data":[],"next":null}`
	if string(body) != want {
		t.Fatalf("got %s, want %s", body, want)
	}
}

// TestAnUnknownQueryParameterIsRefused pins item 5: a typo in a query
// parameter name must not silently fall back to a default with a 200 -- that
// is the same silent-fallback shape a discarded parse error would be.
func TestAnUnknownQueryParameterIsRefused(t *testing.T) {
	err := checkKnownParams(url.Values{"limt": {"5"}}, "after", "limit")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v, want ErrBadRequest", err)
	}
	if !strings.Contains(err.Error(), `"limt"`) {
		t.Fatalf("error %q does not name the offending parameter", err.Error())
	}
}

// TestAnUnknownQueryParameterValueIsNeverEchoed pins the second half of item
// 5: the parameter NAME is self-diagnosing, but its value never appears in the
// error, so a malformed query string cannot be used to reflect
// attacker-controlled content into a client-visible response.
func TestAnUnknownQueryParameterValueIsNeverEchoed(t *testing.T) {
	err := checkKnownParams(url.Values{"env": {"<script>secret-value</script>"}}, "after", "limit")
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("the offending value leaked into the error: %q", err.Error())
	}
}

// TestARepeatedQueryParameterIsRefused pins finding (d): url.Values.Get takes
// the first of several values for a key, so ?limit=5&limit=9 would otherwise
// silently use 5 and drop 9 -- a value the caller supplied that the handler
// never even looks at, the same shape as an unknown parameter name.
func TestARepeatedQueryParameterIsRefused(t *testing.T) {
	err := checkKnownParams(url.Values{"limit": {"5", "9"}}, "after", "limit")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("got %v, want ErrBadRequest", err)
	}
	if !strings.Contains(err.Error(), `"limit"`) {
		t.Fatalf("error %q does not name the repeated parameter", err.Error())
	}
}

// TestAnOversizedParameterNameIsNotEchoed pins finding (e): the parameter
// NAME is as caller-controlled as its value, so an oversized one is replaced
// by a generic placeholder rather than echoed verbatim.
func TestAnOversizedParameterNameIsNotEchoed(t *testing.T) {
	long := strings.Repeat("x", maxEchoedParamName+1)
	err := checkKnownParams(url.Values{long: {"1"}}, "after", "limit")
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	if strings.Contains(err.Error(), long) {
		t.Fatalf("an oversized parameter name was echoed verbatim: %q", err.Error())
	}
}

// TestAnUnprintableParameterNameIsNotEchoed is the same guard for a name
// carrying a control character rather than merely being long.
func TestAnUnprintableParameterNameIsNotEchoed(t *testing.T) {
	bad := "a\x00b"
	err := checkKnownParams(url.Values{bad: {"1"}}, "after", "limit")
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	if strings.Contains(err.Error(), bad) {
		t.Fatalf("an unprintable parameter name was echoed verbatim: %q", err.Error())
	}
}

func TestAKnownQueryParameterIsAccepted(t *testing.T) {
	if err := checkKnownParams(url.Values{"after": {"x"}, "limit": {"5"}}, "after", "limit"); err != nil {
		t.Fatalf("a legitimate parameter set was refused: %v", err)
	}
}

func TestNoQueryParametersIsAlwaysAccepted(t *testing.T) {
	if err := checkKnownParams(url.Values{}); err != nil {
		t.Fatalf("an empty query string was refused: %v", err)
	}
}

// TestNextCursorIsNilOnAShortPage pins nextCursor's two cases directly,
// without a store round trip.
func TestNextCursorIsNilOnAShortPage(t *testing.T) {
	rows := []string{"a", "b"}
	got := nextCursor(rows, 5, func(s string) string { return s })
	if got != nil {
		t.Fatalf("got %v, want nil for a page shorter than the limit", got)
	}
}

func TestNextCursorIsTheLastRowIDOnAFullPage(t *testing.T) {
	rows := []string{"a", "b", "c"}
	got := nextCursor(rows, 3, func(s string) string { return s })
	if got == nil || *got != "c" {
		t.Fatalf("got %v, want a cursor naming the last row", got)
	}
}

func TestNextCursorIsNilOnAnEmptyPage(t *testing.T) {
	var rows []string
	got := nextCursor(rows, 5, func(s string) string { return s })
	if got != nil {
		t.Fatalf("got %v, want nil for an empty page", got)
	}
}

// TestStoreAndAPILimitsAgree is the cross-package half of the constant
// assertion internal/store's api.go doc comment calls for: store cannot
// import internal/api without a cycle, so the two limits are declared twice,
// and nothing but this test keeps them from drifting apart.
func TestStoreAndAPILimitsAgree(t *testing.T) {
	if store.APIDefaultLimit() != DefaultLimit {
		t.Fatalf("store's default page size (%d) and api.DefaultLimit (%d) disagree",
			store.APIDefaultLimit(), DefaultLimit)
	}
	if store.APIMaxLimit() != MaxLimit {
		t.Fatalf("store's max page size (%d) and api.MaxLimit (%d) disagree",
			store.APIMaxLimit(), MaxLimit)
	}
}
