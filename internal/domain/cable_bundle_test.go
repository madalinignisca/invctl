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

func TestNewCableBundle(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	strPtr := func(v string) *string { return &v }

	for _, tc := range []struct {
		name      string
		spec      CableBundleSpec
		wantErr   bool
		wantField string
	}{
		{
			name: "a bare bundle is valid",
			spec: CableBundleSpec{Code: "duct-a", Name: "Duct A"},
		},
		{
			name: "a bundle may carry a description",
			spec: CableBundleSpec{Code: "duct-a", Name: "Duct A", Description: strPtr("Overhead tray, row 3")},
		},
		{
			name:      "an empty code is refused",
			spec:      CableBundleSpec{Code: "", Name: "Duct A"},
			wantErr:   true,
			wantField: "code",
		},
		{
			name:      "a whitespace-only code is refused, not silently trimmed to empty and accepted",
			spec:      CableBundleSpec{Code: "   ", Name: "Duct A"},
			wantErr:   true,
			wantField: "code",
		},
		{
			name:      "an empty name is refused",
			spec:      CableBundleSpec{Code: "duct-a", Name: ""},
			wantErr:   true,
			wantField: "name",
		},
		{
			name: "the code is normalised to lowercase, the same rule as Environment",
			spec: CableBundleSpec{Code: "DUCT-A", Name: "Duct A"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := NewCableBundle("id1", tc.spec, now)
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
			if b.Lifecycle != LifecycleActive {
				t.Errorf("Lifecycle = %q, want %q", b.Lifecycle, LifecycleActive)
			}
			if b.RowVersion != 1 {
				t.Errorf("RowVersion = %d, want 1", b.RowVersion)
			}
			if b.CreatedAt != FormatTime(now) || b.UpdatedAt != FormatTime(now) {
				t.Errorf("timestamps not set from now: created=%q updated=%q", b.CreatedAt, b.UpdatedAt)
			}
			if b.IsRetired() {
				t.Errorf("a freshly constructed bundle must not be retired")
			}
			if b.Code != "duct-a" {
				t.Errorf("Code = %q, want lowercased %q", b.Code, "duct-a")
			}
		})
	}
}

// TestCableBundleValidateRefusesAnUnknownLifecycle exercises Validate
// directly, the way an update path (Task 2) would run it against a value
// that did not come through the constructor -- the same reason
// DeviceTypeComponent's test re-runs Validate after mutating rather than
// only testing the constructor.
func TestCableBundleValidateRefusesAnUnknownLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	b, err := NewCableBundle("id1", CableBundleSpec{Code: "duct-a", Name: "Duct A"}, now)
	if err != nil {
		t.Fatalf("unexpected error constructing fixture: %v", err)
	}
	b.Lifecycle = "withdrawn"
	err = b.Validate()
	if err == nil {
		t.Fatalf("want an error for an unrecognised lifecycle, got none")
	}
	ve, ok := AsValidation(err)
	if !ok {
		t.Fatalf("want a *ValidationError, got %T: %v", err, err)
	}
	if _, ok := ve.Messages()["lifecycle"]; !ok {
		t.Errorf("want a validation error on field %q, got %v", "lifecycle", ve.Messages())
	}
}
