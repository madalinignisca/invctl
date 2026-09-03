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
	"regexp"
	"strings"
	"testing"
)

// docTestName matches a backtick-quoted Go test name in the design document.
var docTestName = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

// TestSectionNineNamesTestsThatExist keeps a documented test index honest.
//
// WRITTEN BECAUSE IT WAS NOT. §9 of docs/custom-fields-design.md lists the
// tests the work package promised. Seven of its thirteen names did not exist
// -- every one of those tests HAD been written and had shipped, under a
// different name. So the section read as a coverage inventory while being a
// false index, which is worse than having no list at all: an absent list
// invites a reader to go and look, and a wrong one tells them not to bother.
// Somebody checking whether "a retired option still keeps existing values"
// was tested would have searched the named test, found nothing, and
// reasonably concluded the property was unguarded.
//
// A rename is the whole mechanism. Nobody edits a design document because
// they improved a test's name, so the drift is silent and one-directional,
// and it accumulates -- this list was already wrong when the work package
// merged and stayed wrong through two releases.
//
// Deliberately name-based rather than behavioural: what rots here is the
// NAME, so the name is what has to be checked. The test does not care where
// the function lives, only that something under internal/ declares it --
// moving a test between packages is refactoring, renaming it is what breaks
// the index.
func TestSectionNineNamesTestsThatExist(t *testing.T) {
	root := repoRoot(t)

	doc, err := os.ReadFile(filepath.Join(root, "docs", "custom-fields-design.md"))
	if err != nil {
		t.Fatalf("reading the design document: %v", err)
	}
	section := sectionNine(t, string(doc))

	names := map[string]bool{}
	for _, m := range docTestName.FindAllStringSubmatch(section, -1) {
		names[m[1]] = true
	}
	if len(names) == 0 {
		t.Fatal("§9 names no tests at all -- either the section moved and this test is " +
			"looking in the wrong place, or the index was emptied; both need a person")
	}

	declared := map[string]bool{}
	err = filepath.Walk(filepath.Join(root, "internal"), func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`).
			FindAllStringSubmatch(string(src), -1) {
			declared[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	for name := range names {
		if !declared[name] {
			t.Errorf("docs/custom-fields-design.md §9 names %q, which no test declares. "+
				"Either the test was renamed and the document was not, or the coverage it "+
				"claims does not exist -- and those two need opposite fixes, so find out "+
				"which before editing either.", name)
		}
	}
}

// sectionNine returns the text of §9, so a name mentioned elsewhere in the
// document is not swept in.
func sectionNine(t *testing.T, doc string) string {
	t.Helper()
	start := strings.Index(doc, "## 9. Tests")
	if start < 0 {
		t.Fatal("docs/custom-fields-design.md has no \"## 9. Tests\" heading -- if the " +
			"section was renumbered, this test needs updating with it rather than deleting")
	}
	rest := doc[start+len("## 9. Tests"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}
