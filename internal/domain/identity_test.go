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

func TestRotationStatus(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	days := func(n int) *int { return &n }
	on := func(s string) *string { return &s }

	for _, tc := range []struct {
		name string
		days *int
		last *string
		want RotationState
	}{
		// The two the old boolean collapsed into one answer, and the whole
		// reason this is a state. They are OPPOSITE FACTS.
		{"no policy is unmanaged", nil, nil, RotationUnmanaged},
		{"no policy and a date is still unmanaged", nil, on("2026-06-01"), RotationUnmanaged},
		{"policy with no record is not fine", days(90), nil, RotationNeverRecorded},

		{"inside the window", days(90), on("2026-08-20"), RotationWithinWindow},
		{"exactly on the due day is still inside", days(90), on("2026-06-17"), RotationWithinWindow},
		{"a day past the due day is overdue", days(90), on("2026-06-16"), RotationOverdue},
		{"long overdue", days(90), on("2026-01-01"), RotationOverdue},

		// A state that cannot be READ must never render as a state that is
		// FINE. RotationOverdue returned false here, which is how an
		// unparseable stored value read as healthy.
		{"unparseable is unreadable, never healthy", days(90), on("not-a-date"), RotationUnreadable},
		{"a real-looking impossible date is unreadable", days(90), on("2026-02-31"), RotationUnreadable},
		{"a timestamp where a date belongs is unreadable", days(90), on("2026-06-01T00:00:00Z"), RotationUnreadable},

		// A future date cannot arrive through the handler (422) but can
		// arrive through a hand-edited database, and it must not read as a
		// policy being met by something that has not happened. Folded into
		// RotationUnreadable, not a new state: the claim is identical -- the
		// stored value cannot be relied on -- and RotationFindings already
		// folds unreadable into its Gap bucket, the right severity for this.
		{"a future date is unreadable, not the healthiest state there is", days(90), on("2026-12-01"), RotationUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := &Identity{RotationDays: tc.days, LastRotated: tc.last}
			if got := i.RotationStatus(now); got != tc.want {
				t.Errorf("RotationStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRotationDueOn(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	days := func(n int) *int { return &n }
	on := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		days *int
		last *string
		want string // "" means nil
	}{
		{"both set", days(90), on("2026-06-01"), "2026-08-30"},
		{"no policy", nil, on("2026-06-01"), ""},
		{"no record", days(90), nil, ""},
		// A due date computed from a value that will not parse is a lie with a
		// date on it, which is worse than no answer.
		{"unreadable", days(90), on("not-a-date"), ""},
		// Same reasoning, for a future date: RotationStatus answers
		// RotationUnreadable for this, and a due date here would be a second
		// answer that disagrees with it.
		{"a future date has no due date either", days(90), on("2026-12-01"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (&Identity{RotationDays: tc.days, LastRotated: tc.last}).RotationDueOn(now)
			switch {
			case tc.want == "" && got != nil:
				t.Errorf("RotationDueOn = %q, want nil", *got)
			case tc.want != "" && got == nil:
				t.Errorf("RotationDueOn = nil, want %q", tc.want)
			case tc.want != "" && *got != tc.want:
				t.Errorf("RotationDueOn = %q, want %q", *got, tc.want)
			}
		})
	}
}

func TestNewIdentityValidates(t *testing.T) {
	ok := IdentitySpec{Kind: IdentityServiceAccount, Name: "svc-orders"}
	for _, tc := range []struct {
		name    string
		spec    IdentitySpec
		wantErr string // the field the error must name; "" means no error
	}{
		{"a valid service account", ok, ""},
		{"a blank name is refused", IdentitySpec{Kind: IdentityServiceAccount}, "name"},
		{"whitespace is not a name", IdentitySpec{Kind: IdentityServiceAccount, Name: "   "}, "name"},
		{"an unknown kind is refused", IdentitySpec{Kind: "robot", Name: "svc-orders"}, "kind"},
		{"a zero rotation policy is refused", func() IdentitySpec {
			s := ok
			n := 0
			s.RotationDays = &n
			return s
		}(), "rotation_days"},
		{"a negative rotation policy is refused", func() IdentitySpec {
			s := ok
			n := -1
			s.RotationDays = &n
			return s
		}(), "rotation_days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewIdentity("id-1", tc.spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("NewIdentity: %v", err)
				}
				if got.Lifecycle != LifecycleActive || got.RowVersion != 1 {
					t.Errorf("a new identity is %s/v%d, want active/v1", got.Lifecycle, got.RowVersion)
				}
				if got.LastRotated != nil {
					t.Error("NewIdentity set last_rotated; only RecordIdentityRotation writes it")
				}
				return
			}
			// AsValidation, not errors.As by hand: it is the accessor the rest
			// of this package and every handler already use
			// (internal/domain/errors.go:114), and Messages() is the field ->
			// message map. ValidationError.Fields is a SLICE of FieldError, not
			// a map, so indexing it is a compile error rather than a lookup.
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("NewIdentity(%+v) = %v (%T), want a *ValidationError -- the "+
					"handler maps that to 422 with the form re-rendered, and anything "+
					"else to a 500", tc.spec, err, err)
			}
			if _, named := ve.Messages()[tc.wantErr]; !named {
				t.Errorf("the refusal names %v, not %q. A message on the wrong field "+
					"lands nowhere near the input the operator has to fix.",
					ve.Messages(), tc.wantErr)
			}
		})
	}
}

// TestValidateIsReachableWithoutTheConstructor is Environment.Validate's own
// lesson, restated for the entity it is being copied onto: "the checks lived
// inside NewEnvironment, so UpdateEnvironment wrote whatever it was handed and
// the table CHECK was the only thing standing between a form and a blank name."
func TestValidateIsReachableWithoutTheConstructor(t *testing.T) {
	// A value that already exists and is then corrupted by an update path,
	// which is exactly what UpdateIdentity is handed. The constructor is not
	// involved, so if the checks lived inside it this would pass silently.
	for _, tc := range []struct {
		name  string
		spoil func(i *Identity)
		field string
	}{
		{"a blank name", func(i *Identity) { i.Name = "" }, "name"},
		{"whitespace for a name", func(i *Identity) { i.Name = "  " }, "name"},
		{"an unknown kind", func(i *Identity) { i.Kind = "robot" }, "kind"},
		{"a zero rotation policy", func(i *Identity) { n := 0; i.RotationDays = &n }, "rotation_days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := &Identity{
				ID: "id-1", Kind: IdentityServiceAccount, Name: "svc",
				Lifecycle: LifecycleActive, RowVersion: 3,
			}
			tc.spoil(i)

			err := i.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value. UpdateIdentity would "+
					"write it and the DB CHECK would be the only thing standing between "+
					"a form and a bad row -- the defect Environment.Validate's own doc "+
					"comment records.", tc.name)
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
