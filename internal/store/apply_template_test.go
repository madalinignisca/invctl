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

// mustAttachDeviceType is the estate-backfill shape ApplyTemplate exists
// for: an asset that was created BEFORE its device type carried a template,
// and is only given a device_type_id afterwards -- through UpdateAsset,
// which never touches interface rows, unlike insertAsset's
// instantiateComponents. Using this instead of assetOfType's
// create-with-device-type-id path is deliberate: creating an asset WITH a
// templated device type from the start would instantiate the template's own
// eth0 immediately, and the tests below need an eth0 that is the
// OPERATOR'S, recorded before the type ever had a template.
func mustAttachDeviceType(t *testing.T, s *SQLStore, ctx context.Context, assetID, deviceTypeID string) {
	t.Helper()
	row, err := s.GetAsset(ctx, assetID)
	if err != nil {
		t.Fatalf("reading asset %s before attaching device type: %v", assetID, err)
	}
	a := row.Asset
	a.DeviceTypeID = &deviceTypeID
	if err := s.UpdateAsset(ctx, testPermit, &a, nil); err != nil {
		t.Fatalf("attaching device type to asset %s: %v", assetID, err)
	}
}

// TestApplyTemplateSkipsAnAlreadyRecordedInterfaceButAddsMissingOnes is the
// test the brief calls out as the one that matters. eth0 was recorded by an
// operator, as sfp28, before this asset's device type ever carried a
// template. The template, declared afterwards, names eth0 too -- as rj45 --
// and also names eth1, which the asset has never had. Applying the template
// must leave eth0 exactly as the operator left it and add only eth1.
func TestApplyTemplateSkipsAnAlreadyRecordedInterfaceButAddsMissingOnes(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// The asset exists first, with no device type at all -- the
			// pre-feature estate shape.
			assetID := assetOfType(t, s, ctx, "predates-template-01", "", nil)

			operatorFormFactor := domain.FFSFP28
			opEth0, err := domain.NewInterface(NewID(), assetID, "eth0", operatorFormFactor)
			if err != nil {
				t.Fatalf("building eth0: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, opEth0); err != nil {
				t.Fatalf("recording the operator's own eth0: %v", err)
			}

			// The device type's template is declared AFTER eth0 already
			// exists on the asset, and disagrees with it on purpose.
			dtID := mustDeviceTypeForComponents(t, s)
			templateFormFactor := domain.FFRJ45
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &templateFormFactor,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}
			eth1FormFactor := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth1",
				FormFactor: &eth1FormFactor,
			}); err != nil {
				t.Fatalf("declaring the template's eth1: %v", err)
			}

			mustAttachDeviceType(t, s, ctx, assetID, dtID)

			added, err := s.ApplyTemplate(ctx, testPermit, assetID)
			if err != nil {
				t.Fatalf("ApplyTemplate: %v", err)
			}
			if added != 1 {
				t.Fatalf("added = %d, want 1 (only eth1)", added)
			}

			ifaces, err := s.ListInterfaces(ctx, assetID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(ifaces) != 2 {
				t.Fatalf("got %d interfaces, want exactly 2: %+v", len(ifaces), ifaces)
			}
			byName := map[string]InterfaceRow{}
			for _, i := range ifaces {
				byName[i.Name] = i
			}

			eth0, ok := byName["eth0"]
			if !ok {
				t.Fatalf("eth0 disappeared: %+v", ifaces)
			}
			// UNTOUCHED: same id, same form factor, same row_version.
			if eth0.ID != opEth0.ID {
				t.Errorf("eth0 id = %s, want %s -- ApplyTemplate must not have replaced the row", eth0.ID, opEth0.ID)
			}
			if eth0.FormFactor != operatorFormFactor {
				t.Errorf("eth0 form_factor = %q, want %q (the operator's own, not the template's %q)",
					eth0.FormFactor, operatorFormFactor, templateFormFactor)
			}
			if eth0.RowVersion != opEth0.RowVersion {
				t.Errorf("eth0 row_version = %d, want %d (unchanged)", eth0.RowVersion, opEth0.RowVersion)
			}

			eth1, ok := byName["eth1"]
			if !ok {
				t.Fatalf("eth1 was not added: %+v", ifaces)
			}
			if eth1.FormFactor != eth1FormFactor {
				t.Errorf("eth1 form_factor = %q, want %q", eth1.FormFactor, eth1FormFactor)
			}

			// eth0 gets no second change_log row: applying the template must
			// not look like a write happened to a port nobody touched.
			eth0Changes, err := s.ListChangesForEntity(ctx, "interface", eth0.ID, 10)
			if err != nil {
				t.Fatalf("listing eth0's changes: %v", err)
			}
			if len(eth0Changes) != 1 {
				t.Fatalf("eth0 has %d change_log rows, want exactly 1 (its original create)", len(eth0Changes))
			}

			// eth1 gets exactly one create entry, same as instantiation at
			// asset-creation time.
			eth1Changes, err := s.ListChangesForEntity(ctx, "interface", eth1.ID, 10)
			if err != nil {
				t.Fatalf("listing eth1's changes: %v", err)
			}
			if len(eth1Changes) != 1 || eth1Changes[0].Action != domain.ActionCreate {
				t.Fatalf("eth1's change_log = %+v, want exactly one create entry", eth1Changes)
			}
		})
	}
}

// TestApplyTemplateTwiceAddsNothingTheSecondTime: idempotence. Once the
// asset has every named interface, a second call finds nothing missing.
func TestApplyTemplateTwiceAddsNothingTheSecondTime(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "Ethernet1/[1-4]",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template: %v", err)
			}

			assetID := assetOfType(t, s, ctx, "apply-twice-01", "", nil)
			mustAttachDeviceType(t, s, ctx, assetID, dtID)

			first, err := s.ApplyTemplate(ctx, testPermit, assetID)
			if err != nil {
				t.Fatalf("first ApplyTemplate: %v", err)
			}
			if first != 4 {
				t.Fatalf("first ApplyTemplate added %d, want 4", first)
			}

			second, err := s.ApplyTemplate(ctx, testPermit, assetID)
			if err != nil {
				t.Fatalf("second ApplyTemplate: %v", err)
			}
			if second != 0 {
				t.Fatalf("second ApplyTemplate added %d, want 0", second)
			}

			ifaces, err := s.ListInterfaces(ctx, assetID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(ifaces) != 4 {
				t.Fatalf("got %d interfaces after applying twice, want exactly 4: %+v", len(ifaces), ifaces)
			}
		})
	}
}

// TestApplyTemplateWithNoDeviceTypeIsANoOp: an asset with no device_type_id
// at all has nothing to apply.
func TestApplyTemplateWithNoDeviceTypeIsANoOp(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := assetOfType(t, s, ctx, "no-device-type-01", "", nil)

			added, err := s.ApplyTemplate(ctx, testPermit, assetID)
			if err != nil {
				t.Fatalf("ApplyTemplate: %v", err)
			}
			if added != 0 {
				t.Fatalf("added = %d, want 0", added)
			}
		})
	}
}

// TestApplyTemplateDoesNotReactivateARetiredInterfaceOfTheSameName is the
// decision this task documents: a retired port is a person's decision, made
// through RetireInterface, and ApplyTemplate never undoes it. eth0 is
// recorded, then withdrawn; the template still names eth0. Applying it
// leaves eth0 retired -- not reactivated, and not duplicated.
func TestApplyTemplateDoesNotReactivateARetiredInterfaceOfTheSameName(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetID := assetOfType(t, s, ctx, "retired-name-01", "", nil)
			eth0, err := domain.NewInterface(NewID(), assetID, "eth0", domain.FFSFP28)
			if err != nil {
				t.Fatalf("building eth0: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, eth0); err != nil {
				t.Fatalf("recording eth0: %v", err)
			}
			if err := s.RetireInterface(ctx, testPermit, eth0.ID); err != nil {
				t.Fatalf("retiring eth0: %v", err)
			}

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFRJ45
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}
			mustAttachDeviceType(t, s, ctx, assetID, dtID)

			added, err := s.ApplyTemplate(ctx, testPermit, assetID)
			if err != nil {
				t.Fatalf("ApplyTemplate: %v", err)
			}
			if added != 0 {
				t.Fatalf("added = %d, want 0 -- a retired name is already accounted for", added)
			}

			// eth0 is still retired, not reactivated, and there is still
			// exactly one interface row named eth0 -- no second row created
			// beside it either.
			live, err := s.ListInterfaces(ctx, assetID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(live) != 0 {
				t.Fatalf("ListInterfaces (active only) returned %d rows, want 0: %+v", len(live), live)
			}

			var names []string
			if err := s.read(ctx, &names, `SELECT name FROM interface WHERE asset_id = ?`, assetID); err != nil {
				t.Fatalf("reading raw interface rows: %v", err)
			}
			if len(names) != 1 || names[0] != "eth0" {
				t.Fatalf("interface rows on the asset = %v, want exactly one, eth0", names)
			}
		})
	}
}

// TestApplyTemplateRefusesAForeignAssetBeforeItReadsAnything is the test the
// auth review found missing: without it, deleting the authorization check
// entirely left the whole suite green, because every other test here uses
// testPermit -- an AdministratorPermit whose Covers is unconditional.
//
// THE FIRST CASE IS THE ONE THAT MATTERS, and it is the one a careless version
// of this test would omit. When something IS missing, the write path refuses
// and any ordering passes. When NOTHING is missing, a check placed after the
// reads returns (0, nil) -- a silent success for a foreign asset -- and the
// caller learns that asset is fully templated. Repeat per asset and the shape
// of somebody else's estate falls out of the difference between "forbidden"
// and "fine".
//
// That distinguishable third answer is what
// TestRetireCostAuthorizationRunsBeforeTheAlreadyRetiredCheck forbids for
// costs; this is the same rule for templates.
func TestApplyTemplateRefusesAForeignAssetBeforeItReadsAnything(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			ff := "rj45"

			// a2 is outside the permit. Give it a templated model whose
			// template it ALREADY satisfies, so nothing is missing and the
			// write path is never reached.
			dtID := mustDeviceTypeForComponents(t, f.s)
			if err := f.s.CreateDeviceTypeComponents(f.ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("creating the template: %v", err)
			}
			if _, err := f.s.DB().Writer.Exec(f.s.DB().Writer.Rebind(
				`UPDATE asset SET device_type_id = ? WHERE id = ?`), dtID, f.a2); err != nil {
				t.Fatalf("giving a2 a device type: %v", err)
			}
			if _, err := f.s.ApplyTemplate(f.ctx, testPermit, f.a2); err != nil {
				t.Fatalf("seeding a2 to completeness: %v", err)
			}

			// Nothing is missing. An out-of-scope caller must still be refused.
			added, err := f.s.ApplyTemplate(f.ctx, f.permit, f.a2)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("a foreign asset with nothing missing answered (%d, %v), want ErrForbidden. "+
					"A check that runs after the reads reports success here, which tells an "+
					"out-of-scope caller that this asset is fully templated.", added, err)
			}

			// And the easy case: something missing, still refused, and nothing
			// written on the way to refusing.
			if err := f.s.CreateDeviceTypeComponents(f.ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth1", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("extending the template: %v", err)
			}
			if _, err := f.s.ApplyTemplate(f.ctx, f.permit, f.a2); !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("a foreign asset with a missing port = %v, want ErrForbidden", err)
			}
			var n int
			if err := f.s.DB().Reader.Get(&n, f.s.DB().Reader.Rebind(
				`SELECT COUNT(*) FROM interface WHERE asset_id = ? AND name = ?`),
				f.a2, "eth1"); err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 0 {
				t.Error("the refused call still wrote an interface")
			}
		})
	}
}

// TestAProjectOwnerCanApplyATemplateToAnAssetTheyOwn is the positive
// counterpart TestApplyTemplateRefusesAForeignAssetBeforeItReadsAnything was
// missing (final whole-branch review, blocking #3): every OTHER test in this
// file calls ApplyTemplate with testPermit, an AdministratorPermit whose
// Covers is unconditional, so applyTemplateSubject's
// ScopedEntities{"interface": newIDs} -- the permit the write transaction
// actually runs under -- is never consulted by anything that passes. Gutting
// it to an empty map would not fail a single existing test while refusing
// every real project owner's own "Add N missing ports" click with a 403.
//
// f.permit is scoped to f.a1 only (newCostScopeFixture), the same fixture
// CreateAssetInProject's positive guard
// (TestAProjectOwnerCanCreateATemplatedAssetInTheirOwnProject) uses.
//
// Mutation: change applyTemplateSubject to mint
// domain.ScopedEntities{"interface": {}} (drop newInterfaceIDs) and this goes
// red with "forbidden" while every other test in this file, still on
// testPermit, stays green.
func TestAProjectOwnerCanApplyATemplateToAnAssetTheyOwn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)

			dtID := mustDeviceTypeForComponents(t, f.s)
			ff := "rj45"
			if err := f.s.CreateDeviceTypeComponents(f.ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}
			if _, err := f.s.DB().Writer.Exec(f.s.DB().Writer.Rebind(
				`UPDATE asset SET device_type_id = ? WHERE id = ?`), dtID, f.a1); err != nil {
				t.Fatalf("giving a1 a device type: %v", err)
			}

			added, err := f.s.ApplyTemplate(f.ctx, f.permit, f.a1)
			if err != nil {
				t.Fatalf("ApplyTemplate for an asset within the permit's own scope: %v", err)
			}
			if added != 1 {
				t.Fatalf("added = %d, want 1 (eth0)", added)
			}

			ifaces, err := f.s.ListInterfaces(f.ctx, f.a1)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(ifaces) != 1 || ifaces[0].Name != "eth0" {
				t.Fatalf("interfaces on a1 = %+v, want exactly one, eth0", ifaces)
			}
		})
	}
}

// TestTheOfferedCountMatchesWhatApplyTemplateAdds pins the page's number and
// the action's behaviour to one rule.
//
// They were two rules and they disagreed. The asset page counted template
// entries missing from the asset's ACTIVE interfaces; ApplyTemplate treats a
// RETIRED port of the same name as already there and declines to re-add it. So
// a withdrawn eth0 made the page offer "1 missing port" for a port that would
// never appear -- a control promising more than it delivers, which is how an
// operator learns to stop believing the number.
func TestTheOfferedCountMatchesWhatApplyTemplateAdds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-count", nil)
			dtID := mustDeviceTypeForComponents(t, s)
			ff := "rj45"
			for _, n := range []string{"eth0", "eth1"} {
				if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
					Kind: domain.ComponentKindInterface, NameSpec: n, FormFactor: &ff,
				}); err != nil {
					t.Fatalf("templating %s: %v", n, err)
				}
			}
			if _, err := s.DB().Writer.Exec(s.DB().Writer.Rebind(
				`UPDATE asset SET device_type_id = ? WHERE id = ?`), dtID, assetID); err != nil {
				t.Fatalf("attaching the type: %v", err)
			}

			// eth0 exists and is then WITHDRAWN. ApplyTemplate will not bring
			// it back, so the offered count must not include it.
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			if err := s.RetireInterface(ctx, testPermit, ifaceID); err != nil {
				t.Fatalf("withdrawing eth0: %v", err)
			}

			components, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing the template: %v", err)
			}
			offered, err := s.MissingTemplateCount(ctx, assetID, components)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			added, err := s.ApplyTemplate(ctx, testPermit, assetID)
			if err != nil {
				t.Fatalf("applying: %v", err)
			}
			if offered != added {
				t.Errorf("the page offered %d ports and applying added %d. A withdrawn "+
					"port whose name a template entry shares is 'already there' to "+
					"ApplyTemplate, so a count that diffs against ACTIVE ports alone "+
					"promises something the button will not deliver.", offered, added)
			}
			if added != 1 {
				t.Errorf("added = %d, want 1 (eth1 only; eth0 is withdrawn, not missing)", added)
			}
		})
	}
}
