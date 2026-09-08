// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

// routesServedByAComputedAction are Update routes this scan cannot attribute to
// a form, because the form's action attribute is ENTIRELY a template
// expression -- `action="{{.Action}}"` -- filled in by Go.
//
// That shape is deliberate and good: one partial serves create and correct for
// the same entity, which is why the project, asset, service and endpoint
// editors all use it. It just means the markup does not say which route it
// posts to, so a static reader cannot tell.
//
// EACH ONE NEEDS ITS GUARD PROVEN SOMEWHERE ELSE, and the entry says where.
// Listing a route here is not a pass -- it is a promise that a behavioural test
// drives that form and asserts the token, which is why every entry names one.
var routesServedByAComputedAction = map[string]string{
	"POST /assets/{id}": "asset_form (forms.html) posts to {{.Action}}; the token is " +
		"driven by TestTwoPeopleEditingTheSameAssetDoNotOverwriteEachOther.",
	"POST /services/{id}": "service_form (forms.html) posts to {{.Action}}; driven by " +
		"the service half of the same concurrency suite.",
	"POST /projects/{id}": "project_form (projects.html) emits row_version only when " +
		"{{.Editing}}, so the create half correctly has none.",
	"POST /endpoints/{id}": "endpoint_edit_form (forms.html) posts to {{.Action}}.",
}

func TestEveryEditFormCarriesItsVersion(t *testing.T) {
	root := repoRoot(t)

	updateRoutes := updateRoutePatterns(t, root)
	// A positive control, for the reason every census in this repository has
	// one: a scan that matches nothing reports success.
	if len(updateRoutes) < 20 {
		t.Fatalf("found only %d Update routes in the census; the parse has stopped "+
			"matching the file and this test is checking nothing", len(updateRoutes))
	}

	forms := editFormsInTemplates(t, root)
	if len(forms) < 10 {
		t.Fatalf("found only %d posting forms across the templates; the scan has "+
			"stopped matching the markup", len(forms))
	}

	seen := map[string]bool{}
	for _, f := range forms {
		for _, route := range matchRoutes(updateRoutes, f.action) {
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
	for _, route := range updateRoutes {
		if seen[route] {
			if why, listed := routesServedByAComputedAction[route]; listed {
				t.Errorf("%s IS matched by a template form now, but is still listed as "+
					"served by a computed action: %q. Delete the entry.", route, why)
			}
			continue
		}
		if _, listed := routesServedByAComputedAction[route]; listed {
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

// updateRoutePatterns maps a normalised action path to the route it names, for
// every route in the write census whose handler corrects something.
//
// Read from testdata/write_routes.txt rather than from the router, so this
// test and routescan's own census cannot disagree about what the routes are --
// and so a route that vanishes from the census fails there first, loudly,
// instead of quietly shrinking this scan's population.
func updateRoutePatterns(t *testing.T, root string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "internal", "web", "routescan",
		"testdata", "write_routes.txt"))
	if err != nil {
		t.Fatalf("reading the write-route census: %v", err)
	}
	wildcard := regexp.MustCompile(`{[^}]*}`)
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Split(line, " | ")
		if len(parts) < 2 {
			continue
		}
		route, handler := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !strings.HasPrefix(route, "POST ") || !strings.Contains(handler, "Update") {
			continue
		}
		path := strings.TrimPrefix(route, "POST ")
		out[wildcard.ReplaceAllString(path, "*")] = route
	}
	return out
}
