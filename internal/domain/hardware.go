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

// The hardware catalogue: who makes a thing, and what the thing is.
//
// A device type is the model, entered once. An asset is one box of that model.
// The distinction earns its keep on dates: a manufacturer publishes end of
// support per MODEL, and typing it onto each of forty servers is how it ends up
// typed onto none of them.

// Manufacturer is who makes a device type.
type Manufacturer struct {
	ID   string `db:"id"`
	Code string `db:"code"`
	Name string `db:"name"`
	// SupportRef is a portal, a partner login URL or a contract reference --
	// where to go when a model on this maker's list is about to lapse. Never a
	// person, and never a credential: the same rule team.contact_ref holds to,
	// and for the same reason, which is that this database is read by more
	// people than wrote it.
	SupportRef *string `db:"support_ref"`
	Lifecycle  string  `db:"lifecycle"`
	CreatedAt  string  `db:"created_at"`
	UpdatedAt  string  `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// HardwareLifecycles reuses the estate-wide set, so a catalogue entry reads the
// way an asset does.
var HardwareLifecycles = []string{
	LifecyclePlanned, LifecycleActive, LifecycleDeprecated, LifecycleRetired,
}

// ManufacturerSpec is what a caller supplies.
type ManufacturerSpec struct {
	Code       string
	Name       string
	SupportRef *string
	Lifecycle  string
}

// NewManufacturer validates and constructs.
func NewManufacturer(id string, spec ManufacturerSpec, now time.Time) (*Manufacturer, error) {
	ve := &ValidationError{}
	code := checkRequired(ve, "code", strings.ToLower(strings.TrimSpace(spec.Code)))
	name := checkRequired(ve, "name", spec.Name)
	lifecycle := defaultedLifecycle(ve, spec.Lifecycle)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &Manufacturer{
		ID: id, Code: code, Name: name,
		SupportRef: blankToNil(spec.SupportRef),
		Lifecycle:  lifecycle,
		CreatedAt:  FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// Validate re-checks a manufacturer after field updates.
//
// Separate from the constructor so an update runs the same rules. Every entity
// here that gained an edit form and did not gain this had a form that could
// write past its own validation.
func (m *Manufacturer) Validate() error {
	ve := &ValidationError{}
	m.Code = checkRequired(ve, "code", strings.ToLower(strings.TrimSpace(m.Code)))
	m.Name = checkRequired(ve, "name", m.Name)
	m.SupportRef = blankToNil(m.SupportRef)
	checkEnum(ve, "lifecycle", m.Lifecycle, HardwareLifecycles)
	return ve.OrNil()
}

// IsRetired reports whether this maker is off the list.
func (m *Manufacturer) IsRetired() bool { return m.Lifecycle == LifecycleRetired }

// DeviceType is a model: the thing you buy several of.
type DeviceType struct {
	ID             string  `db:"id"`
	ManufacturerID string  `db:"manufacturer_id"`
	Model          string  `db:"model"`
	PartNumber     *string `db:"part_number"`
	// UHeight is rack units, and nil rather than zero for anything that does not
	// occupy any -- a blade, a module, a virtual appliance. Zero would be a
	// claim about a thing that does not fit in a rack; nil is the absence of
	// one.
	UHeight   *int `db:"u_height"`
	FullDepth bool `db:"full_depth"`
	// DepthMM is the chassis as the manufacturer states it. What the box needs
	// in a cabinet is larger -- see domain.RearClearanceMM, applied where the
	// check runs rather than baked in here, so a finding can show its working.
	DepthMM *int `db:"depth_mm"`
	// WeightGrams, in grams for the reason money is in minor units. See
	// domain.Kilograms.
	WeightGrams *int `db:"weight_grams"`
	// Airflow is which way the air goes, and nil means NOBODY SAID -- never
	// front-to-rear. Defaulting it would let every uncatalogued box pass the
	// opposing-neighbours check in silence.
	Airflow *string `db:"airflow"`
	// EOLDate is the MANUFACTURER's end of support for this model. An asset that
	// states its own overrides it -- see AssetExpiry.
	EOLDate   *string `db:"eol_date"`
	Notes     *string `db:"notes"`
	Lifecycle string  `db:"lifecycle"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// DeviceTypeSpec is what a caller supplies.
type DeviceTypeSpec struct {
	ManufacturerID string
	Model          string
	PartNumber     *string
	UHeight        *int
	FullDepth      bool
	DepthMM        *int
	WeightGrams    *int
	Airflow        *string
	EOLDate        *string
	Notes          *string
	Lifecycle      string
}

// NewDeviceType validates and constructs.
func NewDeviceType(id string, spec DeviceTypeSpec, now time.Time) (*DeviceType, error) {
	ve := &ValidationError{}
	manufacturer := checkRequired(ve, "manufacturer_id", spec.ManufacturerID)
	model := checkRequired(ve, "model", spec.Model)
	eol := checkDate(ve, "eol_date", spec.EOLDate)
	checkUHeight(ve, spec.UHeight)
	checkMillimetres(ve, "depth_mm", spec.DepthMM)
	checkGrams(ve, "weight_grams", spec.WeightGrams)
	checkOptionalEnum(ve, "airflow", spec.Airflow, Airflows)
	lifecycle := defaultedLifecycle(ve, spec.Lifecycle)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &DeviceType{
		ID: id, ManufacturerID: manufacturer, Model: model,
		PartNumber:  blankToNil(spec.PartNumber),
		UHeight:     spec.UHeight,
		FullDepth:   spec.FullDepth,
		DepthMM:     spec.DepthMM,
		WeightGrams: spec.WeightGrams,
		Airflow:     blankToNil(spec.Airflow),
		EOLDate:     eol,
		Notes:       blankToNil(spec.Notes),
		Lifecycle:   lifecycle,
		CreatedAt:   FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// Validate re-checks a device type after field updates.
func (d *DeviceType) Validate() error {
	ve := &ValidationError{}
	d.ManufacturerID = checkRequired(ve, "manufacturer_id", d.ManufacturerID)
	d.Model = checkRequired(ve, "model", d.Model)
	d.EOLDate = checkDate(ve, "eol_date", d.EOLDate)
	d.PartNumber = blankToNil(d.PartNumber)
	d.Notes = blankToNil(d.Notes)
	checkUHeight(ve, d.UHeight)
	checkMillimetres(ve, "depth_mm", d.DepthMM)
	checkGrams(ve, "weight_grams", d.WeightGrams)
	d.Airflow = blankToNil(d.Airflow)
	checkOptionalEnum(ve, "airflow", d.Airflow, Airflows)
	checkEnum(ve, "lifecycle", d.Lifecycle, HardwareLifecycles)
	return ve.OrNil()
}

// IsRetired reports whether this model is off the catalogue.
func (d *DeviceType) IsRetired() bool { return d.Lifecycle == LifecycleRetired }

// checkUHeight refuses a height that is not a height.
//
// Bounded above as well as below. Nothing is nine hundred rack units tall, and
// the value feeds rack elevation arithmetic in WP-B5 -- where a typo like 442
// for 42 does not look wrong in a form and does look wrong in a diagram, long
// after anybody remembers typing it.
func checkUHeight(ve *ValidationError, u *int) {
	if u == nil {
		return
	}
	switch {
	case *u <= 0:
		ve.Add("u_height", "must be at least 1, or left empty for something that does not occupy rack units")
	case *u > 60:
		ve.Add("u_height", "is taller than any rack; leave it empty if this does not mount")
	}
}

// defaultedLifecycle applies the estate-wide default and checks membership.
func defaultedLifecycle(ve *ValidationError, lifecycle string) string {
	lifecycle = strings.TrimSpace(lifecycle)
	if lifecycle == "" {
		lifecycle = LifecycleActive
	}
	checkEnum(ve, "lifecycle", lifecycle, HardwareLifecycles)
	return lifecycle
}

// EOLSource says where a resolved end-of-support date came from.
//
// The whole point of inheriting a date is that the answer is then only half the
// information. "Out of support in March" and "its MODEL is out of support in
// March, and nobody has checked this particular box against the contract" send
// a reader to different people, and a report that renders them identically has
// quietly merged a fact with an assumption.
//
// Same argument as source/confidence in docs/AUDIT.md, applied to a date.
const (
	// EOLFromAsset means somebody stated it for this box. It WINS over the
	// model's date, in both directions: a private support contract can carry a
	// unit years past what the manufacturer publishes, and a damaged or
	// second-hand unit can fall short of it. The specific assertion beats the
	// general fact, which is the only ordering that lets a person record what
	// they actually know.
	EOLFromAsset = "asset"
	// EOLFromDeviceType means nobody stated it for this box, so its model's
	// published date is standing in.
	EOLFromDeviceType = "device_type"
	// EOLFromNowhere means neither has one. Not an error, and worth rendering:
	// an estate with no dates recorded looks identical to an estate where
	// nothing expires, and those are opposite situations.
	EOLFromNowhere = ""
)

// ResolveEOL applies the override rule: the asset's own date if it has one,
// otherwise its model's, and says which it used.
//
// One function so that the report, the asset page and the search index cannot
// each reach a different answer -- which is what happened the last time a rule
// this small was written out three times.
func ResolveEOL(assetEOL, typeEOL *string) (date *string, source string) {
	if assetEOL != nil && *assetEOL != "" {
		return assetEOL, EOLFromAsset
	}
	if typeEOL != nil && *typeEOL != "" {
		return typeEOL, EOLFromDeviceType
	}
	return nil, EOLFromNowhere
}
