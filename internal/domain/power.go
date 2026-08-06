// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"math"
	"strings"
	"time"
)

// The power chain: a panel distributes to feeds, a feed carries inputs, an
// asset draws through its inputs. See migration 00023 for why it stops there.

// Phases a supply can be. A behavioural set rather than a vocabulary: the value
// selects a branch in the capacity arithmetic below, so it is a Go constant set
// with a matching CHECK.
const (
	PhaseSingle = "single"
	PhaseThree  = "three"
)

// Phases is the allowed set.
var Phases = []string{PhaseSingle, PhaseThree}

// PowerLifecycles reuses the estate-wide set.
var PowerLifecycles = []string{
	LifecyclePlanned, LifecycleActive, LifecycleMaintenance,
	LifecycleDeprecated, LifecycleRetired,
}

// Rating is a supply's electrical rating. Every field is optional, because an
// estate routinely knows a panel exists long before anybody has read its rating
// off the door -- and "not recorded" must stay distinguishable from zero.
type Rating struct {
	Voltage  *int    `db:"voltage"`
	Amperage *int    `db:"amperage"`
	Phase    *string `db:"phase"`
}

// CapacityVA is the rating in volt-amps, and false when it cannot be computed.
//
// VOLT-AMPS, NOT WATTS, and the distinction is not pedantry. Capacity is
// genuinely V×A; converting to watts needs a power factor this system does not
// know and would be inventing. Draw is declared in the same unit for the same
// reason -- a nameplate figure treated as VA compares like with like, and a
// comparison between a VA capacity and a watt draw is wrong by the power factor
// in the direction that makes a feed look safer than it is.
//
// Three-phase capacity is V×A×√3 for a line-to-line voltage, which is how
// three-phase feeds are labelled. An unstated phase is treated as single,
// because that is the conservative reading: it reports LESS capacity, so a feed
// errs towards looking full rather than towards looking safe.
func (r Rating) CapacityVA() (int, bool) {
	if r.Voltage == nil || r.Amperage == nil || *r.Voltage <= 0 || *r.Amperage <= 0 {
		return 0, false
	}
	va := float64(*r.Voltage) * float64(*r.Amperage)
	if r.Phase != nil && *r.Phase == PhaseThree {
		va *= math.Sqrt(3)
	}
	return int(va), true
}

// checkRating validates the three fields together.
func checkRating(ve *ValidationError, r *Rating) {
	if r.Voltage != nil && *r.Voltage <= 0 {
		ve.Add("voltage", "must be more than zero, or left empty if it is not recorded")
	}
	if r.Amperage != nil && *r.Amperage <= 0 {
		ve.Add("amperage", "must be more than zero, or left empty if it is not recorded")
	}
	if r.Phase != nil {
		phase := strings.TrimSpace(*r.Phase)
		if phase == "" {
			r.Phase = nil
		} else if !containsString(Phases, phase) {
			ve.Add("phase", "must be one of %s", strings.Join(Phases, ", "))
		} else {
			r.Phase = &phase
		}
	}
	// A rating with an amperage and no voltage cannot produce a capacity, and a
	// feed whose capacity cannot be computed reports "not recorded" rather than
	// a number. Said here so somebody filling a form learns it at the form.
	if r.Amperage != nil && r.Voltage == nil {
		ve.Add("voltage", "an amperage without a voltage cannot give a capacity; record both or neither")
	}
}

// PowerPanel is a distribution board.
type PowerPanel struct {
	ID string `db:"id"`
	// SiteID is an asset -- a site, a room or a rack. The containment tree
	// already knows where things are; a second location model would be a second
	// answer to the same question.
	SiteID string `db:"site_id"`
	// SourceID is what feeds this board: a UPS, a transfer switch, a generator.
	// Optional, and it stays optional -- an estate that has recorded its boards
	// but not yet what is behind them is the normal starting point. The findings
	// report counts how many are in that state rather than guessing.
	SourceID *string `db:"source_id"`
	Name     string  `db:"name"`
	Rating
	Notes      *string `db:"notes"`
	Lifecycle  string  `db:"lifecycle"`
	CreatedAt  string  `db:"created_at"`
	UpdatedAt  string  `db:"updated_at"`
	RowVersion int     `db:"row_version"`
}

// PowerPanelSpec is what a caller supplies.
type PowerPanelSpec struct {
	SiteID   string
	SourceID *string
	Name     string
	Rating
	Notes     *string
	Lifecycle string
}

// NewPowerPanel validates and constructs.
func NewPowerPanel(id string, spec PowerPanelSpec, now time.Time) (*PowerPanel, error) {
	ve := &ValidationError{}
	site := checkRequired(ve, "site_id", spec.SiteID)
	name := checkRequired(ve, "name", spec.Name)
	checkRating(ve, &spec.Rating)
	lifecycle := defaultedPowerLifecycle(ve, spec.Lifecycle)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &PowerPanel{
		ID: id, SiteID: site, SourceID: blankToNil(spec.SourceID), Name: name, Rating: spec.Rating,
		Notes: blankToNil(spec.Notes), Lifecycle: lifecycle,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// Validate re-checks a panel after field updates.
func (p *PowerPanel) Validate() error {
	ve := &ValidationError{}
	p.SiteID = checkRequired(ve, "site_id", p.SiteID)
	p.Name = checkRequired(ve, "name", p.Name)
	p.SourceID = blankToNil(p.SourceID)
	checkRating(ve, &p.Rating)
	p.Notes = blankToNil(p.Notes)
	checkEnum(ve, "lifecycle", p.Lifecycle, PowerLifecycles)
	return ve.OrNil()
}

// PowerFeed is a circuit off a panel. This is the thing that fails.
type PowerFeed struct {
	ID      string `db:"id"`
	PanelID string `db:"panel_id"`
	Name    string `db:"name"`
	Rating
	// MaxUtilisation is the percent of the rating a continuous load may occupy.
	MaxUtilisation int     `db:"max_utilisation"`
	Notes          *string `db:"notes"`
	Lifecycle      string  `db:"lifecycle"`
	CreatedAt      string  `db:"created_at"`
	UpdatedAt      string  `db:"updated_at"`
	RowVersion     int     `db:"row_version"`
}

// DefaultMaxUtilisation is the common derating for a continuous load.
const DefaultMaxUtilisation = 80

// UsableVA is the capacity a continuous load may occupy, after derating.
func (f PowerFeed) UsableVA() (int, bool) {
	capacity, ok := f.CapacityVA()
	if !ok {
		return 0, false
	}
	return capacity * f.MaxUtilisation / 100, true
}

// PowerFeedSpec is what a caller supplies.
type PowerFeedSpec struct {
	PanelID string
	Name    string
	Rating
	MaxUtilisation int
	Notes          *string
	Lifecycle      string
}

// NewPowerFeed validates and constructs.
func NewPowerFeed(id string, spec PowerFeedSpec, now time.Time) (*PowerFeed, error) {
	ve := &ValidationError{}
	panel := checkRequired(ve, "panel_id", spec.PanelID)
	name := checkRequired(ve, "name", spec.Name)
	checkRating(ve, &spec.Rating)
	if spec.MaxUtilisation == 0 {
		spec.MaxUtilisation = DefaultMaxUtilisation
	}
	checkUtilisation(ve, spec.MaxUtilisation)
	lifecycle := defaultedPowerLifecycle(ve, spec.Lifecycle)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &PowerFeed{
		ID: id, PanelID: panel, Name: name, Rating: spec.Rating,
		MaxUtilisation: spec.MaxUtilisation,
		Notes:          blankToNil(spec.Notes), Lifecycle: lifecycle,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// Validate re-checks a feed after field updates.
func (f *PowerFeed) Validate() error {
	ve := &ValidationError{}
	f.PanelID = checkRequired(ve, "panel_id", f.PanelID)
	f.Name = checkRequired(ve, "name", f.Name)
	checkRating(ve, &f.Rating)
	checkUtilisation(ve, f.MaxUtilisation)
	f.Notes = blankToNil(f.Notes)
	checkEnum(ve, "lifecycle", f.Lifecycle, PowerLifecycles)
	return ve.OrNil()
}

func checkUtilisation(ve *ValidationError, pct int) {
	if pct <= 0 || pct > 100 {
		ve.Add("max_utilisation", "is a percentage: between 1 and 100")
	}
}

// PowerInput is where an asset takes power from.
type PowerInput struct {
	ID      string `db:"id"`
	AssetID string `db:"asset_id"`
	FeedID  string `db:"feed_id"`
	Name    string `db:"name"`
	// DrawVA is DECLARED: a nameplate or allocated figure somebody typed.
	// Nothing observes it. Nil means nobody has recorded one, which the
	// utilisation report says rather than treating as zero.
	DrawVA     *int    `db:"draw_va"`
	Notes      *string `db:"notes"`
	Lifecycle  string  `db:"lifecycle"`
	CreatedAt  string  `db:"created_at"`
	UpdatedAt  string  `db:"updated_at"`
	RowVersion int     `db:"row_version"`
}

// PowerInputSpec is what a caller supplies.
type PowerInputSpec struct {
	AssetID   string
	FeedID    string
	Name      string
	DrawVA    *int
	Notes     *string
	Lifecycle string
}

// NewPowerInput validates and constructs.
func NewPowerInput(id string, spec PowerInputSpec, now time.Time) (*PowerInput, error) {
	ve := &ValidationError{}
	asset := checkRequired(ve, "asset_id", spec.AssetID)
	feed := checkRequired(ve, "feed_id", spec.FeedID)
	name := checkRequired(ve, "name", spec.Name)
	checkDraw(ve, spec.DrawVA)
	lifecycle := defaultedPowerLifecycle(ve, spec.Lifecycle)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &PowerInput{
		ID: id, AssetID: asset, FeedID: feed, Name: name, DrawVA: spec.DrawVA,
		Notes: blankToNil(spec.Notes), Lifecycle: lifecycle,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// Validate re-checks an input after field updates.
func (i *PowerInput) Validate() error {
	ve := &ValidationError{}
	i.AssetID = checkRequired(ve, "asset_id", i.AssetID)
	i.FeedID = checkRequired(ve, "feed_id", i.FeedID)
	i.Name = checkRequired(ve, "name", i.Name)
	checkDraw(ve, i.DrawVA)
	i.Notes = blankToNil(i.Notes)
	checkEnum(ve, "lifecycle", i.Lifecycle, PowerLifecycles)
	return ve.OrNil()
}

// checkDraw bounds a declared draw.
//
// Bounded above as well as below. A nameplate figure is typed by a person, and
// 12000 for 1200 is a keystroke that does not look wrong in a form and does make
// a feed report as catastrophically over-allocated -- which then teaches
// everybody to ignore the finding.
func checkDraw(ve *ValidationError, draw *int) {
	if draw == nil {
		return
	}
	switch {
	case *draw < 0:
		ve.Add("draw_va", "cannot be negative")
	case *draw > 100_000:
		ve.Add("draw_va", "is larger than any single input; check the units — this is volt-amps")
	}
}

func defaultedPowerLifecycle(ve *ValidationError, lifecycle string) string {
	lifecycle = strings.TrimSpace(lifecycle)
	if lifecycle == "" {
		lifecycle = LifecycleActive
	}
	checkEnum(ve, "lifecycle", lifecycle, PowerLifecycles)
	return lifecycle
}

// ---------- what sits above a panel ----------

// Kinds of supply. Behavioural, not a vocabulary: the value decides whether two
// inputs converging here is a fault or the design, so it is a Go constant set
// with a matching CHECK.
const (
	SourceUtility        = "utility"
	SourceGenerator      = "generator"
	SourceTransferSwitch = "transfer_switch"
	SourceUPS            = "ups"
)

// SourceKinds is the allowed set, roughly upstream-first.
var SourceKinds = []string{SourceUtility, SourceGenerator, SourceTransferSwitch, SourceUPS}

// SharingIsAFault reports whether two inputs converging on a source of this
// kind means they die together.
//
// THE DISTINCTION THE WHOLE SUPPLY LAYER EXISTS FOR. Two feeds meeting at a UPS
// or a transfer switch fail at the same instant: that is not redundancy, it is
// one point of failure with two cables. Two feeds meeting only at the GENERATOR
// is the ordinary 2N design -- the generator is what makes a utility failure
// survivable, and reporting it as a single point of failure reports the safety
// measure as the hazard. The utility is the same: everything shares it, which is
// precisely why there are UPSes and a generator below it.
//
// It is still worth SAYING where they converge. "These diverge only above the
// generator" is a true and useful sentence; it is just not an alarm.
func SharingIsAFault(kind string) bool {
	switch kind {
	case SourceUPS, SourceTransferSwitch:
		return true
	default:
		return false
	}
}

// PowerSource is a supply: utility, generator, transfer switch or UPS.
type PowerSource struct {
	ID string `db:"id"`
	// ParentID is what feeds this one; nil is the top of a chain.
	ParentID *string `db:"parent_id"`
	SiteID   string  `db:"site_id"`
	// AssetID is the same thing as an inventory item, when somebody has
	// catalogued it -- which is how a UPS's battery end-of-life reaches the
	// expiry report.
	AssetID    *string `db:"asset_id"`
	Name       string  `db:"name"`
	Kind       string  `db:"kind"`
	Notes      *string `db:"notes"`
	Lifecycle  string  `db:"lifecycle"`
	CreatedAt  string  `db:"created_at"`
	UpdatedAt  string  `db:"updated_at"`
	RowVersion int     `db:"row_version"`
}

// PowerSourceSpec is what a caller supplies.
type PowerSourceSpec struct {
	ParentID  *string
	SiteID    string
	AssetID   *string
	Name      string
	Kind      string
	Notes     *string
	Lifecycle string
}

// NewPowerSource validates and constructs.
func NewPowerSource(id string, spec PowerSourceSpec, now time.Time) (*PowerSource, error) {
	ve := &ValidationError{}
	site := checkRequired(ve, "site_id", spec.SiteID)
	name := checkRequired(ve, "name", spec.Name)
	kind := strings.TrimSpace(spec.Kind)
	if !containsString(SourceKinds, kind) {
		ve.Add("kind", "must be one of %s", strings.Join(SourceKinds, ", "))
	}
	parent := blankToNil(spec.ParentID)
	if parent != nil && *parent == id {
		ve.Add("parent_id", "a supply cannot feed itself")
	}
	lifecycle := defaultedPowerLifecycle(ve, spec.Lifecycle)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &PowerSource{
		ID: id, ParentID: parent, SiteID: site, AssetID: blankToNil(spec.AssetID),
		Name: name, Kind: kind, Notes: blankToNil(spec.Notes), Lifecycle: lifecycle,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// Validate re-checks a source after field updates.
func (p *PowerSource) Validate() error {
	ve := &ValidationError{}
	p.SiteID = checkRequired(ve, "site_id", p.SiteID)
	p.Name = checkRequired(ve, "name", p.Name)
	if !containsString(SourceKinds, p.Kind) {
		ve.Add("kind", "must be one of %s", strings.Join(SourceKinds, ", "))
	}
	p.ParentID = blankToNil(p.ParentID)
	if p.ParentID != nil && *p.ParentID == p.ID {
		ve.Add("parent_id", "a supply cannot feed itself")
	}
	p.AssetID = blankToNil(p.AssetID)
	p.Notes = blankToNil(p.Notes)
	checkEnum(ve, "lifecycle", p.Lifecycle, PowerLifecycles)
	return ve.OrNil()
}
