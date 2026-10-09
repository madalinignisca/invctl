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
	// The pin, as defaults/main.yml states it. Quotes are optional in YAML and
	// both spellings are accepted, because a future edit that drops them is a
	// formatting change and must not silently disable this check.
	rolePin = regexp.MustCompile(`(?m)^invctl_version:\s*"?([0-9]+\.[0-9]+\.[0-9]+)"?\s*$`)
	// The newest released version. CHANGELOG.md is newest-first, so the FIRST
	// match is the one. An `## [Unreleased]` heading cannot match: the pattern
	// requires digits, which is why it is spelled this way rather than
	// `\[([^\]]+)\]`.
	changelogNewest = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\]`)
)

// TestTheAnsibleRolePinMatchesTheChangelog fails when the deployment role pins
// a version that is not the newest release.
//
// THE ALTERNATIVES WERE BOTH WORSE. A line in a release checklist depends on
// somebody reading it on a busy day, which is the failure mode this repository
// keeps replacing with tests. Having the release workflow edit the variable and
// commit adds a new way for a release to go wrong, during the one process most
// worth keeping boring (spec D4).
//
// The consequence of it going stale is quiet rather than loud: the role keeps
// deploying a version that is no longer current, and nothing anywhere says so.
// A silent wrong answer is the class of failure that needs a test rather than a
// convention.
//
// EQUALITY, NOT "NOT BEHIND", AND DELIBERATELY. A release bumps the changelog
// heading and this pin in the same commit, so equality holds through a release.
// Pinning the role to a version that has no changelog entry should fail: it
// would deploy something no release note describes.
//
// Deliberately in internal/license, beside TestTheLinterPinMatchesCI: this
// package is already where the checks that read the repository's own files
// live, and a version pin is exactly that kind of fact.
func TestTheAnsibleRolePinMatchesTheChangelog(t *testing.T) {
	root := repoRoot(t)

	defaults, err := os.ReadFile(filepath.Join(root,
		"deploy", "ansible", "roles", "invctl", "defaults", "main.yml"))
	if err != nil {
		t.Fatalf("reading the role defaults: %v", err)
	}
	pin := rolePin.FindSubmatch(defaults)
	if pin == nil {
		t.Fatal("the role defaults declare no invctl_version -- if the pin moved, " +
			"this test must move with it rather than being deleted: a version stated " +
			"in two places is exactly the shape that needs something reading both")
	}

	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("reading the changelog: %v", err)
	}
	newest := changelogNewest.FindSubmatch(changelog)
	if newest == nil {
		t.Fatal("CHANGELOG.md has no `## [x.y.z]` heading")
	}

	if string(pin[1]) != string(newest[1]) {
		t.Errorf("the Ansible role pins invctl %s and the newest release is %s.\n"+
			"Edit deploy/ansible/roles/invctl/defaults/main.yml and set\n"+
			"    invctl_version: \"%s\"\n"+
			"A role pinned behind the releases deploys an old version and says nothing "+
			"about it, which is why this is a test and not a checklist line (spec D4).",
			pin[1], newest[1], newest[1])
	}
}
