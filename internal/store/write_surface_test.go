// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// THE CENSUS THAT SHOULD HAVE COME FIRST.
//
// unreachableRepairPaths (update_reachable_test.go) empties to zero and
// that is a real zero against the WRONG DENOMINATOR. Its population is built
// from existing `Update*` declarations, so it can only ever report on
// corrections somebody already wrote: an entity with no `Update*` method
// contributes nothing and passes in silence. Six entries went in, six came out,
// and the whole time `Provider` had a live create route and no correction and
// no withdrawal of any kind.
//
// This one is keyed on CREATION, which is the honest denominator: everything a
// person can bring into existence should have a way to fix it or take it back,
// or a stated reason why not.
//
// WHY GO METHODS AND NOT TABLES. A table-keyed scan was prototyped first --
// CLAUDE.md points at table-keying for the delete audit, and that works there
// because `DELETE FROM x` is always a literal. Withdrawal is not. Three
// separate false-negative modes were measured, each of which would have put a
// WRONG entry in the backlog below:
//
//   1. Naming.   CreateAssetInProject pairs with RetireProjectAsset.
//   2. Dynamic.  retireLink(ctx, p, "project_asset", ...) and reach.go's
//                generic helper build `UPDATE `+table+` SET`, so no literal
//                `UPDATE project_asset` string exists anywhere in the repo.
//   3. Idiom.    `tag` withdraws via retired_at/retired_by, not
//                lifecycle = 'retired'.
//
// A census with entries people know are wrong is worse than no census: they
// stop reading all of it. So this keys on method names and carries an explicit
// alias map for the handful of legitimate mismatches -- and every alias asserts
// the method it names EXISTS, so a rename fails here rather than quietly
// turning into a false gap.
//
// EVERY MAP BELOW IS TWO-DIRECTIONAL. An entry that stops applying fails just
// as loudly as a gap that is not listed, so none of them can rot into
// decoration.

// writeSurfaceAliases are entities whose correction or withdrawal exists under
// a name this scan would not guess. Each listed method must exist.
var writeSurfaceAliases = map[string]struct{ correct, withdraw []string }{
	"AssetInProject":   {withdraw: []string{"RetireProjectAsset"}},
	"CircuitInProject": {withdraw: []string{"RetireProjectCircuit"}},
	"ServiceInProject": {withdraw: []string{"RetireProjectService"}},
	// A health override is amended rather than updated, and cleared rather than
	// retired -- it is a temporary human assertion over observed state, so
	// "clear" is the honest verb: the override stops applying, and the estate's
	// own reporting takes over again.
	"HealthOverride": {
		correct:  []string{"AmendHealthOverride"},
		withdraw: []string{"ClearHealthOverride"},
	},
	// A person is corrected one grant at a time, deliberately: each Set* is a
	// separate audited act, and there is no bulk "update this user" that could
	// change a role and a cost grant in one unreviewable write. Withdrawal is
	// ScrubUser -- erasure rather than retirement, because an erasure request
	// is what actually happens to a person (docs/AUDIT.md rule 16), and
	// change_log keeps its integrity by holding an opaque id that simply stops
	// resolving.
	"User": {
		correct:  []string{"SetUserRole", "SetUserActive", "SetUserCostVisibility"},
		withdraw: []string{"ScrubUser"},
	},
}

// writeSurfaceByDesign are entities where a missing verb is correct, with the
// reason. Not a backlog -- these are decisions, and the distinction from
// writeSurfaceGaps below is the same one unreachableRepairPaths draws
// against connectiveTablesOutsideTheGraph.
var writeSurfaceByDesign = map[string]string{
	"CircuitTermination": "where a circuit lands is moved, not edited: the row " +
		"records that this end arrives here, and an end that arrives somewhere " +
		"else is a different fact. Retire and re-land.",
	"L2VPNTermination": "the same as CircuitTermination -- a termination names a " +
		"place, and naming a different place is a different termination.",
	"NetAttachment": "an attachment is an edge with no attributes of its own; " +
		"correcting it would mean pointing it somewhere else, which is a new edge.",
	"PassThrough": "a panel strand is re-punched, not amended. The row records a " +
		"physical fact about a patch panel, and changing it means somebody went " +
		"and moved the copper.",
	// The three project links. Their WITHDRAWAL exists under another name and
	// is asserted in writeSurfaceAliases; it is CORRECTION that does not apply.
	// A link says "this belongs to that project", so pointing it at a different
	// project is not a repair of this link, it is a different link -- and
	// leaving the old one behind is exactly what the retire path is for.
	"AssetInProject":   "no correction, by design: a link naming a different project is a different link. Withdraw it (RetireProjectAsset) and link the right one.",
	"CircuitInProject": "no correction, by design -- see AssetInProject.",
	"ServiceInProject": "no correction, by design -- see AssetInProject.",
}

// writeSurfaceGaps is the backlog: entities somebody can create and then cannot
// fix, cannot take back, or neither. Each entry says what the operator loses.
//
// AS WITH unreachableRepairPaths, THE COUNT CAN ONLY FALL. An entry leaves
// when the path is built. A new one arriving means an entity shipped without a
// way to correct or withdraw it, and that is the thing this test exists to make
// somebody say out loud.
var writeSurfaceGaps = map[string]string{
	// Provider was the first entry here and is the first one gone: it was the
	// only entity in the system with a live create route and no repair of any
	// kind. This test is what found it, and this test is what said to delete
	// the entry once POST /providers/{id} and /retire existed.
	//
	// --- reachable, and the row carries attributes somebody typed ---
	"NetAnchor": "no correction. internal/domain's own comment calls a misplaced " +
		"anchor \"the single highest-leverage wrong row in this model -- one row " +
		"silently changes every external-reachability verdict in the estate\".",
	"NetGroup": "no correction. availability, min_healthy and failover_mode are " +
		"the semantics HANDOVER §3.3 says make impact analysis mean anything, and " +
		"they cannot be fixed after the fact.",
	"NetUplink": "no correction for an uplink edge's own attributes.",
	"Link": "no correction. medium and length_m are DESCRIPTIVE, not identity, so " +
		"retire-and-re-patch writes a physical event into the cabling audit that " +
		"never happened -- somebody reading it later sees a cable that was pulled.",
	"Aggregate": "no correction for a declared aggregate's bounds or purpose.",
	"ASN":       "no correction: a mistyped AS number is withdraw-and-redeclare.",
	"L2VPN":     "no correction for an L2VPN's own attributes.",
	"FHRPGroup": "no correction: a group's protocol, priority or virtual address " +
		"cannot be fixed, and those are what make it a failure target.",

	// --- correction exists, withdrawal does not ---
	"Interface": "no withdrawal. A port that was physically removed stays on the " +
		"asset for ever; UpdateInterface can rename it but nothing can retire it.",
	"Prefix": "no withdrawal. A network declared in error is permanent, and it " +
		"keeps taking part in every containment answer computed over the tree.",
	"IPAddress": "no withdrawal. An address freed cannot be released, so the " +
		"allocator keeps treating it as taken.",
	"Environment": "no withdrawal. Referenced by nearly everything, so retiring " +
		"one is a genuinely bigger question than the others here -- but the answer " +
		"today is that nobody can, which is not the same as having decided.",

	// --- latent: no create route yet. Live the moment any gets a UI ---
	"BackendPool": "neither, and no route today.",
	"RIR":         "neither, and no route today.",
	"Route":       "neither, and no route today.",
	"VLANGroup":   "neither, and no route today.",
	"Identity": "neither, and no route today -- and last_rotated and " +
		"rotation_days are fields whose entire purpose is to change.",
	"ImportJob": "neither. A job record is arguably immutable history rather " +
		"than an entity, which would make this by-design; nobody has decided.",
}

// TestEveryCreatedEntityCanBeCorrectedOrWithdrawn fails when something a person
// can bring into existence has no way to fix it and no way to take it back.
func TestEveryCreatedEntityCanBeCorrectedOrWithdrawn(t *testing.T) {
	methods, err := storeMethodNames(repoRoot(t))
	if err != nil {
		t.Fatalf("scanning the store: %v", err)
	}
	// The positive control every census here carries: a scan that matches
	// nothing reports success.
	if len(methods) < 100 {
		t.Fatalf("found only %d store methods; the scan has stopped matching the "+
			"code and this test is checking nothing", len(methods))
	}

	var created []string
	for m := range methods {
		if strings.HasPrefix(m, "Create") {
			created = append(created, strings.TrimPrefix(m, "Create"))
		}
	}
	sort.Strings(created)
	if len(created) < 30 {
		t.Fatalf("found only %d Create* methods; this package has many more", len(created))
	}

	// Aliases are checked before anything else: an alias naming a method that
	// no longer exists is how this census would start lying.
	for entity, a := range writeSurfaceAliases {
		for _, m := range append(append([]string{}, a.correct...), a.withdraw...) {
			if !methods[m] {
				t.Errorf("%s is aliased to %s, which does not exist. Either it was "+
					"renamed -- in which case fix the alias -- or the path was deleted "+
					"and %s belongs in writeSurfaceGaps.", entity, m, entity)
			}
		}
	}

	for _, entity := range created {
		alias := writeSurfaceAliases[entity]
		canCorrect := methods["Update"+entity] || len(alias.correct) > 0
		canWithdraw := methods["Retire"+entity] || len(alias.withdraw) > 0

		_, byDesign := writeSurfaceByDesign[entity]
		why, listed := writeSurfaceGaps[entity]

		if canCorrect && canWithdraw {
			// Nothing missing, so neither list may claim it.
			if byDesign {
				t.Errorf("%s has both a correction and a withdrawal now, but is still "+
					"listed in writeSurfaceByDesign. Delete the entry.", entity)
			}
			if listed {
				t.Errorf("%s has both a correction and a withdrawal now, but is still "+
					"listed as a gap: %q. Delete the entry -- a backlog that keeps "+
					"things it has finished stops being read.", entity, why)
			}
			continue
		}
		if byDesign || listed {
			continue
		}
		t.Errorf("Create%s exists and %s. Somebody can bring a %s into existence "+
			"and then not repair it.\n"+
			"Either build the path, or add %s to writeSurfaceByDesign (with why the "+
			"verb genuinely does not apply) or to writeSurfaceGaps (with what an "+
			"operator loses without it).",
			entity, missingVerbs(canCorrect, canWithdraw), entity, entity)
	}
}

func missingVerbs(canCorrect, canWithdraw bool) string {
	switch {
	case !canCorrect && !canWithdraw:
		return "there is NEITHER a correction nor a withdrawal for it"
	case !canCorrect:
		return "there is no way to correct one"
	default:
		return "there is no way to withdraw one"
	}
}

// storeMethodNames returns every exported method on *SQLStore.
//
// Walks the AST rather than grepping, for the reason
// TestEveryUpdateMethodIsReachable does: a grep that requires a "(" after the
// name misreports a method passed as a VALUE, and this package passes several
// that way (a.editCost(r, id, get, update)).
func storeMethodNames(root string) (map[string]bool, error) {
	paths, err := filepath.Glob(filepath.Join(root, "internal", "store", "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			out[fn.Name.Name] = true
		}
	}
	return out, nil
}
