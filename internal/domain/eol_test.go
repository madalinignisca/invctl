package domain

import (
	"testing"
	"time"
)

// The boundaries are the whole of this logic, so they are the whole of the
// test. Every case below is a day on one side or the other of a line.
func TestExpiryStateBoundaries(t *testing.T) {
	asOf := time.Date(2026, 7, 31, 14, 30, 0, 0, time.UTC)
	window := 90 * 24 * time.Hour

	date := func(s string) *string { return &s }

	cases := []struct {
		name  string
		eol   *string
		want  string
		days  int
		known bool
	}{
		{"no date is not the same as fine", nil, ExpiryUnknown, 0, false},
		{"an empty string is no date", date(""), ExpiryUnknown, 0, false},

		// Support runs to the END of the day it expires, so today is not yet
		// expired. Getting this wrong makes every renewal look a day late.
		{"expires today", date("2026-07-31"), ExpirySoon, 0, true},
		{"expired yesterday", date("2026-07-30"), ExpiryExpired, -1, true},
		{"expires tomorrow", date("2026-08-01"), ExpirySoon, 1, true},

		// The far edge of the window, from both sides.
		{"last day inside the window", date("2026-10-29"), ExpirySoon, 90, true},
		{"first day outside it", date("2026-10-30"), ExpiryOK, 91, true},

		{"long past", date("2019-01-01"), ExpiryExpired, -2768, true},
		{"far future", date("2031-01-01"), ExpiryOK, 1615, true},

		// A stored value that cannot be read is treated as absent. A report is
		// not the place to discover a bad row, and refusing to render the other
		// four hundred would help nobody.
		{"a real-looking but impossible date", date("2027-02-31"), ExpiryUnknown, 0, false},
		{"a timestamp where a date belongs", date("2027-06-30T00:00:00Z"), ExpiryUnknown, 0, false},
		{"not a date at all", date("soon"), ExpiryUnknown, 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExpiryState(c.eol, asOf, window); got != c.want {
				t.Errorf("ExpiryState = %q, want %q", got, c.want)
			}
			days, known := DaysUntil(c.eol, asOf)
			if known != c.known {
				t.Fatalf("DaysUntil known = %v, want %v", known, c.known)
			}
			if known && days != c.days {
				t.Errorf("DaysUntil = %d, want %d", days, c.days)
			}
		})
	}
}

// The time of day must not move a boundary. An asset does not expire because a
// report ran after lunch.
func TestExpiryStateIgnoresTimeOfDay(t *testing.T) {
	eol := "2026-07-31"
	for _, hour := range []int{0, 9, 13, 23} {
		asOf := time.Date(2026, 7, 31, hour, 59, 59, 0, time.UTC)
		if got := ExpiryState(&eol, asOf, 90*24*time.Hour); got != ExpirySoon {
			t.Errorf("at %02d:59 the state was %q, want %q", hour, got, ExpirySoon)
		}
	}
}

func TestCheckDateAcceptsAbsenceAndRejectsNonsense(t *testing.T) {
	cases := []struct {
		name    string
		in      *string
		wantErr bool
		wantNil bool
	}{
		{"nil stays nil", nil, false, true},
		// An operator clearing the field must not be told they made a mistake.
		{"empty becomes absent", strptr(""), false, true},
		{"whitespace becomes absent", strptr("   "), false, true},
		{"a good date is kept", strptr("2027-06-30"), false, false},
		{"surrounding space is trimmed", strptr(" 2027-06-30 "), false, false},
		// The database CHECK is a length and separator test and passes all
		// three of these. Go is where they are actually caught.
		{"day out of range", strptr("2027-02-31"), true, false},
		{"month out of range", strptr("2027-13-01"), true, false},
		{"right shape, wrong order", strptr("30-06-2027"), true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ve := &ValidationError{}
			got := checkDate(ve, "eol_date", c.in)
			if err := ve.OrNil(); (err != nil) != c.wantErr {
				t.Fatalf("error = %v, want error: %v", err, c.wantErr)
			}
			if c.wantNil && got != nil {
				t.Errorf("got %q, want nil", *got)
			}
			if !c.wantErr && !c.wantNil {
				if got == nil {
					t.Fatal("a valid date was dropped")
				}
				if *got != "2027-06-30" {
					t.Errorf("got %q, want 2027-06-30 (trimmed)", *got)
				}
			}
		})
	}
}

// An EOL date reaches an entity only through its constructor, so the entity
// validators have to reject the same things checkDate does. This is the guard
// against a future constructor that sets the field and forgets to check it.
func TestEntityValidatorsRejectABadEOLDate(t *testing.T) {
	bad := "2027-02-31"

	asset, err := NewAsset("a1", KindServer, "hv-99", nil, time.Now())
	if err != nil {
		t.Fatalf("building the asset: %v", err)
	}
	asset.EOLDate = &bad
	if err := asset.Validate(); err == nil {
		t.Error("an asset accepted 2027-02-31 as an EOL date")
	}

	svc, err := NewService("s1", ServiceSpec{
		Code: "x", Name: "X", Kind: "api", EnvironmentID: "e1",
		Availability: AvailStandalone, Tier: 3,
	}, time.Now())
	if err != nil {
		t.Fatalf("building the service: %v", err)
	}
	svc.EOLDate = &bad
	if err := svc.Validate(); err == nil {
		t.Error("a service accepted 2027-02-31 as an EOL date")
	}

	// And the spec path, which is how a handler actually supplies one.
	if _, err := NewService("s2", ServiceSpec{
		Code: "y", Name: "Y", Kind: "api", EnvironmentID: "e1",
		Availability: AvailStandalone, Tier: 3, EOLDate: &bad,
	}, time.Now()); err == nil {
		t.Error("NewService accepted 2027-02-31 as an EOL date")
	}
}

func strptr(s string) *string { return &s }
