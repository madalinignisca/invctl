// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package estate holds the guard that keeps invctl out of the estate it
// describes. It has no runtime code, for the same reason internal/license has
// none: this is a property of the tree, not of anything that runs.
package estate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// CLAUDE.md states the rule in one line -- "invctl never acts on the estate" --
// and HANDOVER.md 1 lists configuration management as a non-goal. It is the
// claim the whole product rests on: an operator can be given this during an
// incident because the worst it can do is be wrong on a screen. A CMDB that
// could restart a service would need to be argued about before anyone installed
// it.
//
// A rule that valuable should not be held up by everybody remembering it. The
// only reason it holds today is that nobody has yet needed to make an HTTP
// request, and the day somebody does the diff will be three lines in a handler
// and will look entirely reasonable.
//
// So the capability is refused rather than the intent. invctl cannot push
// configuration because it cannot run a command and cannot make an outbound
// request -- not because no code currently does.
//
// WHAT THIS DOES NOT CLAIM. invctl opens sockets: to its database, and to a
// directory server when LDAP is configured. Those are its own infrastructure,
// not the estate, and pretending otherwise would make the guard a lie that gets
// switched off. The line drawn here is between invctl's own dependencies, which
// are two and are named, and a general-purpose ability to reach anything else.

// forbiddenImports cannot appear in non-test code, with the reason.
var forbiddenImports = map[string]string{
	"os/exec": "running a command is how a CMDB becomes a configuration manager. " +
		"There is no ansible-playbook, no systemctl, no ssh -- and no exec.Command to build one out of",
	"net/smtp":                "invctl does not notify; it renders. Anything that needs to send reads the UI or a future read-only API",
	"net/rpc":                 "an RPC client is an outbound call to something that is not our database",
	"golang.org/x/crypto/ssh": "reaching a host over SSH is acting on the estate, whatever the payload says",
}

// forbiddenSelectors are the package-level symbols that would give the binary
// an outbound capability it does not have, keyed as they are written.
//
// Symbols rather than imports, because net/http is imported forty-odd times and
// must be: it is the server. The distinction that matters is not whether the
// package is present but whether the CLIENT half of it is reachable, and that
// is a question about which identifiers appear.
var forbiddenSelectors = map[string]string{
	"http.Get":                   "an outbound HTTP request",
	"http.Head":                  "an outbound HTTP request",
	"http.Post":                  "an outbound HTTP request",
	"http.PostForm":              "an outbound HTTP request",
	"http.Client":                "an HTTP client; the server half of net/http is what this codebase uses",
	"http.DefaultClient":         "an HTTP client",
	"http.Transport":             "an HTTP client transport",
	"http.DefaultTransport":      "an HTTP client transport",
	"http.RoundTripper":          "an HTTP client transport",
	"http.NewRequest":            "a request built to be sent somewhere",
	"http.NewRequestWithContext": "a request built to be sent somewhere",
	"net.Dial":                   "an outbound connection",
	"net.DialTimeout":            "an outbound connection",
	"net.DialTCP":                "an outbound connection",
	"net.DialUDP":                "an outbound connection",
	"net.DialIP":                 "an outbound connection",
	"tls.Dial":                   "an outbound connection",
	"tls.DialWithDialer":         "an outbound connection",
	"exec.Command":               "running a command",
	"exec.CommandContext":        "running a command",
	"ldap.Dial":                  "a directory connection",
	"ldap.DialURL":               "a directory connection",
	"ldap.DialTLS":               "a directory connection",
}

// dialAllowlist is every file permitted a symbol on that list, with the reason.
//
// One entry. The LDAP authenticator binds against INV_LDAP_URL, which is the
// directory invctl authenticates ITS OWN users against -- not a host in the
// inventory, and it is read-only: a simple bind, and an upsert of the app_user
// row on success. Keeping it here rather than exempting the whole auth package
// means a second dialler in that package is a deliberate edit.
var dialAllowlist = map[string]map[string]string{
	"internal/auth/ldap.go": {
		"ldap.DialURL": "simple bind against INV_LDAP_URL: invctl's own directory, not an inventoried host",
	},
}

// TestNothingReachesOutOfThisProcess is the structural half of CLAUDE.md's
// "invctl never acts on the estate".
//
// It parses rather than greps, and that is not fastidiousness: this file names
// every forbidden symbol in order to forbid it, and so does the rule it
// enforces. A grep-based version flags its own explanation, which is how a
// structural test gets deleted by the second person who trips over it -- the
// lesson boundary_source_test.go already paid for.
func TestNothingReachesOutOfThisProcess(t *testing.T) {
	root := repoRoot(t)
	files := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		files++

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}

		for _, imp := range file.Imports {
			pathValue, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if why, bad := forbiddenImports[pathValue]; bad {
				t.Errorf("%s:%d imports %q. %s",
					rel, fset.Position(imp.Pos()).Line, pathValue, why)
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			name := pkg.Name + "." + sel.Sel.Name
			why, bad := forbiddenSelectors[name]
			if !bad {
				return true
			}
			if _, allowed := dialAllowlist[rel][name]; allowed {
				return true
			}
			t.Errorf("%s:%d uses %s, which is %s.\n"+
				"invctl presents state; it does not push configuration, remediate, restart "+
				"or open a firewall rule. Showing is not acting, and the audience is a person "+
				"during an incident. If this genuinely belongs, it is an architecture decision "+
				"-- add it to dialAllowlist with the reason, out loud.",
				rel, fset.Position(sel.Pos()).Line, name, why)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	// A walk that found nothing passes for the wrong reason.
	if files == 0 {
		t.Fatal("no Go source found; this test would pass on an empty repository")
	}
	t.Logf("checked %d non-test Go files", files)
}

// TestTheDialAllowlistIsSpent asserts every allowlisted exception is still
// being used.
//
// An entry that no longer matches anything is an exemption sitting open for
// whoever adds the next dialler to that file. The allowlist is one line long
// and it should stay that way by being checked, not by being short.
func TestTheDialAllowlistIsSpent(t *testing.T) {
	root := repoRoot(t)
	for file, symbols := range dialAllowlist {
		path := filepath.Join(root, filepath.FromSlash(file))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("dialAllowlist names %s, which does not exist. An exemption for a "+
				"file that has moved protects nothing and hides the next one.", file)
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		found := map[string]bool{}
		ast.Inspect(parsed, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok {
					found[pkg.Name+"."+sel.Sel.Name] = true
				}
			}
			return true
		})
		for _, symbol := range sortedKeys(symbols) {
			if !found[symbol] {
				t.Errorf("dialAllowlist permits %s in %s, but it is not there any more (%s).\n"+
					"Remove the entry: an unused exemption is an open door in the one file "+
					"nobody re-reads.", symbol, file, symbols[symbol])
			}
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// repoRoot walks up from the test's working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

func skipDir(name string) bool {
	switch name {
	case ".git", "bin", "node_modules", "vendor", "testdata":
		return true
	}
	return false
}
