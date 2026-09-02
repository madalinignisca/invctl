// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"bufio"
	"os"
	"regexp"
	"testing"
)

// TestTheWriteScopeTableMatchesEntityScope reads docs/AUDIT.md's
// "Write-authorization scope classification" table off disk and checks it
// against entityScope in both directions -- exactly the shape of
// TestTheGoConstantSetMatchesTheDatabaseCheck above, and for the same
// reason: two sources of truth for a classification drift silently, and
// this branch found three hand-maintained lists a reclassification had
// already falsified before this test existed. WP-1.1 item 3 moved
// asset_cost, and items 1/2 moved dependency and link, out of ScopeTopology
// -- each one an entity whose scope class had been copied into prose
// somewhere and left there when the map changed underneath it.
//
// DELIBERATELY PARSES THE REAL DOCUMENT rather than embedding a second copy
// of the table here. A guard that consults a hand-maintained list of its
// own is that list with extra steps -- the exact mistake `aa58a29` had to
// re-do for the census in internal/store/permit_source_test.go. AUDIT.md's
// table is the one a reader actually looks at, so it is the one this test
// makes unfalsifiable, the same way the column classification table above
// it is made unfalsifiable by TestEveryColumnIsClassified.
//
// The document format this parses: a `### ScopeXxx` heading opens a
// section, and every `| \`entity_type\` | ... |` row beneath it, up to the
// next heading, belongs to that class. See docs/AUDIT.md's own comment
// pointing back at this test for the contract.
func TestTheWriteScopeTableMatchesEntityScope(t *testing.T) {
	const auditPath = "../../docs/AUDIT.md"
	f, err := os.Open(auditPath)
	if err != nil {
		t.Fatalf("opening %s: %v", auditPath, err)
	}
	defer f.Close()

	headingRe := regexp.MustCompile(`^### (ScopeProjectLinked|ScopeSubjectDerived|ScopeEstateConfig|ScopeTopology)\b`)
	sectionEndRe := regexp.MustCompile(`^#{1,6}\s`)
	rowRe := regexp.MustCompile("^\\|\\s*`([a-z0-9_]+)`\\s*\\|")

	fromDoc := make(map[string]ScopeClass)
	var current ScopeClass
	inScopeSection := false

	scanner := bufio.NewScanner(f)
	// AUDIT.md's longest table cell (the custom_field_value row in the
	// column-classification table above this one) is well over the
	// default 64KiB bufio.Scanner token limit, so a narrower buffer here
	// would silently truncate a line and make this test's own parse
	// wrong rather than the table it is checking -- 1MiB is comfortably
	// past anything this file holds today.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		if m := headingRe.FindStringSubmatch(line); m != nil {
			current = scopeClassFromHeadingName(m[1])
			inScopeSection = true
			continue
		}
		// Any OTHER heading -- including "## The rules" that follows the
		// four class sections -- closes the scope-table section so rows
		// in an unrelated table later in the file are never attributed
		// to whichever class happened to be seen last.
		if inScopeSection && sectionEndRe.MatchString(line) && headingRe.FindStringSubmatch(line) == nil {
			inScopeSection = false
			current = ""
			continue
		}
		if !inScopeSection || current == "" {
			continue
		}
		if m := rowRe.FindStringSubmatch(line); m != nil {
			entity := m[1]
			if prev, ok := fromDoc[entity]; ok {
				t.Errorf("docs/AUDIT.md lists %q twice, as %s and %s", entity, prev, current)
				continue
			}
			fromDoc[entity] = current
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning %s: %v", auditPath, err)
	}
	if len(fromDoc) == 0 {
		t.Fatalf("found no entity rows under a ScopeXxx heading in %s -- the parser's heading or row "+
			"pattern no longer matches the document, which would make this test pass for the wrong "+
			"reason (nothing to check) rather than fail loudly", auditPath)
	}

	// Direction 1: every entity the table names must have the class
	// entityScope actually gives it.
	for entity, docClass := range fromDoc {
		goClass := ScopeClassOf(entity)
		if goClass == "" {
			t.Errorf("docs/AUDIT.md classifies %q as %s, but internal/domain/role.go's entityScope "+
				"does not classify %q at all", entity, docClass, entity)
			continue
		}
		if goClass != docClass {
			t.Errorf("docs/AUDIT.md classifies %q as %s, but entityScope classifies it as %s -- "+
				"the table has drifted from the code it documents", entity, docClass, goClass)
		}
	}

	// Direction 2: every entity entityScope names must appear in the
	// table, under the class entityScope actually gives it.
	for entity, goClass := range entityScope {
		docClass, ok := fromDoc[entity]
		if !ok {
			t.Errorf("entityScope classifies %q as %s, but docs/AUDIT.md's write-authorization "+
				"scope table has no row for %q", entity, goClass, entity)
			continue
		}
		if docClass != goClass {
			// Already reported by direction 1's loop above; do not
			// double-count the same mismatch as two failures.
			continue
		}
	}
}

// scopeClassFromHeadingName maps the literal Go identifier a "### ScopeXxx"
// heading names to the ScopeClass constant it means. A switch rather than
// ScopeClass(name) alone so a heading whose spelling drifts from the real
// constant name (a typo, a rename only done on one side) is a compile-time
// impossible value caught by the calling test's comparison, not a silent
// string that happens to never equal anything real.
func scopeClassFromHeadingName(name string) ScopeClass {
	switch name {
	case "ScopeProjectLinked":
		return ScopeProjectLinked
	case "ScopeSubjectDerived":
		return ScopeSubjectDerived
	case "ScopeEstateConfig":
		return ScopeEstateConfig
	case "ScopeTopology":
		return ScopeTopology
	default:
		return ""
	}
}
