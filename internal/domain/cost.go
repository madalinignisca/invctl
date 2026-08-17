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

// What things cost, and the one rule that governs the whole feature:
//
// COST ATTACHES TO WHAT IS BILLED, AND DOES NOT INHERIT. You pay for the
// hypervisor, not for each VM inside it; for the Vault licence, not for each
// replica. A guest's cost is not inherited from its host, it is a SHARE of it,
// and three guests each inheriting the host's monthly figure report three times
// one box. That double-count is silent, compounds, and is discovered by the
// first person to reconcile the page against an invoice -- after which nobody
// believes any number on it again.
//
// So nothing here divides, spreads or apportions. Splitting a shared thing is
// ALLOCATION; it needs a driver (vCPU, RAM, equal shares) and a policy, every
// choice is arguable, and when it arrives it will be a share somebody DECLARED
// on a `uses` link rather than a number this package invented.

// Cost periods. A CHECK constraint with a matching constant set rather than a
// lookup table, because these are BEHAVIOURAL: MonthlyMinor divides a yearly
// figure by twelve and excludes a one-off entirely. A period added by INSERT
// would be storable and silently uncomputable, which is exactly the line
// migration 00004 drew between service_kind and availability.
const (
	// CostOnce is paid once and never again. It is NEVER part of a run rate.
	CostOnce = "once"
	// CostMonthly is the natural unit of a run rate.
	CostMonthly = "monthly"
	// CostYearly is divided by twelve for the run rate, in Go, because integer
	// division rounds differently across the two engines and because how to
	// round money is a decision rather than an accident.
	CostYearly = "yearly"
)

// CostPeriods is the Go side of the three period CHECK constraints.
var CostPeriods = []string{CostOnce, CostMonthly, CostYearly}

// What a cost line divides across (migration 00047, COST-ATTRIBUTION.md §5.6).
//
// BEHAVIOURAL, like the periods above and unlike the kinds: each value selects
// a different arithmetic, so it is a Go constant set with a matching CHECK
// rather than a lookup table somebody can add a row to. A fourth shape would
// need code, which is the test of whether something belongs in a vocabulary.
const (
	// CostUniversal: every consumer benefits, so it divides across the whole
	// capacity. Hardware, power, rack space, the hypervisor platform itself.
	CostUniversal = "universal"
	// CostConditional: only the named consumers benefit, in proportion to what
	// they hold. A per-core operating-system or database licence.
	CostConditional = "conditional"
	// CostPerConsumer: only the named consumers, EQUALLY, per head.
	//
	// THE DISTINCTION FROM CONDITIONAL IS NOT PEDANTRY. A backup product
	// licensed per virtual machine costs the same for a 64 GB machine as for a
	// 2 GB one. Dividing it by capacity share would charge the large one
	// thirty times over for a single licence and the small one almost nothing,
	// and the total would still reconcile -- which is exactly the sort of wrong
	// number nobody catches.
	CostPerConsumer = "per_consumer"
)

// CostScopes is the Go side of the asset_cost.applies_to CHECK constraint.
var CostScopes = []string{CostUniversal, CostConditional, CostPerConsumer}

// NeedsConsumers reports whether a scope is meaningless without a named set.
//
// A conditional line naming nobody is not a line that applies to everybody --
// it is a line whose scope somebody started declaring and did not finish, and
// it is reported as a gap rather than quietly falling back to universal. The
// fallback would be the laundering this whole file guards against: a default
// wearing the clothes of a declaration.
func NeedsConsumers(scope string) bool {
	return scope == CostConditional || scope == CostPerConsumer
}

// CostPeriodDescription is the help text for each, engine-defined the same way
// the availability policies are: the code branches on these values, so what they
// mean is a property of this package rather than a row somebody can reword.
func CostPeriodDescription(period string) string {
	switch period {
	case CostOnce:
		return "Paid once. Counted as capital committed and never as a run rate — " +
			"a switch bought two years ago is not part of what this month costs."
	case CostMonthly:
		return "Paid every month. The natural unit of a run rate."
	case CostYearly:
		return "Paid every year. Divided by twelve for the monthly run rate, so a " +
			"yearly support contract and a monthly hosting bill can be added together."
	}
	return ""
}

// Cost is one line of spend against one thing. The entity it belongs to is not
// a field here: it is the parent table's foreign key, and the three parent
// tables are separate so that a typo cannot invent a row that inflates a total
// and belongs to nothing.
type Cost struct {
	ID   string `db:"id"`
	Kind string `db:"kind"`
	// Period decides how AmountMinor enters a total. See MonthlyMinor.
	Period string `db:"period"`
	// AmountMinor is minor units -- cents -- as an integer. Never a float:
	// money in binary floating point is wrong in ways that only show up once
	// the numbers are large enough to matter.
	AmountMinor int64   `db:"amount_minor"`
	Note        *string `db:"note"`
	// ValidFrom and ValidUntil bound when this line applies. ValidUntil nil is
	// open-ended, which is the ordinary case. The window exists from the first
	// release even though only "current" is queried, because without it a
	// renewal at a new price overwrites its predecessor and the history is gone.
	//
	// FOR A ONE-OFF, ValidFrom IS THE ACQUISITION DATE -- the day it was paid.
	// That is what amortisation measures from, and it is why no `acquired_on`
	// column exists: a second field holding the same fact would disagree with
	// this one the first time somebody edited either.
	ValidFrom  string  `db:"valid_from"`
	ValidUntil *string `db:"valid_until"`
	// AppliesTo is which consumers this line divides across (§5.6). Only an
	// asset cost carries a meaningful value; the other three attach to
	// something that is already the unit of attribution.
	AppliesTo string `db:"applies_to"`
	// ProviderID is who invoiced it (WP-J6). Nil is ordinary: most estates fill
	// this in slowly, and the supplier report counts what it could not
	// attribute rather than totalling over the half that was labelled.
	//
	// ON THE LINE RATHER THAN THE ASSET, because one server carries hardware
	// from a reseller, support from the manufacturer and a licence from a third
	// party -- three suppliers and three price histories against one box.
	ProviderID *string `db:"provider_id"`
	Lifecycle  string  `db:"lifecycle"`
	CreatedAt  string  `db:"created_at"`
	UpdatedAt  string  `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// CostSpec is what a caller supplies.
type CostSpec struct {
	Kind        string
	Period      string
	AmountMinor int64
	Note        *string
	ValidFrom   *string
	ValidUntil  *string
	// AppliesTo defaults to universal when blank, which is what every cost in
	// this system meant before the column existed.
	AppliesTo string
	// ProviderID is who invoiced it, or nil.
	ProviderID *string
}

// NewCost validates and constructs a cost line.
func NewCost(id string, spec CostSpec, now time.Time) (*Cost, error) {
	ve := &ValidationError{}
	c := &Cost{
		ID:          id,
		Kind:        checkVocabulary(ve, "kind", spec.Kind),
		Period:      strings.ToLower(strings.TrimSpace(spec.Period)),
		AmountMinor: spec.AmountMinor,
		Note:        spec.Note,
		AppliesTo:   strings.ToLower(strings.TrimSpace(spec.AppliesTo)),
		ProviderID:  blankToNil(spec.ProviderID),
		Lifecycle:   LifecycleActive,
		CreatedAt:   FormatTime(now),
		UpdatedAt:   FormatTime(now),
	}
	checkEnum(ve, "period", c.Period, CostPeriods)
	// Blank is universal, which is what every line in this system meant before
	// the column existed. Anything else must be one of the three.
	if c.AppliesTo == "" {
		c.AppliesTo = CostUniversal
	}
	checkEnum(ve, "applies_to", c.AppliesTo, CostScopes)

	// A negative cost is a credit, and a credit is an accounting concept this
	// system has no way to report honestly -- it would simply subtract from a
	// total and make an estate look cheaper than it is.
	if c.AmountMinor < 0 {
		ve.Add("amount", "cannot be negative")
	}

	// An undated line defaults to starting today rather than being rejected.
	// Most costs are entered when they begin, and demanding a date for the
	// common case buys nothing.
	from := checkDate(ve, "valid_from", spec.ValidFrom)
	if from == nil {
		today := FormatDate(now)
		from = &today
	}
	c.ValidFrom = *from
	c.ValidUntil = checkDate(ve, "valid_until", spec.ValidUntil)

	// A window that closes before it opens never matches anything, so the cost
	// vanishes from every total with no visible symptom.
	if c.ValidUntil != nil && *c.ValidUntil < c.ValidFrom {
		ve.Add("valid_until", "cannot be before the date it starts")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate re-checks a cost after field updates.
func (c *Cost) Validate() error {
	ve := &ValidationError{}
	c.Kind = checkVocabulary(ve, "kind", c.Kind)
	checkEnum(ve, "period", c.Period, CostPeriods)
	checkEnum(ve, "lifecycle", c.Lifecycle, []string{LifecycleActive, LifecycleRetired})
	if c.AppliesTo == "" {
		c.AppliesTo = CostUniversal
	}
	checkEnum(ve, "applies_to", c.AppliesTo, CostScopes)
	if c.AmountMinor < 0 {
		ve.Add("amount", "cannot be negative")
	}
	from := checkDate(ve, "valid_from", &c.ValidFrom)
	if from == nil {
		ve.Add("valid_from", "is required")
	} else {
		c.ValidFrom = *from
	}
	c.ValidUntil = checkDate(ve, "valid_until", c.ValidUntil)
	if c.ValidUntil != nil && *c.ValidUntil < c.ValidFrom {
		ve.Add("valid_until", "cannot be before the date it starts")
	}
	return ve.OrNil()
}

// AppliesOn reports whether this line is in force on a given date.
func (c *Cost) AppliesOn(date string) bool {
	if c.Lifecycle == LifecycleRetired {
		return false
	}
	if date < c.ValidFrom {
		return false
	}
	return c.ValidUntil == nil || date <= *c.ValidUntil
}

// MonthlyMinor is this line's contribution to a monthly run rate.
//
// A one-off contributes NOTHING, and that is the single most important line in
// this file. Amortising it here would fold capital into a run rate silently;
// amortisation is a separate, labelled figure or it is a lie.
//
// A yearly figure is divided by twelve with rounding to nearest, so twelve
// months of a yearly contract differ from the contract by at most six cents
// rather than by eleven. Truncation would make every annualised total read
// slightly low, always in the same direction, which is the kind of error that
// survives review because it looks like rounding.
func (c *Cost) MonthlyMinor() int64 {
	switch c.Period {
	case CostMonthly:
		return c.AmountMinor
	case CostYearly:
		return divRound(c.AmountMinor, 12)
	default:
		return 0
	}
}

// CapitalMinor is this line's contribution to capital committed: the one-offs,
// and only the one-offs.
func (c *Cost) CapitalMinor() int64 {
	if c.Period == CostOnce {
		return c.AmountMinor
	}
	return 0
}

// AnnualMinor is this line's contribution to a year of run rate.
//
// Accumulated in its own right rather than computed as twelve times the monthly,
// and the difference is visible to a reader: a EUR 940 yearly contract has a
// monthly share of 78.33, and twelve of those is 939.96. Showing "per year
// EUR 939.96" next to a contract everybody knows costs 940 reads as a bug and
// invites somebody to check the arithmetic instead of the estate.
//
// So the YEAR is exact and the MONTH is the approximation, which is the right
// way round: a yearly figure can always be stated exactly, a monthly share of it
// sometimes cannot.
func (c *Cost) AnnualMinor() int64 {
	switch c.Period {
	case CostMonthly:
		return c.AmountMinor * 12
	case CostYearly:
		return c.AmountMinor
	default:
		return 0
	}
}

// AmortisedMonthlyMinor spreads a one-off over the life of the thing it bought:
// straight line from the day it was paid to the day that thing stops being
// supportable.
//
// The second return says whether the line CAN be amortised at all, which is a
// different question from how much it contributes this month. A fully
// depreciated switch is amortisable and contributes nothing; a switch with no
// EOL date is not amortisable, and the page has to say so rather than quietly
// leaving it out of a figure labelled "the capital, spread".
//
// Only a one-off amortises. A monthly hosting bill is already a run rate, and
// spreading it again would count it twice.
//
// STRAIGHT LINE, TO THE MONTH. Declining balance, salvage values and partial
// months are accounting rather than inventory, and this is not the system of
// record for any of them -- it is a number that makes a run rate honest, and one
// somebody can check on the back of an envelope.
func (c *Cost) AmortisedMonthlyMinor(eolDate *string, on string) (int64, bool) {
	if c.Period != CostOnce || !c.AppliesOn(on) {
		return 0, false
	}
	if eolDate == nil || *eolDate == "" {
		return 0, false
	}
	start, err := ParseDate(c.ValidFrom)
	if err != nil {
		return 0, false
	}
	end, err := ParseDate(*eolDate)
	if err != nil {
		return 0, false
	}
	months := monthsBetween(start, end)
	if months <= 0 {
		// Bought after it was already unsupportable, or a life under a month.
		// Either way it is an expense rather than capital to spread, and
		// claiming otherwise would divide by something near zero.
		return 0, false
	}

	today, err := ParseDate(on)
	if err != nil {
		return 0, false
	}
	// Outside its life it contributes nothing, but it is still amortisable --
	// the counter that tells a reader how much of the capital this figure
	// covers must not lose it.
	if today.Before(start) || !today.Before(end) {
		return 0, true
	}
	return divRound(c.AmountMinor, int64(months)), true
}

// monthsBetween counts whole months from a to b, so a purchase on the 20th and
// an end-of-support on the 5th does not round up to a month that has not
// happened.
func monthsBetween(a, b time.Time) int {
	months := (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
	if b.Day() < a.Day() {
		months--
	}
	return months
}

// CostTotals is the answer, and it is deliberately three numbers.
//
// A single "this project costs X" is where a cost report dies, because somebody
// always finds the switch bought once inside a monthly figure and is right to
// stop reading. Capital, run rate and annual answer three different questions
// and are never added to each other.
type CostTotals struct {
	// CapitalMinor: what has been spent once. Sunk, not a rate.
	CapitalMinor int64
	// MonthlyMinor: what it costs to keep running, per month. The only figure
	// here that can carry a rounding error, and it carries all of it.
	MonthlyMinor int64
	// AnnualMinor: the same run rate over a year, accumulated exactly rather
	// than as twelve times the monthly. See Cost.AnnualMinor.
	AnnualMinor int64

	// AmortisedMonthlyMinor is the capital spread over the life of what it
	// bought. NEVER added into MonthlyMinor: a run rate is what leaves the bank
	// this month, and amortisation is an accounting view of money that already
	// did. Reported beside it, added only where a page says it is adding them.
	AmortisedMonthlyMinor int64
	// Amortisable and Unamortisable count the one-off lines that could and
	// could not be spread -- the second because the thing they bought carries
	// no end-of-support date. Without the pair, a figure covering two of nine
	// purchases looks like a figure covering all nine.
	Amortisable   int
	Unamortisable int

	// Lines is how many cost lines produced these numbers. A total over zero
	// lines and a total of zero are different answers.
	Lines int
}

// TotalMonthlyMinor is the run rate with amortisation folded in: what owning
// this actually costs per month once the hardware is paid for over its life.
// A page that shows it must say it is doing so.
func (t CostTotals) TotalMonthlyMinor() int64 {
	return t.MonthlyMinor + t.AmortisedMonthlyMinor
}

// HasAmortisation reports whether there is anything to say about spreading.
func (t CostTotals) HasAmortisation() bool {
	return t.Amortisable > 0 || t.Unamortisable > 0
}

// FullyWrittenOff reports that every purchase which could be spread has been:
// the capital is spent and the life it was spread over has ended.
//
// Worth its own sentence rather than rendering "adds EUR 0.00 a month", which is
// true and reads like a missing number. It is also the more interesting state:
// kit that costs nothing per month is usually kit that nobody supports either.
func (t CostTotals) FullyWrittenOff() bool {
	return t.Amortisable > 0 && t.AmortisedMonthlyMinor == 0
}

// IsZero reports whether anything at all was recorded.
func (t CostTotals) IsZero() bool { return t.Lines == 0 }

// Add folds one line into the totals.
//
// eolDate is the end-of-support of the THING the line is attached to, and `on`
// is the date the totals are for. Both are parameters rather than a second
// method to call afterwards: a two-call API where the second call is optional
// is a two-call API where the second call gets forgotten, and the symptom would
// be an amortisation figure that is silently zero.
func (t *CostTotals) Add(c *Cost, eolDate *string, on string) {
	t.CapitalMinor += c.CapitalMinor()
	t.MonthlyMinor += c.MonthlyMinor()
	t.AnnualMinor += c.AnnualMinor()
	t.Lines++

	if c.Period != CostOnce {
		return
	}
	amortised, canAmortise := c.AmortisedMonthlyMinor(eolDate, on)
	if !canAmortise {
		t.Unamortisable++
		return
	}
	t.Amortisable++
	t.AmortisedMonthlyMinor += amortised
}

// Plus merges two sets of totals.
func (t CostTotals) Plus(other CostTotals) CostTotals {
	return CostTotals{
		CapitalMinor:          t.CapitalMinor + other.CapitalMinor,
		MonthlyMinor:          t.MonthlyMinor + other.MonthlyMinor,
		AnnualMinor:           t.AnnualMinor + other.AnnualMinor,
		AmortisedMonthlyMinor: t.AmortisedMonthlyMinor + other.AmortisedMonthlyMinor,
		Amortisable:           t.Amortisable + other.Amortisable,
		Unamortisable:         t.Unamortisable + other.Unamortisable,
		Lines:                 t.Lines + other.Lines,
	}
}

// divRound divides rounding half away from zero. Amounts are non-negative here,
// but the negative branch is written rather than assumed: a helper that is only
// correct for the inputs its first caller happened to pass is a trap for the
// second.
func divRound(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	if n >= 0 {
		return (n + d/2) / d
	}
	return -((-n + d/2) / d)
}
