// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package api holds the handlers of the read-only, token-scoped inventory API
// (WP-A2). Routes are mounted elsewhere (Task 9); this package only joins the
// pieces built by earlier tasks: middleware.ReaderFrom for the authenticated
// principal, ParsePageRequest for ?after=/?limit=, the API* store methods for
// the scoped, keyset-paginated reads, and the DTOs in dto.go for the published
// shape.
//
// EVERY HANDLER HERE IS A GET. Nothing in this package writes: docs/api-design
// §1 says so and the roadmap's "no write routes" line is a guard test, not a
// sentence to trust.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"unicode"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// API holds the store every handler in this package reads through.
type API struct {
	Store *store.SQLStore
	// PageSize is how many rows fetchAllAssets (ansible.go) asks for per
	// round trip while assembling the Ansible view. It lives on the struct,
	// not a package-level var: a package var mutated by one test and read by
	// fetchAllAssets in another goroutine is a data race waiting for the day
	// this package's tests start using t.Parallel(), and a field on a
	// per-test *API (built fresh by New() in every fixture) removes that
	// hazard structurally instead of by convention.
	PageSize int
}

// New builds an API over a store. PageSize defaults to MaxLimit, which
// minimises round trips against a large estate; a test that wants to force
// several pages through a small fixture sets the field on its own instance.
func New(s *store.SQLStore) *API {
	return &API{Store: s, PageSize: MaxLimit}
}

// noReaderInContext is what every handler reports when middleware.ReaderFrom
// finds nothing.
//
// This is unreachable behind RequireReader, and deliberately a 500 rather than
// a default scope: a handler that invented an empty scope to keep going would
// publish the estate to an unauthenticated caller the day somebody mounts this
// route without the guard in front of it.
var errNoReaderInContext = errors.New("no reader in context")

// writeError maps an error to a status without letting a driver message reach
// the client. Sentinels are mapped; everything else is a 500 with a generic
// body and the real error in the server log.
//
// domain.ErrNotFound covers both a genuinely absent id and store.ErrOutOfScope
// (which wraps it): the two must render byte-identically, and mapping both
// through the same errors.Is branch is what guarantees that -- there is no
// separate case above this one that could diverge.
//
// A 400's body comes from errors.As, not from trimming err.Error(): every
// list handler wraps the error it gets back (fmt.Errorf("listing assets:
// %w", err)), so a positional trim over the wrapped text would either fail to
// strip the sentinel or publish the wrapping text itself. badRequestError
// carries the client-facing message separately from Error()'s server-log
// form for exactly this reason -- see page.go.
func writeError(w http.ResponseWriter, err error) {
	var bad *badRequestError
	switch {
	case errors.As(err, &bad):
		render.JSONError(w, http.StatusBadRequest, bad.ClientMessage())
	case errors.Is(err, ErrBadRequest):
		// Reachable only if something in this package ever constructs
		// ErrBadRequest directly instead of through badRequest() -- the status
		// is still right, but with no client-safe message to send, "bad
		// request" is the honest generic body rather than risking whatever
		// err.Error() happens to contain.
		render.JSONError(w, http.StatusBadRequest, "bad request")
	case errors.Is(err, domain.ErrNotFound):
		render.JSONError(w, http.StatusNotFound, "not found")
	default:
		slog.Error("api", "error", err)
		render.JSONError(w, http.StatusInternalServerError, "internal error")
	}
}

// parseQuery is the ONLY place in this package a request's query string
// becomes url.Values, and the reason it exists is r.URL.Query().
//
// r.URL.Query() calls url.ParseQuery and DISCARDS ITS ERROR. That is
// documented behaviour, not a bug in net/http, but it is precisely the shape
// docs/api-design.md §6 refuses: `?after=%zz` is dropped on the floor, so
// ParsePageRequest sees no cursor at all, and the caller gets PAGE ONE with a
// 200 -- the "paginating client restarts forever" failure the whole cursor
// rule exists to forbid. `?%zz=1` is dropped the same way and never reaches
// checkKnownParams, so an unknown parameter goes unrefused too. The AST guard
// in silent_fallback_test.go cannot see any of it, because the discarded
// error is inside net/http rather than in this package.
//
// The raw query is never echoed back. It is as caller-controlled as a
// parameter value, and safeParamName's argument applies to the whole string.
func parseQuery(r *http.Request, allowed ...string) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, badRequest("the query string is malformed")
	}
	if err := checkKnownParams(q, allowed...); err != nil {
		return nil, err
	}
	return q, nil
}

// readerAndQuery is the preamble every handler in this package shares: the
// authenticated principal, then a parsed and validated query string, in that
// order because a request with no reader must fail closed before anything
// else is considered.
//
// One helper rather than the same eight lines in seven handlers, which had
// already drifted: ListAssets cached r.URL.Query() with a comment about not
// re-parsing it while ListServices and ListAddresses called it twice each.
// Sharing it is also what gives parseQuery exactly one call site to fix.
func readerAndQuery(r *http.Request, allowed ...string) (*auth.Reader, url.Values, error) {
	reader, ok := middleware.ReaderFrom(r.Context())
	if !ok {
		return nil, nil, errNoReaderInContext
	}
	q, err := parseQuery(r, allowed...)
	if err != nil {
		return nil, nil, err
	}
	return reader, q, nil
}

// beginList is readerAndQuery plus the ?after=/?limit= every collection
// takes. The caller still names its own full parameter set, "after" and
// "limit" included, because that list is the endpoint's published surface
// (docs/api-design.md §4) and it is checked before this ever parses a page.
func beginList(r *http.Request, allowed ...string) (*auth.Reader, url.Values, PageRequest, error) {
	reader, q, err := readerAndQuery(r, allowed...)
	if err != nil {
		return nil, nil, PageRequest{}, err
	}
	page, err := ParsePageRequest(q)
	if err != nil {
		return nil, nil, PageRequest{}, err
	}
	return reader, q, page, nil
}

// checkKnownParams refuses a request naming a query parameter the endpoint
// does not define, or naming a defined one more than once.
//
// `?limt=5` silently yielding the default with a 200 is the same silent
// fallback shape a malformed cursor would be: a value arrives, cannot be used
// as the caller meant it, and is quietly replaced by something the client
// cannot tell from having asked correctly. The surface here is small, fixed
// and entirely machine-consumed, so the usual HTTP convention of ignoring
// unknown parameters buys nothing.
//
// A repeated parameter is refused for the same reason: url.Values.Get takes
// the first of several values for a key, so `?limit=5&limit=9` would
// silently use 5 and drop 9 on the floor -- a value the caller supplied that
// this handler never even looks at, which is the same shape as an unknown
// name, just spelled differently.
//
// The error names the offending parameter and never its value. The name
// itself is caller-supplied too, so it is bounded and checked for printable
// ASCII before being echoed -- json.Marshal already escapes it correctly and
// this is not an injection risk, but an oversized or unprintable name has no
// business appearing in a log line or a client's error message verbatim.
func checkKnownParams(q url.Values, allowed ...string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	// Sorted so that a request naming several unknown or repeated parameters
	// reports the same one every time, rather than whichever the map happened
	// to yield.
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowedSet[k] {
			return badRequest("unknown query parameter %q", safeParamName(k))
		}
		if len(q[k]) > 1 {
			return badRequest("query parameter %q is repeated", safeParamName(k))
		}
	}
	return nil
}

// maxEchoedParamName bounds how much of a query parameter's name is ever
// echoed back to the caller.
const maxEchoedParamName = 64

// safeParamName returns k unchanged if it is short, printable ASCII, and a
// generic placeholder otherwise -- so an oversized or unprintable parameter
// name never reaches a log line or an error body verbatim.
func safeParamName(k string) string {
	if len(k) > maxEchoedParamName {
		return "(a long parameter name)"
	}
	for _, r := range k {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return "(an unprintable parameter name)"
		}
	}
	return k
}

// nextCursor is nil on a short page -- fewer rows than were asked for, which
// means there is nothing more to fetch -- and the last row's id otherwise.
//
// A full page's last id is handed back even though the caller cannot yet know
// whether another row exists past it: the alternative, fetching one extra row
// to find out, is the limit+1 trick this store layer does not use. The cost is
// one wasted round trip on the very last page, which asks once more and gets
// an empty collection back.
func nextCursor[T any](rows []T, limit int, id func(T) string) *string {
	if limit <= 0 || len(rows) < limit {
		return nil
	}
	last := id(rows[len(rows)-1])
	return &last
}
