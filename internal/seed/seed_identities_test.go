// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
)

// THE HARNESS HERE IS internal/seed's OWN, not the store suite's. package
// store's Engines lives in its test files and is not importable, and
// seed_test.go's header records the scoping decision: SQLite only, because the
// seeder writes no SQL of its own -- every statement it issues comes from a
// store method the store suite already runs against both engines. These tests
// therefore use newFixture / eachEngine (internal/seed/seed_test.go:46,69).

// TestTheSeededEstateShowsEveryRotationState is the "two features shipped as
// empty pages" rule, applied before the page exists rather than after somebody
// notices. Four of the five states and a retired credential still named by a
// live dependency, so every pill and all three findings have a row.
func TestTheSeededEstateShowsEveryRotationState(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		rows, err := f.store.ListIdentities(f.ctx, store.IdentityFilter{IncludeRetired: true})
		if err != nil {
			t.Fatalf("listing identities: %v", err)
		}
		if len(rows) == 0 {
			t.Fatal("the fixture seeds no identities at all; this test proves nothing")
		}

		now := f.store.Now()
		seen := map[domain.RotationState]string{}
		for _, r := range rows {
			if r.Lifecycle == domain.LifecycleRetired {
				continue
			}
			seen[r.RotationStatus(now)] = r.Name
		}
		for _, want := range []domain.RotationState{
			domain.RotationUnmanaged,
			domain.RotationNeverRecorded,
			domain.RotationWithinWindow,
			domain.RotationOverdue,
		} {
			if seen[want] == "" {
				t.Errorf("no seeded identity is in state %q, so the demo shows that pill "+
					"-- and any finding built on it -- nowhere. A fresh estate "+
					"demonstrating one of five states is how two features have already "+
					"shipped rendering as empty pages.", want)
			}
		}
		// RotationUnreadable is DELIBERATELY NOT SEEDED: reaching it needs a
		// value the database CHECK accepts and domain.ParseDate rejects, which
		// is a corrupt row, and seeding one would teach every reader of the demo
		// that the estate produces them. It is covered at the unit layer
		// (TestRotationStatus) and the web layer
		// (TestTheIdentityListRendersEveryRotationState).
		if _, ok := seen[domain.RotationUnreadable]; ok {
			t.Errorf("the fixture seeds a credential whose stored date will not parse. " +
				"That is a corrupt row, and a demo estate must not contain one.")
		}

		// A retired credential still named by a live dependency: the third
		// finding, and the "what is stored keeps displaying" rule, both visible
		// on a fresh estate.
		reader := f.store.DB().Reader
		var n int
		if err := reader.Get(&n, reader.Rebind(`
			SELECT COUNT(*) FROM dependency d
			JOIN identity i ON i.id = d.identity_id
			WHERE d.lifecycle <> 'retired' AND i.lifecycle = 'retired'`)); err != nil {
			t.Fatalf("counting live edges on withdrawn credentials: %v", err)
		}
		if n == 0 {
			t.Error("no live dependency names a retired identity, so the finding that " +
				"catches a withdrawn credential still in use has nothing to show, and " +
				"neither does the used-by panel's most interesting case")
		}
	})
}

// TestASeededRotationHasARealChangeLogEntry. At least one rotation goes through
// RecordIdentityRotation rather than the create call, so the demo detail page
// shows a real rotation in its history -- and so the seeder is not the first
// caller tempted to set last_rotated on the struct.
func TestASeededRotationHasARealChangeLogEntry(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		rows, err := f.store.ListIdentities(f.ctx, store.IdentityFilter{IncludeRetired: true})
		if err != nil {
			t.Fatalf("listing identities: %v", err)
		}

		rotated := 0
		for _, r := range rows {
			if r.LastRotated == nil {
				continue
			}
			rotated++
			changes, err := f.store.ListChangesForEntity(f.ctx, "identity", r.ID, 50)
			if err != nil {
				t.Fatalf("reading the audit trail for %s: %v", r.Name, err)
			}
			found := false
			for _, c := range changes {
				if strings.Contains(c.Diff, "last_rotated") {
					found = true
					if c.ActorKind == "" {
						t.Errorf("%s's rotation entry has no actor_kind. Every view "+
							"rendering actor renders actor_kind beside it.", r.Name)
					}
					break
				}
			}
			if !found {
				t.Errorf("%s carries last_rotated = %q and NO change_log entry naming "+
					"last_rotated. The date was written on the struct instead of through "+
					"RecordIdentityRotation, so the demo's detail page shows a rotation "+
					"with no history behind it -- which is the exact failure the "+
					"one-writer rule exists to prevent.", r.Name, *r.LastRotated)
			}
		}
		if rotated == 0 {
			t.Fatal("no seeded identity has a recorded rotation at all, so the demo " +
				"detail page has no rotation history to show and this test is checking " +
				"nothing")
		}
	})
}

// TestTheSeededRotationDatesAreRelativeToTheClock is the mutation-proof for the
// "never literals" rule, and it is the ONLY shape that works. A literal date
// passes on the day it is written and fails months later -- this repo has
// already shipped a test with exactly that defect, and production was unaffected
// while only the calendar exposed it. Loading the estate into a store whose
// clock is years away makes a literal fail IMMEDIATELY.
func TestTheSeededRotationDatesAreRelativeToTheClock(t *testing.T) {
	future := time.Date(2029, 3, 4, 10, 0, 0, 0, time.UTC)

	dsn := "file:" + filepath.Join(t.TempDir(), "seed-future.db")
	db, err := store.Open(store.DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	// WithClock BEFORE Load, so b.now is the future date and every seeded date
	// is derived from it.
	s := store.New(db).WithClock(func() time.Time { return future })
	if _, err := seed.Load(ctx, s); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	rows, err := s.ListIdentities(ctx, store.IdentityFilter{IncludeRetired: true})
	if err != nil {
		t.Fatalf("listing identities: %v", err)
	}
	seen := map[domain.RotationState]bool{}
	for _, r := range rows {
		if r.Lifecycle == domain.LifecycleRetired {
			continue
		}
		seen[r.RotationStatus(future)] = true
	}

	if !seen[domain.RotationWithinWindow] {
		t.Error("no identity is within its window when the estate is seeded in 2029. " +
			"A literal date in b.identityHistory() drifts: the within-window row " +
			"silently becomes overdue some weeks after the fixture was written, and " +
			"the demo stops demonstrating the state it was built for. Dates are " +
			"relative to b.now, the rule lifetimes() already follows.")
	}
	if !seen[domain.RotationOverdue] {
		t.Error("no identity is overdue when the estate is seeded in 2029, so a " +
			"literal date has replaced the b.now-relative one and the Fault finding " +
			"has nothing to show on any demo reset after that date")
	}
}
