// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// connectiveTablesOutsideTheGraph classifies every table that references two
// or more others and is NOT read by LoadGraph. Each entry is a decision with
// its reason, not a to-do list.
//
// A table with two foreign keys is the mechanical signature of an edge: it
// exists to say that one thing relates to another. Most such edges propagate
// failure and belong in the impact graph. The ones below do not, and each is
// here for a different reason -- which is the point of writing them down
// rather than leaving the absence to be re-derived by whoever next audits
// WP-I1.
var connectiveTablesOutsideTheGraph = map[string]string{
	// --- Deliberately not reachability edges -------------------------------
	"link": "a cable is physical inventory and path tracing (WP-B3), not a " +
		"reachability edge. docs/reachability-design.md models reachability at " +
		"FORWARDER GROUP level, with net_attachment_member naming which chassis " +
		"the cable actually lands on -- \"directed, unlike link\" -- because a " +
		"cable genuinely cannot tell you which way traffic flows, so it is " +
		"declared rather than guessed. Adding link here would be a second, " +
		"disagreeing answer to a question net_attachment already answers.",
	"port_pass_through": "a patch panel passes a cable through without " +
		"terminating it; it belongs to path tracing for the same reason link " +
		"does, and carries even less: it cannot originate an outage, only relay " +
		"one that the group model has already accounted for.",

	// --- Reached by a different route --------------------------------------
	"power_input": "power reaches impact through a resolver, not the graph. " +
		"AssetsLosingPower/AssetsLosingSupply turn a failed feed or supply into " +
		"the assets that actually lose power -- honouring dual-feed redundancy, " +
		"which the graph could not -- and the handler hands that set to the " +
		"ordinary impact page. Request.DownAssetIDs' own comment names it: " +
		"\"reboot this VM\" and \"this rack loses power\" arrive in the same " +
		"shape. Loading power into the graph would give the redundancy rule a " +
		"second implementation, and two implementations are two answers.",

	// --- Not propagation edges at all --------------------------------------
	// Entities with their own identity and lifecycle. Two foreign keys make a
	// table look like an edge; these carry attributes instead, and nothing
	// fails BECAUSE of them.
	"certificate":  "an entity with its own lifecycle. Expiry is a report (WP-I2); an expiring certificate is not an outage that propagates.",
	"custom_field": "a field definition (WP-A4). Its keys are owner and vocabulary, not endpoints.",
	"tag":          "a label definition (WP-G4).",
	"prefix":       "an addressing entity (WP-D2). Containment among prefixes is utilisation, not failure propagation.",
	"ip_address":   "an address ON an interface (WP-D3). It fails with its interface's asset, which the closure already carries; it transmits nothing of its own.",
	"power_panel":  "a distribution board -- part of the power chain, reached the same way power_input is.",
	"power_source": "a supply; see power_input.",
	"rt_container": "runtime detail on an instance (which runtime, which engine). The instance is the edge; this describes it.",
	"rt_windows":   "as rt_container.",

	// Money. A cost line points at what it prices and who invoiced it, which
	// is two keys and no connectivity.
	"asset_cost":   "a price against an asset (WP-J4).",
	"service_cost": "as asset_cost.",
	"circuit_cost": "as asset_cost.",
	"project_cost": "as asset_cost.",

	"asset_occupant":        "who sits in a rack unit. Occupancy is space, not failure.",
	"certificate_asset":     "a certificate deployed on an asset. Expiry is a report (WP-I2), not an outage that propagates.",
	"certificate_service":   "as certificate_asset.",
	"entity_tag":            "a label. Tags carry no failure semantics.",
	"project_asset":         "ownership and cost attribution, not connectivity.",
	"project_service":       "as project_asset.",
	"project_circuit":       "as project_asset.",
	"dependency_data_class": "what data crosses an edge, not whether it exists.",
	"asset_cost_consumer":   "which assets a cost line divides across (WP-J4). Money, not failure.",
	"asset_environment":     "which environments an asset serves. A label used for scoping.",
	"asset_storage_claim":   "declared storage demand, consumed by capacity findings (WP-J3), not by outage propagation.",
	"user_project":          "who owns which project (WP-G1). Authorization, not topology.",
}

// TestEveryConnectiveTableIsAccountedForInTheImpactGraph is WP-I1's recurring
// audit, made standing.
//
// THE WORK PACKAGE ASKS FOR AN AUDIT AND CALLS ITSELF RECURRING: "every new
// edge type must appear in impact simulation, reachability findings, shutdown
// order, and the fixture. Audit that they do; add the missing ones." Doing
// that by hand takes an afternoon and is wrong by the next merge -- and doing
// it by hand on 2026-09-04 nearly produced two FALSE gaps, because
// circuit_termination is reached through a JOIN rather than a FROM, and power
// reaches impact through a redirect rather than the graph. A grep found
// neither; only reading four files did.
//
// So the audit is this test. A table with two or more foreign keys is the
// mechanical signature of an edge between two things; each one must either be
// read by LoadGraph or be listed above with the reason it is not. A new edge
// type therefore cannot be added quietly: it arrives here as a failure that
// names the table and asks the question the work package exists to ask.
//
// It does NOT check that a loaded edge is used WELL -- that an outage
// propagates correctly along it is what the scenario tests in internal/impact
// are for. This checks the thing those cannot: that the edge reached the
// engine at all.
func TestEveryConnectiveTableIsAccountedForInTheImpactGraph(t *testing.T) {
	db := openTestSQLite(t)
	ctx := context.Background()

	var tables []string
	if err := db.Reader.SelectContext(ctx, &tables, `
		SELECT name FROM sqlite_master
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE 'goose%'
		ORDER BY name`); err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	if len(tables) < 40 {
		t.Fatalf("only %d tables found -- the schema did not migrate, so this audit "+
			"would pass by having nothing to check", len(tables))
	}

	loader, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "store", "graph.go"))
	if err != nil {
		t.Fatalf("reading the graph loader: %v", err)
	}
	// FROM and JOIN both count. Reading only FROM is how a hand audit missed
	// circuit_termination, which the circuit-edge query reaches by JOIN.
	touched := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([a-z_][a-z0-9_]*)`).
		FindAllStringSubmatch(string(loader), -1) {
		touched[strings.ToLower(m[1])] = true
	}

	connective := map[string]bool{}
	for _, table := range tables {
		var fks []struct {
			Table string `db:"table"`
		}
		if err := db.Reader.SelectContext(ctx, &fks, `SELECT "table" FROM pragma_foreign_key_list(?)`, table); err != nil {
			t.Fatalf("reading foreign keys of %s: %v", table, err)
		}
		// TWO FOREIGN KEYS, not two distinct targets. A cable's two keys both
		// point at `interface`, and a self-edge between two rows of one table
		// is the most edge-like shape there is -- counting distinct targets
		// silently excludes exactly the case this audit exists for. (Found by
		// this test on its first run, against a hand classification that had
		// used the looser rule.)
		if len(fks) >= 2 {
			connective[table] = true
		}
	}
	if len(connective) < 10 {
		t.Fatalf("only %d connective tables found -- foreign keys did not resolve, so "+
			"this audit would pass vacuously", len(connective))
	}

	for table := range connective {
		if touched[table] {
			if reason, listed := connectiveTablesOutsideTheGraph[table]; listed {
				t.Errorf("%s is BOTH read by LoadGraph and listed as outside the graph "+
					"(%q). One of the two is wrong, and leaving both means the list has "+
					"stopped describing the code.", table, reason)
			}
			continue
		}
		if _, listed := connectiveTablesOutsideTheGraph[table]; !listed {
			t.Errorf("%s references two or more tables -- the signature of an edge -- and "+
				"is not read by internal/store/graph.go.\n"+
				"WP-I1: does an outage propagate along it? If it does, load it into the "+
				"graph and give it a scenario in internal/impact. If it does not, add it "+
				"to connectiveTablesOutsideTheGraph with the reason, so the next audit "+
				"does not have to re-derive it.", table)
		}
	}

	// A stale exemption is worse than none: it stops describing the code and
	// starts licensing whatever is written under that name next.
	for table := range connectiveTablesOutsideTheGraph {
		if !connective[table] {
			t.Errorf("connectiveTablesOutsideTheGraph lists %q, which is not a connective "+
				"table in the current schema -- it was dropped, renamed, or lost a foreign "+
				"key. Remove the entry rather than leaving a standing exemption for a table "+
				"that no longer exists.", table)
		}
	}
}
