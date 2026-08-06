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

var teamNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// A role says who IN A TEAM is answerable, so it means nothing on its own. The
// alternative reading -- accept it and leave the team blank -- produces a row
// that claims a capacity is held and refuses to say by whom, which reads as
// recorded when it is not.
func TestARoleWithoutATeamIsRejected(t *testing.T) {
	teamID, role := "t1", "operator"

	cases := []struct {
		name    string
		team    *string
		role    *string
		wantErr bool
	}{
		{"neither is fine -- both are optional", nil, nil, false},
		{"a team alone is fine and common", &teamID, nil, false},
		{"both together", &teamID, &role, false},
		{"a role alone is not", nil, &role, true},
		// A cleared select posts "", which is the absence of a choice rather
		// than a choice of nothing.
		{"an empty team with a role is the same thing", strptr(""), &role, true},
		{"an empty role is simply absent", &teamID, strptr(""), false},
		{"both empty", strptr(""), strptr("  "), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ve := &ValidationError{}
			gotTeam, gotRole := checkResponsibility(ve, c.team, c.role)
			if err := ve.OrNil(); (err != nil) != c.wantErr {
				t.Fatalf("error = %v, want error: %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			// Blank fields become absent rather than empty strings, or every
			// query filtering on "no team" has to know about two spellings.
			if gotTeam != nil && *gotTeam == "" {
				t.Error("an empty team survived as an empty string")
			}
			if gotRole != nil && *gotRole == "" {
				t.Error("an empty role survived as an empty string")
			}
		})
	}
}

// The rule has to hold through the entity validators too, since that is the only
// path a handler uses.
func TestEntitiesRejectARoleWithoutATeam(t *testing.T) {
	role := "operator"

	asset, err := NewAsset("a1", KindServer, "hv-99", nil, teamNow)
	if err != nil {
		t.Fatalf("building the asset: %v", err)
	}
	asset.ManagerRole = &role
	if err := asset.Validate(); err == nil {
		t.Error("an asset accepted a manager role with no team")
	}

	svc, err := NewService("s1", ServiceSpec{
		Code: "x", Name: "X", Kind: "api", EnvironmentID: "e1",
		Availability: AvailStandalone, Tier: 3,
	}, teamNow)
	if err != nil {
		t.Fatalf("building the service: %v", err)
	}
	svc.ManagerRole = &role
	if err := svc.Validate(); err == nil {
		t.Error("a service accepted a manager role with no team")
	}
}

func TestNewTeam(t *testing.T) {
	contact := "platform@example.com"
	got, err := NewTeam("t1", TeamSpec{
		Code: "  Platform  ", Name: "Platform & Core", ContactRef: &contact,
	}, teamNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if got.Code != "platform" {
		t.Errorf("code = %q, want it lowercased and trimmed", got.Code)
	}
	if got.Lifecycle != LifecycleActive {
		t.Errorf("lifecycle = %q, want it to default to active", got.Lifecycle)
	}

	for _, c := range []struct {
		name string
		spec TeamSpec
	}{
		{"no code", TeamSpec{Name: "X"}},
		{"no name", TeamSpec{Code: "x"}},
		{"a lifecycle nothing knows", TeamSpec{Code: "x", Name: "X", Lifecycle: "disbanded"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewTeam("t1", c.spec, teamNow); err == nil {
				t.Error("accepted")
			}
		})
	}
}
