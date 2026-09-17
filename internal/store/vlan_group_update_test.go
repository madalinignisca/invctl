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

// TestUpdateVLANGroupCorrectsAndAudits.
func TestUpdateVLANGroupCorrectsAndAudits(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			g, err := domain.NewVLANGroup(NewID(), "rack-12-typo", nil)
			if err != nil {
				t.Fatalf("building vlan group: %v", err)
			}
			if err := s.CreateVLANGroup(ctx, testPermit, g); err != nil {
				t.Fatalf("creating vlan group: %v", err)
			}

			got, err := s.GetVLANGroup(ctx, g.ID)
			if err != nil {
				t.Fatalf("getting vlan group: %v", err)
			}
			got.Name = "rack-12"
			if err := s.UpdateVLANGroup(ctx, testPermit, got); err != nil {
				t.Fatalf("updating vlan group: %v", err)
			}

			after, err := s.GetVLANGroup(ctx, g.ID)
			if err != nil {
				t.Fatalf("re-reading vlan group: %v", err)
			}
			if after.Name != "rack-12" {
				t.Errorf("name = %q, want %q", after.Name, "rack-12")
			}

			changes, err := s.ListChangesForEntity(ctx, "vlan_group", g.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows, want 2 (create, update)", len(changes))
			}
		})
	}
}

// TestUpdateVLANGroupRefusesAStaleToken.
func TestUpdateVLANGroupRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			g, err := domain.NewVLANGroup(NewID(), "contended-group", nil)
			if err != nil {
				t.Fatalf("building vlan group: %v", err)
			}
			if err := s.CreateVLANGroup(ctx, testPermit, g); err != nil {
				t.Fatalf("creating vlan group: %v", err)
			}

			first, err := s.GetVLANGroup(ctx, g.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := s.GetVLANGroup(ctx, g.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			first.Name = "contended-group-first"
			if err := s.UpdateVLANGroup(ctx, testPermit, first); err != nil {
				t.Fatalf("the first write must succeed: %v", err)
			}

			second.Name = "contended-group-second"
			err = s.UpdateVLANGroup(ctx, testPermit, second)
			if err == nil {
				t.Fatal("the second, stale write succeeded")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Errorf("error = %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestUpdateVLANGroupRefusesAnInvalidValue.
func TestUpdateVLANGroupRefusesAnInvalidValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			g, err := domain.NewVLANGroup(NewID(), "group-a", nil)
			if err != nil {
				t.Fatalf("building vlan group: %v", err)
			}
			if err := s.CreateVLANGroup(ctx, testPermit, g); err != nil {
				t.Fatalf("creating vlan group: %v", err)
			}

			g.Name = "   "
			err = s.UpdateVLANGroup(ctx, testPermit, g)
			if err == nil {
				t.Fatal("a blank name was accepted")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()["name"]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), "name")
			}
		})
	}
}
