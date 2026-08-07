// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "strings"

// A VLAN is a place things can reach each other, not a number on a network.

const (
	// VLANModeUntagged is the access VLAN: frames arrive and leave without a
	// tag, and a port has at most one.
	VLANModeUntagged = "untagged"
	// VLANModeTagged is trunk membership: the port carries this VLAN's frames
	// with their 802.1Q tag intact, alongside others.
	VLANModeTagged = "tagged"
)

// VLANModes is the Go side of interface_vlan's CHECK constraint.
var VLANModes = []string{VLANModeUntagged, VLANModeTagged}

// VLANModeDescription is the help text for each. The code branches on these
// values -- one untagged VLAN per port is enforced -- so what they mean is a
// property of this package rather than a row somebody can reword.
func VLANModeDescription(mode string) string {
	switch mode {
	case VLANModeUntagged:
		return "The access VLAN. Frames arrive and leave this port without a tag, " +
			"so a port can have only one."
	case VLANModeTagged:
		return "Trunk membership. The port carries this VLAN's frames with their " +
			"802.1Q tag, alongside every other VLAN on the trunk."
	default:
		return ""
	}
}

// MinVID and MaxVID bound an 802.1Q tag. 0 and 4095 are reserved by the
// standard; 1 is the default VLAN, which is a real VLAN people really use and
// is therefore allowed rather than treated as "unset".
const (
	MinVID = 1
	MaxVID = 4094
)

// VLANGroup is where a set of VLAN IDs is unique. Scoped to an asset -- a site,
// a rack, a cluster -- because all three are assets here, so one reference
// covers what would otherwise need a polymorphic scope and a type column.
type VLANGroup struct {
	ID           string  `db:"id"`
	Name         string  `db:"name"`
	ScopeAssetID *string `db:"scope_asset_id"`
	Description  *string `db:"description"`
	Lifecycle    string  `db:"lifecycle"`
	CreatedAt    *string `db:"created_at"`
	UpdatedAt    *string `db:"updated_at"`
	RowVersion   int     `db:"row_version"`
}

// NewVLANGroup validates and constructs a numbering scope.
func NewVLANGroup(id, name string, scopeAssetID *string) (*VLANGroup, error) {
	g := &VLANGroup{
		ID: id, Name: strings.TrimSpace(name),
		ScopeAssetID: scopeAssetID, Lifecycle: LifecycleActive,
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g, nil
}

// Validate checks what the constructor would have.
func (g *VLANGroup) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(g.Name) == "" {
		ve.Add("name", "a group needs a name; it is how somebody chooses between two VLAN 10s")
	}
	if g.Lifecycle != LifecycleActive && g.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", g.Lifecycle)
	}
	return ve.OrNil()
}

// VLAN is one broadcast domain.
type VLAN struct {
	ID            string  `db:"id"`
	VID           int     `db:"vid"`
	Name          string  `db:"name"`
	GroupID       *string `db:"group_id"`
	Role          *string `db:"role"`
	EnvironmentID *string `db:"environment_id"`
	Description   *string `db:"description"`
	Lifecycle     string  `db:"lifecycle"`
	CreatedAt     *string `db:"created_at"`
	UpdatedAt     *string `db:"updated_at"`
	RowVersion    int     `db:"row_version"`
}

// NewVLAN validates and constructs a broadcast domain.
func NewVLAN(id string, vid int, name string, groupID *string) (*VLAN, error) {
	v := &VLAN{
		ID: id, VID: vid, Name: strings.TrimSpace(name),
		GroupID: groupID, Lifecycle: LifecycleActive,
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return v, nil
}

// Validate checks the tag and the name. The DB CHECK is the second line of
// defence, not the first.
func (v *VLAN) Validate() error {
	ve := &ValidationError{}
	if v.VID < MinVID || v.VID > MaxVID {
		ve.Add("vid", "a VLAN ID is between %d and %d; 0 and 4095 are reserved by 802.1Q",
			MinVID, MaxVID)
	}
	if strings.TrimSpace(v.Name) == "" {
		ve.Add("name", "a VLAN needs a name; the number alone does not say what it carries")
	}
	if v.Lifecycle != LifecycleActive && v.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", v.Lifecycle)
	}
	return ve.OrNil()
}

// Retired reports whether the VLAN has been withdrawn.
func (v *VLAN) Retired() bool { return v.Lifecycle == LifecycleRetired }

// InterfaceVLAN is one port's membership of one VLAN. A set row: no id, no
// lifecycle, replaced wholesale with its interface.
type InterfaceVLAN struct {
	InterfaceID string `db:"interface_id"`
	VLANID      string `db:"vlan_id"`
	Mode        string `db:"mode"`
}

// ValidateVLANMembership checks a port's whole membership set at once.
//
// ONE UNTAGGED VLAN, AT MOST. Two would be a frame with no unambiguous home --
// a configuration no switch accepts, and therefore one this inventory must not
// be able to describe. Checked here as well as by the partial unique index
// because a constructor that validates gives the operator a sentence, and a
// constraint violation gives them a stack trace.
func ValidateVLANMembership(members []InterfaceVLAN) error {
	ve := &ValidationError{}
	untagged := 0
	seen := map[string]bool{}
	for _, m := range members {
		if m.Mode != VLANModeTagged && m.Mode != VLANModeUntagged {
			ve.Add("mode", "%q is not a VLAN mode", m.Mode)
			continue
		}
		if m.Mode == VLANModeUntagged {
			untagged++
		}
		if seen[m.VLANID] {
			ve.Add("vlan_id", "the same VLAN is listed twice on this port")
		}
		seen[m.VLANID] = true
	}
	if untagged > 1 {
		ve.Add("mode", "a port can have only one untagged VLAN; %d were given, and a "+
			"frame arriving without a tag would have no unambiguous home", untagged)
	}
	return ve.OrNil()
}
