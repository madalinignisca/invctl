// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The rail is data now, which means it can be wrong in ways markup could not:
// a slug that no handler sets is a link that never lights up, and two links
// sharing a slug light up together. Both were live before this file existed --
// /power and /reports/power shared "power", so opening the report highlighted
// the inventory page.

// TestEveryRailSlugIsSetBySomeHandler.
//
// A link whose slug no page sets is dead: it navigates fine and never shows as
// current, so the rail silently stops saying where you are. Parsing the
// handlers rather than maintaining a second list, for the reason the other
// structural guards here give -- a list somebody has to update is a list that
// drifts.
func TestEveryRailSlugIsSetBySomeHandler(t *testing.T) {
	set := slugsSetByHandlers(t)
	for _, g := range navGroups {
		for _, l := range g.Links {
			if l.Nav == "" {
				continue // a filtered view of another page; see NavLink
			}
			if !set[l.Nav] {
				t.Errorf("the rail's %q link uses slug %q, which no handler passes to "+
					"a.base -- it will never render as the current page",
					l.Label, l.Nav)
			}
		}
	}
}

// TestNoTwoRailLinksShareASlug. Two links on one slug light up together, and
// the operator is told they are in two places at once.
func TestNoTwoRailLinksShareASlug(t *testing.T) {
	seen := map[string]string{}
	for _, g := range navGroups {
		for _, l := range g.Links {
			if l.Nav == "" {
				continue
			}
			if other, dup := seen[l.Nav]; dup {
				t.Errorf("%q and %q both use slug %q, so both highlight at once",
					other, l.Label, l.Nav)
			}
			seen[l.Nav] = l.Label
		}
	}
}

// TestNavForOpensExactlyTheGroupHoldingThePage. Landing somewhere whose rail
// entry is collapsed is disorienting; landing somewhere that opens three
// sections is noise.
func TestNavForOpensExactlyTheGroupHoldingThePage(t *testing.T) {
	for _, g := range navGroups {
		for _, l := range g.Links {
			if l.Nav == "" {
				continue
			}
			t.Run(l.Nav, func(t *testing.T) {
				var open []string
				for _, got := range NavFor(l.Nav) {
					if got.Open {
						open = append(open, got.Label)
					}
				}
				if len(open) != 1 || open[0] != g.Label {
					t.Errorf("on %s the rail opens %v, want exactly [%s]", l.Nav, open, g.Label)
				}
			})
		}
	}
}

// TestNavForDoesNotMutateTheSharedRail. NavFor returns a copy because two
// concurrent requests on different pages would otherwise race on Open, and the
// loser would render somebody else's section expanded.
func TestNavForDoesNotMutateTheSharedRail(t *testing.T) {
	NavFor("prefixes")
	NavFor("assets")
	for _, g := range navGroups {
		if g.Open {
			t.Errorf("the package-level rail has %q left open; NavFor mutated the "+
				"shared slice and two concurrent requests would fight over it", g.Label)
		}
	}
}

// slugsSetByHandlers reads every a.base(...) call in this package.
func slugsSetByHandlers(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the handler package: %v", err)
	}
	re := regexp.MustCompile(`a\.base\(r,\s*[^,]+,\s*"([^"]*)"\)`)
	// A handler may DERIVE its slug instead of passing a literal, which the
	// pattern above cannot see. Exactly one does: the asset list resolves its
	// rail entry from the kind filter, because /assets?kind=firewall is the
	// rail's Firewalls entry and must open Network rather than Estate.
	//
	// Matched on the call actually being there rather than exempted by name.
	// An exemption would go on excusing these slugs after somebody deleted the
	// call, which is precisely the dead link this test exists to catch.
	derived := regexp.MustCompile(`a\.base\(r,\s*[^,]+,\s*AssetListNav\(`)

	out := map[string]bool{}
	files := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		files++
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if m[1] != "" {
				out[m[1]] = true
			}
		}
		if derived.Match(b) {
			// Every slug AssetListNav can return, which is every rail entry
			// that filters the asset list -- asked of the rail, so adding an
			// entry needs no edit here.
			for _, g := range navGroups {
				for _, l := range g.Links {
					if strings.HasPrefix(l.Href, "/assets?kind=") && l.Nav != "" {
						out[l.Nav] = true
					}
				}
			}
		}
	}
	if files == 0 || len(out) == 0 {
		t.Fatal("found no a.base calls at all; this test would assert nothing")
	}
	return out
}
