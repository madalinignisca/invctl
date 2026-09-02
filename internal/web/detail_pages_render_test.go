// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/http"
	"testing"
)

// TestEveryDetailPageRenders fetches every GET /<resource>/{id} page through
// the real router and requires a 200.
//
// THIS IS TestEveryImpactPageRenders' LESSON, GENERALISED -- and it is written
// because that lesson was learned too narrowly. That test exists because the
// circuit impact page reused a shared partial and referenced a field its page
// struct did not carry, so it returned 500 for every circuit while the engine,
// the store and the seed suites were all green. A test was then added for the
// two IMPACT pages and for nothing else.
//
// The identical bug was sitting one route away the whole time. J6 (732c6b0)
// added `"Providers" .Providers` to circuit_detail.html's cost panel without
// adding Providers to renderCircuitDetail's anonymous page struct, so
// GET /circuits/{id} returned 500 for every user, on main and on the deployed
// demo, until this commit. Nothing caught it: html/template resolves a field
// name at EXECUTION time, `go build` and `go vet` cannot see it, and handler
// tests call handlers directly rather than rendering the page.
//
// So the rule is not "test the impact pages". It is: THE ONLY THING IN GO THAT
// CHECKS A TEMPLATE'S CONTRACT IS RENDERING IT. A detail page that nothing
// fetches is a page nothing checks. Add a row here when a detail route is
// added; the cost is one HTTP round trip against a fixture that is already
// built.
func TestEveryDetailPageRenders(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Each row is a detail route and the query that finds a seeded id for it.
	// A resource absent from the fixture fails loudly rather than skipping:
	// a runtime skip here would convert exactly the outage this test exists to
	// catch into a pass (CLAUDE.md, "two different meanings of skip").
	for _, tc := range []struct {
		name  string
		path  string
		query string
	}{
		{"asset", "/assets/", `SELECT id FROM asset WHERE lifecycle = 'active' LIMIT 1`},
		{"service", "/services/", `SELECT id FROM service WHERE lifecycle = 'active' LIMIT 1`},
		{"circuit", "/circuits/", `SELECT id FROM circuit LIMIT 1`},
		{"project", "/projects/", `SELECT id FROM project LIMIT 1`},
		{"team", "/teams/", `SELECT id FROM team LIMIT 1`},
		{"certificate", "/certificates/", `SELECT id FROM certificate LIMIT 1`},
		{"cluster", "/clusters/", `SELECT id FROM cluster LIMIT 1`},
		{"vlan", "/vlans/", `SELECT id FROM vlan LIMIT 1`},
		{"overlay", "/overlays/", `SELECT id FROM l2vpn LIMIT 1`},
		{"redundancy", "/redundancy/", `SELECT id FROM fhrp_group LIMIT 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := h.lookup(tc.query)
			if id == "" {
				t.Fatalf("no %s in the fixture, so %s{id} cannot be checked -- "+
					"seed one rather than letting this row skip", tc.name, tc.path)
			}
			path := tc.path + id
			resp := h.get(path, false)
			page := body(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s returned %d, want 200:\n%.600s",
					path, resp.StatusCode, page)
			}
		})
	}
}
