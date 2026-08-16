// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "sort"

// Several tenants in one machine (WP-J5, COST-ATTRIBUTION.md §5.4).
//
// THE CASE OWNERSHIP CANNOT DESCRIBE. At most one project owns an asset, and
// everything J4 divides follows from that: the whole of a machine's capacity
// lands on its owner. For a box carrying four clients' applications -- packed
// together precisely to save on licensing -- that is not an approximation, it
// is a wrong answer given confidently.
//
// DECLARED, NEVER INFERRED. Nothing measures how four tenants inside one
// operating system divide it. This is a judgement somebody made, audited like
// one, and it is worth more than a measurement nobody can take.

// Occupant is one project's declared share of a shared asset.
type Occupant struct {
	AssetID   string  `db:"asset_id"`
	ProjectID string  `db:"project_id"`
	Percent   int     `db:"percent"`
	Note      *string `db:"note"`
	// ProjectCode is resolved for display; an audit entry and a page are both
	// read by people.
	ProjectCode string `db:"project_code"`
	CreatedAt   string `db:"created_at"`
	UpdatedAt   string `db:"updated_at"`
}

// Occupancy is every declared occupant of one asset.
type Occupancy struct {
	AssetID   string
	AssetName string
	Occupants []Occupant
}

// DeclaredPercent is what the occupants add up to.
func (o Occupancy) DeclaredPercent() int {
	total := 0
	for _, x := range o.Occupants {
		total += x.Percent
	}
	return total
}

// Shared reports whether anybody has declared an occupancy at all.
func (o Occupancy) Shared() bool { return len(o.Occupants) > 0 }

// Balanced reports whether the declared shares total exactly 100.
//
// NOT NORMALISED WHEN THEY DO NOT -- §5.4 is explicit that a total which is not
// 100 is a finding rather than a silent rounding. Normalising 90 up to 100
// would inflate every declared share by a ninth and leave nothing on any page
// to notice; leaving the tenth unattributed is visible and fixable.
func (o Occupancy) Balanced() bool { return !o.Shared() || o.DeclaredPercent() == 100 }

// Denominator is what the shares divide against.
//
// The larger of 100 and what was declared, so an over-declared occupancy
// attributes no more than the whole machine. Same guard Division makes for a
// cluster claimed beyond its capacity, and for the same reason: the arithmetic
// has to survive a state the estate can really be in, and the finding is what
// tells somebody to fix it.
func (o Occupancy) Denominator() int {
	if declared := o.DeclaredPercent(); declared > 100 {
		return declared
	}
	return 100
}

// Split divides one quantity between the occupants, largest remainder, so the
// parts never exceed the whole and never silently gain a unit.
//
// The returned map is keyed by project id. What is NOT returned is the
// remainder when the occupancy is under-declared: the caller attributes that to
// nobody, which is the visible form of the gap.
func (o Occupancy) Split(amount int) map[string]int {
	out := map[string]int{}
	if amount <= 0 || !o.Shared() {
		return out
	}
	ids, weights := o.weights()
	// THE SLACK IS AN EXPLICIT BUCKET, and it has to be. apportion guarantees
	// its parts sum to the whole amount, so handing it 90 points of weight
	// against a denominator of 100 would still distribute all 100 units --
	// quietly normalising the occupancy, which is the exact thing §5.4 forbids.
	// Giving the undeclared tenth its own weight and then dropping it is what
	// makes "attributed to nobody" real rather than a comment.
	parts := apportion(amount, weights, o.Denominator())
	for i, id := range ids {
		out[id] += parts[i]
	}
	return out
}

// weights returns the occupants' shares with the undeclared remainder appended
// as a final, unnamed bucket. The ids slice is deliberately shorter than the
// weights slice by one, so a caller that zips them drops the slack.
func (o Occupancy) weights() ([]string, []int) {
	ids := make([]string, 0, len(o.Occupants))
	weights := make([]int, 0, len(o.Occupants)+1)
	for _, x := range o.Occupants {
		ids = append(ids, x.ProjectID)
		weights = append(weights, x.Percent)
	}
	if slack := o.Denominator() - o.DeclaredPercent(); slack > 0 {
		weights = append(weights, slack)
	}
	return ids, weights
}

// SplitMinor is Split for money.
func (o Occupancy) SplitMinor(amount int64) map[string]int64 {
	out := map[string]int64{}
	if amount <= 0 || !o.Shared() {
		return out
	}
	ids, weights := o.weights()
	parts := apportionMinor(amount, weights, o.Denominator())
	for i, id := range ids {
		out[id] += parts[i]
	}
	return out
}

// ValidateOccupants checks a proposed set before it is written.
//
// The constructor rule this codebase follows: the CHECK constraint is the
// second line of defence, and only a Go error can say WHICH percentage is
// wrong. A total that is not 100 is deliberately NOT an error here -- it is a
// finding, and refusing it would stop somebody recording the two occupants they
// know about while they chase the third.
func ValidateOccupants(occupants []Occupant) error {
	ve := &ValidationError{}
	seen := map[string]bool{}
	for _, x := range occupants {
		if x.ProjectID == "" {
			ve.Add("project_id", "an occupant needs a project")
			continue
		}
		if seen[x.ProjectID] {
			ve.Add("project_id", "%s is named twice; one row per project", x.ProjectID)
		}
		seen[x.ProjectID] = true
		if x.Percent <= 0 || x.Percent > 100 {
			ve.Add("percent", "a share is between 1 and 100, and %d is not", x.Percent)
		}
	}
	return ve.OrNil()
}

// SortOccupants orders them largest first, then by project, so a page and an
// audit entry both read the same way twice running.
func SortOccupants(occupants []Occupant) {
	sort.SliceStable(occupants, func(i, j int) bool {
		if occupants[i].Percent != occupants[j].Percent {
			return occupants[i].Percent > occupants[j].Percent
		}
		return occupants[i].ProjectCode < occupants[j].ProjectCode
	})
}
