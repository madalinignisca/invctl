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

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/web/render"

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
	// parse reads a file into rows without touching the database, so a
	// background job is queued only for a file that is at least well formed.
	parse func(io.Reader) ([]store.AssetImportRow, []store.DeviceTypeImportRow, []store.ImportProblem, error)
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
	parse: func(f io.Reader) ([]store.AssetImportRow, []store.DeviceTypeImportRow, []store.ImportProblem, error) {
		rows, problems := store.ParseAssetCSV(f)
		return rows, nil, problems, nil
	},
	run: func(a *App, r *http.Request, f io.Reader, dry bool) (*store.ImportReport, []store.ImportProblem, error) {
		rows, problems := store.ParseAssetCSV(f)
		if len(problems) > 0 {
			return nil, problems, nil
		}
		report, err := a.Store.ImportAssets(r.Context(), permit(r), rows, dry)
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
	parse: func(f io.Reader) ([]store.AssetImportRow, []store.DeviceTypeImportRow, []store.ImportProblem, error) {
		rows, problems := store.ParseDeviceTypeCSV(f)
		return nil, rows, problems, nil
	},
	run: func(a *App, r *http.Request, f io.Reader, dry bool) (*store.ImportReport, []store.ImportProblem, error) {
		rows, problems := store.ParseDeviceTypeCSV(f)
		if len(problems) > 0 {
			return nil, problems, nil
		}
		report, err := a.Store.ImportDeviceTypes(r.Context(), permit(r), rows, dry)
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

	// A PREVIEW STAYS SYNCHRONOUS. It is the operator standing there asking a
	// question, it writes nothing, and answering it on another page they have to
	// go and watch would be worse than the wait. A real run is the one that
	// takes seven seconds per five thousand rows and outlives the request.
	if dryRun {
		report, problems, err := kind.run(a, r, file, true)
		if len(problems) > 0 {
			a.renderImport(w, r, kind, http.StatusUnprocessableEntity, problems, nil)
			return
		}
		if err != nil {
			a.handleStoreError(w, r, err)
			return
		}
		report.Filename = header.Filename
		status := http.StatusOK
		if len(report.Problems) > 0 {
			status = http.StatusUnprocessableEntity
		}
		a.renderImport(w, r, kind, status, nil, report)
		return
	}

	// Parsed HERE, in the request, so a malformed file is refused while the
	// operator is still looking at the form rather than becoming a job that
	// fails two seconds later on a page they have to find.
	rows, types, problems, err := kind.parse(file)
	if len(problems) > 0 {
		a.renderImport(w, r, kind, http.StatusUnprocessableEntity, problems, nil)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	total := len(rows) + len(types)
	if total == 0 {
		a.renderImport(w, r, kind, http.StatusUnprocessableEntity,
			[]store.ImportProblem{{Message: "the file has a header but no rows"}}, nil)
		return
	}

	who := actor(r)
	// Minted HERE, once, from whoever is signed in for this request -- see
	// importWork.permit's doc comment for why the runner must not do this
	// itself later, on context.Background(), with nothing to derive it from.
	permit := a.Authz.Permit(middleware.UserFrom(r.Context()))
	job := store.ImportJob{
		ID: store.NewID(), Kind: kind.Slug, Filename: header.Filename,
		Actor: who.ID, ActorKind: who.Kind, Status: store.ImportQueued,
		RowsTotal: total, CreatedAt: domain.FormatTime(a.Store.Now()),
	}
	if err := a.Store.CreateImportJob(r.Context(), &job); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.importer().submit(importWork{job: job, assets: rows, types: types, actor: who, permit: permit})
	render.Redirect(w, r, "/imports/"+job.ID)
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
	{"device_type", false, "a catalogued model as manufacturer/model — dell/PowerEdge R650. Inherits its end-of-support date."},
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

// ---------- watching a job ----------

type importJobPage struct {
	Base
	Job  store.ImportJob
	Poll int // seconds between refreshes, for the template's hx-trigger
}

type importJobListPage struct {
	Base
	Jobs []store.ImportJob
}

// ImportJobPage shows one run, refreshing itself while it is going.
func (a *App) ImportJobPage(w http.ResponseWriter, r *http.Request) {
	job, err := a.Store.GetImportJob(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	// The live figure comes from the runner, not the row: a running import
	// holds the single SQLite writer, so nothing can update the row until it is
	// finished. See importRunner.
	if n, running := a.importer().progressOf(job.ID); running {
		job.RowsDone = n
	}

	// Respond, not Page: HTMX asks for the fragment on each poll and a browser
	// with JavaScript off gets the whole page and a meta refresh. Both work.
	a.Render.Respond(w, r, http.StatusOK, "import_job", "import_job_status", importJobPage{
		Base: a.base(r, "Import", "assets"),
		Job:  *job,
		Poll: int(pollEvery.Seconds()),
	})
}

// ImportJobList shows recent runs.
func (a *App) ImportJobList(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.Store.ListImportJobs(r.Context(), 50)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, http.StatusOK, "import_jobs", "import_job_list", importJobListPage{
		Base: a.base(r, "Imports", "assets"),
		Jobs: jobs,
	})
}
