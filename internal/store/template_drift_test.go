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
// noise. Only a MISSING named component is drift.
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
