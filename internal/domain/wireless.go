// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "strings"

// A wireless LAN is a place things can reach each other, the same claim
// docs/wireless-design.md §2.1 makes for a VLAN: an SSID broadcast by six
// access points is a fact no cable trace can produce, so it joins Structures
// (D1) rather than becoming an edge.

// WirelessLAN is one declared SSID.
type WirelessLAN struct {
	ID   string `db:"id"`
	Name string `db:"name"`
	SSID string `db:"ssid"`
	// Security is a foreign key into wireless_security, a domain vocabulary
	// (D3): nothing here branches on it, so it is checked in the store, not
	// against a Go constant set. See Validate.
	Security string `db:"security"`
	// ScopeAssetID is where this SSID applies -- a site, a building, a rack,
	// all assets here (D6, the same trick vlan_group uses). NULL is
	// estate-wide.
	ScopeAssetID *string `db:"scope_asset_id"`
	// VLANID is the broadcast domain clients land in. Nullable: an estate may
	// record an SSID before anybody has written down where it terminates.
	VLANID *string `db:"vlan_id"`
	// AuthServiceID is which service authenticates this SSID (D5). Recorded
	// and rendered, and nothing derives from it -- deliberately not an impact
	// edge. If the RADIUS service dies the SSID does not stop being
	// broadcast.
	AuthServiceID *string `db:"auth_service_id"`
	// PSKRef is a PATH to the passphrase, never the passphrase (D4, §2.5).
	// Joins RedactedFields beside secret_ref and key_ref.
	PSKRef     *string `db:"psk_ref"`
	Notes      *string `db:"notes"`
	Lifecycle  string  `db:"lifecycle"`
	CreatedAt  *string `db:"created_at"`
	UpdatedAt  *string `db:"updated_at"`
	RowVersion int     `db:"row_version"`
}

// NewWirelessLAN validates and constructs a declared SSID.
func NewWirelessLAN(id, name, ssid, security string, scopeAssetID *string) (*WirelessLAN, error) {
	w := &WirelessLAN{
		ID: id, Name: strings.TrimSpace(name), SSID: strings.TrimSpace(ssid),
		Security: security, ScopeAssetID: scopeAssetID, Lifecycle: LifecycleActive,
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

// Validate checks what the domain can check. The DB CHECK and the foreign key
// are the second line of defence, not the first.
//
// SECURITY IS NOT CHECKED HERE, deliberately: it is a vocabulary, not a Go
// constant set, so a value inserted a second ago must be accepted a second
// later. The store checks it with requireVocabulary inside the transaction
// that is about to store it -- which is what turns SQLite's column-less
// "FOREIGN KEY constraint failed" into a field-level 422 that lists the
// values that exist NOW.
//
// PSK_REF IS NEVER REQUIRED, and no cross-field rule ties it to a security
// mode. An estate may record an SSID without recording where its secret
// lives, and demanding one is exactly how a passphrase ends up pasted into
// the field (docs/wireless-design.md §2.5). A wpa2_personal SSID with no
// psk_ref is an incomplete record; a wpa2_personal SSID with the key in it
// is an incident.
func (w *WirelessLAN) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(w.Name) == "" {
		ve.Add("name", "a wireless network needs a name; the SSID alone does not say what it is for")
	}
	if strings.TrimSpace(w.SSID) == "" {
		ve.Add("ssid", "an SSID is what is broadcast on the air; it cannot be blank")
	}
	if strings.TrimSpace(w.Security) == "" {
		ve.Add("security", "is required")
	}
	if w.Lifecycle != LifecycleActive && w.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", w.Lifecycle)
	}
	return ve.OrNil()
}

// Retired reports whether this SSID has been withdrawn.
func (w *WirelessLAN) Retired() bool { return w.Lifecycle == LifecycleRetired }

// InterfaceWLAN is one radio's membership of one SSID. No mode column, unlike
// InterfaceVLAN: a radio broadcasts an SSID or it does not, and there is no
// tagged/untagged distinction to make. A set row: no id, no lifecycle,
// replaced wholesale with its interface (D7).
type InterfaceWLAN struct {
	InterfaceID   string `db:"interface_id"`
	WirelessLANID string `db:"wireless_lan_id"`
}

// ValidateWLANMembership checks a radio's whole membership set at once.
//
// Refuses a duplicate wireless_lan_id in the submitted slice with ErrInvalid.
// The primary key would refuse it anyway; this gives the operator a
// field-level 422 instead of a bare one. No untagged-equivalent rule exists
// here -- one radio broadcasting six SSIDs is normal (§2.4).
func ValidateWLANMembership(members []InterfaceWLAN) error {
	ve := &ValidationError{}
	seen := map[string]bool{}
	for _, m := range members {
		if seen[m.WirelessLANID] {
			ve.Add("wireless_lan_id", "the same SSID is listed twice on this radio")
		}
		seen[m.WirelessLANID] = true
	}
	return ve.OrNil()
}
