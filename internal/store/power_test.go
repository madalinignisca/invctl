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
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The power chain, and the finding it exists for.
//
// Almost every test here is built around the same trap: an asset with an A input
// and a B input that LOOKS redundant from every angle except the one that
// matters. "Two inputs" is what a spreadsheet records and what everybody
// believes; "two inputs, one panel" is the outage.

func mustPanel(t *testing.T, s *SQLStore, ctx context.Context, siteID, name string) string {
	t.Helper()
	p, err := domain.NewPowerPanel(NewID(), domain.PowerPanelSpec{SiteID: siteID, Name: name}, s.Now())
	if err != nil {
		t.Fatalf("building panel %s: %v", name, err)
	}
	if err := s.CreatePowerPanel(ctx, testActor, p); err != nil {
		t.Fatalf("creating panel %s: %v", name, err)
	}
	return p.ID
}

func mustFeed(t *testing.T, s *SQLStore, ctx context.Context, panelID, name string, volts, amps int) string {
	t.Helper()
	spec := domain.PowerFeedSpec{PanelID: panelID, Name: name}
	if volts > 0 {
		spec.Voltage, spec.Amperage = &volts, &amps
	}
	f, err := domain.NewPowerFeed(NewID(), spec, s.Now())
	if err != nil {
		t.Fatalf("building feed %s: %v", name, err)
	}
	if err := s.CreatePowerFeed(ctx, testActor, f); err != nil {
		t.Fatalf("creating feed %s: %v", name, err)
	}
	return f.ID
}

func mustInput(t *testing.T, s *SQLStore, ctx context.Context, assetID, feedID, name string, draw *int) string {
	t.Helper()
	i, err := domain.NewPowerInput(NewID(), domain.PowerInputSpec{
		AssetID: assetID, FeedID: feedID, Name: name, DrawVA: draw,
	}, s.Now())
	if err != nil {
		t.Fatalf("building input %s: %v", name, err)
	}
	if err := s.CreatePowerInput(ctx, testActor, i); err != nil {
		t.Fatalf("creating input %s: %v", name, err)
	}
	return i.ID
}

func findingsOfKind(r *PowerReport, kind string) []PowerFinding {
	var out []PowerFinding
	for _, f := range r.Findings {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func TestTwoInputsOnOnePanelIsNotRedundancy(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)

			// THE TRAP. Two panels exist, so the estate looks properly built --
			// but this box's A and B both land on panel-1.
			p1 := mustPanel(t, s, ctx, site, "panel-1")
			p2 := mustPanel(t, s, ctx, site, "panel-2")
			f1a := mustFeed(t, s, ctx, p1, "F1", 230, 32)
			f1b := mustFeed(t, s, ctx, p1, "F2", 230, 32)
			f2a := mustFeed(t, s, ctx, p2, "F1", 230, 32)

			trap := mustAsset(t, s, ctx, domain.KindServer, "believed-redundant", &site)
			mustInput(t, s, ctx, trap, f1a, "A", nil)
			mustInput(t, s, ctx, trap, f1b, "B", nil)

			// The control: genuinely redundant, and it must NOT be reported.
			// Without this the test passes on an implementation that flags every
			// asset with two inputs.
			ok := mustAsset(t, s, ctx, domain.KindServer, "actually-redundant", &site)
			mustInput(t, s, ctx, ok, f1a, "A", nil)
			mustInput(t, s, ctx, ok, f2a, "B", nil)

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			found := findingsOfKind(report, FindingFalseRedundancy)
			if len(found) != 1 {
				t.Fatalf("%d false-redundancy findings, want exactly 1: %+v", len(found), found)
			}
			if found[0].EntityID != trap {
				t.Errorf("reported %q, want believed-redundant", found[0].Name)
			}
			// The sentence has to name the panel. "This is not redundant" without
			// saying what they share sends somebody to trace it by hand.
			if !strings.Contains(found[0].Detail, "panel-1") {
				t.Errorf("detail = %q, want it to name the shared panel", found[0].Detail)
			}
			if !strings.Contains(found[0].Detail, "A") || !strings.Contains(found[0].Detail, "B") {
				t.Errorf("detail = %q, want it to name the inputs", found[0].Detail)
			}
		})
	}
}

func TestSingleFedIsReportedOnlyWhereSomethingRidesOnIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)

			// Carries a service.
			host := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", &site, env)
			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "app", Name: "App", Kind: domain.SvcAPI,
				EnvironmentID: env, Availability: domain.AvailStandalone, Tier: 2,
			}, s.Now())
			if err != nil {
				t.Fatalf("building service: %v", err)
			}
			if err := s.CreateService(ctx, testActor, svc); err != nil {
				t.Fatalf("creating service: %v", err)
			}
			si, err := domain.NewServiceInstance(NewID(), svc.ID, host, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, testActor, si); err != nil {
				t.Fatalf("placing the service: %v", err)
			}
			mustInput(t, s, ctx, host, feed, "A", nil)

			// Carries nothing. Single-fed on purpose, like most things in a real
			// estate -- and reporting it would bury the finding above in a list
			// nobody reads.
			panelBox := mustAsset(t, s, ctx, domain.KindPatchPanel, "patch-1", &site)
			mustInput(t, s, ctx, panelBox, feed, "A", nil)

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			found := findingsOfKind(report, FindingSingleFed)
			if len(found) != 1 {
				t.Fatalf("%d single-fed findings, want exactly 1: %+v", len(found), found)
			}
			if found[0].EntityID != host {
				t.Errorf("reported %q, want hv-01 -- the one carrying a service", found[0].Name)
			}
			if found[0].ServiceCount != 1 {
				t.Errorf("service count = %d, want 1 so a reader can size it", found[0].ServiceCount)
			}
		})
	}
}

func TestAFeedOverItsDeratedCapacityIsAFinding(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")

			// 230 V × 16 A = 3680 VA, derated to 80% = 2944 usable.
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 16)
			a := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			b := mustAsset(t, s, ctx, domain.KindServer, "srv-2", &site)
			draw := 1600
			mustInput(t, s, ctx, a, feed, "A", &draw)
			mustInput(t, s, ctx, b, feed, "A", &draw) // 3200 > 2944

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			found := findingsOfKind(report, FindingOverAllocated)
			if len(found) != 1 {
				t.Fatalf("%d over-allocation findings, want 1: %+v", len(found), found)
			}
			if !strings.Contains(found[0].Detail, "2944") {
				t.Errorf("detail = %q, want it to state the usable figure, not just that it is over",
					found[0].Detail)
			}

			// The control: under the derating is silent. Without it this passes on
			// an implementation that reports every rated feed.
			quiet := mustFeed(t, s, ctx, panel, "F2", 230, 32)
			c := mustAsset(t, s, ctx, domain.KindServer, "srv-3", &site)
			mustInput(t, s, ctx, c, quiet, "A", &draw)
			report, err = s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			if got := len(findingsOfKind(report, FindingOverAllocated)); got != 1 {
				t.Errorf("%d over-allocation findings after adding a feed well under its "+
					"derating, want still 1", got)
			}
		})
	}
}

// TestAnUnratedFeedIsCountedNotAccused is the difference between a gap in the
// record and a fault in the estate.
func TestAnUnratedFeedIsCountedNotAccused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 0, 0) // no rating recorded
			a := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			draw := 9000
			mustInput(t, s, ctx, a, feed, "A", &draw)

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			if got := len(findingsOfKind(report, FindingOverAllocated)); got != 0 {
				t.Errorf("%d over-allocation findings against a feed with no rating. "+
					"A capacity read as zero makes everything look over-allocated, and "+
					"reporting a missing rating as a fault sends the wrong person to fix "+
					"the wrong thing.", got)
			}
			if report.UnratedFeeds != 1 {
				t.Errorf("unrated feeds = %d, want 1 -- the number that stops a short "+
					"report reading as a clean bill of health", report.UnratedFeeds)
			}
		})
	}
}

// TestOnlyLosingEveryInputTakesAnAssetDown is the resolver impact simulation
// depends on.
func TestOnlyLosingEveryInputTakesAnAssetDown(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			p1 := mustPanel(t, s, ctx, site, "panel-1")
			p2 := mustPanel(t, s, ctx, site, "panel-2")
			fa := mustFeed(t, s, ctx, p1, "F1", 230, 32)
			fb := mustFeed(t, s, ctx, p2, "F1", 230, 32)

			redundant := mustAsset(t, s, ctx, domain.KindServer, "srv-ab", &site)
			mustInput(t, s, ctx, redundant, fa, "A", nil)
			mustInput(t, s, ctx, redundant, fb, "B", nil)

			single := mustAsset(t, s, ctx, domain.KindServer, "srv-a", &site)
			mustInput(t, s, ctx, single, fa, "A", nil)

			// One feed fails.
			down, err := s.AssetsLosingPower(ctx, []string{fa})
			if err != nil {
				t.Fatalf("resolving: %v", err)
			}
			if len(down) != 1 || down[0] != single {
				t.Fatalf("losing one feed took down %v, want only the single-fed box.\n"+
					"A resolver that returns everything on the feed models redundancy and "+
					"then ignores it.", down)
			}

			// Both feeds fail.
			down, err = s.AssetsLosingPower(ctx, []string{fa, fb})
			if err != nil {
				t.Fatalf("resolving: %v", err)
			}
			if len(down) != 2 {
				t.Errorf("losing both feeds took down %d assets, want 2", len(down))
			}
		})
	}
}

func TestRetiredPowerRowsLeaveTheFindings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			f1 := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			f2 := mustFeed(t, s, ctx, panel, "F2", 230, 32)
			box := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, box, f1, "A", nil)
			inputB := mustInput(t, s, ctx, box, f2, "B", nil)

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			if len(findingsOfKind(report, FindingFalseRedundancy)) != 1 {
				t.Fatal("the false-redundancy case was not set up")
			}

			// Unplug B. The asset is now single-fed, not falsely redundant -- and
			// a retired input that still counted would keep reporting a finding
			// about a cable nobody has.
			if err := s.RetirePowerInput(ctx, testActor, inputB); err != nil {
				t.Fatalf("retiring input: %v", err)
			}
			report, err = s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			if got := len(findingsOfKind(report, FindingFalseRedundancy)); got != 0 {
				t.Errorf("%d false-redundancy findings after retiring one input, want 0", got)
			}
		})
	}
}

func TestThePowerChainRefusesWhatWouldStrandIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			box := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			input := mustInput(t, s, ctx, box, feed, "A", nil)

			if err := s.RetirePowerPanel(ctx, testActor, panel); !errors.Is(err, domain.ErrConflict) {
				t.Errorf("retiring a panel with live feeds returned %v, want ErrConflict", err)
			}
			if err := s.RetirePowerFeed(ctx, testActor, feed); !errors.Is(err, domain.ErrConflict) {
				t.Errorf("retiring a feed with live inputs returned %v, want ErrConflict.\n"+
					"The assets on it would claim power from a circuit the model says is "+
					"gone, and the redundancy finding would read them as single-fed.", err)
			}

			// The control: unwind it in the real-world order and each step works.
			if err := s.RetirePowerInput(ctx, testActor, input); err != nil {
				t.Fatalf("retiring the input: %v", err)
			}
			if err := s.RetirePowerFeed(ctx, testActor, feed); err != nil {
				t.Fatalf("retiring the feed: %v", err)
			}
			if err := s.RetirePowerPanel(ctx, testActor, panel); err != nil {
				t.Fatalf("retiring the panel: %v", err)
			}
		})
	}
}

func TestCapacityArithmetic(t *testing.T) {
	v, a := 230, 32
	three := domain.PhaseThree
	cases := []struct {
		name string
		r    domain.Rating
		want int
		ok   bool
	}{
		{"single phase", domain.Rating{Voltage: &v, Amperage: &a}, 7360, true},
		{"three phase is line-to-line", domain.Rating{Voltage: &v, Amperage: &a, Phase: &three}, 12747, true},
		{"no voltage", domain.Rating{Amperage: &a}, 0, false},
		{"no amperage", domain.Rating{Voltage: &v}, 0, false},
		{"nothing recorded", domain.Rating{}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.r.CapacityVA()
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("capacity = %d VA, want %d", got, tc.want)
			}
		})
	}

	// An unstated phase is treated as single, which reports LESS capacity. The
	// conservative direction matters: a feed should err towards looking full
	// rather than towards looking safe.
	unstated := domain.Rating{Voltage: &v, Amperage: &a}
	stated := domain.Rating{Voltage: &v, Amperage: &a, Phase: &three}
	u, _ := unstated.CapacityVA()
	st, _ := stated.CapacityVA()
	if u >= st {
		t.Errorf("an unstated phase reports %d VA and three-phase reports %d; the unknown "+
			"case must be the smaller one", u, st)
	}
}

// The supply layer, and the false negative it exists to close.

func mustSource(t *testing.T, s *SQLStore, ctx context.Context, siteID, name, kind string, parent *string) string {
	t.Helper()
	src, err := domain.NewPowerSource(NewID(), domain.PowerSourceSpec{
		SiteID: siteID, Name: name, Kind: kind, ParentID: parent,
	}, s.Now())
	if err != nil {
		t.Fatalf("building supply %s: %v", name, err)
	}
	if err := s.CreatePowerSource(ctx, testActor, src); err != nil {
		t.Fatalf("creating supply %s: %v", name, err)
	}
	return src.ID
}

func panelOn(t *testing.T, s *SQLStore, ctx context.Context, siteID, name, sourceID string) string {
	t.Helper()
	p, err := domain.NewPowerPanel(NewID(), domain.PowerPanelSpec{
		SiteID: siteID, Name: name, SourceID: &sourceID,
	}, s.Now())
	if err != nil {
		t.Fatalf("building panel %s: %v", name, err)
	}
	if err := s.CreatePowerPanel(ctx, testActor, p); err != nil {
		t.Fatalf("creating panel %s: %v", name, err)
	}
	return p.ID
}

// TestTwoPanelsBehindOneUPSIsNotRedundancyEither is the whole reason the supply
// layer exists.
//
// The ordinary 2N build: a generator behind two UPS groups, boards under each.
// A dual-fed server on A1 and A2 is on TWO PANELS -- which the panel-only model
// reported as genuinely redundant, and which is the more dangerous kind of wrong
// answer because the tool actively reassures. Both boards are behind UPS-A.
func TestTwoPanelsBehindOneUPSIsNotRedundancyEither(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)

			gen := mustSource(t, s, ctx, site, "GEN-1", domain.SourceGenerator, nil)
			upsA := mustSource(t, s, ctx, site, "UPS-A", domain.SourceUPS, &gen)
			upsB := mustSource(t, s, ctx, site, "UPS-B", domain.SourceUPS, &gen)

			a1 := panelOn(t, s, ctx, site, "A1", upsA)
			a2 := panelOn(t, s, ctx, site, "A2", upsA)
			b1 := panelOn(t, s, ctx, site, "B1", upsB)

			fa1 := mustFeed(t, s, ctx, a1, "F1", 230, 32)
			fa2 := mustFeed(t, s, ctx, a2, "F1", 230, 32)
			fb1 := mustFeed(t, s, ctx, b1, "F1", 230, 32)

			// TWO PANELS, ONE UPS. The old model said nothing about this.
			trap := mustAsset(t, s, ctx, domain.KindServer, "same-ups", &site)
			mustInput(t, s, ctx, trap, fa1, "A", nil)
			mustInput(t, s, ctx, trap, fa2, "B", nil)

			// Properly 2N: one side each. Converges only at the generator, which
			// is the design.
			proper := mustAsset(t, s, ctx, domain.KindServer, "proper-2n", &site)
			mustInput(t, s, ctx, proper, fa1, "A", nil)
			mustInput(t, s, ctx, proper, fb1, "B", nil)

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}

			byAsset := map[string]PowerFinding{}
			for _, f := range report.Findings {
				byAsset[f.EntityID] = f
			}

			got, ok := byAsset[trap]
			if !ok {
				t.Fatal("two panels behind ONE UPS was not reported. That is the false " +
					"negative the supply layer exists to close, and it is the dangerous " +
					"kind: the tool actively reassures.")
			}
			if got.Severity != PowerSeverityFault {
				t.Errorf("severity = %q, want %q -- both feeds die the instant that UPS does",
					got.Severity, PowerSeverityFault)
			}
			if !strings.Contains(got.Detail, "UPS-A") {
				t.Errorf("detail = %q, want it to name the UPS they converge on", got.Detail)
			}

			// The properly built one IS reported, but as the design rather than a
			// fault -- and it must not inflate the alarming count.
			good, ok := byAsset[proper]
			if !ok {
				t.Fatal("a properly 2N asset produced no finding at all; converging at the " +
					"generator is worth SAYING, it is just not an alarm")
			}
			if good.Severity != PowerSeverityExpected {
				t.Errorf("severity = %q for an asset fed from both UPS groups, want %q. "+
					"Calling the generator a single point of failure reports the safety "+
					"measure as the hazard.", good.Severity, PowerSeverityExpected)
			}
			if report.FalseRedundancy != 1 {
				t.Errorf("false-redundancy count = %d, want 1 -- the expected convergence "+
					"must not inflate it", report.FalseRedundancy)
			}
			if report.SharedUpstream != 1 {
				t.Errorf("shared-upstream count = %d, want 1", report.SharedUpstream)
			}
		})
	}
}

func TestUnsourcedPanelsAreCountedSoSilenceMeansSomething(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			p1 := mustPanel(t, s, ctx, site, "A1") // no supply recorded
			p2 := mustPanel(t, s, ctx, site, "A2")
			f1 := mustFeed(t, s, ctx, p1, "F1", 230, 32)
			f2 := mustFeed(t, s, ctx, p2, "F1", 230, 32)

			box := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, box, f1, "A", nil)
			mustInput(t, s, ctx, box, f2, "B", nil)

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("finding: %v", err)
			}
			// Nothing can be said: with no supply above either board, two panels
			// look independent whether or not they are.
			if report.FalseRedundancy != 0 {
				t.Errorf("%d findings from panels with no supply recorded; nothing is known "+
					"about what is above them", report.FalseRedundancy)
			}
			// So the report has to say the silence means "not known".
			if report.UnsourcedPanels != 2 {
				t.Errorf("unsourced panels = %d, want 2. Without this number a silent "+
					"report reads as 'checked and fine' when it means 'not known'.",
					report.UnsourcedPanels)
			}
		})
	}
}

func TestASupplyChainCannotLoop(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			gen := mustSource(t, s, ctx, site, "GEN-1", domain.SourceGenerator, nil)
			ups := mustSource(t, s, ctx, site, "UPS-A", domain.SourceUPS, &gen)

			// Feed the generator from the UPS it feeds.
			row, err := s.GetPowerSource(ctx, gen)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			row.ParentID = &ups
			err = s.UpdatePowerSource(ctx, testActor, &row.PowerSource)
			if err == nil {
				t.Fatal("a supply chain was allowed to loop; every walk up it would then " +
					"depend on a depth guard rather than on the data being sane")
			}
			if msg := fieldError(err, "parent_id"); msg == "" {
				t.Errorf("error = %v, want a field failure on parent_id", err)
			}

			// The control: a legitimate re-parent still works.
			util := mustSource(t, s, ctx, site, "UTIL-1", domain.SourceUtility, nil)
			row, err = s.GetPowerSource(ctx, gen)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			row.ParentID = &util
			if err := s.UpdatePowerSource(ctx, testActor, &row.PowerSource); err != nil {
				t.Errorf("a legitimate re-parent was refused: %v", err)
			}
		})
	}
}
