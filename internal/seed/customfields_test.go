// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
)

// TestTheSeedFixtureCoversEveryCustomFieldKind guards WP-A4's own rule for
// itself: "a field null everywhere is a field no test exercises, which is
// how WP-A2 published a column nobody had checked." StageCustomFields is
// staged separately from Load (see seed_customfields.go for why -- it needs
// a real app_user to attribute custom_field.created_by to), so this test
// builds that one extra step itself rather than relying on newFixture.
func TestTheSeedFixtureCoversEveryCustomFieldKind(t *testing.T) {
	f := newFixture(t)

	admin, err := domain.NewAppUser(store.NewID(), "admin", domain.UserSourceLocal, f.store.Now())
	if err != nil {
		t.Fatalf("building admin: %v", err)
	}
	if err := f.store.CreateUser(f.ctx, domain.AdministratorPermit(domain.SystemActor), admin); err != nil {
		t.Fatalf("creating admin: %v", err)
	}

	if err := seed.StageCustomFields(f.ctx, f.store, "admin"); err != nil {
		t.Fatalf("staging custom fields: %v", err)
	}

	assetFields, err := f.store.ListCustomFields(f.ctx, domain.CustomFieldEntityAsset, true)
	if err != nil {
		t.Fatalf("listing asset fields: %v", err)
	}
	serviceFields, err := f.store.ListCustomFields(f.ctx, domain.CustomFieldEntityService, true)
	if err != nil {
		t.Fatalf("listing service fields: %v", err)
	}

	wantKinds := map[string]bool{
		domain.CustomFieldText: false, domain.CustomFieldNumber: false, domain.CustomFieldDate: false,
		domain.CustomFieldBoolean: false, domain.CustomFieldSelect: false,
	}
	for entityType, fields := range map[string][]store.CustomFieldRow{
		domain.CustomFieldEntityAsset:   assetFields,
		domain.CustomFieldEntityService: serviceFields,
	} {
		seen := make(map[string]bool, len(wantKinds))
		for k := range wantKinds {
			seen[k] = false
		}
		for _, field := range fields {
			seen[field.Kind] = true
			if field.Kind == domain.CustomFieldSelect {
				options, err := f.store.GetCustomField(f.ctx, field.ID)
				if err != nil {
					t.Fatalf("reading select field %s: %v", field.Code, err)
				}
				if len(options.Options) == 0 {
					t.Errorf("%s field %s is a select with no options", entityType, field.Code)
				}
			}
		}
		for kind, ok := range seen {
			if !ok {
				t.Errorf("no %s field defined for entity type %s", kind, entityType)
			}
		}
	}

	// hv-02 and vault hold a value for every field this phase defines;
	// sw-oob-1 and backup-agent hold none, deliberately.
	hvValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityAsset, f.refs.Assets["hv-02"])
	if err != nil {
		t.Fatalf("reading hv-02's custom values: %v", err)
	}
	if len(hvValues) != len(assetFields) {
		t.Errorf("hv-02 holds %d custom values, want one per asset field (%d)", len(hvValues), len(assetFields))
	}

	oobValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityAsset, f.refs.Assets["sw-oob-1"])
	if err != nil {
		t.Fatalf("reading sw-oob-1's custom values: %v", err)
	}
	if len(oobValues) != 0 {
		t.Errorf("sw-oob-1 must hold no custom values -- it is the fixture's deliberately empty case; got %d", len(oobValues))
	}

	vaultValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityService, f.refs.Services["vault"])
	if err != nil {
		t.Fatalf("reading vault's custom values: %v", err)
	}
	if len(vaultValues) != len(serviceFields) {
		t.Errorf("vault holds %d custom values, want one per service field (%d)", len(vaultValues), len(serviceFields))
	}

	backupValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityService, f.refs.Services["backup-agent"])
	if err != nil {
		t.Fatalf("reading backup-agent's custom values: %v", err)
	}
	if len(backupValues) != 0 {
		t.Errorf("backup-agent must hold no custom values -- it is the fixture's deliberately empty case; got %d", len(backupValues))
	}

	// Idempotent: a repeated dev-mode start must not fail against the unique
	// (entity_type, code) index.
	if err := seed.StageCustomFields(f.ctx, f.store, "admin"); err != nil {
		t.Fatalf("staging custom fields a second time: %v", err)
	}
	againFields, err := f.store.ListCustomFields(f.ctx, domain.CustomFieldEntityAsset, true)
	if err != nil {
		t.Fatalf("listing asset fields after the second stage: %v", err)
	}
	if len(againFields) != len(assetFields) {
		t.Errorf("staging custom fields twice produced %d asset fields, want %d (unchanged)",
			len(againFields), len(assetFields))
	}
}

// TestTheDemoEstateHasAnOwnershipMix guards the property the NEXT work
// package (an estate-wide ownership report) depends on: a demo estate where
// every custom field is owned gives that report nothing to find, and a
// correct empty result is then indistinguishable from a broken one. Most
// fields keep a live owner; power_budget_watts is left deliberately
// unowned (the state migration 00054 left the eleven pre-existing fields
// in, which no application write path can otherwise reproduce);
// criticality_tier keeps a NAMED owner whose team has since retired.
func TestTheDemoEstateHasAnOwnershipMix(t *testing.T) {
	f := newFixture(t)

	admin, err := domain.NewAppUser(store.NewID(), "admin", domain.UserSourceLocal, f.store.Now())
	if err != nil {
		t.Fatalf("building admin: %v", err)
	}
	if err := f.store.CreateUser(f.ctx, domain.AdministratorPermit(domain.SystemActor), admin); err != nil {
		t.Fatalf("creating admin: %v", err)
	}
	if err := seed.StageCustomFields(f.ctx, f.store, "admin"); err != nil {
		t.Fatalf("staging custom fields: %v", err)
	}

	fields, err := f.store.ListCustomFields(f.ctx, domain.CustomFieldEntityAsset, true)
	if err != nil {
		t.Fatalf("listing asset fields: %v", err)
	}

	var unowned, sinceRetired, owned int
	for _, field := range fields {
		switch {
		case field.OwnerTeamID == nil:
			unowned++
		case field.OwnerRetired():
			sinceRetired++
		default:
			owned++
		}
	}
	if unowned != 1 {
		t.Errorf("got %d unowned asset fields, want exactly 1 (power_budget_watts)", unowned)
	}
	if sinceRetired != 1 {
		t.Errorf("got %d asset fields owned by a since-retired team, want exactly 1 (criticality_tier)", sinceRetired)
	}
	if owned == 0 {
		t.Error("the demo must still have fields with a live owner -- most of them, not none")
	}
}
