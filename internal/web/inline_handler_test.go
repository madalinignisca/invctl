// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Item 6 (2026-09-02 group-a-1-1 round): user_row.html's "sees costs"
// checkbox carried onchange="this.form.requestSubmit()", and this app's own
// CSP is script-src 'self' with no unsafe-inline (middleware.go's
// Content-Security-Policy) -- the browser drops an inline event-handler
// attribute silently, with no console error and no visible failure. The
// checkbox toggled and nothing ever submitted, so an administrator could
// not grant can_see_costs through the UI at all, undermining the whole
// cost-visibility gate this branch built on top of that grant.
//
// That defect cannot be caught by a browser test in this suite (there is
// none here), so the check that fits is a template-level one: no template
// under web/templates may carry a NEW inline "on*=" event-handler attribute.
// knownBrokenInlineHandlers is the closed, explicit list of the remaining
// two that predate this fix and are reported separately for their own
// scheduling (task brief item 6) -- not fixed here, and not silently
// forgotten either. The other two entries this list used to carry --
// custom_field_form.html's and tag_form.html's retire-confirmation
// onclick="return confirm(...)" -- were fixed by replacing the dead-on-CSP
// inline handler with htmx's own hx-confirm attribute (see task-10), so
// they are gone from this list, not just from the source. Anything not on
// this list fails the moment it is added.
var knownBrokenInlineHandlers = map[string]string{
	"partials/network.html:85": "onchange -- group picker for a VLAN member form; " +
		"same CSP defeat as user_row.html's checkbox, reported separately, not fixed here.",
	"partials/network.html:123": "onchange -- group picker for an uplink form; " +
		"same CSP defeat as user_row.html's checkbox, reported separately, not fixed here.",
}

// inlineHandlerRe matches a genuine HTML event-handler attribute: "on"
// followed by lowercase letters and "=", with no letter, digit, underscore
// or hyphen immediately before it. The leading exclusion is what keeps this
// from matching htmx's hx-confirm="..." (whose "confirm" ends in "onfirm=",
// which a naive `on[a-z]+="` would wrongly flag) or ordinary words like
// "action=" -- "ion=" is not "on=" and would not match regardless, but the
// exclusion is kept for any future attribute that does end that way.
var inlineHandlerRe = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_-])(on[a-z]+)\s*=\s*"`)

// TestNoTemplateCarriesAnUnlistedInlineEventHandler is the guard: it scans
// every page, partial and layout template's SOURCE TEXT (not its parsed
// output) for an inline on*= attribute, after stripping Go template
// comments ({{/* ... */}}) -- without that stripping, this test would flag
// its own explanatory comments (see user_row.html's Item 6 comment, which
// names the very attribute it removed) as violations.
//
// Every match found must be in knownBrokenInlineHandlers, keyed by
// "relative/path.html:line". A match missing from that map is a NEW inline
// handler and fails outright; an entry in the map with no matching source
// line is stale and also fails, so the allowlist stays honest rather than
// silently growing permission nobody re-checks.
func TestNoTemplateCarriesAnUnlistedInlineEventHandler(t *testing.T) {
	root := repoRoot(t)
	templatesDir := filepath.Join(root, "web", "templates")

	found := map[string]string{} // "path:line" -> attribute text
	for _, sub := range []string{"pages", "partials", "layouts"} {
		matches, err := filepath.Glob(filepath.Join(templatesDir, sub, "*.html"))
		if err != nil {
			t.Fatalf("globbing %s: %v", sub, err)
		}
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			rel := sub + "/" + filepath.Base(path)
			for ln, line := range stripTemplateComments(string(data)) {
				m := inlineHandlerRe.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				key := rel + ":" + strconv.Itoa(ln+1)
				found[key] = m[1]
			}
		}
	}

	var unlisted []string
	for key, attr := range found {
		if _, ok := knownBrokenInlineHandlers[key]; !ok {
			unlisted = append(unlisted, key+" ("+attr+"=...)")
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("template(s) carry an inline event-handler attribute this app's CSP "+
			"(script-src 'self', no unsafe-inline) silently drops -- the handler will "+
			"never run and nothing will say why. Add a data-* attribute and a delegated "+
			"listener in app.js instead (see app.js's data-submit-on-change for the "+
			"pattern), or if this is one of the four known-and-scheduled cases, add it "+
			"to knownBrokenInlineHandlers with a comment: %v", unlisted)
	}

	var stale []string
	for key := range knownBrokenInlineHandlers {
		if _, ok := found[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("knownBrokenInlineHandlers names location(s) that no longer carry an "+
			"inline handler -- fixed and forgotten, or the line moved and the allowlist "+
			"is stale either way: %v", stale)
	}
}

// stripTemplateComments returns src split into lines with every
// {{/* ... */}} block (single- or multi-line) replaced by nothing, the way
// html/template's own parser discards them before execution. A regex-only
// scan without this would flag the explanatory prose inside a template
// comment that quotes the very attribute it is describing having removed.
func stripTemplateComments(src string) []string {
	var out strings.Builder
	inComment := false
	for i := 0; i < len(src); i++ {
		if inComment {
			if strings.HasPrefix(src[i:], "*/}}") {
				inComment = false
				i += 3
				continue
			}
			// A newline inside the comment must still land in the output,
			// blank, so every line AFTER the comment keeps the same line
			// number it has in src -- the allowlist keys are "path:line"
			// against the real file, and losing this would silently shift
			// every match past a multi-line comment.
			if src[i] == '\n' {
				out.WriteByte('\n')
			}
			continue
		}
		if strings.HasPrefix(src[i:], "{{/*") {
			inComment = true
			i += 3
			continue
		}
		out.WriteByte(src[i])
	}
	return strings.Split(out.String(), "\n")
}

// TestRetireButtonsUseHXConfirmNotDeadOnclick is task-10's regression guard
// for the two entries this file's allowlist used to carry and no longer
// does. TestNoTemplateCarriesAnUnlistedInlineEventHandler already proves the
// SOURCE contains no onclick="return confirm(...)" -- but a source-text scan
// cannot prove the replacement actually renders a working control, only
// that the old one is gone. This renders both real pages through the
// router, as detail_pages_render_test.go's TestEveryDetailPageRenders does,
// and requires the retire button carry hx-confirm with the exact wording
// the task brief pins (the sentence says what SURVIVES retirement, which is
// the point of the prompt -- CLAUDE.md, task-10 brief). It also re-asserts
// no on*= attribute reaches the rendered output, belt-and-braces against
// the source-only scan above.
func TestRetireButtonsUseHXConfirmNotDeadOnclick(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	h.post("/custom-fields", url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {"asset"}, "code": {"confirm_check"}, "label": {"Confirm Check"},
		"kind": {"text"}, "description": {"exercises the retire button's hx-confirm"},
		"owner_team_id": {h.refs.Teams["platform"]},
	}, false).Body.Close()

	h.post("/tags", url.Values{
		"csrf_token":  {h.csrfToken("/tags")},
		"code":        {"confirm-check"},
		"label":       {"Confirm Check"},
		"description": {"exercises the retire button's hx-confirm"},
	}, false).Body.Close()

	fieldsPage := body(t, h.get("/custom-fields", false))
	tagsPage := body(t, h.get("/tags", false))

	for _, tc := range []struct {
		name string
		page string
		want string
	}{
		{"custom field", fieldsPage, `hx-confirm="Retire this field? Every value it already holds is kept."`},
		{"tag", tagsPage, `hx-confirm="Retire this tag? Anything already carrying it keeps it."`},
	} {
		if !strings.Contains(tc.page, tc.want) {
			t.Errorf("%s registry does not render %s", tc.name, tc.want)
		}
	}

	for _, tc := range []struct {
		name string
		page string
	}{
		{"custom field", fieldsPage},
		{"tag", tagsPage},
	} {
		for _, line := range stripTemplateComments(tc.page) {
			if m := inlineHandlerRe.FindStringSubmatch(line); m != nil {
				t.Errorf("%s registry still renders an inline %s= handler: %s",
					tc.name, m[1], line)
			}
		}
	}
}
