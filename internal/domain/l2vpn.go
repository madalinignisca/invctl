// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "strings"

// Overlays: one L2 domain stretched across boxes that share no cable.

const (
	L2VPNVXLAN = "vxlan"
	L2VPNVPLS  = "vpls"
	L2VPNEVPN  = "evpn"
	L2VPNMPLS  = "mpls"
	L2VPNL2TP  = "l2tp"
	L2VPNOther = "other"
)

// L2VPNKinds is the Go side of the CHECK constraint.
var L2VPNKinds = []string{L2VPNVXLAN, L2VPNVPLS, L2VPNEVPN, L2VPNMPLS, L2VPNL2TP, L2VPNOther}

// MaxL2VPNIdentifier is the widest identifier any of these carry: a VXLAN VNI
// is 24 bits. A VPLS VC-ID is 32 bits in principle and never that large in
// practice, and one bound that refuses a typo beats two that refuse nothing.
const MaxL2VPNIdentifier = 16777215

// L2VPN is an overlay carrying one broadcast domain across an underlay.
type L2VPN struct {
	ID          string  `db:"id"`
	Name        string  `db:"name"`
	Kind        string  `db:"kind"`
	Identifier  *int64  `db:"identifier"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewL2VPN validates and constructs an overlay.
func NewL2VPN(id, name, kind string) (*L2VPN, error) {
	v := &L2VPN{
		ID: id, Name: strings.TrimSpace(name), Kind: strings.TrimSpace(kind),
		Lifecycle: LifecycleActive,
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return v, nil
}

// Validate checks the kind, the identifier and the name.
func (v *L2VPN) Validate() error {
	ve := &ValidationError{}
	known := false
	for _, k := range L2VPNKinds {
		if v.Kind == k {
			known = true
		}
	}
	if !known {
		ve.Add("kind", "%q is not an overlay technology", v.Kind)
	}
	if strings.TrimSpace(v.Name) == "" {
		ve.Add("name", "an overlay needs a name")
	}
	if v.Identifier != nil && (*v.Identifier < 0 || *v.Identifier > MaxL2VPNIdentifier) {
		ve.Add("identifier", "an identifier is between 0 and %d", MaxL2VPNIdentifier)
	}
	if v.Lifecycle != LifecycleActive && v.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", v.Lifecycle)
	}
	return ve.OrNil()
}

// Retired reports whether the overlay has been withdrawn.
func (v *L2VPN) Retired() bool { return v.Lifecycle == LifecycleRetired }

// L2VPNTermination is what is attached to the overlay at one site: either a
// VLAN or a port, never both.
type L2VPNTermination struct {
	ID          string  `db:"id"`
	L2VPNID     string  `db:"l2vpn_id"`
	VLANID      *string `db:"vlan_id"`
	InterfaceID *string `db:"interface_id"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewL2VPNTermination validates and constructs an attachment.
//
// EXACTLY ONE END, and the constructor says so rather than leaving it to the
// CHECK: a termination naming both a VLAN and a port claims the overlay
// attaches in two places at once, and one naming neither attaches nowhere.
// Both are rows that look like a connection and are not, which is worse than a
// missing row -- a missing row is visibly missing.
func NewL2VPNTermination(id, l2vpnID string, vlanID, interfaceID *string) (*L2VPNTermination, error) {
	t := &L2VPNTermination{
		ID: id, L2VPNID: l2vpnID, VLANID: vlanID, InterfaceID: interfaceID,
		Lifecycle: LifecycleActive,
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

// Validate checks that exactly one end is named.
func (t *L2VPNTermination) Validate() error {
	ve := &ValidationError{}
	if t.L2VPNID == "" {
		ve.Add("l2vpn_id", "a termination needs an overlay")
	}
	hasVLAN := t.VLANID != nil && *t.VLANID != ""
	hasPort := t.InterfaceID != nil && *t.InterfaceID != ""
	switch {
	case hasVLAN && hasPort:
		ve.Add("vlan_id", "a termination attaches a VLAN or a port, not both — "+
			"two ends on one row says the overlay lands in two places at once")
	case !hasVLAN && !hasPort:
		ve.Add("vlan_id", "a termination needs a VLAN or a port; one naming neither "+
			"attaches nowhere while looking like a connection")
	}
	if t.Lifecycle != LifecycleActive && t.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", t.Lifecycle)
	}
	return ve.OrNil()
}

// L2VPNReach is what an overlay's terminations mean for connectivity.
//
// One termination is a stretched domain with nothing at the far end -- the
// overlay is configured and carries traffic between one site and itself, which
// is the same shape of finding as a redundancy group with one member.
type L2VPNReach string

const (
	// L2VPNStretched: two or more sites are in one broadcast domain.
	L2VPNStretched L2VPNReach = "stretched"
	// L2VPNOneEnd: configured, attached once, connecting nothing to anything.
	L2VPNOneEnd L2VPNReach = "one_end"
	// L2VPNUnattached: declared and terminating nowhere.
	L2VPNUnattached L2VPNReach = "unattached"
)

// Reach classifies a termination count.
func Reach(terminationCount int) L2VPNReach {
	switch {
	case terminationCount == 0:
		return L2VPNUnattached
	case terminationCount == 1:
		return L2VPNOneEnd
	default:
		return L2VPNStretched
	}
}

// ReachDescription says what each state means.
func ReachDescription(r L2VPNReach) string {
	switch r {
	case L2VPNStretched:
		return "Two or more attachments, so this overlay genuinely carries a broadcast " +
			"domain between them."
	case L2VPNOneEnd:
		return "Only one attachment. The overlay is configured and connects nothing to " +
			"anything — the far end is either missing from the inventory or was never built."
	case L2VPNUnattached:
		return "Nothing terminates into this overlay, so it carries no traffic at all."
	default:
		return ""
	}
}
