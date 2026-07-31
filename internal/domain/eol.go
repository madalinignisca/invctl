package domain

import "time"

// End of life: the date after which a thing is expected to stop being fit for
// service. Vendor support ends, the warranty runs out, the licence stops being
// renewable, the box reaches the age this estate replaces at.
//
// ONE DATE, on purpose. Warranty end, support end and planned replacement are
// three different facts and modelling them as three columns produces three
// columns nobody fills in consistently -- and then no report can combine them,
// because "expiring" would have to pick one. A single date plus a note says
// what a person actually knows.
//
// DECLARED. It reads like a fact about the world, which is exactly the trap
// docs/AUDIT.md warns about: somebody read a contract and typed it, nothing
// observes it, nothing derives it, and no monitoring credential may write it.
// It sits beside `lifecycle`, and like lifecycle only a person changes it.
//
// NOTHING HERE ACTS. A date passing does not retire an asset, does not change
// its lifecycle and does not raise an alert to anybody. It changes what a
// report says, which is the whole remit: this system presents state.

// Expiry states, ordered by urgency. Derived at read time from a date and a
// clock; stored nowhere.
const (
	// ExpiryUnknown: no date recorded. Distinct from ok -- "we did not write
	// it down" and "it is fine" are different answers, and collapsing them
	// makes an estate look healthier than it is.
	ExpiryUnknown = "unknown"
	ExpiryOK      = "ok"
	ExpirySoon    = "soon"
	ExpiryExpired = "expired"
)

// ExpirySoonDays is how far ahead "soon" reaches by default: one quarter, which
// is roughly the shortest notice on which hardware can be specified, ordered
// and racked. The expiry report takes its own window; this one drives the pill
// on a detail page, where the reader wants a glance rather than a horizon.
const ExpirySoonDays = 90

// ExpiryState classifies a stored date against a clock.
//
// asOf is passed in rather than read from time.Now() for the same reason every
// timestamp in this codebase is generated in Go and handed to the database: a
// value that depends on an ambient clock cannot be tested, and a report whose
// contents change between two calls in the same request is not a report.
//
// The comparison is on whole days in UTC. An asset whose support ends today is
// NOT yet expired -- support runs to the end of that day -- so the boundary is
// strictly before.
func ExpiryState(eolDate *string, asOf time.Time, window time.Duration) string {
	days, ok := DaysUntil(eolDate, asOf)
	if !ok {
		return ExpiryUnknown
	}
	switch {
	case days < 0:
		return ExpiryExpired
	case float64(days) <= window.Hours()/24:
		return ExpirySoon
	default:
		return ExpiryOK
	}
}

// DaysUntil is whole days from asOf to the stored date, negative once it has
// passed. The second return is false when there is no date or it cannot be
// read -- a stored value that fails to parse is treated as absent rather than
// as an error, because a report is not the place to discover a bad row and
// refusing to render the other four hundred would not help anyone.
func DaysUntil(eolDate *string, asOf time.Time) (int, bool) {
	if eolDate == nil || *eolDate == "" {
		return 0, false
	}
	end, err := ParseDate(*eolDate)
	if err != nil {
		return 0, false
	}
	today := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	return int(end.Sub(today).Hours() / 24), true
}
