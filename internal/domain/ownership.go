// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

// Who can act for a team, and the vocabulary the ownership report (WP-G7)
// consumes rather than restating -- see docs/ownership-report-design.md §2 and
// §7. Named and classified here, ONCE, so that the eventual lint engine (not
// this package, see CLAUDE.md's "never before M5") reads the same answer this
// report does rather than a second opinion of it.
//
// THIS IS NOT A BINARY CHECK, AND THAT IS THE WHOLE POINT. team_lifecycle_check
// (migration 00014) admits four values -- planned, active, deprecated, retired
// -- and a caller that tests only `lifecycle == "retired"` silently treats a
// DEPRECATED team, one that still owns forty things and is on its way out, as
// a fine owner. That is the false assurance this file exists to make
// impossible: every branch below goes through OwnerEligibility, never a bare
// string comparison against "retired".

// OwnerEligibility is whether a team, in a given lifecycle state, can be
// expected to answer for what it owns.
type OwnerEligibility string

const (
	// OwnerCanAct: the team is the estate's normal, answerable state. Not a
	// finding on its own.
	OwnerCanAct OwnerEligibility = "can_act"
	// OwnerTransitional: owned, but flag it. A team on its way in (planned) or
	// on its way out (deprecated) still owns things, and a reader deciding
	// whether to act on this page needs to know that distinction from a team in
	// steady state -- see the design doc's "arguably the most interesting
	// finding" note about deprecated.
	OwnerTransitional OwnerEligibility = "transitional"
	// OwnerCannotAct: the team is gone. It will not answer, and a person has to
	// pick this up themselves.
	OwnerCannotAct OwnerEligibility = "cannot_act"
)

// teamLifecycleEligibility is the exhaustive map from every value
// TeamLifecycles admits to what kind of owner it makes. Exhaustive on
// purpose, the same reasoning classification.go gives for column census: a
// default would make it impossible to notice a new lifecycle value arriving
// with nobody having decided which bucket it falls into.
var teamLifecycleEligibility = map[string]OwnerEligibility{
	LifecycleActive:     OwnerCanAct,
	LifecyclePlanned:    OwnerTransitional,
	LifecycleDeprecated: OwnerTransitional,
	LifecycleRetired:    OwnerCannotAct,
}

// ClassifyTeamLifecycle returns what kind of owner a team in this lifecycle
// state makes. ok is false for a lifecycle value nobody has classified --
// which, given team_lifecycle_check, should never happen outside a test that
// is deliberately trying to provoke it.
func ClassifyTeamLifecycle(lifecycle string) (OwnerEligibility, bool) {
	e, ok := teamLifecycleEligibility[lifecycle]
	return e, ok
}

// EligibleTeamLifecycles lists the lifecycle values that make a fully capable
// owner (OwnerCanAct). Queried rather than hard-coded, so the SQL that decides
// "owner has no contact" (finding 3 -- only an ACTIVE team's silence is worth
// flagging) reads its condition from this file instead of restating it.
func EligibleTeamLifecycles() []string {
	return filterEligibility(OwnerCanAct)
}

// NonEligibleTeamLifecycles lists the lifecycle values that make a team unable
// to fully act -- transitional or gone. This is finding 2, "owner cannot
// act", in its entirety: everything that is not OwnerCanAct.
func NonEligibleTeamLifecycles() []string {
	var out []string
	for _, l := range TeamLifecycles {
		if e, ok := ClassifyTeamLifecycle(l); ok && e != OwnerCanAct {
			out = append(out, l)
		}
	}
	return out
}

func filterEligibility(want OwnerEligibility) []string {
	var out []string
	for _, l := range TeamLifecycles {
		if e, ok := ClassifyTeamLifecycle(l); ok && e == want {
			out = append(out, l)
		}
	}
	return out
}

// OwnershipEntityKinds is every entity kind the ownership report covers
// (docs/ownership-report-design.md §3) -- product-wide, not a custom-fields
// feature. Named once so the store, the handler and the template all walk the
// same list rather than each enumerating it independently.
var OwnershipEntityKinds = []string{
	"asset", "service", "project", "identity", "custom_field",
}
