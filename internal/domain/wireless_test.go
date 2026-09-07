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
	"testing"
)

func TestNewWirelessLANValidation(t *testing.T) {
	tests := []struct {
		name        string
		wlanName    string
		ssid        string
		security    string
		wantInvalid bool
	}{
		{"blank name", "", "corp", "wpa2_personal", true},
		{"blank ssid", "Corp", "", "wpa2_personal", true},
		{"blank security", "Corp", "corp", "", true},
		{"all present is valid", "Corp", "corp", "wpa2_personal", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWirelessLAN("id-1", tt.wlanName, tt.ssid, tt.security, nil)
			if tt.wantInvalid && err == nil {
				t.Fatal("expected a validation error, got none")
			}
			if !tt.wantInvalid && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantInvalid && !errors.Is(err, ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid so the handler returns 422", err)
			}
		})
	}
}

// TestABadLifecycleIsRejected. Validate is reused by an update path, so it
// must catch what the constructor cannot produce on its own.
func TestABadLifecycleIsRejected(t *testing.T) {
	w, err := NewWirelessLAN("id-1", "Corp", "corp", "wpa2_personal", nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	w.Lifecycle = "deleted"
	if err := w.Validate(); err == nil {
		t.Fatal("a bogus lifecycle was accepted")
	} else if !errors.Is(err, ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

// TestAWPA2PersonalLANWithNoPSKRefIsValid. §2.5: an estate may record an SSID
// without recording where its secret lives, and demanding one is exactly how
// a passphrase ends up pasted into the field. This is a positive assertion
// on purpose -- there is no cross-field rule tying psk_ref to a security
// mode, and this proves nobody added one silently.
func TestAWPA2PersonalLANWithNoPSKRefIsValid(t *testing.T) {
	w, err := NewWirelessLAN("id-1", "Corp", "corp", "wpa2_personal", nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if w.PSKRef != nil {
		t.Fatalf("PSKRef = %v, want nil by default", w.PSKRef)
	}
	if err := w.Validate(); err != nil {
		t.Errorf("a wpa2_personal SSID with no psk_ref was rejected: %v", err)
	}
}

func TestValidateWLANMembership(t *testing.T) {
	tests := []struct {
		name        string
		members     []InterfaceWLAN
		wantInvalid bool
	}{
		{"empty is fine", nil, false},
		{"one member is fine", []InterfaceWLAN{{InterfaceID: "i1", WirelessLANID: "w1"}}, false},
		{"several distinct SSIDs is fine, unlike a VLAN trunk's one-untagged rule", []InterfaceWLAN{
			{InterfaceID: "i1", WirelessLANID: "w1"},
			{InterfaceID: "i1", WirelessLANID: "w2"},
			{InterfaceID: "i1", WirelessLANID: "w3"},
		}, false},
		{"the same SSID twice is refused", []InterfaceWLAN{
			{InterfaceID: "i1", WirelessLANID: "w1"},
			{InterfaceID: "i1", WirelessLANID: "w1"},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWLANMembership(tt.members)
			if tt.wantInvalid && err == nil {
				t.Fatal("expected a validation error, got none")
			}
			if !tt.wantInvalid && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantInvalid && !errors.Is(err, ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}
}
