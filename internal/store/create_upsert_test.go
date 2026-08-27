// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoCreatePathIsUpsertShaped is WP-G1 Task 14, Step 3's structural half:
// a create route a project owner can reach must be INSERT-only. Never
// `ON CONFLICT DO UPDATE`, never `INSERT OR REPLACE`, never an upsert
// helper -- a create that quietly becomes an update is how an existing
// entity gets seized, and the escalation test
// (TestPassingAnExistingAssetIdToTheCreatePathIsRefusedAndSeizesNothing,
// internal/web/project_create_test.go) asserts the BEHAVIOUR; this asserts
// the shape cannot even be written without the count budget below moving.
//
// Reuses parseSQLWrites (internal/store/boundary_source_test.go) rather than
// a second scanner, the same reuse role_management_test.go's structural
// check makes of the same helper.
//
// SCOPED TO create-shaped functions, not to the whole file: services.go
// genuinely contains ON CONFLICT elsewhere (ServiceInstance placement,
// unrelated to create), so a whole-file scan would either false-positive on
// that or have to special-case it away -- scoping to the exact functions
// this task added or touched is the structural equivalent of "a create
// route", which is the claim being tested.
var createShapedFuncs = map[string]map[string]bool{
	"internal/store/assets.go": {
		"CreateAsset": true, "insertAsset": true, "CreateAssetInProject": true,
		"insertProjectAssetLink": true,
	},
	"internal/store/services.go": {
		"CreateService": true, "insertService": true, "CreateServiceInProject": true,
		"insertProjectServiceLink": true,
	},
	"internal/store/circuits.go": {
		"CreateCircuit": true, "insertCircuit": true, "CreateCircuitInProject": true,
		"insertProjectCircuitLink": true,
	},
}

// upsertBudget is the EXACT count of ON CONFLICT/OR REPLACE clauses allowed
// across every function named above: zero. An exact budget, not a ceiling --
// the same reasoning dynamicTargetBudget and roleColumnBudget give for why a
// write that silently appears must move a number a reviewer set on purpose,
// not slide under a maximum nobody is watching.
const upsertBudget = 0

func TestNoCreatePathIsUpsertShaped(t *testing.T) {
	root := repoRoot(t)
	found := 0
	var offenders []string

	for rel, funcs := range createShapedFuncs {
		path := filepath.Join(root, rel)
		for _, stmt := range sqlStatementsInFuncs(t, path, funcs) {
			lower := strings.ToLower(stmt.sql)
			if strings.Contains(lower, "on conflict") || strings.Contains(lower, "or replace") {
				found++
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, stmt.line))
			}
		}
	}

	if found != upsertBudget {
		t.Errorf("found %d upsert-shaped write(s) in create paths, want exactly %d: %v",
			found, upsertBudget, offenders)
	}
}

// sqlStatementsInFuncs is sqlStatementsIn (boundary_source_test.go) narrowed
// to only the named functions' bodies, so a check can assert something about
// "a create method" without also matching every OTHER write statement the
// same file happens to contain.
func sqlStatementsInFuncs(t *testing.T, path string, funcNames map[string]bool) []sqlStatement {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	consts := packageStringConstants(t, filepath.Dir(path))
	for name, value := range fileStringConstants(file) {
		consts[name] = value
	}

	var out []sqlStatement
	seen := map[token.Pos]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !funcNames[fn.Name.Name] {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			var text string
			var pos token.Pos
			switch node := n.(type) {
			case *ast.BinaryExpr:
				if node.Op != token.ADD || seen[node.Pos()] {
					return true
				}
				text, pos = flattenStringConcat(node, consts)
				ast.Inspect(node, func(inner ast.Node) bool {
					switch inner.(type) {
					case *ast.BasicLit, *ast.BinaryExpr:
						seen[inner.Pos()] = true
					}
					return true
				})
			case *ast.BasicLit:
				if node.Kind != token.STRING || seen[node.Pos()] {
					return true
				}
				unquoted, err := strconv.Unquote(node.Value)
				if err != nil {
					return true
				}
				text, pos = unquoted, node.Pos()
			case *ast.Ident:
				if seen[node.Pos()] {
					return true
				}
				value, ok := consts[node.Name]
				if !ok {
					return true
				}
				text, pos = value, node.Pos()
			default:
				return true
			}
			if looksLikeSQL(text) {
				out = append(out, sqlStatement{sql: text, line: fset.Position(pos).Line})
			}
			return true
		})
	}
	return out
}
