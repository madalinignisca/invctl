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

// The failure a summary invites is drift: a second implementation of "what is
// wrong" that disagrees with the page it summarises, and is believed because it
// is on the front page. These check that it cannot.

func findingsByLabel(t *testing.T, s *SQLStore, ctx context.Context) map[string]Finding {
	t.Helper()
	rows, err := s.EstateFindings(ctx)
	if err != nil {
		t.Fatalf("gathering findings: %v", err)
	}
	out := map[string]Finding{}
	for _, f := range rows {
		out[f.Label] = f
	}
	return out
}

// TestTheOverviewAgreesWithEveryPageItSummarises.
//
// Each count is checked against the SAME store method the dedicated page calls.
// If somebody optimises the summary by counting differently -- a cheaper query,
// a cached number -- this fails, which is the whole point: the overview may be
// derived and must never be independent.
func TestTheOverviewAgreesWithEveryPageItSummarises(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			seedFindingFixture(t, s, ctx)

			found := findingsByLabel(t, s, ctx)

			t.Run("redundancy", func(t *testing.T) {
				groups, err := s.ListFHRPGroups(ctx)
				if err != nil {
					t.Fatalf("listing groups: %v", err)
				}
				want := 0
				for _, g := range groups {
					if g.Redundancy() == domain.FHRPSingleMember {
						want++
					}
				}
				if got := found["redundancy group with one member"].Count; got != want {
					t.Errorf("the overview says %d single-member groups, the redundancy "+
						"page says %d", got, want)
				}
			})

			t.Run("overlays", func(t *testing.T) {
				overlays, err := s.ListL2VPNs(ctx)
				if err != nil {
					t.Fatalf("listing overlays: %v", err)
				}
				want := 0
				for _, o := range overlays {
					if o.Reach() == domain.L2VPNUnattached {
						want++
					}
				}
				if got := found["overlay with nothing attached"].Count; got != want {
					t.Errorf("the overview says %d unattached overlays, the overlays "+
						"page says %d", got, want)
				}
			})

			t.Run("circuits", func(t *testing.T) {
				circuits, err := s.ListCircuits(ctx)
				if err != nil {
					t.Fatalf("listing circuits: %v", err)
				}
				want := 0
				for _, c := range circuits {
					if !c.Landed() {
						want++
					}
				}
				if got := found["circuit missing an end"].Count; got != want {
					t.Errorf("the overview says %d unlanded circuits, the circuits page "+
						"says %d", got, want)
				}
			})

			t.Run("expiry", func(t *testing.T) {
				report, err := s.Expiring(ctx, s.now(), ExpiryHorizonMonths)
				if err != nil {
					t.Fatalf("building the expiry report: %v", err)
				}
				if got := found["past its date"].Count; got != report.Expired {
					t.Errorf("the overview says %d expired, the expiry report says %d",
						got, report.Expired)
				}
				if got := found["expiring soon"].Count; got != report.Soon {
					t.Errorf("the overview says %d expiring soon, the expiry report says %d",
						got, report.Soon)
				}
			})
		})
	}
}

// TestAnExpectedConvergenceIsNotAFault.
//
// A generator behind two UPS groups is the ordinary 2N design. Counting it as a
// fault teaches people the front page cries wolf, and a page nobody trusts is
// worse than no page.
//
// THIS ASSERTS THE OUTCOME, NOT THE ARITHMETIC. The first version computed the
// expected number by filtering PowerSeverityExpected -- the same expression the
// source uses -- so removing that filter moved both sides and the test passed
// on the bug. It now builds an estate whose ONLY power finding is the expected
// one and asserts the overview stays silent, which no amount of recomputing can
// satisfy incorrectly.
func TestAnExpectedConvergenceIsNotAFault(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			seedFindingFixture(t, s, ctx)

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("power findings: %v", err)
			}
			if len(report.Findings) == 0 {
				t.Fatal("the fixture produced no power findings at all, so this test " +
					"would pass whatever the severity filter did")
			}
			for _, f := range report.Findings {
				if f.Severity != PowerSeverityExpected {
					t.Fatalf("the fixture produced a real power fault (%s), so a silent "+
						"overview would be wrong for a different reason", f.Name)
				}
			}

			found := findingsByLabel(t, s, ctx)
			if got, ok := found["power convergence"]; ok {
				t.Errorf("the overview reports %d power convergence fault(s) on an estate "+
					"whose only convergence is the design -- a generator behind two UPS "+
					"groups is what makes a utility failure survivable", got.Count)
			}
		})
	}
}

// TestFindingsAreOrderedWorstFirst. Somebody scans the top three rows; a gap
// above a fault means the wrong three.
func TestFindingsAreOrderedWorstFirst(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			seedFindingFixture(t, s, ctx)

			rows, err := s.EstateFindings(ctx)
			if err != nil {
				t.Fatalf("gathering: %v", err)
			}
			if len(rows) < 2 {
				t.Skip("too few findings to order")
			}
			for i := 1; i < len(rows); i++ {
				if severityRank(rows[i-1].Severity) > severityRank(rows[i].Severity) {
					t.Errorf("row %d is %q and row %d is %q; faults must precede risks "+
						"and risks gaps", i-1, rows[i-1].Severity, i, rows[i].Severity)
				}
			}
		})
	}
}

// TestAQuietEstateSaysSoRatherThanNothing. An empty slice is a real answer and
// the template renders it as such; a nil error with a silent page is not.
func TestAQuietEstateSaysSoRatherThanNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			rows, err := s.EstateFindings(ctx)
			if err != nil {
				t.Fatalf("an empty estate produced an error: %v", err)
			}
			for _, f := range rows {
				if f.Count == 0 {
					t.Errorf("a zero-count finding was emitted: %q. A row saying "+
						"'0 things are wrong' is noise on a page meant to be scanned", f.Label)
				}
			}
		})
	}
}

// seedFindingFixture builds one of each kind the overview reports.
func seedFindingFixture(t *testing.T, s *SQLStore, ctx context.Context) {
	t.Helper()

	// A redundancy group with one router.
	gid := mustFHRP(t, s, ctx, 10, "gw-single")
	a1 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-lonely", nil)
	i1 := mustInterface(t, s, ctx, a1, "eth0")
	if err := s.SetFHRPMembers(ctx, testActor, gid, []domain.FHRPMember{
		{GroupID: gid, InterfaceID: i1},
	}); err != nil {
		t.Fatalf("setting members: %v", err)
	}

	// An overlay attached to nothing.
	vpn, err := domain.NewL2VPN(NewID(), "ovl-empty", domain.L2VPNVXLAN)
	if err != nil {
		t.Fatalf("building overlay: %v", err)
	}
	if err := s.CreateL2VPN(ctx, testActor, vpn); err != nil {
		t.Fatalf("creating overlay: %v", err)
	}

	// A circuit with neither end recorded.
	pid := mustProvider(t, s, ctx, "Someone")
	mustCircuit(t, s, ctx, pid, "CID-1", nil)

	// DATES, BOTH SIDES OF TODAY. Without these the expiry assertions compare
	// zero against zero and pass however the counts are wired -- swapping
	// Expired for Soon in the source survived until this existed.
	// TWO expired and ONE renewing, deliberately unequal. With one of each the
	// counts match, and swapping Expired for Soon in the source changes nothing
	// a test can see -- which is exactly what happened.
	mustCertificate(t, s, ctx, "lapsed.example.com", nil,
		domain.FormatDate(s.now().AddDate(0, 0, -30)))
	mustCertificate(t, s, ctx, "lapsed-two.example.com", nil,
		domain.FormatDate(s.now().AddDate(0, 0, -10)))
	mustCertificate(t, s, ctx, "renewing.example.com", nil,
		domain.FormatDate(s.now().AddDate(0, 0, 20)))

	// AN EXPECTED CONVERGENCE: a generator behind two UPS groups, which is the
	// ordinary 2N design and must NOT be counted as a fault. Without it the
	// power assertion compares zero against zero and passes even when the
	// severity filter is removed.
	site := mustAsset(t, s, ctx, domain.KindSite, "dc-findings", nil)
	gen := mustSource(t, s, ctx, site, "GEN-1", domain.SourceGenerator, nil)
	upsA := mustSource(t, s, ctx, site, "UPS-A", domain.SourceUPS, &gen)
	upsB := mustSource(t, s, ctx, site, "UPS-B", domain.SourceUPS, &gen)
	// A board names its supply at creation, so the panels are built here rather
	// than through mustPanel -- an unsourced panel makes the convergence
	// invisible, which is the state this fixture exists to avoid.
	panelUnder := func(name string, source string) string {
		t.Helper()
		p, err := domain.NewPowerPanel(NewID(), domain.PowerPanelSpec{
			SiteID: site, Name: name, SourceID: &source,
		}, s.now())
		if err != nil {
			t.Fatalf("building panel %s: %v", name, err)
		}
		if err := s.CreatePowerPanel(ctx, testActor, p); err != nil {
			t.Fatalf("creating panel %s: %v", name, err)
		}
		return p.ID
	}
	panelA := panelUnder("DB-A", upsA)
	panelB := panelUnder("DB-B", upsB)
	feedA := mustFeed(t, s, ctx, panelA, "A-1", 230, 16)
	feedB := mustFeed(t, s, ctx, panelB, "B-1", 230, 16)
	host := mustAsset(t, s, ctx, domain.KindServer, "srv-2n", nil)
	draw := 200
	mustInput(t, s, ctx, host, feedA, "psu-a", &draw)
	mustInput(t, s, ctx, host, feedB, "psu-b", &draw)
}
