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

	"github.com/madalinignisca/invctl/internal/domain"
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
// under web/templates may carry an inline "on*=" event-handler attribute --
// full stop, not "none new". knownBrokenInlineHandlers used to carry a
// closed, explicit allowlist of cases fixed on their own schedule rather
// than in the commit that found them: custom_field_form.html's and
// tag_form.html's retire-confirmation onclick="return confirm(...)" (fixed
// by task-10, replaced with htmx's own hx-confirm), then
// network.html:85/123's group-picker onchange= for the member and uplink
// forms (fixed by task-11, replaced with app.js's delegated
// data-action-template listener -- see reach.go's groupIDFromRequest for
// the server-side half, which refuses a write if the two ever disagree).
// With that last pair gone, the allowlist is empty, and it stays that way:
// every inline handler this app renders is now CSP-dead by construction,
// and any template that adds one back fails this test on the spot, with no
// "report it separately" escape hatch left to reach for. The map is kept,
// empty, as the obvious home for a future genuinely-scheduled exception --
// but adding an entry to it is a decision to make explicitly, not a default.
var knownBrokenInlineHandlers = map[string]string{}

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
			"listener in app.js instead (see app.js's data-submit-on-change and "+
			"data-action-template for the pattern). knownBrokenInlineHandlers is "+
			"intentionally empty; only add to it for a genuinely scheduled, explicitly "+
			"agreed exception, with a comment explaining why it isn't fixed now: %v", unlisted)
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

// TestNetworkGroupPickersUseDataActionTemplateNotDeadOnchange is task-11's
// regression guard for the pair this file's allowlist used to carry --
// network.html:85/123's group pickers on the "add member" and "declare
// uplink" forms. TestNoTemplateCarriesAnUnlistedInlineEventHandler already
// proves the SOURCE contains no onchange=; this renders the real /network
// page through the router (at least one group must exist for either form to
// render at all, per network.html's own {{if .Groups}} guard) and requires
// both selects carry data-action-template with the {id} placeholder app.js's
// delegated listener substitutes -- the thing that actually replaces the
// dead inline rewrite, not just the absence of the old one.
func TestNetworkGroupPickersUseDataActionTemplateNotDeadOnchange(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	mustNetGroupWeb(t, h, "picker-guard-group", domain.NetRoleCore, domain.AvailStandalone)

	page := body(t, h.get("/network", false))

	for _, want := range []string{
		`id="nm-group" name="group_id" data-action-template="/network/groups/{id}/members"`,
		`id="nu-group" name="group_id" data-action-template="/network/groups/{id}/uplinks"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/network does not render %s", want)
		}
	}

	for _, line := range stripTemplateComments(page) {
		if m := inlineHandlerRe.FindStringSubmatch(line); m != nil {
			t.Errorf("/network still renders an inline %s= handler: %s", m[1], line)
		}
	}
}
