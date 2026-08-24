// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/csvsafe"
	"github.com/madalinignisca/invctl/internal/domain"
)

// This is the property ExportAssets and ParseAssetCSV each claim on their own
// doc comments -- "renders an asset list as an importable table" on one side,
// "an unknown column IS an error" on the other -- and that no test had ever
// executed in either direction until now. That gap is exactly how the defect
// this file guards against shipped: eight review gates and a whole-branch
// review all read both comments as true without either one being run against
// the other.
//
// csvBytes turns a Table into the same bytes render.CSV would write to an
// HTTP response, WITHOUT importing internal/web/render. Table declares its
// own render.Table interface specifically so the web layer need not import
// this package's concrete type (see export.go's comment on Table); importing
// render FROM here would run that one-way dependency backwards for the sake
// of a test helper, so this reproduces render.CSV's write loop -- header,
// then every row, every cell defused through csvsafe.Cell exactly the way
// render.CSV defuses it at the boundary where text becomes a file -- rather
// than reach for the package that already does it.
func csvBytes(t *testing.T, table Table) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	write := func(row []string) {
		safe := make([]string, len(row))
		for i, cell := range row {
			safe[i] = csvsafe.Cell(cell)
		}
		if err := w.Write(safe); err != nil {
			t.Fatalf("writing csv row %v: %v", row, err)
		}
	}
	write(table.Header)
	for _, row := range table.Rows {
		write(row)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("flushing csv: %v", err)
	}
	return buf.Bytes()
}

// TestExportAssetsRoundTripsThroughTheImporter feeds ExportAssets' own output
// into ParseAssetCSV and asserts the importer accepts every column and
// recovers the same values that went out.
//
// RUN AGAINST A FIXTURE WITH A CUSTOM FIELD DEFINED AND POPULATED,
// deliberately -- not because ExportAssets is meant to carry custom-field
// values (it is not; see ExportAssetCustomFields and this file's sibling
// test below), but because an estate with zero custom fields would never
// have exposed the defect this test exists to catch: an earlier version of
// ExportAssets appended one column per custom field, assetImportColumns
// never accepted them, and every asset export of an estate holding even one
// custom field failed to load back through its own importer. An empty
// fixture would have passed either way and proved nothing.
func TestExportAssetsRoundTripsThroughTheImporter(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) == 0 {
				t.Fatal("fixture produced no assets; the round trip would pass vacuously")
			}

			table, err := f.s.ExportAssets(f.ctx, rows)
			if err != nil {
				t.Fatalf("exporting: %v", err)
			}

			parsed, problems := ParseAssetCSV(bytes.NewReader(csvBytes(t, table)))
			if len(problems) > 0 {
				t.Fatalf("ExportAssets produced a file ParseAssetCSV refuses -- the exact defect "+
					"this test exists to catch: %+v\nheader: %v", problems, table.Header)
			}
			if len(parsed) != len(rows) {
				t.Fatalf("got %d parsed rows, want %d", len(parsed), len(rows))
			}

			byName := make(map[string]AssetImportRow, len(parsed))
			for _, p := range parsed {
				byName[p.Path()] = p
			}
			for _, a := range rows {
				wantPath := a.Name // fixture assets are all top-level: no parent segment
				p, ok := byName[wantPath]
				if !ok {
					t.Fatalf("asset %q did not survive the round trip; parsed paths: %v",
						wantPath, keysOf(byName))
				}
				if p.Kind != a.Kind {
					t.Errorf("asset %q: got kind %q back, want %q", wantPath, p.Kind, a.Kind)
				}
				wantEnvs := make([]string, 0, len(a.Environments))
				for _, env := range a.Environments {
					wantEnvs = append(wantEnvs, env.Code)
				}
				gotEnvs := []string{}
				if p.Environments != "" {
					gotEnvs = strings.Split(p.Environments, ",")
				}
				if strings.Join(gotEnvs, ",") != strings.Join(wantEnvs, ",") {
					t.Errorf("asset %q: got environments %v back, want %v", wantPath, gotEnvs, wantEnvs)
				}
			}
		})
	}
}

// keysOf lists a parsed-row map's keys for a failure message -- so
// "did not survive the round trip" points at what actually came back rather
// than leaving the reader to guess.
func keysOf(m map[string]AssetImportRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestServiceExportCarriesNoCustomFieldColumn is ExportServices' side of the
// same guarantee, in the shape actually available: there is no
// ParseServiceCSV in this codebase (services have no importer at all yet),
// so there is nothing to feed ExportServices' output into. What DOES hold,
// and what this test enforces so it cannot regress unnoticed if a service
// importer is ever added, is that ExportServices' header is exactly its
// fixed, hand-declared column set -- the custom-field columns an earlier
// version of this function appended are gone, moved to
// ExportServiceCustomFields, and nothing here can silently grow the
// "importable" set again by accident.
func TestServiceExportCarriesNoCustomFieldColumn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "service", "owner", domain.CustomFieldText)
			mustValue(t, f, id, f.serviceID, "platform-team")

			rows, err := f.s.ListServices(f.ctx, ServiceFilter{})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			table := ExportServices(rows)
			want := []string{"code", "name", "tier", "availability", "environment", "team", "lifecycle"}
			if len(table.Header) != len(want) {
				t.Fatalf("got header %v (%d columns), want exactly %v (%d columns) -- "+
					"a custom-field column leaked back onto the importable-shaped export",
					table.Header, len(table.Header), want, len(want))
			}
			for i := range want {
				if table.Header[i] != want[i] {
					t.Fatalf("column %d: got %q, want %q", i, table.Header[i], want[i])
				}
			}
		})
	}
}
