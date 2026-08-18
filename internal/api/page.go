// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/google/uuid"
)

// DefaultLimit is the page size used when the caller does not specify one.
const DefaultLimit = 100

// MaxLimit is the largest page size the API will serve. A request for more
// is clamped, not refused: a clamp is documented, in-band and visible in the
// length of the response the client just received.
const MaxLimit = 500

// ErrBadRequest wraps a client mistake; api.go maps it to 400. Callers that
// need the 400 body to say something specific should use badRequest below
// rather than fmt.Errorf("%w: ...", ErrBadRequest) directly: errors.Is(err,
// ErrBadRequest) still works either way, but only badRequest keeps the
// client-facing message independent of how the error gets wrapped on its way
// back up.
var ErrBadRequest = errors.New("bad request")

// badRequestError carries a message meant for the client, separately from
// Error()'s value, which is what a server-side log line uses. Without this
// separation, writeError had to reconstruct the client-safe message by
// trimming a known prefix off err.Error() -- correct only as long as nothing
// wraps the error further, which every list handler does
// (fmt.Errorf("listing assets: %w", err)). A positional trim over that
// wrapped text would either fail to strip the sentinel or, worse, publish the
// wrapping text ("listing assets: bad request: ...") to the client.
type badRequestError struct {
	msg string
}

// badRequest builds a client-facing 400. The message is exactly what
// render.JSONError sends back, however the error is subsequently wrapped.
func badRequest(format string, args ...any) error {
	return &badRequestError{msg: fmt.Sprintf(format, args...)}
}

// Error is the server-log form: this is not what the client sees.
func (e *badRequestError) Error() string { return "bad request: " + e.msg }

// Unwrap lets errors.Is(err, ErrBadRequest) keep working regardless of how
// the error was constructed.
func (e *badRequestError) Unwrap() error { return ErrBadRequest }

// ClientMessage is what writeError sends back, verbatim.
func (e *badRequestError) ClientMessage() string { return e.msg }

// Page is the collection envelope returned by every list endpoint. Next is
// the cursor to pass as ?after= to fetch the following page, or nil when
// there isn't one.
type Page[T any] struct {
	Data []T     `json:"data"`
	Next *string `json:"next"`
}

// PageRequest is a parsed, validated ?after= and ?limit=.
type PageRequest struct {
	After string // "" is the first page
	Limit int
}

// ParsePageRequest parses and validates the pagination query parameters of a
// list request. It refuses what it cannot use rather than falling back to
// the first page: store.ParseChangeCursor's fall-back-to-first-page
// behaviour is correct for a human clicking a link in a browser, but
// catastrophic for a client paginating a feed — a corrupted cursor would
// silently restart at page one forever, re-ingesting the same rows with a
// 200 every time and never reaching the rest of the estate.
//
// An absent "after" or "limit" is not an error; it is the first page and the
// default limit. A malformed "after" or an unparseable or non-positive
// "limit" is ErrBadRequest. An over-large "limit" is clamped to MaxLimit,
// not refused.
//
// The error messages deliberately do not echo the offending value back, so a
// malformed cursor cannot be used to reflect attacker-controlled content
// into a client's log.
func ParsePageRequest(q url.Values) (PageRequest, error) {
	p := PageRequest{Limit: DefaultLimit}

	if raw := q.Get("after"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return PageRequest{}, badRequest("after must be the id of the last row of the previous page")
		}
		// THE PARSED FORM, NOT THE RAW ONE. uuid.Parse accepts four
		// spellings of the same id -- hyphenated, unhyphenated, braced and
		// `urn:uuid:`-prefixed -- in any case. The cursor is compared as TEXT
		// against ids stored hyphenated and lower-case, so keeping whatever
		// arrived meant a validated, accepted cursor that silently selected
		// the wrong rows: `01924E5A-...` sorts before every lower-case id, so
		// `a.id > ?` repeats the whole page, and a braced form sorts after
		// them all, so it skips to the end. Either way a 200. String()
		// renders the one spelling the estate uses.
		p.After = parsed.String()
	}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return PageRequest{}, badRequest("limit must be a positive whole number")
		}
		if n > MaxLimit {
			n = MaxLimit
		}
		p.Limit = n
	}
	return p, nil
}
