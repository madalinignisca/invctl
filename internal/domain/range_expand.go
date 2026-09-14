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

// MaxRangeExpansion caps what one spec may produce. A 48-port switch needs 48
// and the largest chassis in the wild needs a few hundred; 4096 is comfortably
// above any real device and far below the point where a form submission
// becomes a denial of service against this database.
const MaxRangeExpansion = 4096

// ExpandRange turns "Ethernet1/[1-48]" into the names it stands for. A spec
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
	n := last - first + 1
	if n > MaxRangeExpansion {
		return nil, fmt.Errorf("%q expands to %d names, over the limit of %d",
			spec, n, MaxRangeExpansion)
	}
	out := make([]string, 0, n)
	for i := first; i <= last; i++ {
		out = append(out, prefix+strconv.Itoa(i)+suffix)
	}
	return out, nil
}
