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

// WP-G7 piece 3: bulk assignment of the Unowned finding
// (docs/ownership-report-design.md §4, §6). Built on ownership_test.go's
// fixture, the same one piece 1 and piece 2 already use.

// TestBulkAssignOwnershipMovesExactlyTheNamedIDs proves the submission
// contract: only the ids passed move, the rest of the unowned estate is
// untouched, every moved entity gets its OWN change_log row, and all of
// those rows share one batch_id.
func TestBulkAssignOwnershipMovesExactlyTheNamedIDs(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			target := f.team(t, "network-ops", "", strp("netops@example.com"))

			moved1 := f.asset(t, "move-me-1", nil, "")
			moved2 := f.asset(t, "move-me-2", nil, "")
			leftBehind := f.asset(t, "leave-me", nil, "")

			outcomes, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "asset", []string{moved1, moved2}, target)
			if err != nil {
				t.Fatalf("BulkAssignOwnership: %v", err)
			}
			if len(outcomes) != 2 {
				t.Fatalf("got %d outcomes, want 2", len(outcomes))
			}
			for _, o := range outcomes {
				if o.Result != ReassignAssigned {
					t.Errorf("%s: result = %q, want %q (detail: %s)", o.EntityID, o.Result, ReassignAssigned, o.Detail)
				}
			}

			for _, id := range []string{moved1, moved2} {
				a, err := f.s.GetAsset(f.ctx, id)
				if err != nil {
					t.Fatalf("GetAsset(%s): %v", id, err)
				}
				if a.TeamID == nil || *a.TeamID != target {
					t.Errorf("asset %s team_id = %v, want %s", id, a.TeamID, target)
				}
				row := changeLogFor(t, f, "asset", id, domain.ActionUpdate)
				if row.BatchID == nil || *row.BatchID == "" {
					t.Errorf("asset %s: change_log row carries no batch_id", id)
				}
			}
			if bid1, bid2 := changeLogFor(t, f, "asset", moved1, domain.ActionUpdate).BatchID,
				changeLogFor(t, f, "asset", moved2, domain.ActionUpdate).BatchID; bid1 == nil || bid2 == nil || *bid1 != *bid2 {
				t.Errorf("batch ids differ: %v vs %v", bid1, bid2)
			}

			// The asset NOT named in ids stays unowned -- "these two", never
			// "everything on this page" (design §6).
			left, err := f.s.GetAsset(f.ctx, leftBehind)
			if err != nil {
				t.Fatalf("GetAsset(leftBehind): %v", err)
			}
			if left.TeamID != nil {
				t.Errorf("asset %s was moved despite not being in ids: team_id = %v", leftBehind, *left.TeamID)
			}
		})
	}
}

// TestBulkAssignOwnershipAcrossAllFiveTypes proves the mutation reaches every
// owned entity type, not just asset -- the same breadth ReassignTeamOwnership
// is held to.
func TestBulkAssignOwnershipAcrossAllFiveTypes(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			target := f.team(t, "catch-all", "", strp("catch-all@example.com"))

			specs := []struct {
				entityType string
				id         string
			}{
				{"asset", f.asset(t, "u-asset", nil, "")},
				{"service", f.service(t, "u-service", nil, "")},
				{"project", f.project(t, "u-project", nil, "")},
				{"identity", f.identity(t, "u-identity", nil, "")},
				{"custom_field", f.customField(t, "u_field", nil)},
			}
			for _, spec := range specs {
				outcomes, err := f.s.BulkAssignOwnership(f.ctx, testPermit, spec.entityType, []string{spec.id}, target)
				if err != nil {
					t.Fatalf("BulkAssignOwnership(%s): %v", spec.entityType, err)
				}
				if len(outcomes) != 1 || outcomes[0].Result != ReassignAssigned {
					t.Fatalf("%s: outcomes = %+v, want one ReassignAssigned", spec.entityType, outcomes)
				}
			}

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}
			for _, spec := range specs {
				for _, r := range report.Unowned {
					if r.ID == spec.id {
						t.Errorf("%s %s still appears in Unowned after assignment", spec.entityType, spec.id)
					}
				}
			}
		})
	}
}

// TestBulkAssignOwnershipSkipsAnEntityClaimedBySomebodyElse: an id claimed
// (or restored) between the report rendering and this request landing must
// be skipped and reported, never clobbered. Proven by racing the guard
// directly, the same way TestReassignTeamOwnershipSkipsAStaleEntity does.
func TestBulkAssignOwnershipSkipsAnEntityClaimedBySomebodyElse(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			claimedBy := f.team(t, "quick-claim", "", strp("quick@example.com"))
			target := f.team(t, "too-slow", "", strp("slow@example.com"))

			// Shown to the operator as unowned...
			assetID := f.asset(t, "raced-unowned-asset", nil, "")
			// ...but claimed by somebody else before the bulk-assign request
			// lands. A raw update matches "somebody else's own declared-state
			// change ran first", which is what the guard exists to detect --
			// going through the store's own reassignment would itself
			// re-derive its candidate set fresh and simply omit this asset,
			// which proves nothing about the guard.
			if _, err := f.s.db.Writer.ExecContext(f.ctx, f.s.db.Rebind(
				`UPDATE asset SET team_id = ? WHERE id = ?`), claimedBy, assetID); err != nil {
				t.Fatalf("simulating a race: %v", err)
			}

			outcomes, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "asset", []string{assetID}, target)
			if err != nil {
				t.Fatalf("BulkAssignOwnership: %v", err)
			}
			if len(outcomes) != 1 {
				t.Fatalf("got %d outcomes, want 1", len(outcomes))
			}
			if outcomes[0].Result != AssignNoLongerUnowned {
				t.Errorf("result = %q, want %q", outcomes[0].Result, AssignNoLongerUnowned)
			}

			// Not clobbered: still belongs to whoever claimed it first.
			after, err := f.s.GetAsset(f.ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset: %v", err)
			}
			if after.TeamID == nil || *after.TeamID != claimedBy {
				t.Errorf("asset team_id = %v, want it to still be %s", after.TeamID, claimedBy)
			}

			// And no change_log update row was written for a write that
			// changed nothing.
			changes, err := f.s.ListChangesForEntity(f.ctx, "asset", assetID, 50)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			for _, c := range changes {
				if c.Action == domain.ActionUpdate {
					t.Errorf("a stale (no-op) bulk assignment still wrote a change_log update row: %+v", c)
				}
			}
		})
	}
}

// TestBulkAssignOwnershipRefusesARetiredTarget: exactly the same rule
// ReassignTeamOwnership enforces, applied through the shared requireActiveTeam.
func TestBulkAssignOwnershipRefusesARetiredTarget(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			gone := f.team(t, "gone-target-bulk", domain.LifecycleRetired, strp("a@example.com"))
			assetID := f.asset(t, "untouched-by-refused-target", nil, "")

			_, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "asset", []string{assetID}, gone)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}

			asset, err := f.s.GetAsset(f.ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset: %v", err)
			}
			if asset.TeamID != nil {
				t.Error("the asset moved despite the target team being refused")
			}
		})
	}
}

// TestBulkAssignOwnershipRefusesEmptySelection: no ids named is a refusal,
// not a silent no-op batch.
func TestBulkAssignOwnershipRefusesEmptySelection(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			target := f.team(t, "nothing-to-do", "", strp("a@example.com"))

			_, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "asset", nil, target)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}
		})
	}
}

// TestBulkAssignOwnershipRefusesAnUnknownEntityType guards the table-name
// interpolation in assignOneEntity/bestEffortName: entityType must come from
// the fixed allowlist, never from an unvalidated request value reaching a
// query string.
func TestBulkAssignOwnershipRefusesAnUnknownEntityType(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			target := f.team(t, "unknown-type-target", "", strp("a@example.com"))

			_, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "not_a_real_table", []string{"whatever"}, target)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}
		})
	}
}

// TestUnownedAssetCandidatesRespectsTheFilter is the interaction model's own
// correctness surface (design §6): "select-all applies to the CURRENT
// FILTERED VIEW". The candidate list IS what a client-side select-all
// selects, so if this query ever stopped honouring the filter, select-all
// would silently offer every unowned asset regardless of what the operator
// had narrowed to -- the exact wrong-bulk-assignment design §6 exists to
// prevent. Proven end to end: filter to one kind, bulk-assign exactly what
// the filtered candidate list returned, and confirm the other kind never
// moved.
func TestUnownedAssetCandidatesRespectsTheFilter(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			target := f.team(t, "kind-scoped-target", "", strp("a@example.com"))

			router, err := domain.NewAsset(NewID(), domain.KindSwitch, "unowned-switch", nil, f.s.Now())
			if err != nil {
				t.Fatalf("building switch: %v", err)
			}
			if err := f.s.CreateAsset(f.ctx, testPermit, router, []string{f.env}); err != nil {
				t.Fatalf("creating switch: %v", err)
			}
			vmID := f.asset(t, "unowned-vm", nil, "")

			candidates, err := f.s.UnownedAssetCandidates(f.ctx, AssetFilter{Kind: domain.KindSwitch})
			if err != nil {
				t.Fatalf("UnownedAssetCandidates: %v", err)
			}
			var ids []string
			for _, c := range candidates {
				ids = append(ids, c.ID)
				if c.ID == vmID {
					t.Fatalf("the VM (kind=%s) appeared in a kind=%s filtered candidate list", domain.KindVM, domain.KindSwitch)
				}
			}
			if len(ids) != 1 || ids[0] != router.ID {
				t.Fatalf("filtered candidates = %v, want exactly [%s]", ids, router.ID)
			}

			// Assign exactly what the filtered view produced -- this is what
			// a "select all" click sends.
			if _, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "asset", ids, target); err != nil {
				t.Fatalf("BulkAssignOwnership: %v", err)
			}

			moved, err := f.s.GetAsset(f.ctx, router.ID)
			if err != nil {
				t.Fatalf("GetAsset(switch): %v", err)
			}
			if moved.TeamID == nil || *moved.TeamID != target {
				t.Errorf("the filtered (switch) asset did not move: team_id = %v", moved.TeamID)
			}
			untouched, err := f.s.GetAsset(f.ctx, vmID)
			if err != nil {
				t.Fatalf("GetAsset(vm): %v", err)
			}
			if untouched.TeamID != nil {
				t.Errorf("select-all moved an asset OUTSIDE the active filter: vm team_id = %v", untouched.TeamID)
			}
		})
	}
}

// TestUnownedServiceCandidatesRespectsProjectFilter: the service side of the
// same rule, narrowed by project rather than kind -- design §6's own example
// ("these twelve NETWORKING assets", narrowing by project or site).
func TestUnownedServiceCandidatesRespectsProjectFilter(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)

			inScope := f.service(t, "in-scope-svc", nil, "")
			outOfScope := f.service(t, "out-of-scope-svc", nil, "")

			projectID := f.project(t, "scoping-project", nil, "")
			// ServiceFilter.ProjectID filters on OWNERSHIP (services.go's own
			// comment: "matching the column the list renders"), so the link
			// must be 'owns', not 'uses', for this candidate to match.
			link, err := domain.NewProjectServiceLink(projectID, inScope, domain.ProjectOwns, nil, f.s.Now())
			if err != nil {
				t.Fatalf("building project-service link: %v", err)
			}
			if err := f.s.LinkProjectService(f.ctx, testPermit, link); err != nil {
				t.Fatalf("linking service to project: %v", err)
			}

			candidates, err := f.s.UnownedServiceCandidates(f.ctx, ServiceFilter{ProjectID: projectID})
			if err != nil {
				t.Fatalf("UnownedServiceCandidates: %v", err)
			}
			var ids []string
			for _, c := range candidates {
				ids = append(ids, c.ID)
			}
			if len(ids) != 1 || ids[0] != inScope {
				t.Fatalf("filtered candidates = %v, want exactly [%s]", ids, inScope)
			}
			for _, id := range ids {
				if id == outOfScope {
					t.Errorf("a service outside the project filter appeared in the filtered candidate list")
				}
			}
		})
	}
}

// TestUnownedCustomFieldCandidatesFilterByLabel proves the free-text filter
// used by the entity types with no site/project dimension.
func TestUnownedCustomFieldCandidatesFilterByLabel(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			matchID := f.customField(t, "network_owner", nil)
			f.customField(t, "billing_code", nil)

			candidates, err := f.s.UnownedCustomFieldCandidates(f.ctx, "network")
			if err != nil {
				t.Fatalf("UnownedCustomFieldCandidates: %v", err)
			}
			if len(candidates) != 1 || candidates[0].ID != matchID {
				t.Fatalf("candidates = %+v, want exactly the network_owner field", candidates)
			}
		})
	}
}
