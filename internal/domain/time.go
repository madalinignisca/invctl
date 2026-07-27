package domain

import "time"

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
