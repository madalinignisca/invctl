// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "strings"

// First-hop redundancy: the gateway address that survives losing a router.

const (
	FHRPVRRP2 = "vrrp2"
	FHRPVRRP3 = "vrrp3"
	FHRPHSRP  = "hsrp"
	FHRPGLBP  = "glbp"
	FHRPCARP  = "carp"
)

// FHRPProtocols is the Go side of the CHECK constraint.
var FHRPProtocols = []string{FHRPVRRP2, FHRPVRRP3, FHRPHSRP, FHRPGLBP, FHRPCARP}

// FHRPProtocolLabel is how each renders. Not a lookup table: the set is fixed
// by the standards rather than by this estate, and nothing here is a value
// somebody would add locally.
func FHRPProtocolLabel(p string) string {
	switch p {
	case FHRPVRRP2:
		return "VRRPv2"
	case FHRPVRRP3:
		return "VRRPv3"
	case FHRPHSRP:
		return "HSRP"
	case FHRPGLBP:
		return "GLBP"
	case FHRPCARP:
		return "CARP"
	default:
		return p
	}
}

// MinFHRPGroupNumber and MaxFHRPGroupNumber bound a VRID or HSRP group.
const (
	MinFHRPGroupNumber = 0
	MaxFHRPGroupNumber = 255
)

// FHRPGroup is a set of routers sharing one virtual address.
type FHRPGroup struct {
	ID          string  `db:"id"`
	Protocol    string  `db:"protocol"`
	GroupNumber int     `db:"group_number"`
	Name        string  `db:"name"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewFHRPGroup validates and constructs.
func NewFHRPGroup(id, protocol string, groupNumber int, name string) (*FHRPGroup, error) {
	g := &FHRPGroup{
		ID: id, Protocol: strings.TrimSpace(protocol), GroupNumber: groupNumber,
		Name: strings.TrimSpace(name), Lifecycle: LifecycleActive,
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g, nil
}

// Validate checks the protocol, the group number and the name.
func (g *FHRPGroup) Validate() error {
	ve := &ValidationError{}
	known := false
	for _, p := range FHRPProtocols {
		if g.Protocol == p {
			known = true
		}
	}
	if !known {
		ve.Add("protocol", "%q is not a first-hop redundancy protocol", g.Protocol)
	}
	if g.GroupNumber < MinFHRPGroupNumber || g.GroupNumber > MaxFHRPGroupNumber {
		ve.Add("group_number", "a group number is between %d and %d",
			MinFHRPGroupNumber, MaxFHRPGroupNumber)
	}
	if strings.TrimSpace(g.Name) == "" {
		ve.Add("name", "a group needs a name; \"VRRP 10\" is not one when there are four of them")
	}
	if g.Lifecycle != LifecycleActive && g.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", g.Lifecycle)
	}
	return ve.OrNil()
}

// Retired reports whether the group has been withdrawn.
func (g *FHRPGroup) Retired() bool { return g.Lifecycle == LifecycleRetired }

// FHRPMember is one router's participation. A set row owned by the group.
type FHRPMember struct {
	GroupID     string `db:"group_id"`
	InterfaceID string `db:"interface_id"`
	Priority    *int   `db:"priority"`
}

// ValidateFHRPMembers checks a group's whole membership at once.
func ValidateFHRPMembers(members []FHRPMember) error {
	ve := &ValidationError{}
	seen := map[string]bool{}
	for _, m := range members {
		if m.Priority != nil && (*m.Priority < 0 || *m.Priority > 255) {
			ve.Add("priority", "a priority is between 0 and 255")
		}
		if seen[m.InterfaceID] {
			ve.Add("interface_id", "the same port is listed twice in this group")
		}
		seen[m.InterfaceID] = true
	}
	return ve.OrNil()
}

// FHRPRedundancy is what a group's membership means for survivability.
//
// THIS IS THE POINT OF THE WHOLE WORK PACKAGE. A VIP with two members survives
// losing a router; a VIP with one is a single point of failure wearing the
// costume of a redundant one, and it looks identical on every other screen.
// Naming the three states here rather than counting members at each call site
// is what lets one rule change everywhere.
type FHRPRedundancy string

const (
	// FHRPRedundant: more than one router can answer for the address.
	FHRPRedundant FHRPRedundancy = "redundant"
	// FHRPSingleMember: exactly one. The protocol is configured and buys
	// nothing -- losing that router takes the gateway with it.
	FHRPSingleMember FHRPRedundancy = "single_member"
	// FHRPNoMembers: declared and empty. Nothing answers for the address.
	FHRPNoMembers FHRPRedundancy = "no_members"
)

// Redundancy classifies a member count.
func Redundancy(memberCount int) FHRPRedundancy {
	switch {
	case memberCount == 0:
		return FHRPNoMembers
	case memberCount == 1:
		return FHRPSingleMember
	default:
		return FHRPRedundant
	}
}

// RedundancyDescription says what each state means for an incident.
func RedundancyDescription(r FHRPRedundancy) string {
	switch r {
	case FHRPRedundant:
		return "More than one router can answer for this address; losing one is survivable."
	case FHRPSingleMember:
		return "Only one router is in this group. The protocol is configured and buys " +
			"nothing — losing that router takes the gateway with it."
	case FHRPNoMembers:
		return "No router is in this group, so nothing answers for the address."
	default:
		return ""
	}
}
