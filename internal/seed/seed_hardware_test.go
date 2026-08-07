// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"

	"github.com/madalinignisca/invctl/internal/store"
)

// The fixture has to DEMONSTRATE the findings, not merely contain rows.
//
// A seeded estate that happens to have power tables proves the tables exist. It
// is worth nothing to somebody opening the software for the first time, and it
// silently stops being a demonstration the moment a rule changes. So the shapes
// are asserted here: if the fixture stops showing a false redundancy, this says
// so rather than the demo quietly going blank.

func TestTheFixtureDemonstratesEveryPowerFinding(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		report, err := s.PowerFindings(ctx)
		if err != nil {
			t.Fatalf("power findings: %v", err)
		}

		byName := map[string]store.PowerFinding{}
		for _, f := range report.Findings {
			byName[f.Name] = f
		}

		// The subtle one: two boards, one UPS. Invisible to anything stopping at
		// the panel, and the reason the supply layer exists.
		hv1, ok := byName["hv-01"]
		if !ok || hv1.Severity != store.PowerSeverityFault {
			t.Errorf("hv-01 = %+v, want a FAULT: its two boards share UPS-A", hv1)
		}
		if !strings.Contains(hv1.Detail, "UPS-A") {
			t.Errorf("hv-01 detail = %q, want it to name the UPS", hv1.Detail)
		}

		// The obvious one: both leads on one board.
		if f, ok := byName["sw-core-1"]; !ok || !strings.Contains(f.Detail, "DB-A") {
			t.Errorf("sw-core-1 = %+v, want a false redundancy naming panel DB-A", f)
		}

		// Properly 2N: reported, but as the design. If this ever reads as a fault
		// the report is crying wolf about a correct build.
		if f, ok := byName["hv-02"]; !ok || f.Severity != store.PowerSeverityExpected {
			t.Errorf("hv-02 = %+v, want an EXPECTED convergence at the generator", f)
		}

		// Single-fed, carrying services.
		if f, ok := byName["hv-03"]; !ok || f.Kind != store.FindingSingleFed {
			t.Errorf("hv-03 = %+v, want a single-fed finding", f)
		}

		// And the control: single-fed but carrying nothing, so silent. Without this
		// the fixture would pass on a rule that reports every single lead.
		if f, ok := byName["pdu-a1"]; ok {
			t.Errorf("pdu-a1 produced %+v; it is single-fed on purpose and carries no "+
				"services, so reporting it would make the finding meaningless", f)
		}

		if report.FalseRedundancy != 2 || report.SharedUpstream != 1 || report.SingleFed != 1 {
			t.Errorf("counts = %d fault / %d expected / %d single-fed, want 2 / 1 / 1",
				report.FalseRedundancy, report.SharedUpstream, report.SingleFed)
		}
	})
}

func TestTheFixtureShowsBothEOLProvenances(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		assets, err := s.ListAssets(ctx, store.AssetFilter{})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		byName := map[string]store.AssetRow{}
		for _, a := range assets {
			byName[a.Name] = a
		}

		// Inherited: no date of its own, so the model's stands in. pdu-a1 carries
		// this because every other catalogued asset has a date from lifetimes().
		pdu := byName["pdu-a1"]
		if !pdu.InheritedEOL() {
			t.Error("pdu-a1 does not inherit a support date; the catalogue is not " +
				"demonstrating what it is for")
		}

		// And hv-03 stays UNCATALOGUED, so the expiry report's "nobody wrote the
		// dates down" callout still has something to count. Losing that would be
		// a silent regression in a different report.
		if hv3 := byName["hv-03"]; hv3.DeviceTypeID != nil || hv3.ResolvedEOL() != nil {
			t.Error("hv-03 has gained a date; it is the fixture's undated asset and the " +
				"expiry callout needs one")
		}

		// Overridden: same model as its siblings, but a date recorded on the box.
		hv := byName["hv-01"]
		if hv.DeviceTypeID == nil {
			t.Fatal("hv-01 is not linked to a catalogued model")
		}
		if hv.InheritedEOL() {
			t.Error("hv-01 inherits its model's date; the fixture is meant to show an " +
				"override sitting beside an inheritance")
		}
	})
}

// TestTheCompanyLayerLoadsAndIsOffByDefault covers both halves of a gated
// fixture: that it builds at all, and that a deployment which did not ask for
// it does not get somebody else's company.
func TestTheCompanyLayerLoadsAndIsOffByDefault(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx
		assets, err := s.ListAssets(ctx, store.AssetFilter{Limit: 500})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		for _, a := range assets {
			if a.Name == "colo-fra1" || a.Name == "fw-prod-1" {
				t.Fatalf("%s is in the base fixture; the company layer must be gated, "+
					"or several hundred tests describe an estate nobody chose", a.Name)
			}
		}
	})
}

// TestTheCompanyLayerBuilds loads the gated layer and checks the shape it is
// meant to produce: three racks on-prem, a rented colo, a firewall per
// environment, three ISP handoffs, and an internal certificate per service.
//
// It asserts the SHAPE rather than exact counts, because the point of the layer
// is that somebody can add a rack to it without a test objecting -- but not that
// they can delete the colo without one.
func TestTheCompanyLayerBuilds(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx
		assets, err := s.ListAssets(ctx, store.AssetFilter{Limit: 500})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		byName := map[string]store.AssetRow{}
		kinds := map[string]int{}
		for _, a := range assets {
			byName[a.Name] = a
			kinds[a.Kind]++
		}

		for _, want := range []string{"rack-a2", "colo-fra1", "colo-rack-07", "sw-colo-1",
			"srv-colo-1", "srv-colo-2", "srv-colo-3", "fw-prod-1", "fw-dev-1", "fw-colo-1"} {
			if _, ok := byName[want]; !ok {
				t.Errorf("the company estate has no %s", want)
			}
		}
		if kinds[domain.KindRack] < 3 {
			t.Errorf("%d racks, want at least 3", kinds[domain.KindRack])
		}
		if kinds[domain.KindFirewall] < 4 {
			t.Errorf("%d firewalls, want at least 4 -- one per environment plus the upstream",
				kinds[domain.KindFirewall])
		}

		// The rented machines carry a CONTRACT date of their own, which is what
		// the override is for: same model as the on-prem boxes, different
		// support.
		colo := byName["srv-colo-1"]
		if colo.EOLDate == nil {
			t.Error("a rented server has no support date; the contract is the whole " +
				"difference between it and a box you own")
		}
		if colo.InheritedEOL() {
			t.Error("a rented server inherits its model's date rather than its contract's")
		}

		// The upstream firewall carries a port per ISP handoff.
		edge := byName["fw-edge-1"]
		ports, err := s.ListInterfaces(ctx, edge.ID)
		if err != nil {
			t.Fatalf("listing ports: %v", err)
		}
		var wan int
		for _, p := range ports {
			if strings.HasPrefix(p.Name, "wan") {
				wan++
			}
		}
		if wan < 3 {
			t.Errorf("the upstream firewall has %d WAN ports, want 3 -- one per provider", wan)
		}

		// Certificates: a few, and at least one already lapsed so the expiry
		// report has something to be loud about.
		certs, err := s.ListCertificates(ctx, store.CertificateFilter{})
		if err != nil {
			t.Fatalf("listing certificates: %v", err)
		}
		if len(certs) < 6 {
			t.Errorf("%d certificates, want the base fixture's plus the internal ones", len(certs))
		}
	})
}
