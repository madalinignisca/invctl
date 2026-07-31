package store

import (
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
