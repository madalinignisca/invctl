// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

func mustUser(t *testing.T, username, role string, active bool) *domain.AppUser {
	t.Helper()
	u, err := domain.NewAppUser("id-"+username, username, domain.UserSourceLocal, time.Now())
	if err != nil {
		t.Fatalf("building user %s: %v", username, err)
	}
	u.Role = role
	u.IsActive = active
	return u
}

// fakeProjects is an in-memory ProjectResolver. Authorizer.Permit's own
// logic -- turning "these are my projects" into a ScopedPermit -- is pure
// enough that a database adds nothing here; the store's own reverse-lookup
// queries (AssetIDsForProjects and its two siblings) are covered by
// internal/store's own suite against both engines.
type fakeProjects struct {
	byUser    map[string][]string
	assets    map[string][]string // project id -> asset ids
	services  map[string][]string
	circuits  map[string][]string
	failUser  error
	failAsset error
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{
		byUser:   map[string][]string{},
		assets:   map[string][]string{},
		services: map[string][]string{},
		circuits: map[string][]string{},
	}
}

func (f *fakeProjects) ProjectsForUser(_ context.Context, userID string) ([]string, error) {
	if f.failUser != nil {
		return nil, f.failUser
	}
	return f.byUser[userID], nil
}

func (f *fakeProjects) collect(byProject map[string][]string, projectIDs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range projectIDs {
		for _, entityID := range byProject[id] {
			if !seen[entityID] {
				seen[entityID] = true
				out = append(out, entityID)
			}
		}
	}
	return out
}

func (f *fakeProjects) AssetIDsForProjects(_ context.Context, projectIDs []string) ([]string, error) {
	if f.failAsset != nil {
		return nil, f.failAsset
	}
	return f.collect(f.assets, projectIDs), nil
}

func (f *fakeProjects) ServiceIDsForProjects(_ context.Context, projectIDs []string) ([]string, error) {
	return f.collect(f.services, projectIDs), nil
}

func (f *fakeProjects) CircuitIDsForProjects(_ context.Context, projectIDs []string) ([]string, error) {
	return f.collect(f.circuits, projectIDs), nil
}

// TestAnAdministratorGetsAPermitCoveringEverything mints via
// Authorizer.Permit -- the one caller (Task 12 wires a request through it)
// that turns "who is signed in" into an authorization decision -- and checks
// the mint agrees with isAdministrator's own two paths: the role column, and
// the INV_ADMIN_USERS break-glass override (auth.go's isAdministrator doc
// comment explains why the override must win independently of the column).
func TestAnAdministratorGetsAPermitCoveringEverything(t *testing.T) {
	tests := []struct {
		name  string
		user  *domain.AppUser
		admin []string
	}{
		{"role column", mustUser(t, "alice", domain.RoleAdministrator, true), nil},
		{"break-glass override", mustUser(t, "bob", domain.RoleObserver, true), []string{"bob"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAuthorizer(tc.admin, newFakeProjects())
			p, err := a.Permit(context.Background(), tc.user)
			if err != nil {
				t.Fatalf("Permit: %v", err)
			}
			for _, entityType := range []string{"circuit", "team", "vlan", "anything"} {
				if !p.Covers(entityType, "any-id") {
					t.Errorf("Administrator's permit refused %q; Covers must be "+
						"unconditional for an Administrator or the mechanism denies "+
						"service to the one role the whole estate depends on", entityType)
				}
			}
			if p.Actor().ID != tc.user.ID {
				t.Errorf("Actor().ID = %q, want %q", p.Actor().ID, tc.user.ID)
			}
		})
	}
}

// TestAnObserverGetsNoPermitAtAll is the distinction this whole gate exists
// to make: an Observer never reaches a write handler, so the gate must
// return domain.ErrForbidden -- not a domain.ScopedPermit that happens to
// cover nothing. The two look interchangeable to a caller that only asks
// Covers() and gets false either way, but they fail at different points: an
// error here refuses the write before a transaction opens, the other only at
// tx.log, inside one, after every earlier statement has to be rolled back.
func TestAnObserverGetsNoPermitAtAll(t *testing.T) {
	a := NewAuthorizer(nil, newFakeProjects())
	p, err := a.Permit(context.Background(), mustUser(t, "carol", domain.RoleObserver, true))
	if p != nil {
		t.Errorf("Permit returned a non-nil permit for an Observer: %#v", p)
	}
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("err = %v, want domain.ErrForbidden", err)
	}
}

// TestAProjectOwnerWithNoAssignmentsCanWriteNothing is the honest edge
// docs/rbac-design.md §4 calls out: "entities in no project are
// Administrator territory" cuts both ways. Unlike an Observer, a project
// owner IS meant to reach a write handler in general -- it is the OBJECT
// that turns out to be out of scope, not the role -- so this must return a
// real permit, not domain.ErrForbidden, and that permit must cover nothing.
func TestAProjectOwnerWithNoAssignmentsCanWriteNothing(t *testing.T) {
	a := NewAuthorizer(nil, newFakeProjects())
	p, err := a.Permit(context.Background(), mustUser(t, "dave", domain.RoleProjectOwner, true))
	if err != nil {
		t.Fatalf("Permit returned an error for a project owner with no assignments: %v", err)
	}
	if p == nil {
		t.Fatal("Permit returned a nil permit for a project owner; that is the Observer answer, not this one")
	}
	if p.Covers("asset", "any-asset") {
		t.Error("a project owner with no project assignments covered an asset")
	}
}

// TestAProjectOwnersPermitCoversAssetsServicesAndCircuitsOfTheirProjects
// checks all three project-linkable types, by exact id: an entity linked to
// a project the owner holds is covered, one linked to a project they do not
// hold is not, regardless of type.
func TestAProjectOwnersPermitCoversAssetsServicesAndCircuitsOfTheirProjects(t *testing.T) {
	fp := newFakeProjects()
	fp.byUser["id-erin"] = []string{"proj-mine"}
	fp.assets["proj-mine"] = []string{"asset-mine"}
	fp.assets["proj-other"] = []string{"asset-other"}
	fp.services["proj-mine"] = []string{"service-mine"}
	fp.services["proj-other"] = []string{"service-other"}
	fp.circuits["proj-mine"] = []string{"circuit-mine"}
	fp.circuits["proj-other"] = []string{"circuit-other"}

	a := NewAuthorizer(nil, fp)
	p, err := a.Permit(context.Background(), mustUser(t, "erin", domain.RoleProjectOwner, true))
	if err != nil {
		t.Fatalf("Permit: %v", err)
	}

	tests := []struct {
		entityType string
		id         string
		want       bool
	}{
		{"asset", "asset-mine", true},
		{"asset", "asset-other", false},
		{"service", "service-mine", true},
		{"service", "service-other", false},
		{"circuit", "circuit-mine", true},
		{"circuit", "circuit-other", false},
	}
	for _, tc := range tests {
		if got := p.Covers(tc.entityType, tc.id); got != tc.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", tc.entityType, tc.id, got, tc.want)
		}
	}
}

// permitCoveringEveryType builds a domain.ScopedPermit whose entities map
// carries "shared-id" under EVERY type named, regardless of what
// domain.ScopeClassOf says about it -- the same technique
// TestAnUnclassifiedEntityTypeFailsLoudlyRatherThanBeingAllowed in
// internal/store/permit_source_test.go uses, and for the same reason: a
// negative test that only ever asks about an entity type the gate's own
// population code happens to skip would still pass if the CLASSIFICATION
// check inside Covers were deleted entirely, because nothing would ever put
// a "team" or an "interface" id into ScopedEntities through the real gate.
// Populating every type by hand is what makes the classification guard --
// not the population code -- the thing actually being tested, and what
// makes "add X to the project-scoped classification" a mutation these tests
// can catch.
func permitCoveringEveryType(types ...string) domain.Permit {
	actor := domain.Actor{ID: "po-1", Name: "po-1", Kind: domain.ActorKindUser}
	entities := make(domain.ScopedEntities, len(types))
	for _, et := range types {
		entities[et] = map[string]bool{"shared-id": true}
	}
	return domain.ScopedPermit(actor, []string{"proj-mine"}, entities)
}

// TestAProjectOwnersPermitNeverCoversEstateConfiguration is
// docs/rbac-design.md §4's explicit exclusion list: "without this line,
// scoped write silently becomes write for everything not project-linked" --
// which is most of the schema.
func TestAProjectOwnersPermitNeverCoversEstateConfiguration(t *testing.T) {
	types := []string{
		"team", "tag", "custom_field", "environment", "vocabulary",
		"provider", "app_user", "project", "inflation_rate",
	}
	p := permitCoveringEveryType(types...)
	for _, entityType := range types {
		if p.Covers(entityType, "shared-id") {
			t.Errorf("a project owner's permit covered estate-config type %q", entityType)
		}
	}
}

// TestAProjectOwnersPermitNeverCoversTopology is the topology half of the
// same exclusion: docs/rbac-design.md §6 defaults every entity type not
// proven project-linked to Administrator-only, and this is the negative
// space that default protects.
func TestAProjectOwnersPermitNeverCoversTopology(t *testing.T) {
	types := []string{
		"interface", "link", "power_feed", "prefix", "ip_address",
		"vlan", "cluster", "dependency", "service_instance", "endpoint",
	}
	p := permitCoveringEveryType(types...)
	for _, entityType := range types {
		if p.Covers(entityType, "shared-id") {
			t.Errorf("a project owner's permit covered topology type %q", entityType)
		}
	}
}

// TestAnUnclassifiedEntityTypeIsNeverCovered pins domain.ScopeClassOf's own
// fail-closed default at the gate: a type nothing in
// internal/domain/role.go's census recognises must deny, not fall through
// to "unclassified means topology means denied anyway by luck".
func TestAnUnclassifiedEntityTypeIsNeverCovered(t *testing.T) {
	p := permitCoveringEveryType("this_entity_type_does_not_exist")
	if p.Covers("this_entity_type_does_not_exist", "shared-id") {
		t.Error("a project owner's permit covered an entity type nothing classifies")
	}
}

// TestAPermitCoversAProjectLinkRowOnlyForProjectsItHolds is the Task 14
// carve-out (docs/rbac-design.md §4), tested at the gate rather than only
// through the create route it protects: a project owner may write the LINK
// ROW itself -- project_asset, project_service, project_circuit -- for a
// project they hold, entirely independent of which specific asset the id
// names. NO DEPENDENCE ON WHAT A TRANSACTION DID: that independence is
// exactly what the project-scoped-create-route decision bought over the
// earlier minted-id design (see domain.scopedPermit's own doc comment), and
// testing it here rather than only through the route is what keeps it
// honest.
func TestAPermitCoversAProjectLinkRowOnlyForProjectsItHolds(t *testing.T) {
	fp := newFakeProjects()
	fp.byUser["id-iris"] = []string{"proj-mine"}

	a := NewAuthorizer(nil, fp)
	p, err := a.Permit(context.Background(), mustUser(t, "iris", domain.RoleProjectOwner, true))
	if err != nil {
		t.Fatalf("Permit: %v", err)
	}

	if !p.Covers("project_asset", "proj-mine/any-asset-id") {
		t.Error("Covers refused the link row for a project this owner holds")
	}
	if p.Covers("project_asset", "proj-other/any-asset-id") {
		t.Error("Covers authorized the link row for a project this owner does not hold")
	}
}

// TestADeactivatedAdministratorsPermitIsRefused mirrors
// TestADeactivatedAdministratorMayNotWriteEvenWhenNamedInTheEnvironment in
// auth_test.go: a disabled account named in INV_ADMIN_USERS -- an
// ex-employee's name can sit there long after they left -- must not mint an
// Administrator's permit merely because isAdministrator's role/env check
// would say yes on its own. Permit checks IsActive itself rather than
// trusting a caller to have checked it first, for the same reason
// IsAdministrator does.
func TestADeactivatedAdministratorsPermitIsRefused(t *testing.T) {
	user := mustUser(t, "eve", domain.RoleAdministrator, false)
	a := NewAuthorizer([]string{"eve"}, newFakeProjects())
	p, err := a.Permit(context.Background(), user)
	if p != nil {
		t.Error("a deactivated Administrator's permit was non-nil; break-glass restores a role, not a disabled account")
	}
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("err = %v, want domain.ErrForbidden", err)
	}
}

// TestANilUserGetsNoPermit guards the same fail-closed door for a request
// that reached the gate with nobody signed in at all.
func TestANilUserGetsNoPermit(t *testing.T) {
	a := NewAuthorizer(nil, newFakeProjects())
	p, err := a.Permit(context.Background(), nil)
	if p != nil {
		t.Error("Permit(nil) returned a non-nil permit")
	}
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("err = %v, want domain.ErrForbidden", err)
	}
}

// TestAProjectOwnersPermitPropagatesAResolverError proves the gate does not
// swallow a failed lookup into an empty-but-successful scope -- a store
// error reaching the resolver must reach the caller as an error, not as a
// permit that quietly covers nothing.
func TestAProjectOwnersPermitPropagatesAResolverError(t *testing.T) {
	fp := newFakeProjects()
	fp.failUser = errors.New("database is down")
	a := NewAuthorizer(nil, fp)
	p, err := a.Permit(context.Background(), mustUser(t, "jack", domain.RoleProjectOwner, true))
	if p != nil {
		t.Error("Permit returned a non-nil permit despite a resolver error")
	}
	if err == nil {
		t.Fatal("Permit swallowed a resolver error")
	}
}
