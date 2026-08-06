// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The hardware catalogue, and the rule that makes it worth having: an asset
// inherits its model's end-of-support date unless it states its own.
//
// The direction of that override is the part a test has to pin down. It is not
// "the more cautious date wins" and it is not "the later one wins" -- it is
// "the more SPECIFIC assertion wins", because a private support contract can
// carry one box years past what its manufacturer publishes, and a second-hand
// unit can fall short of it. Both directions are real and both are below.

// eolClock is a fixed date, because "expired" is a question about a moment and
// a test against time.Now() answers a different question every day. Six store
// cost tests in this repo expired at midnight UTC before this was a habit.
var eolClock = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func mustManufacturer(t *testing.T, s *SQLStore, ctx context.Context, code, name string) string {
	t.Helper()
	m, err := domain.NewManufacturer(NewID(), domain.ManufacturerSpec{Code: code, Name: name}, s.Now())
	if err != nil {
		t.Fatalf("building manufacturer %s: %v", code, err)
	}
	if err := s.CreateManufacturer(ctx, testActor, m); err != nil {
		t.Fatalf("creating manufacturer %s: %v", code, err)
	}
	return m.ID
}

func mustDeviceType(t *testing.T, s *SQLStore, ctx context.Context, mfID, model string, eol *string) string {
	t.Helper()
	d, err := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
		ManufacturerID: mfID, Model: model, EOLDate: eol,
	}, s.Now())
	if err != nil {
		t.Fatalf("building device type %s: %v", model, err)
	}
	if err := s.CreateDeviceType(ctx, testActor, d); err != nil {
		t.Fatalf("creating device type %s: %v", model, err)
	}
	return d.ID
}

func ptr(s string) *string { return &s }

// assetOfType creates an asset of a model, optionally with its own EOL date.
func assetOfType(t *testing.T, s *SQLStore, ctx context.Context, name, deviceTypeID string, ownEOL *string) string {
	t.Helper()
	a, err := domain.NewAsset(NewID(), domain.KindServer, name, nil, s.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if deviceTypeID != "" {
		a.DeviceTypeID = &deviceTypeID
	}
	a.EOLDate = ownEOL
	if err := s.CreateAsset(ctx, testActor, a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

// rowFor finds a report row by entity name.
func rowFor(report *ExpiryReport, name string) (ExpiringRow, bool) {
	for _, r := range report.Rows {
		if r.Name == name {
			return r, true
		}
	}
	return ExpiringRow{}, false
}

func TestAnAssetsOwnEndOfSupportBeatsItsModels(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dell := mustManufacturer(t, s, ctx, "dell", "Dell")
			// The model lapses in three months, per the manufacturer.
			r650 := mustDeviceType(t, s, ctx, dell, "R650", ptr("2026-09-30"))

			// Three boxes of that model.
			assetOfType(t, s, ctx, "inherits-01", r650, nil)
			// THE MAGIC CONTRACT: privately supported well past the published
			// date. The specific assertion has to win, or the report nags about
			// hardware somebody has already paid to keep.
			assetOfType(t, s, ctx, "contracted-01", r650, ptr("2029-12-31"))
			// And the other direction: a second-hand unit whose support runs out
			// BEFORE the model's date. If the type's date won, this box would
			// look safe for three months longer than it is.
			assetOfType(t, s, ctx, "secondhand-01", r650, ptr("2026-06-30"))

			report, err := s.Expiring(ctx, eolClock, 12)
			if err != nil {
				t.Fatalf("running the expiry report: %v", err)
			}

			inherited, ok := rowFor(report, "inherits-01")
			if !ok {
				t.Fatal("an asset with no date of its own did not inherit its model's, so " +
					"the catalogue buys nothing")
			}
			if inherited.EOLDate != "2026-09-30" {
				t.Errorf("inherited date = %q, want the model's 2026-09-30", inherited.EOLDate)
			}
			if inherited.EOLSource != domain.EOLFromDeviceType {
				t.Errorf("source = %q, want %q. A date whose origin is not stated is a "+
					"fact and an assumption rendered identically.",
					inherited.EOLSource, domain.EOLFromDeviceType)
			}
			if inherited.DeviceTypeLabel != "Dell R650" {
				t.Errorf("device type label = %q, want \"Dell R650\" so the row can say "+
					"where the date came from", inherited.DeviceTypeLabel)
			}

			// Past the horizon on its own date, so it must NOT appear at all.
			if row, present := rowFor(report, "contracted-01"); present {
				t.Errorf("a box under a private contract until %s was reported as expiring "+
					"on %s. Its own date has to beat its model's, or the report nags about "+
					"hardware somebody has already paid to keep.", "2029-12-31", row.EOLDate)
			}

			short, ok := rowFor(report, "secondhand-01")
			if !ok {
				t.Fatal("an asset whose own date is EARLIER than its model's was not reported")
			}
			if short.EOLDate != "2026-06-30" {
				t.Errorf("date = %q, want its own 2026-06-30. The override is not "+
					"\"whichever is later\" -- a unit can fall short of what its model "+
					"promises.", short.EOLDate)
			}
			if short.EOLSource != domain.EOLFromAsset {
				t.Errorf("source = %q, want %q", short.EOLSource, domain.EOLFromAsset)
			}
		})
	}
}

// TestTheReportAgreesWithTheDomainRule guards the one thing that will rot.
//
// The override is written twice: as COALESCE/CASE in the expiry query, and as
// domain.ResolveEOL for callers holding structs. Two expressions of one rule is
// one more than is safe, and the failure mode is silent -- a page and a report
// quietly disagreeing about the same box.
func TestTheReportAgreesWithTheDomainRule(t *testing.T) {
	cases := []struct {
		name      string
		assetEOL  *string
		typeEOL   *string
		wantDate  string
		wantWhere string
	}{
		{"own date only", ptr("2026-07-01"), nil, "2026-07-01", domain.EOLFromAsset},
		{"model date only", nil, ptr("2026-08-01"), "2026-08-01", domain.EOLFromDeviceType},
		{"own beats model, later", ptr("2027-01-01"), ptr("2026-07-01"), "2027-01-01", domain.EOLFromAsset},
		{"own beats model, earlier", ptr("2026-06-15"), ptr("2026-12-01"), "2026-06-15", domain.EOLFromAsset},
		{"neither", nil, nil, "", domain.EOLFromNowhere},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "acme", "Acme")

			for i, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					dt := mustDeviceType(t, s, ctx, mf, "model-"+string(rune('a'+i)), tc.typeEOL)
					name := "box-" + string(rune('a'+i))
					assetOfType(t, s, ctx, name, dt, tc.assetEOL)

					// The domain's answer.
					date, source := domain.ResolveEOL(tc.assetEOL, tc.typeEOL)
					got := ""
					if date != nil {
						got = *date
					}
					if got != tc.wantDate || source != tc.wantWhere {
						t.Fatalf("domain.ResolveEOL = (%q, %q), want (%q, %q)",
							got, source, tc.wantDate, tc.wantWhere)
					}

					// The report's answer, over a horizon wide enough to hold
					// every case above.
					report, err := s.Expiring(ctx, eolClock, 120)
					if err != nil {
						t.Fatalf("running the report: %v", err)
					}
					row, present := rowFor(report, name)
					if tc.wantDate == "" {
						if present {
							t.Errorf("the report listed %s, which has no date from either source", name)
						}
						return
					}
					if !present {
						t.Fatalf("the report omitted %s, which the domain resolves to %s",
							name, tc.wantDate)
					}
					if row.EOLDate != tc.wantDate || row.EOLSource != tc.wantWhere {
						t.Errorf("report = (%q, %q), domain = (%q, %q).\n"+
							"The SQL and domain.ResolveEOL are two expressions of one rule; "+
							"when they disagree, the function is right.",
							row.EOLDate, row.EOLSource, tc.wantDate, tc.wantWhere)
					}
				})
			}
		})
	}
}

// TestAnInheritedDateIsNotCountedAsMissing covers the number that makes the
// whole report honest.
func TestAnInheritedDateIsNotCountedAsMissing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "dell", "Dell")
			dated := mustDeviceType(t, s, ctx, mf, "R650", ptr("2027-09-30"))
			undated := mustDeviceType(t, s, ctx, mf, "R750", nil)

			base, err := s.Expiring(ctx, eolClock, 12)
			if err != nil {
				t.Fatalf("report: %v", err)
			}

			assetOfType(t, s, ctx, "covered-01", dated, nil)
			after, err := s.Expiring(ctx, eolClock, 12)
			if err != nil {
				t.Fatalf("report: %v", err)
			}
			if after.UndatedAssets != base.UndatedAssets {
				t.Errorf("undated assets went from %d to %d after adding a box whose MODEL "+
					"has a date.\nThat asset is not a gap in the record -- it is the case "+
					"the catalogue exists for, and counting it reports the feature working "+
					"as though it were not.", base.UndatedAssets, after.UndatedAssets)
			}

			assetOfType(t, s, ctx, "uncovered-01", undated, nil)
			last, err := s.Expiring(ctx, eolClock, 12)
			if err != nil {
				t.Fatalf("report: %v", err)
			}
			// The negative control: a box whose model has NO date must still
			// count, or the test above would pass on a counter that never moves.
			if last.UndatedAssets != after.UndatedAssets+1 {
				t.Errorf("undated assets = %d, want %d. A box whose model has no date "+
					"either is genuinely undated.", last.UndatedAssets, after.UndatedAssets+1)
			}
		})
	}
}

// TestRetiringAModelKeepsItsDateWorking is the case that is easy to get
// backwards.
func TestRetiringAModelKeepsItsDateWorking(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "dell", "Dell")
			dt := mustDeviceType(t, s, ctx, mf, "R650", ptr("2026-09-30"))
			assetOfType(t, s, ctx, "still-racked-01", dt, nil)

			if err := s.RetireDeviceType(ctx, testActor, dt); err != nil {
				t.Fatalf("retiring the model: %v", err)
			}

			report, err := s.Expiring(ctx, eolClock, 12)
			if err != nil {
				t.Fatalf("report: %v", err)
			}
			row, ok := rowFor(report, "still-racked-01")
			if !ok {
				t.Fatal("retiring a model blanked the expiry date on every asset of it.\n" +
					"Retiring a model means \"we no longer buy these\" -- and the boxes " +
					"already racked are the reason you wanted to say so.")
			}
			if row.EOLDate != "2026-09-30" {
				t.Errorf("date = %q, want the retired model's 2026-09-30", row.EOLDate)
			}
		})
	}
}

func TestTheCatalogueRefusesWhatWouldStrandAModel(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "dell", "Dell")
			dt := mustDeviceType(t, s, ctx, mf, "R650", nil)

			err := s.RetireManufacturer(ctx, testActor, mf)
			if err == nil {
				t.Fatal("a manufacturer with live models was retired, leaving them filed " +
					"under a maker no picker shows")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict", err)
			}

			// The control: once the model is gone, the maker can go too. Without
			// this the test above passes on a method that refuses everything.
			if err := s.RetireDeviceType(ctx, testActor, dt); err != nil {
				t.Fatalf("retiring the model: %v", err)
			}
			if err := s.RetireManufacturer(ctx, testActor, mf); err != nil {
				t.Errorf("retiring a manufacturer with no live models failed: %v", err)
			}
		})
	}
}

func TestTheCatalogueIsAuditedLikeEverythingElse(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			count := func(entity string) int {
				var n int
				if err := s.db.Reader.GetContext(ctx, &n,
					s.db.Rebind(`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`),
					entity); err != nil {
					t.Fatalf("counting: %v", err)
				}
				return n
			}

			mf := mustManufacturer(t, s, ctx, "dell", "Dell")
			dt := mustDeviceType(t, s, ctx, mf, "R650", nil)
			if count("manufacturer") != 1 || count("device_type") != 1 {
				t.Fatalf("creations wrote %d manufacturer and %d device_type audit rows, want 1 each",
					count("manufacturer"), count("device_type"))
			}

			row, err := s.GetDeviceType(ctx, dt)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			row.EOLDate = ptr("2030-01-01")
			if err := s.UpdateDeviceType(ctx, testActor, &row.DeviceType); err != nil {
				t.Fatalf("updating: %v", err)
			}
			if got := count("device_type"); got != 2 {
				t.Errorf("device_type audit rows = %d after an edit, want 2. Every mutation "+
					"of declared state writes a change_log row in the same transaction.", got)
			}

			// A second save from the same token is refused -- the catalogue is
			// not exempt from optimistic concurrency just because it is a lookup.
			stale := row.DeviceType
			stale.RowVersion = row.RowVersion - 1
			stale.Model = "R650-v2"
			if err := s.UpdateDeviceType(ctx, testActor, &stale); !errors.Is(err, domain.ErrConflict) {
				t.Errorf("a stale save returned %v, want ErrConflict", err)
			}
		})
	}
}

func TestADeviceTypeRefusesWhatWouldMislead(t *testing.T) {
	mf := "01234567-89ab-cdef-0123-456789abcdef"
	cases := []struct {
		name  string
		spec  domain.DeviceTypeSpec
		field string
	}{
		{"no model", domain.DeviceTypeSpec{ManufacturerID: mf}, "model"},
		{"no manufacturer", domain.DeviceTypeSpec{Model: "R650"}, "manufacturer_id"},
		{"unreadable date", domain.DeviceTypeSpec{ManufacturerID: mf, Model: "R650", EOLDate: ptr("next spring")}, "eol_date"},
		{"zero height", domain.DeviceTypeSpec{ManufacturerID: mf, Model: "R650", UHeight: intPtr(0)}, "u_height"},
		{"absurd height", domain.DeviceTypeSpec{ManufacturerID: mf, Model: "R650", UHeight: intPtr(442)}, "u_height"},
		{"unknown lifecycle", domain.DeviceTypeSpec{ManufacturerID: mf, Model: "R650", Lifecycle: "mothballed"}, "lifecycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewDeviceType(NewID(), tc.spec, eolClock)
			if err == nil {
				t.Fatalf("accepted; want a field failure on %q", tc.field)
			}
			if msg := fieldError(err, tc.field); msg == "" {
				t.Errorf("error = %v, want it attached to %q so the form can render it",
					err, tc.field)
			}
		})
	}
}

