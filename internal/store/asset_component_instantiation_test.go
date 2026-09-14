// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestCreateAssetInstantiatesDeviceTypeComponents is Task 4's core case: a
// device type with two interface components and one power_input component,
// an asset created from it, and exactly the interfaces materialise --
// each with the right attributes and its own change_log row.
//
// power_input IS DELIBERATELY NOT INSTANTIATED -- see
// instantiateComponents's doc comment (internal/store/device_type_components.go).
// power_input.feed_id is NOT NULL REFERENCES power_feed(id), and a device
// type's template names no feed: a feed is a fact about how THIS box was
// actually wired, which nothing at asset-creation time knows. So this test
// asserts the power_input template entry produces NO power_input row and NO
// change_log entry for one, rather than asserting a fabricated feed.
func TestCreateAssetInstantiatesDeviceTypeComponents(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)

			rj45 := "rj45"
			sfp28 := "sfp28"
			speed := 1000
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0",
				FormFactor: &rj45, SpeedMbps: &speed, IsMgmt: true,
			}); err != nil {
				t.Fatalf("declaring eth0: %v", err)
			}
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "Ethernet1/[1-1]",
				FormFactor: &sfp28,
			}); err != nil {
				t.Fatalf("declaring Ethernet1/1: %v", err)
			}
			dva := 750
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindPowerInput, NameSpec: "psu1", DrawVA: &dva,
			}); err != nil {
				t.Fatalf("declaring psu1: %v", err)
			}

			assetID := assetOfType(t, s, ctx, "instantiated-01", dtID, nil)

			ifaces, err := s.ListInterfaces(ctx, assetID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(ifaces) != 2 {
				t.Fatalf("got %d interfaces, want exactly 2 (the power_input must not have "+
					"produced a third component of any kind): %+v", len(ifaces), ifaces)
			}

			byName := map[string]InterfaceRow{}
			for _, i := range ifaces {
				byName[i.Name] = i
			}
			eth0, ok := byName["eth0"]
			if !ok {
				t.Fatalf("eth0 was not instantiated: %+v", ifaces)
			}
			if eth0.FormFactor != rj45 {
				t.Errorf("eth0 form_factor = %q, want %q", eth0.FormFactor, rj45)
			}
			if eth0.SpeedMbps == nil || *eth0.SpeedMbps != speed {
				t.Errorf("eth0 speed_mbps = %v, want %d", eth0.SpeedMbps, speed)
			}
			if !eth0.IsMgmt {
				t.Error("eth0 is_mgmt = false, want true (carried from the template)")
			}
			if eth0.Lifecycle != domain.LifecycleActive {
				t.Errorf("eth0 lifecycle = %s, want active", eth0.Lifecycle)
			}

			eth1, ok := byName["Ethernet1/1"]
			if !ok {
				t.Fatalf("Ethernet1/1 was not instantiated: %+v", ifaces)
			}
			if eth1.FormFactor != sfp28 {
				t.Errorf("Ethernet1/1 form_factor = %q, want %q", eth1.FormFactor, sfp28)
			}
			if eth1.SpeedMbps != nil {
				t.Errorf("Ethernet1/1 speed_mbps = %v, want nil -- the template never set one", eth1.SpeedMbps)
			}
			if eth1.IsMgmt {
				t.Error("Ethernet1/1 is_mgmt = true, want false")
			}

			// One change_log row per instantiated component -- not one for the
			// whole batch, unlike CreateDeviceTypeComponents on the template
			// itself. Each is a real, independently auditable row.
			for _, name := range []string{"eth0", "Ethernet1/1"} {
				iface := byName[name]
				changes, err := s.ListChangesForEntity(ctx, "interface", iface.ID, 10)
				if err != nil {
					t.Fatalf("listing changes for %s: %v", name, err)
				}
				if len(changes) != 1 {
					t.Fatalf("interface %s has %d change_log rows, want exactly 1", name, len(changes))
				}
				if changes[0].Action != domain.ActionCreate {
					t.Errorf("interface %s's change_log action = %s, want create", name, changes[0].Action)
				}
			}

			// The power_input template entry produced no row of any kind, and
			// no change_log entry -- a real device_type_component change_log
			// entry exists (its own declaration, above), but nothing claiming a
			// power_input was created for this asset.
			changes, err := s.ListChangesForEntity(ctx, "power_input", assetID, 10)
			if err != nil {
				t.Fatalf("listing power_input changes: %v", err)
			}
			if len(changes) != 0 {
				t.Errorf("got %d change_log rows naming power_input/%s, want 0 -- "+
					"no power_input was supposed to be created", len(changes), assetID)
			}
		})
	}
}

// TestCreateAssetWithNoDeviceTypeIsUnaffected is the regression case the
// brief names explicitly: an asset with no device_type_id at all must be
// created exactly as before Task 4 -- no query against device_type_component,
// no extra change_log rows, no interfaces conjured from nothing.
func TestCreateAssetWithNoDeviceTypeIsUnaffected(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			a, err := domain.NewAsset(NewID(), domain.KindServer, "bare-metal-01", nil, s.Now())
			if err != nil {
				t.Fatalf("building asset: %v", err)
			}
			if err := s.CreateAsset(ctx, testPermit, a, nil); err != nil {
				t.Fatalf("creating asset with no device type: %v", err)
			}

			ifaces, err := s.ListInterfaces(ctx, a.ID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(ifaces) != 0 {
				t.Errorf("a device-type-less asset gained %d interfaces from nowhere: %+v", len(ifaces), ifaces)
			}
		})
	}
}

// TestCreateAssetOfTypeWithNoTemplateIsUnaffected: the other half of the same
// regression -- a device type that exists but declares no template at all
// must not change asset creation either. Every asset created against a
// catalogued model before Task 4 shipped looks like this.
func TestCreateAssetOfTypeWithNoTemplateIsUnaffected(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)

			assetID := assetOfType(t, s, ctx, "templateless-01", dtID, nil)

			ifaces, err := s.ListInterfaces(ctx, assetID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(ifaces) != 0 {
				t.Errorf("an asset of an untemplated device type gained %d interfaces: %+v", len(ifaces), ifaces)
			}
		})
	}
}
