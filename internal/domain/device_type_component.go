// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"strings"
	"time"
)

// Component kinds a device type template can carry (migration 00067).
const (
	ComponentKindInterface  = "interface"
	ComponentKindPowerInput = "power_input"
)

// ComponentKinds are the kinds this code knows how to handle. Unlike a
// vocabulary table, this set IS closed: kind is a BEHAVIOURAL enum (see
// checkEnum) because it decides which of the columns below are meaningful,
// and Task 4's instantiation switches on it to decide which real table a
// component becomes. A third kind arriving as a bare INSERT would fall
// through both switches silently, so a new one requires a release, not a
// row.
var ComponentKinds = []string{ComponentKindInterface, ComponentKindPowerInput}

// DeviceTypeComponent is one entry in a device type's component template: a
// port or a power input every instance of the model has. It is DECLARED, the
// same class as device_type's own physical columns -- somebody read a
// datasheet and asserted this model carries this port. Nothing here is an
// asset yet; Task 4 reads the active rows for a device type and brings the
// real interface/power_input rows into existence when an asset of that type
// is created.
type DeviceTypeComponent struct {
	ID           string `db:"id"`
	DeviceTypeID string `db:"device_type_id"`
	Kind         string `db:"kind"`
	Name         string `db:"name"`
	// Position orders the template for display and for ExpandRange-driven
	// bulk creation (migration 00067 header): "eth0" before "eth1" regardless
	// of what a lexicographic sort of the names would do.
	Position int `db:"position"`
	// FormFactor, SpeedMbps and IsMgmt describe a port and are meaningless on
	// a power_input -- Validate refuses a row that sets any of them outside
	// kind == interface. FormFactor is a FOREIGN KEY into
	// interface_form_factor, the same table Interface.FormFactor points at,
	// so a template can only name a port kind the estate already recognises.
	FormFactor *string `db:"form_factor"`
	SpeedMbps  *int    `db:"speed_mbps"`
	IsMgmt     bool    `db:"is_mgmt"`
	// DrawVA describes a power_input and is meaningless on an interface --
	// Validate refuses a row that sets it outside kind == power_input.
	DrawVA *int `db:"draw_va"`
	// Lifecycle is DECLARED like every other lifecycle in this schema: a
	// component is withdrawn from the template because a person corrected a
	// datasheet reading, never because anything observed reported it
	// missing. The unique index (device_type_id, kind, name) is scoped to
	// `lifecycle = 'active'` (migration 00067 header) precisely so a
	// withdrawn name can be redeclared -- a corrected template is not stuck
	// with a name permanently reserved by the row it just disowned.
	Lifecycle  string `db:"lifecycle"`
	CreatedAt  string `db:"created_at"`
	UpdatedAt  string `db:"updated_at"`
	RowVersion int    `db:"row_version"`
}

// NewDeviceTypeComponent validates and constructs a template entry. id and now
// come from the caller, per package convention (see errors.go's package
// doc) -- this package has no ID or clock source of its own.
// DeviceTypeComponentSpec is what a caller supplies. A spec rather than a
// parameter list because an interface component REQUIRES a form factor, and a
// constructor that cannot accept a required field cannot build a valid value --
// which is what the earlier positional signature did, discovered when the rule
// was added. NewNetGroup and NewPowerInput already take specs for the same
// reason.
type DeviceTypeComponentSpec struct {
	DeviceTypeID string
	Kind         string
	Name         string
	Position     int

	// Interface-shaped. FormFactor is required when Kind is interface.
	FormFactor *string
	SpeedMbps  *int
	IsMgmt     bool

	// power_input-shaped.
	DrawVA *int
}

// NewDeviceTypeComponent validates and constructs one component of a device
// type's template.
func NewDeviceTypeComponent(id string, spec DeviceTypeComponentSpec, now time.Time) (*DeviceTypeComponent, error) {
	ts := FormatTime(now)
	c := &DeviceTypeComponent{
		ID:           id,
		DeviceTypeID: spec.DeviceTypeID,
		Kind:         spec.Kind,
		Name:         spec.Name,
		Position:     spec.Position,
		FormFactor:   spec.FormFactor,
		SpeedMbps:    spec.SpeedMbps,
		IsMgmt:       spec.IsMgmt,
		DrawVA:       spec.DrawVA,
		Lifecycle:    LifecycleActive,
		CreatedAt:    ts,
		UpdatedAt:    ts,
		RowVersion:   1,
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks a template entry against its business rules, kept separate
// from the constructor so an update path (Task 4 or later) runs the same
// checks -- see Interface.Validate for why that split matters.
//
// THE KIND/COLUMN CROSS-CHECK IS THE POINT OF THIS FUNCTION. A power_input
// carrying a speed_mbps is a programming error, not a data-entry mistake a
// person might plausibly make through a form scoped to one kind, so it fails
// loudly here rather than silently storing a column nothing will ever read.
// The DB CHECK on `kind` itself is the second line of defence, not the
// first; there is no DB-level constraint that can express the cross-column
// rule at all, which is exactly why it belongs here.
func (c *DeviceTypeComponent) Validate() error {
	ve := &ValidationError{}
	checkRequired(ve, "device_type_id", c.DeviceTypeID)
	c.Name = checkRequired(ve, "name", c.Name)
	checkEnum(ve, "kind", c.Kind, ComponentKinds)
	if c.Position < 0 {
		ve.Add("position", "must not be negative")
	}
	switch c.Kind {
	case ComponentKindInterface:
		if c.DrawVA != nil {
			ve.Add("draw_va", "belongs to a power_input component, not an interface")
		}
		// REQUIRED, not merely allowed. interface.form_factor is NOT NULL with
		// a foreign key into interface_form_factor, and CreateInterface calls
		// requireVocabulary on it. A template component without one is
		// therefore accepted here and then refused at INSTANTIATION -- meaning
		// every attempt to create an asset of that model fails, with a
		// vocabulary error naming a field the operator never filled in on a
		// form they are not looking at. Refusing it while the template is being
		// written puts the message where the mistake is.
		if c.FormFactor == nil || strings.TrimSpace(*c.FormFactor) == "" {
			ve.Add("form_factor", "an interface component needs a form factor")
		}
	case ComponentKindPowerInput:
		if c.FormFactor != nil {
			ve.Add("form_factor", "belongs to an interface component, not a power_input")
		}
		if c.SpeedMbps != nil {
			ve.Add("speed_mbps", "belongs to an interface component, not a power_input")
		}
		if c.IsMgmt {
			ve.Add("is_mgmt", "belongs to an interface component, not a power_input")
		}
	}
	if c.FormFactor != nil {
		trimmed := checkVocabulary(ve, "form_factor", *c.FormFactor)
		c.FormFactor = &trimmed
	}
	if c.SpeedMbps != nil && *c.SpeedMbps <= 0 {
		ve.Add("speed_mbps", "must be a positive number of megabits, or blank")
	}
	if c.DrawVA != nil && *c.DrawVA <= 0 {
		ve.Add("draw_va", "must be a positive number of volt-amps, or blank")
	}
	if c.Lifecycle != LifecycleActive && c.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", c.Lifecycle)
	}
	return ve.OrNil()
}

// IsRetired reports whether this template entry has been withdrawn.
func (c *DeviceTypeComponent) IsRetired() bool { return c.Lifecycle == LifecycleRetired }
