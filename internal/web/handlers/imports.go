// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
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

// AssetImportForm renders the upload page.
func (a *App) AssetImportForm(w http.ResponseWriter, r *http.Request) {
	a.renderImport(w, r, http.StatusOK, nil, nil)
}

// AssetImportRun parses an uploaded file and applies it, or explains why not.
func (a *App) AssetImportRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxImportUpload); err != nil {
		// NAMED, not a bare 400. The commonest cause by far is a file over the
		// limit, and "Bad Request" sends somebody to look at their CSV syntax.
		a.renderImport(w, r, http.StatusUnprocessableEntity,
			[]store.ImportProblem{{
				Message: "that upload could not be read. The most likely reason is that the " +
					"file is larger than 1 MiB, which is about fifteen thousand rows.",
			}}, nil)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		a.renderImport(w, r, http.StatusUnprocessableEntity,
			[]store.ImportProblem{{Message: "choose a CSV file to import."}}, nil)
		return
	}
	defer file.Close()

	rows, problems := store.ParseAssetCSV(file)
	if len(problems) > 0 {
		a.renderImport(w, r, http.StatusUnprocessableEntity, problems, nil)
		return
	}

	dryRun := formValue(r, "dry_run") != ""
	report, err := a.Store.ImportAssets(r.Context(), actor(r), rows, dryRun)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	report.Filename = header.Filename

	if len(report.Problems) > 0 {
		// 422 with the reasons, like every other refused form in this codebase.
		a.renderImport(w, r, http.StatusUnprocessableEntity, nil, report)
		return
	}
	if report.Applied() {
		a.setFlash(r, "success", pluralised(len(report.Created), "asset imported", "assets imported"))
	}
	a.renderImport(w, r, http.StatusOK, nil, report)
}

// importPage is the upload screen and, once there is one, its report.
type importPage struct {
	Base
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
}

func (a *App) renderImport(w http.ResponseWriter, r *http.Request, status int,
	problems []store.ImportProblem, report *store.ImportReport) {

	page := importPage{
		Base:     a.base(r, "Import assets", "assets"),
		Report:   report,
		Problems: problems,
		Columns:  importColumns,
	}
	a.Render.Page(w, status, "import_assets", page)
}

// pluralised is the flash wording, kept here because the two forms differ by
// more than an "s" in several languages this may yet be translated into.
func pluralised(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
