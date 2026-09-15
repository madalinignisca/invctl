// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestTheIdentityDateShapeCheckIsEnforcedByBothEngines is 00069's second
// statement, asserted rather than assumed. The Go constructor is the first line
// of defence and this is the second; a shape check that silently does nothing on
// one engine is the portability failure this suite exists for.
func TestTheIdentityDateShapeCheckIsEnforcedByBothEngines(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			identity, err := domain.NewIdentity(NewID(), domain.IdentitySpec{
				Kind: domain.IdentityServiceAccount, Name: "svc-shape",
			})
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			if err := s.CreateIdentity(ctx, testPermit, identity); err != nil {
				t.Fatalf("creating identity: %v", err)
			}
			for _, bad := range []string{"2026-9-8", "08/09/2026", "2026-09-08T00:00:00Z", ""} {
				_, err := s.db.Writer.ExecContext(ctx,
					s.db.Writer.Rebind(`UPDATE identity SET last_rotated = ? WHERE id = ?`),
					bad, identity.ID)
				if err == nil {
					t.Errorf("the database accepted last_rotated = %q; the shape CHECK is not "+
						"enforcing on %s", bad, e.Name)
				}
			}
			if _, err := s.db.Writer.ExecContext(ctx,
				s.db.Writer.Rebind(`UPDATE identity SET last_rotated = ? WHERE id = ?`),
				"2026-09-08", identity.ID); err != nil {
				t.Errorf("the database refused a well-formed date: %v", err)
			}
		})
	}
}

// TestAnIdentityIsCreatedWithNoRecordedRotation pins the create half of the
// one-writer rule behaviourally, beside the AST scan that pins it structurally
// (last_rotated_source_test.go, Task 3). Declaring a credential and recording
// when it was last rotated are two acts.
func TestAnIdentityIsCreatedWithNoRecordedRotation(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			identity, err := domain.NewIdentity(NewID(), domain.IdentitySpec{
				Kind: domain.IdentityServiceAccount, Name: "svc-fresh",
			})
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			identity.LastRotated = strPtr("2020-01-01") // deliberately set, deliberately ignored
			if err := s.CreateIdentity(ctx, testPermit, identity); err != nil {
				t.Fatalf("creating identity: %v", err)
			}
			var stored *string
			if err := s.readOne(ctx, &stored,
				`SELECT last_rotated FROM identity WHERE id = ?`, identity.ID); err != nil {
				t.Fatalf("reading it back: %v", err)
			}
			if stored != nil {
				t.Errorf("last_rotated = %q after a create; create must never write it", *stored)
			}
			var version int
			if err := s.readOne(ctx, &version,
				`SELECT row_version FROM identity WHERE id = ?`, identity.ID); err != nil {
				t.Fatalf("reading row_version: %v", err)
			}
			if version != 1 {
				t.Errorf("row_version = %d after a create, want 1", version)
			}
		})
	}
}

// TestBulkOwnershipBumpsIdentityRowVersion is the failure 00069 exists to
// prevent, from the other direction: a bulk assignment moving team_id under an
// open correction form whose token still validates, so the form's save silently
// reverts the assignment.
func TestBulkOwnershipBumpsIdentityRowVersion(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			team := f.team(t, "bump-team", "", strp("bump@example.com"))
			id := f.identity(t, "bump-identity", nil, "")

			before := f.rowVersion(t, id)
			if _, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "identity",
				[]string{id}, team); err != nil {
				t.Fatalf("BulkAssignOwnership: %v", err)
			}
			if after := f.rowVersion(t, id); after != before+1 {
				t.Errorf("row_version = %d after a bulk assignment, want %d. An open "+
					"correction form's token still validates, so its save silently "+
					"reverts the assignment.", after, before+1)
			}

			// The guard is unchanged: a second assignment of a now-owned row is
			// skipped and reported, never 409'd, and does not bump again.
			mid := f.rowVersion(t, id)
			other := f.team(t, "bump-team-2", "", strp("bump2@example.com"))
			outcomes, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "identity",
				[]string{id}, other)
			if err != nil {
				t.Fatalf("second BulkAssignOwnership: %v", err)
			}
			if len(outcomes) != 1 || outcomes[0].Result != AssignNoLongerUnowned {
				t.Errorf("outcome = %+v, want one %s -- the WHERE team_id IS NULL "+
					"guard is still the whole eligibility check", outcomes, AssignNoLongerUnowned)
			}
			if after := f.rowVersion(t, id); after != mid {
				t.Errorf("row_version moved on a skipped assignment: %d -> %d", mid, after)
			}
		})
	}
}

// TestReassignTeamOwnershipBumpsIdentityRowVersion is the same property for the
// team-retirement path, which uses WHERE team_id = ? rather than IS NULL.
func TestReassignTeamOwnershipBumpsIdentityRowVersion(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			from := f.team(t, "from-team", "", strp("from@example.com"))
			to := f.team(t, "to-team", "", strp("to@example.com"))
			id := f.identity(t, "reassigned-identity", &from, "")

			before := f.rowVersion(t, id)
			outcomes, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if err != nil {
				t.Fatalf("ReassignTeamOwnership: %v", err)
			}
			if len(outcomes) == 0 {
				t.Fatal("no outcomes at all; the reassignment found nothing to move and " +
					"the assertion below would pass on an untouched row")
			}
			if after := f.rowVersion(t, id); after != before+1 {
				t.Errorf("row_version = %d after a team reassignment, want %d. The guard "+
					"here is WHERE team_id = fromTeamID and stays that way -- the bump is "+
					"additive, so an open correction form's token stops validating once "+
					"the row moved underneath it.", after, before+1)
			}
			// The guard is untouched: reassigning again from the OLD team finds
			// nothing and reports it, rather than 409'ing or bumping.
			mid := f.rowVersion(t, id)
			again, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if err != nil {
				t.Fatalf("second ReassignTeamOwnership: %v", err)
			}
			for _, o := range again {
				if o.EntityType == "identity" && o.Result != ReassignStale {
					t.Errorf("outcome = %+v, want ReassignStale -- the WHERE team_id = ? "+
						"guard is still the whole eligibility check", o)
				}
			}
			if after := f.rowVersion(t, id); after != mid {
				t.Errorf("row_version moved %d -> %d on a reassignment that matched nothing",
					mid, after)
			}
		})
	}
}
