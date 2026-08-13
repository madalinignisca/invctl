// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

// The rules of physical fit.
//
// EVERY POSITIVE HAS A NEGATIVE CONTROL. A check that fires on a bad
// arrangement proves nothing on its own -- one that fires on everything would
// pass the same assertion -- so each case below is paired with an arrangement
// it must stay quiet about.

func mm(n int) *int        { return &n }
func air(s string) *string { return &s }

// kinds counts problems by kind, which is what the callers do.
func kinds(problems []FitProblem) map[string]int {
	out := map[string]int{}
	for _, p := range problems {
		out[p.Kind]++
	}
	return out
}

// TestDepthCountsTheCablingBehindTheBox.
//
// THE CASE THE WHOLE CHECK EXISTS FOR. A 772mm server in an 800mm cabinet
// passes a bare depth <= usable comparison and does not fit, because the power
// cords and their bend radius are behind it. A check that passed this would be
// muted within a month, so the boundary either side of the allowance is
// asserted rather than a comfortable middle.
func TestDepthCountsTheCablingBehindTheBox(t *testing.T) {
	server := FitInput{AssetID: "a", Name: "srv-1", Position: 1, DepthMM: mm(772)}

	for _, tc := range []struct {
		name   string
		usable int
		want   int
	}{
		{"a bare comparison would pass this, and it does not fit", 800, 1},
		{"exactly the chassis plus the allowance fits", 772 + RearClearanceMM, 0},
		{"one millimetre under does not", 772 + RearClearanceMM - 1, 1},
		{"a deep cabinet is fine", 1000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(CheckDepth(RackFit{UsableDepthMM: mm(tc.usable)}, []FitInput{server}))
			if got[FitTooDeep] != tc.want {
				t.Errorf("a %dmm box in a %dmm cabinet reported %d too-deep findings, want %d",
					772, tc.usable, got[FitTooDeep], tc.want)
			}
		})
	}
}

// TestAnUnmeasuredCabinetReportsUnknownRatherThanFits. The whole argument for
// building this here rather than in a spreadsheet: most tools can only say yes
// or no, so an unmeasured rack silently reads as fine.
func TestAnUnmeasuredCabinetReportsUnknownRatherThanFits(t *testing.T) {
	boxes := []FitInput{{AssetID: "a", Name: "srv-1", DepthMM: mm(772)}}

	got := kinds(CheckDepth(RackFit{}, boxes))
	if got[FitDepthUnknown] != 1 {
		t.Errorf("an unmeasured cabinet produced %d gap findings, want 1 -- silence here "+
			"reads as 'it fits'", got[FitDepthUnknown])
	}
	if got[FitTooDeep] != 0 {
		t.Errorf("an unmeasured cabinet reported %d too-deep findings; it cannot know that",
			got[FitTooDeep])
	}

	// And an EMPTY unmeasured cabinet is not a gap: there is nothing in it to
	// check and nothing to go and measure for.
	if n := len(CheckDepth(RackFit{}, nil)); n != 0 {
		t.Errorf("an empty unmeasured cabinet produced %d findings, want none", n)
	}
}

// TestAnUncataloguedBoxIsCountedRatherThanAssumedToFit.
func TestAnUncataloguedBoxIsCountedRatherThanAssumedToFit(t *testing.T) {
	boxes := []FitInput{
		{AssetID: "a", Name: "measured", DepthMM: mm(400)},
		{AssetID: "b", Name: "unmeasured"},
	}
	got := kinds(CheckDepth(RackFit{UsableDepthMM: mm(900)}, boxes))
	if got[FitDepthUnknown] != 1 {
		t.Errorf("one box with no catalogued depth produced %d gap findings, want 1",
			got[FitDepthUnknown])
	}
	if got[FitTooDeep] != 0 {
		t.Errorf("nothing is too deep here; got %d", got[FitTooDeep])
	}
}

// TestLoadIsALowerBoundWhenAnythingIsUnweighed.
//
// Summing over partial data and printing a total is the dishonest version: it
// silently assumes the boxes nobody weighed weigh nothing, and it is wrong in
// the dangerous direction -- it under-reports a rack somebody is about to add
// to.
func TestLoadIsALowerBoundWhenAnythingIsUnweighed(t *testing.T) {
	boxes := []FitInput{
		{AssetID: "a", Name: "one", WeightGrams: mm(20000)},
		{AssetID: "b", Name: "two", WeightGrams: mm(20000)},
		{AssetID: "c", Name: "three"},
	}
	load := Load(boxes)
	if load.TotalGrams != 40000 || load.Weighed != 2 || load.Unweighed != 1 {
		t.Fatalf("Load = %+v, want 40000g over 2 weighed and 1 unweighed", load)
	}

	// Over the rating, with something unseen: the finding has to say both.
	problems := CheckLoad(RackFit{MaxLoadGrams: mm(30000)}, boxes)
	if len(problems) != 1 || problems[0].Kind != FitOverloaded {
		t.Fatalf("problems = %+v, want one overload", problems)
	}
	detail := problems[0].Detail
	if !mentions(detail, "at least") {
		t.Errorf("detail %q does not say the total is a lower bound", detail)
	}
	if !mentions(detail, "unweighed") {
		t.Errorf("detail %q does not say what it could not see, so the overload "+
			"reads as more precise than it is", detail)
	}

	// NEGATIVE CONTROL: under the rating reports nothing at all.
	if n := len(CheckLoad(RackFit{MaxLoadGrams: mm(90000)}, boxes)); n != 0 {
		t.Errorf("a rack under its rating produced %d findings, want none", n)
	}
}

// TestASideBreatherIsJudgedByTheCabinetNotItsPosition.
//
// THE FIRST INSTINCT WAS THE WRONG AXIS. "Warn when a side-breather is in the
// middle of a rack" fires on every densely populated cabinet and stays silent
// on the one that matters. The predicate is the cabinet's width.
func TestASideBreatherIsJudgedByTheCabinetNotItsPosition(t *testing.T) {
	// Same box, same position, two cabinets. If position decided this, both
	// would report.
	fortigate := FitInput{
		AssetID: "fw", Name: "fw-branch-1", Position: 20,
		Airflow: air(AirflowSideToRear),
	}

	narrow := kinds(CheckAirflow(RackFit{WidthMM: mm(600)}, []FitInput{fortigate}))
	if narrow[FitSideStarved] != 1 {
		t.Errorf("a side-breather in a 600mm cabinet produced %d findings, want 1",
			narrow[FitSideStarved])
	}

	wide := kinds(CheckAirflow(RackFit{WidthMM: mm(800)}, []FitInput{fortigate}))
	if wide[FitSideStarved] != 0 {
		t.Errorf("a side-breather in an 800mm network cabinet produced %d findings, "+
			"want none -- that is the cabinet you buy for it", wide[FitSideStarved])
	}

	// A front-to-rear box in the same narrow cabinet is fine, so the finding is
	// about the airflow and not about the width alone.
	server := FitInput{AssetID: "s", Name: "srv-1", Position: 20, Airflow: air(AirflowFrontToRear)}
	got := kinds(CheckAirflow(RackFit{WidthMM: mm(600)}, []FitInput{server}))
	if got[FitSideStarved] != 0 {
		t.Errorf("a front-to-rear box in a narrow cabinet produced %d side findings, want none",
			got[FitSideStarved])
	}
}

// TestNeighboursBreathingAgainstEachOther. The finding position genuinely
// decides -- and it is adjacency, not middle-ness.
func TestNeighboursBreathingAgainstEachOther(t *testing.T) {
	lower := FitInput{AssetID: "a", Name: "srv-1", Position: 10, Height: 1, Airflow: air(AirflowFrontToRear)}
	upper := FitInput{AssetID: "b", Name: "sw-1", Position: 11, Height: 1, Airflow: air(AirflowRearToFront)}

	got := kinds(CheckAirflow(RackFit{WidthMM: mm(800)}, []FitInput{lower, upper}))
	if got[FitOpposedAir] != 1 {
		t.Fatalf("a rear-to-front switch above a front-to-rear server produced %d "+
			"findings, want 1", got[FitOpposedAir])
	}

	// ORDER MUST NOT MATTER -- AND TWO BOXES CANNOT PROVE THAT.
	//
	// The first version of this assertion swapped a pair and expected the same
	// answer. It passed with the sort DELETED, because airflowOpposes is
	// symmetric: whichever way round the two arrive, the pair still opposes.
	// The mutation that should have failed did not, which is the whole reason
	// to run one.
	//
	// Three boxes distinguish it. Sorted, the adjacent pairs are (1,20) and
	// (20,21) -- and only the second pair is touching, so exactly one finding.
	// Unsorted in the order below the loop would compare (20,1) and (1,21),
	// which are neither of them adjacent, and report nothing at all.
	scrambled := []FitInput{
		{AssetID: "x", Name: "sw-top", Position: 20, Height: 1, Airflow: air(AirflowRearToFront)},
		{AssetID: "y", Name: "srv-bottom", Position: 1, Height: 1, Airflow: air(AirflowFrontToRear)},
		{AssetID: "z", Name: "srv-above", Position: 21, Height: 1, Airflow: air(AirflowFrontToRear)},
	}
	got3 := kinds(CheckAirflow(RackFit{WidthMM: mm(800)}, scrambled))
	if got3[FitOpposedAir] != 1 {
		t.Errorf("three scrambled boxes produced %d opposing findings, want exactly 1 -- "+
			"the pair at U20/U21. Getting 0 means the input was not sorted; getting 2 "+
			"means U1 and U20 were treated as neighbours", got3[FitOpposedAir])
	}

	// DISTANT BOXES ARE NOT NEIGHBOURS. The detail says "directly above", and a
	// server at U1 with a switch at U40 is not that in the words or the physics.
	distant := []FitInput{
		{AssetID: "a", Name: "srv-1", Position: 1, Height: 1, Airflow: air(AirflowFrontToRear)},
		{AssetID: "b", Name: "sw-1", Position: 40, Height: 1, Airflow: air(AirflowRearToFront)},
	}
	if n := kinds(CheckAirflow(RackFit{WidthMM: mm(800)}, distant))[FitOpposedAir]; n != 0 {
		t.Errorf("boxes 39 units apart produced %d opposing findings, want none", n)
	}

	// NEGATIVE CONTROL: agreeing neighbours report nothing.
	agreeing := FitInput{AssetID: "c", Name: "sw-2", Position: 11, Height: 1, Airflow: air(AirflowFrontToRear)}
	quiet := kinds(CheckAirflow(RackFit{WidthMM: mm(800)}, []FitInput{lower, agreeing}))
	if quiet[FitOpposedAir] != 0 {
		t.Errorf("two front-to-rear boxes produced %d opposing findings, want none",
			quiet[FitOpposedAir])
	}

	// A PASSIVE box opposes nothing, which is the point of being able to
	// declare it -- a patch panel between two servers is not a thermal problem.
	panel := FitInput{AssetID: "p", Name: "patch-1", Position: 11, Height: 1, Airflow: air(AirflowPassive)}
	passive := kinds(CheckAirflow(RackFit{WidthMM: mm(800)}, []FitInput{lower, panel}))
	if passive[FitOpposedAir] != 0 {
		t.Errorf("a passive panel opposed something: %d findings, want none",
			passive[FitOpposedAir])
	}
}

// TestUndeclaredAirflowIsNeitherAConflictNorASilence.
//
// The default that would ruin this feature: treating nil as front-to-rear makes
// an estate that has declared nothing report perfect airflow.
func TestUndeclaredAirflowIsNeitherAConflictNorASilence(t *testing.T) {
	declared := FitInput{AssetID: "a", Name: "srv-1", Position: 10, Height: 1, Airflow: air(AirflowRearToFront)}
	unknown := FitInput{AssetID: "b", Name: "mystery", Position: 11, Height: 1}

	got := kinds(CheckAirflow(RackFit{WidthMM: mm(800)}, []FitInput{declared, unknown}))
	if got[FitOpposedAir] != 0 {
		t.Errorf("an undeclared box was treated as opposing its neighbour: %d findings, "+
			"want none -- nil is not front_to_rear", got[FitOpposedAir])
	}
	if got[FitAirflowUnkown] != 1 {
		t.Errorf("an undeclared box produced %d gap findings, want 1 -- silence here is "+
			"the false confidence this check exists to avoid", got[FitAirflowUnkown])
	}
}

// TestSideClearanceFollowsTheStandard. The derivation that IS legitimate,
// because EIA-310 fixes the equipment width.
func TestSideClearanceFollowsTheStandard(t *testing.T) {
	for _, tc := range []struct{ width, want int }{
		{600, (600 - RackFaceplateMM) / 2},
		{800, (800 - RackFaceplateMM) / 2},
	} {
		if got := SideClearanceMM(tc.width); got != tc.want {
			t.Errorf("SideClearanceMM(%d) = %d, want %d", tc.width, got, tc.want)
		}
	}
	// The numbers the roadmap and the migration both quote, so a change to the
	// constant fails here rather than making three documents quietly wrong.
	if got := SideClearanceMM(600); got < 50 || got > 65 {
		t.Errorf("a 600mm cabinet leaves %dmm a side; every comment in this codebase "+
			"says roughly 55", got)
	}
	if got := SideClearanceMM(800); got < 150 || got > 165 {
		t.Errorf("an 800mm cabinet leaves %dmm a side; every comment says roughly 155", got)
	}
}

// TestKilogramsDoesNotLoseGramsToAFloat.
func TestKilogramsDoesNotLoseGramsToAFloat(t *testing.T) {
	for _, tc := range []struct {
		grams int
		want  string
	}{
		{19500, "19.5 kg"},
		{20000, "20 kg"},
		{4300, "4.3 kg"},
		{600000, "600 kg"},
	} {
		if got := Kilograms(tc.grams); got != tc.want {
			t.Errorf("Kilograms(%d) = %q, want %q", tc.grams, got, tc.want)
		}
	}
}

// mentions is strings.Contains, spelled locally because the package already
// has a `contains` for prefix nodes.
func mentions(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Cabling (WP-C3)
// ---------------------------------------------------------------------------

func face(s string) *string { return &s }

// TestALeadCrossesTheCabinetOnlyWhenTheFacesDiffer.
//
// THE FIRST VERSION OF THIS CHECK COMPARED PORT FACE AGAINST MOUNTED FACE and
// reported every server in the estate — a server is universally racked from the
// front with its ports at the back, and nothing about that is a problem. The
// case below is the one that costs somebody an afternoon: a front-ported panel
// patched to rear-ported hosts.
func TestALeadCrossesTheCabinetOnlyWhenTheFacesDiffer(t *testing.T) {
	rack := RackFit{WidthMM: mm(600)}
	boxes := []FitInput{
		{AssetID: "p", Name: "pp-1", Position: 40, PortFace: face(PortFaceFront), Leads: 1},
		{AssetID: "h", Name: "srv-1", Position: 10, PortFace: face(PortFaceRear), Leads: 1},
	}
	crossing := []CableRun{{
		LinkID: "l1", FromName: "pp-1", ToName: "srv-1",
		FromFace: face(PortFaceFront), ToFace: face(PortFaceRear),
		FromUnit: 40, ToUnit: 10,
	}}
	if got := kinds(CheckCabling(rack, boxes, crossing))[FitPortsWrongFace]; got != 1 {
		t.Errorf("a front-to-rear patch produced %d crossing findings, want 1", got)
	}

	// NEGATIVE CONTROL, and the one the first version failed: two rear-ported
	// boxes patched together cross nothing. This is the ordinary case and must
	// be silent.
	same := []CableRun{{
		LinkID: "l2", FromName: "srv-1", ToName: "srv-2",
		FromFace: face(PortFaceRear), ToFace: face(PortFaceRear),
		FromUnit: 10, ToUnit: 11,
	}}
	if got := kinds(CheckCabling(rack, boxes, same))[FitPortsWrongFace]; got != 0 {
		t.Errorf("two rear-ported boxes patched together produced %d crossing "+
			"findings, want none — that is how every rack in the world is wired", got)
	}

	// `both` accommodates either side, which is the whole reason it is a value.
	panel := []CableRun{{
		LinkID: "l3", FromName: "pp-both", ToName: "srv-1",
		FromFace: face(PortFaceBoth), ToFace: face(PortFaceRear),
		FromUnit: 40, ToUnit: 10,
	}}
	if got := kinds(CheckCabling(rack, boxes, panel))[FitPortsWrongFace]; got != 0 {
		t.Errorf("a both-faced panel produced %d crossing findings, want none", got)
	}
}

// TestLeadDensityNeedsBothTheCountAndTheNarrowCabinet.
func TestLeadDensityNeedsBothTheCountAndTheNarrowCabinet(t *testing.T) {
	dense := []FitInput{{AssetID: "p", Name: "pp-1", PortFace: face(PortFaceFront), Leads: DenseLeadCount}}

	if got := kinds(CheckCabling(RackFit{WidthMM: mm(600)}, dense, nil))[FitLeadDensity]; got != 1 {
		t.Errorf("%d leads in a 600mm cabinet produced %d findings, want 1", DenseLeadCount, got)
	}
	// A wide cabinet has the channel for it.
	if got := kinds(CheckCabling(RackFit{WidthMM: mm(800)}, dense, nil))[FitLeadDensity]; got != 0 {
		t.Errorf("%d leads in an 800mm network cabinet produced %d findings, want none — "+
			"that is the cabinet you buy for it", DenseLeadCount, got)
	}
	// And one lead under the threshold is quiet, so the count matters too.
	light := []FitInput{{AssetID: "p", Name: "pp-1", PortFace: face(PortFaceFront), Leads: DenseLeadCount - 1}}
	if got := kinds(CheckCabling(RackFit{WidthMM: mm(600)}, light, nil))[FitLeadDensity]; got != 0 {
		t.Errorf("%d leads produced %d findings, want none", DenseLeadCount-1, got)
	}
}

// TestACableIsOnlyTooShortWhenItCannotReach.
//
// The allowance is the point: a bare vertical comparison calls every correctly
// specified cable long enough, because a lead does not travel diagonally
// between two ports.
func TestACableIsOnlyTooShortWhenItCannotReach(t *testing.T) {
	run := func(lengthM, from, to int) []CableRun {
		return []CableRun{{
			LinkID: "l", Label: "a to b", FromName: "a", ToName: "b",
			LengthM: lengthM, FromUnit: from, ToUnit: to,
		}}
	}
	// 30 units is 1333mm of drop, plus 500mm of routing = 1833mm. A 1m lead
	// cannot do it; a 2m lead can.
	if got := kinds(CheckCabling(RackFit{}, nil, run(1, 40, 10)))[FitCableTooShort]; got != 1 {
		t.Errorf("a 1m lead across 30 units produced %d findings, want 1", got)
	}
	if got := kinds(CheckCabling(RackFit{}, nil, run(2, 40, 10)))[FitCableTooShort]; got != 0 {
		t.Errorf("a 2m lead across 30 units produced %d findings, want none", got)
	}
	// THE ALLOWANCE IS WHAT THIS CASE TURNS ON, and without it the mutation
	// that deletes it goes unnoticed. 34 units is 1511mm of bare drop and
	// 2011mm once routed, so a 2m lead clears the drop and does NOT reach —
	// which is exactly the cable somebody buys, fits under tension, and blames
	// on the port six months later.
	// Through a variable: `int(34 * RackUnitMM)` is a constant expression and Go
	// refuses to truncate a non-integer constant.
	units := 34.0
	bareDrop := int(units * RackUnitMM)
	routed := bareDrop + CableRouteAllowanceMM
	if got := kinds(CheckCabling(RackFit{}, nil, run(2, 40, 6)))[FitCableTooShort]; got != 1 {
		t.Errorf("a 2m lead across 34 units produced %d findings, want 1. It clears "+
			"the %dmm vertical drop and not the %dmm it needs once routed — if this "+
			"passes, the routing allowance is not being applied", got, bareDrop, routed)
	}

	// Direction must not matter: the same pair the other way round is the same
	// cable.
	if got := kinds(CheckCabling(RackFit{}, nil, run(1, 10, 40)))[FitCableTooShort]; got != 1 {
		t.Errorf("the same lead measured the other way produced %d findings, want 1", got)
	}
	// A cable with no declared length is not a finding: nobody claimed
	// anything, and reporting every unmeasured patch lead would bury the one
	// that cannot reach.
	if got := kinds(CheckCabling(RackFit{}, nil, run(0, 40, 10)))[FitCableTooShort]; got != 0 {
		t.Errorf("an undeclared length produced %d findings, want none", got)
	}
}

// TestAnUndeclaredPortFaceIsCountedOnlyWhenSomethingIsPluggedIn.
func TestAnUndeclaredPortFaceIsCountedOnlyWhenSomethingIsPluggedIn(t *testing.T) {
	cabled := []FitInput{{AssetID: "x", Name: "mystery", Leads: 3}}
	if got := kinds(CheckCabling(RackFit{}, cabled, nil))[FitPortFaceUnknown]; got != 1 {
		t.Errorf("a cabled box with no declared port face produced %d gap findings, want 1", got)
	}
	bare := []FitInput{{AssetID: "x", Name: "mystery"}}
	if got := kinds(CheckCabling(RackFit{}, bare, nil))[FitPortFaceUnknown]; got != 0 {
		t.Errorf("an uncabled box produced %d gap findings, want none — nothing is "+
			"plugged into it, so which way its ports face changes nothing", got)
	}
}
