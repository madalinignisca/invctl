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

// TestUpdateDependencyCannotForgeAttestationOrLifecycle is the fix-round
// item 2 case: UpdateDependency must carry verified_by, verified_at and
// lifecycle over from the STORED row, never accept them from the caller.
// VerifyDependency is the only place that is allowed to set the first two,
// deriving verified_by from p.Actor().ID rather than taking it as input;
// RetireDependency is the only place allowed to flip lifecycle. Before this
// carry-over, a caller reaching UpdateDependency could submit ANY app_user
// id as verified_by (an attestation nobody made) or flip a withdrawn edge
// back to active with no retire guard and no honest change_log entry.
func TestUpdateDependencyCannotForgeAttestationOrLifecycle(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "attest-svc")
			ep := mustEndpoint(t, s, ctx, svc, "sock", 9101)

			d, err := domain.NewDependency(NewID(), newDependencySpec(svc, ep), s.Now())
			if err != nil {
				t.Fatalf("building the dependency: %v", err)
			}
			if err := s.CreateDependency(ctx, testPermit, d, nil); err != nil {
				t.Fatalf("creating the dependency: %v", err)
			}
			if d.VerifiedBy != nil || d.VerifiedAt != nil {
				t.Fatalf("a freshly created dependency already carries an attestation")
			}

			forgedActor := "admin-nobody-asked"
			forgedAt := s.Now().Format("2006-01-02T15:04:05Z")
			forged := *d
			forged.VerifiedBy = &forgedActor
			forged.VerifiedAt = &forgedAt
			forged.Lifecycle = domain.LifecycleRetired

			if err := s.UpdateDependency(ctx, testPermit, &forged, nil); err != nil {
				t.Fatalf("UpdateDependency with a forged attestation and lifecycle: %v", err)
			}

			after, err := s.GetDependency(ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the dependency: %v", err)
			}
			if after.VerifiedBy != nil {
				t.Errorf("UpdateDependency persisted a forged verified_by = %q, want nil", *after.VerifiedBy)
			}
			if after.VerifiedAt != nil {
				t.Errorf("UpdateDependency persisted a forged verified_at = %q, want nil", *after.VerifiedAt)
			}
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("UpdateDependency persisted a submitted lifecycle = %q, want %q",
					after.Lifecycle, domain.LifecycleActive)
			}
		})
	}
}
