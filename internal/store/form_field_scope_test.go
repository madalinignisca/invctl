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
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G1 Task 15: "entities identified by a form field, not a path".
//
// HealthOverrideCreate's `target` (split by splitTarget), VocabularyUpsert's
// lookup table, CertificateDeployAsset/DeployService's asset_id/service_id,
// AssetStorageClaim's pool_id, OwnershipAssign's entity_type+ids and
// bulkApplyTag's entity_type+selections all name the row to write from a
// FORM FIELD rather than a URL path segment. The brief's claim: nothing in
// splitTarget or any of these forms' parsers needs to be trusted, because
// the refusal happens on the row tx.log is actually about to write, not on
// whatever the request claimed to be naming. Every case below builds an
// out-of-scope target exactly the way a form field naming it would, calls
// the store method the handler itself calls, and checks the same two
// things: domain.ErrForbidden (never some other error, never success), and
// nothing persisted.
//
// Driven against the permit layer directly, not HTTP -- see
// project_create_test.go's package comment for why: CanWrite(RoleProjectOwner)
// is still false, so a real request from a project owner never reaches any
// of these handlers today.

func TestAnOutOfScopeEntityNamedInAFormFieldIsRefusedJustAsAPathParameterWouldBe(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			cases := []struct {
				name string
				run  func(t *testing.T, s *SQLStore, ctx context.Context)
			}{
				{
					name: "HealthOverrideCreate/target",
					run: func(t *testing.T, s *SQLStore, ctx context.Context) {
						assetID := mustAsset(t, s, ctx, domain.KindServer, "ff-health-asset", nil)
						poID := mustEntityTagsScopeUser(t, s, ctx, "ff-po-health")
						permit := domain.ScopedPermit(
							domain.Actor{ID: poID, Name: "ff-po-health", Kind: domain.ActorKindUser},
							nil, nil)
						o, err := domain.NewHealthOverride(NewID(), domain.HealthOverrideSpec{
							EntityType: domain.ObservableAsset, EntityID: assetID,
							AssertedState: string(domain.HealthDown), Reason: "form-field scope test",
							ExpiresAt: s.Now().Add(domain.MaxOverrideDuration),
						}, permit.Actor(), s.Now())
						if err != nil {
							t.Fatalf("building override: %v", err)
						}
						err = s.CreateHealthOverride(ctx, permit, o)
						if !errors.Is(err, domain.ErrForbidden) {
							t.Fatalf("CreateHealthOverride = %v, want domain.ErrForbidden", err)
						}
						active, err := s.ActiveOverride(ctx, domain.ObservableAsset, assetID)
						if err != nil {
							t.Fatalf("ActiveOverride: %v", err)
						}
						if active != nil {
							t.Error("a health override was created for an out-of-scope asset")
						}
					},
				},
				{
					name: "VocabularyUpsert/table",
					run: func(t *testing.T, s *SQLStore, ctx context.Context) {
						poID := mustEntityTagsScopeUser(t, s, ctx, "ff-po-vocab")
						permit := domain.ScopedPermit(
							domain.Actor{ID: poID, Name: "ff-po-vocab", Kind: domain.ActorKindUser},
							nil, nil)
						err := s.UpsertVocabularyTerm(ctx, permit, "asset_kind",
							VocabularyTerm{Code: "ff-scope-term", Label: "Scope test term"})
						if !errors.Is(err, domain.ErrForbidden) {
							t.Fatalf("UpsertVocabularyTerm = %v, want domain.ErrForbidden", err)
						}
						terms, err := s.AssetKinds(ctx)
						if err != nil {
							t.Fatalf("AssetKinds: %v", err)
						}
						for _, term := range terms {
							if term.Code == "ff-scope-term" {
								t.Error("a vocabulary term was created by a project owner")
							}
						}
					},
				},
				{
					name: "CertificateDeployAsset/asset_id",
					run: func(t *testing.T, s *SQLStore, ctx context.Context) {
						assetID := mustAsset(t, s, ctx, domain.KindServer, "ff-cert-asset", nil)
						certID := mustCertificate(t, s, ctx, "ff-cert.example.invalid", nil, "")
						poID := mustEntityTagsScopeUser(t, s, ctx, "ff-po-cert-asset")
						permit := domain.ScopedPermit(
							domain.Actor{ID: poID, Name: "ff-po-cert-asset", Kind: domain.ActorKindUser},
							nil, domain.ScopedEntities{"asset": {assetID: true}})
						err := s.DeployCertificateToAsset(ctx, permit, certID, assetID, nil)
						if !errors.Is(err, domain.ErrForbidden) {
							t.Fatalf("DeployCertificateToAsset = %v, want domain.ErrForbidden", err)
						}
						deployments, err := s.ListCertificateAssets(ctx, certID)
						if err != nil {
							t.Fatalf("CertificateAssets: %v", err)
						}
						if len(deployments) != 0 {
							t.Error("a certificate deployment was recorded against an out-of-scope permit")
						}
					},
				},
				{
					name: "CertificateDeployService/service_id",
					run: func(t *testing.T, s *SQLStore, ctx context.Context) {
						env := mustEnvironment(t, s, ctx, "ff-cert-env", domain.EnvRoleProduction)
						svc, err := domain.NewService(NewID(), domain.ServiceSpec{
							Code: "ff-cert-svc", Name: "ff-cert-svc", Kind: domain.SvcAPI,
							EnvironmentID: env, Availability: domain.AvailStandalone, Tier: 2,
						}, s.Now())
						if err != nil {
							t.Fatalf("building service: %v", err)
						}
						if err := s.CreateService(ctx, testPermit, svc); err != nil {
							t.Fatalf("creating service: %v", err)
						}
						certID := mustCertificate(t, s, ctx, "ff-cert-svc.example.invalid", nil, "")
						poID := mustEntityTagsScopeUser(t, s, ctx, "ff-po-cert-svc")
						permit := domain.ScopedPermit(
							domain.Actor{ID: poID, Name: "ff-po-cert-svc", Kind: domain.ActorKindUser},
							nil, domain.ScopedEntities{"service": {svc.ID: true}})
						err = s.DeployCertificateToService(ctx, permit, certID, svc.ID, nil)
						if !errors.Is(err, domain.ErrForbidden) {
							t.Fatalf("DeployCertificateToService = %v, want domain.ErrForbidden", err)
						}
						deployments, err := s.ListCertificateServices(ctx, certID)
						if err != nil {
							t.Fatalf("CertificateServices: %v", err)
						}
						if len(deployments) != 0 {
							t.Error("a certificate deployment was recorded against an out-of-scope permit")
						}
					},
				},
				{
					name: "AssetStorageClaim/pool_id",
					run: func(t *testing.T, s *SQLStore, ctx context.Context) {
						poolID := mustAsset(t, s, ctx, domain.KindStorage, "ff-pool", nil)
						workloadID := mustAsset(t, s, ctx, domain.KindVM, "ff-workload", nil)
						poID := mustEntityTagsScopeUser(t, s, ctx, "ff-po-storage")
						// Holds the WORKLOAD, not the pool -- the audit rides
						// the workload (storage.go's own doc comment), so this
						// is the out-of-scope half that matters.
						permit := domain.ScopedPermit(
							domain.Actor{ID: poID, Name: "ff-po-storage", Kind: domain.ActorKindUser},
							nil, domain.ScopedEntities{"asset": {}})
						err := s.SetStorageClaim(ctx, permit, workloadID, poolID, 10, nil)
						if !errors.Is(err, domain.ErrForbidden) {
							t.Fatalf("SetStorageClaim = %v, want domain.ErrForbidden", err)
						}
						claims, err := s.StorageClaimsFor(ctx, workloadID)
						if err != nil {
							t.Fatalf("StorageClaimsFor: %v", err)
						}
						if len(claims) != 0 {
							t.Error("a storage claim was recorded for an out-of-scope workload")
						}
					},
				},
				{
					name: "OwnershipAssign/entity_type+ids",
					run: func(t *testing.T, s *SQLStore, ctx context.Context) {
						assetID := mustAsset(t, s, ctx, domain.KindServer, "ff-own-asset", nil)
						teamID := mustEntityTagsScopeTeam(t, s, ctx, "ff-own-team")
						poID := mustEntityTagsScopeUser(t, s, ctx, "ff-po-own")
						permit := domain.ScopedPermit(
							domain.Actor{ID: poID, Name: "ff-po-own", Kind: domain.ActorKindUser},
							nil, nil)
						outcomes, err := s.BulkAssignOwnership(ctx, permit, "asset", []string{assetID}, teamID)
						if err != nil {
							t.Fatalf("BulkAssignOwnership: %v", err)
						}
						if len(outcomes) != 1 || outcomes[0].Result != ReassignFailed {
							t.Fatalf("outcomes = %+v, want exactly one ReassignFailed", outcomes)
						}
						asset, err := s.GetAsset(ctx, assetID)
						if err != nil {
							t.Fatalf("GetAsset: %v", err)
						}
						if asset.TeamID != nil {
							t.Error("the out-of-scope asset was assigned a team anyway")
						}
					},
				},
				{
					name: "bulkApplyTag/entity_type+selections",
					run: func(t *testing.T, s *SQLStore, ctx context.Context) {
						assetID := mustAsset(t, s, ctx, domain.KindServer, "ff-bulktag-asset", nil)
						asset, err := s.GetAsset(ctx, assetID)
						if err != nil {
							t.Fatalf("GetAsset: %v", err)
						}
						tagAdmin := mustEntityTagsScopeUser(t, s, ctx, "ff-tag-admin")
						tag, err := domain.NewTag(NewID(), "ff-bulk-tag", "FF bulk tag", "a fixture tag", tagAdmin, s.Now())
						if err != nil {
							t.Fatalf("building tag: %v", err)
						}
						if err := s.CreateTag(ctx, testPermit, tag); err != nil {
							t.Fatalf("creating tag: %v", err)
						}
						poID := mustEntityTagsScopeUser(t, s, ctx, "ff-po-bulktag")
						permit := domain.ScopedPermit(
							domain.Actor{ID: poID, Name: "ff-po-bulktag", Kind: domain.ActorKindUser},
							nil, nil)
						outcomes, err := s.ApplyTagToSelection(ctx, permit, domain.TagEntityAsset, tag.ID,
							[]TagSelection{{ID: assetID, RowVersion: asset.RowVersion}})
						if err != nil {
							t.Fatalf("ApplyTagToSelection: %v", err)
						}
						if len(outcomes) != 1 || outcomes[0].Result != TagApplyFailed {
							t.Fatalf("outcomes = %+v, want exactly one TagApplyFailed", outcomes)
						}
						applied, err := s.EntityTagsFor(ctx, domain.TagEntityAsset, assetID)
						if err != nil {
							t.Fatalf("EntityTagsFor: %v", err)
						}
						if len(applied) != 0 {
							t.Error("the out-of-scope asset was tagged anyway")
						}
					},
				},
			}

			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					s, ctx := newStore(t, e)
					c.run(t, s, ctx)
				})
			}
		})
	}
}

// mustEntityTagsScopeTeam creates a real, active team for the form-field
// scope tests -- BulkAssignOwnership's requireActiveTeam refuses an unknown
// or retired one before it ever reaches Covers, so the target must be real.
func mustEntityTagsScopeTeam(t *testing.T, s *SQLStore, ctx context.Context, code string) string {
	t.Helper()
	team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: code, Name: code}, s.Now())
	if err != nil {
		t.Fatalf("building team %s: %v", code, err)
	}
	if err := s.CreateTeam(ctx, testPermit, team); err != nil {
		t.Fatalf("creating team %s: %v", code, err)
	}
	return team.ID
}
