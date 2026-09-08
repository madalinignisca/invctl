// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// THIS TEST WAS CITED BEFORE IT EXISTED. internal/web/handlers/app.go's
// submittedVersion says a form arriving with no token falls back to the stored
// value -- no protection rather than a refusal -- and justifies it like this:
//
//	"That is only safe because TestEveryEditFormCarriesItsVersion enumerates
//	the forms and fails if one stops emitting it, so the fallback cannot
//	quietly become the normal path."
//
// The reasoning is right and the test was never written. So every edit form's
// optimistic-concurrency guard has been resting on a citation, and a form that
// forgot the token would work perfectly: the save succeeds, the guard is gone,
// and the next concurrent edit is a blind overwrite of somebody else's.
//
// Found while adding six new correction forms at once -- exactly the moment the
// missing census would have cost something.
//
// WHY STATIC AND NOT BEHAVIOURAL. Driving every edit form through the browser
// would need a fixture in the right state for each of thirty-odd routes, and
// the ones that are hardest to set up are the ones most likely to be missed --
// so the coverage would thin out precisely where it matters. Reading the
// templates covers all of them at once and cannot be satisfied by a route that
// happens to be untested.

// versionExemptRoutes are Update routes whose form legitimately carries no
// row_version, with the reason.
//
// A backlog, like correctionPathsNotYetBuilt: an entry is a claim that this
// particular write does not need the guard, and it has to be argued rather
// than assumed. Empty is the goal.
var versionExemptRoutes = map[string]string{}

// routesServedByAComputedAction are versioned routes this scan cannot attribute
// to a form, mapped to the template that actually serves them.
//
// That shape is deliberate and good: one partial serves create and correct for
// the same entity, so its action is `action="{{.Action}}"`, filled in by Go.
// The markup simply does not say which route it posts to.
//
// THE VALUE IS THE PROOF, NOT A PROMISE. The first version of this list carried
// prose naming a behavioural test for each entry -- and three of the four names
// were of tests that DO NOT EXIST (one was invented outright; another named a
// template, `endpoint_edit_form`, that is really `endpoint_form`). In a file
// written because `submittedVersion` cited a guard nobody had written, that is
// the same defect reproduced inside its own fix, which is the pattern
// docs/power-cost-design.md records and names.
//
// So an entry is now a TEMPLATE PATH the test opens and checks: the named file
// must contain a form carrying the token. An entry that goes stale fails here
// instead of reassuring the next reader. The behavioural half is
// TestAStaleFormIsRefusedOnAComputedActionForm below, which drives these routes
// through their real rendered forms.
var routesServedByAComputedAction = map[string]string{
	"POST /assets/{id}":                  "partials/forms.html",
	"POST /services/{id}":                "partials/forms.html",
	"POST /endpoints/{id}":               "partials/forms.html",
	"POST /projects/{id}":                "partials/projects.html",
	"POST /assets/{id}/tags":             "partials/entity_tags_form.html",
	"POST /services/{id}/tags":           "partials/entity_tags_form.html",
	"POST /projects/{id}/tags":           "partials/entity_tags_form.html",
	"POST /assets/{id}/custom-fields":    "partials/custom_fields_form.html",
	"POST /services/{id}/custom-fields":  "partials/custom_fields_form.html",
	"POST /assets/{id}/costs/{costID}":   "partials/costs.html",
	"POST /services/{id}/costs/{costID}": "partials/costs.html",
	"POST /circuits/{id}/costs/{costID}": "partials/costs.html",
	"POST /projects/{id}/costs/{costID}": "partials/costs.html",
}

func TestEveryEditFormCarriesItsVersion(t *testing.T) {
	root := repoRoot(t)

	versionedRoutes := versionedRoutePatterns(t, root)
	// A positive control, for the reason every census in this repository has
	// one: a scan that matches nothing reports success.
	if len(versionedRoutes) < 20 {
		t.Fatalf("found only %d version-reading routes in the census; the parse has stopped "+
			"matching the file and this test is checking nothing", len(versionedRoutes))
	}

	forms := editFormsInTemplates(t, root)
	if len(forms) < 10 {
		t.Fatalf("found only %d posting forms across the templates; the scan has "+
			"stopped matching the markup", len(forms))
	}

	seen := map[string]bool{}
	for _, f := range forms {
		for _, route := range matchRoutes(versionedRoutes, f.action) {
			seen[route] = true
			if why, exempt := versionExemptRoutes[route]; exempt {
				if f.hasVersion {
					t.Errorf("%s (%s) carries row_version but is listed as exempt: %q. "+
						"Delete the entry.", route, f.file, why)
				}
				continue
			}
			if !f.hasVersion {
				t.Errorf("the form posting to %s in %s carries no %q input.\n"+
					"submittedVersion falls back to the stored value when the token is "+
					"absent, so this form saves happily and silently has NO "+
					"optimistic-concurrency guard: the next concurrent edit blindly "+
					"overwrites whatever somebody else just saved. Add the hidden input, "+
					"or add the route to versionExemptRoutes with the reason.",
					route, f.file, "row_version")
			}
		}
	}

	// THE OTHER DIRECTION, and the half that keeps this test honest. A route
	// no form posts to is invisible to everything above, so the scan could
	// quietly stop matching most of the markup and still pass. Every such
	// route has to be argued in routesServedByAComputedAction, naming where
	// its guard IS proven -- and a newly-unmatched route fails here rather
	// than scrolling past in a log line.
	for _, route := range versionedRoutes {
		if seen[route] {
			if why, listed := routesServedByAComputedAction[route]; listed {
				t.Errorf("%s IS matched by a template form now, but is still listed as "+
					"served by a computed action: %q. Delete the entry.", route, why)
			}
			continue
		}
		if tmpl, listed := routesServedByAComputedAction[route]; listed {
			// The entry is checked, not believed: the named template must
			// exist and must actually emit the token.
			raw, err := os.ReadFile(filepath.Join(root, "web", "templates", tmpl))
			if err != nil {
				t.Errorf("%s is listed as served by %s, and that template cannot be "+
					"read: %v. A pointer to a file that is not there is worse than no "+
					"entry -- it reads as proof.", route, tmpl, err)
				continue
			}
			if !strings.Contains(string(raw), `name="row_version"`) {
				t.Errorf("%s is listed as served by %s, and that template emits no "+
					"row_version input at all. Either the token was lost -- in which "+
					"case every form it serves now saves with no concurrency guard -- "+
					"or this entry is pointing at the wrong file.", route, tmpl)
			}
			continue
		}
		t.Errorf("no template form posts to %s, so nothing here checks that its "+
			"correction form carries row_version. Either the form's action is "+
			"computed in Go -- add it to routesServedByAComputedAction naming the "+
			"test that drives its token -- or this route has no form at all, which "+
			"is the gap correctionPathsNotYetBuilt exists to catch one layer down.",
			route)
	}
}

// matchRoutes finds every Update route an action names.
//
// Both sides carry wildcards and they do not line up: a route pattern keeps its
// literal prefix ("/assets/*/journal/*") while a SHARED PARTIAL computes even
// that ("/*/*/journal/*", from {{$.JournalResource}}). The journal editor is one
// form serving eight routes, so this returns all of them -- an exact string
// comparison matched none, which is how eight guarded forms first read as eight
// unguarded ones.
//
// THE WILDCARDS ARE NOT SYMMETRIC, and that asymmetry is the whole correctness
// of this function. An ACTION's "*" came from {{...}}, which may well have
// produced a literal the route spells out, so it matches anything. A ROUTE's
// "*" is a path PARAMETER -- an id -- so it must never match a literal segment
// in an action: without that rule "/*/*/journal" (the new-note form) matched
// "/catalogue/types/{id}", and a form for one entity was reported as the
// unguarded form of a completely unrelated one.
func matchRoutes(routes map[string]string, action string) []string {
	want := strings.Split(action, "/")
	var out []string
	for pattern, route := range routes {
		got := strings.Split(pattern, "/")
		if len(got) != len(want) {
			continue
		}
		ok := true
		for i := range got {
			switch {
			case got[i] == want[i]:
			case want[i] == "*": // the template computed this segment
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok {
			out = append(out, route)
		}
	}
	return out
}

type editForm struct {
	file   string
	action string // normalised: every {{...}} replaced with *
	// hasVersion is true when a row_version input belongs to this form --
	// nested inside it, or elsewhere in the file carrying form="<this id>".
	hasVersion bool
}

var (
	formOpenRe = regexp.MustCompile(`(?is)<form\b[^>]*>`)
	actionRe   = regexp.MustCompile(`(?is)action="([^"]*)"`)
	formIDRe   = regexp.MustCompile(`(?is)\bid="([^"]*)"`)
	// A template expression, anywhere in an attribute value.
	exprRe = regexp.MustCompile(`{{[^}]*}}`)
)

// editFormsInTemplates finds every posting form in the template tree.
//
// Both attachment shapes count, because this codebase uses both: an input
// nested inside <form>…</form>, and an input anywhere on the page carrying
// form="<id>" -- which is how a row editor puts its controls in table cells
// while the form element itself lives in the actions column.
func editFormsInTemplates(t *testing.T, root string) []editForm {
	t.Helper()
	var out []editForm

	// Glob per known subdirectory rather than filepath.Walk: the three
	// directories are the whole template tree, and gosec's G122 rejects a
	// file read inside a Walk callback (symlink TOCTOU). The same swap
	// respond_partial_test.go made, and the same one every other template
	// census here already uses.
	templatesDir := filepath.Join(root, "web", "templates")
	for _, sub := range []string{"pages", "partials", "layouts"} {
		matches, err := filepath.Glob(filepath.Join(templatesDir, sub, "*.html"))
		if err != nil {
			t.Fatalf("globbing %s: %v", sub, err)
		}
		for _, path := range matches {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("reading %s: %v", path, readErr)
			}
			page := string(raw)
			rel := sub + "/" + filepath.Base(path)

			for _, loc := range formOpenRe.FindAllStringIndex(page, -1) {
				tag := page[loc[0]:loc[1]]
				action := actionRe.FindStringSubmatch(tag)
				if action == nil {
					continue
				}
				normalised := exprRe.ReplaceAllString(action[1], "*")
				if !strings.HasPrefix(normalised, "/") {
					continue
				}

				// The form's own body, up to its close tag.
				body := page[loc[1]:]
				if end := strings.Index(body, "</form>"); end >= 0 {
					body = body[:end]
				}
				has := strings.Contains(body, `name="row_version"`)

				// ...plus anything on the page bound to it by id.
				if !has {
					if id := formIDRe.FindStringSubmatch(tag); id != nil {
						has = boundInput(page, id[1], "row_version")
					}
				}
				out = append(out, editForm{file: rel, action: normalised, hasVersion: has})
			}
		}
	}
	return out
}

// boundInput reports whether any element carries both form="<formID>" and
// name="<field>". The two attributes may appear in either order, so this
// checks the whole tag rather than a fixed sequence.
func boundInput(page, formID, field string) bool {
	needle := `form="` + formID + `"`
	for i := 0; ; {
		j := strings.Index(page[i:], needle)
		if j < 0 {
			return false
		}
		start := i + j
		// Walk back to the opening angle bracket, forward to the closing one.
		open := strings.LastIndex(page[:start], "<")
		closeAt := strings.Index(page[start:], ">")
		if open >= 0 && closeAt >= 0 &&
			strings.Contains(page[open:start+closeAt], `name="`+field+`"`) {
			return true
		}
		i = start + len(needle)
	}
}

// versionedRoutePatterns maps a normalised action path to the route it names,
// for every route whose handler ACTUALLY READS a version token.
//
// THE POPULATION IS DERIVED, NOT NAMED. The first version of this selected
// routes whose handler name contained "Update", which is a naming convention
// rather than a fact about the code -- and it silently excluded
// `HealthOverrideAmend` and the four `CostEditOn*` handlers. Choosing a
// population by naming convention is the same class of instrument error as the
// missing test this file was written to replace, so the population is now the
// set of handlers that reach `submittedVersion`.
//
// `AmendHealthOverride` is correctly absent as a result: it re-reads inside its
// transaction and applies a spec rather than a round-tripped struct, which is a
// second and better concurrency strategy that needs no token. A name-based scan
// could not have told the difference.
//
// Routes come from testdata/write_routes.txt rather than from the router, so
// this test and routescan's census cannot disagree about what the routes are.
func versionedRoutePatterns(t *testing.T, root string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "internal", "web", "routescan",
		"testdata", "write_routes.txt"))
	if err != nil {
		t.Fatalf("reading the write-route census: %v", err)
	}
	readers := handlersReadingAVersion(t, root)
	wildcard := regexp.MustCompile(`{[^}]*}`)
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Split(line, " | ")
		if len(parts) < 2 {
			continue
		}
		route, handler := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !strings.HasPrefix(route, "POST ") || !readers[handler] {
			continue
		}
		path := strings.TrimPrefix(route, "POST ")
		out[wildcard.ReplaceAllString(path, "*")] = route
	}
	return out
}

// handlersReadingAVersion returns every function in internal/web/handlers that
// reaches submittedVersion, directly or through a helper.
//
// THE TRANSITIVE STEP IS NOT OPTIONAL. The four cost editors do not call
// submittedVersion themselves -- they hand their get/update pair to the shared
// `editCost`, which calls it (costs.go). A direct-call scan would drop all four
// and report a smaller population as success, which is precisely the vacuous
// pass this file exists to prevent. Iterated to a fixpoint rather than one hop,
// so a second helper inserted later does not silently shrink the population.
func handlersReadingAVersion(t *testing.T, root string) map[string]bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "internal", "web", "handlers", "*.go"))
	if err != nil {
		t.Fatalf("globbing the handlers package: %v", err)
	}
	fset := token.NewFileSet()
	// calls[fn] is every function name fn's body mentions.
	calls := map[string]map[string]bool{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", path, perr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			seen := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.Ident:
					seen[v.Name] = true
				case *ast.SelectorExpr:
					seen[v.Sel.Name] = true
				}
				return true
			})
			calls[fn.Name.Name] = seen
		}
	}

	reaches := map[string]bool{"submittedVersion": true}
	for changed := true; changed; {
		changed = false
		for fn, body := range calls {
			if reaches[fn] {
				continue
			}
			for callee := range body {
				if reaches[callee] {
					reaches[fn] = true
					changed = true
					break
				}
			}
		}
	}
	delete(reaches, "submittedVersion")
	if len(reaches) < 10 {
		t.Fatalf("only %d handler functions reach submittedVersion; the call-graph "+
			"walk has stopped matching the code and the population is empty", len(reaches))
	}
	return reaches
}

// TestAStaleFormIsRefusedOnAComputedActionForm is the behavioural half of
// routesServedByAComputedAction, and the test three entries in that map used to
// claim existed.
//
// The static scan above cannot attribute these forms to their routes, because
// the whole action is `{{.Action}}`. So it checks the named template emits a
// token, and this checks the token actually WORKS on the route it is emitted
// for -- two people open the same editor, both save, the second is refused.
//
// TestASecondSaveFromAStaleFormIsRefused (edits_test.go) already proves this
// for /environments/{id}, which the static scan attributes on its own and which
// therefore needed no entry. This drives the ones that DO have entries.
//
// IT POSTS WHAT THE FORM RENDERED, not a hand-built payload. The first version
// invented each form's fields and the service case came back 422, because a
// service carries required fields this test had no business knowing about. A
// test that guesses the payload is testing its own guess: it fails when the
// form gains a field, which is noise, and it can pass while the form emits
// something entirely different, which is the dangerous half.
func TestAStaleFormIsRefusedOnAComputedActionForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	cases := []struct {
		name   string
		id     string
		prefix string // the route prefix, also the page the editor renders on
		change string // the one field this test varies between the two saves
	}{
		{"asset", h.lookup(`SELECT id FROM asset WHERE lifecycle = 'active' ORDER BY id LIMIT 1`), "/assets/", "name"},
		{"service", h.lookup(`SELECT id FROM service WHERE lifecycle = 'active' ORDER BY id LIMIT 1`), "/services/", "name"},
		{"project", h.lookup(`SELECT id FROM project WHERE lifecycle = 'active' ORDER BY id LIMIT 1`), "/projects/", "name"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			editPage := c.prefix + c.id + "?edit=" + c.id
			page := body(t, h.get(editPage, false))

			form := renderedFormFields(t, page, c.prefix+c.id)
			if form.Get(domain.VersionField) == "" {
				t.Fatalf("the %s editor rendered no %s, so there is no guard to test "+
					"and every save on it is a blind overwrite", c.name, domain.VersionField)
			}
			form.Set("csrf_token", h.csrfToken(editPage))

			send := func(value string) int {
				attempt := url.Values{}
				for k, vs := range form {
					attempt[k] = append([]string(nil), vs...)
				}
				attempt.Set(c.change, value)
				resp := h.post(c.prefix+c.id, attempt, false)
				defer resp.Body.Close()
				return resp.StatusCode
			}

			if got := send("Stale form, first writer"); got != http.StatusSeeOther {
				t.Fatalf("the first save returned %d, want 303 -- this test can say "+
					"nothing about the SECOND save if the first never landed", got)
			}
			// The same, now stale, token.
			if got := send("Stale form, second writer"); got == http.StatusSeeOther {
				t.Errorf("a second save from the same, now stale, form was accepted (%d). "+
					"Without the guard the slower writer silently reverts the faster one, "+
					"and change_log records the revert as a deliberate act by whoever "+
					"was slower", got)
			}
		})
	}
}

// renderedFormFields reads every named input and selected option belonging to
// ONE form on a page -- the one posting to action.
//
// SCOPED, BECAUSE A PAGE-WIDE SCRAPE READS THE WRONG FORM. The first version
// took every input on the page and the project case came back with empty `code`
// and `name`: the project page renders the edit form and then a create form
// with the same field names and blank values, so the later, empty ones won. It
// also swept up `new_tag_code` and `priced_for_vcpu` from panels that have
// nothing to do with this save. The action is the exact anchor precisely
// because these templates compute it -- `{{.Action}}` renders to the concrete
// route, so the markup names its own destination even though the SOURCE does
// not, which is the same fact that makes routesServedByAComputedAction
// necessary in the first place.
//
// Deliberately shallow -- it scrapes the two shapes these templates emit rather
// than parsing HTML -- because its job is to hand back what the browser would
// submit. Unchecked checkboxes are absent, which is correct: they submit
// nothing.
func renderedFormFields(t *testing.T, page, action string) url.Values {
	t.Helper()

	i := strings.Index(page, `action="`+action+`"`)
	if i < 0 {
		t.Fatalf("no form on this page posts to %s, so the editor did not render "+
			"and this test would post an empty payload", action)
	}
	// Back to this form's opening tag, forward to its close.
	open := strings.LastIndex(page[:i], "<form")
	if open < 0 {
		t.Fatalf("found action=%s outside any form element", action)
	}
	seg := page[open:]
	if end := strings.Index(seg, "</form>"); end >= 0 {
		seg = seg[:end]
	}

	out := url.Values{}
	// <input name="x" value="y"> in either attribute order.
	for _, m := range regexp.MustCompile(
		`<input[^>]*\bname="([^"]+)"[^>]*\bvalue="([^"]*)"`).FindAllStringSubmatch(seg, -1) {
		out.Set(m[1], html.UnescapeString(m[2]))
	}
	for _, m := range regexp.MustCompile(
		`<input[^>]*\bvalue="([^"]*)"[^>]*\bname="([^"]+)"`).FindAllStringSubmatch(seg, -1) {
		if out.Get(m[2]) == "" {
			out.Set(m[2], html.UnescapeString(m[1]))
		}
	}
	// <select name="x"> ... <option value="y" selected>
	for _, m := range regexp.MustCompile(
		`(?s)<select[^>]*\bname="([^"]+)"(.*?)</select>`).FindAllStringSubmatch(seg, -1) {
		if sel := regexp.MustCompile(`<option value="([^"]*)"[^>]*selected`).
			FindStringSubmatch(m[2]); sel != nil {
			out.Set(m[1], html.UnescapeString(sel[1]))
		}
	}
	if len(out) < 3 {
		t.Fatalf("scraped only %d fields from the form posting to %s; the scrape has "+
			"stopped matching the markup and this test would post an empty form",
			len(out), action)
	}
	return out
}
