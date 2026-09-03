// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package license

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var (
	makefilePin = regexp.MustCompile(`(?m)^GOLANGCI_VERSION\s*:?=\s*(v[0-9][^\s]*)`)
	workflowPin = regexp.MustCompile(`golangci-lint-action@v\d+\s*\n\s*with:\s*\n\s*version:\s*(v[0-9][^\s]*)`)
)

// TestTheLinterPinMatchesCI fails when the Makefile and the CI workflow name
// different golangci-lint versions.
//
// THEY ALREADY DID, AND IT COST TWO RED PUSHES IN ONE DAY. `make tools`
// installed v2.6.2 while the workflow ran v2.11.1, so `make lint` reported
// zero issues on a tree CI then rejected -- for a gosec rule (G122) that five
// minor versions of new checks had introduced. The local gate was not weaker
// by accident; it was a different gate wearing the same name.
//
// The Makefile already carried a careful argument for pinning rather than
// tracking latest, written after a new rule shipped mid-day and broke a tree
// that had linted clean that morning. The argument was right and the number
// under it had gone stale -- which is the failure this repository keeps
// finding, and the reason a version stated in two places needs something that
// reads both.
//
// Deliberately in internal/license rather than internal/store: this package is
// already the home of the checks that read the repository's own files rather
// than its behaviour, and a linter pin is exactly that kind of fact.
func TestTheLinterPinMatchesCI(t *testing.T) {
	root := repoRoot(t)

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("reading the Makefile: %v", err)
	}
	mk := makefilePin.FindSubmatch(makefile)
	if mk == nil {
		t.Fatal("the Makefile declares no GOLANGCI_VERSION -- if the pin moved, this " +
			"test must move with it rather than being deleted: two places naming a " +
			"version is exactly the shape that needs checking")
	}

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("reading the CI workflow: %v", err)
	}
	ci := workflowPin.FindSubmatch(workflow)
	if ci == nil {
		t.Fatal("the CI workflow passes no explicit version to golangci-lint-action -- " +
			"an unpinned action tracks whatever ships, which is the drift this pin exists " +
			"to prevent")
	}

	if string(mk[1]) != string(ci[1]) {
		t.Errorf("the Makefile pins golangci-lint %s and CI runs %s.\n"+
			"`make lint` would then report on a different gate than the one that decides "+
			"whether this branch merges -- which is how a tree linted clean locally and "+
			"was rejected by CI for a rule the local version did not have. Bump both, in "+
			"a commit that also handles what the new version finds.",
			mk[1], ci[1])
	}
}
