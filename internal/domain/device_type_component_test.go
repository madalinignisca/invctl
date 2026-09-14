// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"testing"
	"time"
)

func TestNewDeviceTypeComponent(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	intPtr := func(v int) *int { return &v }
	strPtr := func(v string) *string { return &v }

	for _, tc := range []struct {
		name         string
		deviceTypeID string
		kind         string
		compName     string
		position     int
		mutate       func(*DeviceTypeComponent)
		wantErr      bool
		wantField    string
		// specMutate shapes the SPEC before construction, where mutate shapes
		// the built value after it. A required field can only be tested absent
		// from here -- once the value exists the constructor has already run.
		specMutate func(*DeviceTypeComponentSpec)
	}{
		{
			name: "a bare interface is valid", deviceTypeID: "dt1",
			kind: ComponentKindInterface, compName: "eth0", position: 0,
		},
		{
			name:         "an interface may carry form_factor, speed and mgmt flag",
			deviceTypeID: "dt1", kind: ComponentKindInterface, compName: "eth0", position: 0,
			mutate: func(c *DeviceTypeComponent) {
				c.FormFactor = strPtr("rj45")
				c.SpeedMbps = intPtr(1000)
				c.IsMgmt = true
			},
		},
		{
			// The rule this case guards: interface.form_factor is NOT NULL with
			// a vocabulary foreign key, so a component without one is accepted
			// by the template and then refused at INSTANTIATION -- every asset
			// of that model failing to create, with an error naming a field on
			// a form the operator is not looking at.
			name:         "an interface with no form factor at all is refused",
			deviceTypeID: "dt1", kind: ComponentKindInterface, compName: "eth0", position: 0,
			specMutate: func(sp *DeviceTypeComponentSpec) { sp.FormFactor = nil },
			wantErr:    true,
			wantField:  "form_factor",
		},
		{
			name: "a bare power_input is valid", deviceTypeID: "dt1",
			kind: ComponentKindPowerInput, compName: "psu0", position: 0,
		},
		{
			name:         "a power_input may carry draw_va",
			deviceTypeID: "dt1", kind: ComponentKindPowerInput, compName: "psu0", position: 0,
			mutate: func(c *DeviceTypeComponent) { c.DrawVA = intPtr(750) },
		},
		{
			name:         "a power_input carrying speed_mbps is refused",
			deviceTypeID: "dt1", kind: ComponentKindPowerInput, compName: "psu0", position: 0,
			mutate:    func(c *DeviceTypeComponent) { c.SpeedMbps = intPtr(1000) },
			wantErr:   true,
			wantField: "speed_mbps",
		},
		{
			name:         "a power_input carrying form_factor is refused",
			deviceTypeID: "dt1", kind: ComponentKindPowerInput, compName: "psu0", position: 0,
			mutate:    func(c *DeviceTypeComponent) { c.FormFactor = strPtr("rj45") },
			wantErr:   true,
			wantField: "form_factor",
		},
		{
			name:         "a power_input flagged is_mgmt is refused",
			deviceTypeID: "dt1", kind: ComponentKindPowerInput, compName: "psu0", position: 0,
			mutate:    func(c *DeviceTypeComponent) { c.IsMgmt = true },
			wantErr:   true,
			wantField: "is_mgmt",
		},
		{
			name:         "an interface carrying draw_va is refused",
			deviceTypeID: "dt1", kind: ComponentKindInterface, compName: "eth0", position: 0,
			mutate:    func(c *DeviceTypeComponent) { c.DrawVA = intPtr(100) },
			wantErr:   true,
			wantField: "draw_va",
		},
		{
			name:         "an unknown kind is refused",
			deviceTypeID: "dt1", kind: "widget", compName: "w0", position: 0,
			wantErr: true, wantField: "kind",
		},
		{
			name:         "an empty name is refused",
			deviceTypeID: "dt1", kind: ComponentKindInterface, compName: "", position: 0,
			wantErr: true, wantField: "name",
		},
		{
			name:         "an empty device_type_id is refused",
			deviceTypeID: "", kind: ComponentKindInterface, compName: "eth0", position: 0,
			wantErr: true, wantField: "device_type_id",
		},
		{
			name:         "a negative position is refused",
			deviceTypeID: "dt1", kind: ComponentKindInterface, compName: "eth0", position: -1,
			wantErr: true, wantField: "position",
		},
		{
			name:         "zero speed_mbps is refused, not treated as absent",
			deviceTypeID: "dt1", kind: ComponentKindInterface, compName: "eth0", position: 0,
			mutate:    func(c *DeviceTypeComponent) { c.SpeedMbps = intPtr(0) },
			wantErr:   true,
			wantField: "speed_mbps",
		},
		{
			name:         "a blank form_factor is refused, not silently accepted",
			deviceTypeID: "dt1", kind: ComponentKindInterface, compName: "eth0", position: 0,
			mutate:    func(c *DeviceTypeComponent) { c.FormFactor = strPtr("   ") },
			wantErr:   true,
			wantField: "form_factor",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := DeviceTypeComponentSpec{
				DeviceTypeID: tc.deviceTypeID, Kind: tc.kind,
				Name: tc.compName, Position: tc.position,
			}
			// An interface component requires a form factor, so the fixtures
			// carry one by default; the cases that test its absence or its
			// blankness clear it through specMutate.
			if tc.kind == ComponentKindInterface {
				spec.FormFactor = strPtr("rj45")
			}
			if tc.specMutate != nil {
				tc.specMutate(&spec)
			}
			c, err := NewDeviceTypeComponent("id1", spec, now)
			if tc.mutate != nil {
				// The constructor already ran; re-run Validate after mutating
				// to exercise the cross-column rule the same way an update
				// path would (Validate is split from the constructor exactly
				// so both go through the same checks -- see the doc comment).
				if err != nil {
					t.Fatalf("unexpected construction error before mutation: %v", err)
				}
				tc.mutate(c)
				err = c.Validate()
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got none")
				}
				ve, ok := AsValidation(err)
				if !ok {
					t.Fatalf("want a *ValidationError, got %T: %v", err, err)
				}
				if tc.wantField != "" {
					if _, ok := ve.Messages()[tc.wantField]; !ok {
						t.Errorf("want a validation error on field %q, got %v", tc.wantField, ve.Messages())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.Lifecycle != LifecycleActive {
				t.Errorf("Lifecycle = %q, want %q", c.Lifecycle, LifecycleActive)
			}
			if c.RowVersion != 1 {
				t.Errorf("RowVersion = %d, want 1", c.RowVersion)
			}
			if c.CreatedAt != FormatTime(now) || c.UpdatedAt != FormatTime(now) {
				t.Errorf("timestamps not set from now: created=%q updated=%q", c.CreatedAt, c.UpdatedAt)
			}
			if c.IsRetired() {
				t.Errorf("a freshly constructed component must not be retired")
			}
		})
	}
}
