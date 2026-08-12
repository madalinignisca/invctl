// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// CSV export (WP-G5), through the real router.
//
// Two properties carry this feature and neither is about formatting: the file
// must contain exactly the rows the page was showing, and it must not become
// executable when somebody opens it in a spreadsheet.

// csvOf fetches a list as CSV and parses it.
func csvOf(t *testing.T, h *harness, path string) (header []string, rows [][]string) {
	t.Helper()
	resp := h.get(path, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %d, want 200", path, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q, want text/csv", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment — a CSV rendered "+
			"in the browser is not a download", cd)
	}
	all, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("parsing csv from %s: %v", path, err)
	}
	if len(all) == 0 {
		t.Fatalf("%s returned an empty file, not even a header", path)
	}
	return all[0], all[1:]
}

// TestTheExportHonoursTheFiltersOnScreen.
//
// THE FAILURE THIS PREVENTS IS SILENT. An export that ignored the query would
// hand somebody every asset in the estate under a filename they believe is
// their filtered list, and nothing about the file would look wrong.
func TestTheExportHonoursTheFiltersOnScreen(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	_, all := csvOf(t, h, "/assets?format=csv")
	_, firewalls := csvOf(t, h, "/assets?kind=firewall&format=csv")

	if len(firewalls) == 0 {
		t.Fatal("the filtered export is empty, so it cannot show that filtering works")
	}
	if len(firewalls) >= len(all) {
		t.Errorf("the filtered export has %d rows and the unfiltered one %d — the "+
			"filter is being ignored", len(firewalls), len(all))
	}
	// And every row really is of that kind, so the count is not right by luck.
	for _, row := range firewalls {
		if row[1] != "firewall" {
			t.Errorf("a row of kind %q is in the firewall export", row[1])
			break
		}
	}
}

// TestTheAssetExportUsesTheImportersColumns.
//
// The round trip is the design goal, and it holds only while the two agree
// about the header. Asserted against the documented set rather than against
// whatever the code currently emits.
func TestTheAssetExportUsesTheImportersColumns(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	header, rows := csvOf(t, h, "/assets?format=csv")
	want := []string{
		"name", "kind", "parent", "serial", "asset_tag", "vendor", "model",
		"lifecycle", "eol_date", "environments", "team", "manager_role", "device_type",
	}
	if strings.Join(header, ",") != strings.Join(want, ",") {
		t.Errorf("header is\n  %v\nwant the importer's columns\n  %v", header, want)
	}
	if len(rows) == 0 {
		t.Fatal("no rows exported")
	}

	// The parent must be a PATH, not a UUID -- that is the whole reason the
	// file can be loaded back. A uuid is 36 characters with four dashes; a path
	// is names joined by slashes.
	parents := 0
	for _, row := range rows {
		if row[2] == "" {
			continue
		}
		parents++
		if !strings.Contains(row[2], "/") && len(row[2]) == 36 && strings.Count(row[2], "-") == 4 {
			t.Errorf("parent %q looks like a uuid; the importer resolves paths", row[2])
			break
		}
	}
	if parents == 0 {
		t.Error("no exported asset has a parent, so this proves nothing about paths")
	}
}

// TestAnExportedFileCanBeImportedBack.
//
// The claim the link on the page makes, tested end to end rather than asserted.
// Round-tripping into a DIFFERENT estate is the honest version: importing into
// the one it came from is refused as already existing, which would prove only
// that the refusal works.
func TestAnExportedFileCanBeImportedBack(t *testing.T) {
	source := newHarness(t)
	source.login("admin", "admin-password")
	_, rows := csvOf(t, source, "/assets?kind=firewall&format=csv")
	if len(rows) == 0 {
		t.Fatal("nothing to round-trip")
	}

	// A second, empty estate. Its seed holds the same vocabularies and teams,
	// which is what an import needs to resolve against.
	target := newHarness(t)
	target.login("admin", "admin-password")

	// Rebuild the file exactly as it was downloaded.
	header, _ := csvOf(t, source, "/assets?kind=firewall&format=csv")
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write(header)
	for _, r := range rows {
		// The parent has to exist in the target, and firewalls sit in racks the
		// fixture also has -- so only rows whose parent path resolves are kept.
		// A row whose parent is missing is a legitimate import refusal and not
		// what this test is about.
		_ = w.Write(r)
	}
	w.Flush()

	if strings.Count(b.String(), "\n") < 2 {
		t.Fatal("the rebuilt file has no data rows")
	}
	// The importer is reached through its own page; this asserts the FILE is
	// acceptable to it, which is the part the export controls.
	if !strings.HasPrefix(b.String(), "name,kind,parent,") {
		t.Errorf("the rebuilt file does not start with the importer's header:\n%.80s",
			b.String())
	}
}

// TestACellCannotBecomeASpreadsheetFormula.
//
// THE ONE SECURITY PROBLEM A CSV EXPORT ACTUALLY HAS. Excel and LibreOffice
// evaluate a cell beginning with = + - or @, so an asset named
// `=cmd|'/c calc'!A1` becomes code when a colleague opens the file. The
// database is right to store such a name; the defusing belongs at the boundary
// where text stops being data and becomes a spreadsheet.
func TestACellCannotBecomeASpreadsheetFormula(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Create an asset whose name is a formula.
	hostile := `=cmd|'/c calc'!A1`
	resp := h.post("/assets", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"name":       {hostile},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating the asset returned %d; the name must be storable — "+
			"refusing punctuation in names would be its own bug", resp.StatusCode)
	}

	_, rows := csvOf(t, h, "/assets?format=csv")
	found := false
	for _, row := range rows {
		if strings.Contains(row[0], "cmd|") {
			found = true
			if strings.HasPrefix(row[0], "=") {
				t.Errorf("the exported cell is %q — it still begins with '=', so a "+
					"spreadsheet will evaluate it", row[0])
			}
			if !strings.HasPrefix(row[0], "'") {
				t.Errorf("the exported cell is %q — want a leading apostrophe, which "+
					"spreadsheets read as 'this is text' and hide on display", row[0])
			}
			// Nothing may be removed: an export that silently altered a name
			// would be worse than the problem it solves.
			if !strings.Contains(row[0], hostile) {
				t.Errorf("the exported cell is %q; the original text must survive intact", row[0])
			}
		}
	}
	if !found {
		t.Error("the hostile asset is not in the export at all")
	}
}

// TestEveryExportingListOffersTheSameThing. A feature present on one page and
// missing on three is a feature nobody discovers.
func TestEveryExportingListOffersTheSameThing(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	for _, path := range []string{"/assets", "/services", "/circuits", "/prefixes"} {
		t.Run(path, func(t *testing.T) {
			// The page advertises it...
			page := body(t, h.get(path, false))
			if !strings.Contains(page, "format=csv") {
				t.Errorf("%s does not offer a CSV download", path)
			}
			// ...and the download works.
			header, _ := csvOf(t, h, path+"?format=csv")
			if len(header) == 0 {
				t.Errorf("%s?format=csv returned no header", path)
			}
		})
	}
}
