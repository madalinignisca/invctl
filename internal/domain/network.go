// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

// Interface form factors, covering the physical port types the estate uses
// plus the virtual constructs that sit on top of them.
const (
	FFRJ45     = "rj45"
	FFSFP      = "sfp"
	FFSFPPlus  = "sfp+"
	FFSFP28    = "sfp28"
	FFQSFPPlus = "qsfp+"
	FFQSFP28   = "qsfp28"
	FFVirtual  = "virtual"
	FFLAG      = "lag"
	FFLoopback = "loopback"
)

// FormFactors are the form factors this code knows by name. It is NOT the
// permitted set: since migration 00004 that lives in the interface_form_factor
// table and interface.form_factor is a FOREIGN KEY into it, so 400G optics land
// as an INSERT and appear in this slice never. The constants stay because the
// seed and the tests need names rather than string literals. Nothing branches
// on a form factor -- it is rendered and passed through -- which is exactly why
// this vocabulary became a table.
var FormFactors = []string{
	FFRJ45, FFSFP, FFSFPPlus, FFSFP28, FFQSFPPlus, FFQSFP28,
	FFVirtual, FFLAG, FFLoopback,
}

// Interface is a port on an asset.
//
// Identity is (asset_id, name), not MAC — see docs/DECISIONS.md Q2. A NIC swap
// keeps the port; a MAC follows the card, and virtual interfaces regenerate
// theirs on every boot.
type Interface struct {
	ID          string  `db:"id"`
	AssetID     string  `db:"asset_id"`
	Name        string  `db:"name"`
	FormFactor  string  `db:"form_factor"`
	SpeedMbps   *int    `db:"speed_mbps"`
	MAC         *string `db:"mac"`
	MTU         *int    `db:"mtu"`
	LagParentID *string `db:"lag_parent_id"`
	IsMgmt      bool    `db:"is_mgmt"`
	Enabled     bool    `db:"enabled"`
	// Nullable, unlike on the tables that carried these from the start: rows
	// that predate migration 00019 have whatever change_log could tell us, and
	// NULL where it could tell us nothing. See the migration header.
	CreatedAt *string `db:"created_at"`
	UpdatedAt *string `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token. See Versioned.
	RowVersion int `db:"row_version"`
}

// NewInterface validates and constructs. A MAC, if given, is normalized to
// lowercase colon form so that lookups match regardless of paste format.
func NewInterface(id, assetID, name, formFactor string) (*Interface, error) {
	i := &Interface{
		ID: id, AssetID: assetID, Name: name, FormFactor: formFactor, Enabled: true,
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}

// Validate checks a port against its business rules.
//
// SEPARATE FROM THE CONSTRUCTOR so an update runs the same rules. The checks
// used to live inside NewInterface, where nothing editing an existing port
// could reach them -- the same gap Environment had, and for the same reason:
// there was no update path, so nobody noticed the rules were unreachable.
func (i *Interface) Validate() error {
	ve := &ValidationError{}
	i.Name = checkRequired(ve, "name", i.Name)
	checkRequired(ve, "asset_id", i.AssetID)
	i.FormFactor = checkVocabulary(ve, "form_factor", i.FormFactor)
	// A speed or an MTU that is present must be a real one. Absent is fine --
	// plenty of ports have neither recorded.
	if i.SpeedMbps != nil && *i.SpeedMbps <= 0 {
		ve.Add("speed_mbps", "must be a positive number of megabits, or blank")
	}
	// 68 is the IPv4 minimum an interface must be able to carry; 65535 is the
	// widest any driver here will accept. Both ends catch a transposed digit,
	// which is the realistic error.
	if i.MTU != nil && (*i.MTU < 68 || *i.MTU > 65535) {
		ve.Add("mtu", "must be between 68 and 65535, or blank")
	}
	// A port bonded into itself is a cycle of one, and the LAG walk would not
	// terminate. Deeper cycles are the store's problem, not a field check.
	if i.LagParentID != nil && *i.LagParentID == i.ID {
		ve.Add("lag_parent_id", "a port cannot be bonded into itself")
	}
	return ve.OrNil()
}

// SetMAC normalises and assigns a MAC address.
func (i *Interface) SetMAC(mac string) error {
	if mac == "" {
		i.MAC = nil
		return nil
	}
	normalized, err := ParseMAC(mac)
	if err != nil {
		ve := &ValidationError{}
		ve.Add("mac", "%s", err.Error())
		return ve
	}
	i.MAC = &normalized
	return nil
}

// Link is a cable between two interfaces. Undirected in meaning but stored
// with an a/b side; the store enforces a single active row per port.
//
// Lifecycle is 'active' or 'retired' (docs/DECISIONS.md, 2026-07-28): cables
// get unpatched constantly, and soft-delete-only is a hard rule here same as
// everywhere else. A retired link keeps its row and audit history but is
// excluded from every far-end lookup.
type Link struct {
	ID           string  `db:"id"`
	AInterfaceID string  `db:"a_interface_id"`
	BInterfaceID string  `db:"b_interface_id"`
	Medium       *string `db:"medium"`
	LengthM      *int    `db:"length_m"`
	Lifecycle    string  `db:"lifecycle"`
}

// NewLink validates and constructs a cable.
func NewLink(id, aID, bID string) (*Link, error) {
	ve := &ValidationError{}
	checkRequired(ve, "a_interface_id", aID)
	checkRequired(ve, "b_interface_id", bID)
	if aID == bID {
		ve.Add("b_interface_id", "an interface cannot be linked to itself")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &Link{ID: id, AInterfaceID: aID, BInterfaceID: bID, Lifecycle: LifecycleActive}, nil
}

// IsRetired reports whether this cable has been unpatched.
func (l *Link) IsRetired() bool { return l.Lifecycle == LifecycleRetired }

// Prefix is a network. Bounds are stored as big-endian bytes so containment is
// a range scan (§4.1); the text form is kept for display and uniqueness.
type Prefix struct {
	ID            string  `db:"id"`
	CIDRText      string  `db:"cidr_text"`
	AddrFamily    int     `db:"addr_family"`
	AddrStart     []byte  `db:"addr_start"`
	AddrEnd       []byte  `db:"addr_end"`
	VLANID        *int    `db:"vlan_id"`
	EnvironmentID *string `db:"environment_id"`
	Role          *string `db:"role"`
	CreatedAt     *string `db:"created_at"`
	UpdatedAt     *string `db:"updated_at"`
	RowVersion    int     `db:"row_version"`
}

// NewPrefix parses and normalizes a CIDR into the stored representation.
func NewPrefix(id, cidr string) (*Prefix, error) {
	pv, err := ParsePrefix(cidr)
	if err != nil {
		ve := &ValidationError{}
		ve.Add("cidr_text", "%s", err.Error())
		return nil, ve
	}
	return &Prefix{
		ID: id, CIDRText: pv.Text, AddrFamily: pv.Family,
		AddrStart: pv.Start, AddrEnd: pv.End,
	}, nil
}

// SetCIDR reparses a network and rewrites ALL FOUR stored columns, for the
// reason IPAddress.SetAddress does: the text is the label and the byte range is
// what ResolveAddress scans. A prefix whose text and range disagree answers
// "which network is this address on" with the wrong network.
func (p *Prefix) SetCIDR(cidr string) error {
	pv, err := ParsePrefix(cidr)
	if err != nil {
		ve := &ValidationError{}
		ve.Add("cidr_text", "%s", err.Error())
		return ve
	}
	p.CIDRText, p.AddrFamily = pv.Text, pv.Family
	p.AddrStart, p.AddrEnd = pv.Start, pv.End
	return nil
}

// Validate checks a network against its business rules.
func (p *Prefix) Validate() error {
	ve := &ValidationError{}
	if p.CIDRText == "" || len(p.AddrStart) == 0 || len(p.AddrEnd) == 0 {
		ve.Add("cidr_text", "a network is required")
	}
	// 802.1Q: 0 and 4095 are reserved, so a real tag is 1..4094.
	if p.VLANID != nil && (*p.VLANID < 1 || *p.VLANID > 4094) {
		ve.Add("vlan_id", "must be between 1 and 4094, or blank")
	}
	return ve.OrNil()
}

// IP address roles.
const (
	IPRolePrimary   = "primary"
	IPRoleSecondary = "secondary"
	IPRoleVIP       = "vip"
	IPRoleMgmt      = "mgmt"
	IPRoleFloating  = "floating"
)

// IPRoles are the address roles this code knows by name. It is NOT the
// permitted set: since migration 00004 that lives in the ip_address_role table
// and ip_address.role is a FOREIGN KEY into it. IPRolePrimary stays because it
// is the form's default, not because the set is closed.
var IPRoles = []string{IPRolePrimary, IPRoleSecondary, IPRoleVIP, IPRoleMgmt, IPRoleFloating}

// IPAddress is a single address, optionally bound to an interface. A VIP may
// float, so interface_id is nullable and ON DELETE SET NULL.
type IPAddress struct {
	ID          string  `db:"id"`
	AddrText    string  `db:"addr_text"`
	AddrFamily  int     `db:"addr_family"`
	AddrStart   []byte  `db:"addr_start"`
	InterfaceID *string `db:"interface_id"`
	Role        string  `db:"role"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewIPAddress parses and normalizes an address into the stored representation.
func NewIPAddress(id, addr string, interfaceID *string, role string) (*IPAddress, error) {
	ve := &ValidationError{}
	role = checkVocabulary(ve, "role", role)
	av, err := ParseAddr(addr)
	if err != nil {
		ve.Add("addr_text", "%s", err.Error())
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &IPAddress{
		ID: id, AddrText: av.Text, AddrFamily: av.Family, AddrStart: av.Start,
		InterfaceID: interfaceID, Role: role,
	}, nil
}

// SetAddress reparses an address and rewrites ALL THREE stored columns.
//
// THE ONLY WAY TO CHANGE AN ADDRESS. addr_text is what a person reads and
// addr_start is what every range query actually uses (HANDOVER §4.1) -- they
// are one fact stored three times, and a caller that assigned AddrText alone
// would leave a row that displays 10.1.0.9 and answers containment queries as
// whatever it used to be. Wrong, invisible, and only discovered during an
// incident. A method that cannot set one without the others removes the
// possibility rather than documenting it.
func (a *IPAddress) SetAddress(addr string) error {
	av, err := ParseAddr(addr)
	if err != nil {
		ve := &ValidationError{}
		ve.Add("addr_text", "%s", err.Error())
		return ve
	}
	a.AddrText, a.AddrFamily, a.AddrStart = av.Text, av.Family, av.Start
	return nil
}

// Validate checks an address against its business rules. The address value
// itself is checked by SetAddress, which is the only way to set it.
func (a *IPAddress) Validate() error {
	ve := &ValidationError{}
	a.Role = checkVocabulary(ve, "role", a.Role)
	if a.AddrText == "" || len(a.AddrStart) == 0 {
		ve.Add("addr_text", "an address is required")
	}
	return ve.OrNil()
}
