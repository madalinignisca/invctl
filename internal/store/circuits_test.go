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
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

func mustProvider(t *testing.T, s *SQLStore, ctx context.Context, name string) string {
	t.Helper()
	p, err := domain.NewProvider(NewID(), name)
	if err != nil {
		t.Fatalf("building provider: %v", err)
	}
	if err := s.CreateProvider(ctx, testActor, p); err != nil {
		t.Fatalf("creating provider: %v", err)
	}
	return p.ID
}

func mustCircuit(t *testing.T, s *SQLStore, ctx context.Context, providerID, cid string, end *string) string {
	t.Helper()
	c, err := domain.NewCircuit(NewID(), cid, providerID)
	if err != nil {
		t.Fatalf("building circuit: %v", err)
	}
	c.ContractEnd = end
	if err := s.CreateCircuit(ctx, testActor, c); err != nil {
		t.Fatalf("creating circuit: %v", err)
	}
	return c.ID
}

// TestAContractEndReachesTheExpiryReport.
//
// The whole reason this half of WP-E1 shipped first. A contract end is not an
// end of support -- nothing stops working -- but it needs a decision before a
// date, which is the identical question the report already answers for hardware
// and certificates. A second report nobody opens would be worse than a row in
// the one they already do.
func TestAContractEndReachesTheExpiryReport(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			asOf := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
			pid := mustProvider(t, s, ctx, "Telenor")
			soon := domain.FormatDate(asOf.AddDate(0, 2, 0))
			far := domain.FormatDate(asOf.AddDate(5, 0, 0))
			lapsed := domain.FormatDate(asOf.AddDate(0, 0, -30))

			mustCircuit(t, s, ctx, pid, "TN-100-SOON", &soon)
			mustCircuit(t, s, ctx, pid, "TN-200-FAR", &far)
			mustCircuit(t, s, ctx, pid, "TN-300-LAPSED", &lapsed)
			mustCircuit(t, s, ctx, pid, "TN-400-UNDATED", nil)

			report, err := s.Expiring(ctx, asOf, 12)
			if err != nil {
				t.Fatalf("building the report: %v", err)
			}
			found := map[string]string{}
			for _, row := range report.Rows {
				if row.EntityType == "circuit" {
					found[row.Name] = row.State
				}
			}
			if _, ok := found["TN-100-SOON"]; !ok {
				t.Error("a circuit renewing in two months is not in the report; the " +
					"contract would auto-renew at a rate nobody checked")
			}
			if got := found["TN-300-LAPSED"]; got != domain.ExpiryExpired {
				t.Errorf("a contract that ended a month ago reports %q, want %q", got, domain.ExpiryExpired)
			}
			if _, ok := found["TN-200-FAR"]; ok {
				t.Error("a contract ending in five years is inside a twelve-month horizon")
			}
			if _, ok := found["TN-400-UNDATED"]; ok {
				t.Error("a circuit with no contract end was given one")
			}
			// The provider has to be on the row: "TN-100-SOON expires" sends
			// nobody anywhere without knowing who to ring.
			for _, row := range report.Rows {
				if row.EntityType == "circuit" && row.Name == "TN-100-SOON" && row.Kind != "Telenor" {
					t.Errorf("the row names %q where the provider should be", row.Kind)
				}
			}
		})
	}
}

// TestACircuitCostAmortisesToItsContractEnd.
//
// The fourth cost surface reuses the existing machinery, and the one thing that
// genuinely differs is what a one-off spreads over: a circuit has no
// end-of-support, it has a contract, and an install fee spread over anything
// else is a made-up number.
func TestACircuitCostAmortisesToItsContractEnd(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustProvider(t, s, ctx, "Altibox")
			end := "2029-06-30"
			cid := mustCircuit(t, s, ctx, pid, "AB-900", &end)

			from := "2026-01-01"
			cost, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "connectivity", Period: domain.CostMonthly,
				AmountMinor: 89000, ValidFrom: &from,
			}, s.now())
			if err != nil {
				t.Fatalf("building cost: %v", err)
			}
			if err := s.AddCircuitCost(ctx, testActor, cid, cost); err != nil {
				t.Fatalf("adding circuit cost: %v", err)
			}

			rows, err := s.ListCircuitCosts(ctx, cid)
			if err != nil {
				t.Fatalf("listing circuit costs: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d cost rows, want 1", len(rows))
			}
			if rows[0].OwnerEOLDate == nil {
				t.Fatal("the cost has no horizon to amortise over; a circuit's life is " +
					"its contract and the query must read contract_end")
			}
			if *rows[0].OwnerEOLDate != end {
				t.Errorf("amortises to %s, want %s (the contract end)", *rows[0].OwnerEOLDate, end)
			}
		})
	}
}

// TestATerminationLandsExactlyOneEnd, and one side each.
func TestATerminationLandsExactlyOneEnd(t *testing.T) {
	site, port := "a-1", "i-1"
	cases := []struct {
		name    string
		side    string
		asset   *string
		iface   *string
		wantErr bool
	}{
		{"a site", domain.SideA, &site, nil, false},
		{"a port", domain.SideZ, nil, &port, false},
		{"both", domain.SideA, &site, &port, true},
		{"neither", domain.SideA, nil, nil, true},
		{"a side that is not a side", "middle", &site, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewCircuitTermination("t", "c", tc.side, tc.asset, tc.iface)
			if tc.wantErr && err == nil {
				t.Error("accepted, want refused")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

// TestOneSidePerCircuit. A second A end is a contradiction rather than extra
// information, and soft delete would make it permanent.
func TestOneSidePerCircuit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustProvider(t, s, ctx, "GlobalConnect")
			cid := mustCircuit(t, s, ctx, pid, "GC-1", nil)
			site1 := mustAsset(t, s, ctx, domain.KindSite, "dc-oslo", nil)
			site2 := mustAsset(t, s, ctx, domain.KindSite, "colo-fra1", nil)

			t1, _ := domain.NewCircuitTermination(NewID(), cid, domain.SideA, &site1, nil)
			if err := s.CreateCircuitTermination(ctx, testActor, t1); err != nil {
				t.Fatalf("landing the A end: %v", err)
			}
			t2, _ := domain.NewCircuitTermination(NewID(), cid, domain.SideA, &site2, nil)
			if err := s.CreateCircuitTermination(ctx, testActor, t2); err == nil {
				t.Error("a circuit was given two A ends")
			}

			// The Z end is fine, and now both ends are recorded.
			t3, _ := domain.NewCircuitTermination(NewID(), cid, domain.SideZ, &site2, nil)
			if err := s.CreateCircuitTermination(ctx, testActor, t3); err != nil {
				t.Fatalf("landing the Z end: %v", err)
			}
			rows, err := s.ListCircuits(ctx)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, r := range rows {
				if r.ID == cid && !r.Landed() {
					t.Errorf("a circuit with both ends recorded reports %d terminations",
						r.Terminations)
				}
			}
		})
	}
}

// TestAContractCannotEndBeforeItWasInstalled.
func TestAContractCannotEndBeforeItWasInstalled(t *testing.T) {
	c, err := domain.NewCircuit("c", "X-1", "p")
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	install, end := "2026-01-01", "2025-01-01"
	c.InstallDate, c.ContractEnd = &install, &end
	if err := c.Validate(); err == nil {
		t.Fatal("a contract ending before installation was accepted; it renders as two " +
			"plausible dates in different columns")
	} else if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}
