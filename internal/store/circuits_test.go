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
	"reflect"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// testPermit is the Administrator decision every store test uses unless a
// test is specifically exercising scoped authorization (see
// TestAPermitThatDoesNotCoverACircuitCannotUpdateIt and its neighbours
// below), the same way testActor stands in for "somebody signed in" for the
// 142 store methods this task does not touch. AdministratorPermit.Covers is
// unconditional, so wrapping testActor here changes nothing about what these
// fixtures were already allowed to do.
var testPermit = domain.AdministratorPermit(testActor)

func mustProvider(t *testing.T, s *SQLStore, ctx context.Context, name string) string {
	t.Helper()
	p, err := domain.NewProvider(NewID(), name)
	if err != nil {
		t.Fatalf("building provider: %v", err)
	}
	if err := s.CreateProvider(ctx, testPermit, p); err != nil {
		t.Fatalf("creating provider: %v", err)
	}
	return p.ID
}

func mustCircuit(t *testing.T, s *SQLStore, ctx context.Context, providerID, cid string, end *string) string {
	t.Helper()
	c, err := domain.NewCircuit(NewID(), cid, providerID)
	if err != nil {
		t.Fatalf("building circuit: %v", err)
	}
	c.ContractEnd = end
	if err := s.CreateCircuit(ctx, testPermit, c); err != nil {
		t.Fatalf("creating circuit: %v", err)
	}
	return c.ID
}

// TestAContractEndReachesTheExpiryReport.
//
// The whole reason this half of WP-E1 shipped first. A contract end is not an
// end of support -- nothing stops working -- but it needs a decision before a
// date, which is the identical question the report already answers for hardware
// and certificates. A second report nobody opens would be worse than a row in
// the one they already do.
func TestAContractEndReachesTheExpiryReport(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			asOf := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
			pid := mustProvider(t, s, ctx, "Telenor")
			soon := domain.FormatDate(asOf.AddDate(0, 2, 0))
			far := domain.FormatDate(asOf.AddDate(5, 0, 0))
			lapsed := domain.FormatDate(asOf.AddDate(0, 0, -30))

			mustCircuit(t, s, ctx, pid, "TN-100-SOON", &soon)
			mustCircuit(t, s, ctx, pid, "TN-200-FAR", &far)
			mustCircuit(t, s, ctx, pid, "TN-300-LAPSED", &lapsed)
			mustCircuit(t, s, ctx, pid, "TN-400-UNDATED", nil)

			report, err := s.Expiring(ctx, asOf, 12)
			if err != nil {
				t.Fatalf("building the report: %v", err)
			}
			found := map[string]string{}
			for _, row := range report.Rows {
				if row.EntityType == "circuit" {
					found[row.Name] = row.State
				}
			}
			if _, ok := found["TN-100-SOON"]; !ok {
				t.Error("a circuit renewing in two months is not in the report; the " +
					"contract would auto-renew at a rate nobody checked")
			}
			if got := found["TN-300-LAPSED"]; got != domain.ExpiryExpired {
				t.Errorf("a contract that ended a month ago reports %q, want %q", got, domain.ExpiryExpired)
			}
			if _, ok := found["TN-200-FAR"]; ok {
				t.Error("a contract ending in five years is inside a twelve-month horizon")
			}
			if _, ok := found["TN-400-UNDATED"]; ok {
				t.Error("a circuit with no contract end was given one")
			}
			// The provider has to be on the row: "TN-100-SOON expires" sends
			// nobody anywhere without knowing who to ring.
			for _, row := range report.Rows {
				if row.EntityType == "circuit" && row.Name == "TN-100-SOON" && row.Kind != "Telenor" {
					t.Errorf("the row names %q where the provider should be", row.Kind)
				}
			}
		})
	}
}

// TestACircuitCostAmortisesToItsContractEnd.
//
// The fourth cost surface reuses the existing machinery, and the one thing that
// genuinely differs is what a one-off spreads over: a circuit has no
// end-of-support, it has a contract, and an install fee spread over anything
// else is a made-up number.
func TestACircuitCostAmortisesToItsContractEnd(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustProvider(t, s, ctx, "Altibox")
			end := "2029-06-30"
			cid := mustCircuit(t, s, ctx, pid, "AB-900", &end)

			from := "2026-01-01"
			cost, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "connectivity", Period: domain.CostMonthly,
				AmountMinor: 89000, ValidFrom: &from,
			}, s.now())
			if err != nil {
				t.Fatalf("building cost: %v", err)
			}
			if err := s.AddCircuitCost(ctx, testActor, cid, cost); err != nil {
				t.Fatalf("adding circuit cost: %v", err)
			}

			rows, err := s.ListCircuitCosts(ctx, cid)
			if err != nil {
				t.Fatalf("listing circuit costs: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d cost rows, want 1", len(rows))
			}
			if rows[0].OwnerEOLDate == nil {
				t.Fatal("the cost has no horizon to amortise over; a circuit's life is " +
					"its contract and the query must read contract_end")
			}
			if *rows[0].OwnerEOLDate != end {
				t.Errorf("amortises to %s, want %s (the contract end)", *rows[0].OwnerEOLDate, end)
			}
		})
	}
}

// TestATerminationLandsExactlyOneEnd, and one side each.
func TestATerminationLandsExactlyOneEnd(t *testing.T) {
	site, port := "a-1", "i-1"
	cases := []struct {
		name    string
		side    string
		asset   *string
		iface   *string
		wantErr bool
	}{
		{"a site", domain.SideA, &site, nil, false},
		{"a port", domain.SideZ, nil, &port, false},
		{"both", domain.SideA, &site, &port, true},
		{"neither", domain.SideA, nil, nil, true},
		{"a side that is not a side", "middle", &site, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewCircuitTermination("t", "c", tc.side, tc.asset, tc.iface)
			if tc.wantErr && err == nil {
				t.Error("accepted, want refused")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

// TestOneSidePerCircuit. A second A end is a contradiction rather than extra
// information, and soft delete would make it permanent.
func TestOneSidePerCircuit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustProvider(t, s, ctx, "GlobalConnect")
			cid := mustCircuit(t, s, ctx, pid, "GC-1", nil)
			site1 := mustAsset(t, s, ctx, domain.KindSite, "dc-oslo", nil)
			site2 := mustAsset(t, s, ctx, domain.KindSite, "colo-fra1", nil)

			t1, _ := domain.NewCircuitTermination(NewID(), cid, domain.SideA, &site1, nil)
			if err := s.CreateCircuitTermination(ctx, testPermit, t1); err != nil {
				t.Fatalf("landing the A end: %v", err)
			}
			t2, _ := domain.NewCircuitTermination(NewID(), cid, domain.SideA, &site2, nil)
			if err := s.CreateCircuitTermination(ctx, testPermit, t2); err == nil {
				t.Error("a circuit was given two A ends")
			}

			// The Z end is fine, and now both ends are recorded.
			t3, _ := domain.NewCircuitTermination(NewID(), cid, domain.SideZ, &site2, nil)
			if err := s.CreateCircuitTermination(ctx, testPermit, t3); err != nil {
				t.Fatalf("landing the Z end: %v", err)
			}
			rows, err := s.ListCircuits(ctx)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, r := range rows {
				if r.ID == cid && !r.Landed() {
					t.Errorf("a circuit with both ends recorded reports %d terminations",
						r.Terminations)
				}
			}
		})
	}
}

// TestAContractCannotEndBeforeItWasInstalled.
func TestAContractCannotEndBeforeItWasInstalled(t *testing.T) {
	c, err := domain.NewCircuit("c", "X-1", "p")
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	install, end := "2026-01-01", "2025-01-01"
	c.InstallDate, c.ContractEnd = &install, &end
	if err := c.Validate(); err == nil {
		t.Fatal("a contract ending before installation was accepted; it renders as two " +
			"plausible dates in different columns")
	} else if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

// ---------------------------------------------------------------------------
// WP-G1 Task 7 -- proving domain.Permit on circuits.go's six transactions
// ---------------------------------------------------------------------------

// TestAPermitThatDoesNotCoverACircuitCannotUpdateIt.
//
// A ScopedPermit that covers circuit A must not authorize a write to circuit
// B, even though both are the same entity TYPE and the permit's entities set
// is non-empty. This is the whole reason Covers takes an entityID and not
// just an entityType: a project owner scoped to their own project's circuit
// must not incidentally reach every OTHER circuit in the estate merely
// because circuit is a type they are sometimes allowed to touch.
func TestAPermitThatDoesNotCoverACircuitCannotUpdateIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustProvider(t, s, ctx, "Telia")
			aID := mustCircuit(t, s, ctx, pid, "A-1", nil)
			bID := mustCircuit(t, s, ctx, pid, "B-1", nil)

			scoped := domain.ScopedPermit(
				domain.Actor{ID: "po-1", Name: "po-1", Kind: domain.ActorKindUser},
				[]string{"proj-1"},
				domain.ScopedEntities{"circuit": {aID: true}},
			)

			b, err := s.GetCircuit(ctx, bID)
			if err != nil {
				t.Fatalf("reading circuit B: %v", err)
			}
			b.Description = strPtr("renamed by an intruder")
			if err := s.UpdateCircuit(ctx, scoped, b); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateCircuit(scoped-to-A, B) error = %v, want ErrForbidden", err)
			}

			// Covering A itself must still work -- a permit that refuses
			// everything would pass the test above for the wrong reason.
			a, err := s.GetCircuit(ctx, aID)
			if err != nil {
				t.Fatalf("reading circuit A: %v", err)
			}
			a.Description = strPtr("renamed by its own project owner")
			if err := s.UpdateCircuit(ctx, scoped, a); err != nil {
				t.Fatalf("UpdateCircuit(scoped-to-A, A) = %v, want nil", err)
			}
		})
	}
}

// TestARefusedWriteLeavesTheRowUnchangedAndWritesNoAuditRow is the important
// one. The guard lives inside the transaction (tx.log, called from inside
// s.write's fn), so a refusal must roll the whole write back -- not merely
// return an error after the UPDATE already committed. This test proves the
// ROLLBACK, not just the error: it compares the row byte-for-byte and counts
// change_log rows, because a guard that fired after the UPDATE would still
// make this test's error-checking half pass while leaving the database and
// the audit trail disagreeing with each other.
func TestARefusedWriteLeavesTheRowUnchangedAndWritesNoAuditRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustProvider(t, s, ctx, "Broadnet")
			otherID := mustCircuit(t, s, ctx, pid, "OTHER-1", nil)
			targetID := mustCircuit(t, s, ctx, pid, "TARGET-1", nil)

			before, err := s.GetCircuit(ctx, targetID)
			if err != nil {
				t.Fatalf("reading before: %v", err)
			}
			beforeCount, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"circuit", targetID)
			if err != nil {
				t.Fatalf("counting change_log before: %v", err)
			}

			scoped := domain.ScopedPermit(
				domain.Actor{ID: "po-2", Name: "po-2", Kind: domain.ActorKindUser},
				[]string{"proj-2"},
				domain.ScopedEntities{"circuit": {otherID: true}},
			)

			attempt, err := s.GetCircuit(ctx, targetID)
			if err != nil {
				t.Fatalf("reading target: %v", err)
			}
			attempt.Description = strPtr("this must never land")
			attempt.CommitMbps = intPtr(9999)
			err = s.UpdateCircuit(ctx, scoped, attempt)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateCircuit error = %v, want ErrForbidden", err)
			}

			after, err := s.GetCircuit(ctx, targetID)
			if err != nil {
				t.Fatalf("reading after: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the row changed despite a refused write.\nbefore: %+v\nafter:  %+v",
					before, after)
			}

			afterCount, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"circuit", targetID)
			if err != nil {
				t.Fatalf("counting change_log after: %v", err)
			}
			if afterCount != beforeCount {
				t.Fatalf("change_log gained %d row(s) for a write that was refused; "+
					"the guard fired after something already committed, which is worse "+
					"than no guard at all because the audit trail now disagrees with "+
					"the database", afterCount-beforeCount)
			}
		})
	}
}

// TestANoOpUpdateIsStillAuthorized closes the bypass an authorization review
// found: logUpdateBatch used to check `if !changed { return nil }` BEFORE
// ever reaching tx.log's Covers check, so resubmitting a row's CURRENT
// values -- a diff of nothing -- skipped authorization entirely while the
// unconditional `UPDATE ... row_version = row_version + 1` earlier in the
// same transaction had already run. A ScopedPermit covering nothing could
// still bump row_version and move updated_at on an entity it does not own,
// with no change_log row naming who did it.
//
// Unlike TestARefusedWriteLeavesTheRowUnchangedAndWritesNoAuditRow, which
// submits CHANGED values, this submits the row's OWN current values back
// unmodified -- diffJSON reports no change, which is exactly the condition
// that let the old code path skip past authorize() unnoticed.
func TestANoOpUpdateIsStillAuthorized(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustProvider(t, s, ctx, "Broadnet")
			otherID := mustCircuit(t, s, ctx, pid, "OTHER-1", nil)
			targetID := mustCircuit(t, s, ctx, pid, "TARGET-1", nil)

			before, err := s.GetCircuit(ctx, targetID)
			if err != nil {
				t.Fatalf("reading before: %v", err)
			}
			beforeCount, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"circuit", targetID)
			if err != nil {
				t.Fatalf("counting change_log before: %v", err)
			}

			// Covers nothing -- otherID is a decoy, targetID is deliberately
			// absent from the scope.
			scoped := domain.ScopedPermit(
				domain.Actor{ID: "po-3", Name: "po-3", Kind: domain.ActorKindUser},
				[]string{"proj-3"},
				domain.ScopedEntities{"circuit": {otherID: true}},
			)

			// Resubmit the row's OWN current values, unmodified -- the no-op
			// case the bug depended on.
			resubmit, err := s.GetCircuit(ctx, targetID)
			if err != nil {
				t.Fatalf("reading target: %v", err)
			}
			err = s.UpdateCircuit(ctx, scoped, resubmit)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateCircuit(no-op) error = %v, want ErrForbidden", err)
			}

			after, err := s.GetCircuit(ctx, targetID)
			if err != nil {
				t.Fatalf("reading after: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the row changed despite a refused no-op write.\nbefore: %+v\nafter:  %+v",
					before, after)
			}
			if before.RowVersion != after.RowVersion {
				t.Fatalf("row_version moved from %d to %d on a refused write",
					before.RowVersion, after.RowVersion)
			}
			if derefString(before.UpdatedAt) != derefString(after.UpdatedAt) {
				t.Fatalf("updated_at moved from %v to %v on a refused write",
					derefString(before.UpdatedAt), derefString(after.UpdatedAt))
			}

			afterCount, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"circuit", targetID)
			if err != nil {
				t.Fatalf("counting change_log after: %v", err)
			}
			if afterCount != beforeCount {
				t.Fatalf("change_log gained %d row(s) for a no-op write that was refused",
					afterCount-beforeCount)
			}
		})
	}
}

// TestAnAdministratorPermitCoversEveryCircuit -- or the mechanism is a denial
// of service on the people who are supposed to have access. An
// Administrator's write must not start failing merely because a circuit's
// scope classification, or a future entity type, was never taught to a
// ScopedPermit's narrower rule.
func TestAnAdministratorPermitCoversEveryCircuit(t *testing.T) {
	admin := domain.AdministratorPermit(domain.Actor{ID: "root", Name: "root", Kind: domain.ActorKindUser})

	for _, id := range []string{"any-circuit-id", "", "does-not-exist"} {
		if !admin.Covers("circuit", id) {
			t.Errorf("AdministratorPermit.Covers(%q, %q) = false, want true unconditionally", "circuit", id)
		}
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			pid := mustProvider(t, s, ctx, "GEANT")
			cid := mustCircuit(t, s, ctx, pid, "GEANT-1", nil)

			c, err := s.GetCircuit(ctx, cid)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			c.Description = strPtr("an administrator may always do this")
			if err := s.UpdateCircuit(ctx, admin, c); err != nil {
				t.Fatalf("UpdateCircuit(admin, ...) = %v, want nil", err)
			}
		})
	}
}

// TestAPermitIsUnchangedByARolledBackTransaction forces writeSerializable's
// retry loop to discard a transaction and start over, and asserts the
// permit's Covers answers -- and the permit value itself, field for field --
// are identical before and after. Cheap, and it is what keeps
// scopedPermit's "carries no mutable state" claim honest if somebody later
// adds a field: see that type's doc comment in internal/domain/role.go for
// why the state was removed rather than merely reset correctly.
//
// The abort is fabricated rather than genuinely raced: writeSerializable's
// retry loop only inspects isSerializationFailure(err), so a fn that returns
// an error matching that check exercises the exact same retry path a real
// SQLSTATE 40001 would, deterministically and on both engines, without
// standing up two interleaved transactions to prove a property that has
// nothing to do with the database's own concurrency control.
func TestAPermitIsUnchangedByARolledBackTransaction(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-3", Name: "po-3", Kind: domain.ActorKindUser},
				[]string{"proj-3"},
				domain.ScopedEntities{"circuit": {"c-1": true, "c-2": true}},
			)
			before := reflect.ValueOf(permit).Elem().Interface()

			attempts := 0
			var coversAcrossAttempts []bool
			err := s.writeSerializable(ctx, permit, func(t *tx) error {
				attempts++
				coversAcrossAttempts = append(coversAcrossAttempts,
					permit.Covers("circuit", "c-1"), permit.Covers("circuit", "c-2"),
					permit.Covers("circuit", "c-3"))
				if attempts < 3 {
					return errors.New("could not serialize access due to concurrent update")
				}
				return nil
			})
			if err != nil {
				t.Fatalf("writeSerializable: %v", err)
			}
			if attempts != 3 {
				t.Fatalf("fn ran %d times, want exactly 3 (2 forced aborts then a success)", attempts)
			}

			for i, want := range []bool{true, true, false} {
				for attempt := 0; attempt < 3; attempt++ {
					if got := coversAcrossAttempts[attempt*3+i]; got != want {
						t.Errorf("attempt %d, entity %d: Covers = %v, want %v (unchanged across retries)",
							attempt+1, i, got, want)
					}
				}
			}

			after := reflect.ValueOf(permit).Elem().Interface()
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the permit itself changed across a rolled-back/retried transaction.\n"+
					"before: %+v\nafter:  %+v\n"+
					"scopedPermit must carry no mutable state -- see its doc comment.", before, after)
			}
		})
	}
}
