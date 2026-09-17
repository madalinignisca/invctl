// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

// TestVLANGroupValidateIsReachableWithoutTheConstructor is Identity.Validate's
// own test, restated for VLANGroup: a value that already exists and is then
// corrupted the way UpdateVLANGroup's caller would hand it back, with
// NewVLANGroup never in the loop.
func TestVLANGroupValidateIsReachableWithoutTheConstructor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(g *VLANGroup)
		field string
	}{
		{"a blank name", func(g *VLANGroup) { g.Name = "" }, "name"},
		{"whitespace for a name", func(g *VLANGroup) { g.Name = "   " }, "name"},
		{"an unknown lifecycle", func(g *VLANGroup) { g.Lifecycle = "gone" }, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &VLANGroup{ID: "grp-1", Name: "rack-12", Lifecycle: LifecycleActive, RowVersion: 2}
			tc.spoil(g)

			err := g.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value; UpdateVLANGroup would write it", tc.name)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()[tc.field]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), tc.field)
			}
		})
	}
}
