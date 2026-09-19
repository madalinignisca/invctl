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

// TestScrubbingAnOIDCUserSurvivesTheirNextSignIn is the erasure guarantee,
// tested against the one authenticator that can undo it.
//
// ScrubUser answers a GDPR erasure request by emptying the personal columns
// and leaving the opaque id the audit trail refers to. That holds for local
// and LDAP accounts because both are matched on USERNAME, and scrubbing
// randomises the username -- a returning person cannot land on their old row,
// so they get a fresh account and the scrubbed one stays scrubbed.
//
// OIDC matches on `subject` instead (spec D2), which is exactly what makes it
// survive a rename -- and would make it survive a scrub too, if the subject
// were left behind. A scrubbed row still carrying its `sub` is found by
// GetUserBySubject on the next sign-in attempt, and updateOIDCUser then writes
// the name and email straight back from the fresh Keycloak claims. The erasure
// is reversed by the erased person merely TRYING to sign in -- and, because
// that write is audited, their name and email land in `change_log`, which is
// append-only and kept forever precisely because it was promised to hold no
// personal data.
//
// Clearing `subject` on scrub is what makes OIDC behave like every other
// authenticator here. The unique index on it is partial (`WHERE subject IS
// NOT NULL`, migration 00072), so any number of scrubbed rows can hold NULL.
func TestScrubbingAnOIDCUserSurvivesTheirNextSignIn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := domain.AdministratorPermit(domain.SystemActor)

			erased, err := s.UpsertOIDCUser(ctx, "kc-sub-erika", "erika", "Erika Berger", "erika@example.com")
			if err != nil {
				t.Fatalf("first sign-in: %v", err)
			}
			if err := s.ScrubUser(ctx, admin, erased.ID); err != nil {
				t.Fatalf("scrubbing: %v", err)
			}

			// The same person, at the same IdP, with the same sub, tries again.
			returned, err := s.UpsertOIDCUser(ctx, "kc-sub-erika", "erika", "Erika Berger", "erika@example.com")
			if err != nil {
				t.Fatalf("sign-in after scrub: %v", err)
			}
			if returned.ID == erased.ID {
				t.Errorf("signing in after a scrub landed back on the erased account %s. "+
					"The scrubbed row kept its subject, so GetUserBySubject found it and "+
					"the Keycloak claims were written back over the erasure.", erased.ID)
			}

			// Whatever happened above, the erased row must still be erased.
			after, err := s.GetUser(ctx, erased.ID)
			if err != nil {
				t.Fatalf("re-reading the scrubbed account: %v", err)
			}
			if after.DisplayName != nil {
				t.Errorf("display_name came back as %q after a scrub", *after.DisplayName)
			}
			if after.Email != nil {
				t.Errorf("email came back as %q after a scrub", *after.Email)
			}
			if after.Subject != nil {
				t.Errorf("subject %q survived the scrub; it is the key that lets a "+
					"returning sign-in find and repopulate this row", *after.Subject)
			}
			if after.IsActive {
				t.Error("the scrubbed account was reactivated")
			}
		})
	}
}
