// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Task 7: custom fields leave the system through the CSV export, alongside
// everything else. docs/custom-fields-design.md §7 is the design.

// indexOf returns the position of want in header, or -1. Used instead of
// strings.Contains on the header so a test asserts an EXACT column name --
// "Cost Centre" is a substring of "Cost Centre (retired)", so a Contains
// check would pass on the wrong column.
func indexOf(header []string, want string) int {
	for i, h := range header {
		if h == want {
			return i
		}
	}
	return -1
}

// cellFor returns the cell one entity holds in one column of an export
// Table, matched by position against the AssetRow or ServiceRow slice that
// produced it -- ExportAssets and ExportServices emit exactly one row per
// input row, in the same order, so the entity's index in rows is its index
// in table.Rows.
func cellFor(t *testing.T, entityIDs []string, table Table, entityID string, col int) string {
	t.Helper()
	if col < 0 {
		t.Fatalf("no such column (index %d)", col)
	}
	for i, id := range entityIDs {
		if id == entityID {
			if i >= len(table.Rows) {
				t.Fatalf("row %d out of range: the table has %d rows", i, len(table.Rows))
			}
			row := table.Rows[i]
			if col >= len(row) {
				t.Fatalf("column %d out of range: the row has %d cells", col, len(row))
			}
			return row[col]
		}
	}
	t.Fatalf("entity %s is not among the exported rows", entityID)
	return ""
}

// assetIDsOf and serviceIDsOf extract the id list cellFor needs to locate a
// row, in the same order ExportAssets/ExportServices consume it.
func assetIDsOf(rows []AssetRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func TestExportIncludesACustomFieldColumn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			// UPDATED FOR THIS SUITE: mustField builds a definition whose label
			// equals its code ("cost_centre"), not "Cost Centre" -- see mustField
			// in customfields_test.go. Retype it here so the exported header is
			// the human label the design promises, not the machine code.
			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Label = "Cost Centre"
			if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
				t.Fatalf("relabelling: %v", err)
			}

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}
			col := indexOf(table.Header, "Cost Centre (cost_centre)")
			if col < 0 {
				t.Fatalf("no Cost Centre (cost_centre) column; header is %v", table.Header)
			}
			if got := cellFor(t, assetIDsOf(rows), table, f.assetID, col); got != "IT-42" {
				t.Fatalf("got %q in the Cost Centre column, want IT-42", got)
			}
		})
	}
}

// TestARetiredFieldStillExportsUnderARetiredHeader is design.md §7's whole
// promise: retiring a field destroys no value, and the export is where an
// operator goes to see what they still hold.
//
// THE HEADER IS ASSERTED WITH AN EXACT COMPARISON, NOT strings.Contains.
// "Cost Centre" is a substring of "Cost Centre (retired)", so a Contains
// check would pass on either header and prove nothing about the marker.
func TestARetiredFieldStillExportsUnderARetiredHeader(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Label = "Cost Centre"
			if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
				t.Fatalf("relabelling: %v", err)
			}
			mustValue(t, f, id, f.assetID, "IT-42")

			if err := f.s.RetireCustomField(f.ctx, f.actor, id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			retired, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("re-reading after retiring: %v", err)
			}
			if retired.RetiredAt == nil {
				t.Fatal("retired_at is nil after retiring")
			}
			wantRetiredHeader := "Cost Centre (retired " + retiredDate(*retired.RetiredAt) + ") (cost_centre)"

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}

			if got := indexOf(table.Header, "Cost Centre (cost_centre)"); got >= 0 {
				t.Fatalf("the live header %q is still present after retirement; header is %v",
					"Cost Centre (cost_centre)", table.Header)
			}
			col := indexOf(table.Header, wantRetiredHeader)
			if col < 0 {
				t.Fatalf("no exact %q column; header is %v", wantRetiredHeader, table.Header)
			}
			for i, h := range table.Header {
				if h != wantRetiredHeader && strings.Contains(h, "Cost Centre") {
					t.Fatalf("column %d is %q -- a header that partially matches but is not "+
						"the exact retired marker", i, h)
				}
			}
			if got := cellFor(t, assetIDsOf(rows), table, f.assetID, col); got != "IT-42" {
				t.Fatalf("got %q in the retired Cost Centre column, want IT-42 -- "+
					"retiring a field must not lose the value", got)
			}
		})
	}
}

// TestACustomValueIsDefusedLikeEveryOtherCell. WP-G5 defuses at the boundary
// where text becomes a spreadsheet; a custom value an operator typed freely
// is not an exception to that rule.
func TestACustomValueIsDefusedLikeEveryOtherCell(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "note", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "=1+1")

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}
			col := indexOf(table.Header, "note (note)")
			if col < 0 {
				t.Fatalf("no note (note) column; header is %v", table.Header)
			}
			got := cellFor(t, assetIDsOf(rows), table, f.assetID, col)
			if strings.HasPrefix(got, "=") {
				t.Fatalf("the cell is still a live formula: %q. WP-G5 defuses at "+
					"the boundary where text becomes a spreadsheet, and a custom "+
					"value is not an exception", got)
			}
			if got != "'=1+1" {
				t.Fatalf("got %q, want the WP-G5 leading-apostrophe form %q", got, "'=1+1")
			}
		})
	}
}

// TestAFieldWithNoValueExportsAnEmptyCell: a field nobody has set yet is a
// real, common state and must not make the row shorter than the header or
// panic building the table.
func TestAFieldWithNoValueExportsAnEmptyCell(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			// f.secondAssetID never receives a value.

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}
			col := indexOf(table.Header, "cost_centre (cost_centre)")
			if col < 0 {
				t.Fatalf("no cost_centre (cost_centre) column; header is %v", table.Header)
			}
			if got := cellFor(t, assetIDsOf(rows), table, f.secondAssetID, col); got != "" {
				t.Fatalf("got %q for an asset holding no value, want an empty cell", got)
			}
		})
	}
}

// TestOnlyAssetFieldsAppearInTheAssetExport: a custom field defined for
// services must not grow a column on the asset export -- entity_type scopes
// custom fields, and the export has to respect it the same way every other
// surface does.
func TestOnlyAssetFieldsAppearInTheAssetExport(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			svcFieldID := mustField(t, f, "service", "owner", domain.CustomFieldText)
			mustValue(t, f, svcFieldID, f.serviceID, "platform-team")

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}
			if col := indexOf(table.Header, "owner (owner)"); col >= 0 {
				t.Fatalf("a service-only field appeared in the asset export at column %d; header is %v",
					col, table.Header)
			}
		})
	}
}

// TestServiceExportIncludesACustomFieldColumn is ExportAssets' test above,
// against ExportServices -- the signature change this task owns
// (ctx, error) makes it reachable at all.
func TestServiceExportIncludesACustomFieldColumn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "service", "owner", domain.CustomFieldText)
			mustValue(t, f, id, f.serviceID, "platform-team")

			rows, err := f.s.ListServices(f.ctx, ServiceFilter{})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportServices(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}
			col := indexOf(table.Header, "owner (owner)")
			if col < 0 {
				t.Fatalf("no owner (owner) column; header is %v", table.Header)
			}
			ids := make([]string, len(rows))
			for i, r := range rows {
				ids[i] = r.ID
			}
			if got := cellFor(t, ids, table, f.serviceID, col); got != "platform-team" {
				t.Fatalf("got %q in the owner column, want platform-team", got)
			}
		})
	}
}

// ---------- Ruling AL: the chunk-merge logic, exercised directly ----------

// TestCustomFieldColumnsMergeAcrossChunks calls customFieldColumns directly
// with 501 entity ids -- one more than idChunkSize (store.go) -- so
// chunkIDs splits it into two chunks. Every shipped ExportAssets test above
// goes through ListAssets, which is capped at AssetListLimit (500) and can
// therefore never produce more than one chunk; this is the only place the
// 500-id boundary, the seen-map dedup, and the cross-chunk value
// accumulation are exercised at all.
//
// The two ids that matter sit at the two positions a broken merge would get
// wrong: f.assetID at index 0 (first chunk) and f.secondAssetID at the very
// last index (last chunk). A per-chunk reset of the values accumulator would
// keep only the second chunk's contribution and lose the first; a per-chunk
// re-append of the column without the seen-map dedup would duplicate the
// column instead of merging into one.
func TestCustomFieldColumnsMergeAcrossChunks(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			mustValue(t, f, id, f.secondAssetID, "IT-99")

			// entity_id carries no foreign key (design.md §2), so a filler id
			// need not name a real asset -- only f.assetID and f.secondAssetID,
			// which already hold real values, have to.
			const total = idChunkSize + 1
			ids := make([]string, 0, total)
			ids = append(ids, f.assetID)
			for len(ids) < total-1 {
				ids = append(ids, NewID())
			}
			ids = append(ids, f.secondAssetID)
			if len(ids) != total {
				t.Fatalf("test setup: got %d ids, want %d", len(ids), total)
			}
			if len(chunkIDs(ids)) < 2 {
				t.Fatalf("test setup: %d ids did not split into more than one chunk", len(ids))
			}

			cols, values, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, ids)
			if err != nil {
				t.Fatalf("reading columns: %v", err)
			}

			var matches int
			for _, c := range cols {
				if c.FieldID == id {
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("the cost_centre column appears %d times across chunks, want exactly 1", matches)
			}

			byField := values[id]
			if got := byField[f.assetID]; got != "IT-42" {
				t.Fatalf("got %q for the first-chunk entity, want IT-42", got)
			}
			if got := byField[f.secondAssetID]; got != "IT-99" {
				t.Fatalf("got %q for the last-chunk entity, want IT-99 -- a per-chunk reset of "+
					"the values accumulator would have dropped it", got)
			}
		})
	}
}

// ---------- Ruling AN: every header carries its code, unconditionally ----------

// TestEveryHeaderCarriesItsCodeUnconditionally: a field with no colliding
// label still gets the code suffix. Ruling AM tried appending it only on a
// detected collision; that failed twice over (a self-collision with an
// already-code-suffixed label, and a new field silently renaming an existing
// one's header on arrival) and Ruling AN replaced it with an unconditional
// rule precisely so there is no "common case" left that reads differently
// from the collision case -- a header is a pure function of its own field,
// always.
func TestEveryHeaderCarriesItsCodeUnconditionally(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Label = "Cost Centre"
			if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
				t.Fatalf("relabelling: %v", err)
			}

			cols, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns: %v", err)
			}
			if len(cols) != 1 {
				t.Fatalf("got %d columns, want 1", len(cols))
			}
			if want := "Cost Centre (cost_centre)"; cols[0].Header != want {
				t.Fatalf("got header %q, want %q -- the code is appended unconditionally, "+
					"not only on a collision", cols[0].Header, want)
			}
		})
	}
}

// TestTwoFieldsSharingALabelGetDifferentHeadersByCode: custom_field.label
// carries no uniqueness constraint (design.md §2/§3, deliberately), so two
// live fields with different codes can share one label. Each header names
// its own field's code, so the two are distinguishable without either one
// being computed differently from the other.
func TestTwoFieldsSharingALabelGetDifferentHeadersByCode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			relabel := func(fieldID, label string) {
				t.Helper()
				row, err := f.s.GetCustomField(f.ctx, fieldID)
				if err != nil {
					t.Fatalf("reading %s: %v", fieldID, err)
				}
				row.Label = label
				if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
					t.Fatalf("relabelling %s: %v", fieldID, err)
				}
			}
			first := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			relabel(first, "Cost Centre")
			second := mustField(t, f, "asset", "cc", domain.CustomFieldText)
			relabel(second, "Cost Centre")

			cols, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns: %v", err)
			}
			if len(cols) != 2 {
				t.Fatalf("got %d columns, want 2", len(cols))
			}
			byField := make(map[string]string, 2)
			for _, c := range cols {
				byField[c.FieldID] = c.Header
			}
			if want := "Cost Centre (cost_centre)"; byField[first] != want {
				t.Fatalf("got header %q for %s, want %q", byField[first], first, want)
			}
			if want := "Cost Centre (cc)"; byField[second] != want {
				t.Fatalf("got header %q for %s, want %q", byField[second], second, want)
			}
		})
	}
}

// TestTwoRetiredFieldsSharingALabelReadAsLabelRetiredCode documents the
// answer to "what does a collided RETIRED column read as": the retired
// marker, the retirement date, and the code suffix all apply, in that order
// -- "Label (retired YYYY-MM-DD) (code)" -- for each field, unconditionally.
// These two fields differ by code as well as by date, so Ruling AN's code
// suffix alone would already distinguish them; TestTwoRetiredFieldsSharing
// CodeAndLabelOnDifferentDaysGetDistinctHeaders below is the sharper case,
// where the code is identical too and only the date is left to do the work.
func TestTwoRetiredFieldsSharingALabelReadAsLabelRetiredCode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			relabel := func(fieldID, label string) {
				t.Helper()
				row, err := f.s.GetCustomField(f.ctx, fieldID)
				if err != nil {
					t.Fatalf("reading %s: %v", fieldID, err)
				}
				row.Label = label
				if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
					t.Fatalf("relabelling %s: %v", fieldID, err)
				}
			}
			first := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			relabel(first, "Cost Centre")
			second := mustField(t, f, "asset", "cc", domain.CustomFieldText)
			relabel(second, "Cost Centre")
			if err := f.s.RetireCustomField(f.ctx, f.actor, first); err != nil {
				t.Fatalf("retiring %s: %v", first, err)
			}
			if err := f.s.RetireCustomField(f.ctx, f.actor, second); err != nil {
				t.Fatalf("retiring %s: %v", second, err)
			}
			firstRow, err := f.s.GetCustomField(f.ctx, first)
			if err != nil {
				t.Fatalf("re-reading %s: %v", first, err)
			}
			secondRow, err := f.s.GetCustomField(f.ctx, second)
			if err != nil {
				t.Fatalf("re-reading %s: %v", second, err)
			}
			if firstRow.RetiredAt == nil || secondRow.RetiredAt == nil {
				t.Fatal("retired_at is nil after retiring")
			}

			cols, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns: %v", err)
			}
			byField := make(map[string]string, 2)
			for _, c := range cols {
				byField[c.FieldID] = c.Header
			}
			if want := "Cost Centre (retired " + retiredDate(*firstRow.RetiredAt) + ") (cost_centre)"; byField[first] != want {
				t.Fatalf("got header %q for %s, want %q", byField[first], first, want)
			}
			if want := "Cost Centre (retired " + retiredDate(*secondRow.RetiredAt) + ") (cc)"; byField[second] != want {
				t.Fatalf("got header %q for %s, want %q", byField[second], second, want)
			}
		})
	}
}

// TestALiveAndARetiredFieldSharingALabelGetDifferentHeaders: a live field and
// a same-labelled retired field are already distinguishable, each still
// carrying its own code and, for the retired one, its retirement date.
func TestALiveAndARetiredFieldSharingALabelGetDifferentHeaders(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			relabel := func(fieldID, label string) {
				t.Helper()
				row, err := f.s.GetCustomField(f.ctx, fieldID)
				if err != nil {
					t.Fatalf("reading %s: %v", fieldID, err)
				}
				row.Label = label
				if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
					t.Fatalf("relabelling %s: %v", fieldID, err)
				}
			}
			live := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			relabel(live, "Cost Centre")
			retired := mustField(t, f, "asset", "cc", domain.CustomFieldText)
			relabel(retired, "Cost Centre")
			if err := f.s.RetireCustomField(f.ctx, f.actor, retired); err != nil {
				t.Fatalf("retiring %s: %v", retired, err)
			}
			retiredRow, err := f.s.GetCustomField(f.ctx, retired)
			if err != nil {
				t.Fatalf("re-reading %s: %v", retired, err)
			}
			if retiredRow.RetiredAt == nil {
				t.Fatal("retired_at is nil after retiring")
			}

			cols, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns: %v", err)
			}
			byField := make(map[string]string, 2)
			for _, c := range cols {
				byField[c.FieldID] = c.Header
			}
			if want := "Cost Centre (cost_centre)"; byField[live] != want {
				t.Fatalf("got header %q for the live field, want %q", byField[live], want)
			}
			if want := "Cost Centre (retired " + retiredDate(*retiredRow.RetiredAt) + ") (cc)"; byField[retired] != want {
				t.Fatalf("got header %q for the retired field, want %q", byField[retired], want)
			}
		})
	}
}

// ---------- Ruling AO: the retirement date, closing what AN's code suffix
// alone could not ----------

// TestTwoRetiredFieldsSharingCodeAndLabelOnDifferentDaysGetDistinctHeaders is
// the reviewer's case (a): design.md §6 explicitly touts reusing a code after
// retirement as intended behaviour, so retiring a field, then creating and
// retiring ANOTHER field with the identical code and label, is ordinary use.
// Before Ruling AO the two retired headers were byte-identical ("Label
// (retired) (code)" for both). The retirement date is controlled via
// SQLStore.WithClock so the two retirements land on different UTC days,
// which is what the ruling's fix actually closes -- the residual case it
// accepts (same code, same label, AND the same day) is deliberately not what
// this test exercises.
func TestTwoRetiredFieldsSharingCodeAndLabelOnDifferentDaysGetDistinctHeaders(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			relabel := func(fieldID, label string) {
				t.Helper()
				row, err := f.s.GetCustomField(f.ctx, fieldID)
				if err != nil {
					t.Fatalf("reading %s: %v", fieldID, err)
				}
				row.Label = label
				if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
					t.Fatalf("relabelling %s: %v", fieldID, err)
				}
			}

			first := mustField(t, f, "asset", "cc", domain.CustomFieldText)
			relabel(first, "Cost Centre")
			day1 := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
			onDay1 := f.s.WithClock(func() time.Time { return day1 })
			if err := onDay1.RetireCustomField(f.ctx, f.actor, first); err != nil {
				t.Fatalf("retiring first: %v", err)
			}

			// The code is free again the moment the first field is retired
			// (the partial unique index enforces uniqueness among LIVE rows
			// only), and design.md §6 calls that reuse intended, not abuse.
			second := mustField(t, f, "asset", "cc", domain.CustomFieldText)
			relabel(second, "Cost Centre")
			day2 := time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)
			onDay2 := f.s.WithClock(func() time.Time { return day2 })
			if err := onDay2.RetireCustomField(f.ctx, f.actor, second); err != nil {
				t.Fatalf("retiring second: %v", err)
			}

			cols, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns: %v", err)
			}
			if len(cols) != 2 {
				t.Fatalf("got %d columns, want 2", len(cols))
			}
			byField := make(map[string]string, 2)
			for _, c := range cols {
				byField[c.FieldID] = c.Header
			}
			if byField[first] == byField[second] {
				t.Fatalf("two retired fields sharing a code and a label produced identical "+
					"headers (%q) once retired on different days -- Ruling AO's retirement date "+
					"must distinguish them", byField[first])
			}
			if want := "Cost Centre (retired 2026-01-10) (cc)"; byField[first] != want {
				t.Fatalf("got header %q for the first field, want %q", byField[first], want)
			}
			if want := "Cost Centre (retired 2026-03-04) (cc)"; byField[second] != want {
				t.Fatalf("got header %q for the second field, want %q", byField[second], want)
			}
		})
	}
}

// TestALiveLabelImitatingTheRetiredMarkerDoesNotCollideWithARetiredField is
// the reviewer's case (b): before Ruling AO, a LIVE field labelled literally
// "Support (retired)" with a reused code rendered byte-identically to the
// RETIRED field labelled "Support" that had held that code before it --
// because the marker was plain text with nothing tying it to an actual
// retirement. The retirement date closes this completely: no live label a
// human can type also names the exact calendar day this OTHER field was
// retired on.
func TestALiveLabelImitatingTheRetiredMarkerDoesNotCollideWithARetiredField(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			relabel := func(fieldID, label string) {
				t.Helper()
				row, err := f.s.GetCustomField(f.ctx, fieldID)
				if err != nil {
					t.Fatalf("reading %s: %v", fieldID, err)
				}
				row.Label = label
				if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
					t.Fatalf("relabelling %s: %v", fieldID, err)
				}
			}

			retiredField := mustField(t, f, "asset", "support", domain.CustomFieldText)
			relabel(retiredField, "Support")
			if err := f.s.RetireCustomField(f.ctx, f.actor, retiredField); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			retiredRow, err := f.s.GetCustomField(f.ctx, retiredField)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if retiredRow.RetiredAt == nil {
				t.Fatal("retired_at is nil after retiring")
			}
			wantRetiredHeader := "Support (retired " + retiredDate(*retiredRow.RetiredAt) + ") (support)"

			// §6: the code is free for reuse once retired. This live field
			// deliberately imitates the bare marker in its own label.
			liveField := mustField(t, f, "asset", "support", domain.CustomFieldText)
			relabel(liveField, "Support (retired)")

			cols, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns: %v", err)
			}
			byField := make(map[string]string, 2)
			for _, c := range cols {
				byField[c.FieldID] = c.Header
			}
			if byField[retiredField] != wantRetiredHeader {
				t.Fatalf("got header %q for the retired field, want %q", byField[retiredField], wantRetiredHeader)
			}
			wantLiveHeader := "Support (retired) (support)"
			if byField[liveField] != wantLiveHeader {
				t.Fatalf("got header %q for the live field, want %q", byField[liveField], wantLiveHeader)
			}
			if byField[retiredField] == byField[liveField] {
				t.Fatalf("a live label imitating the retired marker collided with an actually "+
					"retired field's header: %q", byField[retiredField])
			}
		})
	}
}

// TestAddingASecondFieldDoesNotRenameTheFirstsHeader is the property Ruling
// AN exists for. Ruling AM's conditional disambiguation made a field's header
// depend on what ELSE was defined: a second field arriving with a colliding
// label silently renamed the first field's header from "Label" to
// "Label (code)", with nothing about the first field having changed -- which
// a diff, or any header-keyed tool, reads as a column removed and another
// added. A header built unconditionally from a field's own code cannot move
// when an unrelated field is created.
func TestAddingASecondFieldDoesNotRenameTheFirstsHeader(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			first := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			row, err := f.s.GetCustomField(f.ctx, first)
			if err != nil {
				t.Fatalf("reading %s: %v", first, err)
			}
			row.Label = "Cost Centre"
			if err := f.s.UpdateCustomField(f.ctx, f.actor, &row.CustomField); err != nil {
				t.Fatalf("relabelling %s: %v", first, err)
			}

			before, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns before: %v", err)
			}
			if len(before) != 1 {
				t.Fatalf("got %d columns before, want 1", len(before))
			}
			headerBefore := before[0].Header

			// A second field arrives, sharing the first field's label.
			second := mustField(t, f, "asset", "cc", domain.CustomFieldText)
			row2, err := f.s.GetCustomField(f.ctx, second)
			if err != nil {
				t.Fatalf("reading %s: %v", second, err)
			}
			row2.Label = "Cost Centre"
			if err := f.s.UpdateCustomField(f.ctx, f.actor, &row2.CustomField); err != nil {
				t.Fatalf("relabelling %s: %v", second, err)
			}

			after, _, err := f.s.customFieldColumns(f.ctx, domain.CustomFieldEntityAsset, []string{f.assetID})
			if err != nil {
				t.Fatalf("reading columns after: %v", err)
			}
			byField := make(map[string]string, len(after))
			for _, c := range after {
				byField[c.FieldID] = c.Header
			}
			if got := byField[first]; got != headerBefore {
				t.Fatalf("adding a second, colliding field renamed the first field's header: "+
					"was %q, now %q -- a header must be a pure function of its own field", headerBefore, got)
			}
		})
	}
}
