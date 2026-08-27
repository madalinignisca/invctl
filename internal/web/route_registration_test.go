// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"testing"
)

// allowedDirectRegistrations are the only expressions that may appear as the
// pattern argument of a mux.Handle/mux.HandleFunc call in routes.go, with the
// reason each is exempt from going through a registrar closure.
//
// "pattern" covers the three registrars themselves -- read, write and
// writeAdminOnly all register their argument, and everything that goes
// through them is inventoried by routescan and driven by the boundary suite.
// Every other entry is a route that deliberately does not.
var allowedDirectRegistrations = map[string]string{
	"pattern":        "the read/write/writeAdminOnly registrar closures themselves",
	`"GET /static/"`: "static assets; no handler, no store access",
	`"GET /healthz"`: "liveness probe, unauthenticated by design",
	`"GET /login"`:   "the login form; unauthenticated by necessity",
	`"POST /login"`:  "the login attempt itself; rate-limited, not session-gated",
	`"POST /logout"`: "ends a session; requires one to matter",
	`"POST " + ObservationsPath`: "the observed-state webhook -- a machine credential, " +
		"never an app_user, gated by middleware.RequireAgent and the single " +
		"documented CSRF exemption (see docs/AUDIT.md rule 6)",
	"apiPattern(route)": "the read-only JSON API, gated by middleware.RequireReader; " +
		"writes nothing, so it is outside the write bucket by construction",
}

// TestEveryRouteIsRegisteredThroughARegistrarOrAnAllowlistedException closes
// the hole underneath the generated route inventory.
//
// routescan.WriteRoutes builds its list by finding calls to the write and
// writeAdminOnly closures in routes.go, and the RBAC boundary suite drives
// exactly that list. Both are therefore blind to a route registered straight
// on the mux: a `mux.Handle("POST /assets/{id}/backdoor", ...)` with no admin
// gate leaves the census green AND the boundary suite green, because neither
// can see a route that never went through a registrar. Verified by injecting
// precisely that and watching both pass.
//
// A generated list is only as complete as the thing generating it. This test
// is what makes "every write route is inventoried" true, rather than "every
// write route someone remembered to register the usual way".
//
// Adding a genuinely exceptional route is not forbidden -- it requires an
// entry above saying why, which is a line a reviewer sees.
func TestEveryRouteIsRegisteredThroughARegistrarOrAnAllowlistedException(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "routes.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing routes.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "mux" {
			return true
		}
		if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, call.Args[0]); err != nil {
			t.Fatalf("rendering pattern at %s: %v", fset.Position(call.Pos()), err)
		}
		if _, allowed := allowedDirectRegistrations[buf.String()]; !allowed {
			t.Errorf("%s registers %s directly on the mux. Route it through "+
				"read/write/writeAdminOnly so the inventory and the RBAC "+
				"boundary suite can see it, or add it to "+
				"allowedDirectRegistrations with the reason it is exempt.",
				fset.Position(call.Pos()), buf.String())
		}
		return true
	})
}
