// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

// TestRIRValidateIsReachableWithoutTheConstructor, TestAggregateValidate... and
// TestASNValidate... are the same shape Identity.Validate's own test proves
// (identity_test.go): a value that already exists, corrupted the way an update
// path would hand it back, with the constructor never in the loop. RIR,
// Aggregate and ASN already had a Validate() split from their constructors
// before write-surface-gaps -- these tests exist so that stays true rather
// than assumed, and so a future edit that moves a check back inside NewX
// (Environment.Validate's own defect) fails here.

func TestRIRValidateIsReachableWithoutTheConstructor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(r *RIR)
		field string
	}{
		{"a blank name", func(r *RIR) { r.Name = "" }, "name"},
		{"whitespace for a name", func(r *RIR) { r.Name = "   " }, "name"},
		{"an unknown lifecycle", func(r *RIR) { r.Lifecycle = "gone" }, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &RIR{ID: "rir-1", Name: "ARIN", Lifecycle: LifecycleActive, RowVersion: 2}
			tc.spoil(r)

			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value; UpdateRIR would write it", tc.name)
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

func TestAggregateValidateIsReachableWithoutTheConstructor(t *testing.T) {
	base := func() *Aggregate {
		a, err := NewAggregate("agg-1", "10.0.0.0/16")
		if err != nil {
			t.Fatalf("building a valid aggregate: %v", err)
		}
		a.RowVersion = 4
		return a
	}
	for _, tc := range []struct {
		name  string
		spoil func(a *Aggregate)
		field string
	}{
		{"an emptied range", func(a *Aggregate) { a.AddrStart, a.AddrEnd = nil, nil }, "cidr_text"},
		{"an unparseable allocation date", func(a *Aggregate) { d := "not-a-date"; a.AllocatedOn = &d }, "allocated_on"},
		{"an unknown lifecycle", func(a *Aggregate) { a.Lifecycle = "gone" }, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base()
			tc.spoil(a)

			err := a.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value; UpdateAggregate would write it", tc.name)
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

func TestASNValidateIsReachableWithoutTheConstructor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(a *ASN)
		field string
	}{
		{"zero, which is reserved", func(a *ASN) { a.Number = 0 }, "number"},
		{"past the 32-bit ceiling", func(a *ASN) { a.Number = 4294967295 }, "number"},
		{"an unknown lifecycle", func(a *ASN) { a.Lifecycle = "gone" }, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &ASN{ID: "asn-1", Number: 65000, Lifecycle: LifecycleActive, RowVersion: 3}
			tc.spoil(a)

			err := a.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value; UpdateASN would write it", tc.name)
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
