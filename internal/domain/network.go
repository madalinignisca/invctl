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
}

// NewInterface validates and constructs. A MAC, if given, is normalized to
// lowercase colon form so that lookups match regardless of paste format.
func NewInterface(id, assetID, name, formFactor string) (*Interface, error) {
	ve := &ValidationError{}
	name = checkRequired(ve, "name", name)
	checkRequired(ve, "asset_id", assetID)
	formFactor = checkVocabulary(ve, "form_factor", formFactor)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &Interface{
		ID: id, AssetID: assetID, Name: name, FormFactor: formFactor, Enabled: true,
	}, nil
}

// SetMAC normalizes and assigns a MAC address.
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
