// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

// TestL2VPNValidateIsReachableWithoutTheConstructor is Identity.Validate's own
// test, restated for L2VPN: a value that already exists and is then corrupted
// the way UpdateL2VPN's caller would hand it back, with NewL2VPN never in the
// loop.
func TestL2VPNValidateIsReachableWithoutTheConstructor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(v *L2VPN)
		field string
	}{
		{"a blank name", func(v *L2VPN) { v.Name = "" }, "name"},
		{"whitespace for a name", func(v *L2VPN) { v.Name = "   " }, "name"},
		{"an unknown overlay technology", func(v *L2VPN) { v.Kind = "quic" }, "kind"},
		{"an identifier past the 24-bit VNI ceiling", func(v *L2VPN) {
			n := int64(MaxL2VPNIdentifier + 1)
			v.Identifier = &n
		}, "identifier"},
		{"a negative identifier", func(v *L2VPN) { n := int64(-1); v.Identifier = &n }, "identifier"},
		{"an unknown lifecycle", func(v *L2VPN) { v.Lifecycle = "gone" }, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &L2VPN{
				ID: "l2vpn-1", Name: "prod-overlay", Kind: L2VPNVXLAN,
				Lifecycle: LifecycleActive, RowVersion: 5,
			}
			tc.spoil(v)

			err := v.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value; UpdateL2VPN would write it", tc.name)
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
