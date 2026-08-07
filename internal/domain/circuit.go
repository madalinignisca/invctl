// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "strings"

// Circuits: the connectivity somebody signs a contract for.

const (
	// SideA and SideZ are the two ends. The schema does not decide which is
	// "ours" -- that is a property of what each terminates on.
	SideA = "a"
	SideZ = "z"
)

// CircuitSides is the Go side of the CHECK constraint.
var CircuitSides = []string{SideA, SideZ}

// Provider is who sells the connectivity.
type Provider struct {
	ID          string  `db:"id"`
	Name        string  `db:"name"`
	AccountRef  *string `db:"account_ref"`
	PortalURL   *string `db:"portal_url"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewProvider validates and constructs a carrier.
func NewProvider(id, name string) (*Provider, error) {
	p := &Provider{ID: id, Name: strings.TrimSpace(name), Lifecycle: LifecycleActive}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// Validate checks a provider.
//
// account_ref is governed by the team.contact_ref rule: an account or customer
// reference, never a named person. A CMDB kept for ever with an append-only
// change_log must carry nothing anybody could ask to have erased.
func (p *Provider) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(p.Name) == "" {
		ve.Add("name", "a provider needs a name")
	}
	if p.Lifecycle != LifecycleActive && p.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", p.Lifecycle)
	}
	return ve.OrNil()
}

// Circuit is one contracted connection.
type Circuit struct {
	ID          string  `db:"id"`
	CID         string  `db:"cid"`
	ProviderID  string  `db:"provider_id"`
	ServiceType *string `db:"service_type"`
	CommitMbps  *int    `db:"commit_mbps"`
	InstallDate *string `db:"install_date"`
	// ContractEnd is why this joins the expiry report, and it is NOT an end of
	// support: nothing stops working on the day. Somebody either renegotiates
	// or is auto-renewed at a rate nobody checked, which is the cheaper of the
	// two failures to catch early.
	ContractEnd *string `db:"contract_end"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewCircuit validates and constructs.
func NewCircuit(id, cid, providerID string) (*Circuit, error) {
	c := &Circuit{
		ID: id, CID: strings.TrimSpace(cid), ProviderID: providerID,
		Lifecycle: LifecycleActive,
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the identifier, the dates and the commit.
func (c *Circuit) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(c.CID) == "" {
		ve.Add("cid", "a circuit needs the provider's own identifier; it is what you "+
			"quote when you ring them")
	}
	if strings.TrimSpace(c.ProviderID) == "" {
		ve.Add("provider_id", "a circuit needs a provider")
	}
	c.InstallDate = checkDate(ve, "install_date", c.InstallDate)
	c.ContractEnd = checkDate(ve, "contract_end", c.ContractEnd)
	if c.CommitMbps != nil && *c.CommitMbps <= 0 {
		ve.Add("commit_mbps", "a committed rate is a positive number of megabits, or blank")
	}
	// A contract that ended before it was installed is not a typo the reader
	// can see -- it renders as two plausible dates in different columns.
	if c.InstallDate != nil && c.ContractEnd != nil && *c.ContractEnd < *c.InstallDate {
		ve.Add("contract_end", "the contract ends before the circuit was installed")
	}
	if c.Lifecycle != LifecycleActive && c.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", c.Lifecycle)
	}
	return ve.OrNil()
}

// Retired reports whether the circuit has been ceased.
func (c *Circuit) Retired() bool { return c.Lifecycle == LifecycleRetired }

// CircuitTermination is one end of a circuit: a site or a port, never both.
type CircuitTermination struct {
	ID          string  `db:"id"`
	CircuitID   string  `db:"circuit_id"`
	Side        string  `db:"side"`
	AssetID     *string `db:"asset_id"`
	InterfaceID *string `db:"interface_id"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewCircuitTermination validates and constructs one end.
func NewCircuitTermination(id, circuitID, side string, assetID, interfaceID *string) (*CircuitTermination, error) {
	t := &CircuitTermination{
		ID: id, CircuitID: circuitID, Side: strings.TrimSpace(side),
		AssetID: assetID, InterfaceID: interfaceID, Lifecycle: LifecycleActive,
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

// Validate checks the side and that exactly one end is named.
func (t *CircuitTermination) Validate() error {
	ve := &ValidationError{}
	if t.Side != SideA && t.Side != SideZ {
		ve.Add("side", "a circuit has an A end and a Z end; %q is neither", t.Side)
	}
	hasAsset := t.AssetID != nil && *t.AssetID != ""
	hasPort := t.InterfaceID != nil && *t.InterfaceID != ""
	switch {
	case hasAsset && hasPort:
		ve.Add("asset_id", "a termination lands on a site or a port, not both — "+
			"two ends on one row says the circuit arrives twice")
	case !hasAsset && !hasPort:
		ve.Add("asset_id", "a termination needs a site or a port; one naming neither "+
			"lands nowhere while looking like a connection")
	}
	if t.Lifecycle != LifecycleActive && t.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", t.Lifecycle)
	}
	return ve.OrNil()
}
