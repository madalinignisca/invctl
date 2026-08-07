// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

// Rack elevations: where a box sits, and whether two of them are in the same
// place. See migration 00027 for why each column is nullable.

// Faces a box can be mounted on.
const (
	FaceFront = "front"
	FaceRear  = "rear"
)

// Faces is the allowed set.
var Faces = []string{FaceFront, FaceRear}

// DefaultRackUnits is what a rack is assumed to be when nobody has said.
//
// Used only for DISPLAY, never for validation: a position is refused for
// exceeding a height somebody actually recorded, never for exceeding a guess.
const DefaultRackUnits = 42

// MaxRackUnits bounds both a rack's height and a position in one.
const MaxRackUnits = 60

// Placement is one box's occupancy of a rack, resolved.
//
// Height comes from the catalogued model, not from the asset: a rack's own
// u_height is its capacity, and a mounted box's height is a property of what it
// is. Unknown height is treated as 1U for occupancy and SAID SO on the diagram
// -- assuming a box is one unit is right far more often than it is wrong, and
// silently assuming it is nothing would draw an empty rack full of servers.
type Placement struct {
	AssetID   string
	Name      string
	Kind      string
	Position  int
	Height    int
	FullDepth bool
	Face      string
	// HeightKnown is false when the height was assumed rather than catalogued.
	HeightKnown bool
}

// Top is the highest unit this box occupies.
func (p Placement) Top() int { return p.Position + p.Height - 1 }

// Occupies reports whether this placement covers a unit on a face.
//
// A full-depth box covers both faces, which is the whole reason face is stored
// at all: without it, two half-depth panels mounted back to back at U20 would
// read as a collision, and a full-depth server would read as leaving the rear
// free.
func (p Placement) Occupies(unit int, face string) bool {
	if unit < p.Position || unit > p.Top() {
		return false
	}
	return p.FullDepth || p.Face == face
}

// Overlaps reports whether two placements want the same space.
func (p Placement) Overlaps(other Placement) bool {
	if p.AssetID == other.AssetID {
		return false
	}
	for unit := p.Position; unit <= p.Top(); unit++ {
		for _, face := range Faces {
			if p.Occupies(unit, face) && other.Occupies(unit, face) {
				return true
			}
		}
	}
	return false
}

// CheckPlacement validates a position against a rack and what is already in it.
//
// Returns a field error, so a refusal lands on the input that caused it.
func CheckPlacement(p Placement, rackUnits int, existing []Placement) error {
	ve := &ValidationError{}
	switch {
	case p.Position <= 0:
		ve.Add("rack_position", "is the lowest unit the box occupies, counting from the floor")
	case p.Position > MaxRackUnits:
		ve.Add("rack_position", "is higher than any rack")
	}
	if p.Face != "" && !containsString(Faces, p.Face) {
		ve.Add("rack_face", "must be %s or %s", FaceFront, FaceRear)
	}
	if err := ve.OrNil(); err != nil {
		return err
	}

	// Only against a height somebody RECORDED. Refusing U30 in a rack of
	// unknown height, on the grounds that racks are usually 42, would be
	// enforcing a guess.
	if rackUnits > 0 && p.Top() > rackUnits {
		if p.Height > 1 {
			ve.Add("rack_position", "a %dU box at U%d would reach U%d, past the top of a %dU rack",
				p.Height, p.Position, p.Top(), rackUnits)
		} else {
			ve.Add("rack_position", "this rack is %dU", rackUnits)
		}
		return ve.OrNil()
	}

	for _, other := range existing {
		if !p.Overlaps(other) {
			continue
		}
		where := "at U" + itoa(other.Position)
		if other.Height > 1 {
			where = "at U" + itoa(other.Position) + "–U" + itoa(other.Top())
		}
		ve.Add("rack_position", "%s is already there, %s", other.Name, where)
		return ve.OrNil()
	}
	return nil
}
