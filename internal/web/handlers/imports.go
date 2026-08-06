// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"io"
	"net/http"
	"strconv"

	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
)

// Bulk asset import.
//
// SUBMITTING APPLIES THE FILE. There is no two-step preview-then-confirm, and
// that is a decision rather than a shortcut: an HTTP response clears the file
// input, so any confirm step makes the operator find and select the same file a
// second time -- and the safety it would buy is already bought. The import is
// whole-file-or-nothing, so a file with a fault applies none of itself and
// returns the reasons. "Preview only" is there for the cautious, not as the
// thing standing between a bad file and the database.

// maxImportUpload bounds the multipart parse.
//
// It is below middleware.MaxRequestBody on purpose. The outer limit already
// caps the request, and nesting a LOOSER limit inside it would not raise the
// ceiling -- it would just move where the failure is reported to somewhere with
// a worse message. Roughly fifteen thousand asset rows.
const maxImportUpload = middleware.MaxRequestBody

// importKind is one import surface: what it creates, where it posts, and the
// columns it accepts.
//
// A struct rather than two near-identical handlers and two near-identical
// templates. The second copy is the one that stops refusing unknown columns.
type importKind struct {
	Slug    string
	Title   string
	Nav     string
	Lede    string
	Columns []importColumn
	Example string
	// run parses and applies a file. It returns a report, or the problems from
	// parsing, or an error -- the three outcomes are distinct and collapsing any
	// two of them is how an import surface starts lying.
	run func(*App, *http.Request, io.Reader, bool) (*store.ImportReport, []store.ImportProblem, error)
}

var assetImport = importKind{
	Slug:  "assets",
	Title: "Import assets",
	Nav:   "assets",
	Lede: "A CSV creates assets. It never updates one: a row naming something that " +
		"is already here is reported, not applied. The whole file lands or none of " +
		"it does, so a fault on line 300 leaves the first 299 unwritten rather than " +
		"half-imported.",
	Columns: importColumns,
	Example: "parent,name,kind\n,dc-a,site\ndc-a,rack-1,rack\ndc-a/rack-1,esx-01,hypervisor",
	run: func(a *App, r *http.Request, f io.Reader, dry bool) (*store.ImportReport, []store.ImportProblem, error) {
		rows, problems := store.ParseAssetCSV(f)
		if len(problems) > 0 {
			return nil, problems, nil
		}
		report, err := a.Store.ImportAssets(r.Context(), actor(r), rows, dry)
		return report, nil, err
	},
}

var deviceTypeImport = importKind{
	Slug:  "device-types",
	Title: "Import device types",
	Nav:   "catalogue",
	Lede: "A CSV catalogues models. Each one carries the manufacturer's end of " +
		"support, and every asset pointed at it inherits that date — so a hardware " +
		"list loaded here answers \"what lapses next year\" for the whole estate at once.",
	Columns: deviceTypeColumns,
	Example: "manufacturer,model,part_number,u_height,eol_date\ndell,R650,P30721-B21,1,2029-03-31\nhpe,DL380 Gen10,868703-B21,2,2028-12-31",
	run: func(a *App, r *http.Request, f io.Reader, dry bool) (*store.ImportReport, []store.ImportProblem, error) {
		rows, problems := store.ParseDeviceTypeCSV(f)
		if len(problems) > 0 {
			return nil, problems, nil
		}
		report, err := a.Store.ImportDeviceTypes(r.Context(), actor(r), rows, dry)
		return report, nil, err
	},
}

// deviceTypeColumns documents the catalogue file on the page that accepts it.
var deviceTypeColumns = []importColumn{
	{"manufacturer", true, "the maker's code, as catalogued — dell, hpe. It must already exist."},
	{"model", true, "the model name: R650"},
	{"part_number", false, "what procurement and support portals call it"},
	{"u_height", false, "rack units. Leave empty for anything that does not mount."},
	{"full_depth", false, "true/false, yes/no or 1/0"},
	{"eol_date", false, "the manufacturer's end of support, YYYY-MM-DD"},
	{"notes", false, ""},
	{"lifecycle", false, "defaults to active"},
}

// AssetImportForm renders the asset upload page.
func (a *App) AssetImportForm(w http.ResponseWriter, r *http.Request) {
	a.renderImport(w, r, assetImport, http.StatusOK, nil, nil)
}

// DeviceTypeImportForm renders the catalogue upload page.
func (a *App) DeviceTypeImportForm(w http.ResponseWriter, r *http.Request) {
	a.renderImport(w, r, deviceTypeImport, http.StatusOK, nil, nil)
}

// DeviceTypeImportRun applies an uploaded catalogue file.
func (a *App) DeviceTypeImportRun(w http.ResponseWriter, r *http.Request) {
	a.runImport(w, r, deviceTypeImport)
}

// AssetImportRun parses an uploaded file and applies it, or explains why not.
func (a *App) AssetImportRun(w http.ResponseWriter, r *http.Request) {
	a.runImport(w, r, assetImport)
}

func (a *App) runImport(w http.ResponseWriter, r *http.Request, kind importKind) {
	if err := r.ParseMultipartForm(maxImportUpload); err != nil {
		// NAMED, not a bare 400. The commonest cause by far is a file over the
		// limit, and "Bad Request" sends somebody to look at their CSV syntax.
		a.renderImport(w, r, kind, http.StatusUnprocessableEntity,
			[]store.ImportProblem{{
				Message: "that upload could not be read. The most likely reason is that the " +
					"file is larger than 1 MiB, which is about fifteen thousand rows.",
			}}, nil)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		a.renderImport(w, r, kind, http.StatusUnprocessableEntity,
			[]store.ImportProblem{{Message: "choose a CSV file to import."}}, nil)
		return
	}
	defer file.Close()

	dryRun := formValue(r, "dry_run") != ""
	report, problems, err := kind.run(a, r, file, dryRun)
	if len(problems) > 0 {
		a.renderImport(w, r, kind, http.StatusUnprocessableEntity, problems, nil)
		return
	}
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	report.Filename = header.Filename

	if len(report.Problems) > 0 {
		// 422 with the reasons, like every other refused form in this codebase.
		a.renderImport(w, r, kind, http.StatusUnprocessableEntity, nil, report)
		return
	}
	if report.Applied() {
		a.setFlash(r, "success", pluralised(len(report.Created), "row imported", "rows imported"))
	}
	a.renderImport(w, r, kind, http.StatusOK, nil, report)
}

// importPage is the upload screen and, once there is one, its report.
type importPage struct {
	Base
	Kind     importKind
	Report   *store.ImportReport
	Problems []store.ImportProblem
	Columns  []importColumn
}

type importColumn struct {
	Name     string
	Required bool
	Note     string
}

// importColumns documents the file format on the page that accepts it.
//
// Here rather than only in the docs because the person about to build a CSV is
// looking at this screen, and a format described somewhere else is a format
// somebody guesses at.
var importColumns = []importColumn{
	{"name", true, "what the asset is called"},
	{"kind", true, "site, rack, server, vm, switch…"},
	{"parent", false, "the containing asset, as a path: dc-a/rack-1. Empty means top level."},
	{"serial", false, "manufacturer serial number"},
	{"asset_tag", false, "your own asset tag"},
	{"vendor", false, ""},
	{"model", false, ""},
	{"lifecycle", false, "defaults to active"},
	{"eol_date", false, "YYYY-MM-DD"},
	{"environments", false, "environment codes, comma separated: prod,dr"},
	{"team", false, "the owning team's code — a team, never a person"},
	{"manager_role", false, "the capacity that team holds: owner, operator…  needs a team"},
}

func (a *App) renderImport(w http.ResponseWriter, r *http.Request, kind importKind, status int,
	problems []store.ImportProblem, report *store.ImportReport) {

	page := importPage{
		Base:     a.base(r, kind.Title, kind.Nav),
		Kind:     kind,
		Report:   report,
		Problems: problems,
		Columns:  kind.Columns,
	}
	a.Render.Page(w, status, "import", page)
}

// pluralised is the flash wording, kept here because the two forms differ by
// more than an "s" in several languages this may yet be translated into.
func pluralised(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
