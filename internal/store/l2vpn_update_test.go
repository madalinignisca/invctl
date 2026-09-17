// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestUpdateL2VPNCorrectsAndAudits.
func TestUpdateL2VPNCorrectsAndAudits(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			v, err := domain.NewL2VPN(NewID(), "prod-overlay-typo", domain.L2VPNVXLAN)
			if err != nil {
				t.Fatalf("building overlay: %v", err)
			}
			if err := s.CreateL2VPN(ctx, testPermit, v); err != nil {
				t.Fatalf("creating overlay: %v", err)
			}

			got, err := s.GetL2VPN(ctx, v.ID)
			if err != nil {
				t.Fatalf("getting overlay: %v", err)
			}
			got.Name = "prod-overlay"
			if err := s.UpdateL2VPN(ctx, testPermit, got); err != nil {
				t.Fatalf("updating overlay: %v", err)
			}

			after, err := s.GetL2VPN(ctx, v.ID)
			if err != nil {
				t.Fatalf("re-reading overlay: %v", err)
			}
			if after.Name != "prod-overlay" {
				t.Errorf("name = %q, want %q", after.Name, "prod-overlay")
			}

			changes, err := s.ListChangesForEntity(ctx, "l2vpn", v.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows, want 2 (create, update)", len(changes))
			}
		})
	}
}

// TestUpdateL2VPNRefusesAStaleToken.
func TestUpdateL2VPNRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			v, err := domain.NewL2VPN(NewID(), "contended-overlay", domain.L2VPNEVPN)
			if err != nil {
				t.Fatalf("building overlay: %v", err)
			}
			if err := s.CreateL2VPN(ctx, testPermit, v); err != nil {
				t.Fatalf("creating overlay: %v", err)
			}

			first, err := s.GetL2VPN(ctx, v.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := s.GetL2VPN(ctx, v.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			first.Name = "contended-overlay-first"
			if err := s.UpdateL2VPN(ctx, testPermit, first); err != nil {
				t.Fatalf("the first write must succeed: %v", err)
			}

			second.Name = "contended-overlay-second"
			err = s.UpdateL2VPN(ctx, testPermit, second)
			if err == nil {
				t.Fatal("the second, stale write succeeded")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Errorf("error = %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestUpdateL2VPNRefusesAnInvalidValue.
func TestUpdateL2VPNRefusesAnInvalidValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			v, err := domain.NewL2VPN(NewID(), "overlay-a", domain.L2VPNMPLS)
			if err != nil {
				t.Fatalf("building overlay: %v", err)
			}
			if err := s.CreateL2VPN(ctx, testPermit, v); err != nil {
				t.Fatalf("creating overlay: %v", err)
			}

			v.Kind = "quic"
			err = s.UpdateL2VPN(ctx, testPermit, v)
			if err == nil {
				t.Fatal("an unknown overlay technology was accepted")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()["kind"]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), "kind")
			}
		})
	}
}
