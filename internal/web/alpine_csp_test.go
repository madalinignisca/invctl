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

// TestAlpineDirectivesAreCSPSafe.
//
// The app ships the CSP build of Alpine, because the Content-Security-Policy
// is `script-src 'self'` with no 'unsafe-eval'. That build evaluates NO
// expressions: `x-data` must name a component registered with Alpine.data(),
// and x-on/x-bind must reference a method or a property. Anything else is
// SILENTLY INERT -- no console error, no visual clue, the attribute simply
// does nothing.
//
// That is how the help drawer shipped doing nothing at all. It used
// `x-data="{ helpOpen: false }"`, `x-on:click="helpOpen = true"` and
// `x-bind:class="helpOpen ? 'is-open' : ”"`, all three unsupported, and every
// server-side test passed because the HTML was perfect -- the panel simply
// never opened in a browser. A human found it by clicking the link.
//
// So the constraint is asserted against the templates themselves. It is a
// lint, not a browser test, and it catches the whole class in milliseconds.
func TestAlpineDirectivesAreCSPSafe(t *testing.T) {
	// A CSP-safe value is a bare identifier chain: `show`, `panelClass`,
	// `form.kind`. Anything with an operator, a literal or a call is not.
	safe := regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)
	directive := regexp.MustCompile(`(x-(?:data|on:[a-z.]+|bind:[a-z-]+|text|show|model))="([^"]*)"`)

	registered := registeredAlpineComponents(t)

	var checked int
	for _, path := range templateFiles(t) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, m := range directive.FindAllStringSubmatch(string(src), -1) {
			name, value := m[1], strings.TrimSpace(m[2])
			checked++
			if value == "" {
				continue
			}
			if !safe.MatchString(value) {
				t.Errorf("%s: %s=%q is an expression. The CSP build of Alpine does not "+
					"evaluate expressions, so this attribute does nothing at all -- and "+
					"nothing reports that it does nothing. Register a component in "+
					"web/static/app.js and reference a method or getter instead.",
					filepath.Base(path), name, value)
				continue
			}
			// An x-data must name something that actually exists, or the whole
			// subtree silently has no state.
			if name == "x-data" {
				base := strings.SplitN(value, ".", 2)[0]
				if !registered[base] {
					t.Errorf("%s: x-data=%q is not registered with Alpine.data() in "+
						"web/static/app.js, so every directive inside it is inert",
						filepath.Base(path), value)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Alpine directives found, so this test proves nothing")
	}
	t.Logf("checked %d Alpine directives across the templates", checked)
}

// templateFiles lists every template, from the same three directories the
// renderer parses.
func templateFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"layouts", "pages", "partials"} {
		matches, err := filepath.Glob(filepath.Join("..", "..", "web", "templates", dir, "*.html"))
		if err != nil {
			t.Fatalf("globbing %s: %v", dir, err)
		}
		out = append(out, matches...)
	}
	if len(out) == 0 {
		t.Fatal("no templates found, so the check would pass vacuously")
	}
	return out
}

func registeredAlpineComponents(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "app.js"))
	if err != nil {
		t.Fatalf("reading app.js: %v", err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`Alpine\.data\(\s*'([^']+)'`).FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("no Alpine.data registrations found; the check would pass vacuously")
	}
	return out
}
