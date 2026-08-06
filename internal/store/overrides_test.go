// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Tests for docs/AUDIT.md rule 14: an operator may override an observation, the
// override is DECLARED state, and it shadows the reading without mutating it.
//
// Both engines, like everything else in this package.

// overrideSpec builds a well-formed override for the fixture's asset.
func (f *observedFixture) overrideSpec(state, reason string, ttl time.Duration) domain.HealthOverrideSpec {
	return domain.HealthOverrideSpec{
		EntityType:    domain.ObservableAsset,
		EntityID:      f.assetID,
		AssertedState: state,
		Reason:        reason,
		ExpiresAt:     f.clock.Now().Add(ttl),
	}
}

func (f *observedFixture) mustOverride(t *testing.T, spec domain.HealthOverrideSpec) *domain.HealthOverride {
	t.Helper()
	o, err := domain.NewHealthOverride(NewID(), spec, testActor, f.clock.Now())
	if err != nil {
		t.Fatalf("building override: %v", err)
	}
	if err := f.store.CreateHealthOverride(f.ctx, testActor, o); err != nil {
		t.Fatalf("creating override: %v", err)
	}
	return o
}

func (f *observedFixture) changeCount(t *testing.T, entityType string) int {
	t.Helper()
	return f.count(t, `SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, entityType)
}

// TestOverrideShadowsTheObservationAndNeverMutatesIt is the whole rule in one
// test: the asset_health row must come out of the override byte-identical.
//
// The reporter keeps recording the truth underneath, because when the override
// lapses you need to know what actually happened while it was in force. An
// implementation that "helpfully" wrote the asserted state into asset_health
// would pass every other test in this file and destroy exactly that.
func TestOverrideShadowsTheObservationAndNeverMutatesIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			f.record(t, f.report("down", f.clock.Now()))
			before := rowSnapshot(t, f, `SELECT * FROM asset_health WHERE entity_id = ?`, f.assetID)
			transitionsBefore := f.transitions(t)

			f.mustOverride(t, f.overrideSpec("up", "known-bad probe, ticket INC-4102", 2*time.Hour))

			after := rowSnapshot(t, f, `SELECT * FROM asset_health WHERE entity_id = ?`, f.assetID)
			assertRowUnchanged(t, "asset_health", before, after)
			if got := f.transitions(t); got != transitionsBefore {
				t.Errorf("override wrote %d transition row(s); an override is declared state and must write none",
					got-transitionsBefore)
			}

			health, err := f.store.GetEntityHealth(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("getting entity health: %v", err)
			}
			if !health.Overridden() {
				t.Fatal("GetEntityHealth reports no override; the view would show the reading as if nobody had disputed it")
			}
			if got := health.AssertedState(); got != domain.HealthUp {
				t.Errorf("asserted state = %q, want up", got)
			}
			// The reading underneath is still `down`, unshadowed and unaltered.
			if len(health.Readings) != 1 {
				t.Fatalf("got %d readings, want 1", len(health.Readings))
			}
			if got := health.Readings[0].State; got != domain.HealthDown {
				t.Errorf("reading underneath the override = %q, want down: the reporter must keep recording the truth", got)
			}
			if health.Override.Reason == "" {
				t.Error("override has no reason; the reason is most of the value of the row")
			}
		})
	}
}

// TestOverrideExpiresAtReadTimeWithNoWrite: a lapsed override stops applying
// the moment the clock passes it, and nothing is written to make that true.
//
// A sweeper job would be the obvious alternative and it is wrong: a deployment
// whose sweeper is not running would keep silencing an outage indefinitely, and
// the failure would be invisible.
func TestOverrideExpiresAtReadTimeWithNoWrite(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			f.record(t, f.report("down", f.clock.Now()))
			f.mustOverride(t, f.overrideSpec("up", "maintenance window", time.Hour))

			active, err := f.store.ActiveOverride(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil || active == nil {
				t.Fatalf("ActiveOverride before expiry = %v, %v; want a row", active, err)
			}
			changesBefore := f.changeCount(t, "health_override")

			f.clock.Advance(2 * time.Hour)

			active, err = f.store.ActiveOverride(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("ActiveOverride after expiry: %v", err)
			}
			if active != nil {
				t.Fatal("a lapsed override is still shadowing the reading; expiry is evaluated at read time")
			}
			if got := f.changeCount(t, "health_override"); got != changesBefore {
				t.Errorf("expiry wrote %d change_log row(s); lapsing is the passage of time, not an act", got-changesBefore)
			}

			// And the reading underneath is visible again, unchanged.
			health, err := f.store.GetEntityHealth(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("getting entity health: %v", err)
			}
			if health.Overridden() {
				t.Error("EntityHealth still reports an override after it lapsed")
			}
		})
	}
}

// TestOverrideMutationsAreAudited: create, amend and clear each write a
// change_log row, like any other declared mutation.
func TestOverrideMutationsAreAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			o := f.mustOverride(t, f.overrideSpec("up", "known-bad probe", time.Hour))
			if got := f.changeCount(t, "health_override"); got != 1 {
				t.Fatalf("after create: %d change_log rows, want 1", got)
			}

			f.clock.Advance(5 * time.Minute)
			amended, err := f.store.AmendHealthOverride(f.ctx, testActor, o.ID, domain.HealthOverrideSpec{
				AssertedState: "degraded",
				Reason:        "partially recovered, still not trusting the probe",
				ExpiresAt:     f.clock.Now().Add(30 * time.Minute),
			})
			if err != nil {
				t.Fatalf("amending override: %v", err)
			}
			if amended.AssertedState != domain.HealthDegraded {
				t.Errorf("amended state = %q, want degraded", amended.AssertedState)
			}
			if got := f.changeCount(t, "health_override"); got != 2 {
				t.Fatalf("after amend: %d change_log rows, want 2", got)
			}

			f.clock.Advance(time.Minute)
			if err := f.store.ClearHealthOverride(f.ctx, testActor, o.ID); err != nil {
				t.Fatalf("clearing override: %v", err)
			}
			if got := f.changeCount(t, "health_override"); got != 3 {
				t.Fatalf("after clear: %d change_log rows, want 3", got)
			}

			// Soft delete: the row survives, because "who silenced this, and for
			// how long" is read after the fact.
			row, err := f.store.GetHealthOverride(f.ctx, o.ID)
			if err != nil {
				t.Fatalf("getting cleared override: %v", err)
			}
			if row.Lifecycle != domain.OverrideCleared {
				t.Errorf("lifecycle after clear = %q, want %q", row.Lifecycle, domain.OverrideCleared)
			}
			active, err := f.store.ActiveOverride(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil || active != nil {
				t.Fatalf("ActiveOverride after clear = %v, %v; want none", active, err)
			}
		})
	}
}

// TestOverrideRejectsWhatWouldMakeItPermanent covers the two mandatory fields
// and the 24h ceiling, on the constructor and on the amendment path -- a cap
// enforced on create and forgotten on amend is not a cap.
func TestOverrideRejectsWhatWouldMakeItPermanent(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	base := domain.HealthOverrideSpec{
		EntityType:    domain.ObservableAsset,
		EntityID:      "asset-1",
		AssertedState: "up",
		Reason:        "known-bad probe",
		ExpiresAt:     now.Add(time.Hour),
	}

	cases := []struct {
		name    string
		mutate  func(*domain.HealthOverrideSpec)
		actor   domain.Actor
		wantErr bool
	}{
		{name: "well formed", mutate: func(*domain.HealthOverrideSpec) {}, actor: testActor},
		{
			name:    "no reason",
			mutate:  func(s *domain.HealthOverrideSpec) { s.Reason = "  " },
			actor:   testActor,
			wantErr: true,
		},
		{
			name:    "no expiry",
			mutate:  func(s *domain.HealthOverrideSpec) { s.ExpiresAt = time.Time{} },
			actor:   testActor,
			wantErr: true,
		},
		{
			name:    "expiry in the past",
			mutate:  func(s *domain.HealthOverrideSpec) { s.ExpiresAt = now.Add(-time.Minute) },
			actor:   testActor,
			wantErr: true,
		},
		{
			name:    "beyond the 24h ceiling",
			mutate:  func(s *domain.HealthOverrideSpec) { s.ExpiresAt = now.Add(25 * time.Hour) },
			actor:   testActor,
			wantErr: true,
		},
		{
			name:    "unmapped state",
			mutate:  func(s *domain.HealthOverrideSpec) { s.AssertedState = "flapping" },
			actor:   testActor,
			wantErr: true,
		},
		{
			// A compromised monitoring credential that could write an override
			// could silence the estate it is reporting on.
			name:    "machine actor",
			mutate:  func(*domain.HealthOverrideSpec) {},
			actor:   domain.Actor{ID: "monitor:prom-a", Name: "prom-a", Kind: domain.ActorKindAgent},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			tc.mutate(&spec)

			_, err := domain.NewHealthOverride("o-1", spec, tc.actor, now)
			if tc.wantErr {
				if err == nil {
					t.Fatal("NewHealthOverride accepted it; want a validation error")
				}
				if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("error %v does not satisfy errors.Is(ErrInvalid); a handler would return 500 rather than 422", err)
				}
			} else if err != nil {
				t.Fatalf("NewHealthOverride rejected a well-formed spec: %v", err)
			}

			// The same spec through Amend must reach the same verdict, apart
			// from the fields an amendment cannot touch.
			existing, err := domain.NewHealthOverride("o-2", base, testActor, now)
			if err != nil {
				t.Fatalf("building the override to amend: %v", err)
			}
			amendErr := existing.Amend(spec, tc.actor, now)
			if tc.wantErr && amendErr == nil {
				t.Error("Amend accepted what NewHealthOverride rejected; a ceiling enforced only on create is not a ceiling")
			}
			if !tc.wantErr && amendErr != nil {
				t.Errorf("Amend rejected a well-formed spec: %v", amendErr)
			}
		})
	}
}

// TestASecondOverrideConflictsButSupersedesALapsedOne.
//
// The partial unique index keeps one active override per entity, and a lapsed
// row still occupies it because expiry is never swept. Making the operator
// clear it first would mean knowing about an index to write down what they
// know, so the create supersedes -- audited, in the same transaction.
func TestASecondOverrideConflictsButSupersedesALapsedOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			first := f.mustOverride(t, f.overrideSpec("up", "first", time.Hour))

			second, err := domain.NewHealthOverride(NewID(),
				f.overrideSpec("down", "second", time.Hour), testActor, f.clock.Now())
			if err != nil {
				t.Fatalf("building the second override: %v", err)
			}
			err = f.store.CreateHealthOverride(f.ctx, testActor, second)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("second override while one is active = %v, want ErrConflict", err)
			}

			// Once the first has lapsed, the same call succeeds and clears it.
			f.clock.Advance(2 * time.Hour)
			third, err := domain.NewHealthOverride(NewID(),
				f.overrideSpec("down", "after the first lapsed", time.Hour), testActor, f.clock.Now())
			if err != nil {
				t.Fatalf("building the third override: %v", err)
			}
			if err := f.store.CreateHealthOverride(f.ctx, testActor, third); err != nil {
				t.Fatalf("creating an override after the previous one lapsed: %v", err)
			}

			superseded, err := f.store.GetHealthOverride(f.ctx, first.ID)
			if err != nil {
				t.Fatalf("getting the superseded override: %v", err)
			}
			if superseded.Lifecycle != domain.OverrideCleared {
				t.Errorf("lapsed override lifecycle = %q, want %q", superseded.Lifecycle, domain.OverrideCleared)
			}
			// Three rows: create, the supersede, create.
			if got := f.changeCount(t, "health_override"); got != 3 {
				t.Errorf("%d change_log rows, want 3 (create, supersede, create)", got)
			}
		})
	}
}

// TestOverrideOnSomethingThatDoesNotExistIs404.
//
// health_override.entity_id carries no foreign key -- entity_type is
// polymorphic, so no single one could express it -- which means this check has
// to be in Go or not at all.
func TestOverrideOnSomethingThatDoesNotExistIs404(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			spec := f.overrideSpec("up", "typed the wrong id", time.Hour)
			spec.EntityID = NewID()

			o, err := domain.NewHealthOverride(NewID(), spec, testActor, f.clock.Now())
			if err != nil {
				t.Fatalf("building override: %v", err)
			}
			err = f.store.CreateHealthOverride(f.ctx, testActor, o)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("override on an unknown entity = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestBulkHealthCoversEveryRequestedEntity: an unobserved entity gets an entry
// rather than a missing map key. The two render identically in a template and
// mean completely different things.
func TestBulkHealthCoversEveryRequestedEntity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			f.record(t, f.report("degraded", f.clock.Now()))
			f.mustOverride(t, f.overrideSpec("up", "known-bad probe", time.Hour))

			unwatched := NewID()
			health, err := f.store.EntityHealthFor(f.ctx, domain.ObservableAsset,
				[]string{f.assetID, unwatched})
			if err != nil {
				t.Fatalf("bulk health: %v", err)
			}
			if len(health) != 2 {
				t.Fatalf("got %d entries, want 2", len(health))
			}
			if got := health[unwatched]; got.Observed() {
				t.Error("an entity nothing has reported on came back as observed")
			}
			watched := health[f.assetID]
			if !watched.Observed() || !watched.Overridden() {
				t.Fatalf("watched entity: observed=%v overridden=%v, want both true",
					watched.Observed(), watched.Overridden())
			}
			if watched.Readings[0].State != domain.HealthDegraded {
				t.Errorf("reading = %q, want degraded", watched.Readings[0].State)
			}
		})
	}
}
