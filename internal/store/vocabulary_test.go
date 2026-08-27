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
	"sort"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// vocabularyReaders pairs each lookup table with its store method and with the
// Go constants that still name values in it.
//
// The `constants` column is the thing this file exists to police. Migration
// 00004 turned these columns from CHECK constraints into foreign keys, and the
// Go slices stopped being the permitted set -- but they did NOT stop being
// load-bearing. domain.KindK8sNode is bound into the impact graph's cluster
// expansion, domain.EnvRoleTransit into the spanning-assets query,
// domain.IPRolePrimary into the address form's default, and the whole seed is
// written in these names. Deleting the matching row from a lookup table is now
// a plain DELETE that the foreign key permits whenever nothing references it --
// and the damage is silent: cluster_ip endpoints would simply resolve to no
// hosts. This turns that into a failing build.
//
// The check runs one way only, deliberately. Every Go constant must exist as a
// row; a row need not have a Go constant. That asymmetry IS the change: adding
// `vrf` to asset_kind must not require a Go release, and a test demanding the
// two sides match exactly would reimpose exactly the coupling the lookup tables
// removed. ('bridge' used to be the example here and is now a seeded kind, so
// it no longer demonstrates anything -- the placeholder has to be a code the
// migration does NOT ship.)
var vocabularyReaders = []struct {
	table     string
	read      func(*SQLStore, context.Context) ([]VocabularyTerm, error)
	constants []string
	// count is the number of rows seeded for this vocabulary across ALL
	// migrations, not just 00004 -- asset_kind gained `bridge` in 00006 and
	// service_kind gained `lb` and `secrets` in 00008. Asserted so that a
	// silently truncated seed is caught rather than a shorter dropdown.
	count int
}{
	{"asset_kind", (*SQLStore).AssetKinds, domain.AssetKinds, 13},
	{"service_kind", (*SQLStore).ServiceKinds, domain.ServiceKinds, 14},
	{"interface_form_factor", (*SQLStore).InterfaceFormFactors, domain.FormFactors, 9},
	{"environment_role", (*SQLStore).EnvironmentRoles, domain.EnvRoles, 6},
	{"ip_address_role", (*SQLStore).IPAddressRoles, domain.IPRoles, 5},
	{"data_class", (*SQLStore).DataClassVocabulary, domain.DataClasses, 7},
	// container_engine has no Go constant set at all -- it is the only one of
	// the seven that never had one, and nothing in the codebase names an
	// engine except the seeder's literal "docker".
	{"container_engine", (*SQLStore).ContainerEngines, []string{"docker", "podman"}, 2},
}

// TestVocabularyReadersReturnTheSeededValues covers the readers themselves: a
// form fed from one of these is the only way an operator can pick a value that
// was added as data, so a reader that silently returned nothing would make the
// lookup tables useless while every other test stayed green.
func TestVocabularyReadersReturnTheSeededValues(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			for _, v := range vocabularyReaders {
				t.Run(v.table, func(t *testing.T) {
					terms, err := v.read(s, ctx)
					if err != nil {
						t.Fatalf("reading %s: %v", v.table, err)
					}
					if len(terms) != v.count {
						t.Fatalf("%s returned %d terms, want %d", v.table, len(terms), v.count)
					}

					seen := map[string]bool{}
					for _, term := range terms {
						if term.Code == "" {
							t.Errorf("%s has a term with an empty code: %+v", v.table, term)
						}
						if term.Label == "" {
							t.Errorf("%s.%s has no label; the form would render a blank option",
								v.table, term.Code)
						}
						if seen[term.Code] {
							t.Errorf("%s returned %q twice", v.table, term.Code)
						}
						seen[term.Code] = true
					}

					// Display order is the estate's own -- physical nesting for
					// asset kinds, regulatory weight for data classes. It is
					// not the alphabet, and a reader that lost the ORDER BY
					// would reorder every dropdown in the application without
					// breaking anything else.
					if !sort.SliceIsSorted(terms, func(i, j int) bool {
						if terms[i].SortOrder != terms[j].SortOrder {
							return terms[i].SortOrder < terms[j].SortOrder
						}
						return terms[i].Code < terms[j].Code
					}) {
						t.Errorf("%s is not in (sort_order, code) order: %+v", v.table, terms)
					}
				})
			}
		})
	}
}

// TestGoConstantsExistInEveryVocabulary is the guard the lookup tables need in
// place of the CHECK constraint they replaced.
//
// The CHECK and the Go slice used to be two statements of the same list, and
// docs/AUDIT.md rule 13's boundary test asserts they agree "or one of them is
// decorative". For these seven columns the CHECK is gone and the slice no
// longer bounds anything, so the surviving obligation runs the other way: every
// value the code still names by constant must exist as a row.
func TestGoConstantsExistInEveryVocabulary(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			for _, v := range vocabularyReaders {
				terms, err := v.read(s, ctx)
				if err != nil {
					t.Fatalf("reading %s: %v", v.table, err)
				}
				have := map[string]bool{}
				for _, term := range terms {
					have[term.Code] = true
				}
				for _, code := range v.constants {
					if !have[code] {
						t.Errorf("%s has no row %q, but Go still names it. A lookup row may be "+
							"added freely -- that is the point -- but removing one the code refers "+
							"to by constant breaks it silently: dropping k8s_node leaves the impact "+
							"graph's cluster expansion returning no hosts, with no error anywhere.",
							v.table, code)
					}
				}
			}
		})
	}
}

// TestVocabularyValueAddedAsDataIsUsableImmediately is the whole change,
// end to end and on both engines: INSERT a value, and the same process that was
// already running accepts it.
//
// If this test ever needs a restart, a rebuild or a Go edit to pass, the lookup
// tables have been reduced to a more expensive CHECK constraint.
func TestVocabularyValueAddedAsDataIsUsableImmediately(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// Before: the value does not exist, and the store refuses it with a
			// field-level validation error rather than a driver error, so the
			// form can re-render at 422 with the field marked.
			a, err := domain.NewAsset(NewID(), "vrf", "vrf-red", nil, s.Now())
			if err != nil {
				t.Fatalf("the constructor rejected a well-formed unknown kind: %v", err)
			}
			err = s.CreateAsset(ctx, testPermit, a, nil)
			if err == nil {
				t.Fatal("an asset with an unknown kind was stored")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("rejecting an unknown kind gave %v, want a validation error", err)
			}
			if ve, ok := domain.AsValidation(err); !ok {
				t.Errorf("rejecting an unknown kind gave %T, want *domain.ValidationError -- "+
					"an operator needs the field named, not \"that request was not valid\"", err)
			} else if _, named := ve.Messages()["kind"]; !named {
				t.Errorf("the validation error does not name the kind field: %v", ve.Messages())
			}

			// The value arrives as data. No migration, no deploy.
			if _, err := s.db.Writer.Exec(s.db.Rebind(
				`INSERT INTO asset_kind (code, label, sort_order) VALUES (?, ?, ?)`),
				"vrf", "VRF", 55); err != nil {
				t.Fatalf("inserting the new kind: %v", err)
			}

			// After: the same store, in the same process, stores it.
			b, err := domain.NewAsset(NewID(), "vrf", "vrf-red", nil, s.Now())
			if err != nil {
				t.Fatalf("building the asset: %v", err)
			}
			if err := s.CreateAsset(ctx, testPermit, b, nil); err != nil {
				t.Fatalf("storing an asset of a kind added as data: %v", err)
			}

			// And the form offers it, in the position sort_order asked for --
			// between patch_panel (60) at 60 and switch (50). A value that is
			// storable but unofferable is a value nobody can enter.
			kinds, err := s.AssetKinds(ctx)
			if err != nil {
				t.Fatalf("reading asset kinds: %v", err)
			}
			position := -1
			for i, k := range kinds {
				if k.Code == "vrf" {
					position = i
				}
			}
			if position == -1 {
				t.Fatal("the new kind is storable but the form would not offer it")
			}
			if kinds[position-1].Code != domain.KindSwitch {
				t.Errorf("the new kind sorted after %q, want %q -- sort_order is ignored",
					kinds[position-1].Code, domain.KindSwitch)
			}
		})
	}
}

// TestVocabularyRowInUseCannotBeRemoved is the half of the foreign key that the
// CHECK constraint never provided: a CHECK could be dropped from the schema and
// leave every row referencing it intact, whereas the vocabulary now cannot be
// edited out from under the estate.
func TestVocabularyRowInUseCannotBeRemoved(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mustAsset(t, s, ctx, domain.KindVM, "vm-1", nil)

			if _, err := s.db.Writer.Exec(s.db.Rebind(
				`DELETE FROM asset_kind WHERE code = ?`), domain.KindVM); err == nil {
				t.Error("a vocabulary row still referenced by an asset was deleted")
			}
		})
	}
}

// TestUnknownVocabularyValueIsRejectedOnEveryColumn walks each of the six
// columns an operator can reach and asserts the same contract: a well-formed
// value that is not in the lookup table is refused, and refused as a field
// error so the form comes back at 422 with the field marked rather than as a
// bare "that request was not valid".
func TestUnknownVocabularyValueIsRejectedOnEveryColumn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-1", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")

			cases := []struct {
				name  string
				field string
				write func() error
			}{
				{"environment.role", "role", func() error {
					env := &domain.Environment{
						ID: NewID(), Code: "wat", Name: "Wat", Role: "backstage",
						Criticality: 3, CreatedAt: domain.FormatTime(s.Now()),
						UpdatedAt: domain.FormatTime(s.Now()),
					}
					return s.CreateEnvironment(ctx, testPermit, env)
				}},
				{"asset.kind", "kind", func() error {
					a := &domain.Asset{
						ID: NewID(), Kind: "vrf", Name: "vrf-1",
						Lifecycle: domain.LifecycleActive, Attrs: "{}",
						CreatedAt: domain.FormatTime(s.Now()), UpdatedAt: domain.FormatTime(s.Now()),
					}
					return s.CreateAsset(ctx, testPermit, a, nil)
				}},
				{"interface.form_factor", "form_factor", func() error {
					i := &domain.Interface{
						ID: NewID(), AssetID: assetID, Name: "eth9",
						FormFactor: "osfp", Enabled: true,
					}
					return s.CreateInterface(ctx, testActor, i)
				}},
				{"ip_address.role", "role", func() error {
					av, err := domain.ParseAddr("10.20.30.90")
					if err != nil {
						return err
					}
					addr := &domain.IPAddress{
						ID: NewID(), AddrText: av.Text, AddrFamily: av.Family,
						AddrStart: av.Start, InterfaceID: &ifaceID, Role: "anycast",
					}
					return s.CreateIPAddress(ctx, testActor, addr)
				}},
				{"service.kind", "kind", func() error {
					svc := &domain.Service{
						ID: NewID(), Code: "wat", Name: "Wat", Kind: "ledger",
						EnvironmentID: envID, Availability: domain.AvailStandalone,
						Tier: 3, Lifecycle: domain.LifecycleActive, Attrs: "{}",
						CreatedAt: domain.FormatTime(s.Now()), UpdatedAt: domain.FormatTime(s.Now()),
					}
					// Validate would catch the kind's shape but not its
					// existence, so reach the store directly the way an
					// importer would.
					return s.CreateService(ctx, testActor, svc)
				}},
				{"dependency_data_class.data_class", "data_class", func() error {
					return setDataClassesForTest(ctx, s, "does-not-matter", []string{"biometrics"})
				}},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					err := tc.write()
					if err == nil {
						t.Fatalf("%s accepted a value that is not in its lookup table", tc.name)
					}
					ve, ok := domain.AsValidation(err)
					if !ok {
						t.Fatalf("%s failed with %T (%v), want *domain.ValidationError", tc.name, err, err)
					}
					if _, named := ve.Messages()[tc.field]; !named {
						t.Errorf("%s: the error does not name %q: %v", tc.name, tc.field, ve.Messages())
					}
				})
			}
		})
	}
}

// setDataClassesForTest reaches setDataClasses without a real dependency row.
// The class is checked before the insert is attempted, so the write never gets
// far enough to need one -- which is the behaviour being asserted.
func setDataClassesForTest(ctx context.Context, s *SQLStore, depID string, classes []string) error {
	return s.write(ctx, domain.AdministratorPermit(testActor), func(t *tx) error {
		return setDataClasses(ctx, t, depID, classes)
	})
}

// TestAssetKindBehaviourMatchesTheSeededSets.
//
// The assertions here used to live in internal/domain, switching over a
// hardcoded list. They moved with the behaviour: what a kind may do is now a
// column on its lookup row, so this asserts the migration seeded those columns
// to exactly the sets the code previously hardcoded. If the two ever disagree,
// an install silently changes what a stock estate can express.
func TestAssetKindBehaviourMatchesTheSeededSets(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			canHost := map[string]bool{
				domain.KindServer: true, domain.KindHypervisor: true, domain.KindVM: true,
				domain.KindK8sNode: true, domain.KindCluster: true, domain.KindFirewall: true,
				domain.KindSwitch: true, domain.KindStorage: true,
				domain.KindSite: false, domain.KindRack: false,
				domain.KindPDU: false, domain.KindPatchPanel: false,
			}
			for kind, want := range canHost {
				b, err := s.AssetKindBehaviour(ctx, kind)
				if err != nil {
					t.Fatalf("reading behaviour of %s: %v", kind, err)
				}
				if b.CanHostInstances != want {
					t.Errorf("can_host_instances(%s) = %v, want %v", kind, b.CanHostInstances, want)
				}
			}

			// is_attachable is seeded from domain.AttachableAssetKinds, which
			// survives precisely so the two cannot drift at install time.
			attachable := make(map[string]bool, len(domain.AttachableAssetKinds))
			for _, k := range domain.AttachableAssetKinds {
				attachable[k] = true
			}
			for kind := range canHost {
				b, err := s.AssetKindBehaviour(ctx, kind)
				if err != nil {
					t.Fatalf("reading behaviour of %s: %v", kind, err)
				}
				if b.IsAttachable != attachable[kind] {
					t.Errorf("is_attachable(%s) = %v, want %v (domain.AttachableAssetKinds)",
						kind, b.IsAttachable, attachable[kind])
				}
			}

			// A forwarder is a vertex in the network graph, not a host that
			// attaches to one. Called out because it is the least obvious.
			if b, _ := s.AssetKindBehaviour(ctx, domain.KindSwitch); b.IsAttachable {
				t.Error("a switch is attachable; a forwarder is a vertex, not a host")
			}
		})
	}
}

// TestOnlyTransitRoleBrokers pins the one behaviour an environment role carries.
func TestOnlyTransitRoleBrokers(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			for _, role := range []string{
				domain.EnvRoleProduction, domain.EnvRoleStaging, domain.EnvRoleDev,
				domain.EnvRoleTransit, domain.EnvRoleShared, domain.EnvRoleDR,
			} {
				got, err := s.RoleIsTransit(ctx, role)
				if err != nil {
					t.Fatalf("reading transit flag of %s: %v", role, err)
				}
				if want := role == domain.EnvRoleTransit; got != want {
					t.Errorf("is_transit(%s) = %v, want %v", role, got, want)
				}
			}
		})
	}
}

// TestAVocabularyAddedAsDataIsUsable is the whole point of the lookup tables,
// and the case the first attempt failed.
//
// The motivating example WAS 'bridge' -- a thing a veth attaches to, which
// forwards onto a physical NIC -- and this test is why adding it later cost a
// four-line migration and no code. Now that migration 00006 ships it, the
// placeholder has to be a kind the migration does not: 'vrf' is the next such
// construct, and the assertion is unchanged. A kind added as a row must produce
// one that can take a network attachment and carry a workload, not merely one
// that can be stored. Measured before the behaviour columns existed: the INSERT
// worked, the asset was created, and CanHostInstances and IsAttachable both
// answered false with no diagnostic, so the kind was inert.
func TestAVocabularyAddedAsDataIsUsable(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			w := s.DB().Writer

			// Step 1: an INSERT, not a migration and not a release.
			if _, err := w.Exec(w.Rebind(
				`INSERT INTO asset_kind (code, label, sort_order, can_host_instances, is_attachable)
				 VALUES (?, ?, ?, TRUE, TRUE)`), "vrf", "VRF", 135); err != nil {
				t.Fatalf("adding a kind as data: %v", err)
			}

			// Step 2: it is offered by the form, in sort order.
			kinds, err := s.AssetKinds(ctx)
			if err != nil {
				t.Fatalf("listing kinds: %v", err)
			}
			if !containsID(Codes(kinds), "vrf") {
				t.Error("the new kind is not offered to an operator; a value nothing can select " +
					"is not extensibility")
			}

			// Step 3: an asset of that kind can be created.
			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			br, err := domain.NewAsset(NewID(), "vrf", "vrf-red", nil, s.Now())
			if err != nil {
				t.Fatalf("the domain constructor refused a kind the database accepts: %v", err)
			}
			if err := s.CreateAsset(ctx, testPermit, br, []string{envID}); err != nil {
				t.Fatalf("creating an asset of the new kind: %v", err)
			}

			// Step 4: THE PART THAT USED TO FAIL. It can carry a workload and
			// take a network attachment.
			b, err := s.AssetKindBehaviour(ctx, "vrf")
			if err != nil {
				t.Fatalf("reading behaviour: %v", err)
			}
			if !b.CanHostInstances {
				t.Error("a kind added as data cannot host a workload; the kind is storable " +
					"but inert, which is the defect the behaviour columns exist to fix")
			}
			if !b.IsAttachable {
				t.Error("a kind added as data cannot take a network attachment, so it cannot " +
					"appear in the network model it was added for")
			}

			// Step 5: and it arrives on the loaded row, which is what handlers
			// filter on when they build a form.
			row, err := s.GetAsset(ctx, br.ID)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			if !row.CanHostInstances() || !row.IsAttachable() {
				t.Errorf("the loaded row says canHost=%v attachable=%v; a form filtering on these "+
					"would hide the new kind from every operator",
					row.CanHostInstances(), row.IsAttachable())
			}
		})
	}
}

// TestEveryVocabularyTermHasADescription.
//
// The description column exists so a person choosing from a dropdown can find
// out what the options mean; a term without one is a term the UI cannot
// explain. This is a completeness check rather than a spot check on purpose:
// the failure mode is a LATER migration adding a term and not describing it,
// which is exactly what happened to asset_kind `bridge` -- added by 00006,
// invisible to a migration written against 00004's list, and caught only
// because this check counts rows rather than naming them.
func TestEveryVocabularyTermHasADescription(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			for _, v := range []struct {
				name string
				read func(context.Context) ([]VocabularyTerm, error)
			}{
				{"asset_kind", s.AssetKinds},
				{"service_kind", s.ServiceKinds},
				{"interface_form_factor", s.InterfaceFormFactors},
				{"environment_role", s.EnvironmentRoles},
				{"ip_address_role", s.IPAddressRoles},
				{"data_class", s.DataClassVocabulary},
				{"container_engine", s.ContainerEngines},
			} {
				terms, err := v.read(ctx)
				if err != nil {
					t.Fatalf("reading %s: %v", v.name, err)
				}
				if len(terms) == 0 {
					t.Errorf("%s is empty, so this proves nothing", v.name)
				}
				for _, term := range terms {
					if term.Description == "" {
						t.Errorf("%s term %q has no description; the dropdown that offers it "+
							"cannot say what it means", v.name, term.Code)
					}
				}
			}
		})
	}
}
