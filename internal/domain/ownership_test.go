// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

// TestEveryTeamLifecycleIsClassified is the census check, mirroring
// TestClassificationIsSelfConsistent's reasoning: a lifecycle value admitted
// by team_lifecycle_check but absent from teamLifecycleEligibility has no
// class at all, which is a gap, not a default.
func TestEveryTeamLifecycleIsClassified(t *testing.T) {
	if len(teamLifecycleEligibility) != len(TeamLifecycles) {
		t.Fatalf("teamLifecycleEligibility has %d entries, TeamLifecycles has %d -- they must name exactly the same set",
			len(teamLifecycleEligibility), len(TeamLifecycles))
	}
	for _, l := range TeamLifecycles {
		if _, ok := ClassifyTeamLifecycle(l); !ok {
			t.Errorf("team lifecycle %q is not classified", l)
		}
	}
	// An unknown lifecycle value is unclassified, not defaulted to something
	// safe -- the same "no default" property classification.go relies on for
	// its own census.
	if _, ok := ClassifyTeamLifecycle("not-a-real-lifecycle"); ok {
		t.Error("an unlisted lifecycle value was classified")
	}
}

// TestDeprecatedIsNotRetired is the one a binary active/retired test misses
// silently, and the reason this file exists at all: a team on its way out
// still owns things, and that is the most interesting finding in the report,
// not one folded invisibly into "fine".
func TestDeprecatedIsNotRetired(t *testing.T) {
	got, ok := ClassifyTeamLifecycle(LifecycleDeprecated)
	if !ok {
		t.Fatal("deprecated is not classified at all")
	}
	if got == OwnerCannotAct {
		t.Error("deprecated classified the same as retired -- a binary check would miss this")
	}
	if got != OwnerTransitional {
		t.Errorf("deprecated classified as %q, want %q", got, OwnerTransitional)
	}
}

func TestClassifyTeamLifecycleTable(t *testing.T) {
	cases := []struct {
		lifecycle string
		want      OwnerEligibility
	}{
		{LifecycleActive, OwnerCanAct},
		{LifecyclePlanned, OwnerTransitional},
		{LifecycleDeprecated, OwnerTransitional},
		{LifecycleRetired, OwnerCannotAct},
	}
	for _, c := range cases {
		t.Run(c.lifecycle, func(t *testing.T) {
			got, ok := ClassifyTeamLifecycle(c.lifecycle)
			if !ok {
				t.Fatalf("lifecycle %q not classified", c.lifecycle)
			}
			if got != c.want {
				t.Errorf("ClassifyTeamLifecycle(%q) = %q, want %q", c.lifecycle, got, c.want)
			}
		})
	}
}

func TestEligibleAndNonEligibleTeamLifecyclesPartitionTheSet(t *testing.T) {
	eligible := EligibleTeamLifecycles()
	nonEligible := NonEligibleTeamLifecycles()
	if len(eligible)+len(nonEligible) != len(TeamLifecycles) {
		t.Fatalf("eligible (%d) + non-eligible (%d) != total (%d)",
			len(eligible), len(nonEligible), len(TeamLifecycles))
	}
	seen := map[string]bool{}
	for _, l := range eligible {
		seen[l] = true
	}
	for _, l := range nonEligible {
		if seen[l] {
			t.Errorf("%q appears in both eligible and non-eligible", l)
		}
		seen[l] = true
	}
	for _, l := range TeamLifecycles {
		if !seen[l] {
			t.Errorf("%q appears in neither eligible nor non-eligible", l)
		}
	}
	// active is the one value everything else in this file assumes is the
	// only eligible one -- pin it so a change to teamLifecycleEligibility
	// that quietly widens eligibility is caught here as well as in the table
	// test above.
	if len(eligible) != 1 || eligible[0] != LifecycleActive {
		t.Errorf("EligibleTeamLifecycles() = %v, want exactly [%q]", eligible, LifecycleActive)
	}
}
