// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "time"

// PassThrough is what a patch panel does: a front port and the rear port behind
// it. See migration 00028 for why this is one table rather than a port model.
type PassThrough struct {
	ID               string `db:"id"`
	FrontInterfaceID string `db:"front_interface_id"`
	RearInterfaceID  string `db:"rear_interface_id"`
	// Position is which slot of the rear port this front port takes. 1 for
	// an ordinary panel; a rear port that breaks out has one row per strand,
	// numbered as the trunk is numbered. The tracer reads it: internal/store/
	// cabling.go's walk continues into EVERY strand recorded on a rear port,
	// in position order. Declared, never derived and never renumbered --
	// strand 7 stays strand 7 when strand 6 is unpatched.
	Position   int    `db:"position"`
	Lifecycle  string `db:"lifecycle"`
	CreatedAt  string `db:"created_at"`
	UpdatedAt  string `db:"updated_at"`
	RowVersion int    `db:"row_version"`
}

// PassThroughSpec is what a caller supplies.
type PassThroughSpec struct {
	FrontInterfaceID string
	RearInterfaceID  string
	Position         int
}

// NewPassThrough validates and constructs.
func NewPassThrough(id string, spec PassThroughSpec, now time.Time) (*PassThrough, error) {
	ve := &ValidationError{}
	front := checkRequired(ve, "front_interface_id", spec.FrontInterfaceID)
	rear := checkRequired(ve, "rear_interface_id", spec.RearInterfaceID)
	if front != "" && front == rear {
		ve.Add("rear_interface_id", "a port cannot pass through to itself")
	}
	position := spec.Position
	if position == 0 {
		position = 1
	}
	if position < 1 {
		ve.Add("position", "is which slot of the rear port this takes, counting from 1")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &PassThrough{
		ID: id, FrontInterfaceID: front, RearInterfaceID: rear, Position: position,
		Lifecycle: LifecycleActive,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// Validate re-checks after field updates.
func (p *PassThrough) Validate() error {
	ve := &ValidationError{}
	p.FrontInterfaceID = checkRequired(ve, "front_interface_id", p.FrontInterfaceID)
	p.RearInterfaceID = checkRequired(ve, "rear_interface_id", p.RearInterfaceID)
	if p.FrontInterfaceID != "" && p.FrontInterfaceID == p.RearInterfaceID {
		ve.Add("rear_interface_id", "a port cannot pass through to itself")
	}
	if p.Position < 1 {
		ve.Add("position", "is which slot of the rear port this takes, counting from 1")
	}
	checkEnum(ve, "lifecycle", p.Lifecycle, []string{LifecycleActive, LifecycleRetired})
	return ve.OrNil()
}
