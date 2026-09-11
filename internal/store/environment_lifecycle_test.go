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

// An environment can be withdrawn (migration 00065), and unlike interface and
// prefix the design rests on the OPPOSITE invariant: a retired environment is
// NEVER made bare. It is a label, not an occupancy, and the write-surface
// census carried it as a decision deferred: "the answer today is that nobody
// can, which is not the same as having decided." RetireEnvironment is that
// decision, and every test below is checking that it refuses nothing and
// rewrites nothing that already carries the label.

// TestRetiringAnEnvironmentTouchesNothingItLabels drives the refusal
// RetireInterface has and RetireEnvironment deliberately does not.
func TestRetiringAnEnvironmentTouchesNothingItLabels(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			envID := mustEnvironment(t, s, ctx, "prod-lc", domain.EnvRoleProduction)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-lc", nil, envID)

			if err := s.RetireEnvironment(ctx, testPermit, envID); err != nil {
				t.Fatalf("retiring a plain environment: %v", err)
			}

			env, err := s.GetEnvironment(ctx, envID)
			if err != nil {
				t.Fatalf("reading environment: %v", err)
			}
			if env.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q, want retired", env.Lifecycle)
			}

			// The membership row is untouched: asset_environment still names
			// this environment, exactly as it did before the withdrawal. If
			// RetireEnvironment ever starts cascading, this is the first
			// place that would go quiet.
			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("reading asset: %v", err)
			}
			found := false
			for _, m := range asset.Environments {
				if m.ID == envID {
					found = true
				}
			}
			if !found {
				t.Error("the asset no longer carries the retired environment -- " +
					"RetireEnvironment must not cascade into asset_environment")
			}
		})
	}
}

// TestRetiringAnEnvironmentTwiceLogsOneWithdrawal. A second audit entry would
// claim a withdrawal that did not happen -- the same guard RetireInterface and
// RetireTeam both carry.
func TestRetiringAnEnvironmentTwiceLogsOneWithdrawal(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			envID := mustEnvironment(t, s, ctx, "twice-lc", domain.EnvRoleDev)

			if err := s.RetireEnvironment(ctx, testPermit, envID); err != nil {
				t.Fatalf("first retire: %v", err)
			}
			before := countChangeLog(t, s, ctx, envID)
			if err := s.RetireEnvironment(ctx, testPermit, envID); err != nil {
				t.Fatalf("second retire: %v", err)
			}
			if after := countChangeLog(t, s, ctx, envID); after != before {
				t.Errorf("a second retire wrote another change_log row (%d -> %d), "+
					"claiming a withdrawal that did not happen", before, after)
			}
		})
	}
}

// TestListEnvironmentsExcludesRetiredByDefault: a picker built from the zero
// value filter must not offer a withdrawn label to a NEW assignment, while
// asking for it explicitly still returns it -- the environment list page
// itself, and any form that must keep a stored assignment visible, need that.
func TestListEnvironmentsExcludesRetiredByDefault(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			liveID := mustEnvironment(t, s, ctx, "live-lc", domain.EnvRoleProduction)
			retiredID := mustEnvironment(t, s, ctx, "gone-lc", domain.EnvRoleDev)
			if err := s.RetireEnvironment(ctx, testPermit, retiredID); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			picker, err := s.ListEnvironments(ctx, EnvironmentFilter{})
			if err != nil {
				t.Fatalf("listing for a picker: %v", err)
			}
			for _, env := range picker {
				if env.ID == retiredID {
					t.Error("the default (picker) filter offered a retired environment")
				}
			}
			var sawLive bool
			for _, env := range picker {
				if env.ID == liveID {
					sawLive = true
				}
			}
			if !sawLive {
				t.Error("the default filter dropped a LIVE environment -- it must exclude " +
					"only retired ones")
			}

			display, err := s.ListEnvironments(ctx, EnvironmentFilter{IncludeRetired: true})
			if err != nil {
				t.Fatalf("listing for a display: %v", err)
			}
			var sawRetired bool
			for _, env := range display {
				if env.ID == retiredID {
					sawRetired = true
				}
			}
			if !sawRetired {
				t.Error("IncludeRetired:true dropped a retired environment -- " +
					"what is stored must keep displaying")
			}
		})
	}
}

// TestAssetEnvironmentSurvivesAnUnrelatedAssetUpdateAfterRetirement is THE
// TRAP this task exists to close, at the layer that actually proves it:
// asset_environment is a set REPLACED WHOLESALE on every save
// (setAssetEnvironments), so a save that omits a currently-assigned
// environment from environmentIDs drops it -- silently, the identical shape
// as the setDataClasses bug that shipped once already (nil read as "leave
// alone", unticking every box sent no key). A retired-but-assigned
// environment must round-trip through a save that changes something else
// entirely, because it renders as a ticked, ENABLED checkbox
// (unionAssignedEnvironments, internal/web/handlers/forms.go) and the browser
// therefore submits it exactly like every other ticked box.
func TestAssetEnvironmentSurvivesAnUnrelatedAssetUpdateAfterRetirement(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			envID := mustEnvironment(t, s, ctx, "trap-lc", domain.EnvRoleProduction)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-trap", nil, envID)

			if err := s.RetireEnvironment(ctx, testPermit, envID); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("reading asset: %v", err)
			}
			updated := asset.Asset
			updated.Name = "srv-trap-renamed" // the unrelated field being corrected

			// environmentIDs carries the retired environment's id, exactly as
			// a form rendering it ticked-and-enabled would submit -- not nil
			// (which the store reads as "not managing membership") and not
			// empty (which would clear it).
			if err := s.UpdateAsset(ctx, testPermit, &updated, []string{envID}); err != nil {
				t.Fatalf("updating an unrelated field: %v", err)
			}

			after, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("reading asset after update: %v", err)
			}
			if after.Name != "srv-trap-renamed" {
				t.Fatalf("the unrelated field did not save: name = %q", after.Name)
			}
			found := false
			for _, m := range after.Environments {
				if m.ID == envID {
					found = true
				}
			}
			if !found {
				t.Error("the retired environment's assignment was dropped by an unrelated " +
					"field's save -- this is the setDataClasses trap, shipped again")
			}
		})
	}
}

// TestReDeclaringARetiredEnvironmentReactivatesItByCode. environment.code
// carries a TABLE unique constraint, the identical situation CreateInterface
// documents for (asset_id, name): a retired code stays taken, so re-declaring
// it must bring the SAME row back rather than fail or fork a second "staging".
func TestReDeclaringARetiredEnvironmentReactivatesItByCode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			original := mustEnvironment(t, s, ctx, "staging", domain.EnvRoleStaging)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-react", nil, original)

			if err := s.RetireEnvironment(ctx, testPermit, original); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			replacement, err := domain.NewEnvironment(NewID(), "staging", "Staging (rebuilt)",
				domain.EnvRoleStaging, true, 4, s.Now())
			if err != nil {
				t.Fatalf("building the redeclaration: %v", err)
			}
			if err := s.CreateEnvironment(ctx, testPermit, replacement); err != nil {
				t.Fatalf("redeclaring staging: %v", err)
			}

			if replacement.ID != original {
				t.Fatalf("the environment came back as a new row (%s, was %s); it must "+
					"come back as ITSELF so the six tables pointing at it stay pointed "+
					"at a live row instead of a retired one beside a new impostor",
					replacement.ID, original)
			}
			got, err := s.GetEnvironment(ctx, original)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if got.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q after redeclaring, want active", got.Lifecycle)
			}
			if got.Name != "Staging (rebuilt)" {
				t.Errorf("name = %q, want the redeclaration's own attributes to have taken", got.Name)
			}

			// The asset that was pointing at the retired row is now pointing
			// at a LIVE one, with no action of its own -- exactly the payoff
			// reactivation exists for.
			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("reading asset: %v", err)
			}
			for _, m := range asset.Environments {
				if m.ID == original && m.Lifecycle != domain.LifecycleActive {
					t.Errorf("the asset's own membership still reads lifecycle=%q", m.Lifecycle)
				}
			}
		})
	}
}

// TestReDeclaringALiveEnvironmentIsStillRefused. Reactivation applies to
// RETIRED rows only; a duplicate live code is the conflict it always was.
func TestReDeclaringALiveEnvironmentIsStillRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mustEnvironment(t, s, ctx, "dup-lc", domain.EnvRoleProduction)

			dup, err := domain.NewEnvironment(NewID(), "dup-lc", "Duplicate",
				domain.EnvRoleProduction, true, 3, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateEnvironment(ctx, testPermit, dup); err == nil {
				t.Error("a second live environment with the same code was accepted")
			}
		})
	}
}

// TestARetiredEnvironmentIsNotNewlySelectableButKeepsRoundTripping is the
// enforcement half of the rule, and it lives here rather than in a handler
// test on purpose.
//
// docs/AUDIT.md: "what is STORED must keep displaying, what is RETIRED must not
// be newly selectable." The displaying half is the templates' job. This half
// cannot be, for two reasons:
//
//   - The prefix, VLAN and service pickers are SELECT elements on list pages
//     that render ONE shared option list across every row's inline edit.
//     Dropping retired options there silently reassigns any row whose stored
//     value just vanished from the list -- a worse bug than the one being
//     fixed, and the same shape as the setDataClasses nil/empty collision where
//     unticking the last data class silently restored it.
//   - A rule enforced only in a template is not enforced. The read-only API and
//     the importer reach these same store methods without rendering anything.
//
// The two halves of the assertion pull against each other, which is why both
// are here: refusing a retired environment outright would make an entity that
// already carries one uneditable, forcing a relabel before any unrelated fix.
func TestARetiredEnvironmentIsNotNewlySelectableButKeepsRoundTripping(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			live := mustEnvironment(t, s, ctx, "live-env", domain.EnvRoleProduction)
			dead := mustEnvironment(t, s, ctx, "dead-env", domain.EnvRoleStaging)

			// Labelled BEFORE withdrawal, which is the only way to get here.
			labelled := mustPrefix(t, s, ctx, "10.95.0.0/24")
			stored, err := s.GetPrefix(ctx, labelled)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			stored.EnvironmentID = &dead
			if err := s.UpdatePrefix(ctx, testPermit, stored); err != nil {
				t.Fatalf("labelling while still live: %v", err)
			}
			if err := s.RetireEnvironment(ctx, testPermit, dead); err != nil {
				t.Fatalf("withdrawing the environment: %v", err)
			}

			// ROUND TRIP: an unrelated edit must still go through, carrying the
			// withdrawn label untouched.
			stored, err = s.GetPrefix(ctx, labelled)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			role := "container"
			stored.Role = &role
			if err := s.UpdatePrefix(ctx, testPermit, stored); err != nil {
				t.Fatalf("editing a prefix that carries a withdrawn environment: %v\n"+
					"An entity labelled before the withdrawal must stay editable, or "+
					"withdrawing an environment freezes everything wearing it.", err)
			}
			var got string
			if err := s.DB().Reader.Get(&got, s.DB().Reader.Rebind(
				`SELECT COALESCE(environment_id, '') FROM prefix WHERE id = ?`), labelled); err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if got != dead {
				t.Errorf("environment_id = %q after an unrelated edit, want the withdrawn "+
					"one kept: the stored label was dropped on save", got)
			}

			// NOT NEWLY SELECTABLE: moving a different prefix ONTO it is refused.
			fresh := mustPrefix(t, s, ctx, "10.96.0.0/24")
			other, err := s.GetPrefix(ctx, fresh)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			other.EnvironmentID = &live
			if err := s.UpdatePrefix(ctx, testPermit, other); err != nil {
				t.Fatalf("labelling with a live environment: %v", err)
			}
			other, err = s.GetPrefix(ctx, fresh)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			other.EnvironmentID = &dead
			if err := s.UpdatePrefix(ctx, testPermit, other); err == nil {
				t.Error("a prefix was newly labelled with a withdrawn environment. " +
					"The picker still lists it -- deliberately, so a stored value " +
					"cannot vanish out of a SELECT and silently reassign the row -- " +
					"so this refusal is the only thing enforcing the rule.")
			}
		})
	}
}
