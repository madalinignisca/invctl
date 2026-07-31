package domain

import (
	"testing"
	"time"
)

var costNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

func cost(t *testing.T, period string, amount int64) *Cost {
	t.Helper()
	c, err := NewCost("c1", CostSpec{Kind: "support", Period: period, AmountMinor: amount}, costNow)
	if err != nil {
		t.Fatalf("building the cost: %v", err)
	}
	return c
}

// TestAOneOffIsNeverPartOfARunRate is the single most important assertion in
// the cost feature. Folding capital into a monthly figure is silent, plausible
// and destroys the number's credibility the moment somebody traces it.
func TestAOneOffIsNeverPartOfARunRate(t *testing.T) {
	once := cost(t, CostOnce, 840000)

	if got := once.MonthlyMinor(); got != 0 {
		t.Errorf("a one-off contributed %d to the monthly run rate, want 0", got)
	}
	if got := once.CapitalMinor(); got != 840000 {
		t.Errorf("capital = %d, want 840000", got)
	}

	// And the converse: a recurring line is never capital, or "what have we
	// spent" would grow every month without anybody buying anything.
	monthly := cost(t, CostMonthly, 38000)
	if got := monthly.CapitalMinor(); got != 0 {
		t.Errorf("a monthly cost contributed %d to capital, want 0", got)
	}
}

func TestYearlyIsTwelfthedWithRoundingToNearest(t *testing.T) {
	cases := []struct {
		name        string
		yearlyMinor int64
		wantMonthly int64
	}{
		{"exact", 120000, 10000},
		// 94000/12 = 7833.33 -> 7833. Truncation and rounding agree here.
		{"rounds down", 94000, 7833},
		// 100000/12 = 8333.33... the remainder is 4, and half of 12 is 6, so
		// it stays down.
		{"just below the half", 100000, 8333},
		// 110000/12 = 9166.67 -> 9167. TRUNCATION WOULD GIVE 9166, and every
		// annualised total in the estate would read low, always in the same
		// direction -- the kind of error that survives review because it looks
		// like rounding.
		{"rounds up", 110000, 9167},
		{"zero", 0, 0},
		{"one cent a year", 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cost(t, CostYearly, c.yearlyMinor).MonthlyMinor()
			if got != c.wantMonthly {
				t.Errorf("MonthlyMinor = %d, want %d", got, c.wantMonthly)
			}
		})
	}
}

func TestTotalsKeepTheThreeNumbersApart(t *testing.T) {
	var totals CostTotals
	totals.Add(cost(t, CostOnce, 840000), nil, "2026-07-31")   // capital only
	totals.Add(cost(t, CostMonthly, 38000), nil, "2026-07-31") // 380.00/month
	totals.Add(cost(t, CostYearly, 120000), nil, "2026-07-31") // 100.00/month
	totals.Add(cost(t, CostOnce, 520000), nil, "2026-07-31")   // capital only

	if totals.CapitalMinor != 1360000 {
		t.Errorf("capital = %d, want 1360000", totals.CapitalMinor)
	}
	if totals.MonthlyMinor != 48000 {
		t.Errorf("monthly = %d, want 48000", totals.MonthlyMinor)
	}
	// Annual is accumulated EXACTLY: 38000*12 + 120000 = 576000. Twelve times
	// the rounded monthly would give 576000 here too, so the case that
	// distinguishes them is asserted separately below.
	if totals.AnnualMinor != 576000 {
		t.Errorf("annual = %d, want 576000", totals.AnnualMinor)
	}
	if totals.Lines != 4 {
		t.Errorf("lines = %d, want 4", totals.Lines)
	}

	// A total of zero over some lines and a total over no lines are different
	// answers, and the page says different things about them.
	if totals.IsZero() {
		t.Error("totals over four lines reported themselves as empty")
	}
	if !(CostTotals{}).IsZero() {
		t.Error("empty totals did not report themselves as empty")
	}
}

func TestAppliesOnRespectsTheWindowAndTheLifecycle(t *testing.T) {
	from, until := "2026-01-01", "2026-12-31"
	c, err := NewCost("c1", CostSpec{
		Kind: "support", Period: CostYearly, AmountMinor: 100,
		ValidFrom: &from, ValidUntil: &until,
	}, costNow)
	if err != nil {
		t.Fatalf("building the cost: %v", err)
	}

	cases := []struct {
		date string
		want bool
	}{
		{"2025-12-31", false},
		{"2026-01-01", true}, // the first day is inside
		{"2026-07-31", true},
		{"2026-12-31", true}, // and so is the last
		{"2027-01-01", false},
	}
	for _, tc := range cases {
		if got := c.AppliesOn(tc.date); got != tc.want {
			t.Errorf("AppliesOn(%s) = %v, want %v", tc.date, got, tc.want)
		}
	}

	// A retired line applies on no date at all, whatever its window says.
	c.Lifecycle = LifecycleRetired
	if c.AppliesOn("2026-07-31") {
		t.Error("a retired cost line still applied")
	}

	// An open-ended line applies forever forward.
	open, err := NewCost("c2", CostSpec{
		Kind: "support", Period: CostMonthly, AmountMinor: 100, ValidFrom: &from,
	}, costNow)
	if err != nil {
		t.Fatalf("building the open cost: %v", err)
	}
	if !open.AppliesOn("2099-01-01") {
		t.Error("an open-ended cost line stopped applying")
	}
}

func TestNewCostRejectsWhatWouldSilentlyDistortATotal(t *testing.T) {
	from, until := "2026-06-01", "2026-01-01"

	cases := []struct {
		name string
		spec CostSpec
	}{
		{"no kind", CostSpec{Period: CostMonthly, AmountMinor: 100}},
		{"no period", CostSpec{Kind: "support", AmountMinor: 100}},
		{"a period the arithmetic does not know", CostSpec{
			Kind: "support", Period: "quarterly", AmountMinor: 100}},
		// A credit is an accounting concept this system cannot report honestly;
		// it would simply subtract and make the estate look cheaper than it is.
		{"a negative amount", CostSpec{
			Kind: "support", Period: CostMonthly, AmountMinor: -1}},
		// A window that closes before it opens never matches, so the cost
		// vanishes from every total with no visible symptom at all.
		{"a window that closes before it opens", CostSpec{
			Kind: "support", Period: CostMonthly, AmountMinor: 100,
			ValidFrom: &from, ValidUntil: &until}},
		{"an impossible date", CostSpec{
			Kind: "support", Period: CostMonthly, AmountMinor: 100,
			ValidFrom: strptr("2026-02-31")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewCost("c1", c.spec, costNow); err == nil {
				t.Error("accepted")
			}
		})
	}

	// Zero is a legitimate amount: "we checked, it is free" is worth recording
	// and is different from nobody having looked.
	if _, err := NewCost("c1", CostSpec{
		Kind: "support", Period: CostMonthly, AmountMinor: 0}, costNow); err != nil {
		t.Errorf("a zero-amount line was rejected: %v", err)
	}
	// An undated line starts today rather than being refused.
	c, err := NewCost("c1", CostSpec{
		Kind: "support", Period: CostMonthly, AmountMinor: 100}, costNow)
	if err != nil {
		t.Fatalf("an undated line was rejected: %v", err)
	}
	if c.ValidFrom != "2026-07-31" {
		t.Errorf("valid_from defaulted to %q, want today", c.ValidFrom)
	}
}

// TestTheYearIsExactEvenWhenTheMonthCannotBe.
//
// A EUR 940 yearly contract has no exact monthly share. Deriving the annual
// figure as twelve times the rounded month gives 939.96, and a page that tells a
// reader their EUR 940 contract costs EUR 939.96 a year invites them to check
// the arithmetic instead of the estate. The year is stated exactly; the month
// carries the rounding, because that is the figure that genuinely has some.
func TestTheYearIsExactEvenWhenTheMonthCannotBe(t *testing.T) {
	var totals CostTotals
	totals.Add(cost(t, CostYearly, 94000), nil, "2026-07-31")

	if totals.AnnualMinor != 94000 {
		t.Errorf("annual = %d, want 94000 exactly", totals.AnnualMinor)
	}
	if totals.MonthlyMinor != 7833 {
		t.Errorf("monthly = %d, want 7833", totals.MonthlyMinor)
	}
	if derived := totals.MonthlyMinor * 12; derived == totals.AnnualMinor {
		t.Skip("this input no longer distinguishes the two, pick another")
	}
}

// Amortisation: spreading a one-off over the life of what it bought.
//
// The acquisition date is the line's own ValidFrom -- for a one-off that IS the
// day it was paid -- and the end is the EOL date of the thing it is attached to.
// No third column holds either, which is the point: two fields that mean the
// same thing disagree the first time somebody edits one.
func TestAmortisationSpreadsAOneOffOverItsLife(t *testing.T) {
	eol := func(s string) *string { return &s }

	cases := []struct {
		name         string
		period       string
		amountMinor  int64
		from         string
		eol          *string
		on           string
		wantMonthly  int64
		wantPossible bool
	}{
		// 24 months from 2025-08-01 to 2027-08-01: 240000/24 = 10000.
		{"a two-year life", CostOnce, 240000, "2025-08-01", eol("2027-08-01"), "2026-07-31", 10000, true},

		// Only a one-off amortises. A monthly bill is already a run rate, and
		// spreading it again would count it twice.
		{"a monthly line", CostMonthly, 240000, "2025-08-01", eol("2027-08-01"), "2026-07-31", 0, false},
		{"a yearly line", CostYearly, 240000, "2025-08-01", eol("2027-08-01"), "2026-07-31", 0, false},

		// No EOL date: NOT amortisable, which is different from contributing
		// zero. The page counts these rather than hiding them.
		{"nothing to spread against", CostOnce, 240000, "2025-08-01", nil, "2026-07-31", 0, false},
		{"an empty EOL", CostOnce, 240000, "2025-08-01", eol(""), "2026-07-31", 0, false},

		// Past its end: fully written off. Amortisable -- it counts towards
		// what the figure covers -- and contributing nothing more.
		{"fully depreciated", CostOnce, 240000, "2019-01-01", eol("2024-01-01"), "2026-07-31", 0, true},
		// The last day of the life is already outside it: an asset supported
		// until the 1st is not still depreciating on the 1st.
		{"the day it ends", CostOnce, 240000, "2025-08-01", eol("2027-08-01"), "2027-08-01", 0, true},
		{"the day before it ends", CostOnce, 240000, "2025-08-01", eol("2027-08-01"), "2027-07-31", 10000, true},

		// A life under a month, or bought after it was already unsupportable.
		// Dividing by something near zero would produce a gigantic monthly
		// figure from a small purchase.
		{"bought after its EOL", CostOnce, 240000, "2027-01-01", eol("2026-01-01"), "2027-02-01", 0, false},
		{"a life of days", CostOnce, 240000, "2026-07-01", eol("2026-07-20"), "2026-07-10", 0, false},

		// The day count matters: bought on the 20th, supported to the 5th, so
		// the last month has not happened.
		{"a partial month does not round up", CostOnce, 110000, "2025-08-20", eol("2026-08-05"), "2026-01-01", 10000, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from := c.from
			line, err := NewCost("c1", CostSpec{
				Kind: "acquisition", Period: c.period, AmountMinor: c.amountMinor,
				ValidFrom: &from,
			}, costNow)
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			got, possible := line.AmortisedMonthlyMinor(c.eol, c.on)
			if possible != c.wantPossible {
				t.Fatalf("amortisable = %v, want %v", possible, c.wantPossible)
			}
			if got != c.wantMonthly {
				t.Errorf("monthly = %d, want %d", got, c.wantMonthly)
			}
		})
	}
}

// Amortisation is NEVER folded into the run rate. A run rate is what leaves the
// bank this month; amortisation is an accounting view of money that already did.
// Adding them silently would double-count every purchase.
func TestAmortisationStaysOutOfTheRunRate(t *testing.T) {
	eol := "2027-08-01"
	from := "2025-08-01"

	once, err := NewCost("c1", CostSpec{
		Kind: "acquisition", Period: CostOnce, AmountMinor: 240000, ValidFrom: &from,
	}, costNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	monthly, err := NewCost("c2", CostSpec{
		Kind: "support", Period: CostMonthly, AmountMinor: 5000, ValidFrom: &from,
	}, costNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	var totals CostTotals
	totals.Add(once, &eol, "2026-07-31")
	totals.Add(monthly, &eol, "2026-07-31")

	if totals.MonthlyMinor != 5000 {
		t.Errorf("run rate = %d, want 5000 — amortisation leaked into it", totals.MonthlyMinor)
	}
	if totals.AmortisedMonthlyMinor != 10000 {
		t.Errorf("amortised = %d, want 10000", totals.AmortisedMonthlyMinor)
	}
	// The combined figure exists, but only where a page asks for it by name.
	if totals.TotalMonthlyMinor() != 15000 {
		t.Errorf("total monthly = %d, want 15000", totals.TotalMonthlyMinor())
	}
	// The annual run rate is likewise untouched: 5000*12.
	if totals.AnnualMinor != 60000 {
		t.Errorf("annual = %d, want 60000", totals.AnnualMinor)
	}
	if totals.Amortisable != 1 || totals.Unamortisable != 0 {
		t.Errorf("counters = %d/%d, want 1/0", totals.Amortisable, totals.Unamortisable)
	}
}

// The counters are what stop the figure being flattering. A total covering two
// of nine purchases must not look like a total covering nine.
func TestUnamortisablePurchasesAreCounted(t *testing.T) {
	from := "2025-08-01"
	eol := "2027-08-01"

	priced := func(id string) *Cost {
		c, err := NewCost(id, CostSpec{
			Kind: "acquisition", Period: CostOnce, AmountMinor: 240000, ValidFrom: &from,
		}, costNow)
		if err != nil {
			t.Fatalf("building: %v", err)
		}
		return c
	}

	var totals CostTotals
	totals.Add(priced("c1"), &eol, "2026-07-31") // has a life
	totals.Add(priced("c2"), nil, "2026-07-31")  // has none
	totals.Add(priced("c3"), nil, "2026-07-31")

	if totals.Amortisable != 1 {
		t.Errorf("amortisable = %d, want 1", totals.Amortisable)
	}
	if totals.Unamortisable != 2 {
		t.Errorf("unamortisable = %d, want 2 — purchases with no EOL date vanished", totals.Unamortisable)
	}
	if totals.AmortisedMonthlyMinor != 10000 {
		t.Errorf("amortised = %d, want 10000", totals.AmortisedMonthlyMinor)
	}
}
