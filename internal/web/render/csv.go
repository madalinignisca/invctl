// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CSV downloads.
//
// NOT A JSON API AND NOT AN EXCEPTION TO THE RULE. The house rule is that the
// UI gets HTML and never JSON, and it exists so there are not two ways to
// render the same page that drift apart. A CSV is not a second rendering of a
// page -- it is a file somebody takes away and opens in a spreadsheet, and it
// is served from the same handler with the same filters, so it cannot disagree
// with what they were looking at.

// WantsCSV reports whether this request asked for a file rather than a page.
//
// A QUERY PARAMETER ON THE EXISTING ROUTE, not a route of its own. /assets.csv
// would need its own filter parsing, and the day the two implementations
// diverge is the day somebody exports a filtered list and gets everything.
// Sharing the handler makes that impossible rather than merely unlikely.
func WantsCSV(r *http.Request) bool {
	return strings.EqualFold(r.URL.Query().Get("format"), "csv")
}

// Table is what CSV renders. Declared here rather than imported from store so
// this package keeps its one-way dependency.
type Table interface {
	CSVName() string
	CSVHeader() []string
	CSVRows() [][]string
}

// CSV writes a downloadable file.
//
// The filename carries the date, because a downloads folder with four files
// called assets.csv is a folder nobody can use.
func CSV(w http.ResponseWriter, r *http.Request, t Table, now time.Time) {
	name := fmt.Sprintf("invctl-%s-%s.csv", t.CSVName(), now.UTC().Format("20060102"))

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// The filename is built from a constant and a date, never from user input,
	// so it cannot carry a quote or a newline into the header.
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// A spreadsheet opening this over a slow link should not be handed a
	// truncated file that looks complete, and nothing here is cacheable anyway:
	// it is a snapshot of a filtered list at one moment.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	// encoding/csv quotes anything containing a comma, a quote or a newline, so
	// a note or a description cannot break the shape of the file. It does NOT
	// defend a spreadsheet against a cell beginning with = or +, which is the
	// formula-injection problem -- see sanitise below.
	rows := append([][]string{t.CSVHeader()}, t.CSVRows()...)
	for _, row := range rows {
		safe := make([]string, len(row))
		for i, cell := range row {
			safe[i] = sanitise(cell)
		}
		if err := cw.Write(safe); err != nil {
			// The response is already begun, so this cannot become an error
			// status. Logged rather than swallowed: a truncated download is
			// exactly the failure somebody would otherwise not notice.
			slog.Error("writing csv", "error", err, "path", r.URL.Path)
			return
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		slog.Error("flushing csv", "error", err, "path", r.URL.Path)
	}
}

// sanitise defuses a cell a spreadsheet would treat as a formula.
//
// THE ONE SECURITY PROBLEM A CSV EXPORT ACTUALLY HAS. Excel and LibreOffice
// evaluate a cell beginning with = + - or @, so an asset somebody named
// `=cmd|'/c calc'!A1` becomes code the moment a colleague opens the file. The
// database is right to store it -- it is a name, and refusing punctuation in
// names would be its own bug -- so the defusing belongs here, at the boundary
// where the text stops being data and starts being a spreadsheet.
//
// A leading apostrophe is the conventional fix: spreadsheets read it as "treat
// the rest as text" and drop it on display, so the cell still reads correctly
// to a human. Nothing is removed, which matters -- an export that silently
// altered a name would be worse than the problem.
func sanitise(cell string) string {
	if cell == "" {
		return cell
	}
	switch cell[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + cell
	}
	return cell
}
