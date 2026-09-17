// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

func TestLinkValidate(t *testing.T) {
	strPtr := func(v string) *string { return &v }
	intPtr := func(v int) *int { return &v }

	for _, tc := range []struct {
		name      string
		link      Link
		wantErr   bool
		wantField string
	}{
		{
			name: "an ordinary two-ended cable carrying no breakout fields is valid",
			link: Link{ID: "l1", AInterfaceID: "a", BInterfaceID: "b"},
		},
		{
			name: "a breakout strand with both fields set is valid",
			link: Link{ID: "l1", AInterfaceID: "a", BInterfaceID: "b",
				BreakoutID: strPtr("bk1"), BreakoutPosition: intPtr(1)},
		},
		{
			name:      "a missing a-end is refused",
			link:      Link{ID: "l1", BInterfaceID: "b"},
			wantErr:   true,
			wantField: "a_interface_id",
		},
		{
			name:      "a missing b-end is refused",
			link:      Link{ID: "l1", AInterfaceID: "a"},
			wantErr:   true,
			wantField: "b_interface_id",
		},
		{
			name:      "an interface cannot be linked to itself",
			link:      Link{ID: "l1", AInterfaceID: "a", BInterfaceID: "a"},
			wantErr:   true,
			wantField: "b_interface_id",
		},
		{
			name: "a breakout id with no position is a half-written breakout",
			link: Link{ID: "l1", AInterfaceID: "a", BInterfaceID: "b",
				BreakoutID: strPtr("bk1")},
			wantErr:   true,
			wantField: "breakout_position",
		},
		{
			name: "a breakout position with no id is a half-written breakout",
			link: Link{ID: "l1", AInterfaceID: "a", BInterfaceID: "b",
				BreakoutPosition: intPtr(1)},
			wantErr:   true,
			wantField: "breakout_id",
		},
		{
			name: "a zero breakout position is refused",
			link: Link{ID: "l1", AInterfaceID: "a", BInterfaceID: "b",
				BreakoutID: strPtr("bk1"), BreakoutPosition: intPtr(0)},
			wantErr:   true,
			wantField: "breakout_position",
		},
		{
			name: "a negative breakout position is refused",
			link: Link{ID: "l1", AInterfaceID: "a", BInterfaceID: "b",
				BreakoutID: strPtr("bk1"), BreakoutPosition: intPtr(-1)},
			wantErr:   true,
			wantField: "breakout_position",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.link.Validate()
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
		})
	}
}

func TestNewLinkStillValidates(t *testing.T) {
	// NewLink used to duplicate Validate's rules inline; this pins that the
	// constructor and Validate agree by construction, not by two copies of
	// the same checks staying in sync by hand.
	if _, err := NewLink("id1", "a", "a"); err == nil {
		t.Fatalf("want an error linking an interface to itself, got none")
	}
	if _, err := NewLink("id1", "", "b"); err == nil {
		t.Fatalf("want an error on a missing a-end, got none")
	}
	l, err := NewLink("id1", "a", "b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l.BreakoutID != nil || l.BreakoutPosition != nil {
		t.Errorf("a freshly constructed link must carry no breakout fields")
	}
	if l.Lifecycle != LifecycleActive {
		t.Errorf("Lifecycle = %q, want %q", l.Lifecycle, LifecycleActive)
	}
}
