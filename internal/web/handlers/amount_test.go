package handlers

import (
	"math"
	"testing"
)

// parseAmountMinor turns what a person typed into money, so every way of
// getting it subtly wrong is a way of storing a number nobody will question.
func TestParseAmountMinor(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{"plain", "1200", 120000, false},
		{"two decimals", "1200.50", 120050, false},
		{"one decimal is tenths", "1200.5", 120050, false},
		{"trailing point is mid-typing", "1200.", 120000, false},
		{"a comma is a decimal point", "1200,50", 120050, false},
		{"spaces are grouping", "1 200,50", 120050, false},
		{"empty is zero", "", 0, false},

		// Found by a security review. strconv.ParseInt accepts a leading sign,
		// so the fraction "-5" parsed to 5 and silently subtracted a cent from
		// the whole part: "1.-5" became 95 minor units with no error at all.
		{"a signed fraction is not a number", "1.-5", 0, true},
		{"a positively signed fraction either", "1.+5", 0, true},
		{"nor one that goes negative", "0.-5", 0, true},
		{"nor letters in the fraction", "1.5x", 0, true},

		// Genuinely ambiguous: 1.200,50 and 1,200.50 differ by a factor of a
		// thousand, and guessing produces a silently wrong budget.
		{"both separators", "1.200,50", 0, true},
		{"three decimals", "1200.505", 0, true},
		{"not a number", "twelve", 0, true},
		{"negative", "-5", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAmountMinor(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("parseAmountMinor(%q) error = %v, want error: %v", c.in, err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("parseAmountMinor(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// The cap has to cover the ANNUALISED figure, not just the stored one. A monthly
// amount is multiplied by twelve to produce the yearly total, so a value that
// fits in the column and not in the total wraps negative a page later rather
// than being refused at the form.
func TestParseAmountMinorRefusesWhatWouldOverflowAnnualised(t *testing.T) {
	// One major unit past what twelve times the minor amount can hold.
	tooBig := (int64(math.MaxInt64) / 12 / 100) + 1
	if _, err := parseAmountMinor(itoa(tooBig)); err == nil {
		t.Errorf("accepted %d, which overflows once annualised", tooBig)
	}
	// And the largest safe value is still accepted, or the cap is merely a
	// smaller arbitrary number.
	ok := int64(math.MaxInt64) / 12 / 100
	got, err := parseAmountMinor(itoa(ok))
	if err != nil {
		t.Fatalf("rejected the largest holdable amount: %v", err)
	}
	if got*12 < 0 {
		t.Errorf("%d minor units still overflows when annualised", got)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
