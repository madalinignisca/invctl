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

// ErrBadRequest wraps a client mistake; api.go maps it to 400.
var ErrBadRequest = errors.New("bad request")

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
		if _, err := uuid.Parse(raw); err != nil {
			return PageRequest{}, fmt.Errorf(
				"%w: after must be the id of the last row of the previous page", ErrBadRequest)
		}
		p.After = raw
	}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return PageRequest{}, fmt.Errorf("%w: limit must be a positive whole number", ErrBadRequest)
		}
		if n > MaxLimit {
			n = MaxLimit
		}
		p.Limit = n
	}
	return p, nil
}
