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

// Port faces a catalogued model can declare (WP-C3).
//
// NIL IS NOT front, for the reason nil airflow is not front_to_rear: it is the
// common answer, so defaulting to it would let every uncatalogued box pass the
// wrong-face check in silence.
const (
	PortFaceFront = "front"
	PortFaceRear  = "rear"
	// PortFaceBoth is a patch panel or a chassis switch with ports on each
	// side. It is never wrong-facing, which is why it needs saying rather than
	// being left to look like an unanswered question.
	PortFaceBoth = "both"
)

// PortFaces is the allowed set, matching the CHECK in migration 00040.
var PortFaces = []string{PortFaceFront, PortFaceRear, PortFaceBoth}

// PortFaceLabels are what the UI shows.
var PortFaceLabels = map[string]string{
	PortFaceFront: "front",
	PortFaceRear:  "rear",
	PortFaceBoth:  "front and rear",
}

// DenseLeadCount is where a box stops being ordinary to cable.
//
// A NUMBER WITH A REASON, and the finding prints it so a reader can disagree.
// A 600mm cabinet leaves roughly 55mm a side (see SideClearanceMM), which is a
// vertical channel that manages a couple of dozen patch leads before it stops
// being a channel and becomes a bundle. Twenty-four is one side of a 48-port
// switch, which is the point at which somebody who has cabled a rack starts
// thinking about where the slack goes.
//
// It is not a limit and nothing is refused by it: it is the threshold at which
// the cabinet's width becomes worth mentioning.
const DenseLeadCount = 24

// RackUnitMM is the height of one rack unit, fixed by EIA-310.
//
// Exact, like RackFaceplateMM, which is what makes the distance between two
// mounted boxes arithmetic rather than an estimate.
const RackUnitMM = 44.45

// CableRouteAllowanceMM is what a lead needs BEYOND the vertical drop.
//
// A cable does not travel diagonally between two ports. It leaves the port,
// runs to the vertical channel, drops, comes back out and reaches the far port,
// with a service loop so the box can be slid forward without unplugging it.
// Comparing a declared length against the bare vertical distance would call
// every correctly-specified cable long enough and catch nothing.
//
// Deliberately generous: this check exists to catch a lead that cannot possibly
// reach, not to second-guess somebody's cable management.
const CableRouteAllowanceMM = 500

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
	// Cabling (WP-C3).
	FitPortsWrongFace  = "ports_wrong_face"
	FitLeadDensity     = "lead_density"
	FitCableTooShort   = "cable_too_short"
	FitPortFaceUnknown = "port_face_unknown"
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
	// PortFace is where the catalogued model's ports are, and Leads is how many
	// active cables actually terminate on this box. Both are needed together:
	// a rear-ported box with nothing plugged into it is a fact about the
	// catalogue, not a cabling problem.
	PortFace *string
	Leads    int
}

// CableRun is one cable between two boxes in the SAME cabinet, with the
// distance it has to cover already resolved.
//
// Same-cabinet only, and that is a limit rather than an omission: two racks
// have no recorded distance between them. invctl holds no floor plan, so the
// span between cabinets is unknown, and a check that guessed it would be
// inventing the one number the answer turns on.
type CableRun struct {
	LinkID   string
	Label    string
	LengthM  int
	FromUnit int
	ToUnit   int
	// The port face at each end, when the catalogue says. A lead between two
	// boxes whose ports face opposite ways has to travel round the cabinet.
	FromFace *string
	ToFace   *string
	FromName string
	ToName   string
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

// CheckCabling reports what makes this cabinet miserable to work in (WP-C3).
//
// NONE OF THIS REFUSES A PLACEMENT either. A rear-ported switch mounted the
// wrong way round works perfectly; somebody just has to run every lead round
// the cabinet, and will keep doing so until somebody notices. That is exactly
// the sort of fact an inventory is for and a rack inspection is not.
//
// IT DOES NOT INFER THAT THE CHANNEL IS FULL. Cable ROUTING is not modelled --
// which side a lead runs down, whether the manager is already packed -- so
// "48 leads therefore blocked" would be a confident claim about something
// nobody recorded. The finding names the count and the cabinet, and sends a
// person to look.
func CheckCabling(rack RackFit, boxes []FitInput, runs []CableRun) []FitProblem {
	var out []FitProblem
	undeclared := 0

	for _, b := range boxes {
		if b.PortFace == nil {
			// Only counted when something is actually plugged in. An
			// uncatalogued box with no cables is a gap in the catalogue, and
			// reporting it here would bury the ones that matter.
			if b.Leads > 0 {
				undeclared++
			}
			continue
		}
		// A lot of cable on one box, in a cabinet with nowhere to put it.
		if b.Leads >= DenseLeadCount && rack.WidthMM != nil && *rack.WidthMM < SideBreatherMinWidthMM {
			out = append(out, FitProblem{
				Kind:     FitLeadDensity,
				Severity: FindingRiskSeverity,
				AssetID:  b.AssetID, Asset: b.Name,
				Detail: fmt.Sprintf("%s land on it in a %dmm cabinet, which leaves about "+
					"%dmm a side for the whole channel", leads(b.Leads), *rack.WidthMM,
					SideClearanceMM(*rack.WidthMM)),
			})
		}
	}

	// Leads that cross the cabinet.
	//
	// THE FIRST VERSION OF THIS CHECK COMPARED A BOX'S PORT FACE AGAINST THE
	// FACE IT IS MOUNTED ON, and it fired on every server in the estate --
	// which is correct arithmetic and a useless finding, because a server is
	// universally racked from the front with its ports at the back and nothing
	// about that is a problem. A check that reports the normal case is one
	// people switch off.
	//
	// The real cost is a lead between two boxes whose ports face OPPOSITE ways:
	// it leaves the front of one, travels round the cabinet and arrives at the
	// back of the other. That is the patch nobody wants to trace, and it is
	// what the declared faces can actually prove.
	//
	// `both` matches either side, which is the whole reason it is a value: a
	// patch panel presenting ports on each face accommodates whatever it is
	// cabled to.
	crossing := map[string]int{}
	firstFor := map[string]string{}
	for _, run := range runs {
		if run.FromFace == nil || run.ToFace == nil {
			continue
		}
		if *run.FromFace == PortFaceBoth || *run.ToFace == PortFaceBoth {
			continue
		}
		if *run.FromFace == *run.ToFace {
			continue
		}
		crossing[run.FromName]++
		crossing[run.ToName]++
		if _, seen := firstFor[run.FromName]; !seen {
			firstFor[run.FromName] = fmt.Sprintf("%s (%s ports) to %s (%s ports)",
				run.FromName, *run.FromFace, run.ToName, *run.ToFace)
		}
	}
	names := make([]string, 0, len(crossing))
	for name := range crossing {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		detail, ok := firstFor[name]
		if !ok {
			continue // the far end of somebody else's crossing lead
		}
		out = append(out, FitProblem{
			Kind:     FitPortsWrongFace,
			Severity: FindingRiskSeverity,
			Asset:    name,
			Detail: fmt.Sprintf("%s — %s cross the cabinet to reach the far end",
				detail, leads(crossing[name])),
		})
	}

	// Cables that cannot reach.
	for _, run := range runs {
		if run.LengthM <= 0 {
			continue // nobody declared a length; nothing to check it against
		}
		gap := run.FromUnit - run.ToUnit
		if gap < 0 {
			gap = -gap
		}
		needMM := int(float64(gap)*RackUnitMM) + CableRouteAllowanceMM
		if run.LengthM*1000 >= needMM {
			continue
		}
		out = append(out, FitProblem{
			Kind:     FitCableTooShort,
			Severity: FindingFaultSeverity,
			Asset:    run.Label,
			Detail: fmt.Sprintf("declared %dm across %d units, which needs about %dmm "+
				"once routed (%d units of drop plus %dmm to reach the channel and back). "+
				"Either the length is wrong or the cable is under tension",
				run.LengthM, gap, needMM, gap, CableRouteAllowanceMM),
		})
	}

	if undeclared > 0 {
		out = append(out, FitProblem{
			Kind:     FitPortFaceUnknown,
			Severity: FindingGapSeverity,
			Detail: fmt.Sprintf("%s cabled but with no declared port face, so nothing "+
				"can be said about which way their leads run", boxCount(undeclared)),
		})
	}
	return out
}

// leads writes a cable count as English.
func leads(n int) string {
	if n == 1 {
		return "1 lead"
	}
	return fmt.Sprintf("%d leads", n)
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

// sortStrings orders names so a finding list is the same on every run.
// Insertion sort: a rack holds tens of things and importing sort into a package
// that has managed without it would be a poor trade.
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
