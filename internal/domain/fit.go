// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "fmt"

// Physical fit: will it go in, will the rack hold it, will it stay cool.
//
// SEPARATE FROM rack.go ON PURPOSE, because the two answer different questions
// and must behave differently. rack.go decides OCCUPANCY -- which unit on which
// face -- and REFUSES a placement it cannot accept, because two boxes in one
// unit is impossible and the record would simply be false.
//
// Everything here is possible. A 780mm server in a 600mm cabinet is in there,
// the rear door does not close, and somebody did it anyway. Refusing that
// placement would not stop it happening, it would stop it being RECORDED: the
// operator either lies to the form or leaves the box out, and the inventory
// gets worse so the validator can be tidier. So none of this returns a
// validation error. It returns findings.

// The estate's severity vocabulary.
//
// HERE RATHER THAN IN store, because domain may not import store and this file
// has to name a severity. store's own constants are defined FROM these rather
// than beside them, so the two cannot drift into disagreeing about what "fault"
// is spelled like -- which would be invisible until a finding stopped sorting.
const (
	// FindingFaultSeverity: wrong now.
	FindingFaultSeverity = "fault"
	// FindingRiskSeverity: survivable now, not after one more failure.
	FindingRiskSeverity = "risk"
	// FindingGapSeverity: not recorded, so not knowable.
	FindingGapSeverity = "gap"
)

// Airflow directions a catalogued model can declare.
//
// NIL IS NOT front_to_rear. Defaulting an undeclared model to the common case
// would let every uncatalogued box pass the opposing-neighbours check in
// silence, and an estate that had declared nothing would report perfect
// airflow. Unknown is unknown, and it reports as a gap.
const (
	AirflowFrontToRear = "front_to_rear"
	AirflowRearToFront = "rear_to_front"
	// AirflowSideToRear is the one that started this: a firewall drawing air
	// from both flanks and exhausting behind. In a standard-width cabinet its
	// intakes and the vertical cable bundles want the same few centimetres.
	AirflowSideToRear = "side_to_rear"
	AirflowSideToSide = "side_to_side"
	// AirflowPassive is a real declaration and NOT the same as unknown: a patch
	// panel, a blanking plate and a cable manager move no air, and saying so is
	// an assertion somebody made.
	AirflowPassive = "passive"
)

// Airflows is the allowed set, matching the CHECK in migration 00038.
var Airflows = []string{
	AirflowFrontToRear, AirflowRearToFront,
	AirflowSideToRear, AirflowSideToSide, AirflowPassive,
}

// AirflowLabels are what the UI shows. Kept beside the constants so a new
// direction cannot be added without somebody deciding what to call it.
var AirflowLabels = map[string]string{
	AirflowFrontToRear: "front to rear",
	AirflowRearToFront: "rear to front",
	AirflowSideToRear:  "sides to rear",
	AirflowSideToSide:  "side to side",
	AirflowPassive:     "moves no air",
}

// DrawsFromSide reports whether this direction takes air from the flanks.
func DrawsFromSide(airflow string) bool {
	return airflow == AirflowSideToRear || airflow == AirflowSideToSide
}

// RearClearanceMM is what a box needs BEHIND its chassis before it fits.
//
// A bare depth_mm <= usable_depth_mm comparison passes a 772mm server into an
// 800mm cabinet, and it does not fit: power cords leave the back of the box,
// and a C13 lead has a bend radius. A check that passes the exact case it was
// built for is a check people mute inside a month.
//
// A CONSTANT RATHER THAN A COLUMN, deliberately. Per-rack clearance is a field
// nobody fills in, and an empty field would silently mean zero -- which is the
// bug this constant exists to avoid. It is stated in the finding text instead,
// so the arithmetic is visible rather than hidden in a stored number.
const RearClearanceMM = 75

// RackFaceplateMM is the width EIA-310 fixes a 19" faceplate at.
//
// This is what makes deriving side clearance from cabinet width HONEST, where
// deriving usable depth from external depth is not. A standard pins the term:
// equipment width is a constant, so clearance follows by arithmetic. Nothing
// pins where a rear door sits, so depth must be measured.
const RackFaceplateMM = 483

// SideBreatherMinWidthMM is the cabinet width below which a side-breathing box
// is competing with the cable channel for its intake.
//
// 600mm cabinets leave roughly 55mm a side and 800mm ones roughly 155mm. The
// threshold sits between them rather than at either, so a 700mm cabinet -- which
// does have room -- does not report.
const SideBreatherMinWidthMM = 700

// SideClearanceMM is what a cabinet of this width leaves beside the equipment.
func SideClearanceMM(widthMM int) int { return (widthMM - RackFaceplateMM) / 2 }

// FitProblem is one thing wrong with a placement, or unknowable about it.
//
// Severity is the estate's own vocabulary rather than a private one, so these
// land on the overview beside everything else without translation.
type FitProblem struct {
	// Kind is what sort of problem this is, so a caller can count them
	// separately without reading the prose. An earlier draft classified these
	// by matching on Detail, which works until somebody improves the wording
	// and silently stops counting.
	Kind     string
	Severity string
	AssetID  string
	Asset    string
	// Detail is written for somebody standing at the rack, so it carries the
	// numbers rather than only the verdict.
	Detail string
}

// The kinds of physical problem, one per check.
const (
	FitTooDeep       = "too_deep"
	FitOverloaded    = "overloaded"
	FitSideStarved   = "side_starved"
	FitOpposedAir    = "opposed_airflow"
	FitDepthUnknown  = "depth_unknown"
	FitLoadUnknown   = "load_unknown"
	FitAirflowUnkown = "airflow_unknown"
)

// FitInput is one placed box, resolved against its model.
//
// Every measurement is optional because every measurement usually is missing.
// That is the ordinary state of an estate and not an error, which is why the
// absent case produces a gap rather than a silence.
type FitInput struct {
	AssetID  string
	Name     string
	Position int
	Height   int
	Face     string
	DepthMM  *int
	// WeightGrams is the catalogued model's, so an uncatalogued box contributes
	// nothing to the total AND is counted as unweighed. Both halves matter: see
	// RackLoad.
	WeightGrams *int
	Airflow     *string
}

// RackFit is the cabinet's own measurements.
type RackFit struct {
	UsableDepthMM *int
	WidthMM       *int
	MaxLoadGrams  *int
}

// CheckDepth reports boxes too long for the cabinet, and says when it cannot
// tell.
//
// Ordered so the reader gets the actionable rows first: something that does not
// fit, then something nobody measured.
func CheckDepth(rack RackFit, boxes []FitInput) []FitProblem {
	var out []FitProblem
	if rack.UsableDepthMM == nil {
		// One row for the cabinet rather than one per box. Forty boxes in an
		// unmeasured rack is a single thing to go and do.
		if len(boxes) > 0 {
			out = append(out, FitProblem{
				Kind:     FitDepthUnknown,
				Severity: FindingGapSeverity,
				Detail: fmt.Sprintf("no usable depth recorded, so nothing in it can be checked "+
					"(%d placed)", len(boxes)),
			})
		}
		return out
	}
	usable := *rack.UsableDepthMM
	unmeasured := 0
	for _, b := range boxes {
		if b.DepthMM == nil {
			unmeasured++
			continue
		}
		needs := *b.DepthMM + RearClearanceMM
		if needs > usable {
			out = append(out, FitProblem{
				Kind:     FitTooDeep,
				Severity: FindingFaultSeverity,
				AssetID:  b.AssetID, Asset: b.Name,
				Detail: fmt.Sprintf("%dmm chassis plus %dmm for cabling needs %dmm, "+
					"and the cabinet has %dmm", *b.DepthMM, RearClearanceMM, needs, usable),
			})
		}
	}
	if unmeasured > 0 {
		out = append(out, FitProblem{
			Kind:     FitDepthUnknown,
			Severity: FindingGapSeverity,
			Detail:   fmt.Sprintf("%s have no catalogued depth", boxCount(unmeasured)),
		})
	}
	return out
}

// RackLoad is what is in a rack and how much of it could be counted.
type RackLoad struct {
	// TotalGrams is a LOWER BOUND, not a total, whenever Unweighed is above
	// zero. Naming it Total would invite exactly the reading this type exists
	// to prevent.
	TotalGrams int
	Weighed    int
	Unweighed  int
}

// Load sums what could be weighed and counts what could not.
//
// SUMMING OVER PARTIAL DATA AND PRINTING A TOTAL IS THE DISHONEST VERSION.
// Eleven boxes of which four are uncatalogued produce a number that silently
// assumes those four weigh nothing, and it is wrong in the dangerous direction:
// it under-reports the load on a rack somebody is about to add to.
func Load(boxes []FitInput) RackLoad {
	var l RackLoad
	for _, b := range boxes {
		if b.WeightGrams == nil {
			l.Unweighed++
			continue
		}
		l.TotalGrams += *b.WeightGrams
		l.Weighed++
	}
	return l
}

// CheckLoad reports a rack carrying more than it is rated for.
func CheckLoad(rack RackFit, boxes []FitInput) []FitProblem {
	load := Load(boxes)
	if rack.MaxLoadGrams == nil {
		if load.Weighed > 0 {
			return []FitProblem{{
				Kind:     FitLoadUnknown,
				Severity: FindingGapSeverity,
				Detail: fmt.Sprintf("carrying at least %s with no load rating recorded",
					Kilograms(load.TotalGrams)),
			}}
		}
		return nil
	}
	rated := *rack.MaxLoadGrams
	if load.TotalGrams <= rated {
		return nil
	}
	// The wording carries the bound and what it could not see, because a
	// reported overload with four boxes unweighed is worse than it reads.
	detail := fmt.Sprintf("at least %s on a %s rating",
		Kilograms(load.TotalGrams), Kilograms(rated))
	if load.Unweighed > 0 {
		verb := "are"
		if load.Unweighed == 1 {
			verb = "is"
		}
		detail += fmt.Sprintf(", and %d of %d boxes %s unweighed",
			load.Unweighed, load.Weighed+load.Unweighed, verb)
	}
	return []FitProblem{{Kind: FitOverloaded, Severity: FindingFaultSeverity, Detail: detail}}
}

// CheckAirflow reports thermal trouble that the declared facts can prove.
//
// TWO FINDINGS, AND THE FIRST INSTINCT WAS THE WRONG ONE. "Warn when a
// side-breather sits in the middle of a rack" fires on every densely populated
// cabinet and stays quiet on the one that matters. A side-breather does not
// care what is above and below it; it cares what is beside it, and that is
// decided by the cabinet's width.
//
// Position does matter, but for the other finding: two neighbours breathing
// against each other, where one is fed the other's exhaust.
//
// IT DOES NOT INFER THAT THE CHANNELS ARE FULL. Cable routing is not modelled,
// so "48 leads terminate here, therefore the intake is blocked" would be a
// confident claim about something nobody recorded. It names the risk and sends
// a person to look.
func CheckAirflow(rack RackFit, boxes []FitInput) []FitProblem {
	var out []FitProblem

	// A side-breather in a cabinet too narrow to feed it.
	for _, b := range boxes {
		if b.Airflow == nil || !DrawsFromSide(*b.Airflow) {
			continue
		}
		if rack.WidthMM == nil {
			out = append(out, FitProblem{
				Kind:     FitAirflowUnkown,
				Severity: FindingGapSeverity,
				AssetID:  b.AssetID, Asset: b.Name,
				Detail: "draws air from the sides and the cabinet width is not recorded",
			})
			continue
		}
		if *rack.WidthMM < SideBreatherMinWidthMM {
			out = append(out, FitProblem{
				Kind:     FitSideStarved,
				Severity: FindingRiskSeverity,
				AssetID:  b.AssetID, Asset: b.Name,
				Detail: fmt.Sprintf("draws air from the sides in a %dmm cabinet, which leaves "+
					"about %dmm a side for both the cable channel and its intake",
					*rack.WidthMM, SideClearanceMM(*rack.WidthMM)),
			})
		}
	}

	// Neighbours breathing against each other.
	//
	// SORTED, AND ADJACENCY IS CHECKED RATHER THAN ASSUMED. Consecutive in the
	// slice is not the same as touching in the rack: the boxes arrive in
	// whatever order the query returned, and even sorted, the entries either
	// side of a gap are not neighbours. A server at U1 and a switch at U40 do
	// not feed each other anything, and reporting them as "directly above"
	// would be false in the words as well as the verdict.
	//
	// Found by mutation -- deleting the sort left the two-box test passing,
	// because airflowOpposes is symmetric, which exposed that the test could
	// not see the ordering AND that the loop never checked adjacency at all.
	sorted := make([]FitInput, len(boxes))
	copy(sorted, boxes)
	sortByPosition(sorted)
	for i := 1; i < len(sorted); i++ {
		lower, upper := sorted[i-1], sorted[i]
		if lower.Airflow == nil || upper.Airflow == nil {
			continue // unknown is not a conflict; it is counted as a gap below
		}
		// Touching, or with at most one free unit between them. A wider gap is
		// a different and much weaker thermal argument, and reporting it would
		// be the noise that gets a page ignored.
		//
		// BOUNDED BELOW AS WELL AS ABOVE. A gap of zero or less means upper is
		// not above lower at all, which cannot happen in sorted input and
		// happens immediately in unsorted -- so an unbounded test let a
		// reversed pair through and made the sort above look optional. The
		// mutation that deleted the sort passed twice before this line said
		// what it meant.
		//
		// The lower bound cannot be killed on its own -- the sort makes a
		// negative gap unreachable through the public entry point. Its job is
		// to make the SORT's removal detectable, and it does: with `gap > 2`
		// alone, deleting sortByPosition passes the suite.
		gap := upper.Position - (lower.Position + lower.Height - 1)
		if gap < 1 || gap > 2 {
			continue
		}
		if !airflowOpposes(*lower.Airflow, *upper.Airflow) {
			continue
		}
		out = append(out, FitProblem{
			Kind:     FitOpposedAir,
			Severity: FindingRiskSeverity,
			AssetID:  upper.AssetID, Asset: upper.Name,
			Detail: fmt.Sprintf("blows %s directly above %s, which blows %s",
				AirflowLabels[*upper.Airflow], lower.Name, AirflowLabels[*lower.Airflow]),
		})
	}

	undeclared := 0
	for _, b := range boxes {
		if b.Airflow == nil {
			undeclared++
		}
	}
	if undeclared > 0 {
		out = append(out, FitProblem{
			Kind:     FitAirflowUnkown,
			Severity: FindingGapSeverity,
			Detail:   fmt.Sprintf("%s have no declared airflow", boxCount(undeclared)),
		})
	}
	return out
}

// airflowOpposes reports whether one box is fed the other's exhaust.
//
// Only front-to-rear against rear-to-front. A side-breather is NOT counted as
// opposing anything: it exhausts rearward like a normal box, and its problem is
// the width one above rather than its neighbour. Passive opposes nothing, which
// is the point of declaring it.
func airflowOpposes(a, b string) bool {
	return (a == AirflowFrontToRear && b == AirflowRearToFront) ||
		(a == AirflowRearToFront && b == AirflowFrontToRear)
}

// sortByPosition orders boxes bottom to top. Insertion sort: a rack holds
// tens of things, and pulling in a dependency for that would be silly.
func sortByPosition(boxes []FitInput) {
	for i := 1; i < len(boxes); i++ {
		for j := i; j > 0 && boxes[j].Position < boxes[j-1].Position; j-- {
			boxes[j], boxes[j-1] = boxes[j-1], boxes[j]
		}
	}
}

// Kilograms renders grams for a human, without a float in sight.
//
// Stored in grams for the reason money is stored in minor units: twenty
// switches at 8.5kg lose ten kilograms to whole-kilogram rounding, and a REAL
// column would put a float across the SQLite/Postgres boundary for a quantity
// nothing ever divides.
func Kilograms(grams int) string {
	whole := grams / 1000
	frac := (grams % 1000) / 100
	if frac == 0 {
		return fmt.Sprintf("%d kg", whole)
	}
	return fmt.Sprintf("%d.%d kg", whole, frac)
}

// boxCount writes a count of placed boxes as English rather than as "box(es)",
// which is the shape of a message nobody proof-read.
func boxCount(n int) string {
	if n == 1 {
		return "1 placed box"
	}
	return fmt.Sprintf("%d placed boxes", n)
}
