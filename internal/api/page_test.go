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
	"net/url"
	"testing"
)

func TestAMalformedCursorIsRefusedNotIgnored(t *testing.T) {
	// ParseChangeCursor treats a bad cursor as "first page", which is right for
	// a human clicking a link and catastrophic for a client paginating: it
	// would re-ingest page one forever, with a 200 every time, and never reach
	// the rest of the estate. TestNoParseErrorIsDiscarded exists to refuse
	// exactly this mechanism.
	for _, bad := range []string{"not-a-uuid", "../etc/passwd", "01924e5a zzz", "%%%"} {
		_, err := ParsePageRequest(url.Values{"after": {bad}})
		if err == nil {
			t.Errorf("cursor %q was accepted; a cursor that cannot be used must be refused", bad)
		}
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("cursor %q gave %v, want ErrBadRequest", bad, err)
		}
	}
}

func TestAnAbsentCursorIsTheFirstPage(t *testing.T) {
	p, err := ParsePageRequest(url.Values{})
	if err != nil {
		t.Fatalf("an absent cursor is not an error: %v", err)
	}
	if p.After != "" {
		t.Fatalf("got after %q, want empty", p.After)
	}
	if p.Limit != DefaultLimit {
		t.Fatalf("got limit %d, want %d", p.Limit, DefaultLimit)
	}
}

func TestAValidCursorRoundTrips(t *testing.T) {
	id := "01924e5a-1c2b-7f3a-9d4e-5f6a7b8c9d0e"
	p, err := ParsePageRequest(url.Values{"after": {id}})
	if err != nil {
		t.Fatalf("a well-formed cursor must be accepted: %v", err)
	}
	if p.After != id {
		t.Fatalf("got after %q, want %q", p.After, id)
	}
}

func TestAnOversizedLimitIsClamped(t *testing.T) {
	// A clamp, unlike a swallowed cursor, is documented, in-band and visible in
	// the length of the response the client just received.
	p, err := ParsePageRequest(url.Values{"limit": {"100000"}})
	if err != nil {
		t.Fatalf("an oversized limit is clamped, not refused: %v", err)
	}
	if p.Limit != MaxLimit {
		t.Fatalf("got limit %d, want %d", p.Limit, MaxLimit)
	}
}

func TestAnUnparseableLimitIsRefused(t *testing.T) {
	for _, bad := range []string{"lots", "-1", "0", "1.5"} {
		if _, err := ParsePageRequest(url.Values{"limit": {bad}}); !errors.Is(err, ErrBadRequest) {
			t.Errorf("limit %q gave %v, want ErrBadRequest", bad, err)
		}
	}
}

// TestACursorIsCanonicalisedNotJustValidated pins the final review's C2. The
// cursor was VALIDATED with uuid.Parse and then stored raw, and uuid.Parse
// accepts four spellings of the same id in any case. The cursor is compared
// as TEXT against ids stored hyphenated and lower-case, so an upper-case one
// sorts before every id in the estate (`a.id > ?` repeats the whole page) and
// a braced one sorts after them all (the page is skipped) -- both with a 200.
func TestACursorIsCanonicalisedNotJustValidated(t *testing.T) {
	const canonical = "01924e5a-1c2b-7f3a-9d4e-5f6a7b8c9d0e"
	cases := []struct {
		name string
		raw  string
	}{
		{"already canonical", canonical},
		{"upper case", "01924E5A-1C2B-7F3A-9D4E-5F6A7B8C9D0E"},
		{"mixed case", "01924e5a-1C2B-7f3a-9D4E-5f6a7b8c9d0e"},
		{"braced", "{01924e5a-1c2b-7f3a-9d4e-5f6a7b8c9d0e}"},
		{"urn prefixed", "urn:uuid:01924e5a-1c2b-7f3a-9d4e-5f6a7b8c9d0e"},
		{"unhyphenated", "01924e5a1c2b7f3a9d4e5f6a7b8c9d0e"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := ParsePageRequest(url.Values{"after": {c.raw}})
			if err != nil {
				t.Fatalf("uuid.Parse accepts %q, so the API must too: %v", c.raw, err)
			}
			if p.After != canonical {
				t.Fatalf("got after %q, want %q -- a validated cursor must be stored in the one "+
					"spelling the estate's ids use, or it silently selects the wrong rows", p.After, canonical)
			}
		})
	}
}
