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

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestUpdateRIRCorrectsAndAudits proves the ordinary path: a correction
// succeeds and leaves exactly one change_log row behind it.
func TestUpdateRIRCorrectsAndAudits(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			r, err := domain.NewRIR(NewID(), "ARIN mistyped", false)
			if err != nil {
				t.Fatalf("building rir: %v", err)
			}
			if err := s.CreateRIR(ctx, testPermit, r); err != nil {
				t.Fatalf("creating rir: %v", err)
			}

			got, err := s.GetRIR(ctx, r.ID)
			if err != nil {
				t.Fatalf("getting rir: %v", err)
			}
			got.Name = "ARIN"
			if err := s.UpdateRIR(ctx, testPermit, got); err != nil {
				t.Fatalf("updating rir: %v", err)
			}

			after, err := s.GetRIR(ctx, r.ID)
			if err != nil {
				t.Fatalf("re-reading rir: %v", err)
			}
			if after.Name != "ARIN" {
				t.Errorf("name = %q, want %q", after.Name, "ARIN")
			}

			changes, err := s.ListChangesForEntity(ctx, "rir", r.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 { // create + update
				t.Fatalf("got %d change_log rows, want 2 (create, update)", len(changes))
			}
			if changes[0].Action != domain.ActionUpdate {
				t.Errorf("most recent action = %q, want %q", changes[0].Action, domain.ActionUpdate)
			}
		})
	}
}

// TestUpdateRIRRefusesAStaleToken is domain-level docs/identity-surface-
// design.md's row_version story applied to RIR: two readers holding the same
// row, the slower one's write must be refused rather than silently reverting
// the faster one's.
func TestUpdateRIRRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			r, err := domain.NewRIR(NewID(), "RIPE", false)
			if err != nil {
				t.Fatalf("building rir: %v", err)
			}
			if err := s.CreateRIR(ctx, testPermit, r); err != nil {
				t.Fatalf("creating rir: %v", err)
			}

			first, err := s.GetRIR(ctx, r.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := s.GetRIR(ctx, r.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			first.Name = "RIPE NCC"
			if err := s.UpdateRIR(ctx, testPermit, first); err != nil {
				t.Fatalf("the first write must succeed, or the second is not stale: %v", err)
			}

			second.Name = "RIPE (typo)"
			err = s.UpdateRIR(ctx, testPermit, second)
			if err == nil {
				t.Fatal("the second, stale write succeeded")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Errorf("error = %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestUpdateRIRRefusesAnInvalidValue proves Validate() is actually consulted
// on this path, not just reachable in isolation (registry_test.go covers
// that in the domain package).
func TestUpdateRIRRefusesAnInvalidValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			r, err := domain.NewRIR(NewID(), "APNIC", false)
			if err != nil {
				t.Fatalf("building rir: %v", err)
			}
			if err := s.CreateRIR(ctx, testPermit, r); err != nil {
				t.Fatalf("creating rir: %v", err)
			}

			r.Name = "   "
			err = s.UpdateRIR(ctx, testPermit, r)
			if err == nil {
				t.Fatal("a blank name was accepted")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()["name"]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), "name")
			}
		})
	}
}

// TestUpdateAggregateCorrectsAndAudits.
func TestUpdateAggregateCorrectsAndAudits(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a, err := domain.NewAggregate(NewID(), "10.10.0.0/16")
			if err != nil {
				t.Fatalf("building aggregate: %v", err)
			}
			if err := s.CreateAggregate(ctx, testPermit, a); err != nil {
				t.Fatalf("creating aggregate: %v", err)
			}

			got, err := s.GetAggregate(ctx, a.ID)
			if err != nil {
				t.Fatalf("getting aggregate: %v", err)
			}
			desc := "corrected"
			got.Description = &desc
			if err := s.UpdateAggregate(ctx, testPermit, got); err != nil {
				t.Fatalf("updating aggregate: %v", err)
			}

			after, err := s.GetAggregate(ctx, a.ID)
			if err != nil {
				t.Fatalf("re-reading aggregate: %v", err)
			}
			if after.Description == nil || *after.Description != "corrected" {
				t.Errorf("description = %v, want %q", after.Description, "corrected")
			}
		})
	}
}

// TestUpdateAggregateRefusesAStaleToken.
func TestUpdateAggregateRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a, err := domain.NewAggregate(NewID(), "10.20.0.0/16")
			if err != nil {
				t.Fatalf("building aggregate: %v", err)
			}
			if err := s.CreateAggregate(ctx, testPermit, a); err != nil {
				t.Fatalf("creating aggregate: %v", err)
			}

			first, err := s.GetAggregate(ctx, a.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := s.GetAggregate(ctx, a.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			d1 := "first"
			first.Description = &d1
			if err := s.UpdateAggregate(ctx, testPermit, first); err != nil {
				t.Fatalf("the first write must succeed: %v", err)
			}

			d2 := "second"
			second.Description = &d2
			err = s.UpdateAggregate(ctx, testPermit, second)
			if err == nil {
				t.Fatal("the second, stale write succeeded")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Errorf("error = %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestUpdateAggregateRefusesAnInvalidValue.
func TestUpdateAggregateRefusesAnInvalidValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a, err := domain.NewAggregate(NewID(), "10.30.0.0/16")
			if err != nil {
				t.Fatalf("building aggregate: %v", err)
			}
			if err := s.CreateAggregate(ctx, testPermit, a); err != nil {
				t.Fatalf("creating aggregate: %v", err)
			}

			bogus := "not-a-date"
			a.AllocatedOn = &bogus
			err = s.UpdateAggregate(ctx, testPermit, a)
			if err == nil {
				t.Fatal("an unparseable allocation date was accepted")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()["allocated_on"]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), "allocated_on")
			}
		})
	}
}

// TestUpdateASNCorrectsTheNumberItself is the roadmap entry's own case: "a
// mistyped AS number is withdraw-and-redeclare" IS the gap this closes, so
// the number has to actually be correctable through this path, unlike
// UpdateRoute's pinned foreign keys.
func TestUpdateASNCorrectsTheNumberItself(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a, err := domain.NewASN(NewID(), 65001)
			if err != nil {
				t.Fatalf("building asn: %v", err)
			}
			if err := s.CreateASN(ctx, testPermit, a); err != nil {
				t.Fatalf("creating asn: %v", err)
			}

			got, err := s.GetASN(ctx, a.ID)
			if err != nil {
				t.Fatalf("getting asn: %v", err)
			}
			got.Number = 65002
			if err := s.UpdateASN(ctx, testPermit, got); err != nil {
				t.Fatalf("correcting the AS number: %v", err)
			}

			after, err := s.GetASN(ctx, a.ID)
			if err != nil {
				t.Fatalf("re-reading asn: %v", err)
			}
			if after.Number != 65002 {
				t.Errorf("number = %d, want 65002 -- the whole point of this method is that "+
					"the number itself can be fixed", after.Number)
			}
		})
	}
}

// TestUpdateASNRefusesACollisionAsAValidationError proves the uniqueness
// requirement survives correction, AND that it surfaces as a field-level
// 422-able error rather than a raw domain.ErrConflict a form has nothing to
// hang off of.
func TestUpdateASNRefusesACollisionAsAValidationError(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			taken, err := domain.NewASN(NewID(), 65010)
			if err != nil {
				t.Fatalf("building taken asn: %v", err)
			}
			if err := s.CreateASN(ctx, testPermit, taken); err != nil {
				t.Fatalf("creating taken asn: %v", err)
			}
			mine, err := domain.NewASN(NewID(), 65011)
			if err != nil {
				t.Fatalf("building mine: %v", err)
			}
			if err := s.CreateASN(ctx, testPermit, mine); err != nil {
				t.Fatalf("creating mine: %v", err)
			}

			mine.Number = 65010
			err = s.UpdateASN(ctx, testPermit, mine)
			if err == nil {
				t.Fatal("colliding with a live AS number was accepted")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError so the handler returns "+
					"422 with the field named, not a raw conflict", err, err)
			}
			if _, named := ve.Messages()["number"]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), "number")
			}
		})
	}
}

// TestUpdateASNRefusesAStaleToken.
func TestUpdateASNRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a, err := domain.NewASN(NewID(), 65020)
			if err != nil {
				t.Fatalf("building asn: %v", err)
			}
			if err := s.CreateASN(ctx, testPermit, a); err != nil {
				t.Fatalf("creating asn: %v", err)
			}

			first, err := s.GetASN(ctx, a.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := s.GetASN(ctx, a.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			first.Number = 65021
			if err := s.UpdateASN(ctx, testPermit, first); err != nil {
				t.Fatalf("the first write must succeed: %v", err)
			}

			second.Number = 65022
			err = s.UpdateASN(ctx, testPermit, second)
			if err == nil {
				t.Fatal("the second, stale write succeeded")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Errorf("error = %v, want domain.ErrStale", err)
			}
		})
	}
}
