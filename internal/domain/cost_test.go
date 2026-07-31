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
	totals.Add(cost(t, CostOnce, 840000))   // capital only
	totals.Add(cost(t, CostMonthly, 38000)) // 380.00/month
	totals.Add(cost(t, CostYearly, 120000)) // 100.00/month
	totals.Add(cost(t, CostOnce, 520000))   // capital only

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
	totals.Add(cost(t, CostYearly, 94000))

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
