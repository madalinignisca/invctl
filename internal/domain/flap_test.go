// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"testing"
	"time"
)

// The domain half of flap compression (docs/AUDIT.md rule 9) and of the
// retention floor (rule 10). Both are pure arithmetic over values, and both
// decide something that is invisible when it goes wrong: how much history is
// hidden, and how much of it is destroyed.

func TestFlapQualifiesIsStrictlyAboveTheThreshold(t *testing.T) {
	tests := []struct {
		transitions int
		want        bool
		why         string
	}{
		{0, false, "nothing has happened"},
		{4, false, "under the threshold"},
		{5, false, "AT the threshold. Rule 9 says ABOVE five, and five state changes in " +
			"five minutes is churn worth seeing one row at a time"},
		{6, true, "above the threshold: the entity is oscillating rather than moving"},
		{50, true, "well above"},
	}
	for _, tc := range tests {
		if got := FlapQualifies(tc.transitions); got != tc.want {
			t.Errorf("FlapQualifies(%d) = %v, want %v -- %s", tc.transitions, got, tc.want, tc.why)
		}
	}
}

func TestFlapWindowStartIsTheSlidingWindow(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 5, 0, 0, time.UTC)
	if got, want := FlapWindowStart(now), now.Add(-FlapWindow); !got.Equal(want) {
		t.Errorf("FlapWindowStart = %s, want %s", got, want)
	}
}

// TestAssetHealthFlapAccessors covers the shape the store reads back, including
// the case that matters most: four NULLs means no episode, and a reading that
// is not flapping must never claim to be.
func TestAssetHealthFlapAccessors(t *testing.T) {
	quiet := AssetHealth{State: HealthUp, StateSince: "2026-07-28T09:00:00Z"}
	if quiet.Flapping() {
		t.Error("a reading with no episode reports that it is flapping")
	}
	if quiet.FlapOpenedAt() != "" || quiet.FlapTransitions() != 0 || quiet.FlapStates() != nil {
		t.Error("a reading with no episode carries episode detail")
	}
	if quiet.SawDuringEpisode(HealthUp) {
		t.Error("a reading with no episode claims to have seen a value in one")
	}
	if quiet.FlapSettled(time.Now()) {
		t.Error("a reading with no episode reports a settled one")
	}

	since, count, first, seen := "2026-07-28T09:00:00Z", 7, "up", "up,down"
	flapping := AssetHealth{
		State: HealthDown, StateSince: "2026-07-28T09:04:00Z",
		FlapSince: &since, FlapCount: &count, FlapFirstState: &first, FlapSeen: &seen,
	}
	if !flapping.Flapping() {
		t.Fatal("an open episode does not report as flapping")
	}
	if flapping.FlapTransitions() != 7 {
		t.Errorf("FlapTransitions = %d, want 7", flapping.FlapTransitions())
	}
	if !flapping.SawDuringEpisode(HealthDown) || !flapping.SawDuringEpisode(HealthUp) {
		t.Error("a state in the seen set is reported as novel")
	}
	// The escape hatch. A value never seen in the episode is not part of the
	// oscillation being compressed, and that is what stops a stolen token from
	// hiding a state it has not already demonstrated.
	if flapping.SawDuringEpisode(HealthDegraded) || flapping.SawDuringEpisode(HealthUnknown) {
		t.Error("a state never seen in the episode is reported as already seen")
	}

	// Settling is measured from the ONSET of the current state, which is the
	// time of the last transition by construction.
	base := time.Date(2026, 7, 28, 9, 4, 0, 0, time.UTC)
	if flapping.FlapSettled(base.Add(FlapWindow - time.Second)) {
		t.Error("the episode settled a second early")
	}
	if !flapping.FlapSettled(base.Add(FlapWindow)) {
		t.Error("the episode did not settle after a full window with no transition")
	}
}

func TestHealthStateSetEncoding(t *testing.T) {
	tests := []struct {
		name string
		in   []HealthState
		want string
	}{
		{"empty", nil, ""},
		{"one", []HealthState{HealthDown}, "down"},
		// Sorted into the canonical vocabulary order and deduplicated, so the
		// same set always renders the same text and two rows can be compared.
		{"deduplicated and ordered", []HealthState{HealthDown, HealthUp, HealthDown}, "up,down"},
		{"all four", []HealthState{HealthUnknown, HealthDegraded, HealthDown, HealthUp}, "up,degraded,down,unknown"},
	}
	for _, tc := range tests {
		if got := EncodeHealthStates(tc.in); got != tc.want {
			t.Errorf("%s: EncodeHealthStates = %q, want %q", tc.name, got, tc.want)
		}
	}

	round := DecodeHealthStates("up,down")
	if len(round) != 2 || round[0] != HealthUp || round[1] != HealthDown {
		t.Errorf("DecodeHealthStates round trip = %v", round)
	}
	// A value that somehow got past the CHECK is dropped rather than failing
	// the read: this column is an optimisation over the ledger, and a reading
	// that will not display because its flap bookkeeping is malformed is worse
	// than one that briefly under-compresses.
	if got := DecodeHealthStates("up,sideways,down"); len(got) != 2 {
		t.Errorf("DecodeHealthStates dropped %d of 3 entries, want 1 dropped: %v", 3-len(got), got)
	}
}

// TestInScopeRetentionFloorIsAYearOfCalendar pins rule 10's floor.
//
// Calendar arithmetic rather than 365*24h, so a leap year does not quietly
// shave a day off the evidence an audit is going to ask for.
func TestInScopeRetentionFloorIsAYearOfCalendar(t *testing.T) {
	if MinInScopeRetentionDays != 365 {
		t.Errorf("MinInScopeRetentionDays = %d, want 365", MinInScopeRetentionDays)
	}
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	if got, want := InScopeRetentionFloor(now), time.Date(2025, 7, 28, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("InScopeRetentionFloor = %s, want %s", got, want)
	}
	// Across a leap day the floor still lands on the same calendar date.
	leap := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	if got, want := InScopeRetentionFloor(leap), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("across a leap year the floor = %s, want %s", got, want)
	}
}

// TestASlowingEpisodeStopsSuppressing.
//
// An episode that stayed open forever was a mute button. FlapSettled measured
// quiet from StateSince, which moves on every transition INCLUDING the
// compressed ones, so any cadence faster than one change per FlapWindow kept
// compression alive at a rate that would never have qualified for it: toggling
// once every four minutes produced twenty real state changes and zero ledger
// rows. A stolen token could trip the floor once and then hide indefinitely.
//
// An episode now also ends when it stops earning its suppression.
func TestASlowingEpisodeStopsSuppressing(t *testing.T) {
	open := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	since := FormatTime(open)
	first, seen := "up", "up,down"

	build := func(count int, lastChange time.Time) AssetHealth {
		c := count
		return AssetHealth{
			State: HealthDown, StateSince: FormatTime(lastChange),
			FlapSince: &since, FlapCount: &c, FlapFirstState: &first, FlapSeen: &seen,
		}
	}

	tests := []struct {
		name        string
		count       int
		elapsed     time.Duration
		wantSettled bool
		why         string
	}{
		{
			name: "genuine flap keeps compressing", count: 60, elapsed: 10 * time.Minute,
			wantSettled: false,
			why:         "six changes a minute is exactly what compression exists for",
		},
		{
			name: "the abuse case: one toggle every four minutes", count: 20, elapsed: 80 * time.Minute,
			wantSettled: true,
			why: "a quarter of the qualifying rate is not flapping, it is changing, " +
				"and every change is worth its own row",
		},
		{
			name: "inside the first window it keeps the benefit of the doubt", count: 6, elapsed: 3 * time.Minute,
			wantSettled: false,
			why:         "it qualified to get here and has not had time to prove otherwise",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := open.Add(tc.elapsed)
			// StateSince is kept recent so the quiet-window condition cannot be
			// what decides this -- the rate rule has to carry it alone.
			h := build(tc.count, now.Add(-30*time.Second))
			if got := h.FlapSettled(now); got != tc.wantSettled {
				t.Errorf("FlapSettled = %v, want %v: %s", got, tc.wantSettled, tc.why)
			}
		})
	}
}
