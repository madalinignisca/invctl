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
	"fmt"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The ownership report (WP-G7, piece 1): what has no owner, or an owner who
// cannot act.

type ownershipFixture struct {
	s     *SQLStore
	ctx   context.Context
	actor domain.Actor
	env   string
	admin *domain.AppUser
	// throwawayTeam is a single active team reused as the pass-through owner
	// for every UNOWNED custom field this fixture builds. NewCustomField
	// refuses an empty owner outright, so an unowned field is always created
	// WITH a real owner and then orphaned by a raw UPDATE (matching
	// seed_customfields.go's orphanCustomFieldOwner). One shared team here
	// rather than one per field keeps a bulk fixture (TestOwnershipBoundsThe
	// ResultSet builds hundreds) from paying for hundreds of team rows that
	// exist only to be discarded immediately.
	throwawayTeam string
}

func newOwnershipFixture(t *testing.T, e Engine) *ownershipFixture {
	t.Helper()
	s, ctx := newStore(t, e)

	user, err := domain.NewAppUser(NewID(), "ownership-admin", domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building fixture user: %v", err)
	}
	if err := s.CreateUser(ctx, testActor, user); err != nil {
		t.Fatalf("creating fixture user: %v", err)
	}

	env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	return &ownershipFixture{
		s: s, ctx: ctx, actor: domain.UserActor(user), env: env, admin: user,
	}
}

// team builds a team in the given lifecycle with the given contact, and
// returns its id. lifecycle == "" means active. A nil contact leaves
// contact_ref unset, which is finding 3's own condition.
func (f *ownershipFixture) team(t *testing.T, code, lifecycle string, contact *string) string {
	t.Helper()
	spec := domain.TeamSpec{Code: code, Name: code, ContactRef: contact}
	team, err := domain.NewTeam(NewID(), spec, f.s.Now())
	if err != nil {
		t.Fatalf("building team %s: %v", code, err)
	}
	if err := f.s.CreateTeam(f.ctx, testPermit, team); err != nil {
		t.Fatalf("creating team %s: %v", code, err)
	}
	if lifecycle != "" && lifecycle != domain.LifecycleActive {
		// CreateTeam always lands active (NewTeam's own default); moving it
		// to planned/deprecated/retired afterwards is a second, explicit
		// step for the same reason retiring is: nothing in this codebase
		// creates a team already in a non-active state.
		if lifecycle == domain.LifecycleRetired {
			if err := f.s.RetireTeam(f.ctx, testPermit, team.ID); err != nil {
				t.Fatalf("retiring team %s: %v", code, err)
			}
		} else {
			team.Lifecycle = lifecycle
			if err := f.s.UpdateTeam(f.ctx, testPermit, team); err != nil {
				t.Fatalf("moving team %s to %s: %v", code, lifecycle, err)
			}
		}
	}
	return team.ID
}

// asset creates an asset, owned by teamID (nil for unowned), in the given
// lifecycle ("" means active).
func (f *ownershipFixture) asset(t *testing.T, name string, teamID *string, lifecycle string) string {
	t.Helper()
	a, err := domain.NewAsset(NewID(), domain.KindVM, name, nil, f.s.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	a.TeamID = teamID
	if lifecycle != "" {
		a.Lifecycle = lifecycle
	}
	if err := f.s.CreateAsset(f.ctx, testPermit, a, []string{f.env}); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

// service creates a service, owned by teamID (nil for unowned), in the given
// lifecycle.
func (f *ownershipFixture) service(t *testing.T, code string, teamID *string, lifecycle string) string {
	t.Helper()
	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: domain.SvcAPI,
		EnvironmentID: f.env, Availability: domain.AvailStandalone, Tier: 3,
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	svc.TeamID = teamID
	if lifecycle != "" {
		svc.Lifecycle = lifecycle
	}
	if err := f.s.CreateService(f.ctx, testPermit, svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	return svc.ID
}

// project creates a project, owned by teamID (nil for unowned), in the given
// lifecycle.
func (f *ownershipFixture) project(t *testing.T, code string, teamID *string, lifecycle string) string {
	t.Helper()
	p, err := domain.NewProject(NewID(), domain.ProjectSpec{
		Code: code, Name: code, TeamID: teamID, Lifecycle: lifecycle,
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building project %s: %v", code, err)
	}
	if err := f.s.CreateProject(f.ctx, testPermit, p); err != nil {
		t.Fatalf("creating project %s: %v", code, err)
	}
	return p.ID
}

// identity creates an identity, owned by teamID (nil for unowned), in the
// given lifecycle ("" means active). identity's own lifecycle vocabulary is
// only active/retired (migration 00003) -- CHECK planned/deprecated aren't
// legal here and this helper does not pretend otherwise.
func (f *ownershipFixture) identity(t *testing.T, name string, teamID *string, lifecycle string) string {
	t.Helper()
	id, err := domain.NewIdentity(NewID(), domain.IdentityServiceAccount, name)
	if err != nil {
		t.Fatalf("building identity %s: %v", name, err)
	}
	id.TeamID = teamID
	if lifecycle != "" {
		id.Lifecycle = lifecycle
	}
	if err := f.s.CreateIdentity(f.ctx, testPermit, id); err != nil {
		t.Fatalf("creating identity %s: %v", name, err)
	}
	return id.ID
}

// customField creates a live custom field. teamID nil produces the ONE state
// domain.NewCustomField itself refuses to create -- a live field with no
// owner at all -- reproduced the same way seed_customfields.go's
// orphanCustomFieldOwner does: a raw UPDATE outside the store's write path,
// because the real event (migration 00054 landing under 11 pre-existing
// fields) cannot be replayed through CreateCustomField.
func (f *ownershipFixture) customField(t *testing.T, code string, teamID *string) string {
	t.Helper()
	var ownerID string
	if teamID != nil {
		ownerID = *teamID
	} else {
		// NewCustomField refuses an empty owner outright, so build with a
		// throwaway team and null the column out afterwards.
		if f.throwawayTeam == "" {
			f.throwawayTeam = f.team(t, "throwaway-owner", "", nil)
		}
		ownerID = f.throwawayTeam
	}
	cf, err := domain.NewCustomField(NewID(), domain.CustomFieldEntityAsset, code, code,
		domain.CustomFieldText, "an ownership-report fixture field", f.admin.ID, ownerID, f.s.Now())
	if err != nil {
		t.Fatalf("building custom field %s: %v", code, err)
	}
	if err := f.s.CreateCustomField(f.ctx, f.actor, cf); err != nil {
		t.Fatalf("creating custom field %s: %v", code, err)
	}
	if teamID == nil {
		if _, err := f.s.db.Writer.ExecContext(f.ctx, f.s.db.Rebind(
			`UPDATE custom_field SET owner_team_id = NULL WHERE id = ?`), cf.ID); err != nil {
			t.Fatalf("orphaning custom field %s: %v", code, err)
		}
	}
	return cf.ID
}

func strp(s string) *string { return &s }

// TestOwnershipUnowned covers finding 1 across all five entity types.
func TestOwnershipUnowned(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			ownedTeam := f.team(t, "owns-stuff", "", strp("owns-stuff@example.com"))

			f.asset(t, "unowned-asset", nil, "")
			f.service(t, "unowned-svc", nil, "")
			f.project(t, "unowned-proj", nil, "")
			f.identity(t, "unowned-identity", nil, "")
			f.customField(t, "unowned_field", nil)

			// A control row of each type, owned, must NOT show up.
			f.asset(t, "owned-asset", &ownedTeam, "")
			f.service(t, "owned-svc", &ownedTeam, "")
			f.project(t, "owned-proj", &ownedTeam, "")
			f.identity(t, "owned-identity", &ownedTeam, "")
			f.customField(t, "owned_field", &ownedTeam)

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}

			wantTypes := map[string]bool{
				"asset": false, "service": false, "project": false,
				"identity": false, "custom_field": false,
			}
			for _, r := range report.Unowned {
				if _, ok := wantTypes[r.EntityType]; ok {
					wantTypes[r.EntityType] = true
				}
				if r.TeamID != "" {
					t.Errorf("unowned row %s/%s names a team: %q", r.EntityType, r.Name, r.TeamID)
				}
			}
			for et, found := range wantTypes {
				if !found {
					t.Errorf("entity type %s never appeared in Unowned", et)
				}
			}
			if report.UnownedTotal != len(report.Unowned) {
				t.Errorf("UnownedTotal = %d, len(Unowned) = %d, want equal under the bound",
					report.UnownedTotal, len(report.Unowned))
			}

			for _, r := range report.Unowned {
				if r.Name == "owned-asset" || r.Name == "owned-svc" ||
					r.Name == "owned-proj" || r.Name == "owned-identity" || r.Name == "owned_field" {
					t.Errorf("an OWNED entity %q appeared in Unowned", r.Name)
				}
			}
		})
	}
}

// TestOwnershipDeprecatedOwnerCannotAct is the one a binary active/retired
// check misses silently: a DEPRECATED team still owns something, and the
// design calls that "arguably the most interesting finding".
func TestOwnershipDeprecatedOwnerCannotAct(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			deprecated := f.team(t, "on-the-way-out", domain.LifecycleDeprecated, strp("winding-down@example.com"))
			f.asset(t, "carried-by-deprecated-team", &deprecated, "")

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}

			var found *OwnershipRow
			for i := range report.CannotAct {
				if report.CannotAct[i].Name == "carried-by-deprecated-team" {
					found = &report.CannotAct[i]
				}
			}
			if found == nil {
				t.Fatal("an asset owned by a deprecated team did not appear in CannotAct -- " +
					"a binary active/retired check would miss this silently")
			}
			if !found.Transitional() {
				t.Errorf("a deprecated owner classified as %q, want transitional", found.Eligibility())
			}
			for _, r := range report.Unowned {
				if r.Name == "carried-by-deprecated-team" {
					t.Error("an OWNED (if by a deprecated team) asset appeared in Unowned too")
				}
			}
		})
	}
}

// TestOwnershipRetiredOwnerCannotAct is the other half of finding 2: a team
// that has fully disbanded, which is CannotAct but not Transitional.
func TestOwnershipRetiredOwnerCannotAct(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			retired := f.team(t, "long-gone", domain.LifecycleRetired, strp("long-gone@example.com"))
			f.service(t, "carried-by-retired-team", &retired, "")

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}

			var found *OwnershipRow
			for i := range report.CannotAct {
				if report.CannotAct[i].Name == "carried-by-retired-team" {
					found = &report.CannotAct[i]
				}
			}
			if found == nil {
				t.Fatal("a service owned by a retired team did not appear in CannotAct")
			}
			if found.Transitional() {
				t.Error("a fully retired owner classified as transitional")
			}
			if found.Eligibility() != domain.OwnerCannotAct {
				t.Errorf("eligibility = %q, want %q", found.Eligibility(), domain.OwnerCannotAct)
			}
		})
	}
}

// TestOwnershipRetiredEntityExcluded: an entity that is itself retired is not
// a finding, whether it is unowned or owned by a team that cannot act. Nobody
// needs to look after a retired thing.
func TestOwnershipRetiredEntityExcluded(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			retiredTeam := f.team(t, "gone-team", domain.LifecycleRetired, strp("gone@example.com"))

			unownedID := f.asset(t, "retired-unowned-asset", nil, "")
			if err := f.s.RetireAsset(f.ctx, testPermit, unownedID); err != nil {
				t.Fatalf("retiring asset: %v", err)
			}
			ownedID := f.asset(t, "retired-owned-by-gone-team", &retiredTeam, "")
			if err := f.s.RetireAsset(f.ctx, testPermit, ownedID); err != nil {
				t.Fatalf("retiring asset: %v", err)
			}

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}
			for _, r := range report.Unowned {
				if r.ID == unownedID {
					t.Error("a RETIRED unowned asset appeared in Unowned")
				}
			}
			for _, r := range report.CannotAct {
				if r.ID == ownedID {
					t.Error("a RETIRED asset owned by a gone team appeared in CannotAct")
				}
			}
		})
	}
}

// TestOwnershipNoContactTeamAppearsOnce is finding 3's whole point: a team
// owning several things with no contact is ONE row, not one per entity.
func TestOwnershipNoContactTeamAppearsOnce(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			noContact := f.team(t, "unreachable", "", nil)

			f.asset(t, "nc-asset-1", &noContact, "")
			f.asset(t, "nc-asset-2", &noContact, "")
			f.service(t, "nc-service-1", &noContact, "")

			// A retired thing it owns must not inflate the count.
			retiredID := f.asset(t, "nc-asset-retired", &noContact, "")
			if err := f.s.RetireAsset(f.ctx, testPermit, retiredID); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}

			var found *NoContactTeam
			count := 0
			for i := range report.NoContact {
				if report.NoContact[i].TeamID == noContact {
					found = &report.NoContact[i]
					count++
				}
			}
			if count != 1 {
				t.Fatalf("team with no contact appeared %d times, want exactly 1", count)
			}
			if found.AssetCount != 2 {
				t.Errorf("AssetCount = %d, want 2 (the retired one must not count)", found.AssetCount)
			}
			if found.ServiceCount != 1 {
				t.Errorf("ServiceCount = %d, want 1", found.ServiceCount)
			}
			if found.Total() != 3 {
				t.Errorf("Total() = %d, want 3", found.Total())
			}
		})
	}
}

// TestOwnershipNoContactRequiresOwnership: a contactless team owning nothing
// is not a finding -- there is nothing at stake for anybody yet.
func TestOwnershipNoContactRequiresOwnership(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			f.team(t, "idle-unreachable", "", nil)

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}
			for _, r := range report.NoContact {
				if r.TeamCode == "idle-unreachable" {
					t.Error("a contactless team owning nothing appeared in NoContact")
				}
			}
		})
	}
}

// TestOwnershipNoContactOnlyEligibleTeams: the design restricts finding 3 to
// an ACTIVE team's silence. A deprecated or retired team with no contact is
// already covered by finding 2 and must not double up here.
func TestOwnershipNoContactOnlyEligibleTeams(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			deprecated := f.team(t, "deprecated-no-contact", domain.LifecycleDeprecated, nil)
			f.asset(t, "owned-by-deprecated-no-contact", &deprecated, "")

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}
			for _, r := range report.NoContact {
				if r.TeamID == deprecated {
					t.Error("a deprecated team appeared in NoContact -- that is finding 2's job, not finding 3's")
				}
			}
		})
	}
}

// TestOwnershipEmptyEstateIsHonest: no gaps must render as a real, positive
// answer -- not indistinguishable from a broken query.
func TestOwnershipEmptyEstateIsHonest(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			// One fully healthy asset: owned, by a reachable, active team.
			team := f.team(t, "healthy-team", "", strp("healthy@example.com"))
			f.asset(t, "healthy-asset", &team, "")

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}
			if !report.Empty() {
				t.Errorf("report is not Empty(): %d unowned, %d cannot-act, %d no-contact",
					len(report.Unowned), len(report.CannotAct), len(report.NoContact))
			}
		})
	}
}

// TestOwnershipBoundsTheResultSet proves the LIMIT is real: an estate with
// more unowned custom fields than the bound still returns an honest total
// and a truncated flag, and the merged result never exceeds the bound.
func TestOwnershipBoundsTheResultSet(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			const extra = 5
			want := ownershipRowLimit + extra
			for i := 0; i < want; i++ {
				f.customField(t, fmt.Sprintf("bulk_field_%03d", i), nil)
			}

			report, err := f.s.OwnershipFindings(f.ctx)
			if err != nil {
				t.Fatalf("OwnershipFindings: %v", err)
			}
			if len(report.Unowned) > ownershipRowLimit {
				t.Fatalf("Unowned has %d rows, the page must never exceed the bound of %d",
					len(report.Unowned), ownershipRowLimit)
			}
			if report.UnownedTotal != want {
				t.Errorf("UnownedTotal = %d, want %d", report.UnownedTotal, want)
			}
			if !report.UnownedTruncated {
				t.Error("UnownedTruncated is false despite the total exceeding the bound")
			}
		})
	}
}
