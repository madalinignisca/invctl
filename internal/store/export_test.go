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
			col := indexOf(table.Header, "Cost Centre")
			if col < 0 {
				t.Fatalf("no Cost Centre column; header is %v", table.Header)
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

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}

			if got := indexOf(table.Header, "Cost Centre"); got >= 0 {
				t.Fatalf("the live header %q is still present after retirement; header is %v",
					"Cost Centre", table.Header)
			}
			col := indexOf(table.Header, "Cost Centre (retired)")
			if col < 0 {
				t.Fatalf(`no exact "Cost Centre (retired)" column; header is %v`, table.Header)
			}
			for i, h := range table.Header {
				if h != "Cost Centre (retired)" && strings.Contains(h, "Cost Centre") {
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
			col := indexOf(table.Header, "note")
			if col < 0 {
				t.Fatalf("no note column; header is %v", table.Header)
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
			col := indexOf(table.Header, "cost_centre")
			if col < 0 {
				t.Fatalf("no cost_centre column; header is %v", table.Header)
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
			if col := indexOf(table.Header, "owner"); col >= 0 {
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
			col := indexOf(table.Header, "owner")
			if col < 0 {
				t.Fatalf("no owner column; header is %v", table.Header)
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
