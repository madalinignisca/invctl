// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G7 piece 2: the retirement flow offers the fix.
// docs/ownership-report-design.md §5 and §4. Built on ownership_test.go's
// fixture, which already knows how to build a team and one entity of each
// owned kind.

// TestTeamOwnershipCountsSkipsTheWarningWhenEmpty is design §5's opening
// rule: a team that owns nothing must produce Total() == 0, which is what
// the confirmation screen uses to skip straight to a plain confirmation
// rather than render an empty warning.
func TestTeamOwnershipCountsSkipsTheWarningWhenEmpty(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			teamID := f.team(t, "idle-team", "", strp("idle@example.com"))

			counts, err := f.s.TeamOwnershipCounts(f.ctx, teamID)
			if err != nil {
				t.Fatalf("TeamOwnershipCounts: %v", err)
			}
			if counts.Total() != 0 {
				t.Errorf("Total() = %d, want 0 for a team that owns nothing", counts.Total())
			}
		})
	}
}

// TestTeamOwnershipCountsAcrossAllFiveTypes proves counts are correct per
// type and that a retired entity does not inflate them -- the same
// correctness bar TestOwnershipNoContactTeamAppearsOnce already applies to
// the shared query this reuses (teamOwnershipCountColumns).
func TestTeamOwnershipCountsAcrossAllFiveTypes(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			teamID := f.team(t, "busy-team", "", strp("busy@example.com"))

			f.asset(t, "busy-asset-1", &teamID, "")
			f.asset(t, "busy-asset-2", &teamID, "")
			f.service(t, "busy-service", &teamID, "")
			f.project(t, "busy-project", &teamID, "")
			f.identity(t, "busy-identity", &teamID, "")
			f.customField(t, "busy_field", &teamID)

			// A retired asset owned by the same team must not count.
			retiredID := f.asset(t, "busy-asset-retired", &teamID, "")
			if err := f.s.RetireAsset(f.ctx, testPermit, retiredID); err != nil {
				t.Fatalf("retiring asset: %v", err)
			}

			counts, err := f.s.TeamOwnershipCounts(f.ctx, teamID)
			if err != nil {
				t.Fatalf("TeamOwnershipCounts: %v", err)
			}
			if counts.AssetCount != 2 {
				t.Errorf("AssetCount = %d, want 2 (the retired one must not count)", counts.AssetCount)
			}
			if counts.ServiceCount != 1 {
				t.Errorf("ServiceCount = %d, want 1", counts.ServiceCount)
			}
			if counts.ProjectCount != 1 {
				t.Errorf("ProjectCount = %d, want 1", counts.ProjectCount)
			}
			if counts.IdentityCount != 1 {
				t.Errorf("IdentityCount = %d, want 1", counts.IdentityCount)
			}
			if counts.CustomFieldCount != 1 {
				t.Errorf("CustomFieldCount = %d, want 1", counts.CustomFieldCount)
			}
			if counts.Total() != 6 {
				t.Errorf("Total() = %d, want 6", counts.Total())
			}
		})
	}
}

// changeLogFor is a small assertion helper: the single change_log row this
// entity has for the given action, or a test failure if there is not exactly
// one.
func changeLogFor(t *testing.T, f *ownershipFixture, entityType, entityID, action string) domain.ChangeLog {
	t.Helper()
	changes, err := f.s.ListChangesForEntity(f.ctx, entityType, entityID, 50)
	if err != nil {
		t.Fatalf("listing changes for %s %s: %v", entityType, entityID, err)
	}
	var matches []domain.ChangeLog
	for _, c := range changes {
		if c.Action == action {
			matches = append(matches, c)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("%s %s has %d %s change_log rows, want exactly 1: %+v",
			entityType, entityID, len(matches), action, matches)
	}
	return matches[0]
}

// TestReassignTeamOwnershipMovesEverythingAndSharesOneBatchID is the
// correctness core of piece 2's bulk mutation (design §4): every owned
// entity moves, each gets its OWN change_log row (never one row for the
// batch), and every one of those rows carries the SAME batch_id so the set
// can be reconstructed later.
func TestReassignTeamOwnershipMovesEverythingAndSharesOneBatchID(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			from := f.team(t, "outgoing-team", "", strp("outgoing@example.com"))
			to := f.team(t, "incoming-team", "", strp("incoming@example.com"))

			assetID := f.asset(t, "reassign-asset", &from, "")
			serviceID := f.service(t, "reassign-service", &from, "")
			projectID := f.project(t, "reassign-project", &from, "")
			identityID := f.identity(t, "reassign-identity", &from, "")
			fieldID := f.customField(t, "reassign_field", &from)

			outcomes, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if err != nil {
				t.Fatalf("ReassignTeamOwnership: %v", err)
			}
			if len(outcomes) != 5 {
				t.Fatalf("got %d outcomes, want 5 (one per owned entity)", len(outcomes))
			}
			for _, o := range outcomes {
				if o.Result != ReassignAssigned {
					t.Errorf("%s %s: result = %q, want %q", o.EntityType, o.EntityID, o.Result, ReassignAssigned)
				}
			}

			// Every entity actually moved.
			asset, err := f.s.GetAsset(f.ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset: %v", err)
			}
			if asset.TeamID == nil || *asset.TeamID != to {
				t.Errorf("asset team_id = %v, want %s", asset.TeamID, to)
			}
			svc, err := f.s.GetService(f.ctx, serviceID)
			if err != nil {
				t.Fatalf("GetService: %v", err)
			}
			if svc.TeamID == nil || *svc.TeamID != to {
				t.Errorf("service team_id = %v, want %s", svc.TeamID, to)
			}

			// Every entity gets its OWN row -- five distinct entity ids, not
			// one row naming the batch.
			var batchIDs []string
			for _, spec := range []struct{ entityType, id string }{
				{"asset", assetID}, {"service", serviceID}, {"project", projectID},
				{"identity", identityID}, {"custom_field", fieldID},
			} {
				row := changeLogFor(t, f, spec.entityType, spec.id, domain.ActionUpdate)
				if row.BatchID == nil || *row.BatchID == "" {
					t.Errorf("%s %s: change_log row carries no batch_id", spec.entityType, spec.id)
					continue
				}
				batchIDs = append(batchIDs, *row.BatchID)
			}
			if len(batchIDs) != 5 {
				t.Fatalf("only %d of 5 rows carried a batch_id", len(batchIDs))
			}
			for _, b := range batchIDs[1:] {
				if b != batchIDs[0] {
					t.Errorf("batch ids differ across the five rows: %v", batchIDs)
				}
			}

			// And no sixth, "receipt" row exists anywhere: every row this
			// operation wrote names one of the five entities above, never
			// the team or a synthetic batch entity.
			teamChanges, err := f.s.ListChangesForEntity(f.ctx, "team", from, 50)
			if err != nil {
				t.Fatalf("listing changes for team %s: %v", from, err)
			}
			for _, c := range teamChanges {
				if c.BatchID != nil && *c.BatchID == batchIDs[0] {
					t.Error("a change_log row against the TEAM itself carries the reassignment's batch_id; " +
						"the batch must only ever be reconstructed from the five per-entity rows")
				}
			}
		})
	}
}

// TestReassignTeamOwnershipSkipsAStaleEntity exercises the guard directly,
// rather than through ReassignTeamOwnership's own candidate SELECT.
//
// ReassignTeamOwnership computes its candidate set fresh, immediately before
// writing, from the team it was actually asked to move -- so an edit that
// lands BEFORE the call starts simply leaves that entity off the candidate
// list entirely (a different, and equally correct, way of never clobbering
// it). The window this guard exists for is narrower: a concurrent request
// racing between ReassignTeamOwnership's own SELECT and its own UPDATE for
// one entity. reassignEntity is the unexported method that guard lives in,
// and calling it directly is the only way to force exactly that window in a
// single-threaded test: give it an entity whose team_id has already moved
// out from under the guard's WHERE clause, the same shape a real race would
// produce.
func TestReassignTeamOwnershipSkipsAStaleEntity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			from := f.team(t, "stale-source", "", strp("a@example.com"))
			to := f.team(t, "stale-target", "", strp("b@example.com"))
			elsewhere := f.team(t, "stale-elsewhere", "", strp("c@example.com"))

			assetID := f.asset(t, "raced-asset", &elsewhere, "")

			outcome := f.s.reassignEntity(f.ctx, testPermit, "asset", assetID, "raced-asset", from, to, "test-batch",
				func(tx *tx) (sql.Result, error) {
					return tx.exec(f.ctx,
						`UPDATE asset SET team_id = ?, updated_at = ?, row_version = row_version + 1
						 WHERE id = ? AND team_id = ?`,
						to, tx.at, assetID, from)
				})
			if outcome.Result != ReassignStale {
				t.Errorf("result = %q, want %q", outcome.Result, ReassignStale)
			}

			// Not clobbered: it still belongs to whoever held it when the
			// guard's WHERE clause ran, not to either team the call named.
			after, err := f.s.GetAsset(f.ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset: %v", err)
			}
			if after.TeamID == nil || *after.TeamID != elsewhere {
				t.Errorf("asset team_id = %v, want it to still be %s (unchanged by the stale write)", after.TeamID, elsewhere)
			}

			// And no change_log row was written for a write that changed
			// nothing (logUpdate's own rule, reused here: an audit trail
			// full of "nothing happened" entries is worse than one without
			// them).
			changes, err := f.s.ListChangesForEntity(f.ctx, "asset", assetID, 50)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			for _, c := range changes {
				if c.Action == domain.ActionUpdate {
					t.Errorf("a stale (no-op) reassignment still wrote a change_log update row: %+v", c)
				}
			}
		})
	}
}

// TestReassignTeamOwnershipRefusesARetiredTarget: the target team is
// validated once, up front, rather than reported as N separate write
// failures.
func TestReassignTeamOwnershipRefusesARetiredTarget(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			from := f.team(t, "has-stuff", "", strp("a@example.com"))
			to := f.team(t, "gone-target", domain.LifecycleRetired, strp("b@example.com"))

			assetID := f.asset(t, "untouched-asset", &from, "")

			_, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}

			asset, err := f.s.GetAsset(f.ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset: %v", err)
			}
			if asset.TeamID == nil || *asset.TeamID != from {
				t.Error("the asset moved despite the target team being refused")
			}
		})
	}
}

// TestReassignTeamOwnershipRefusesTheSameTeam: reassigning a team to itself
// is refused rather than silently doing nothing.
func TestReassignTeamOwnershipRefusesTheSameTeam(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			teamID := f.team(t, "self-target", "", strp("a@example.com"))

			_, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, teamID, teamID)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}
		})
	}
}

// TestRetireAnywayLeavesOwnershipUntouched: retiring a team WITHOUT
// reassigning first must not move anything, and everything it owned becomes
// a report finding -- proving piece 1 and piece 2 agree with each other.
func TestRetireAnywayLeavesOwnershipUntouched(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			teamID := f.team(t, "retiring-anyway", "", strp("a@example.com"))
			assetID := f.asset(t, "left-behind-asset", &teamID, "")

			if err := f.s.RetireTeam(f.ctx, testPermit, teamID); err != nil {
				t.Fatalf("RetireTeam: %v", err)
			}

			asset, err := f.s.GetAsset(f.ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset: %v", err)
			}
			if asset.TeamID == nil || *asset.TeamID != teamID {
				t.Error("retiring the team without reassigning changed the asset's team_id")
			}

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}
			var found bool
			for _, r := range report.CannotAct {
				if r.ID == assetID {
					found = true
				}
			}
			if !found {
				t.Error("the asset left behind by the retired team did not appear in CannotAct")
			}
		})
	}
}

// TestRetireTeamStillWritesItsOwnRetireRowAfterReassignment: the retirement
// itself keeps its existing shape (design §4 -- "its own existing retire
// row") and carries no batch_id, even when it runs right after a
// reassignment in the same request.
func TestRetireTeamStillWritesItsOwnRetireRowAfterReassignment(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			from := f.team(t, "reassigned-then-retired", "", strp("a@example.com"))
			to := f.team(t, "absorbs-it", "", strp("b@example.com"))
			f.asset(t, "moving-asset", &from, "")

			if _, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to); err != nil {
				t.Fatalf("ReassignTeamOwnership: %v", err)
			}
			if err := f.s.RetireTeam(f.ctx, testPermit, from); err != nil {
				t.Fatalf("RetireTeam: %v", err)
			}

			row := changeLogFor(t, f, "team", from, domain.ActionRetire)
			if row.BatchID != nil {
				t.Errorf("the team's own retire row carries a batch_id (%q); it must keep its own shape", *row.BatchID)
			}
		})
	}
}
