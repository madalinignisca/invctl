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
	"strconv"
	"strings"
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
	if err := s.CreateManufacturer(ctx, testPermit, m); err != nil {
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
	if err := s.CreateDeviceType(ctx, testPermit, d); err != nil {
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
	if err := s.CreateAsset(ctx, testPermit, a, nil); err != nil {
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
					dt := mustDeviceType(t, s, ctx, mf, "model-"+strconv.Itoa(i), tc.typeEOL)
					name := "box-" + strconv.Itoa(i)
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

			if err := s.RetireDeviceType(ctx, testPermit, dt); err != nil {
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

			err := s.RetireManufacturer(ctx, testPermit, mf)
			if err == nil {
				t.Fatal("a manufacturer with live models was retired, leaving them filed " +
					"under a maker no picker shows")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict", err)
			}

			// The control: once the model is gone, the maker can go too. Without
			// this the test above passes on a method that refuses everything.
			if err := s.RetireDeviceType(ctx, testPermit, dt); err != nil {
				t.Fatalf("retiring the model: %v", err)
			}
			if err := s.RetireManufacturer(ctx, testPermit, mf); err != nil {
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
			if err := s.UpdateDeviceType(ctx, testPermit, &row.DeviceType); err != nil {
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
			if err := s.UpdateDeviceType(ctx, testPermit, &stale); !errors.Is(err, domain.ErrConflict) {
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

// Search over the catalogue: a part number, and a serial in whatever case it
// was read out in.

func TestAPartNumberFindsTheModelAndCountsTheBoxes(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "hpe", "HPE")
			d, err := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
				ManufacturerID: mf, Model: "DL380 Gen10", PartNumber: ptr("P30721-B21"),
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateDeviceType(ctx, testPermit, d); err != nil {
				t.Fatalf("creating: %v", err)
			}
			assetOfType(t, s, ctx, "dl-01", d.ID, nil)
			assetOfType(t, s, ctx, "dl-02", d.ID, nil)

			hits, err := s.Search(ctx, "P30721-B21", 20)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			var found *SearchResult
			for i := range hits {
				if hits[i].EntityType == "device_type" {
					found = &hits[i]
				}
			}
			if found == nil {
				t.Fatalf("a part number found no model. %d hits: %+v\n"+
					"A part number is what arrives from procurement or a support portal, "+
					"and it is the one identifier nobody can translate by hand.",
					len(hits), hits)
			}
			if found.Title != "DL380 Gen10" {
				t.Errorf("title = %q, want the model", found.Title)
			}
			// The COUNT is the answer, not decoration. "Do we have any of these"
			// is the question behind pasting a part number.
			if found.Assets != 2 {
				t.Errorf("hit says %d assets, want 2", found.Assets)
			}
			if !strings.Contains(found.Why, "2 assets") {
				t.Errorf("why = %q, want it to say how many boxes are of this model", found.Why)
			}
		})
	}
}

func TestAPartNumberIsFoundInAnyCase(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "hpe", "HPE")
			d, _ := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
				ManufacturerID: mf, Model: "DL380", PartNumber: ptr("P30721-B21"),
			}, s.Now())
			if err := s.CreateDeviceType(ctx, testPermit, d); err != nil {
				t.Fatalf("creating: %v", err)
			}

			// The EXACT hit specifically, not merely "a model came back". The
			// free-text index also matches a part number in the body, so an
			// assertion on entity type alone passes even when the structured
			// lookup is case-sensitive and finding nothing -- which is exactly
			// what mutation testing caught here.
			for _, typed := range []string{"p30721-b21", "P30721-b21"} {
				hits, err := s.Search(ctx, typed, 20)
				if err != nil {
					t.Fatalf("searching %q: %v", typed, err)
				}
				var exact bool
				for _, h := range hits {
					if h.EntityType == "device_type" && strings.Contains(h.Why, "part number matches exactly") {
						exact = true
					}
				}
				if !exact {
					t.Errorf("%q did not resolve to the model as an exact part-number match. "+
						"A part number is copied out of a quote or read off a label; the "+
						"case it arrives in is not the case it was recorded in.", typed)
				}
			}
		})
	}
}

func TestASerialIsFoundInWhateverCaseItWasReadOutIn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a, err := domain.NewAsset(NewID(), domain.KindServer, "srv-01", nil, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			a.Serial = ptr("FCH2033V0YR")
			a.AssetTag = ptr("ASSET-0042")
			if err := s.CreateAsset(ctx, testPermit, a, nil); err != nil {
				t.Fatalf("creating: %v", err)
			}

			// The case that matters is the lower one: somebody typing what they
			// are reading off a sticker, at 03:00, does not hold shift.
			for _, typed := range []string{"FCH2033V0YR", "fch2033v0yr", "asset-0042", "ASSET-0042"} {
				hits, err := s.Search(ctx, typed, 20)
				if err != nil {
					t.Fatalf("searching %q: %v", typed, err)
				}
				var exact bool
				for _, h := range hits {
					if h.EntityID == a.ID && strings.Contains(h.Why, "exactly") {
						exact = true
					}
				}
				if !exact {
					t.Errorf("%q did not resolve to the box as an exact identifier match", typed)
				}
			}
		})
	}
}

func TestACataloguedModelIsFindableByName(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "dell", "Dell")
			mustDeviceType(t, s, ctx, mf, "PowerEdge R650", nil)

			hits, err := s.Search(ctx, "PowerEdge", 20)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			for _, h := range hits {
				if h.EntityType == "device_type" {
					return
				}
			}
			t.Errorf("a catalogued model is not in the search index at all. %d hits: %+v",
				len(hits), hits)
		})
	}
}

func TestCorrectingAPartNumberCorrectsTheIndex(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mf := mustManufacturer(t, s, ctx, "hpe", "HPE")
			d, _ := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
				ManufacturerID: mf, Model: "DL380", PartNumber: ptr("WRONG-1"),
			}, s.Now())
			if err := s.CreateDeviceType(ctx, testPermit, d); err != nil {
				t.Fatalf("creating: %v", err)
			}

			row, err := s.GetDeviceType(ctx, d.ID)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			row.PartNumber = ptr("P30721-B21")
			if err := s.UpdateDeviceType(ctx, testPermit, &row.DeviceType); err != nil {
				t.Fatalf("updating: %v", err)
			}

			// Searching the OLD part number, not the new one.
			//
			// The new one is answered by the STRUCTURED lookup, which reads
			// device_type directly and knows nothing about the index -- so
			// asserting on it passes whether or not anything was reindexed, which
			// is what mutation testing caught. The stale value can only be
			// answered by the index, so it is the one that proves the reindex ran.
			hits, err := s.Search(ctx, "WRONG-1", 20)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			for _, h := range hits {
				if h.EntityID == d.ID {
					t.Errorf("the model is still findable by the part number it no longer "+
						"has (%q). A value fixed in the form and not in the index is a "+
						"wrong answer the index goes on giving.", "WRONG-1")
				}
			}

			// And the control: the corrected value does resolve, so the test
			// above cannot pass on a search that finds nothing at all.
			hits, err = s.Search(ctx, "P30721-B21", 20)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			var ok bool
			for _, h := range hits {
				if h.EntityID == d.ID {
					ok = true
				}
			}
			if !ok {
				t.Error("the corrected part number finds nothing at all")
			}
		})
	}
}

// Catalogue import. The machinery is the asset importer's, so what is tested
// here is what DIFFERS: the natural key, the manufacturer reference, and the
// yes/no column.

const catalogueHeader = "manufacturer,model\n"

func catalogueCSV(t *testing.T, body string) []DeviceTypeImportRow {
	t.Helper()
	rows, problems := ParseDeviceTypeCSV(strings.NewReader(body))
	if len(problems) > 0 {
		t.Fatalf("parsing the fixture file failed: %+v", problems)
	}
	return rows
}

func TestImportingACatalogueFile(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			t.Run("models land, with their dates", func(t *testing.T) {
				s, ctx := newStore(t, e)
				mustManufacturer(t, s, ctx, "dell", "Dell")

				rows, problems := ParseDeviceTypeCSV(strings.NewReader(
					"manufacturer,model,part_number,u_height,full_depth,eol_date\n" +
						"dell,R650,P30721-B21,1,yes,2029-03-31\n"))
				if len(problems) > 0 {
					t.Fatalf("parsing: %+v", problems)
				}
				report, err := s.ImportDeviceTypes(ctx, testPermit, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("a valid file was refused: %+v", report.Problems)
				}

				list, err := s.ListDeviceTypes(ctx, DeviceTypeFilter{})
				if err != nil || len(list) != 1 {
					t.Fatalf("reading back: %v (%d rows)", err, len(list))
				}
				got := list[0]
				// Read every field back. "No error" is what a correct import and
				// one that stored a row of zeroes both look like.
				if got.Model != "R650" {
					t.Errorf("model = %q", got.Model)
				}
				if got.PartNumber == nil || *got.PartNumber != "P30721-B21" {
					t.Errorf("part number = %v", got.PartNumber)
				}
				if got.UHeight == nil || *got.UHeight != 1 {
					t.Errorf("u_height = %v", got.UHeight)
				}
				if !got.FullDepth {
					t.Error("full_depth = false, but the file said yes")
				}
				if got.EOLDate == nil || *got.EOLDate != "2029-03-31" {
					t.Errorf("eol_date = %v", got.EOLDate)
				}
			})

			t.Run("an unknown manufacturer is named, not created", func(t *testing.T) {
				s, ctx := newStore(t, e)
				report, err := s.ImportDeviceTypes(ctx, testPermit,
					catalogueCSV(t, catalogueHeader+"nosuchmaker,R650\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(problemsAbout(report, "nosuchmaker")) != 1 {
					t.Fatalf("problems = %+v, want one quoting the unknown code", report.Problems)
				}
				// Creating it from a bare code would give the catalogue an entry
				// with no name that nobody chose.
				var makers int
				if err := s.db.Reader.GetContext(ctx, &makers,
					`SELECT COUNT(*) FROM manufacturer`); err != nil {
					t.Fatalf("counting: %v", err)
				}
				if makers != 0 {
					t.Errorf("the import created %d manufacturers; it must reference, not invent", makers)
				}
			})

			t.Run("a model already catalogued is refused, not updated", func(t *testing.T) {
				s, ctx := newStore(t, e)
				mf := mustManufacturer(t, s, ctx, "dell", "Dell")
				mustDeviceType(t, s, ctx, mf, "R650", ptr("2029-03-31"))

				report, err := s.ImportDeviceTypes(ctx, testPermit,
					catalogueCSV(t, "manufacturer,model,eol_date\ndell,R650,2035-01-01\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(problemsAbout(report, "already catalogued")) != 1 {
					t.Fatalf("problems = %+v, want one saying it already exists", report.Problems)
				}
				list, err := s.ListDeviceTypes(ctx, DeviceTypeFilter{})
				if err != nil || len(list) != 1 {
					t.Fatalf("reading back: %v", err)
				}
				if *list[0].EOLDate != "2029-03-31" {
					t.Errorf("eol_date = %q; import creates, it does not update", *list[0].EOLDate)
				}
			})

			t.Run("the same model under two makers is fine", func(t *testing.T) {
				s, ctx := newStore(t, e)
				mustManufacturer(t, s, ctx, "dell", "Dell")
				mustManufacturer(t, s, ctx, "acme", "Acme")
				report, err := s.ImportDeviceTypes(ctx, testPermit,
					catalogueCSV(t, catalogueHeader+"dell,R650\nacme,R650\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("refused: %+v\nThe key is (manufacturer, model); two makers "+
						"using the same model string is not a collision.", report.Problems)
				}
			})

			t.Run("one bad row refuses the whole file", func(t *testing.T) {
				s, ctx := newStore(t, e)
				mustManufacturer(t, s, ctx, "dell", "Dell")
				report, err := s.ImportDeviceTypes(ctx, testPermit, catalogueCSV(t,
					catalogueHeader+"dell,R650\ndell,R750\nnosuchmaker,R850\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) == 0 {
					t.Fatal("a file with an unknown manufacturer was accepted")
				}
				list, err := s.ListDeviceTypes(ctx, DeviceTypeFilter{})
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				if len(list) != 0 {
					t.Errorf("%d models survived a refused file", len(list))
				}
			})
		})
	}
}

// TestAYesNoColumnRefusesWhatItCannotRead is the silent-fallback shape in its
// most tempting form.
func TestAYesNoColumnRefusesWhatItCannotRead(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mustManufacturer(t, s, ctx, "dell", "Dell")

			// The tempting implementation returns false for anything it does not
			// recognise, which turns a localised "ja" -- or a stray character --
			// into a quiet no. A full-depth chassis recorded as half-depth is
			// wrong in a way nobody notices until a rack diagram is built on it.
			report, err := s.ImportDeviceTypes(ctx, testPermit, catalogueCSV(t,
				"manufacturer,model,full_depth\ndell,R650,ja\n"), false)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if len(report.Problems) != 1 {
				t.Fatalf("problems = %+v, want one about the unreadable yes/no", report.Problems)
			}
			if report.Problems[0].Field != "full_depth" {
				t.Errorf("problem field = %q, want full_depth", report.Problems[0].Field)
			}

			// The control: the spellings a spreadsheet actually produces all work,
			// so the refusal above is not simply a column that never accepts
			// anything.
			for _, yes := range []string{"true", "TRUE", "yes", "Y", "1"} {
				r, err := s.ImportDeviceTypes(ctx, testPermit, catalogueCSV(t,
					"manufacturer,model,full_depth\ndell,M-"+yes+","+yes+"\n"), true)
				if err != nil {
					t.Fatalf("importing %q: %v", yes, err)
				}
				if len(r.Problems) > 0 {
					t.Errorf("%q was refused as a yes: %+v", yes, r.Problems)
				}
			}
		})
	}
}

func TestAnUnreadableRackHeightIsRefusedRatherThanDropped(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mustManufacturer(t, s, ctx, "dell", "Dell")

			report, err := s.ImportDeviceTypes(ctx, testPermit, catalogueCSV(t,
				"manufacturer,model,u_height\ndell,R650,1U\n"), false)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if len(report.Problems) != 1 || report.Problems[0].Field != "u_height" {
				t.Fatalf("problems = %+v, want one on u_height.\nA model stored with the "+
					"height quietly dropped occupies nothing in every future elevation "+
					"calculation, and nothing on screen says why.", report.Problems)
			}
		})
	}
}

func TestACatalogueImportIsAuditedAndIndexed(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mustManufacturer(t, s, ctx, "hpe", "HPE")

			if _, err := s.ImportDeviceTypes(ctx, testPermit, catalogueCSV(t,
				"manufacturer,model,part_number\nhpe,DL380,P30721-B21\n"), false); err != nil {
				t.Fatalf("importing: %v", err)
			}

			var audits int
			if err := s.db.Reader.GetContext(ctx, &audits,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = 'device_type'`); err != nil {
				t.Fatalf("counting: %v", err)
			}
			if audits != 1 {
				t.Errorf("import wrote %d device_type audit rows, want 1", audits)
			}

			// Indexed too. The importer goes through insertDeviceType precisely so
			// it cannot skip half of what a create does.
			hits, err := s.Search(ctx, "P30721-B21", 10)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if len(hits) == 0 {
				t.Error("an imported model is not findable by its part number")
			}
		})
	}
}

func TestABatchedCatalogueImportWritesAndRefusesTheSameWay(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mustManufacturer(t, s, ctx, "dell", "Dell")

			report, err := s.ImportDeviceTypesBatched(ctx, domain.AdministratorPermit(testActor),
				catalogueCSV(t, catalogueHeader+"dell,R650\ndell,R750\n"), nil)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if len(report.Problems) > 0 {
				t.Fatalf("a valid file was refused: %+v", report.Problems)
			}
			if len(report.Created) != 2 || report.PartialRows != 0 {
				t.Fatalf("created %v, partial %d; want 2 and 0", report.Created, report.PartialRows)
			}

			// Create only, on the path that writes -- the gap the asset version
			// had until mutation testing found it.
			again, err := s.ImportDeviceTypesBatched(ctx, domain.AdministratorPermit(testActor),
				catalogueCSV(t, catalogueHeader+"dell,R650\n"), nil)
			if err != nil {
				t.Fatalf("re-importing: %v", err)
			}
			if len(problemsAbout(again, "already catalogued")) != 1 {
				t.Fatalf("problems = %+v, want one saying it already exists", again.Problems)
			}

			// An unknown manufacturer is still refused before anything is
			// written, so a bad row in a batched file leaves nothing half-done.
			bad, err := s.ImportDeviceTypesBatched(ctx, domain.AdministratorPermit(testActor),
				catalogueCSV(t, catalogueHeader+"dell,R850\nnosuchmaker,R950\n"), nil)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if len(bad.Created) != 0 || bad.PartialRows != 0 {
				t.Errorf("a refused file created %v (partial %d); validation runs over the "+
					"whole file before any batch is written", bad.Created, bad.PartialRows)
			}
			list, err := s.ListDeviceTypes(ctx, DeviceTypeFilter{})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(list) != 2 {
				t.Errorf("catalogue has %d models after a refused file, want 2", len(list))
			}
		})
	}
}
