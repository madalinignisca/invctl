// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"slices"
	"testing"
)

func TestExpandRange(t *testing.T) {
	for _, tc := range []struct {
		name, spec string
		want       []string
		wantErr    bool
	}{
		{"no range is one name", "eth0", []string{"eth0"}, false},
		{"simple range", "Ethernet1/[1-4]", []string{"Ethernet1/1", "Ethernet1/2", "Ethernet1/3", "Ethernet1/4"}, false},
		{"single element range", "xe-[7-7]", []string{"xe-7"}, false},
		{"suffix after the range", "[1-2]/0", []string{"1/0", "2/0"}, false},
		{"reversed is refused", "eth[4-1]", nil, true},
		{"malformed is refused", "eth[1-]", nil, true},
		{"non-numeric is refused", "eth[a-c]", nil, true},
		{"two ranges are refused", "e[1-2]/[1-2]", nil, true},
		{"over the cap is refused", "eth[1-4097]", nil, true},
		// The cap is checked against last-first, not last-first+1, because the
		// addition overflows here: this spec made the count MinInt64, which is
		// not greater than the cap, so the guard passed and make() panicked
		// with "cap out of range". Found in review; the arithmetic was wrong in
		// the plan this was transcribed from, not in the transcription.
		{"a bound that overflows the count is refused", "eth[0-9223372036854775807]", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandRange(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ExpandRange(%q) = %v, want an error", tc.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandRange(%q): %v", tc.spec, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ExpandRange(%q) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}

// TestExpandRangeCapBoundary pins the arithmetic either side of the cap, which
// the table above cannot: asserting the exact 4096 names would be a wall of
// text nobody reads, and the property here is the COUNT, not the names.
//
// The boundary is checked against last-first rather than the count, because
// last-first+1 overflows at the extreme -- see the comment in ExpandRange.
func TestExpandRangeCapBoundary(t *testing.T) {
	got, err := ExpandRange("eth[1-4096]")
	if err != nil {
		t.Fatalf("a range of exactly the cap was refused: %v", err)
	}
	if len(got) != MaxRangeExpansion {
		t.Errorf("got %d names, want %d", len(got), MaxRangeExpansion)
	}
	if _, err := ExpandRange("eth[1-4097]"); err == nil {
		t.Error("one over the cap was accepted")
	}
}
