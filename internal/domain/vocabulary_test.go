// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestConstructorsAcceptUnknownVocabularyValues is the load-bearing assertion
// of migration 00004's Go half, and it is deliberately a test that would have
// FAILED before the change.
//
// A lookup table exists so that a value can be added as data. A constructor
// that hard-fails against a fixed Go slice defeats that entirely: insert `vrf`
// into asset_kind and NewAsset would still refuse it, so the operator would
// have added a row that nothing can use. Every constructor below must
// therefore accept a well-formed code it has never heard of, and leave
// existence to the foreign key.
func TestConstructorsAcceptUnknownVocabularyValues(t *testing.T) {
	now := time.Now()

	t.Run("asset kind", func(t *testing.T) {
		a, err := NewAsset("id", "vrf", "vrf-red", nil, now)
		if err != nil {
			t.Fatalf("NewAsset rejected an unknown kind: %v", err)
		}
		if a.Kind != "vrf" {
			t.Errorf("kind = %q, want it stored unchanged", a.Kind)
		}
	})

	t.Run("environment role", func(t *testing.T) {
		e, err := NewEnvironment("id", "lab", "Lab", "sandbox", false, 3, now)
		if err != nil {
			t.Fatalf("NewEnvironment rejected an unknown role: %v", err)
		}
		if e.Role != "sandbox" {
			t.Errorf("role = %q, want it stored unchanged", e.Role)
		}
	})

	t.Run("interface form factor", func(t *testing.T) {
		// The reason this vocabulary became a table: optics keep arriving and
		// there is no code to write when they do.
		i, err := NewInterface("id", "asset", "eth0", "osfp800")
		if err != nil {
			t.Fatalf("NewInterface rejected an unknown form factor: %v", err)
		}
		if i.FormFactor != "osfp800" {
			t.Errorf("form_factor = %q, want it stored unchanged", i.FormFactor)
		}
	})

	t.Run("ip address role", func(t *testing.T) {
		addr, err := NewIPAddress("id", "10.20.30.5", nil, "anycast")
		if err != nil {
			t.Fatalf("NewIPAddress rejected an unknown role: %v", err)
		}
		if addr.Role != "anycast" {
			t.Errorf("role = %q, want it stored unchanged", addr.Role)
		}
	})

	t.Run("service kind", func(t *testing.T) {
		s, err := NewService("id", ServiceSpec{
			Code: "svc", Name: "Service", Kind: "ledger",
			EnvironmentID: "env", Availability: AvailStandalone, Tier: 3,
		}, now)
		if err != nil {
			t.Fatalf("NewService rejected an unknown kind: %v", err)
		}
		if s.Kind != "ledger" {
			t.Errorf("kind = %q, want it stored unchanged", s.Kind)
		}
	})
}

// TestBehaviouralEnumsStayClosed is the other half of the same decision, and
// the reason it is not "constructors stopped validating".
//
// These values select a code path. availability reaches
// AvailabilityPolicy.Evaluate and the impact engine's capacity arithmetic;
// lifecycle decides retirement. A value no switch handles must never reach the
// database, because the switch falls through to its default silently and
// produces a wrong answer during an incident with nothing in the logs. For
// these, a new value SHOULD require a release -- the release is where the code
// that understands it gets written.
func TestBehaviouralEnumsStayClosed(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name  string
		build func() error
		field string
	}{
		{
			name: "service availability",
			build: func() error {
				_, err := NewService("id", ServiceSpec{
					Code: "svc", Name: "Service", Kind: SvcAPI,
					EnvironmentID: "env", Availability: "best_effort", Tier: 3,
				}, now)
				return err
			},
			field: "availability",
		},
		{
			name: "asset lifecycle",
			build: func() error {
				a := &Asset{ID: "id", Kind: KindServer, Name: "srv", Lifecycle: "mothballed"}
				return a.Validate()
			},
			field: "lifecycle",
		},
		{
			name: "dependency nature",
			build: func() error {
				_, err := NewDependency("id", DependencySpec{
					ConsumerServiceID:  "svc",
					ProviderEndpointID: ptrStr("ep"),
					Nature:             "eventual",
					FailureMode:        "orders fail",
				}, now)
				return err
			},
			field: "nature",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.build()
			if err == nil {
				t.Fatalf("%s accepted a value no code path handles", tc.name)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("%s failed with %T, want *ValidationError", tc.name, err)
			}
			if _, named := ve.Messages()[tc.field]; !named {
				t.Errorf("%s: the error does not name %q: %v", tc.name, tc.field, ve.Messages())
			}
		})
	}
}

// TestVocabularyShapeRules covers what is left in the constructor once
// membership moved to the foreign key.
//
// A vocabulary code is a stable machine identifier: it is a primary key, it
// travels in query strings, and both engines compare it byte for byte. Shape is
// what a constructor can still usefully assert without knowing the list --
// whether `vrf` is a real asset kind is a question only the table can answer,
// but " vrf\n" is wrong whatever the table says, because it looks identical to
// a code it is not equal to.
func TestVocabularyShapeRules(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
		want    string // the value as stored, when accepted
	}{
		{name: "a plain code", value: "server", want: "server"},
		{name: "underscores", value: "patch_panel", want: "patch_panel"},
		{name: "the plus in sfp+", value: "sfp+", want: "sfp+"},
		{name: "digits", value: "qsfp28", want: "qsfp28"},
		{name: "a code nothing has heard of", value: "osfp800", want: "osfp800"},
		{name: "surrounding whitespace is trimmed", value: "  vm\t", want: "vm"},

		{name: "empty", value: "", wantErr: true},
		{name: "whitespace only", value: "   ", wantErr: true},
		{name: "an interior space", value: "patch panel", wantErr: true},
		{name: "an embedded newline", value: "vm\nk8s_node", wantErr: true},
		{name: "a control character", value: "vm\x00", wantErr: true},
		{name: "longer than the cap", value: strings.Repeat("a", MaxVocabularyCodeLen+1), wantErr: true},
		{name: "exactly the cap", value: strings.Repeat("a", MaxVocabularyCodeLen),
			want: strings.Repeat("a", MaxVocabularyCodeLen)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ve := &ValidationError{}
			got := checkVocabulary(ve, "kind", tc.value)
			err := ve.OrNil()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("checkVocabulary(%q) accepted it", tc.value)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("error does not match ErrInvalid: %v", err)
				}
				if _, named := ve.Messages()["kind"]; !named {
					t.Errorf("the error does not name the field: %v", ve.Messages())
				}
				return
			}
			if err != nil {
				t.Fatalf("checkVocabulary(%q): %v", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("checkVocabulary(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}
