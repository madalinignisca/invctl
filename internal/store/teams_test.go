// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// TestRetiringATeamLeavesWhatItLookedAfterPointingAtIt.
//
// The tempting alternative -- null the column so the lists come out clean -- is
// wrong. A retired team still named by live assets is the estate saying "this
// was theirs and nobody has picked it up", which is a finding. Clearing it
// erases the question along with the answer, and does so silently.
func TestRetiringATeamLeavesWhatItLookedAfterPointingAtIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: "platform", Name: "Platform"}, s.Now())
			if err != nil {
				t.Fatalf("building the team: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating the team: %v", err)
			}

			id := mustAsset(t, s, ctx, domain.KindServer, "hv-01", nil, env)
			row, err := s.GetAsset(ctx, id)
			if err != nil {
				t.Fatalf("reading the asset: %v", err)
			}
			a := row.Asset
			a.TeamID = &team.ID
			role := "operator"
			a.ManagerRole = &role
			if err := s.UpdateAsset(ctx, testActor, &a, []string{env}); err != nil {
				t.Fatalf("assigning the team: %v", err)
			}

			if err := s.RetireTeam(ctx, testActor, team.ID); err != nil {
				t.Fatalf("retiring the team: %v", err)
			}

			after, err := s.GetAsset(ctx, id)
			if err != nil {
				t.Fatalf("re-reading the asset: %v", err)
			}
			if after.TeamID == nil || *after.TeamID != team.ID {
				t.Error("retiring a team cleared it from the asset it looked after; " +
					"the estate can no longer say nobody has picked this up")
			}
			// And the team's own counts still see it, so the finding is visible
			// from both ends.
			tr, err := s.GetTeam(ctx, team.ID)
			if err != nil {
				t.Fatalf("reading the team: %v", err)
			}
			if tr.AssetCount != 1 {
				t.Errorf("the retired team reports %d assets, want 1", tr.AssetCount)
			}
		})
	}
}

// The team is a real foreign key now, which is the whole point of promoting it
// out of free text: `platform`, `Platform` and `platform-team` can no longer be
// three teams, and an id that names nothing is refused rather than stored.
func TestAnUnknownTeamIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			id := mustAsset(t, s, ctx, domain.KindServer, "hv-01", nil, env)

			row, err := s.GetAsset(ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			a := row.Asset
			ghost := NewID()
			a.TeamID = &ghost
			if err := s.UpdateAsset(ctx, testActor, &a, []string{env}); err == nil {
				t.Error("an asset was assigned to a team that does not exist")
			}
		})
	}
}

// Assigning a team is a declared change and takes a change_log row, like every
// other statement of who is answerable for what.
func TestAssigningATeamIsAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: "platform", Name: "Platform"}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating: %v", err)
			}
			id := mustAsset(t, s, ctx, domain.KindServer, "hv-01", nil, env)

			before, err := s.countOne(ctx, `SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "asset")
			if err != nil {
				t.Fatalf("counting: %v", err)
			}

			row, err := s.GetAsset(ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			a := row.Asset
			a.TeamID = &team.ID
			if err := s.UpdateAsset(ctx, testActor, &a, []string{env}); err != nil {
				t.Fatalf("assigning: %v", err)
			}

			after, err := s.countOne(ctx, `SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "asset")
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if after != before+1 {
				t.Errorf("change_log went %d -> %d, want one more", before, after)
			}

			// Creating the team is audited too, under its own entity type.
			teams, err := s.countOne(ctx, `SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "team")
			if err != nil {
				t.Fatalf("counting team entries: %v", err)
			}
			if teams < 1 {
				t.Error("creating a team wrote no change_log entry")
			}
		})
	}
}

// TestATeamsContactNeverReachesTheAuditTrail.
//
// From a security review, and it found the gap this feature's own design claimed
// to close. The rule is that a contact must be a group address and never a
// person; the application cannot check that, so the form hint is the whole
// enforcement. That is adequate for the `team` row and for search_index, because
// both hold only the CURRENT value and an erasure request is answered by editing
// the team.
//
// It is NOT adequate for change_log, which is append-only. A predictable
// operator mistake would be permanent -- and worse, CORRECTING it wrote the
// personal value a second time, as the `old` side of the update diff. So the
// trail records that the contact changed and never what to, exactly as it
// already treats secret_ref.
func TestATeamsContactNeverReachesTheAuditTrail(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// The mistake the hint warns against, made anyway.
			personal := "alice.smith@example.com"
			team, err := domain.NewTeam(NewID(), domain.TeamSpec{
				Code: "platform", Name: "Platform", ContactRef: &personal,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating: %v", err)
			}

			// ...and then corrected, which is what writes it a second time.
			corrected := *team
			group := "platform@example.com"
			corrected.ContactRef = &group
			if err := s.UpdateTeam(ctx, testActor, &corrected); err != nil {
				t.Fatalf("correcting: %v", err)
			}

			var diffs []string
			if err := s.read(ctx, &diffs,
				`SELECT diff FROM change_log WHERE entity_type = ? ORDER BY at, id`, "team"); err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			if len(diffs) < 2 {
				t.Fatalf("expected a create and an update entry, got %d", len(diffs))
			}
			for i, d := range diffs {
				if strings.Contains(d, personal) {
					t.Errorf("change_log entry %d carries the personal address permanently: %s", i, d)
				}
			}

			// The control: the trail must still say the contact CHANGED, or
			// redaction has quietly become deletion and the audit is worthless.
			if !strings.Contains(diffs[1], "contact_ref") {
				t.Errorf("the update entry does not record that the contact changed: %s", diffs[1])
			}
			// And the team row itself keeps the real value -- redaction applies
			// to the permanent trail, not to the correctable record.
			row, err := s.GetTeam(ctx, team.ID)
			if err != nil {
				t.Fatalf("reading the team: %v", err)
			}
			if row.ContactRef == nil || *row.ContactRef != group {
				t.Errorf("the team row lost its contact: %v", row.ContactRef)
			}
		})
	}
}

// A retired team keeps its row, so an erasure request can still edit it -- but
// it must not stay discoverable by a contact nobody is looking after any more.
func TestRetiringATeamDropsItsContactFromSearch(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			contact := "platform@example.com"
			team, err := domain.NewTeam(NewID(), domain.TeamSpec{
				Code: "platform", Name: "Platform", ContactRef: &contact,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating: %v", err)
			}

			found, err := s.Search(ctx, contact, 10)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if len(found) == 0 {
				t.Fatal("a live team is not findable by its contact; the index is not doing its job")
			}

			if err := s.RetireTeam(ctx, testActor, team.ID); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			after, err := s.Search(ctx, contact, 10)
			if err != nil {
				t.Fatalf("searching after retirement: %v", err)
			}
			for _, r := range after {
				if r.EntityType == "team" && r.EntityID == team.ID {
					t.Error("a disbanded team is still findable by its contact")
				}
			}
			// The control: it is still findable by NAME, because the row still
			// exists and somebody looking for what it used to own needs it.
			byName, err := s.Search(ctx, "Platform", 10)
			if err != nil {
				t.Fatalf("searching by name: %v", err)
			}
			var seen bool
			for _, r := range byName {
				if r.EntityType == "team" && r.EntityID == team.ID {
					seen = true
				}
			}
			if !seen {
				t.Error("retiring a team removed it from search entirely; it should lose " +
					"its contact, not its existence")
			}
		})
	}
}
