// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/web/routescan"
)

// THE LATCH WP-J9 LEFT OPEN, CLOSED.
//
// WP-J9 sorted every posting element into two classes. Class 1 declares
// `hx-target="#<partial id>" hx-swap="outerHTML"` because its handler
// re-renders a fragment on 422. Class 2 declares `hx-target="this"
// hx-swap="none"` because its handler only redirects -- it renders no fragment
// on any path, so there is nothing to swap.
//
// `hx-swap="none"` IS A LATCH. It does not mean "this handler returns nothing";
// it means "discard whatever comes back". The two agree today because somebody
// read all twenty-eight handlers and confirmed it. Nothing holds them together
// afterwards: the day one of those handlers grows a fragment path -- an inline
// validation error, a re-rendered row -- HTMX will throw the fragment away and
// the operator will see nothing happen. No error, no console warning, no failing
// test. docs/htmx-swap-targets-design.md §2b records the classification; it
// could not record a guard, because the guard did not exist.
//
// WHY THIS SHAPE AND NOT A STATIC JOIN. The obvious guard is to resolve each
// element's hx-post back to its handler and inspect the handler for Render
// calls. That was considered during WP-J9 and refused: it needs a template ->
// route -> handler -> call-graph analysis, which is a second router model
// living beside the real one for the two to disagree over. This instead DRIVES
// the routes and looks at what actually comes back, which is the only evidence
// that survives a refactor of how handlers are written.
//
// WHAT AN EMPTY PAYLOAD BUYS, AND IT IS MORE THAN IT LOOKS. driveRoute posts an
// empty form. For a withdrawal that is a VALID request -- a retire takes no
// fields -- so those routes execute their success path. For a correction it is
// an invalid one, so those execute their refusal path. Between them the two
// paths most likely to grow a fragment are both exercised, without a fixture
// per route.

// classTwoUnresolvable are class-2 elements whose hx-post cannot be resolved to
// a single registered route, and why. Each entry is a hole in this test's
// coverage and is written down so the hole is visible.
var classTwoUnresolvable = map[string]string{
	// `{{$.Action}}/{{.ID}}/retire` and its siblings come from a dict passed by
	// the including page, so the resource segment is decided at render time by
	// the caller. partials/costs.html is shared by the asset, service, circuit
	// and project pages, and each passes a different Action. Resolving it would
	// mean evaluating the template, which is a second renderer beside the real
	// one -- the thing the doc comment above refuses.
	//
	// These are covered by the cost editors' own functional tests instead
	// (money_visibility_test.go drives every money route behaviourally).
	"{}/{}/retire":  "dict-driven resource segment (partials/costs.html, shared by four pages).",
	"{}/{}/reprice": "dict-driven resource segment (partials/costs.html, shared by four pages).",
	"{}":            "dict-driven whole action (partials/costs.html's own save form).",
	// partials/journal.html is included by four detail pages and builds its
	// action from the including page's resource, same shape as costs.html.
	"/{}/{}/journal/{}/retire": "dict-driven resource segment (partials/journal.html, shared by four pages).",
}

var (
	// A template expression in an hx-post: `{{.Asset.ID}}`.
	templateExprRe = regexp.MustCompile(`\{\{[^}]*\}\}`)
	// A router parameter in a registered pattern: `{id}`, `{member}`.
	routeParamRe = regexp.MustCompile(`\{[^{}]*\}`)
)

func TestNoClassTwoHandlerEverRendersAFragment(t *testing.T) {
	root := repoRoot(t)

	// Step 1: the class-2 population, read from the templates themselves.
	var classTwo []string
	seen := map[string]bool{}
	for _, el := range postingElements(t, root) {
		if el.swap != "none" {
			continue
		}
		pattern := "POST " + templateExprRe.ReplaceAllString(el.action, "{}")
		if seen[pattern] {
			continue
		}
		seen[pattern] = true
		classTwo = append(classTwo, pattern)
	}
	sort.Strings(classTwo)

	if len(classTwo) == 0 {
		t.Fatal("found no hx-swap=\"none\" posting elements. Either the class-2 " +
			"population is genuinely empty -- it was 28 when this was written -- or " +
			"the scan has stopped matching the markup, which reads as a clean pass " +
			"and is not one.")
	}

	// Step 2: resolve each to a registered route. The router's own inventory,
	// not a second list maintained by hand.
	// Keyed by SHAPE, not by spelling. An element writes
	// `/network/groups/{{.Group.ID}}/members/{{.ID}}/retire`; the router
	// declares `/network/groups/{id}/members/{member}/retire`. Both collapse to
	// the same thing once every parameter becomes `{}`, and the parameter NAMES
	// were never the point -- what has to agree is which path the element posts
	// to. Collapsing the router side too is what makes the two comparable; an
	// earlier version compared against `{id}` literally and reported six real
	// routes as missing.
	registered := map[string]routescan.Route{}
	for _, r := range routescan.WriteRoutes(t) {
		registered[routeParamRe.ReplaceAllString(r.Pattern, "{}")] = r
	}

	var drivable []routescan.Route
	for _, pattern := range classTwo {
		if route, ok := registered[pattern]; ok {
			// The ROUTER's spelling, because paramsFor fills parameters by name.
			drivable = append(drivable, route)
			continue
		}
		key := strings.TrimPrefix(pattern, "POST ")
		if _, excused := classTwoUnresolvable[key]; excused {
			continue
		}
		t.Errorf("class-2 element posts to %q, which matches no registered write route.\n"+
			"    Either the element points at a route that does not exist -- a dead "+
			"control -- or this scan cannot resolve it and the gap needs an entry in "+
			"classTwoUnresolvable saying why.", pattern)
	}

	// Direction 2: an exemption that no longer applies.
	for key := range classTwoUnresolvable {
		if _, ok := registered["POST "+key]; ok {
			t.Errorf("classTwoUnresolvable excuses %q, but it resolves to a real route "+
				"now. Delete the entry -- an exemption that cannot fire is decoration.", key)
		}
	}

	if len(drivable) == 0 {
		t.Fatal("resolved no class-2 routes to drive, so this test proves nothing")
	}

	// Step 3: drive them and look at what actually comes back.
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			// An ADMIN session, unlike rbac_boundary_test.go's observer. This
			// test is not asking whether the route refuses; it is asking what a
			// permitted request gets back, which only an authorised caller sees.
			h.login(boundaryAdminUser, boundaryAdminPassword)

			var fullPages, examined int
			for _, route := range drivable {
				t.Run(route.Pattern, func(t *testing.T) {
					resp := driveClassTwo(t, h, route, fx)
					body := drainedBody(t, resp)
					if strings.Contains(body, "<!doctype html>") {
						fullPages++
						return
					}
					examined++
					if fragment, why := looksLikeAFragment(resp, body); fragment {
						t.Errorf("%s answers with a fragment (%s), but every element "+
							"posting to it declares hx-swap=\"none\".\n"+
							"    HTMX will DISCARD this response. The operator clicks and "+
							"sees nothing happen -- no error, no console warning.\n"+
							"    Either give the element a real target (class 1, "+
							"docs/htmx-swap-targets-design.md §2b), or stop the handler "+
							"rendering a fragment.\n    status %d, body: %s",
							route.Pattern, why, resp.StatusCode, truncate(body))
					}
				})
			}

			// THE CONTROL, not a budget. Every route that answered with a whole
			// document was skipped above, so a test where most did would report
			// "no drift" having looked at almost nothing -- the exact shape of a
			// green test that proves nothing. This fails if fewer than half the
			// class-2 routes were actually examined, which says the fixtures
			// have stopped filling parameters rather than that the handlers are
			// clean.
			if examined*2 < len(drivable) {
				t.Errorf("only %d of %d class-2 routes answered with something this test "+
					"could read; %d returned a whole page, which usually means paramsFor "+
					"could not fill their parameters.\n"+
					"    A pass built on that is a pass over nothing. Add fixtures for "+
					"the missing parameters in setupBoundary.", examined, len(drivable), fullPages)
			}
			t.Logf("examined %d of %d class-2 routes (%d answered with a full page)",
				examined, len(drivable), fullPages)
		})
	}
}

// driveClassTwo posts to route AS HTMX, which driveRoute does not.
//
// THIS IS THE WHOLE POINT AND IT WAS WRONG FIRST. Without HX-Request every
// handler takes render.Respond's non-HTMX branch and answers with a complete
// page -- doctype, layout, nav -- which this test then read as "a fragment",
// failing every route for a reason that has nothing to do with drift. The
// class-2 contract is about what an HTMX REQUEST gets back, because
// hx-swap="none" only ever applies to one.
func driveClassTwo(t *testing.T, h *harness, route routescan.Route, fx *boundaryFixtures) *http.Response {
	t.Helper()
	path := classTwoPath(t, h, route.Pattern, fx)
	token := boundaryCSRFToken(t, h)
	req := h.request(http.MethodPost, path, strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("HX-Request", "true")
	return h.do(req)
}

// classTwoResourceTable maps a URL segment to the table whose first live row
// stands in for "a real one of these".
//
// WHY THIS EXISTS AT ALL. paramsFor (rbac_boundary_test.go) falls back to
// store.NewID() for any parameter it does not recognise, which is exactly right
// for the question that file asks -- authorization is decided before the row is
// looked up, so a nonexistent id still proves the 403. It is useless here: a
// random id 404s, the handler never reaches its render path, and this test
// would report "no drift" having never run the code it is guarding. That is
// what the coverage control below caught, at 9 of 23.
var classTwoResourceTable = map[string]string{
	"addresses":    "ip_address",
	"bundles":      "cable_bundle",
	"endpoints":    "endpoint",
	"interfaces":   "interface",
	"links":        "link",
	"prefixes":     "prefix",
	"ip-ranges":    "ip_range",
	"redundancy":   "fhrp_group",
	"dependencies": "dependency",
	"anchors":      "net_anchor",
	"uplinks":      "net_uplink",
	"attachments":  "net_attachment",
	"members":      "net_group_member",
	"components":   "device_type_component",
	"overlays":     "l2vpn",
	"pools":        "backend_pool",
	"routes":       "route",
	"clusters":     "cluster",
	"identities":   "identity",
}

// classTwoPath fills a route's parameters, preferring a real seeded row over
// paramsFor's random-id fallback.
func classTwoPath(t *testing.T, h *harness, pattern string, fx *boundaryFixtures) string {
	t.Helper()
	path := paramsFor(pattern, fx)

	declared := strings.SplitN(pattern, " ", 2)
	segments := strings.Split(strings.Trim(declared[len(declared)-1], "/"), "/")
	filled := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) != len(filled) {
		return path
	}
	for i, seg := range segments {
		if i == 0 || !strings.HasPrefix(seg, "{") {
			continue
		}
		table, ok := classTwoResourceTable[segments[i-1]]
		if !ok {
			continue
		}
		if id := firstLiveID(t, h, table); id != "" {
			filled[i] = id
		}
	}
	return "/" + strings.Join(filled, "/")
}

// firstLiveID returns some live row's id from table, or "" if the seed left it
// empty. Table names come from classTwoResourceTable above and never from a
// request, so the interpolation below cannot carry anything a caller chose.
func firstLiveID(t *testing.T, h *harness, table string) string {
	t.Helper()
	var id string
	q := fmt.Sprintf("SELECT id FROM %s WHERE lifecycle <> 'retired' LIMIT 1", table)
	if err := h.store.DB().Reader.Get(&id, q); err == nil {
		return id
	}
	// No lifecycle column: net_group_member and friends.
	q = fmt.Sprintf("SELECT id FROM %s LIMIT 1", table)
	if err := h.store.DB().Reader.Get(&id, q); err != nil {
		return ""
	}
	return id
}

// looksLikeAFragment decides whether a response carries markup HTMX would have
// swapped, had the element asked it to.
//
// Three things are not fragments, and each is deliberate:
//   - A redirect, by status or by HX-Redirect. HTMX leaves the page before any
//     swap; render.Redirect's whole job.
//   - An empty body.
//   - An out-of-band flash ALONE. hx-swap-oob is delivered regardless of the
//     primary swap -- that is the mechanism a class-2 refusal uses to say
//     anything at all, and WP-J9's refuseInvariant depends on it. So an OOB
//     element is stripped before the check rather than counted as drift.
func looksLikeAFragment(resp *http.Response, body string) (bool, string) {
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return false, ""
	}
	if resp.Header.Get("HX-Redirect") != "" {
		return false, "HX-Redirect"
	}
	if strings.Contains(body, "<!doctype html>") {
		// A whole document, not a fragment. Reachable when paramsFor cannot
		// fill a parameter and the handler 404s into the full-page error view;
		// that is a fixture gap in this test, not drift in the handler, and
		// reporting it as drift would be a false accusation with a 10KB body
		// attached. Counted below so the gap cannot hide.
		return false, "full page"
	}
	rest := strings.TrimSpace(oobElementRe.ReplaceAllString(body, ""))
	if rest == "" {
		return false, ""
	}
	if !strings.Contains(rest, "<") {
		// Plain text: http.Error's output, which is what a non-HTMX refusal
		// returns. Nothing to swap.
		return false, ""
	}
	return true, fmt.Sprintf("%d bytes of markup beyond any OOB flash", len(rest))
}

// oobElementRe strips the out-of-band flash dock and its content.
//
// Pinned to the exact element this codebase emits -- partials/flash.html's
// `<div id="flash-dock" hx-swap-oob="beforeend">` -- rather than written
// generically. Go's regexp is RE2 and has no backreferences, so a generic
// "any element carrying hx-swap-oob, closed by its own tag name" cannot be
// expressed; enumerating would guess at tags nobody emits. Pinning it means a
// SECOND kind of OOB element would not be stripped and would read as a
// fragment -- a false failure naming the response, which is the safe direction
// to be wrong in, and the message tells whoever hits it what to widen.
var oobElementRe = regexp.MustCompile(`(?s)<div[^>]*id="flash-dock"[^>]*>.*?</div>`)
