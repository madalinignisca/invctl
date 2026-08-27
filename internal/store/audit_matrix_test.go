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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G1 Task 8: the positive half of the audit matrix.
//
// Task 7 moved the object-level authorization check into tx.log -- the one
// INSERT INTO change_log in this codebase. That turned "every declared
// mutation writes a change_log row" from an audit rule into an authorization
// invariant: a store method that stops calling logCreate/logUpdate is a
// method the guard never sees, and it fails OPEN -- the write goes through
// with no permission check of any kind.
//
// Every test in boundary_source_test.go is negative: it proves a path does
// NOT log, does NOT touch a declared table, does NOT reach change_log. None
// of them proves the opposite -- that a declared mutation DOES log. This file
// is that other half.
//
// The coverage below is not just "what a project owner may write" (asset,
// service, circuit). It also covers a representative sample of what a project
// owner is REFUSED -- estate config (team, tag, custom field, vocabulary
// term) and topology (interface, link, prefix) -- because a mutation there
// that quietly stopped logging is a write a project owner's ScopedPermit was
// never meant to reach passing completely unchecked. The bypass this test
// exists to catch is not bounded by what the role is allowed to touch; it
// covers every entity type the guard is supposed to refuse, which is most of
// the schema.
//
// CREATE-ONLY COVERAGE OF AN ESTATE-CONFIG TYPE WAS PROVEN INSUFFICIENT
// DURING REVIEW: suppressing UpdateTeam's tx.logUpdate call compiled and left
// this whole matrix green, because the first draft only exercised "team
// create". A project owner is refused on team entirely, so an unlogged team
// EDIT is exactly the write that role can perform and this matrix exists to
// catch -- create-only coverage of a type does not stand in for it. Every
// estate-config and topology type below is now exercised on every verb its
// own store package actually implements (see entityTypeVerbs and
// TestEveryAuditMatrixEntityTypeCoversEveryApplicableVerb), not merely one
// representative mutation. The three child-row types (asset_cost,
// service_instance, circuit_termination) are the deliberate exception --
// task-8-brief.md asks for one representative child-row mutation each, not
// full CRUD on every child table, and entityTypeVerbs records that narrower
// scope explicitly rather than leaving it to be assumed.

// auditCase is one store mutation this test drives to completion and checks
// against change_log.
//
// setup and mutate are split deliberately. Update and retire cases need an
// existing row to act on, and CREATING that row is itself an audited
// mutation -- so the assertion below cannot look at the absolute number of
// change_log rows for the id, only at how many mutate itself adds. setup
// does whatever fixture-building the case needs (which may include creating
// the very entity mutate will then update or retire) and returns its id;
// mutate performs THE ONE mutation under test.
type auditCase struct {
	// name identifies the row in test output.
	name string
	// entityType is the change_log.entity_type the mutation under test must
	// produce.
	entityType string
	// verb is which of create/update/retire this case exercises -- checked
	// against entityTypeVerbs by
	// TestEveryAuditMatrixEntityTypeCoversEveryApplicableVerb so the matrix
	// cannot be narrowed to create-only coverage of a type without that
	// narrowing being visible in a failing test, the way it was not the
	// first time this file was written.
	verb string
	// scope is asserted against domain.ScopeClassOf(entityType) so this file
	// cannot silently drift from internal/domain's own classification --
	// see TestTheAuditMatrixCoversEveryProjectScopedEntityType below.
	scope domain.ScopeClass
	// setup builds every fixture the mutation needs, INCLUDING the target
	// row for an update/retire/child-row case, and returns that row's id.
	// For a "create" case there is nothing to build yet, so setup returns
	// "" and mutate ignores it.
	setup func(t *testing.T, s *SQLStore, ctx context.Context) (entityID string)
	// mutate performs the mutation under test. It receives setup's id (used
	// by update/retire/child-row cases) and returns the id the mutation
	// itself acted on (which for a "create" case is the id just minted) and
	// the action change_log is expected to record for it.
	mutate func(t *testing.T, s *SQLStore, ctx context.Context, setupID string) (entityID, wantAction string)
}

const (
	verbCreate = "create"
	verbUpdate = "update"
	verbRetire = "retire"
)

// entityTypeVerbs is the hand-maintained expected coverage for every entity
// type in auditMatrix, in permitMinterBudget's style (permit_source_test.go):
// GOVERNANCE, NOT A SECURITY CONTROL. Nothing here can stop somebody editing
// both this map and the matrix in the same commit to narrow coverage on
// purpose; what it buys is that doing so is VISIBLE in the diff, and that
// forgetting to update the matrix after changing this map (or vice versa)
// fails TestEveryAuditMatrixEntityTypeCoversEveryApplicableVerb rather than
// shipping quietly.
//
// Three project-scoped and three estate-config/topology types are held to
// every verb their own store package implements: asset, service and circuit
// (create/update/retire), team, tag and custom_field (create/update/retire),
// and asset_kind/interface/link/prefix to whichever subset of
// create/update/retire the corresponding store methods actually provide --
// UpsertVocabularyTerm and UpdateInterface never delete a row, and
// RetireLink is the only mutation of an existing link (there is no
// UpdateLink). The three child-row types are the deliberate exception noted
// above this type's own doc comment: one representative mutation each, not
// full CRUD.
var entityTypeVerbs = map[string][]string{
	"asset":               {verbCreate, verbUpdate, verbRetire},
	"service":             {verbCreate, verbUpdate, verbRetire},
	"circuit":             {verbCreate, verbUpdate, verbRetire},
	"team":                {verbCreate, verbUpdate, verbRetire},
	"tag":                 {verbCreate, verbUpdate, verbRetire},
	"custom_field":        {verbCreate, verbUpdate, verbRetire},
	"asset_kind":          {verbCreate, verbUpdate},
	"interface":           {verbCreate, verbUpdate},
	"link":                {verbCreate, verbRetire},
	"prefix":              {verbCreate, verbUpdate},
	"asset_cost":          {verbCreate},
	"service_instance":    {verbCreate},
	"circuit_termination": {verbCreate},
}

// auditMatrix is deliberately not exhaustive against auditedEntityTypes (71
// entries, permit_source_test.go) -- it is the representative coverage
// task-8-brief.md asks for, expanded per entityTypeVerbs above.
var auditMatrix = []auditCase{
	// ---------------------------------------------------------------
	// Project-scoped: asset. A project owner may write these, so a
	// missing log here is a direct bypass for the role Piece 3 introduces.
	// ---------------------------------------------------------------
	{
		name:       "asset create",
		entityType: "asset",
		verb:       verbCreate,
		scope:      domain.ScopeProjectLinked,
		setup:      func(t *testing.T, s *SQLStore, ctx context.Context) string { return "" },
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, _ string) (string, string) {
			a, err := domain.NewAsset(NewID(), domain.KindServer, "audit-asset-create", nil, s.Now())
			if err != nil {
				t.Fatalf("building asset: %v", err)
			}
			if err := s.CreateAsset(ctx, testPermit, a, nil); err != nil {
				t.Fatalf("creating asset: %v", err)
			}
			return a.ID, domain.ActionCreate
		},
	},
	{
		name:       "asset update",
		entityType: "asset",
		verb:       verbUpdate,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			return mustAsset(t, s, ctx, domain.KindServer, "audit-asset-update", nil)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			row, err := s.GetAsset(ctx, id)
			if err != nil {
				t.Fatalf("getting asset: %v", err)
			}
			updated := row.Asset
			updated.Name = "audit-asset-update-renamed"
			if err := s.UpdateAsset(ctx, testPermit, &updated, nil); err != nil {
				t.Fatalf("updating asset: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "asset retire",
		entityType: "asset",
		verb:       verbRetire,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			return mustAsset(t, s, ctx, domain.KindServer, "audit-asset-retire", nil)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			if err := s.RetireAsset(ctx, testPermit, id); err != nil {
				t.Fatalf("retiring asset: %v", err)
			}
			return id, domain.ActionRetire
		},
	},
	{
		name:       "asset cost (child row)",
		entityType: "asset_cost",
		verb:       verbCreate,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			return mustAsset(t, s, ctx, domain.KindServer, "audit-asset-cost", nil)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, assetID string) (string, string) {
			c, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "operating", Period: domain.CostMonthly, AmountMinor: 1000,
			}, s.Now())
			if err != nil {
				t.Fatalf("building cost: %v", err)
			}
			if err := s.AddAssetCost(ctx, testActor, assetID, c); err != nil {
				t.Fatalf("adding asset cost: %v", err)
			}
			return c.ID, domain.ActionCreate
		},
	},

	// ---------------------------------------------------------------
	// Project-scoped: service.
	// ---------------------------------------------------------------
	{
		name:       "service create",
		entityType: "service",
		verb:       verbCreate,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			return mustEnvironment(t, s, ctx, "audit-svc-create-env", domain.EnvRoleProduction)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, envID string) (string, string) {
			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "audit-svc-create", Name: "Audit Service Create", Kind: domain.SvcAPI,
				EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 3,
			}, s.Now())
			if err != nil {
				t.Fatalf("building service: %v", err)
			}
			if err := s.CreateService(ctx, testPermit, svc); err != nil {
				t.Fatalf("creating service: %v", err)
			}
			return svc.ID, domain.ActionCreate
		},
	},
	{
		name:       "service update",
		entityType: "service",
		verb:       verbUpdate,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			envID := mustEnvironment(t, s, ctx, "audit-svc-update-env", domain.EnvRoleProduction)
			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "audit-svc-update", Name: "Audit Service Update", Kind: domain.SvcAPI,
				EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 3,
			}, s.Now())
			if err != nil {
				t.Fatalf("building service: %v", err)
			}
			if err := s.CreateService(ctx, testPermit, svc); err != nil {
				t.Fatalf("creating service: %v", err)
			}
			return svc.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			svc, err := s.GetService(ctx, id)
			if err != nil {
				t.Fatalf("getting service: %v", err)
			}
			updated := svc.Service
			updated.Name = "Audit Service Update Renamed"
			if err := s.UpdateService(ctx, testPermit, &updated); err != nil {
				t.Fatalf("updating service: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "service retire",
		entityType: "service",
		verb:       verbRetire,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			envID := mustEnvironment(t, s, ctx, "audit-svc-retire-env", domain.EnvRoleProduction)
			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "audit-svc-retire", Name: "Audit Service Retire", Kind: domain.SvcAPI,
				EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 3,
			}, s.Now())
			if err != nil {
				t.Fatalf("building service: %v", err)
			}
			if err := s.CreateService(ctx, testPermit, svc); err != nil {
				t.Fatalf("creating service: %v", err)
			}
			return svc.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			if err := s.RetireService(ctx, testPermit, id); err != nil {
				t.Fatalf("retiring service: %v", err)
			}
			return id, domain.ActionRetire
		},
	},
	{
		name:       "service instance (child row)",
		entityType: "service_instance",
		verb:       verbCreate,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			envID := mustEnvironment(t, s, ctx, "audit-svc-instance-env", domain.EnvRoleProduction)
			hostID := mustAsset(t, s, ctx, domain.KindServer, "audit-svc-instance-host", nil, envID)
			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "audit-svc-instance", Name: "Audit Service Instance", Kind: domain.SvcAPI,
				EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 3,
			}, s.Now())
			if err != nil {
				t.Fatalf("building service: %v", err)
			}
			if err := s.CreateService(ctx, testPermit, svc); err != nil {
				t.Fatalf("creating service: %v", err)
			}
			return svc.ID + "|" + hostID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, setupID string) (string, string) {
			svcID, hostID := splitPair(t, setupID)
			si, err := domain.NewServiceInstance(NewID(), svcID, hostID, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, testPermit, si); err != nil {
				t.Fatalf("creating instance: %v", err)
			}
			return si.ID, domain.ActionCreate
		},
	},

	// ---------------------------------------------------------------
	// Project-scoped: circuit.
	// ---------------------------------------------------------------
	{
		name:       "circuit create",
		entityType: "circuit",
		verb:       verbCreate,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			return mustProvider(t, s, ctx, "Audit Provider Create")
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, providerID string) (string, string) {
			c, err := domain.NewCircuit(NewID(), "audit-circuit-create", providerID)
			if err != nil {
				t.Fatalf("building circuit: %v", err)
			}
			if err := s.CreateCircuit(ctx, testPermit, c); err != nil {
				t.Fatalf("creating circuit: %v", err)
			}
			return c.ID, domain.ActionCreate
		},
	},
	{
		name:       "circuit update",
		entityType: "circuit",
		verb:       verbUpdate,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			providerID := mustProvider(t, s, ctx, "Audit Provider Update")
			return mustCircuit(t, s, ctx, providerID, "audit-circuit-update", nil)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			c, err := s.GetCircuit(ctx, id)
			if err != nil {
				t.Fatalf("getting circuit: %v", err)
			}
			desc := "renamed by the audit matrix"
			c.Description = &desc
			if err := s.UpdateCircuit(ctx, testPermit, c); err != nil {
				t.Fatalf("updating circuit: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "circuit retire",
		entityType: "circuit",
		verb:       verbRetire,
		scope:      domain.ScopeProjectLinked,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			providerID := mustProvider(t, s, ctx, "Audit Provider Retire")
			return mustCircuit(t, s, ctx, providerID, "audit-circuit-retire", nil)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			if err := s.RetireCircuit(ctx, testPermit, id); err != nil {
				t.Fatalf("retiring circuit: %v", err)
			}
			// RetireCircuit calls logUpdate, not logCreate/log(...,
			// ActionRetire, ...) the way RetireAsset/RetireService do -- a
			// circuit's retirement is recorded as a lifecycle field update,
			// not a distinct action. Both are legitimate; this case asserts
			// what the code actually does, not what a naming convention
			// might suggest it should.
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "circuit termination (child row)",
		entityType: "circuit_termination",
		verb:       verbCreate,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			providerID := mustProvider(t, s, ctx, "Audit Provider Termination")
			circuitID := mustCircuit(t, s, ctx, providerID, "audit-circuit-termination", nil)
			assetID := mustAsset(t, s, ctx, domain.KindSite, "audit-circuit-termination-site", nil)
			return circuitID + "|" + assetID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, setupID string) (string, string) {
			circuitID, assetID := splitPair(t, setupID)
			term, err := domain.NewCircuitTermination(NewID(), circuitID, "a", &assetID, nil)
			if err != nil {
				t.Fatalf("building termination: %v", err)
			}
			if err := s.CreateCircuitTermination(ctx, testPermit, term); err != nil {
				t.Fatalf("landing termination: %v", err)
			}
			return term.ID, domain.ActionCreate
		},
	},

	// ---------------------------------------------------------------
	// Estate config -- a project owner is REFUSED these. A missing log here
	// is a write that role can perform, not one it is confined to. Every one
	// of team/tag/custom_field is exercised on create, update AND retire --
	// see this file's own top-of-file note on why create-only coverage of
	// this bucket was proven insufficient during review.
	// ---------------------------------------------------------------
	{
		name:       "team create",
		entityType: "team",
		verb:       verbCreate,
		scope:      domain.ScopeEstateConfig,
		setup:      func(t *testing.T, s *SQLStore, ctx context.Context) string { return "" },
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, _ string) (string, string) {
			team, err := domain.NewTeam(NewID(), domain.TeamSpec{
				Code: "audit-team-create", Name: "Audit Team Create",
			}, s.Now())
			if err != nil {
				t.Fatalf("building team: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating team: %v", err)
			}
			return team.ID, domain.ActionCreate
		},
	},
	{
		name: "team update -- THE CASE THAT PROVED CREATE-ONLY COVERAGE " +
			"INSUFFICIENT: a project owner is refused write access to team " +
			"entirely, so an unlogged EDIT of an existing team is a bypass " +
			"the earlier create-only case could never have caught",
		entityType: "team",
		verb:       verbUpdate,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			team, err := domain.NewTeam(NewID(), domain.TeamSpec{
				Code: "audit-team-update", Name: "Audit Team Update",
			}, s.Now())
			if err != nil {
				t.Fatalf("building team: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating team: %v", err)
			}
			return team.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			row, err := s.GetTeam(ctx, id)
			if err != nil {
				t.Fatalf("getting team: %v", err)
			}
			updated := row.Team
			updated.Name = "Audit Team Update Renamed"
			if err := s.UpdateTeam(ctx, testActor, &updated); err != nil {
				t.Fatalf("updating team: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "team retire",
		entityType: "team",
		verb:       verbRetire,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			team, err := domain.NewTeam(NewID(), domain.TeamSpec{
				Code: "audit-team-retire", Name: "Audit Team Retire",
			}, s.Now())
			if err != nil {
				t.Fatalf("building team: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating team: %v", err)
			}
			return team.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			if err := s.RetireTeam(ctx, testActor, id); err != nil {
				t.Fatalf("retiring team: %v", err)
			}
			return id, domain.ActionRetire
		},
	},
	{
		name:       "tag create",
		entityType: "tag",
		verb:       verbCreate,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			// tag.created_by is a foreign key into app_user -- testActor's
			// bare id satisfies nothing there, the way tags_test.go's own
			// tagFixture already has to work around. Packed as
			// "id|username" so mutate can rebuild the domain.Actor it needs
			// for retire (RetireTag writes retired_by, also FK-constrained)
			// without a second lookup.
			actor := mustAuditActor(t, s, ctx, "audit-tag-admin-create")
			return actor.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, createdBy string) (string, string) {
			tag, err := domain.NewTag(NewID(), "audit-tag-create", "Audit Tag Create",
				"a fixture tag for the positive audit matrix", createdBy, s.Now())
			if err != nil {
				t.Fatalf("building tag: %v", err)
			}
			if err := s.CreateTag(ctx, testActor, tag); err != nil {
				t.Fatalf("creating tag: %v", err)
			}
			return tag.ID, domain.ActionCreate
		},
	},
	{
		name:       "tag update",
		entityType: "tag",
		verb:       verbUpdate,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			actor := mustAuditActor(t, s, ctx, "audit-tag-admin-update")
			tag, err := domain.NewTag(NewID(), "audit-tag-update", "Audit Tag Update",
				"a fixture tag for the positive audit matrix", actor.ID, s.Now())
			if err != nil {
				t.Fatalf("building tag: %v", err)
			}
			if err := s.CreateTag(ctx, actor, tag); err != nil {
				t.Fatalf("creating tag: %v", err)
			}
			return tag.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			row, err := s.GetTag(ctx, id)
			if err != nil {
				t.Fatalf("getting tag: %v", err)
			}
			updated := row.Tag
			updated.Label = "Audit Tag Update Renamed"
			if err := s.UpdateTag(ctx, testActor, &updated); err != nil {
				t.Fatalf("updating tag: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "tag retire",
		entityType: "tag",
		verb:       verbRetire,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			actor := mustAuditActor(t, s, ctx, "audit-tag-admin-retire")
			tag, err := domain.NewTag(NewID(), "audit-tag-retire", "Audit Tag Retire",
				"a fixture tag for the positive audit matrix", actor.ID, s.Now())
			if err != nil {
				t.Fatalf("building tag: %v", err)
			}
			if err := s.CreateTag(ctx, actor, tag); err != nil {
				t.Fatalf("creating tag: %v", err)
			}
			// Returned UNPACKED, unlike the child-row cases above: mutate
			// derives the app_user id RetireTag needs (retired_by is
			// FK-constrained) from the row's own CreatedBy rather than a
			// packed string, so setupID stays equal to the id mutate acts
			// on and the baseline calculation below stays correct.
			return tag.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			row, err := s.GetTag(ctx, id)
			if err != nil {
				t.Fatalf("getting tag: %v", err)
			}
			actor := domain.Actor{ID: row.CreatedBy, Name: row.CreatedBy, Kind: domain.ActorKindUser}
			if err := s.RetireTag(ctx, actor, id); err != nil {
				t.Fatalf("retiring tag: %v", err)
			}
			return id, domain.ActionRetire
		},
	},
	{
		name:       "custom field create",
		entityType: "custom_field",
		verb:       verbCreate,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			actor := mustAuditActor(t, s, ctx, "audit-cf-admin-create")
			team := mustAuditOwnerTeam(t, s, ctx, "audit-cf-owner-create")
			return actor.ID + "|" + team
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, setupID string) (string, string) {
			createdBy, teamID := splitPair(t, setupID)
			cf, err := domain.NewCustomField(NewID(), domain.CustomFieldEntityAsset,
				"audit_cf_create", "Audit Field Create", domain.CustomFieldText,
				"a fixture field for the positive audit matrix", createdBy, teamID, s.Now())
			if err != nil {
				t.Fatalf("building custom field: %v", err)
			}
			if err := s.CreateCustomField(ctx, testActor, cf); err != nil {
				t.Fatalf("creating custom field: %v", err)
			}
			return cf.ID, domain.ActionCreate
		},
	},
	{
		name: "custom field update -- checked for the same create-only gap " +
			"as team, since a project owner is refused write access to " +
			"custom_field entirely too",
		entityType: "custom_field",
		verb:       verbUpdate,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			actor := mustAuditActor(t, s, ctx, "audit-cf-admin-update")
			team := mustAuditOwnerTeam(t, s, ctx, "audit-cf-owner-update")
			cf, err := domain.NewCustomField(NewID(), domain.CustomFieldEntityAsset,
				"audit_cf_update", "Audit Field Update", domain.CustomFieldText,
				"a fixture field for the positive audit matrix", actor.ID, team, s.Now())
			if err != nil {
				t.Fatalf("building custom field: %v", err)
			}
			if err := s.CreateCustomField(ctx, testActor, cf); err != nil {
				t.Fatalf("creating custom field: %v", err)
			}
			return cf.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			row, err := s.GetCustomField(ctx, id)
			if err != nil {
				t.Fatalf("getting custom field: %v", err)
			}
			updated := row.CustomField
			updated.Label = "Audit Field Update Renamed"
			if err := s.UpdateCustomField(ctx, testActor, &updated); err != nil {
				t.Fatalf("updating custom field: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "custom field retire",
		entityType: "custom_field",
		verb:       verbRetire,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			actor := mustAuditActor(t, s, ctx, "audit-cf-admin-retire")
			team := mustAuditOwnerTeam(t, s, ctx, "audit-cf-owner-retire")
			cf, err := domain.NewCustomField(NewID(), domain.CustomFieldEntityAsset,
				"audit_cf_retire", "Audit Field Retire", domain.CustomFieldText,
				"a fixture field for the positive audit matrix", actor.ID, team, s.Now())
			if err != nil {
				t.Fatalf("building custom field: %v", err)
			}
			if err := s.CreateCustomField(ctx, testActor, cf); err != nil {
				t.Fatalf("creating custom field: %v", err)
			}
			// Returned UNPACKED for the same reason as the tag retire case
			// above: mutate derives the FK-safe actor from the row's own
			// CreatedBy, so setupID stays equal to the id mutate acts on.
			return cf.ID
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			row, err := s.GetCustomField(ctx, id)
			if err != nil {
				t.Fatalf("getting custom field: %v", err)
			}
			actor := domain.Actor{ID: row.CreatedBy, Name: row.CreatedBy, Kind: domain.ActorKindUser}
			if err := s.RetireCustomField(ctx, actor, id); err != nil {
				t.Fatalf("retiring custom field: %v", err)
			}
			return id, domain.ActionRetire
		},
	},
	{
		name: "vocabulary term create -- THE CASE A NARROWER MATRIX WOULD " +
			"HAVE MISSED: a project owner is refused write access to asset_kind " +
			"entirely, so an unlogged write here is a bypass of that refusal, " +
			"not merely a hole in what a project owner may already touch",
		entityType: "asset_kind",
		verb:       verbCreate,
		scope:      domain.ScopeEstateConfig,
		setup:      func(t *testing.T, s *SQLStore, ctx context.Context) string { return "" },
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, _ string) (string, string) {
			code := "audit-vocab-create"
			if err := s.UpsertVocabularyTerm(ctx, testActor, "asset_kind", VocabularyTerm{
				Code: code, Label: "Audit Vocabulary Term",
			}); err != nil {
				t.Fatalf("upserting vocabulary term: %v", err)
			}
			return code, domain.ActionCreate
		},
	},
	{
		name:       "vocabulary term update",
		entityType: "asset_kind",
		verb:       verbUpdate,
		scope:      domain.ScopeEstateConfig,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			code := "audit-vocab-update"
			if err := s.UpsertVocabularyTerm(ctx, testActor, "asset_kind", VocabularyTerm{
				Code: code, Label: "Audit Vocabulary Term Update",
			}); err != nil {
				t.Fatalf("upserting vocabulary term: %v", err)
			}
			return code
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, code string) (string, string) {
			if err := s.UpsertVocabularyTerm(ctx, testActor, "asset_kind", VocabularyTerm{
				Code: code, Label: "Audit Vocabulary Term Update Renamed",
			}); err != nil {
				t.Fatalf("upserting vocabulary term: %v", err)
			}
			return code, domain.ActionUpdate
		},
	},

	// ---------------------------------------------------------------
	// Topology -- also refused to a project owner.
	// ---------------------------------------------------------------
	{
		name:       "interface create",
		entityType: "interface",
		verb:       verbCreate,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			return mustAsset(t, s, ctx, domain.KindServer, "audit-interface-host", nil)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, assetID string) (string, string) {
			iface, err := domain.NewInterface(NewID(), assetID, "eth-audit", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building interface: %v", err)
			}
			if err := s.CreateInterface(ctx, testActor, iface); err != nil {
				t.Fatalf("creating interface: %v", err)
			}
			return iface.ID, domain.ActionCreate
		},
	},
	{
		name:       "interface update",
		entityType: "interface",
		verb:       verbUpdate,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			assetID := mustAsset(t, s, ctx, domain.KindServer, "audit-interface-update-host", nil)
			return mustInterface(t, s, ctx, assetID, "eth-audit-update")
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			iface, err := s.GetInterface(ctx, id)
			if err != nil {
				t.Fatalf("getting interface: %v", err)
			}
			updated := *iface
			mtu := 9000
			updated.MTU = &mtu
			if err := s.UpdateInterface(ctx, testActor, &updated); err != nil {
				t.Fatalf("updating interface: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
	{
		name:       "link create",
		entityType: "link",
		verb:       verbCreate,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			aAsset := mustAsset(t, s, ctx, domain.KindSwitch, "audit-link-a", nil)
			bAsset := mustAsset(t, s, ctx, domain.KindServer, "audit-link-b", nil)
			aPort := mustInterface(t, s, ctx, aAsset, "eth-a")
			bPort := mustInterface(t, s, ctx, bAsset, "eth-b")
			return aPort + "|" + bPort
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, setupID string) (string, string) {
			aPort, bPort := splitPair(t, setupID)
			l, err := domain.NewLink(NewID(), aPort, bPort)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, l); err != nil {
				t.Fatalf("creating link: %v", err)
			}
			return l.ID, domain.ActionCreate
		},
	},
	{
		name:       "link retire",
		entityType: "link",
		verb:       verbRetire,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			aAsset := mustAsset(t, s, ctx, domain.KindSwitch, "audit-link-retire-a", nil)
			bAsset := mustAsset(t, s, ctx, domain.KindServer, "audit-link-retire-b", nil)
			aPort := mustInterface(t, s, ctx, aAsset, "eth-a")
			bPort := mustInterface(t, s, ctx, bAsset, "eth-b")
			return mustCable(t, s, ctx, aPort, bPort)
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			if err := s.RetireLink(ctx, testActor, id); err != nil {
				t.Fatalf("retiring link: %v", err)
			}
			return id, domain.ActionRetire
		},
	},
	{
		name:       "prefix create",
		entityType: "prefix",
		verb:       verbCreate,
		scope:      domain.ScopeTopology,
		setup:      func(t *testing.T, s *SQLStore, ctx context.Context) string { return "" },
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, _ string) (string, string) {
			p, err := domain.NewPrefix(NewID(), "10.222.0.0/24")
			if err != nil {
				t.Fatalf("building prefix: %v", err)
			}
			if err := s.CreatePrefix(ctx, testActor, p); err != nil {
				t.Fatalf("creating prefix: %v", err)
			}
			return p.ID, domain.ActionCreate
		},
	},
	{
		name:       "prefix update",
		entityType: "prefix",
		verb:       verbUpdate,
		scope:      domain.ScopeTopology,
		setup: func(t *testing.T, s *SQLStore, ctx context.Context) string {
			return mustPrefix(t, s, ctx, "10.223.0.0/24")
		},
		mutate: func(t *testing.T, s *SQLStore, ctx context.Context, id string) (string, string) {
			p, err := s.GetPrefix(ctx, id)
			if err != nil {
				t.Fatalf("getting prefix: %v", err)
			}
			role := "audit-role"
			p.Role = &role
			if err := s.UpdatePrefix(ctx, testActor, p); err != nil {
				t.Fatalf("updating prefix: %v", err)
			}
			return id, domain.ActionUpdate
		},
	},
}

// mustAuditActor creates a real app_user row and returns the domain.Actor
// for it, for the handful of cases whose created_by/retired_by column is a
// foreign key into app_user rather than accepting testActor's bare id --
// the same workaround tags_test.go's tagFixture already needs.
func mustAuditActor(t *testing.T, s *SQLStore, ctx context.Context, username string) domain.Actor {
	t.Helper()
	user, err := domain.NewAppUser(NewID(), username, domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building fixture user %s: %v", username, err)
	}
	if err := s.CreateUser(ctx, testActor, user); err != nil {
		t.Fatalf("creating fixture user %s: %v", username, err)
	}
	return domain.UserActor(user)
}

// mustAuditOwnerTeam creates a live team and returns its id, for the custom
// field cases' required owner_team_id.
func mustAuditOwnerTeam(t *testing.T, s *SQLStore, ctx context.Context, code string) string {
	t.Helper()
	team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: code, Name: code}, s.Now())
	if err != nil {
		t.Fatalf("building owner team %s: %v", code, err)
	}
	if err := s.CreateTeam(ctx, testActor, team); err != nil {
		t.Fatalf("creating owner team %s: %v", code, err)
	}
	return team.ID
}

// splitPair undoes the "a|b" packing setup funcs above use to pass two ids
// through the single string auditCase.setup returns.
func splitPair(t *testing.T, packed string) (a, b string) {
	t.Helper()
	for i := 0; i < len(packed); i++ {
		if packed[i] == '|' {
			return packed[:i], packed[i+1:]
		}
	}
	t.Fatalf("splitPair: %q has no '|' separator", packed)
	return "", ""
}

// TestEveryDeclaredMutationWritesExactlyOneChangeLogRow is the positive audit
// matrix task-8-brief.md asks for. setup builds whatever fixture the
// mutation needs (which may itself log several change_log rows -- an update
// or retire case's setup CREATES the row mutate goes on to act on); mutate
// then performs the one mutation under test, and the change_log rows for
// that mutation's own (entity_type, entity_id) must have grown by exactly
// one since setup finished.
//
// The "grown by" is measured, not assumed to start at zero: an update or
// retire case's mutate acts on the SAME id setup just created (setupID ==
// entityID), which already carries setup's own create row, so the baseline
// is read for that id before mutate runs. A create or child-row case mints a
// brand new id inside mutate, which cannot coincide with setupID (a bare
// UUID, an empty string, or a "prereq|prereq" pair of a DIFFERENT entity
// type's ids), so its baseline is correctly zero without special-casing it.
//
// ListChangesForEntity filters on entity_type AND entity_id in its own WHERE
// clause, so "exactly one new row with exactly that type and id" is not a
// separate assertion bolted on afterwards -- it is what the query already
// enforces; what this test adds on top is that the count moved by exactly
// one, and that the row records the expected action.
func TestEveryDeclaredMutationWritesExactlyOneChangeLogRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, c := range auditMatrix {
				t.Run(c.name, func(t *testing.T) {
					s, ctx := newStore(t, e)

					setupID := c.setup(t, s, ctx)
					before, err := s.ListChangesForEntity(ctx, c.entityType, setupID, 50)
					if err != nil {
						t.Fatalf("listing baseline changes for %s %s: %v", c.entityType, setupID, err)
					}

					entityID, wantAction := c.mutate(t, s, ctx, setupID)

					baseline := 0
					if entityID == setupID {
						baseline = len(before)
					}

					changes, err := s.ListChangesForEntity(ctx, c.entityType, entityID, 50)
					if err != nil {
						t.Fatalf("listing changes for %s %s: %v", c.entityType, entityID, err)
					}
					if got := len(changes) - baseline; got != 1 {
						t.Fatalf("mutate added %d change_log rows for (%s, %s), want exactly 1 -- "+
							"a mutation of declared state that does not log exactly once is, "+
							"from Task 7 onward, an authorization bypass rather than merely a "+
							"missing audit entry: tx.log is the only object-level authorization "+
							"gate this codebase has", got, c.entityType, entityID)
					}
					if changes[0].Action != wantAction {
						t.Errorf("action = %s, want %s", changes[0].Action, wantAction)
					}
				})
			}
		})
	}
}

// TestTheAuditMatrixCoversEveryProjectScopedEntityType stops the matrix
// above being silently narrowed the day a fourth type becomes
// domain.ScopeProjectLinked. Task 7 classifies exactly three entity types
// that way today (asset, service, circuit); if a future task adds a fourth
// without adding it to auditMatrix, this test fails rather than letting the
// positive matrix quietly stop covering the role's own write surface.
func TestTheAuditMatrixCoversEveryProjectScopedEntityType(t *testing.T) {
	wantTypes := map[string]bool{}
	for _, et := range domain.ClassifiedTables() {
		if domain.ScopeClassOf(et) == domain.ScopeProjectLinked {
			wantTypes[et] = true
		}
	}
	if len(wantTypes) == 0 {
		t.Fatal("no entity type classifies as domain.ScopeProjectLinked -- either " +
			"domain.ClassifiedTables() or entityScope regressed, and this test caught " +
			"neither of the things it exists to guard, it caught a third")
	}

	coveredTypes := map[string]bool{}
	for _, c := range auditMatrix {
		if c.scope == domain.ScopeProjectLinked {
			coveredTypes[c.entityType] = true
		}
	}

	for et := range wantTypes {
		if !coveredTypes[et] {
			t.Errorf("%q is domain.ScopeProjectLinked but auditMatrix has no case for it -- "+
				"a project owner may write this entity type, so a case proving it still "+
				"logs is not optional coverage, it is what stands between that role and an "+
				"unchecked write", et)
		}
	}
	for et := range coveredTypes {
		if !wantTypes[et] {
			t.Errorf("%q is marked domain.ScopeProjectLinked in auditMatrix but "+
				"domain.ScopeClassOf disagrees -- the matrix's own scope field has drifted "+
				"from internal/domain's classification", et)
		}
	}
}

// TestEveryAuditMatrixEntityTypeCoversEveryApplicableVerb stops the matrix
// being narrowed to create-only coverage of a type the way this file's first
// draft was: suppressing UpdateTeam's tx.logUpdate call compiled and left
// that draft's matrix entirely green, because it exercised "team create" and
// nothing else. This test compares, for every entity type appearing in
// auditMatrix, the set of verbs (create/update/retire) it is actually
// exercised on against entityTypeVerbs' hand-maintained expectation --
// exact equality in both directions, so a case silently dropped and a case
// silently added both fail rather than one of them passing by accident.
//
// Like permitMinterBudget, this is governance, not a security control on its
// own: entityTypeVerbs can be edited in the same commit that narrows
// coverage. What it buys is that the narrowing is visible in the diff, and
// that editing one without the other -- the exact way UpdateTeam's missing
// case went unnoticed -- fails the build instead of shipping quietly.
func TestEveryAuditMatrixEntityTypeCoversEveryApplicableVerb(t *testing.T) {
	covered := map[string]map[string]bool{}
	for _, c := range auditMatrix {
		if c.verb == "" {
			t.Errorf("auditMatrix case %q has no verb set", c.name)
			continue
		}
		if covered[c.entityType] == nil {
			covered[c.entityType] = map[string]bool{}
		}
		if covered[c.entityType][c.verb] {
			t.Errorf("entity type %q has more than one auditMatrix case for verb %q",
				c.entityType, c.verb)
		}
		covered[c.entityType][c.verb] = true
	}

	for entityType, wantVerbs := range entityTypeVerbs {
		got, ok := covered[entityType]
		if !ok {
			t.Errorf("entityTypeVerbs expects auditMatrix to cover %q but no case names it", entityType)
			continue
		}
		want := map[string]bool{}
		for _, v := range wantVerbs {
			want[v] = true
			if !got[v] {
				t.Errorf("%q is expected to cover verb %q (entityTypeVerbs) but auditMatrix has "+
					"no case for it -- this is the exact shape of gap that let a suppressed "+
					"UpdateTeam pass the whole matrix undetected", entityType, v)
			}
		}
		for v := range got {
			if !want[v] {
				t.Errorf("auditMatrix covers %q verb %q, which entityTypeVerbs does not expect -- "+
					"either the case is redundant or entityTypeVerbs is stale", entityType, v)
			}
		}
	}

	for entityType := range covered {
		if _, ok := entityTypeVerbs[entityType]; !ok {
			t.Errorf("auditMatrix has a case for entity type %q but entityTypeVerbs does not "+
				"list it -- add it so this test can hold its coverage to a stated, checked bar "+
				"rather than whatever happened to be written", entityType)
		}
	}
}
