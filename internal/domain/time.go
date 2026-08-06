// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"strings"
	"time"
)

// TimeFormat is the canonical on-disk timestamp format: RFC3339 in UTC with
// second precision. Stored as TEXT in both engines because it sorts correctly
// lexicographically, which means ORDER BY and range predicates work without a
// native timestamp type on either side.
const TimeFormat = "2006-01-02T15:04:05Z"

// FormatTime renders t for storage. Always UTC — a mixed-offset column would
// break lexicographic ordering.
func FormatTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(TimeFormat)
}

// ParseTime reads a stored timestamp.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(TimeFormat, s)
}

// NullableTime formats a pointer, returning nil for a nil input, for columns
// that are genuinely optional (last_seen, verified_at).
func NullableTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := FormatTime(*t)
	return &s
}

// DateFormat is the canonical on-disk date: YYYY-MM-DD, ten characters, which
// sorts lexicographically for the same reason TimeFormat does.
//
// A date rather than a timestamp wherever the fact is about a DAY. Support ends
// on a date; storing 00:00:00Z alongside it would invite a reader to believe a
// precision that is not there, and would make "is it past?" depend on a
// timezone nobody chose.
const DateFormat = "2006-01-02"

// FormatDate renders t as a stored date.
func FormatDate(t time.Time) string { return t.UTC().Format(DateFormat) }

// ParseDate reads a stored date. It is strict: time.Parse rejects 2027-02-31
// and 2027-13-01, which the database CHECK -- a length and separator test built
// from the two string functions both engines agree on -- cannot.
func ParseDate(s string) (time.Time, error) { return time.Parse(DateFormat, s) }

// checkDate validates an optional date field in place, returning the
// normalised value. An empty string is the ABSENCE of a date, not a bad one:
// an operator clearing the field on a form must not be told they made a
// mistake.
func checkDate(ve *ValidationError, field string, value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	if _, err := ParseDate(trimmed); err != nil {
		ve.Add(field, "must be a real date in the form %s", DateFormat)
		return value
	}
	return &trimmed
}
