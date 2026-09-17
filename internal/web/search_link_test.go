// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// THE SECOND HALF OF THE SEARCH GAP, and the half nobody was looking for.
//
// internal/store's census (TestEveryCreatableEntityIsIndexedOrArgued) answers
// "does this entity reach the index". This answers the question after it: a hit
// that comes back and cannot be clicked.
//
// partials/rows.html resolves a hit to a URL with an {{if eq .EntityType}}
// chain, and its final {{else}} renders a bare <span>. So an indexed type with
// no branch renders as plain text: found, named, and unreachable. No error, no
// warning, nothing in a log -- the reader sees the thing they searched for and
// has no way to get to it, which is arguably worse than not finding it, because
// now they know it exists.
//
// FOUND BY MEASUREMENT, NOT BY REVIEW. When this test was written the chain had
// four branches against nineteen indexed types. Fifteen were stranded, and had
// been for as long as each type had been indexed. Reading the template does not
// reveal that; only counting both sides does.
//
// BOTH DIRECTIONS, like every census in this repo. A branch for a type nothing
// indexes is dead template code that will never run and will outlive whatever
// made it plausible.

// searchHitUnlinked are indexed entity types that deliberately render without a
// link, and why. Each needs a reason that is about the ENTITY, not about
// somebody not having got round to it.
var searchHitUnlinked = map[string]string{
	// These four are rows on a list page, not pages of their own. Linking to
	// the bare list would be worse than not linking: the reader clicks, lands
	// on hundreds of rows, and has to find by eye the thing search had just
	// found for them. Give them a link when they get a detail page, not before.
	"prefix":    "no detail page; prefixes are rows on /prefixes.",
	"aggregate": "no detail page; aggregates are rows on /allocations.",
	"asn":       "no detail page; ASNs are rows on /allocations.",
	"ip_range":  "no detail page; ranges are rows on /allocations.",

	// An environment's page IS a map (/environments/{id}/map) rather than a
	// detail view. Sending a text-search hit into a topology diagram answers a
	// different question from the one that was typed.
	"environment": "its only per-id page is a topology map, not a detail view.",
}

var (
	searchDocType   = regexp.MustCompile(`searchDoc\{[^}]*EntityType:\s*"([a-z_0-9]+)"`)
	searchLinkType  = regexp.MustCompile(`eq \.EntityType "([a-z_0-9]+)"`)
	searchRowsFile  = filepath.Join("web", "templates", "partials", "rows.html")
	searchStoreGlob = filepath.Join("internal", "store", "*.go")
)

func TestEveryIndexedEntityTypeCanBeClickedOrSaysWhyNot(t *testing.T) {
	root := repoRoot(t)

	indexed := indexedEntityTypes(t, root)
	if len(indexed) == 0 {
		t.Fatal("found no searchDoc literals in internal/store. The scan no longer " +
			"matches the source, which reads as a clean census and is not one.")
	}

	raw, err := os.ReadFile(filepath.Join(root, searchRowsFile))
	if err != nil {
		t.Fatalf("reading %s: %v", searchRowsFile, err)
	}
	linked := map[string]bool{}
	for _, m := range searchLinkType.FindAllStringSubmatch(string(raw), -1) {
		linked[m[1]] = true
	}

	// Direction 1: every indexed type is clickable, or says why not.
	for _, entityType := range indexed {
		if linked[entityType] {
			if reason, listed := searchHitUnlinked[entityType]; listed {
				t.Errorf("searchHitUnlinked says %q renders unlinked (%q), but "+
					"%s now has a branch for it. Delete the entry: a stale "+
					"exemption stops describing the template and starts excusing "+
					"the next type that has no branch.",
					entityType, reason, searchRowsFile)
			}
			continue
		}
		if _, excused := searchHitUnlinked[entityType]; excused {
			continue
		}
		t.Errorf("%q is written into search_index but %s has no branch for it, so a "+
			"hit renders as an unlinked <span>.\n"+
			"    The reader sees the thing they searched for and cannot reach it. "+
			"Nothing errors; the final {{else}} swallows it.\n"+
			"    Add a branch pointing at its detail page, or add an argued entry "+
			"to searchHitUnlinked.", entityType, searchRowsFile)
	}

	// Direction 2: no branch for a type nothing indexes.
	indexedSet := map[string]bool{}
	for _, e := range indexed {
		indexedSet[e] = true
	}
	for entityType := range linked {
		if !indexedSet[entityType] {
			t.Errorf("%s has a link branch for %q, but nothing in internal/store "+
				"writes a searchDoc with that entity_type. The branch is dead: it "+
				"cannot run, so it cannot be seen to be wrong.",
				searchRowsFile, entityType)
		}
	}

	// Direction 3: an exemption for a type nothing indexes can never fail.
	for entityType := range searchHitUnlinked {
		if !indexedSet[entityType] {
			t.Errorf("searchHitUnlinked names %q, which nothing indexes. An entry "+
				"that can never fire is decoration.", entityType)
		}
	}
}

// indexedEntityTypes reads the entity_type of every searchDoc literal in the
// store package.
//
// Source scanning rather than a runtime enumeration, because there is no
// runtime list to enumerate: indexing is decentralised across sixteen files by
// design, and the set of indexed types exists only as literals. That is the
// same property that made the identity gap silent, so the guard has to read
// what the guard's subject reads.
func indexedEntityTypes(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, searchStoreGlob))
	if err != nil {
		t.Fatalf("globbing the store package: %v", err)
	}
	seen := map[string]bool{}
	for _, path := range matches {
		if filepath.Ext(path) != ".go" {
			continue
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		for _, m := range searchDocType.FindAllStringSubmatch(string(raw), -1) {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for entityType := range seen {
		out = append(out, entityType)
	}
	sort.Strings(out)
	return out
}
