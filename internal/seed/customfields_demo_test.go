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

// TestTheDemoSpreadWidensCoverageWithoutMovingTheFixtureAssertion guards the
// isolation this task depends on: StageCustomFieldSpread must add to the
// estate the way StageDemoOverride adds a health override, never change what
// StageCustomFields itself put there. If this drifted, the fixture assertion
// in TestTheSeedFixtureCoversEveryCustomFieldKind would start failing on an
// unrelated change -- exactly the "isolate better, don't update the test"
// rule this task was built under.
func TestTheDemoSpreadWidensCoverageWithoutMovingTheFixtureAssertion(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	f := newFixture(t)

	admin, err := domain.NewAppUser(store.NewID(), "admin", domain.UserSourceLocal, f.store.Now())
	if err != nil {
		t.Fatalf("building admin: %v", err)
	}
	if err := f.store.CreateUser(f.ctx, domain.SystemActor, admin); err != nil {
		t.Fatalf("creating admin: %v", err)
	}

	if err := seed.StageCustomFields(f.ctx, f.store, "admin"); err != nil {
		t.Fatalf("staging custom fields: %v", err)
	}
	if err := seed.StageCustomFieldSpread(f.ctx, f.store, "admin"); err != nil {
		t.Fatalf("staging the demo custom field spread: %v", err)
	}

	// hv-02 and vault still hold every field StageCustomFields defined --
	// the spread must never touch them.
	assetFields, err := f.store.ListCustomFields(f.ctx, domain.CustomFieldEntityAsset, true)
	if err != nil {
		t.Fatalf("listing asset fields: %v", err)
	}
	hvValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityAsset, f.refs.Assets["hv-02"])
	if err != nil {
		t.Fatalf("reading hv-02's custom values: %v", err)
	}
	// StageCustomFields defined 5 asset fields; the spread added a sixth
	// (legacy_ticket_ref) that hv-02 never receives a value for.
	wantHVFields := len(assetFields) - 1
	if len(hvValues) != wantHVFields {
		t.Errorf("hv-02 holds %d custom values, want %d (untouched by the spread)", len(hvValues), wantHVFields)
	}

	// sw-oob-1 and backup-agent are still the deliberately empty case.
	oobValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityAsset, f.refs.Assets["sw-oob-1"])
	if err != nil {
		t.Fatalf("reading sw-oob-1's custom values: %v", err)
	}
	if len(oobValues) != 0 {
		t.Errorf("sw-oob-1 must hold no custom values; got %d", len(oobValues))
	}
	backupValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityService, f.refs.Services["backup-agent"])
	if err != nil {
		t.Fatalf("reading backup-agent's custom values: %v", err)
	}
	if len(backupValues) != 0 {
		t.Errorf("backup-agent must hold no custom values; got %d", len(backupValues))
	}

	// The spread actually widened coverage: hv-01, a company-tier asset the
	// base fixture test never touches, now holds values too.
	hv01Values, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityAsset, f.refs.Assets["hv-01"])
	if err != nil {
		t.Fatalf("reading hv-01's custom values: %v", err)
	}
	if len(hv01Values) == 0 {
		t.Error("hv-01 holds no custom values; the spread should have populated it")
	}

	// The retired field: legacy_ticket_ref is retired, and hv-01 still holds
	// the value it was given before retirement.
	var legacyRetired bool
	var legacyCode = "legacy_ticket_ref"
	for _, f := range assetFields {
		if f.Code == legacyCode {
			legacyRetired = f.IsRetired()
		}
	}
	if !legacyRetired {
		t.Error("legacy_ticket_ref should be retired")
	}
	var sawLegacyValue bool
	for _, v := range hv01Values {
		if v.Code == legacyCode {
			sawLegacyValue = true
			if !v.Retired {
				t.Error("hv-01's legacy_ticket_ref value should report its field as retired")
			}
			if v.ValueText == "" {
				t.Error("hv-01's legacy_ticket_ref value should not be empty")
			}
		}
	}
	if !sawLegacyValue {
		t.Error("hv-01 should still hold a value for the retired legacy_ticket_ref field")
	}

	// The retired option: criticality_tier's "low" option no longer offers
	// itself to a NEW value, but fw-dev-1 still holds it.
	critField, err := f.store.GetCustomField(f.ctx, criticalityTierFieldID(assetFields))
	if err != nil {
		t.Fatalf("reading criticality_tier: %v", err)
	}
	var lowRetired bool
	for _, o := range critField.Options {
		if o.Value == "low" && o.RetiredAt != nil {
			lowRetired = true
		}
	}
	if !lowRetired {
		t.Error("criticality_tier's low option should be retired")
	}
	fwDevValues, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityAsset, f.refs.Assets["fw-dev-1"])
	if err != nil {
		t.Fatalf("reading fw-dev-1's custom values: %v", err)
	}
	var fwDevHoldsLow bool
	for _, v := range fwDevValues {
		if v.Code == "criticality_tier" && v.ValueText == "low" {
			fwDevHoldsLow = true
		}
	}
	if !fwDevHoldsLow {
		t.Error("fw-dev-1 should still hold criticality_tier=low after the option was retired")
	}

	// Idempotent: a second call must not fail and must not change the
	// spread's shape.
	if err := seed.StageCustomFieldSpread(f.ctx, f.store, "admin"); err != nil {
		t.Fatalf("staging the demo custom field spread a second time: %v", err)
	}
	hv01Again, err := f.store.CustomValuesFor(f.ctx, domain.CustomFieldEntityAsset, f.refs.Assets["hv-01"])
	if err != nil {
		t.Fatalf("reading hv-01's custom values after the second stage: %v", err)
	}
	if len(hv01Again) != len(hv01Values) {
		t.Errorf("staging the spread twice produced %d values on hv-01, want %d (unchanged)",
			len(hv01Again), len(hv01Values))
	}
}

func criticalityTierFieldID(rows []store.CustomFieldRow) string {
	for _, r := range rows {
		if r.Code == "criticality_tier" {
			return r.ID
		}
	}
	return ""
}
