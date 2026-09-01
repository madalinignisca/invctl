// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain_test

import (
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

func TestNewSavedViewRejectsAnEmptyName(t *testing.T) {
	_, err := domain.NewSavedView("id-1", "user-1", domain.SavedViewAsset, "", `{"kind":"server"}`, time.Now())
	if err == nil {
		t.Fatal("NewSavedView accepted an empty name")
	}
}

func TestNewSavedViewRejectsAnUnknownEntity(t *testing.T) {
	_, err := domain.NewSavedView("id-1", "user-1", "widget", "My view", `{}`, time.Now())
	if err == nil {
		t.Fatal("NewSavedView accepted an entity outside the CHECK constraint")
	}
}

func TestNewSavedViewRejectsParamsThatAreNotAJSONObject(t *testing.T) {
	// params is unmarshalled in Go and never queried in SQL, so the
	// constructor is the only place its shape is enforced. A bare array or
	// scalar would round-trip through TEXT and fail later, further from the
	// cause.
	for _, bad := range []string{"", "[]", `"a string"`, "not json", "null"} {
		if _, err := domain.NewSavedView("id-1", "user-1", domain.SavedViewAsset, "My view", bad, time.Now()); err == nil {
			t.Errorf("NewSavedView accepted params %q", bad)
		}
	}
}

func TestSavedViewValidateRejectsParamsThatAreNotAJSONObject(t *testing.T) {
	// Validate is the path Task 3's update flow calls on a struct that was
	// mutated by hand, so it must refuse the same shapes NewSavedView does --
	// in particular the literal `null`, which unmarshals into a nil map with
	// err == nil and is therefore the one input the constructor's own bug
	// let through undetected (see the failing test this guards against).
	for _, bad := range []string{"", "[]", `"a string"`, "not json", "null"} {
		v, err := domain.NewSavedView("id-1", "user-1", domain.SavedViewAsset, "My view", `{}`, time.Now())
		if err != nil {
			t.Fatalf("NewSavedView: %v", err)
		}
		v.Params = bad
		if err := v.Validate(); err == nil {
			t.Errorf("Validate accepted params %q", bad)
		}
	}
}

func TestNewSavedViewAcceptsAValidView(t *testing.T) {
	v, err := domain.NewSavedView("id-1", "user-1", domain.SavedViewAsset, "Production servers", `{"kind":"server"}`, time.Now())
	if err != nil {
		t.Fatalf("NewSavedView: %v", err)
	}
	if v.Lifecycle != domain.LifecycleActive {
		t.Errorf("Lifecycle = %q, want %q", v.Lifecycle, domain.LifecycleActive)
	}
	if v.RowVersion != 1 {
		t.Errorf("RowVersion = %d, want 1", v.RowVersion)
	}
}
