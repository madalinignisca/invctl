// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Tests for the observed write path (docs/AUDIT.md rules 1-8). Every one runs
// on both engines: a change that only passes on SQLite is not done.
//
// Nothing here sleeps. The clock is injected and the flusher is driven by a
// channel the test owns, because a test that waits on wall time is a test that
// is flaky on a loaded build machine and silent about why.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// testClock is the store's time source under test. Mutex-guarded because the
// flusher reads it from its own goroutine.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func newTestClock() *testClock {
	return &testClock{at: time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// observedFixture is an estate with one host and one placement on it, plus the
// recorder and a monitoring credential to report through.
type observedFixture struct {
	store      *SQLStore
	recorder   *ObservedRecorder
	ctx        context.Context
	clock      *testClock
	agent      domain.Actor
	scope      domain.EnvironmentScope
	assetID    string
	instanceID string
}

func newObservedFixture(t *testing.T, e Engine) *observedFixture {
	t.Helper()

	clock := newTestClock()
	s := New(e.Open(t)).WithClock(clock.Now)
	ctx := context.Background()

	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	assetID := mustAsset(t, s, ctx, domain.KindServer, "app-01", nil, envID)

	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: "orders", Name: "Orders", Kind: domain.SvcAPI,
		EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
	}, s.Now())
	if err != nil {
		t.Fatalf("building service: %v", err)
	}
	if err := s.CreateService(ctx, testPermit, svc); err != nil {
		t.Fatalf("creating service: %v", err)
	}

	si, err := domain.NewServiceInstance(NewID(), svc.ID, assetID, domain.RuntimeSystemd, 0, s.Now())
	if err != nil {
		t.Fatalf("building instance: %v", err)
	}
	if err := s.CreateInstance(ctx, testPermit, si); err != nil {
		t.Fatalf("creating instance: %v", err)
	}

	agent, err := domain.AgentActor("prom-a")
	if err != nil {
		t.Fatalf("building agent actor: %v", err)
	}
	// Rule 6: every credential arrives with an explicit environment scope, and
	// the fixture's estate lives in prod.
	scope, err := domain.NewEnvironmentScope([]string{"prod"})
	if err != nil {
		t.Fatalf("building environment scope: %v", err)
	}

	return &observedFixture{
		store: s, recorder: NewObservedRecorder(s), ctx: ctx, clock: clock,
		agent: agent, scope: scope, assetID: assetID, instanceID: si.ID,
	}
}

// report is the reporter-supplied half of one observation.
func (f *observedFixture) report(state string, reportedAt time.Time) domain.ObservationSpec {
	return domain.ObservationSpec{
		EntityType:      domain.ObservableAsset,
		EntityID:        f.assetID,
		State:           state,
		ReportedAt:      domain.FormatTime(reportedAt),
		IntervalSeconds: 30,
	}
}

func (f *observedFixture) record(t *testing.T, spec domain.ObservationSpec) *ObservationResult {
	t.Helper()
	res, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, spec)
	if err != nil {
		t.Fatalf("recording observation %+v: %v", spec, err)
	}
	return res
}

func (f *observedFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.store.db.Reader.Get(&n, f.store.db.Reader.Rebind(query), args...); err != nil {
		t.Fatalf("counting (%s): %v", query, err)
	}
	return n
}

func (f *observedFixture) health(t *testing.T) domain.AssetHealth {
	t.Helper()
	h, err := f.store.readHealth(f.ctx, healthKey{
		EntityType: domain.ObservableAsset, EntityID: f.assetID, Reporter: "prom-a",
	})
	if err != nil {
		t.Fatalf("reading health: %v", err)
	}
	if h == nil {
		t.Fatal("no asset_health row; expected one")
	}
	return *h
}

func (f *observedFixture) transitions(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT COUNT(*) FROM observed_transition WHERE entity_id = ?`, f.assetID)
}

// ---------------------------------------------------------------------------
// Rules 3 and 4: what a report does
// ---------------------------------------------------------------------------

// TestObservationDispositions walks one entity through the sequence a real
// collector produces, and asserts the disposition and the ledger at each step.
//
// The load-bearing assertions are the ones about state_since: the onset moves
// only on a transition, and "down since when" is the first question anyone asks
// at 03:00.
func TestObservationDispositions(t *testing.T) {
	base := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)

	steps := []struct {
		name string
		// offset of the reporter's clock from base.
		reportedOffset  time.Duration
		state           string
		observationID   string
		want            ObservationDisposition
		wantTransitions int
		// wantOnsetOffset is where state_since should sit, as an offset from
		// base on the SERVER clock. -1 means "unchanged from the step before".
		wantOnsetOffset time.Duration
	}{
		{
			name: "first report is a transition and is logged",
			// The first observation has no prior value; it is the entry an
			// incident reviewer looks for, so it must not be silent.
			reportedOffset: 0, state: "up", observationID: "o1",
			want: ObservationTransitioned, wantTransitions: 1, wantOnsetOffset: 0,
		},
		{
			name:           "repeat of the same state is not a transition",
			reportedOffset: 30 * time.Second, state: "up", observationID: "o2",
			want: ObservationUnchanged, wantTransitions: 1, wantOnsetOffset: 0,
		},
		{
			name:           "second repeat is still not a transition",
			reportedOffset: 60 * time.Second, state: "up", observationID: "o3",
			want: ObservationUnchanged, wantTransitions: 1, wantOnsetOffset: 0,
		},
		{
			name:           "a repeated observation_id is a no-op",
			reportedOffset: 90 * time.Second, state: "down", observationID: "o3",
			want: ObservationDuplicate, wantTransitions: 1, wantOnsetOffset: 0,
		},
		{
			name:           "a change of state writes through immediately",
			reportedOffset: 120 * time.Second, state: "down", observationID: "o4",
			want: ObservationTransitioned, wantTransitions: 2, wantOnsetOffset: 120 * time.Second,
		},
		{
			name: "an older report is discarded, not applied",
			// Last-write-wins on arrival order turns a delayed `down` landing
			// after `up` into two transitions that never happened.
			reportedOffset: 60 * time.Second, state: "up", observationID: "o5",
			want: ObservationStale, wantTransitions: 2, wantOnsetOffset: 120 * time.Second,
		},
		{
			name:           "a report at exactly the stored time is not newer",
			reportedOffset: 120 * time.Second, state: "up", observationID: "o6",
			want: ObservationStale, wantTransitions: 2, wantOnsetOffset: 120 * time.Second,
		},
		{
			name:           "recovery is a transition",
			reportedOffset: 150 * time.Second, state: "up", observationID: "o7",
			want: ObservationTransitioned, wantTransitions: 3, wantOnsetOffset: 150 * time.Second,
		},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			for _, step := range steps {
				// The server clock tracks the reporter's here so that the
				// expected onset is readable; the two are separate fields and
				// the code never conflates them.
				f.clock.mu.Lock()
				f.clock.at = base.Add(step.reportedOffset)
				f.clock.mu.Unlock()

				spec := f.report(step.state, base.Add(step.reportedOffset))
				spec.ObservationID = step.observationID

				res := f.record(t, spec)
				if res.Disposition != step.want {
					t.Fatalf("%s: disposition = %s, want %s", step.name, res.Disposition, step.want)
				}
				if got := f.transitions(t); got != step.wantTransitions {
					t.Errorf("%s: %d transition rows, want %d", step.name, got, step.wantTransitions)
				}
				if got, want := f.health(t).StateSince, domain.FormatTime(base.Add(step.wantOnsetOffset)); got != want {
					t.Errorf("%s: state_since = %s, want %s", step.name, got, want)
				}
			}

			// The ledger names what each row replaced. The first row's
			// from_state is NULL because there was no prior value.
			var rows []domain.ObservedTransition
			rows, err := f.store.ListTransitions(f.ctx, domain.ObservableAsset, f.assetID, 0)
			if err != nil {
				t.Fatalf("listing transitions: %v", err)
			}
			if len(rows) != 3 {
				t.Fatalf("%d transitions, want 3", len(rows))
			}
			// Newest first.
			assertTransition(t, rows[2], nil, "up")
			assertTransition(t, rows[1], strptr("up"), "down")
			assertTransition(t, rows[0], strptr("down"), "up")
			for _, row := range rows {
				if row.Kind != domain.TransitionChange {
					t.Errorf("transition kind = %q, want %q", row.Kind, domain.TransitionChange)
				}
				if row.Reporter != "prom-a" {
					t.Errorf("transition reporter = %q, want prom-a", row.Reporter)
				}
			}
		})
	}
}

func assertTransition(t *testing.T, got domain.ObservedTransition, from *string, to string) {
	t.Helper()
	switch {
	case from == nil && got.FromState != nil:
		t.Errorf("from_state = %q, want NULL", *got.FromState)
	case from != nil && got.FromState == nil:
		t.Errorf("from_state = NULL, want %q", *from)
	case from != nil && *got.FromState != *from:
		t.Errorf("from_state = %q, want %q", *got.FromState, *from)
	}
	if string(got.ToState) != to {
		t.Errorf("to_state = %q, want %q", got.ToState, to)
	}
}

func strptr(s string) *string { return &s }

// TestRepeatObservationStaysOffTheWritePath is rule 4's core claim, asserted
// rather than assumed: a hundred heartbeats that change nothing produce no
// transition rows, do not move the onset, and do not reach the database at all
// until the flush.
//
// On SQLite the writer pool is a single connection. A design where heartbeats
// can occupy it makes "retire this asset" time out at exactly the moment it
// matters, so "did not open a write transaction" is the property, not an
// optimisation.
func TestRepeatObservationStaysOffTheWritePath(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			base := f.clock.Now()

			first := f.record(t, f.report("up", base))
			if first.Disposition != ObservationTransitioned {
				t.Fatalf("first report disposition = %s", first.Disposition)
			}
			stored := f.health(t)

			for i := 1; i <= 100; i++ {
				f.clock.Advance(30 * time.Second)
				res := f.record(t, f.report("up", base.Add(time.Duration(i)*30*time.Second)))
				if res.Disposition != ObservationUnchanged {
					t.Fatalf("report %d: disposition = %s, want %s", i, res.Disposition, ObservationUnchanged)
				}
			}

			if got := f.transitions(t); got != 1 {
				t.Errorf("%d transition rows after 100 repeats, want 1", got)
			}

			after := f.health(t)
			if after.StateSince != stored.StateSince {
				t.Errorf("state_since moved on a repeat: %s -> %s", stored.StateSince, after.StateSince)
			}
			if after.LastReportAt != stored.LastReportAt {
				t.Errorf("last_report_at reached the database without a flush: %s -> %s",
					stored.LastReportAt, after.LastReportAt)
			}

			// The buffer is authoritative until it is flushed, which is what
			// makes the staleness clock keep running for the caller.
			n, err := f.recorder.Flush(f.ctx)
			if err != nil {
				t.Fatalf("flushing: %v", err)
			}
			if n != 1 {
				t.Errorf("flushed %d readings, want 1 (a hundred reports collapse to one write)", n)
			}
			flushed := f.health(t)
			if want := domain.FormatTime(base.Add(100 * 30 * time.Second)); flushed.LastReportAt != want {
				t.Errorf("last_report_at after flush = %s, want %s", flushed.LastReportAt, want)
			}
			if flushed.StateSince != stored.StateSince {
				t.Errorf("flush moved state_since: %s -> %s", stored.StateSince, flushed.StateSince)
			}
			if flushed.State != stored.State {
				t.Errorf("flush changed state: %s -> %s", stored.State, flushed.State)
			}

			// A second flush has nothing to do; a buffer that never empties
			// would write the same row forever.
			if n, err := f.recorder.Flush(f.ctx); err != nil || n != 0 {
				t.Errorf("second flush = (%d, %v), want (0, nil)", n, err)
			}
		})
	}
}

// TestTransitionSupersedesTheBuffer. A buffered bump that outlived the write it
// was superseded by would rewind reported_at on the next flush, and the entity
// would then accept a replay it had already rejected.
func TestTransitionSupersedesTheBuffer(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			base := f.clock.Now()

			f.record(t, f.report("up", base))
			f.clock.Advance(30 * time.Second)
			f.record(t, f.report("up", base.Add(30*time.Second))) // buffered

			f.clock.Advance(30 * time.Second)
			res := f.record(t, f.report("down", base.Add(60*time.Second)))
			if res.Disposition != ObservationTransitioned {
				t.Fatalf("disposition = %s, want %s", res.Disposition, ObservationTransitioned)
			}

			n, err := f.recorder.Flush(f.ctx)
			if err != nil {
				t.Fatalf("flushing: %v", err)
			}
			if n != 0 {
				t.Errorf("flushed %d readings after a write-through, want 0", n)
			}

			h := f.health(t)
			if want := domain.FormatTime(base.Add(60 * time.Second)); h.ReportedAt != want {
				t.Errorf("reported_at = %s, want %s (the flush rewound it)", h.ReportedAt, want)
			}
			if h.State != domain.HealthDown {
				t.Errorf("state = %s, want down", h.State)
			}
		})
	}
}

// TestReDeliveryDuringTheBufferIsIdempotent. Idempotency and ordering are both
// decided against "the last report we accepted", and for a buffered key that
// value is in memory rather than on disk. Comparing against the disk copy alone
// re-accepts a report already seen this interval -- and since the buffer holds
// up to thirty seconds of heartbeats, that is the common case for a collector
// with at-least-once delivery, not an edge case.
func TestReDeliveryDuringTheBufferIsIdempotent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			base := f.clock.Now()

			first := f.report("up", base)
			first.ObservationID = "o1"
			f.record(t, first)

			f.clock.Advance(30 * time.Second)
			second := f.report("up", base.Add(30*time.Second))
			second.ObservationID = "o2"
			if got := f.record(t, second).Disposition; got != ObservationUnchanged {
				t.Fatalf("second report disposition = %s, want %s", got, ObservationUnchanged)
			}

			// The same report again, before the flush. The stored row still
			// says o1 at base; only the buffer knows about o2.
			if got := f.record(t, second).Disposition; got != ObservationDuplicate {
				t.Errorf("re-delivery disposition = %s, want %s", got, ObservationDuplicate)
			}

			// And a replay of an older report, likewise only detectable against
			// the buffer.
			replay := f.report("up", base.Add(15*time.Second))
			replay.ObservationID = "o1-replayed"
			if got := f.record(t, replay).Disposition; got != ObservationStale {
				t.Errorf("replay disposition = %s, want %s", got, ObservationStale)
			}

			if _, err := f.recorder.Flush(f.ctx); err != nil {
				t.Fatalf("flushing: %v", err)
			}
			h := f.health(t)
			if want := domain.FormatTime(base.Add(30 * time.Second)); h.ReportedAt != want {
				t.Errorf("reported_at = %s, want %s: a replay was applied", h.ReportedAt, want)
			}
			if got := f.transitions(t); got != 1 {
				t.Errorf("%d transition rows, want 1", got)
			}
		})
	}
}

// TestFutureReportIsRejected. RFC3339 TEXT sorts lexicographically, so a
// timestamp from a broken or hostile clock pins to the top of every ORDER BY
// and wins every monotonicity comparison from then on -- the entity could never
// be updated again.
func TestFutureReportIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		ahead  time.Duration
		wantOK bool
	}{
		{name: "at the server clock", ahead: 0, wantOK: true},
		{name: "inside the skew allowance", ahead: 299 * time.Second, wantOK: true},
		{name: "exactly at the allowance", ahead: domain.MaxClockSkew, wantOK: true},
		{name: "past the allowance", ahead: domain.MaxClockSkew + time.Second, wantOK: false},
		{name: "wildly ahead", ahead: 365 * 24 * time.Hour, wantOK: false},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newObservedFixture(t, e)
					_, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope,
						f.report("up", f.clock.Now().Add(tc.ahead)))

					switch {
					case tc.wantOK && err != nil:
						t.Fatalf("report %s ahead rejected: %v", tc.ahead, err)
					case !tc.wantOK && err == nil:
						t.Fatalf("report %s ahead accepted", tc.ahead)
					case !tc.wantOK:
						if !errors.Is(err, domain.ErrInvalid) {
							t.Errorf("error = %v, want ErrInvalid", err)
						}
						if got := f.count(t, `SELECT COUNT(*) FROM asset_health`); got != 0 {
							t.Errorf("%d health rows written for a rejected report", got)
						}
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rule 6: an observation never creates an entity
// ---------------------------------------------------------------------------

// TestUnknownEntityIs404AndQueuedAsDrift. `INSERT ... ON CONFLICT` keyed on the
// reported id is the natural implementation and it is exactly what turns a
// deliberately narrow monitoring token into an inventory-write vector. The
// report is not dropped either: an asset the estate has and the inventory does
// not is a finding.
func TestUnknownEntityIs404AndQueuedAsDrift(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			assetsBefore := f.count(t, `SELECT COUNT(*) FROM asset`)
			instancesBefore := f.count(t, `SELECT COUNT(*) FROM service_instance`)

			ghost := f.report("down", f.clock.Now())
			ghost.EntityID = "host-the-inventory-has-never-heard-of"

			for i := 1; i <= 3; i++ {
				_, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, ghost)
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("report %d: error = %v, want ErrNotFound", i, err)
				}
			}

			if got := f.count(t, `SELECT COUNT(*) FROM asset`); got != assetsBefore {
				t.Errorf("%d assets, want %d: an observation created an entity", got, assetsBefore)
			}
			if got := f.count(t, `SELECT COUNT(*) FROM service_instance`); got != instancesBefore {
				t.Errorf("%d instances, want %d: an observation created an entity", got, instancesBefore)
			}
			if got := f.count(t, `SELECT COUNT(*) FROM asset_health`); got != 0 {
				t.Errorf("%d health rows for an unknown entity, want 0", got)
			}

			queued, err := f.store.ListUnmatchedObservations(f.ctx, 0)
			if err != nil {
				t.Fatalf("listing unmatched: %v", err)
			}
			if len(queued) != 1 {
				t.Fatalf("%d unmatched rows, want 1: a queue that floods is a queue nobody reads", len(queued))
			}
			if queued[0].ReportCount != 3 {
				t.Errorf("report_count = %d, want 3", queued[0].ReportCount)
			}
			if queued[0].EntityRef != ghost.EntityID {
				t.Errorf("entity_ref = %q, want %q", queued[0].EntityRef, ghost.EntityID)
			}
			if queued[0].Reporter != "prom-a" {
				t.Errorf("reporter = %q, want prom-a", queued[0].Reporter)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rules 2, 3 and 13: the allowlist
// ---------------------------------------------------------------------------

// TestObservationLeavesDeclaredStateUntouched is the allowlist asserted at the
// database rather than read off the code. Every declared column of the observed
// entity -- including updated_at, which means "when a person last changed this
// row" -- must be byte-identical afterwards, and nothing may reach the audit
// trail or the search index.
func TestObservationLeavesDeclaredStateUntouched(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			base := f.clock.Now()

			assetBefore := rowSnapshot(t, f, `SELECT * FROM asset WHERE id = ?`, f.assetID)
			instanceBefore := rowSnapshot(t, f, `SELECT * FROM service_instance WHERE id = ?`, f.instanceID)
			changesBefore := f.count(t, `SELECT COUNT(*) FROM change_log`)
			indexBefore := f.count(t, `SELECT COUNT(*) FROM search_index`)

			// A transition, a buffered repeat, a flush, a second transition and
			// an unmatched report: every write this file can perform.
			f.record(t, f.report("up", base))
			f.clock.Advance(30 * time.Second)
			f.record(t, f.report("up", base.Add(30*time.Second)))
			if _, err := f.recorder.Flush(f.ctx); err != nil {
				t.Fatalf("flushing: %v", err)
			}
			f.clock.Advance(30 * time.Second)
			f.record(t, f.report("down", base.Add(60*time.Second)))

			instanceReport := domain.ObservationSpec{
				EntityType: domain.ObservableServiceInstance, EntityID: f.instanceID,
				State: "degraded", ReportedAt: domain.FormatTime(f.clock.Now()), IntervalSeconds: 60,
			}
			f.record(t, instanceReport)

			ghost := f.report("down", f.clock.Now())
			ghost.EntityID = "nope"
			if _, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, ghost); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("unmatched report error = %v, want ErrNotFound", err)
			}

			assertRowUnchanged(t, "asset", assetBefore, rowSnapshot(t, f, `SELECT * FROM asset WHERE id = ?`, f.assetID))
			assertRowUnchanged(t, "service_instance", instanceBefore,
				rowSnapshot(t, f, `SELECT * FROM service_instance WHERE id = ?`, f.instanceID))

			// Rule 3: observed writes put nothing in the audit trail. The
			// unbounded growth this prevents is the whole reason the tables are
			// split, and a single leaked row here becomes permanent -- the log
			// is append-only.
			if got := f.count(t, `SELECT COUNT(*) FROM change_log`); got != changesBefore {
				t.Errorf("change_log grew from %d to %d during observed writes", changesBefore, got)
			}

			// Rule 13: never indexed. Reindexing per heartbeat rewrites the FTS
			// table thousands of times a day against a single SQLite writer,
			// and a health value in the index surfaces to a responder pasting a
			// hostname mid-incident.
			if got := f.count(t, `SELECT COUNT(*) FROM search_index`); got != indexBefore {
				t.Errorf("search_index grew from %d to %d during observed writes", indexBefore, got)
			}
			assertIndexFreeOfObservedValues(t, f)
		})
	}
}

// rowSnapshot reads one row as column -> rendered value, so a comparison covers
// every column including ones added after this test was written.
func rowSnapshot(t *testing.T, f *observedFixture, query string, args ...any) map[string]string {
	t.Helper()
	row := f.store.db.Reader.QueryRowx(f.store.db.Reader.Rebind(query), args...)
	raw := map[string]any{}
	if err := row.MapScan(raw); err != nil {
		t.Fatalf("snapshotting (%s): %v", query, err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if b, ok := v.([]byte); ok {
			out[k] = string(b)
			continue
		}
		out[k] = fmt.Sprintf("%v", v)
	}
	if len(out) == 0 {
		t.Fatalf("snapshot of (%s) is empty", query)
	}
	return out
}

func assertRowUnchanged(t *testing.T, table string, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("%s: %d columns before, %d after", table, len(before), len(after))
	}
	cols := make([]string, 0, len(before))
	for col := range before {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	for _, col := range cols {
		if before[col] != after[col] {
			t.Errorf("%s.%s changed during an observed write: %q -> %q",
				table, col, before[col], after[col])
		}
	}
}

// assertIndexFreeOfObservedValues checks the index holds nothing an external
// credential authored. The reporter id is the precise probe: unlike "up" or
// "down" it appears nowhere in declared data, so a hit is unambiguous.
func assertIndexFreeOfObservedValues(t *testing.T, f *observedFixture) {
	t.Helper()
	type doc struct {
		Title    string `db:"title"`
		Subtitle string `db:"subtitle"`
		Body     string `db:"body"`
	}
	var docs []doc
	err := f.store.db.Reader.Select(&docs, `SELECT title, subtitle, body FROM search_index`)
	if err != nil {
		t.Fatalf("reading search_index: %v", err)
	}
	// An empty index would make every assertion below vacuous, which is how a
	// negative test quietly stops testing anything.
	if len(docs) == 0 {
		t.Fatal("search_index is empty; this assertion proves nothing")
	}
	for _, d := range docs {
		text := strings.ToLower(d.Title + " " + d.Subtitle + " " + d.Body)
		for _, needle := range []string{"prom-a", "monitor:", "degraded"} {
			if strings.Contains(text, needle) {
				t.Errorf("search_index contains an observed value %q: %+v", needle, d)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Rule 5: attribution
// ---------------------------------------------------------------------------

// TestObservationRequiresAMonitoringCredential. The reporter is part of the
// identity key, so a caller that could choose it could overwrite another
// monitor's history rather than sit beside it -- and an actor that is not a
// machine has no business writing an observed column at all: an operator who
// knows a reading is wrong writes a health_override, which is declared and
// audited.
func TestObservationRequiresAMonitoringCredential(t *testing.T) {
	goodAgent, err := domain.AgentActor("prom-a")
	if err != nil {
		t.Fatalf("building agent actor: %v", err)
	}

	cases := []struct {
		name         string
		actor        domain.Actor
		wantAccepted bool
	}{
		{name: "a monitoring credential", actor: goodAgent, wantAccepted: true},
		{name: "a signed-in operator", actor: testActor},
		{name: "the system actor", actor: domain.SystemActor},
		{name: "the zero actor", actor: domain.Actor{}},
		{
			// The constructor exists so that the namespace and the kind
			// spelling are not in the caller's hands. A hand-built literal must
			// fail here, in Go, rather than as a CHECK failure inside a webhook
			// at 03:00.
			name:  "a hand-built agent literal missing the namespace",
			actor: domain.Actor{ID: "prom-a", Name: "prom-a", Kind: domain.ActorKindAgent},
		},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newObservedFixture(t, e)
					_, err := f.recorder.RecordObservation(f.ctx, tc.actor, f.scope, f.report("up", f.clock.Now()))

					if tc.wantAccepted {
						if err != nil {
							t.Fatalf("rejected a valid credential: %v", err)
						}
						if got := f.count(t, `SELECT COUNT(*) FROM asset_health WHERE reporter = ?`, "prom-a"); got != 1 {
							t.Errorf("%d rows for reporter prom-a, want 1", got)
						}
						return
					}
					if err == nil {
						t.Fatal("accepted an observation from a non-agent actor")
					}
					if got := f.count(t, `SELECT COUNT(*) FROM asset_health`); got != 0 {
						t.Errorf("%d health rows written for a rejected actor", got)
					}
					if got := f.count(t, `SELECT COUNT(*) FROM observed_transition`); got != 0 {
						t.Errorf("%d transition rows written for a rejected actor", got)
					}
				})
			}
		})
	}
}

// TestTwoReportersCoexist. Reporter is in the primary key because two monitors
// watching one asset and disagreeing must show as two readings that disagree.
// Keyed on the entity alone they would overwrite each other on every poll and
// the entity would appear to flap forever -- the one failure mode that makes an
// operator stop believing the panel.
func TestTwoReportersCoexist(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			base := f.clock.Now()

			second, err := domain.AgentActor("nagios-b")
			if err != nil {
				t.Fatalf("building second agent: %v", err)
			}

			f.record(t, f.report("up", base))
			if _, err := f.recorder.RecordObservation(f.ctx, second, f.scope, f.report("down", base)); err != nil {
				t.Fatalf("recording second reporter: %v", err)
			}

			readings, err := f.store.GetObservedState(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("getting observed state: %v", err)
			}
			if len(readings) != 2 {
				t.Fatalf("%d readings, want 2", len(readings))
			}
			// Ordered by reporter, so the disagreement is stable on screen.
			if readings[0].Reporter != "nagios-b" || readings[1].Reporter != "prom-a" {
				t.Fatalf("reporters = %s, %s; want nagios-b, prom-a", readings[0].Reporter, readings[1].Reporter)
			}
			if readings[0].State != domain.HealthDown || readings[1].State != domain.HealthUp {
				t.Errorf("states = %s, %s; want down, up -- one reporter overwrote the other",
					readings[0].State, readings[1].State)
			}
			if got := f.transitions(t); got != 2 {
				t.Errorf("%d transition rows, want 2 (one per reporter)", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rule 8: staleness at read time
// ---------------------------------------------------------------------------

// TestStalenessIsComputedAtReadTime. Silence is not health. An intruder's first
// act is killing the collector, and under transition-only logging a dead
// collector and a healthy estate are otherwise the same picture, forever -- so
// a reading past its horizon renders as unknown, never as its last value, and
// no background job invents a transition to make that happen.
func TestStalenessIsComputedAtReadTime(t *testing.T) {
	last := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	interval := 30

	cases := []struct {
		name           string
		age            time.Duration
		intervalSecs   *int
		wantDisplay    domain.HealthState
		wantStale      bool
		wantStaleSince string
	}{
		{
			name: "fresh", age: 0, intervalSecs: &interval,
			wantDisplay: domain.HealthUp,
		},
		{
			name: "one missed poll is noise", age: 45 * time.Second, intervalSecs: &interval,
			wantDisplay: domain.HealthUp,
		},
		{
			name: "exactly at the horizon is still believed", age: 90 * time.Second, intervalSecs: &interval,
			wantDisplay: domain.HealthUp,
		},
		{
			name: "one second past the horizon", age: 91 * time.Second, intervalSecs: &interval,
			wantDisplay: domain.HealthUnknown, wantStale: true,
			wantStaleSince: domain.FormatTime(last.Add(90 * time.Second)),
		},
		{
			name: "long dead", age: 7 * 24 * time.Hour, intervalSecs: &interval,
			wantDisplay: domain.HealthUnknown, wantStale: true,
			wantStaleSince: domain.FormatTime(last.Add(90 * time.Second)),
		},
		{
			// An unknown horizon is not evidence of freshness. A reading whose
			// cadence nobody declared can never be shown to have gone quiet, so
			// it is never shown as believed either.
			name: "no declared cadence", age: 0, intervalSecs: nil,
			wantDisplay: domain.HealthUnknown, wantStale: true,
			wantStaleSince: domain.FormatTime(last),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := domain.AssetHealth{
				EntityType: domain.ObservableAsset, EntityID: "a1", Reporter: "prom-a",
				State: domain.HealthUp, StateSince: domain.FormatTime(last),
				ReportedAt: domain.FormatTime(last), LastReportAt: domain.FormatTime(last),
				IntervalSeconds: tc.intervalSecs,
			}
			got := newObservedReading(h, last.Add(tc.age))

			if got.Display != tc.wantDisplay {
				t.Errorf("display = %s, want %s", got.Display, tc.wantDisplay)
			}
			if got.Stale != tc.wantStale {
				t.Errorf("stale = %v, want %v", got.Stale, tc.wantStale)
			}
			if got.StaleSince != tc.wantStaleSince {
				t.Errorf("stale_since = %q, want %q", got.StaleSince, tc.wantStaleSince)
			}
			// The reading underneath is never rewritten: when the collector
			// comes back you need to know what it last said.
			if got.State != domain.HealthUp {
				t.Errorf("state = %s, want up: staleness rewrote the stored reading", got.State)
			}
		})
	}
}

// TestGetObservedStateAppliesStaleness end-to-ends the same arithmetic through
// the store, because the clock the view uses is the store's, not the caller's.
func TestGetObservedStateAppliesStaleness(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			f.record(t, f.report("up", f.clock.Now()))

			fresh, err := f.store.GetObservedState(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("getting observed state: %v", err)
			}
			if len(fresh) != 1 || fresh[0].Display != domain.HealthUp || fresh[0].Stale {
				t.Fatalf("fresh reading = %+v, want one believed 'up'", fresh)
			}

			// Three intervals and a second later the collector has gone quiet.
			f.clock.Advance(91 * time.Second)
			stale, err := f.store.GetObservedState(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("getting observed state: %v", err)
			}
			if len(stale) != 1 {
				t.Fatalf("%d readings, want 1", len(stale))
			}
			if stale[0].Display != domain.HealthUnknown || !stale[0].Stale {
				t.Errorf("stale reading = %+v, want unknown", stale[0])
			}
			if stale[0].StaleSince == "" {
				t.Error("stale_since is empty; a view cannot render 'unknown (stale since T)'")
			}
			// Nothing was written to make that happen.
			if got := f.transitions(t); got != 1 {
				t.Errorf("%d transition rows, want 1: staleness invented a transition", got)
			}

			// An entity nothing has ever reported on is unobserved, not up.
			none, err := f.store.GetObservedState(f.ctx, domain.ObservableServiceInstance, f.instanceID)
			if err != nil {
				t.Fatalf("getting observed state for an unobserved entity: %v", err)
			}
			if len(none) != 0 {
				t.Errorf("%d readings for an unobserved entity, want 0", len(none))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The flusher
// ---------------------------------------------------------------------------

// TestRunFlushesOnTickAndOnShutdown drives the flusher through a channel the
// test owns. Nothing sleeps: the second send cannot be accepted until the loop
// is back at its select, which is after the first flush has committed, so the
// assertion below is ordered by the channel rather than by hope.
func TestRunFlushesOnTickAndOnShutdown(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			base := f.clock.Now()

			f.record(t, f.report("up", base))
			f.clock.Advance(30 * time.Second)
			f.record(t, f.report("up", base.Add(30*time.Second)))

			ctx, cancel := context.WithCancel(f.ctx)
			tick := make(chan time.Time)
			done := make(chan struct{})
			go func() {
				defer close(done)
				f.recorder.Run(ctx, tick)
			}()

			tick <- time.Time{} // flush
			tick <- time.Time{} // accepted only once the flush above finished

			if got, want := f.health(t).LastReportAt, domain.FormatTime(base.Add(30*time.Second)); got != want {
				t.Errorf("last_report_at after a tick = %s, want %s", got, want)
			}

			// Shutdown must not discard the staleness clock for every entity
			// that is reporting normally.
			f.clock.Advance(30 * time.Second)
			f.record(t, f.report("up", base.Add(60*time.Second)))
			cancel()
			<-done

			if got, want := f.health(t).LastReportAt, domain.FormatTime(base.Add(60*time.Second)); got != want {
				t.Errorf("last_report_at after shutdown = %s, want %s", got, want)
			}
		})
	}
}

// TestFlushIsOrderedAndBatched. One transaction for the whole estate is the
// trade rule 4 asks for, and a deterministic order keeps the write predictable
// under a lock -- and the test repeatable.
func TestFlushIsOrderedAndBatched(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			base := f.clock.Now()

			reporters := []string{"prom-a", "nagios-b", "zabbix-c"}
			actors := map[string]domain.Actor{}
			for _, id := range reporters {
				a, err := domain.AgentActor(id)
				if err != nil {
					t.Fatalf("building actor %s: %v", id, err)
				}
				actors[id] = a
				if _, err := f.recorder.RecordObservation(f.ctx, a, f.scope, f.report("up", base)); err != nil {
					t.Fatalf("first report from %s: %v", id, err)
				}
			}

			f.clock.Advance(30 * time.Second)
			for _, id := range reporters {
				res, err := f.recorder.RecordObservation(f.ctx, actors[id], f.scope, f.report("up", base.Add(30*time.Second)))
				if err != nil {
					t.Fatalf("repeat from %s: %v", id, err)
				}
				if res.Disposition != ObservationUnchanged {
					t.Fatalf("repeat from %s: disposition = %s", id, res.Disposition)
				}
			}

			n, err := f.recorder.Flush(f.ctx)
			if err != nil {
				t.Fatalf("flushing: %v", err)
			}
			if n != len(reporters) {
				t.Errorf("flushed %d readings, want %d", n, len(reporters))
			}

			readings, err := f.store.GetObservedState(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("getting observed state: %v", err)
			}
			if len(readings) != len(reporters) {
				t.Fatalf("%d readings, want %d", len(readings), len(reporters))
			}
			want := domain.FormatTime(base.Add(30 * time.Second))
			for _, r := range readings {
				if r.LastReportAt != want {
					t.Errorf("%s: last_report_at = %s, want %s", r.Reporter, r.LastReportAt, want)
				}
				if r.StateSince != domain.FormatTime(base) {
					t.Errorf("%s: flush moved state_since to %s", r.Reporter, r.StateSince)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rule 13: the vocabulary
// ---------------------------------------------------------------------------

// TestUnmappedStateIsRejected. Vendor vocabularies (`firing`, `NotReady`, `2`)
// are mapped at the adapter, per reporter. An unmapped value is a validation
// failure with the offending value echoed -- never coerced, never stored raw. A
// column an external credential authors and an operator reads mid-incident is
// the last place to accept arbitrary strings.
func TestUnmappedStateIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		wantOK bool
	}{
		{name: "up", state: "up", wantOK: true},
		{name: "degraded", state: "degraded", wantOK: true},
		{name: "down", state: "down", wantOK: true},
		{name: "unknown is a positive assertion of ignorance", state: "unknown", wantOK: true},
		{name: "a prometheus vocabulary", state: "firing"},
		{name: "a kubernetes vocabulary", state: "NotReady"},
		{name: "a nagios exit code", state: "2"},
		{name: "the old free-text value the seed used to write", state: "running"},
		{name: "case is a mapping decision, not a parse", state: "UP"},
		{name: "empty", state: ""},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					_, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, f.report(tc.state, f.clock.Now()))
					if tc.wantOK {
						if err != nil {
							t.Fatalf("rejected %q: %v", tc.state, err)
						}
						f.clock.Advance(time.Second)
						return
					}
					if err == nil {
						t.Fatalf("accepted %q", tc.state)
					}
					if !errors.Is(err, domain.ErrInvalid) {
						t.Errorf("error = %v, want ErrInvalid", err)
					}
					if !strings.Contains(err.Error(), tc.state) && tc.state != "" {
						t.Errorf("error %q does not echo the offending value %q", err, tc.state)
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rule 6: the environment scope
// ---------------------------------------------------------------------------

// TestObservationOutsideTheCredentialsScopeIsRefused. Rule 6: "A credential is
// scoped to an explicit set of environments -- a dev collector's token must not
// be able to write health for an in_scope environment."
//
// The check lives here rather than in the handler on purpose. The webhook is
// the only caller today, and a rule enforced only at the caller is a rule that
// the second caller does not have.
func TestObservationOutsideTheCredentialsScopeIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			elsewhere, err := domain.NewEnvironmentScope([]string{"dev"})
			if err != nil {
				t.Fatalf("building scope: %v", err)
			}

			_, err = f.recorder.RecordObservation(f.ctx, f.agent, elsewhere, f.report("down", f.clock.Now()))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want ErrForbidden", err)
			}
			// The message names both sides, because a 403 that says only "no"
			// takes an afternoon to diagnose.
			for _, want := range []string{"dev", "prod"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
			// Refused means nothing written anywhere -- and not queued as drift
			// either: the entity exists, so this is an authorization finding
			// rather than inventory drift.
			for _, table := range []string{"asset_health", "observed_transition", "unmatched_observation"} {
				if got := f.count(t, `SELECT COUNT(*) FROM `+table); got != 0 {
					t.Errorf("%d rows in %s after a refused report, want 0", got, table)
				}
			}

			// The same report from a credential that is scoped for it lands.
			if _, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope,
				f.report("down", f.clock.Now())); err != nil {
				t.Fatalf("refused an in-scope report: %v", err)
			}
		})
	}
}

// TestObservationWithNoScopeIsRefused. An empty scope is a mis-wired route, not
// a bad request: every credential is scoped explicitly at startup, so a scope
// that arrives empty was lost on the way. Refusing beats the alternative, which
// is defaulting to "everything" in the one place a default must never widen.
func TestObservationWithNoScopeIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			for _, scope := range []domain.EnvironmentScope{nil, {}} {
				_, err := f.recorder.RecordObservation(f.ctx, f.agent, scope, f.report("up", f.clock.Now()))
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid for an empty scope", err)
				}
			}
			if got := f.count(t, `SELECT COUNT(*) FROM asset_health`); got != 0 {
				t.Errorf("%d readings written with no scope, want 0", got)
			}
		})
	}
}

// TestAnEntityInNoEnvironmentIsInNobodysScope. An asset nobody placed is a data
// gap. Surfacing it as a denial gets it fixed; an implicit allow would make the
// scope silently optional for exactly the rows nobody has looked at.
func TestAnEntityInNoEnvironmentIsInNobodysScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			orphan := mustAsset(t, f.store, f.ctx, domain.KindServer, "unplaced-01", nil)

			spec := f.report("up", f.clock.Now())
			spec.EntityID = orphan

			_, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, spec)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want ErrForbidden", err)
			}
			if got := f.count(t, `SELECT COUNT(*) FROM asset_health`); got != 0 {
				t.Errorf("%d readings for an unplaced asset, want 0", got)
			}
		})
	}
}

// TestAWorkloadInheritsItsHostsEnvironments. A service_instance has no
// environment of its own; the placement is the physical fact and a workload
// cannot be in an environment its host is not in. Without this inheritance
// every workload would be outside every scope, and rule 6 would be
// unimplementable for the entity type that matters most during an incident.
func TestAWorkloadInheritsItsHostsEnvironments(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			spec := f.report("down", f.clock.Now())
			spec.EntityType = domain.ObservableServiceInstance
			spec.EntityID = f.instanceID

			elsewhere, err := domain.NewEnvironmentScope([]string{"dev"})
			if err != nil {
				t.Fatalf("building scope: %v", err)
			}
			if _, err := f.recorder.RecordObservation(f.ctx, f.agent, elsewhere, spec); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want ErrForbidden for an out-of-scope workload", err)
			}
			if _, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, spec); err != nil {
				t.Fatalf("refused an in-scope workload: %v", err)
			}
			if got := f.count(t,
				`SELECT COUNT(*) FROM asset_health WHERE entity_type = ? AND entity_id = ?`,
				domain.ObservableServiceInstance, f.instanceID); got != 1 {
				t.Errorf("%d readings for the placement, want 1", got)
			}
		})
	}
}

// TestTheDriftQueueIsBoundedAndPrunable.
//
// The upsert key includes entity_ref, which the reporter chooses freely, so
// novel refs never collide and the counter never absorbs them. An authenticated
// credential could drive unbounded unindexed inserts -- each its own
// transaction against SQLite's single writer -- and rule 10 named
// observed_transition as the only prunable table, so nothing could ever remove
// them. Same writer contention rule 4 exists to prevent, arriving through the
// drift queue instead of the heartbeat path.
func TestTheDriftQueueIsBoundedAndPrunable(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// Every ref is novel, which is the attacker's shape: the upsert
			// counter can never collapse them.
			ghosts := MaxUnmatchedPerReporter + 50
			for i := 0; i < ghosts; i++ {
				_, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, domain.ObservationSpec{
					EntityType:      domain.ObservableAsset,
					EntityID:        fmt.Sprintf("ghost-%06d", i),
					State:           "down",
					ReportedAt:      domain.FormatTime(f.clock.Now()),
					IntervalSeconds: 30,
				})
				// Every one is a 404 for an unknown entity; that is correct and
				// unchanged. What matters is what it left behind.
				if err == nil {
					t.Fatalf("ghost %d was accepted; an observation for an unknown entity must never create it", i)
				}
			}

			queued := f.count(t, `SELECT COUNT(*) FROM unmatched_observation`)
			if queued > MaxUnmatchedPerReporter {
				t.Errorf("%d drift rows queued from %d novel refs, above the %d cap: a credential "+
					"can use the queue as unbounded storage on the single writer",
					queued, ghosts, MaxUnmatchedPerReporter)
			}
			if queued == 0 {
				t.Fatal("nothing was queued at all, so the drift finding rule 6 wants is gone entirely")
			}

			// And it has a way out. Without one the cap alone would turn a
			// saturated queue into a permanent blind spot.
			operator, err := domain.NewAppUser(NewID(), "drift-operator", domain.UserSourceLocal, f.store.Now())
			if err != nil {
				t.Fatalf("building operator: %v", err)
			}
			if err := f.store.CreateUser(f.ctx, testPermit, operator); err != nil {
				t.Fatalf("creating operator: %v", err)
			}
			// Move time forward rather than asking the prune to reach into the
			// future, which it refuses -- correctly, since a cutoff ahead of
			// now is a truncate wearing a cutoff's clothes.
			queuedAt := f.clock.Now()
			f.clock.Advance(48 * time.Hour)
			report, err := f.store.PruneUnmatchedObservations(f.ctx, domain.AdministratorPermit(domain.UserActor(operator)), PruneOptions{
				Before: queuedAt.Add(time.Minute),
			})
			if err != nil {
				t.Fatalf("pruning the drift queue: %v", err)
			}
			if report.Deleted == 0 {
				t.Error("the prune removed nothing, so the drift queue has no way out")
			}
			if left := f.count(t, `SELECT COUNT(*) FROM unmatched_observation`); left != 0 {
				t.Errorf("%d drift rows survived a prune reaching past all of them", left)
			}

			// An agent may not clear the record of what it named.
			if _, err := f.store.PruneUnmatchedObservations(f.ctx, domain.AdministratorPermit(f.agent), PruneOptions{
				Before: f.clock.Now(),
			}); err == nil {
				t.Error("a machine credential pruned the drift queue; it could cover its own reconnaissance")
			}
		})
	}
}
