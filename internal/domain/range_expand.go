// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// isZeroPadded reports whether s is a leading-zero numeral like "01" or
// "007" -- "0" itself is not padded, it is just zero.
func isZeroPadded(s string) bool {
	return len(s) > 1 && s[0] == '0'
}

// MaxRangeExpansion caps what one spec may produce. A 48-port switch needs 48
// and the largest chassis in the wild needs a few hundred; 4096 is comfortably
// above any real device and far below the point where a form submission
// becomes a denial of service against this database.
const MaxRangeExpansion = 4096

// ExpandRange turns "Ethernet[1-48]" into the names it stands for. A spec
// with no bracket is one name, so callers need no special case.
func ExpandRange(spec string) ([]string, error) {
	open := strings.Index(spec, "[")
	if open < 0 {
		if strings.Contains(spec, "]") {
			return nil, fmt.Errorf("%q closes a range it never opened", spec)
		}
		return []string{spec}, nil
	}
	end := strings.Index(spec[open:], "]")
	if end < 0 {
		return nil, fmt.Errorf("%q opens a range and never closes it", spec)
	}
	end += open
	prefix, body, suffix := spec[:open], spec[open+1:end], spec[end+1:]
	// One range at a time. Two would multiply, and a form that can produce
	// 48x48 names from one line is the denial of service the cap exists for.
	if strings.ContainsAny(prefix, "[]") || strings.ContainsAny(suffix, "[]") {
		return nil, fmt.Errorf("%q has more than one range; expand one at a time", spec)
	}
	lo, hi, ok := strings.Cut(body, "-")
	if !ok {
		return nil, fmt.Errorf("%q is not a range: want [low-high]", spec)
	}
	// A ZERO-PADDED BOUND IS REFUSED RATHER THAN SILENTLY UNPADDED.
	// strconv.Atoi accepts "01" as 1, and strconv.Itoa never puts the padding
	// back -- so "Port[01-48]" would quietly produce "Port1".."Port48", names
	// the operator never typed, with nothing telling them the width was
	// dropped. Preserving the width is the other option, and it is not
	// cheaper: it needs the original field's length carried through the loop,
	// decided per bound (do "1" and "01" widen to the same output, or not?),
	// and it is exactly the kind of subtlety that looks fine on the first
	// example and wrong on the next one somebody types. Refusing costs one
	// early return.
	if isZeroPadded(lo) {
		return nil, fmt.Errorf("%q: %q has leading zeros, which this does not preserve -- write it as %q",
			spec, lo, strings.TrimLeft(lo, "0"))
	}
	if isZeroPadded(hi) {
		return nil, fmt.Errorf("%q: %q has leading zeros, which this does not preserve -- write it as %q",
			spec, hi, strings.TrimLeft(hi, "0"))
	}
	first, err := strconv.Atoi(lo)
	if err != nil {
		return nil, fmt.Errorf("%q: %q is not a number", spec, lo)
	}
	last, err := strconv.Atoi(hi)
	if err != nil {
		return nil, fmt.Errorf("%q: %q is not a number", spec, hi)
	}
	if last < first {
		return nil, fmt.Errorf("%q counts backwards", spec)
	}
	// THE SUBTRACTION IS SAFE AND THE ADDITION IS NOT, which is why the cap is
	// tested against the difference rather than the count.
	//
	// After the two checks above, first >= 0 (a leading "-" makes the low bound
	// empty and fails Atoi) and last >= first, so last-first cannot overflow.
	// last-first+1 CAN: "eth[0-9223372036854775807]" makes it MinInt64, which
	// is not > MaxRangeExpansion, so the cap passed and make() was handed a
	// negative capacity -- panic: makeslice: cap out of range. A panic in
	// internal/domain reached from a form is exactly what CLAUDE.md forbids,
	// and it defeated the very guard it walked past.
	if last-first >= MaxRangeExpansion {
		return nil, fmt.Errorf("%q expands to more than %d names",
			spec, MaxRangeExpansion)
	}
	n := last - first + 1
	out := make([]string, 0, n)
	for i := first; i <= last; i++ {
		out = append(out, prefix+strconv.Itoa(i)+suffix)
	}
	return out, nil
}
