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

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestTemplateDriftFindingsReportsAnAssetMissingAComponent is the case the
// task exists for: the device type's template names eth0, the asset was
// created before the template did and never got it, and ApplyTemplate was
// never run. That gap must surface here, since nothing ever rewrites the
// asset to match.
func TestTemplateDriftFindingsReportsAnAssetMissingAComponent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}

			// The asset predates the template, same fixture shape as
			// apply_template_test.go: created first, attached second, and
			// ApplyTemplate is deliberately never called.
			assetID := assetOfType(t, s, ctx, "drift-missing-01", "", nil)
			mustAttachDeviceType(t, s, ctx, assetID, dtID)

			findings, err := s.TemplateDriftFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateDriftFindings: %v", err)
			}
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
			}
			f := findings[0]
			if f.Severity != FindingGap {
				t.Errorf("severity = %q, want %q", f.Severity, FindingGap)
			}
			if f.Count != 1 {
				t.Errorf("count = %d, want 1", f.Count)
			}
			if !strings.Contains(f.Detail, "drift-missing-01") || !strings.Contains(f.Detail, "eth0") {
				t.Errorf("detail = %q, want it to name the asset and the missing component", f.Detail)
			}
		})
	}
}

// TestTemplateDriftFindingsIsEmptyWhenTheAssetMatchesItsTemplate: applying
// the template (or building the asset fresh from a type that already
// carried one) leaves nothing to report.
func TestTemplateDriftFindingsIsEmptyWhenTheAssetMatchesItsTemplate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}

			// Created AFTER the template exists, so instantiateComponents
			// seeds eth0 for real at creation time -- no gap to report.
			assetOfType(t, s, ctx, "drift-matched-01", dtID, nil)

			findings, err := s.TemplateDriftFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateDriftFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
			}
		})
	}
}

// TestTemplateDriftFindingsIgnoresAnAssetWithNoDeviceType: nothing to
// compare against, so nothing to report.
func TestTemplateDriftFindingsIgnoresAnAssetWithNoDeviceType(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetOfType(t, s, ctx, "drift-no-type-01", "", nil)

			findings, err := s.TemplateDriftFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateDriftFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
			}
		})
	}
}

// TestTemplateDriftFindingsAggregatesAcrossAssetsIntoOneRow: several
// drifting assets fold into a single Finding with Count set, not one row
// each -- the rule the whole file is built on.
func TestTemplateDriftFindingsAggregatesAcrossAssetsIntoOneRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}

			for i := 0; i < 3; i++ {
				assetID := assetOfType(t, s, ctx,
					"drift-many-0"+string(rune('1'+i)), "", nil)
				mustAttachDeviceType(t, s, ctx, assetID, dtID)
			}

			findings, err := s.TemplateDriftFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateDriftFindings: %v", err)
			}
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want 1 row for the whole kind: %+v", len(findings), findings)
			}
			if findings[0].Count != 3 {
				t.Errorf("count = %d, want 3", findings[0].Count)
			}
		})
	}
}

// TestTemplateDriftFindingsIgnoresExtraInterfaces: an asset carrying an
// interface its template never named is not drift -- an operator adding a
// real port beyond the model sheet is ordinary, and reporting it would be
// noise. Only a MISSING named component is drift. TemplateExtraFindings, the
// mirror-image finding below, is what surfaces this same fixture instead --
// they are separate findings and this one must keep ignoring it.
func TestTemplateDriftFindingsIgnoresExtraInterfaces(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}

			// Created from the templated type, so eth0 is instantiated, then
			// an operator adds an extra port the template never declared.
			assetID := assetOfType(t, s, ctx, "drift-extra-01", dtID, nil)
			extra, err := domain.NewInterface(NewID(), assetID, "eth99", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building eth99: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, extra); err != nil {
				t.Fatalf("recording eth99: %v", err)
			}

			findings, err := s.TemplateDriftFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateDriftFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("got %d findings, want 0 -- an extra interface is not drift: %+v", len(findings), findings)
			}
		})
	}
}

// TestEstateFindingsReachesTemplateDriftFindings is the registration proof
// the final whole-branch review asked for (blocking #2): deleting the
// "drift, err := s.TemplateDriftFindings..." block and its append from
// EstateFindings in findings.go left the whole suite green before this test
// existed, because every drift test in this file calls
// s.TemplateDriftFindings(ctx) directly and findings_test.go never mentioned
// drift at all. The seed estate declares no templates, so
// TestTheOverviewFindsAllOfIt could not have caught it either.
//
// Mutation: delete that block and this goes red with "asset missing a
// component its device type declares" never appearing in what EstateFindings
// returns; restore to go green again.
func TestEstateFindingsReachesTemplateDriftFindings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}
			assetID := assetOfType(t, s, ctx, "estate-drift-01", "", nil)
			mustAttachDeviceType(t, s, ctx, assetID, dtID)

			by := findingsByLabel(t, s, ctx)
			f, ok := by["asset missing a component its device type declares"]
			if !ok {
				t.Fatalf("EstateFindings did not carry the template-drift finding: %+v", by)
			}
			if f.Count != 1 {
				t.Errorf("count = %d, want 1", f.Count)
			}
		})
	}
}

// TestTemplateExtraFindingsReportsAnAssetWithMoreInterfacesThanItsTemplate is
// the counterpart the final whole-branch review asked for: a device type
// whose template names eth0 only, an asset built from it, and an operator (or
// a range that expanded to the wrong shape) adding eth99 besides. That extra
// port is invisible to TemplateDriftFindings by design -- see this file's own
// comment on TestTemplateDriftFindingsIgnoresExtraInterfaces -- so nothing
// catches it unless this finding does.
func TestTemplateExtraFindingsReportsAnAssetWithMoreInterfacesThanItsTemplate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}

			assetID := assetOfType(t, s, ctx, "extra-01", dtID, nil)
			extra, err := domain.NewInterface(NewID(), assetID, "eth99", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building eth99: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, extra); err != nil {
				t.Fatalf("recording eth99: %v", err)
			}

			findings, err := s.TemplateExtraFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateExtraFindings: %v", err)
			}
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
			}
			f := findings[0]
			if f.Severity != FindingGap {
				t.Errorf("severity = %q, want %q", f.Severity, FindingGap)
			}
			if f.Count != 1 {
				t.Errorf("count = %d, want 1", f.Count)
			}
			if !strings.Contains(f.Detail, "extra-01") || !strings.Contains(f.Detail, "eth99") {
				t.Errorf("detail = %q, want it to name the asset and the undeclared component", f.Detail)
			}
			if f.Href != "/assets/"+assetID {
				t.Errorf("href = %q, want it to link the drifting asset", f.Href)
			}
		})
	}
}

// TestTemplateExtraFindingsIsEmptyWhenTheAssetMatchesItsTemplate: nothing
// beyond what the template declares, nothing to report.
func TestTemplateExtraFindingsIsEmptyWhenTheAssetMatchesItsTemplate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}
			assetOfType(t, s, ctx, "extra-matched-01", dtID, nil)

			findings, err := s.TemplateExtraFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateExtraFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
			}
		})
	}
}

// TestTemplateExtraFindingsIgnoresATypeWithNoTemplateAtAll: a device type
// that has never declared a template makes no claim about what its assets
// should have, so an asset of that type is never "extra" no matter how many
// interfaces it carries. Without the EXISTS guard in templateExtraQuery,
// every ordinary port on every asset of an untemplated type would be flagged.
func TestTemplateExtraFindingsIgnoresATypeWithNoTemplateAtAll(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			assetID := assetOfType(t, s, ctx, "no-template-01", dtID, nil)
			iface, err := domain.NewInterface(NewID(), assetID, "eth0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building eth0: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, iface); err != nil {
				t.Fatalf("recording eth0: %v", err)
			}

			findings, err := s.TemplateExtraFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateExtraFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("got %d findings, want 0 -- the device type never declared a template: %+v", len(findings), findings)
			}
		})
	}
}

// TestTemplateExtraFindingsIgnoresAnAssetWithNoDeviceType: nothing to compare
// against.
func TestTemplateExtraFindingsIgnoresAnAssetWithNoDeviceType(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetID := assetOfType(t, s, ctx, "extra-no-type-01", "", nil)
			iface, err := domain.NewInterface(NewID(), assetID, "eth0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building eth0: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, iface); err != nil {
				t.Fatalf("recording eth0: %v", err)
			}

			findings, err := s.TemplateExtraFindings(ctx)
			if err != nil {
				t.Fatalf("TemplateExtraFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
			}
		})
	}
}

// TestEstateFindingsReachesTemplateExtraFindings is the registration proof
// (final whole-branch review, blocking #2's sibling): without
// TemplateExtraFindings appended into EstateFindings, this fails even though
// every test above, calling TemplateExtraFindings directly, stays green.
//
// Mutation: delete the "extra, err := s.TemplateExtraFindings..." block (and
// its append) from EstateFindings in findings.go and this goes red with
// "asset with a component its device type's template does not declare" never
// appearing in what EstateFindings returns; restore to go green again.
func TestEstateFindingsReachesTemplateExtraFindings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dtID := mustDeviceTypeForComponents(t, s)
			ff := domain.FFSFP28
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &ff,
			}); err != nil {
				t.Fatalf("declaring the template's eth0: %v", err)
			}
			assetID := assetOfType(t, s, ctx, "estate-extra-01", dtID, nil)
			extra, err := domain.NewInterface(NewID(), assetID, "eth99", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building eth99: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, extra); err != nil {
				t.Fatalf("recording eth99: %v", err)
			}

			by := findingsByLabel(t, s, ctx)
			f, ok := by["asset with a component its device type's template does not declare"]
			if !ok {
				t.Fatalf("EstateFindings did not carry the template-extra finding: %+v", by)
			}
			if f.Count != 1 {
				t.Errorf("count = %d, want 1", f.Count)
			}
		})
	}
}
