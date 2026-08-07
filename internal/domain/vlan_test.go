// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain_test

import (
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

func TestAVLANIDIsBoundedByTheStandard(t *testing.T) {
	cases := []struct {
		vid int
		ok  bool
		why string
	}{
		{0, false, "0 is reserved by 802.1Q for priority-tagged frames"},
		{1, true, "the default VLAN is a real VLAN people really use"},
		{30, true, "an ordinary tag"},
		{4094, true, "the last usable tag"},
		{4095, false, "4095 is reserved by 802.1Q"},
		{-1, false, "negative is not a tag"},
		{9000, false, "past the 12-bit field"},
	}
	for _, tc := range cases {
		_, err := domain.NewVLAN("v", tc.vid, "test", nil)
		if tc.ok && err != nil {
			t.Errorf("VID %d was refused (%s): %v", tc.vid, tc.why, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("VID %d was accepted, but %s", tc.vid, tc.why)
		}
	}
}

func TestAVLANNeedsAName(t *testing.T) {
	if _, err := domain.NewVLAN("v", 30, "   ", nil); err == nil {
		t.Fatal("a nameless VLAN was accepted; the number alone does not say what it carries")
	} else if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid so the handler returns 422", err)
	}
}

// TestAPortHasAtMostOneUntaggedVLAN. A frame arriving without a tag must have
// one unambiguous home; two untagged VLANs is a configuration no switch
// accepts, so the inventory must not be able to describe it.
func TestAPortHasAtMostOneUntaggedVLAN(t *testing.T) {
	tagged := func(id string) domain.InterfaceVLAN {
		return domain.InterfaceVLAN{InterfaceID: "i", VLANID: id, Mode: domain.VLANModeTagged}
	}
	untagged := func(id string) domain.InterfaceVLAN {
		return domain.InterfaceVLAN{InterfaceID: "i", VLANID: id, Mode: domain.VLANModeUntagged}
	}

	tests := []struct {
		name    string
		members []domain.InterfaceVLAN
		wantErr bool
	}{
		{"a bare trunk", []domain.InterfaceVLAN{tagged("a"), tagged("b"), tagged("c")}, false},
		{"an access port", []domain.InterfaceVLAN{untagged("a")}, false},
		{"a trunk with a native VLAN", []domain.InterfaceVLAN{untagged("a"), tagged("b")}, false},
		{"two untagged", []domain.InterfaceVLAN{untagged("a"), untagged("b")}, true},
		{"the same VLAN twice", []domain.InterfaceVLAN{tagged("a"), tagged("a")}, true},
		{"an unknown mode", []domain.InterfaceVLAN{{InterfaceID: "i", VLANID: "a", Mode: "native"}}, true},
		{"nothing at all", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateVLANMembership(tc.members)
			if tc.wantErr && err == nil {
				t.Error("accepted, want refused")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

func TestAVLANGroupNeedsAName(t *testing.T) {
	if _, err := domain.NewVLANGroup("g", "", nil); err == nil {
		t.Fatal("a nameless group was accepted; it is how somebody chooses between two VLAN 10s")
	}
}
