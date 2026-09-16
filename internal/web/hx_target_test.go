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
	"strconv"
	"strings"
	"testing"
)

// CLAUDE.md, "HTTP and HTMX conventions":
//
//	"Swap targets are declared in the template, not chosen by the handler.
//	 Default to hx-swap='outerHTML' on a wrapping element with a stable id."
//
// That rule was written down, never enforced, and violated in 38 of 90 places.
// It is not decoration: it interlocks with app.js's htmx:beforeSwap listener,
// which force-swaps a 422 because htmx 2 would not, "so the server does the
// right thing and the operator sees nothing happen at all". An untargeted
// <form> swallows that swap into its own innerHTML, and an untargeted <button>
// into the button, so the refusal arrives and lands inside the control that
// asked for it. Seen in a browser on the identity rotation form, fixed in
// 068d91c; docs/htmx-swap-targets-design.md is the full argument.
//
// WHY STATIC. Several of the ninety are retire forms on entities that need a
// live fixture in a specific state before a refusal is reachable at all, and
// the ones hardest to set up are the ones most likely to be missed -- so
// behavioural coverage would thin out precisely where it matters. The same
// reasoning TestEveryEditFormCarriesItsVersion gives, and it holds more
// strongly here. The behavioural half is tests/e2e/specs/refusal-targets.spec.js,
// on the surfaces with a reachable refusal.
//
// WHY ITS OWN FILE rather than a second check inside the version census:
// different population (every hx-post element, buttons and unversioned retire
// routes included, against that test's "routes whose handler reaches
// submittedVersion"), different failure (where a response lands, not an
// optimistic-concurrency guard), and different matching -- that test's
// matchRoutes and its asymmetric-wildcard rule exist because it JOINS templates
// to routes. This joins nothing: an element either declares the attributes or it
// does not.

// hxTargetExempt are posting elements that legitimately declare no swap target.
//
// A backlog, like versionExemptRoutes and the store's unreachableRepairPaths.
// An entry is a claim that THIS ELEMENT'S RESPONSE HAS NO IN-BAND BODY UNDER ANY
// STATUS ITS HANDLER CAN PRODUCE, and it has to be argued from the handler --
// never from app.js's current listener. Whoever adds an entry writes down what
// they read to believe it. Empty is the goal, and it is where this starts.
//
// THE OBVIOUS CRITERION -- "a route that can never return a force-swapped
// status" -- IS REJECTED, and the reason is one line in a different file. The
// set of force-swapped statuses is one comparison in app.js. Widening it to 409
// (reachable from every versioned edit form and from handleStoreError's
// ErrConflict branch) would silently re-arm every element exempted on that
// basis. An exemption whose validity rests on a line nobody consulted is not an
// exemption; it is a fuse.
//
// Note what is NOT here: the twenty-eight elements whose handler answers only
// with a redirect or a flash. They are not exempt -- they declare
// hx-target="this" hx-swap="none", which is a declaration and says something
// true. See partials/teams.html's retire forms.
var hxTargetExempt = map[string]string{}

// CO-LOCATION, added in review round 1 (2026-09-16). A regex over tag text has
// no notion of {{if}}/{{end}}, so an attribute wrapped in a template
// conditional read as unconditionally present -- the census below was passing
// this, live, before this fix:
//
//	<form hx-post="/{{$.JournalResource}}/{{$.JournalID}}/journal/{{.ID}}/retire"
//	      {{if $.SomeCondition}}hx-target="this" hx-swap="none"{{end}}
//	      hx-confirm="...">
//
// hx-post is unconditional; hx-target and hx-swap are not. When the condition
// is false the element posts with no target at all -- the exact silent
// refusal this census exists to prevent -- and the census reported green,
// because the regex found hx-target="this" hx-swap="none" as plain text
// regardless of what gated it.
//
// The shape already exists twice, safely, in the tree:
// partials/rows.html:260 and partials/asset_table.html:64, both
// `<form {{if $bulkTag}}hx-post=... hx-target=... hx-swap=...{{end}}>`. They
// are safe only because hx-post, hx-target and hx-swap all sit inside the
// SAME {{if}}...{{end}} -- all three render or none do. Nothing in the census
// required that co-location; it was accidental correctness.
//
// The rule enforced below: within one matched tag, hx-post, hx-target and
// hx-swap must share one {{if}} context -- all three outside every {{if}}, or
// all three inside the same one. A tag that splits them across a boundary
// fails, naming the element.

// hxTargetFloor is a "did the scan stop matching the markup" control, not a
// budget and not a target.
//
// Every census in this repository has one, because a scan that matches nothing
// reports success. The population was 90 on 2026-09-16 and will grow. IF THIS
// NUMBER EVER HAS TO BE RAISED TO KEEP THE TEST PASSING, THE SCAN IS BROKEN,
// NOT THE FLOOR.
const hxTargetFloor = 80

func TestEveryPostingElementDeclaresItsSwapTarget(t *testing.T) {
	root := repoRoot(t)

	elements := postingElements(t, root)
	if len(elements) < hxTargetFloor {
		t.Fatalf("found only %d hx-post elements across the template tree, floor is %d; "+
			"the scan has stopped matching the markup and this test is checking nothing",
			len(elements), hxTargetFloor)
	}

	ids := declaredIDs(t, root)
	// The other half of the same control: a target-existence check against an
	// empty id set would pass every target in the tree.
	if len(ids) < 100 {
		t.Fatalf("found only %d id attributes across the template tree; the id scan "+
			"has stopped matching the markup, so every target below would resolve "+
			"against nothing and pass", len(ids))
	}

	for _, el := range elements {
		where := el.file + ":" + el.line

		if why, exempt := hxTargetExempt[where]; exempt {
			if el.target != "" || el.swap != "" {
				t.Errorf("%s declares hx-target=%q hx-swap=%q but is listed as exempt: %q. "+
					"Delete the entry.", where, el.target, el.swap, why)
			}
			continue
		}

		// CO-LOCATION, checked before "is it declared at all": a target found
		// as text is not the same as a target that is ACTUALLY THERE when
		// hx-post is. If hx-target (or hx-swap) sits inside a different
		// {{if}} nesting than hx-post, the three attributes do not all render
		// together, and the element can post with none of them -- see the
		// comment on hxTargetExempt above for the live evasion this closes.
		if el.target != "" && el.targetCtx != el.postCtx {
			t.Errorf("%s posts to %q (context %q) but hx-target=%q sits in a different "+
				"{{if}} context (%q).\n"+
				"hx-post, hx-target and hx-swap must all sit outside every {{if}}, or all "+
				"three inside the SAME one -- see partials/rows.html:260 for the safe shape. "+
				"Split across a boundary, the element can post with hx-post present and "+
				"hx-target absent, landing the response wherever htmx's default takes it.",
				where, el.action, el.postCtx, el.target, el.targetCtx)
			continue
		}
		if el.swap != "" && el.swapCtx != el.postCtx {
			t.Errorf("%s posts to %q (context %q) but hx-swap=%q sits in a different "+
				"{{if}} context (%q).\n"+
				"Same rule as hx-target: all three must share one {{if}} context.",
				where, el.action, el.postCtx, el.swap, el.swapCtx)
			continue
		}

		// BOTH, NOT JUST THE TARGET. htmx's default swap is innerHTML, so a form
		// declaring hx-target="#team" and no swap puts the re-rendered #team
		// partial INSIDE #team -- the same duplicated-heading failure one level
		// up, and harder to see because the outer shell still looks right. Worse,
		// the fragment's own root carries id="team", so the DOM then holds two
		// elements with that id and every later hx-target="#team" resolves to
		// whichever the browser finds first. A half-declared target is a slower
		// version of the bug.
		if el.target == "" {
			t.Errorf("%s posts to %q and declares no hx-target.\n"+
				"htmx's default target is the element itself, so a 422 -- which "+
				"app.js force-swaps -- lands inside the form or the button that "+
				"sent it. Add hx-target and hx-swap, or add %q to hxTargetExempt "+
				"with the reason, argued from the handler.", where, el.action, where)
			continue
		}
		if el.swap == "" {
			t.Errorf("%s declares hx-target=%q and no hx-swap.\n"+
				"htmx defaults to innerHTML, which nests the response inside the "+
				"element it names -- and when the response's own root carries that "+
				"id, the DOM ends up holding two of them. hx-swap='outerHTML' is "+
				"the default this codebase uses; hx-swap='none' is a legitimate "+
				"declaration for a route that answers with a navigation or a flash.",
				where, el.target)
			continue
		}

		// THE OTHER DIRECTION: does the id it names exist anywhere in the tree?
		// Catches failure mode (c) -- a target inside a conditional the element
		// is not -- which otherwise fails at REQUEST time with htmx:targetError
		// and is silent in every log this project has.
		//
		// IT WILL NOT CATCH A CONDITIONAL ID, and this comment must not read as
		// stronger than it is: an id that exists in the file but only inside
		// {{if .IsAdmin}} passes here and can still be absent at render time.
		// That half is the per-element check in the plan and the browser spec.
		if !strings.HasPrefix(el.target, "#") {
			// An extended selector: "this", "closest ...", "find ...", "next",
			// "previous". Declared, so the rule above is satisfied; there is no
			// id to look for. "this" is what the twenty-eight
			// redirect-or-flash routes use.
			continue
		}
		want := normaliseID(strings.TrimPrefix(el.target, "#"))
		found := false
		for id := range ids {
			if globsOverlap(want, id) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s targets %q and no element in web/templates declares that id.\n"+
				"htmx resolves the target when the REQUEST is built, not when the "+
				"response arrives, so this fires htmx:targetError and does nothing "+
				"at all -- the silent refusal this census exists to prevent.",
				where, el.target)
		}
	}
}

// hxElement is one posting element and what it declares.
type hxElement struct {
	file, line   string
	action       string
	target, swap string
	// postCtx, targetCtx, swapCtx are the {{if}}-nesting path in effect at
	// each attribute's position within the tag, from blockContextAt. Equal
	// paths mean the attributes are gated the same way (or not gated at all);
	// unequal means one can render without the other -- see the co-location
	// comment on hxTargetExempt.
	postCtx, targetCtx, swapCtx string
}

var (
	// <form ...> or <button ...>, attributes possibly spanning lines. One rule
	// for both tags: the blast radius differs and that is not a reason to split
	// the rule, because a two-rule census has to decide which applies from the
	// tag name and the population is not stable -- partials/rows.html already
	// has a <button hx-post> carrying its own target and swap.
	hxOpenRe   = regexp.MustCompile(`(?is)<(?:form|button)\b[^>]*>`)
	hxPostRe   = regexp.MustCompile(`(?is)\bhx-post="([^"]*)"`)
	hxTargetRe = regexp.MustCompile(`(?is)\bhx-target="([^"]*)"`)
	hxSwapRe   = regexp.MustCompile(`(?is)\bhx-swap="([^"]*)"`)
	// An id attribute on any element. hx-swap-oob is deliberately not excluded:
	// #flash-dock is declared by partials/flash.html and by layouts/base.html,
	// and both are real places a response can land.
	hxIDRe = regexp.MustCompile(`(?is)\bid="([^"]*)"`)
	// A template expression anywhere in an attribute value, the way
	// edit_form_version_test.go's exprRe normalises an action.
	hxExprRe = regexp.MustCompile(`{{[^}]*}}`)
	// A bare {{if ...}}, {{else ...}} or {{end}} block token, used to track
	// {{if}} nesting within a matched tag for the co-location check.
	// {{range}}/{{with}} are deliberately not tracked -- see blockContextAt's
	// doc comment on what that leaves undisclosed... covered, not silently
	// assumed safe.
	hxBlockRe = regexp.MustCompile(`\{\{-?\s*(if|else|end)\b[^}]*-?\}\}`)
)

// blockContextAt returns a function reporting the {{if}}-nesting path in
// effect at any byte offset within tag: "" outside every {{if}}, "if1" inside
// the first one encountered, "if1/if2" inside a nested one, and so on. Two
// attributes with equal paths are gated by the same set of conditions (or by
// none); unequal paths mean one can be present in the rendered tag without
// the other.
//
// {{else}}/{{else if}} do NOT change the path -- a then-branch and its
// else-branch are treated as the same context, because the co-location rule
// this feeds is "gated by the same {{if}}", not "gated by the same boolean
// outcome". THIS IS A KNOWN GAP, not an oversight: a tag that puts hx-post in
// the then-branch and hx-target in the else-branch of one {{if}} would still
// pass, because the two branches are mutually exclusive and neither attribute
// combination this check compares ever coexists in a render either way. No
// element in this tree takes that shape today; if one ever does, this
// function will not catch it.
//
// {{range}} and {{with}} are not tracked at all -- only {{if}}/{{else}}/{{end}}
// are recognised block tokens. A tag using either to gate an attribute is
// invisible to this check, exactly as it always was to the rest of the
// census.
//
// Nesting is matched by TEXTUAL position, not by condition identity: two
// separate {{if $.SameCondition}}...{{end}} blocks around different
// attributes get different paths ("if1" and "if2") even though the condition
// text is identical, and are therefore flagged as split even if a human would
// read them as always co-present. That is the conservative direction to be
// wrong in -- a false positive here is a comment explaining why the split is
// safe; a false negative is the failure mode this whole fix exists to close.
func blockContextAt(tag string) func(pos int) string {
	type event struct {
		pos  int
		id   string // "" for a closing {{end}}
		open bool
	}
	var events []event
	n := 0
	for _, loc := range hxBlockRe.FindAllStringSubmatchIndex(tag, -1) {
		kw := tag[loc[2]:loc[3]]
		switch kw {
		case "if":
			n++
			events = append(events, event{pos: loc[0], open: true, id: "if" + strconv.Itoa(n)})
		case "end":
			events = append(events, event{pos: loc[0], open: false})
		}
		// "else" adds no event: see the doc comment above.
	}
	return func(pos int) string {
		var stack []string
		for _, e := range events {
			if e.pos >= pos {
				break
			}
			if e.open {
				stack = append(stack, e.id)
			} else if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		return strings.Join(stack, "/")
	}
}

// postingElements finds every hx-post element in the template tree.
//
// Glob per subdirectory rather than filepath.Walk: the three directories are the
// whole tree, and gosec's G122 rejects a file read inside a Walk callback
// (symlink TOCTOU). The same swap respond_partial_test.go and the version census
// both already made.
func postingElements(t *testing.T, root string) []hxElement {
	t.Helper()
	var out []hxElement
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
			for _, loc := range hxOpenRe.FindAllStringIndex(page, -1) {
				tag := page[loc[0]:loc[1]]
				post := hxPostRe.FindStringSubmatchIndex(tag)
				if post == nil {
					continue
				}
				ctxAt := blockContextAt(tag)
				el := hxElement{
					file:    rel,
					line:    strconv.Itoa(1 + strings.Count(page[:loc[0]], "\n")),
					action:  tag[post[2]:post[3]],
					postCtx: ctxAt(post[0]),
				}
				if m := hxTargetRe.FindStringSubmatchIndex(tag); m != nil {
					el.target = tag[m[2]:m[3]]
					el.targetCtx = ctxAt(m[0])
				}
				if m := hxSwapRe.FindStringSubmatchIndex(tag); m != nil {
					el.swap = tag[m[2]:m[3]]
					el.swapCtx = ctxAt(m[0])
				}
				out = append(out, el)
			}
		}
	}
	return out
}

// declaredIDs is every id attribute in the tree, normalised.
func declaredIDs(t *testing.T, root string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
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
			for _, m := range hxIDRe.FindAllStringSubmatch(string(raw), -1) {
				ids[normaliseID(m[1])] = true
			}
		}
	}
	return ids
}

// normaliseID collapses every template expression to a single "*", so
// "#user-row-{{.User.ID}}" and id="user-row-{{$.User.ID}}" both become
// "user-row-*". The same normalisation matchRoutes performs on an action, and
// for the same reason: the markup does not say what the expression renders to.
func normaliseID(s string) string { return hxExprRe.ReplaceAllString(s, "*") }

// globsOverlap reports whether two '*'-globs can describe the same string.
//
// BOTH SIDES CARRY WILDCARDS and they do not line up -- "#{{.Prefix}}-form"
// against id="{{.Prefix}}-form" is the common case, and
// partials/projects.html:391 takes its whole target from the dict
// ("{{.Target}}", normalised to "*"), which therefore matches every id in the
// tree. That is the honest answer for it: nothing in the markup says where it
// lands, and this census cannot know. It is one element, it is in the 52 that
// already declared both attributes, and docs/htmx-swap-targets-design.md's
// ruling 4 ("the partial owns its id") says it is the one place that shape
// should not have been used. Recorded here rather than silently passing.
//
// Exponential in the worst case and irrelevant: the inputs are id-length
// strings with at most two stars.
func globsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return allStars(a) && allStars(b)
	}
	if a[0] == '*' {
		return globsOverlap(a[1:], b) || globsOverlap(a, b[1:])
	}
	if b[0] == '*' {
		return globsOverlap(a, b[1:]) || globsOverlap(a[1:], b)
	}
	return a[0] == b[0] && globsOverlap(a[1:], b[1:])
}

func allStars(s string) bool { return strings.Trim(s, "*") == "" }
