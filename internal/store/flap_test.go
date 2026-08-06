// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Tests for flap compression (docs/AUDIT.md rule 9). Both engines, no sleeping:
// the clock is injected and every instant below is explicit, because the whole
// rule is about timing and a test that guesses at timing tests nothing.
//
// The property under test is "compressed, never suppressed". A reader must
// always be able to see that compression happened and how much it hid, and a
// value that is not part of the oscillation must never be swallowed by it --
// that last one is a security property, not a display nicety.

// flapBase is the instant every sequence below is measured from. It matches the
// fixture clock so an offset in a table reads as an absolute time.
var flapBase = time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)

func (c *testClock) set(at time.Time) {
	c.mu.Lock()
	c.at = at
	c.mu.Unlock()
}

// observe reports one state at an offset from flapBase, with the server clock
// moved to the same instant. The two are separate fields and the code never
// conflates them; tracking them here just makes the expectations readable.
func (f *observedFixture) observe(t *testing.T, offset time.Duration, state string) *ObservationResult {
	t.Helper()
	at := flapBase.Add(offset)
	f.clock.set(at)
	return f.record(t, f.report(state, at))
}

func (f *observedFixture) kindCount(t *testing.T, kind string) int {
	t.Helper()
	return f.count(t,
		`SELECT COUNT(*) FROM observed_transition WHERE entity_id = ? AND kind = ?`,
		f.assetID, kind)
}

// flapRow reads the single flap_close row, which is where rule 9 requires the
// suppression to be accounted for.
func (f *observedFixture) flapRow(t *testing.T, kind string) domain.ObservedTransition {
	t.Helper()
	var rows []domain.ObservedTransition
	err := f.store.read(f.ctx, &rows,
		`SELECT * FROM observed_transition WHERE entity_id = ? AND kind = ? ORDER BY at, id`,
		f.assetID, kind)
	if err != nil {
		t.Fatalf("reading %s rows: %v", kind, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d %s rows, want exactly 1", len(rows), kind)
	}
	return rows[0]
}

// oscillate walks the entity through n alternating up/down reports at 30s
// intervals starting at flapBase, which is one transition per report. It
// returns the offset of the last one.
func (f *observedFixture) oscillate(t *testing.T, n int) time.Duration {
	t.Helper()
	var last time.Duration
	for i := 0; i < n; i++ {
		last = time.Duration(i) * 30 * time.Second
		state := "up"
		if i%2 == 1 {
			state = "down"
		}
		f.observe(t, last, state)
	}
	return last
}

// ---------------------------------------------------------------------------
// The threshold boundary
// ---------------------------------------------------------------------------

// TestFlapThresholdBoundary pins the threshold at exactly the value rule 9
// states: "above domain.FlapThreshold (5) transitions in domain.FlapWindow".
//
// Above, not at. Five state changes in five minutes is churn worth seeing in
// full; the sixth is what says the entity is oscillating rather than moving.
// The boundary is tested on both sides and on the value itself, because an
// off-by-one here either suppresses a real sequence of five changes or never
// compresses anything -- and both failures are silent.
func TestFlapThresholdBoundary(t *testing.T) {
	cases := []struct {
		name        string
		transitions int
		// Every state change below the threshold is its own row.
		wantTransitionRows int
		wantFlapOpen       int
		wantFlapping       bool
	}{
		{
			name: "four transitions is churn, not a flap",
			// Well under. Nothing is compressed and nothing is bracketed.
			transitions: 4, wantTransitionRows: 4, wantFlapOpen: 0, wantFlapping: false,
		},
		{
			name: "five transitions is exactly the threshold and is still not above it",
			// The boundary itself. Rule 9 says ABOVE five, so five stays visible
			// one row at a time.
			transitions: 5, wantTransitionRows: 5, wantFlapOpen: 0, wantFlapping: false,
		},
		{
			name: "six transitions is above the threshold and opens an episode",
			// The sixth qualifies. It is carried by the flap_open row rather
			// than by a transition row beside it -- flap_open names both states,
			// so nothing about that change is lost.
			transitions: 6, wantTransitionRows: 5, wantFlapOpen: 1, wantFlapping: true,
		},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newObservedFixture(t, e)
					last := f.oscillate(t, tc.transitions)

					if got := f.kindCount(t, domain.TransitionChange); got != tc.wantTransitionRows {
						t.Errorf("%d transition rows, want %d", got, tc.wantTransitionRows)
					}
					if got := f.kindCount(t, domain.TransitionFlapOpen); got != tc.wantFlapOpen {
						t.Errorf("%d flap_open rows, want %d", got, tc.wantFlapOpen)
					}
					if got := f.kindCount(t, domain.TransitionFlapClose); got != 0 {
						t.Errorf("%d flap_close rows before the episode ended, want 0", got)
					}

					health := f.health(t)
					if got := health.Flapping(); got != tc.wantFlapping {
						t.Errorf("flapping = %v, want %v", got, tc.wantFlapping)
					}

					// Rule 9's last sentence: the current value and its onset
					// keep tracking THROUGHOUT. A compressed transition is still
					// a transition as far as the reading is concerned, or a box
					// that died twenty minutes ago still shows as flapping
					// instead of down.
					wantState := "up"
					if (tc.transitions-1)%2 == 1 {
						wantState = "down"
					}
					if string(health.State) != wantState {
						t.Errorf("state = %s, want %s", health.State, wantState)
					}
					if got, want := health.StateSince, domain.FormatTime(flapBase.Add(last)); got != want {
						t.Errorf("state_since = %s, want %s -- the onset must track through compression", got, want)
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The novel-value escape hatch
// ---------------------------------------------------------------------------

// TestNovelValueAlwaysGetsItsOwnRow is the security half of rule 9.
//
// A transition to a value not already seen in the episode is always its own
// row. Without it, a stolen credential can oscillate deliberately to trip the
// suppression floor so that the real `down` a moment later is swallowed by the
// episode and reads as a monitoring artefact. With it, the only values an
// attacker can hide are ones they have already demonstrated, and demonstrating
// them is itself visible in the ledger.
func TestNovelValueAlwaysGetsItsOwnRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// Six alternating reports: the sixth opens the episode, leaving the
			// entity `down` with {up, down} seen.
			f.oscillate(t, 6)
			if !f.health(t).Flapping() {
				t.Fatal("no episode after six transitions; the rest of this test is meaningless")
			}
			rowsAtOpen := f.kindCount(t, domain.TransitionChange)

			// Two more oscillations between values already seen. These are the
			// ones compression exists to absorb: no rows, but the reading keeps
			// moving.
			f.observe(t, 180*time.Second, "up")
			f.observe(t, 210*time.Second, "down")

			if got := f.kindCount(t, domain.TransitionChange); got != rowsAtOpen {
				t.Errorf("%d transition rows after two compressed oscillations, want %d -- "+
					"a value already seen in the episode is part of the oscillation", got, rowsAtOpen)
			}
			if got, want := f.health(t).StateSince, domain.FormatTime(flapBase.Add(210*time.Second)); got != want {
				t.Errorf("state_since = %s, want %s", got, want)
			}

			// A value never seen in this episode. Always its own row.
			f.observe(t, 240*time.Second, "degraded")

			if got, want := f.kindCount(t, domain.TransitionChange), rowsAtOpen+1; got != want {
				t.Fatalf("%d transition rows after a novel value, want %d -- "+
					"a novel value is by definition not part of the oscillation being compressed", got, want)
			}

			// And the oscillation being compressed is over, so the bracket
			// closes and says what it covered.
			closed := f.flapRow(t, domain.TransitionFlapClose)
			assertFlapClose(t, closed, flapClose{
				count:   4, // the opening change plus two compressed plus this one
				start:   domain.FormatTime(flapBase.Add(150 * time.Second)),
				end:     domain.FormatTime(flapBase.Add(240 * time.Second)),
				first:   "up",       // what it was when the episode opened
				last:    "down",     // the last value the oscillation touched
				settled: "degraded", // where it went
			})
			if f.health(t).Flapping() {
				t.Error("still flapping after a novel value ended the episode")
			}

			// The closed episode is not resumed. Re-qualification is measured
			// from emitted rows, and the window still holds the churn that
			// qualified the first time, so the next oscillation opens a FRESH
			// bracket rather than quietly rejoining the old one. Either way the
			// reader sees a new episode start; what must never happen is
			// compression continuing under a bracket that has already been
			// closed and accounted for.
			openBefore := f.kindCount(t, domain.TransitionFlapOpen)
			f.observe(t, 270*time.Second, "up")
			if got, want := f.kindCount(t, domain.TransitionFlapOpen), openBefore+1; got != want {
				t.Errorf("%d flap_open rows, want %d -- compression after a close must be bracketed afresh", got, want)
			}
			reopened := f.health(t)
			if got := reopened.FlapOpenedAt(); got != domain.FormatTime(flapBase.Add(270*time.Second)) {
				t.Errorf("the new episode opened at %s, want %s -- it must not inherit the closed one's window",
					got, domain.FormatTime(flapBase.Add(270*time.Second)))
			}
			if got := reopened.FlapTransitions(); got != 1 {
				t.Errorf("the new episode starts at %d transitions, want 1 -- the closed episode's count "+
					"has already been reported and must not be carried forward", got)
			}
		})
	}
}

// TestFlapCompressionNeverHidesTheState is rule 9's closing sentence, isolated.
//
// "Flapping" is displayed alongside the state, never instead of it. A reading
// whose state froze at the moment compression started would tell an operator
// that a box which died twenty minutes ago is merely noisy.
func TestFlapCompressionNeverHidesTheState(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			f.oscillate(t, 6)

			// One more compressed oscillation, ending `down` and staying there.
			f.observe(t, 180*time.Second, "up")
			f.observe(t, 210*time.Second, "down")

			readings, err := f.store.GetObservedState(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("reading observed state: %v", err)
			}
			if len(readings) != 1 {
				t.Fatalf("%d readings, want 1", len(readings))
			}
			r := readings[0]

			if !r.Flapping() {
				t.Error("the reading does not report that it is flapping")
			}
			if r.State != domain.HealthDown || r.Display != domain.HealthDown {
				t.Errorf("state/display = %s/%s, want down/down -- flapping is shown BESIDE the state",
					r.State, r.Display)
			}
			if got, want := r.StateSince, domain.FormatTime(flapBase.Add(210*time.Second)); got != want {
				t.Errorf("state_since = %s, want %s -- the onset answers \"down since when\"", got, want)
			}
			if got := r.FlapOpenedAt(); got != domain.FormatTime(flapBase.Add(150*time.Second)) {
				t.Errorf("flap opened at %s, want %s", got, domain.FormatTime(flapBase.Add(150*time.Second)))
			}
			if got := r.FlapTransitions(); got != 3 {
				t.Errorf("flap count = %d, want 3", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// What the closing row reports
// ---------------------------------------------------------------------------

// TestFlapCloseReportsWhatItHid is the other half of "compressed, never
// suppressed". Compression is only acceptable because the closing row accounts
// for it in full: a reader who never opens the ledger still sees that a stretch
// of history was collapsed, over what window, and how much of it there was.
func TestFlapCloseReportsWhatItHid(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// Nine state changes in four minutes. Five are emitted, the sixth
			// opens the episode, and the last three are compressed.
			f.oscillate(t, 6)
			f.observe(t, 180*time.Second, "up")
			f.observe(t, 210*time.Second, "down")
			f.observe(t, 240*time.Second, "up")

			if got := f.kindCount(t, domain.TransitionChange); got != 5 {
				t.Fatalf("%d transition rows, want 5 -- three oscillations should have been compressed", got)
			}

			// A full window with no state change, then one ordinary heartbeat.
			// The episode ends on that heartbeat rather than on a background
			// sweep: rule 8 forbids inventing transitions from a job, and a
			// closure that only lands when a person looks is a closure nobody
			// can rely on.
			settleAt := 240*time.Second + domain.FlapWindow
			res := f.observe(t, settleAt, "up")
			if res.Disposition != ObservationUnchanged {
				t.Fatalf("disposition = %s, want unchanged", res.Disposition)
			}

			closed := f.flapRow(t, domain.TransitionFlapClose)
			assertFlapClose(t, closed, flapClose{
				count:   4,
				start:   domain.FormatTime(flapBase.Add(150 * time.Second)),
				end:     domain.FormatTime(flapBase.Add(settleAt)),
				first:   "up",
				last:    "up",
				settled: "up",
			})

			// from_state and to_state span the whole episode, so a timeline that
			// reads only those two columns still tells the truth about it.
			if got := derefText(closed.FromState); got != "up" {
				t.Errorf("from_state = %q, want the state at the start of the episode", got)
			}
			if closed.ToState != domain.HealthUp {
				t.Errorf("to_state = %q, want the settled state", closed.ToState)
			}

			health := f.health(t)
			if health.Flapping() {
				t.Error("the episode did not close after a full window with no transition")
			}
			// Closing a bracket must not disturb the reading it bracketed.
			if got, want := health.StateSince, domain.FormatTime(flapBase.Add(240*time.Second)); got != want {
				t.Errorf("state_since = %s, want %s -- closing an episode is not a transition", got, want)
			}
			if health.State != domain.HealthUp {
				t.Errorf("state = %s, want up", health.State)
			}

			// One bracket, not two: a second heartbeat must not close an
			// episode that has already ended.
			f.observe(t, settleAt+30*time.Second, "up")
			if got := f.kindCount(t, domain.TransitionFlapClose); got != 1 {
				t.Errorf("%d flap_close rows, want 1", got)
			}

			// And the whole point: nine state changes produced seven ledger
			// rows, and the seventh says how many the compression covered.
			total := f.transitions(t)
			if total != 7 {
				t.Errorf("%d ledger rows for nine state changes, want 7", total)
			}
		})
	}
}

// TestFlapEpisodeClosesBeforeALaterTransitionIsClassified covers the sequence
// rule 9 describes in its own words: a token trips the floor, stops, and the
// real `down` arrives five minutes later. It must be an ordinary transition
// with a row of its own, not something absorbed by a stale episode.
func TestFlapEpisodeClosesBeforeALaterTransitionIsClassified(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// Trip the floor and stop, leaving the entity `down` and flapping.
			f.oscillate(t, 6)
			if !f.health(t).Flapping() {
				t.Fatal("no episode after six transitions")
			}
			rowsAtOpen := f.kindCount(t, domain.TransitionChange)

			// Five minutes of quiet, then a real state change to a value the
			// episode has definitely seen -- the hardest case for the escape
			// hatch, because novelty alone would not save it.
			quiet := 150*time.Second + domain.FlapWindow
			f.observe(t, quiet, "up")

			if got, want := f.kindCount(t, domain.TransitionChange), rowsAtOpen+1; got != want {
				t.Errorf("%d transition rows, want %d -- a change after the quiet period is an "+
					"ordinary transition, not a monitoring artefact", got, want)
			}
			if got := f.kindCount(t, domain.TransitionFlapClose); got != 1 {
				t.Errorf("%d flap_close rows, want 1", got)
			}
			if f.health(t).Flapping() {
				t.Error("still flapping five minutes after the oscillation stopped")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The thresholds are not reachable by a writer
// ---------------------------------------------------------------------------

// TestFlapThresholdsAreNotWritableByAReporter enforces the sentence in rule 9
// that has no other enforcement: the constants live in internal/domain, never
// in env, never as a per-row column, and never in a payload.
//
// A writer that can raise its own suppression threshold has a mute button, and
// the whole compression mechanism becomes a way to go quiet on request. The
// wire type is the only surface a reporter controls, so that is what is
// asserted here.
func TestFlapThresholdsAreNotWritableByAReporter(t *testing.T) {
	forbidden := []string{"flap", "threshold", "window", "suppress", "quiet", "mute"}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(domain.ObservationSpec{}),
		reflect.TypeOf(domain.Observation{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, word := range forbidden {
				if strings.Contains(name, word) {
					t.Errorf("%s.%s: a reporter must not be able to influence compression from a payload",
						typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}

	// The values themselves, so a change to them is a deliberate edit to a test
	// rather than a silent widening.
	if domain.FlapThreshold != 5 {
		t.Errorf("FlapThreshold = %d, want 5", domain.FlapThreshold)
	}
	if domain.FlapWindow != 5*time.Minute {
		t.Errorf("FlapWindow = %s, want 5m", domain.FlapWindow)
	}
}

// ---------------------------------------------------------------------------
// Shared assertion
// ---------------------------------------------------------------------------

type flapClose struct {
	count                int
	start, end           string
	first, last, settled string
}

func assertFlapClose(t *testing.T, row domain.ObservedTransition, want flapClose) {
	t.Helper()
	if row.FlapCount == nil || *row.FlapCount != want.count {
		t.Errorf("flap_count = %v, want %d -- the closing row is where the suppression is accounted for",
			formatIntPtr(row.FlapCount), want.count)
	}
	for _, check := range []struct {
		name string
		got  *string
		want string
	}{
		{"window_start", row.WindowStart, want.start},
		{"window_end", row.WindowEnd, want.end},
		{"first_state", row.FirstState, want.first},
		{"last_state", row.LastState, want.last},
		{"settled_state", row.SettledState, want.settled},
	} {
		if check.got == nil {
			t.Errorf("%s is NULL, want %q", check.name, check.want)
			continue
		}
		if *check.got != check.want {
			t.Errorf("%s = %q, want %q", check.name, *check.got, check.want)
		}
	}
}

func formatIntPtr(v *int) string {
	if v == nil {
		return "NULL"
	}
	return strconv.Itoa(*v)
}
