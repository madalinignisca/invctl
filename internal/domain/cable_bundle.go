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

// CableBundle is a fact somebody declares about cables that run together: a
// duct, a tray, a trunk (migration 00068, docs/cable-bundles-design.md).
// Nothing instantiates or derives a bundle -- it groups link rows because a
// person said these cables run together, and Task 2's SetBundleMembers is
// what records which ones.
//
// DECLARED THROUGHOUT, and unlike NetGroup there is no source/confidence
// pair: nothing ever proposes a bundle from observed data, so there is no
// provenance question a column needs to answer.
type CableBundle struct {
	ID          string  `db:"id"`
	Code        string  `db:"code"`
	Name        string  `db:"name"`
	Description *string `db:"description"`
	// Lifecycle is DECLARED, like every other lifecycle in this schema:
	// retiring a bundle retires nothing else -- "we stopped managing these
	// cables together" is not "somebody pulled them" (design doc, "Rules").
	// A retired link stays in its bundle for the same reason.
	Lifecycle  string `db:"lifecycle"`
	CreatedAt  string `db:"created_at"`
	UpdatedAt  string `db:"updated_at"`
	RowVersion int    `db:"row_version"`
}

// CableBundleSpec is what a caller supplies to declare a bundle. A spec
// rather than positional arguments for the reason NewNetGroup and
// NewPowerInput already take one: a positional signature had to be replaced
// mid-branch when it could not accept a required field, so a constructor
// that cannot pass a required value cannot build a valid one.
type CableBundleSpec struct {
	Code        string
	Name        string
	Description *string
}

// NewCableBundle validates and constructs a cable bundle. id and now come
// from the caller, per package convention (see errors.go's package doc) --
// this package has no ID or clock source of its own.
func NewCableBundle(id string, spec CableBundleSpec, now time.Time) (*CableBundle, error) {
	ts := FormatTime(now)
	b := &CableBundle{
		ID:          id,
		Code:        spec.Code,
		Name:        spec.Name,
		Description: spec.Description,
		Lifecycle:   LifecycleActive,
		CreatedAt:   ts,
		UpdatedAt:   ts,
		RowVersion:  1,
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// Validate checks a bundle against its business rules, kept separate from
// the constructor so an update path (Task 2) runs the same checks -- see
// Environment.Validate for why that split matters. The DB CHECK is the
// second line of defence, not the first.
func (b *CableBundle) Validate() error {
	ve := &ValidationError{}
	b.Code = strings.ToLower(checkRequired(ve, "code", b.Code))
	b.Name = checkRequired(ve, "name", b.Name)
	if b.Lifecycle != LifecycleActive && b.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", b.Lifecycle)
	}
	return ve.OrNil()
}

// IsRetired reports whether this bundle has been withdrawn.
func (b *CableBundle) IsRetired() bool { return b.Lifecycle == LifecycleRetired }
