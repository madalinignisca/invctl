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
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// API holds the store every handler in this package reads through.
type API struct {
	Store *store.SQLStore
}

// New builds an API over a store.
func New(s *store.SQLStore) *API {
	return &API{Store: s}
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
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBadRequest):
		render.JSONError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "bad request: "))
	case errors.Is(err, domain.ErrNotFound):
		render.JSONError(w, http.StatusNotFound, "not found")
	default:
		slog.Error("api", "error", err)
		render.JSONError(w, http.StatusInternalServerError, "internal error")
	}
}

// checkKnownParams refuses a request naming a query parameter the endpoint
// does not define.
//
// `?limt=5` silently yielding the default with a 200 is the same silent
// fallback shape a malformed cursor would be: a value arrives, cannot be used
// as the caller meant it, and is quietly replaced by something the client
// cannot tell from having asked correctly. The surface here is small, fixed
// and entirely machine-consumed, so the usual HTTP convention of ignoring
// unknown parameters buys nothing.
//
// The error names the offending parameter and never its value, so a
// malformed query string cannot be used to reflect attacker-controlled
// content into a client's log.
func checkKnownParams(q url.Values, allowed ...string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	// Sorted so that a request naming several unknown parameters reports the
	// same one every time, rather than whichever the map happened to yield.
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowedSet[k] {
			return fmt.Errorf("%w: unknown query parameter %q", ErrBadRequest, k)
		}
	}
	return nil
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
