// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyOneFunctionWritesBundleMembership is what replaced a database
// constraint, and it exists because that replacement was a real cost rather
// than a free simplification.
//
// One cable belongs to at most one LIVE bundle. That rule cannot be a unique
// index: the lifecycle it depends on lives on cable_bundle, and a partial index
// on cable_bundle_member cannot reach another table on either engine. So
// migration 00068 has no such index and SetBundleMembers enforces it inside its
// own transaction instead -- the same shape tx.log's authorization rests on,
// and sound for the same reason: it holds exactly as long as every writer goes
// through the one place that checks.
//
// Which is a property, not a hope, and this is the test that makes it one. A
// second INSERT somewhere else would not fail any other test in this package:
// the rows would be valid, the audit would be written by whatever wrote them,
// and a cable would quietly sit in two live bundles until somebody asked the
// impact page what a cut takes out and got half an answer.
//
// The DELETE side is already covered -- TestTheOnlyFactDeletingStatementIsThe
// Prune allowlists cable_bundle_member to this same file as a wholesale set
// replacement. This is its complement.
func TestOnlyOneFunctionWritesBundleMembership(t *testing.T) {
	const (
		table  = "cable_bundle_member"
		writer = "cable_bundles.go"
	)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no source files found; this test is asserting nothing")
	}

	var offenders []string
	found := false
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(src), "INSERT INTO "+table) {
			continue
		}
		if filepath.Base(path) == writer {
			found = true
			continue
		}
		offenders = append(offenders, path)
	}

	if !found {
		t.Errorf("no INSERT INTO %s in %s. Either the write moved -- in which case "+
			"this test is now guarding nothing and must be pointed at wherever it "+
			"went -- or it was deleted, and one cable per live bundle is no longer "+
			"enforced anywhere at all.", table, writer)
	}
	for _, path := range offenders {
		t.Errorf("%s writes %s. Only %s may: the one-live-bundle-per-cable rule is "+
			"checked in SetBundleMembers' transaction because it cannot be a unique "+
			"index (the lifecycle is on the parent table). A second writer bypasses "+
			"that check, and nothing else in this package would notice -- the rows "+
			"are valid, and the damage only surfaces when somebody asks what a cut "+
			"takes out and gets half an answer.", path, table, writer)
	}
}
