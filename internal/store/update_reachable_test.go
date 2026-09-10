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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unreachableRepairPaths is every Update* or Retire* store method that nothing
// calls, with what it means for the operator who needs it.
//
// IT WAS CALLED correctionPathsNotYetBuilt AND THAT NAME OVERSOLD IT. It reads
// as "corrections an operator cannot make", which is a claim about the product;
// what this actually checks is narrower and mechanical -- repair methods with
// no caller. The honest name for the wider claim now belongs to
// write_surface_test.go, which keys on creation rather than on what somebody
// happened to write.
//
// THIS ONE IS A BACKLOG, NOT A SET OF DECISIONS, and it is the difference
// between this list and connectiveTablesOutsideTheGraph's -- that one says a
// table is deliberately not an edge and always will be. Every entry here is a
// gap somebody will eventually hit, kept visible so the count can only fall.
// Removing an entry means the correction path was built. Adding one means a
// new entity shipped without one, and that is the thing this test exists to
// make somebody say out loud.
//
// IT IS EMPTY, AND KEEPING IT EMPTY IS THE POINT. Six entries went in when this
// test was written and all six came out over the days after: the power chain's
// feed, supply and input, then the reservation, the circuit and the dependency.
// An empty map is not a reason to delete the mechanism -- the test below still
// fails on any new unreachable Update method, and this is where the next one
// gets justified in writing or, better, refused.
//
// EMPTY DOES NOT MEAN THE WRITE SURFACE IS COMPLETE, and reading it that way is
// the mistake this paragraph exists to prevent. The population below is built
// from storeUpdateMethods -- existing `func (s *SQLStore) Update…` declarations
// -- so it can only ever report on corrections somebody already wrote. AN
// ENTITY WITH NO Update* METHOD AT ALL CONTRIBUTES NOTHING AND PASSES IN
// SILENCE. Verified in the tree on 2026-09-08, after the six landed:
//
//   - Provider has CreateProvider, a live POST /providers, and no update and no
//     retire method of any kind. A supplier typed wrong stays wrong for ever --
//     and CircuitUpdate now offers a picker for it.
//   - NetAnchor has neither, and this codebase's own domain comment calls a
//     misplaced anchor "the single highest-leverage wrong row in this model".
//   - The net_* family has five create routes and zero retire routes, while
//     RetireNetGroup, RetireNetGroupMember and RetireNetUplink sit in reach.go
//     with no caller. This test never looked: it scans only Update*.
//
// It is blind the other way too -- AmendHealthOverride is a real, routed
// correction path the Update prefix will never match. What this checks is a
// naming convention with a capability behind it, not the capability itself.
//
// The complementary census IS NOW WRITTEN -- write_surface_test.go, keyed on
// Create* rather than on what somebody happened to write, with its gaps and its
// by-design decisions kept apart. Between the two: that one asks whether a
// repair path EXISTS, this one asks whether anything can REACH it. Neither
// question implies the other, and the net_* family was failing both at once --
// no way to correct a forwarder group, and a withdrawal method nothing called.
var unreachableRepairPaths = map[string]string{
	// EMPTY AGAIN, and the second time round it emptied for the right reason.
	//
	// The five net_* withdrawal paths that filled it -- RetireNetAnchor,
	// RetireNetGroup, RetireNetGroupMember, RetireNetUplink and
	// RetireNetAttachment -- were complete store methods with no routes at all:
	// the reachability layer had five create routes and none to take anything
	// back. They were wired 2026-09-10, which is the same shape WP-1.2 took for
	// the six correction paths before them. The methods were never wrong; they
	// were unreachable, and this test is what refused to let that stay quiet.
}

// TestEveryUpdateMethodIsReachable fails when a store method written to correct
// something has nothing calling it.
//
// THREE OF THESE SHIPPED IN ONE DAY. UpdateVLAN had been unreachable since
// VLANs arrived; UpdateWirelessLAN shipped unreachable in WP-F1, hours after
// the VLAN gap was found and fixed and written up. Knowing about the pattern
// did not prevent reproducing it, because the omission is not a decision
// anybody makes -- it is a question nobody asks. So it becomes a test.
//
// WHAT THE ROUTE CENSUS CANNOT SEE. write_routes.txt checks that the routes
// match the router; it has no opinion on whether an entity's write surface is
// COMPLETE. Four wireless write routes satisfied it while the correction path
// was missing, which is exactly how this went unnoticed.
//
// A reference of any kind counts -- a call, or the method passed as a value,
// which is how the cost editors reach theirs (`a.editCost(r, id, get, update)`).
// An earlier grep-based version of this scan required a '(' immediately after
// the name and reported UpdateServiceCost as unreachable when it is not; this
// walks the AST instead, where a selector is a selector however it is used.
func TestEveryUpdateMethodIsReachable(t *testing.T) {
	root := repoRoot(t)

	updaters, err := storeUpdateMethods(filepath.Join(root, "internal", "store"))
	if err != nil {
		t.Fatalf("scanning the store for Update methods: %v", err)
	}
	// A positive control. If the scan stops matching the code it finds nothing
	// and reports success, which is the vacuous pass every census in this
	// repository guards against.
	if len(updaters) < 20 {
		t.Fatalf("found only %d Update* store methods; this package has many more, so "+
			"the scan has stopped matching the code and is checking nothing", len(updaters))
	}

	referenced, err := methodsReferencedUnder(root, "internal", "cmd")
	if err != nil {
		t.Fatalf("scanning for callers: %v", err)
	}

	for _, m := range updaters {
		if referenced[m] {
			if why, listed := unreachableRepairPaths[m]; listed {
				t.Errorf("%s IS reachable now, but is still listed as a gap: %q. Delete the "+
					"entry -- a backlog that keeps things it has finished stops being read.",
					m, why)
			}
			continue
		}
		if _, listed := unreachableRepairPaths[m]; listed {
			continue
		}
		t.Errorf("%s has no caller anywhere outside tests, so the thing it exists to "+
			"undo cannot be undone through the product: an Update nothing reaches "+
			"means a row can be declared and never fixed, and a Retire nothing "+
			"reaches means it can be declared and never taken back. Either wire it "+
			"to a handler and a route, or add it to unreachableRepairPaths "+
			"saying what an operator loses without it.", m)
	}
}

// storeUpdateMethods returns every `func (s *SQLStore) Update…` or `Retire…`
// in dir.
//
// RETIRE WAS ADDED AFTER THE FACT, and finding what it found is the argument
// for it. This scanned only Update* for as long as it existed, so five
// withdrawal paths -- the whole net_* family -- sat complete and unreachable
// without anything noticing: five create routes, no retire routes,
// RetireNetGroup and its siblings called by nothing. An estate could declare a
// forwarder group and never take it back.
//
// A correction path and a withdrawal path are the same KIND of thing from this
// test's point of view: a store method written so somebody can undo a mistake,
// which is worth nothing if no route reaches it.
func storeUpdateMethods(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var out []string
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
			if !ok || fn.Recv == nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Update") &&
				!strings.HasPrefix(fn.Name.Name, "Retire") {
				continue
			}
			out = append(out, fn.Name.Name)
		}
	}
	return out, nil
}

// methodsReferencedUnder collects every selector name used in non-test Go
// files under the given directories, excluding internal/store's own
// definitions -- a method calling itself is not a caller.
func methodsReferencedUnder(root string, dirs ...string) (map[string]bool, error) {
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, d := range dirs {
		err := filepath.Walk(filepath.Join(root, d), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			inStore := strings.Contains(filepath.ToSlash(path), "/internal/store/")
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// Inside the store package, a reference through the receiver
				// `s` is the method's own neighbourhood rather than a caller
				// that would make it reachable from outside.
				if inStore {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "s" {
						return true
					}
				}
				seen[sel.Sel.Name] = true
				return true
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return seen, nil
}
