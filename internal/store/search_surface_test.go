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

// THE CENSUS THAT WOULD HAVE CAUGHT IDENTITIES.
//
// WP-J8 gave identities a full surface -- list, detail, declare, correct,
// withdraw, record-rotation -- and every one of those paths worked. Nothing
// indexed them. `svc-orders` could be created, edited and audited, and typing
// its name into search returned nothing, for two days, until a person grepped
// for it. No test failed, because no test was asking.
//
// That is the whole shape of the defect class this file exists for. Indexing in
// this package is DECENTRALISED: sixteen files call indexEntity themselves, so
// adding an entity means remembering, and forgetting is silent by construction.
// A search that returns nothing looks exactly like a search that found nothing.
//
// KEYED ON CREATION, for the reason write_surface_test.go records: creation is
// the honest denominator. Keying on "entities that already have an index*
// helper" would report only on the ones somebody remembered, which is the
// question already answered.
//
// EVERY MAP HERE IS TWO-DIRECTIONAL. An exemption that stops applying -- an
// entity that gains indexing while still listed as exempt -- fails as loudly as
// an entity that is silently unindexed. An exemption nobody can rot into
// decoration is the only kind worth writing.

// searchExempt is every Create* method whose entity deliberately does not enter
// the search index, and why.
//
// "Nobody would search for it" is not a reason on its own -- it is the belief
// that produced the identity gap. A reason has to say what makes this entity
// different from the ones that ARE indexed: it has no name a person would type,
// it is reachable only through a parent that is itself indexed, or it is not a
// fact about the estate at all.
var searchExempt = map[string]string{
	// --- not a fact about the estate ---
	//
	// Search answers "where is the thing I am holding a name for". These are not
	// things in the estate, so a hit on one would answer a question nobody asked
	// and push a real hit off the page.
	"HealthOverride": "a temporary human assertion over observed state, cleared rather " +
		"than retired. It is a note about a reading, not a thing in the estate.",
	"ImportJob": "a record that an import ran. Telemetry of an operation, like " +
		"observed_transition and unmatched_observation, which docs/AUDIT.md rule 10 " +
		"already classes as not a fact about the estate.",
	"CustomField": "a field DEFINITION -- administrative configuration for what may be " +
		"recorded, not something recorded. The values are on the entities, and those " +
		"entities are indexed.",
	"SavedView": "one person's shortcut. ScrubUser deletes them outright (docs/AUDIT.md " +
		"rule 16) precisely because they belong to nobody once their owner is erased; " +
		"putting them in a surface every reader queries is the opposite of that.",

	// --- indexing it would widen a surface deliberately kept narrow ---
	"User": "a person. change_log stores an opaque app_user.id and never a username or " +
		"email so the trail carries no personal data; indexing people by name would " +
		"put into the WIDEST read surface exactly what the audit trail is careful to " +
		"keep out. An erasure request must not have to reach into a search index.",
	"JournalEntry": "free operator prose attached to an entity that is itself indexed. " +
		"A journal body can contain anything somebody typed, including personal data " +
		"and incident detail, and nothing validates it -- so it is the one text field " +
		"in the schema that must not become globally queryable.",

	// --- no name of its own: reachable only through an indexed parent ---
	//
	// These have no identifier a person would type. Indexing them would mean
	// inventing a display string, and a hit that reads "termination 3" helps
	// nobody.
	"CircuitTermination":   "one end of a circuit. The circuit carries the CID and is indexed.",
	"L2VPNTermination":     "one end of an L2VPN. The L2VPN is indexed.",
	"DeviceTypeComponents": "the template rows of a device type, which is indexed.",
	"PassThrough":          "what happens inside a patch panel. The panel is an asset and is indexed.",
	"Link": "a cable, and a cable has no name of its own -- seed_bundles.go keys them " +
		"by the two interfaces they join for exactly this reason. Both ends' assets " +
		"are indexed.",
	"Breakout": "the SAME reason as Link, one level up: a breakout is n cable rows -- " +
		"CreateBreakout writes plain `link` rows, one Create* call producing several " +
		"of an entity that is already exempt above. It has no name of its own either; " +
		"the shared a-end asset and every b-end asset are indexed.",
	"Dependency": "an edge between two services, both of which are indexed.",
	"Instance":   "a placement of a service on a host. Both the service and the host are indexed.",
	"Interface": "a port. `Ethernet46` is meaningful only beside its asset, which is " +
		"indexed; a global index of port names would return thousands of identical " +
		"titles. searchStructured already resolves a port through the address on it.",
	"PowerInput":    "an asset's inlet. The asset is indexed.",
	"NetAttachment": "which chassis a cable lands on -- a topology edge, not a named thing.",
	"NetAnchor":     "a topology edge.",
	"NetUplink":     "a topology edge.",

	// --- a second answer to a question something else already answers ---
	"IPAddress": "searchStructured already resolves a typed address to the interface " +
		"that holds it AND the prefix that contains it, with a `Why` explaining the " +
		"match. A text hit on the same string would be a second, worse answer " +
		"competing with it -- the shape WP-I1 refused when it declined to load " +
		"`link` into the reachability graph beside `net_attachment`.",

	// --- arguable, and deliberately NOT NOW rather than never ---
	//
	// Decided 2026-09-17 (scoping): this package fixes the identity gap and
	// builds the census. Widening the index is a separate decision with its own
	// cost -- every type added needs a link branch in partials/rows.html, a
	// ranking that still puts assets first, and an answer for how it behaves at
	// estate scale. Each entry below names what would make the case.
	"Endpoint": "arguable, and the strongest case in this group: an endpoint carries a " +
		"hostname and a port, which is exactly what somebody pastes out of a config. " +
		"Left out under the identity-only scoping of 2026-09-17, for a reason that is " +
		"about ranking rather than principle -- endpoints are the most numerous named " +
		"thing in the schema, so indexing them without first deciding how they rank " +
		"against assets would bury the asset hits that search exists to return. " +
		"Revisit WITH a ranking change, never on its own.",
	"PowerFeed": "arguable. Named, and reached from the panel and the asset it feeds, " +
		"both indexed. Note `EntityType: \"power_feed\"` in power_findings.go is a " +
		"FINDING, not a search document -- that string being present is what made a " +
		"grep-based read of this question wrong before this census existed.",
	"Provider": "arguable. An operator hunting a support contract might type `Hetzner`. " +
		"Revisit when somebody asks for it; /providers is one click from the rail today.",
	"Manufacturer": "arguable, same shape as Provider -- and device_type IS indexed with " +
		"its manufacturer's name in the document, so `Arista` already returns the models.",
	"BackendPool": "arguable. Named, but reached through the service it serves, which is indexed.",
	"NetGroup":    "arguable. Named, and /network lists them; no one has searched for one yet.",
	"PowerPanel":  "arguable. Named, and reached from the site asset, which is indexed.",
	"PowerSource": "arguable. Named, and reached from the site asset, which is indexed.",
	"RIR":         "arguable. A handful of rows that change once a decade; /allocations lists them all.",
	"Route":       "arguable. Reached through the prefix or aggregate it belongs to, both indexed.",
	"VLANGroup":   "arguable. The VLANs themselves ARE indexed, which is what an operator types.",
	"Tag": "arguable, and the only one here with a real counter-argument: a tag name is " +
		"exactly the kind of string somebody types. It is left out because tags are a " +
		"FILTER, and every list page already filters by them -- a search hit would " +
		"land on a tag rather than on the tagged things, which is not what the typist " +
		"wanted. Revisit as 'search by tag returns the tagged entities', which is a " +
		"different feature from indexing the tag row.",
}

// searchIndexEntry is the function every indexed write funnels through. Reaching
// it -- directly or through one helper -- is what "indexed" means here.
const searchIndexEntry = "indexEntity"

func TestEveryCreatableEntityIsIndexedOrArgued(t *testing.T) {
	// Glob + ParseFile rather than ParseDir: ParseDir is deprecated (it ignores
	// build tags), and this is the idiom write_surface_test.go already uses for
	// the same walk over the same package.
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing the store package: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", path, parseErr)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatalf("no non-test Go files found in %s", mustAbs(t, "."))
	}

	// calls maps a bare function or method name to the union of everything each
	// declaration of that name calls.
	//
	// UNION RATHER THAN PER-RECEIVER, and the reason is worth stating because it
	// is the unsafe direction. A `*SQLStore` method and a `*tx` method can share
	// a name -- `countOne` does, prune.go:320 and prune.go:329 -- and resolving
	// `x.countOne` to the right one needs go/types, not go/ast. Taking the union
	// makes a name look MORE reachable than it is, which for this census means a
	// false "indexed": exactly the miss it exists to prevent.
	//
	// So the union is paired with dangerousCollisions below, which fails if any
	// shared name actually sits on an index path. Today none do. The day one
	// does, this test says so by name instead of quietly passing an entity that
	// nothing indexes.
	calls := map[string]map[string]bool{}
	declCount := map[string]int{}
	creators := []string{}

	for _, file := range files {
		{
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				name := fn.Name.Name
				declCount[name]++
				called := calls[name]
				if called == nil {
					called = map[string]bool{}
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch f := call.Fun.(type) {
					case *ast.Ident:
						called[f.Name] = true
					case *ast.SelectorExpr:
						called[f.Sel.Name] = true
					}
					return true
				})
				calls[name] = called
				if strings.HasPrefix(name, "Create") && isSQLStoreMethod(fn) {
					creators = append(creators, name)
				}
			}
		}
	}

	if len(creators) == 0 {
		t.Fatal("found no Create* methods on *SQLStore. The scan is not matching " +
			"the source any more, which reads as a clean census and is not one.")
	}
	sort.Strings(creators)

	// The union above is only safe while no shared name sits on an index path.
	for name, n := range declCount {
		if n < 2 || name == searchIndexEntry {
			continue
		}
		if !reaches(calls, name, searchIndexEntry, map[string]bool{}) {
			continue
		}
		t.Fatalf("%d declarations share the name %q, and that name reaches %s.\n"+
			"    This scan resolves calls by bare name, so it can no longer tell "+
			"which one a Create* method actually calls -- and would report an "+
			"unindexed entity as indexed.\n"+
			"    Rename one of them, or teach this scan go/types.",
			n, name, searchIndexEntry)
	}

	var unindexed []string
	indexed := map[string]bool{}
	for _, creator := range creators {
		entity := strings.TrimPrefix(creator, "Create")
		if reaches(calls, creator, searchIndexEntry, map[string]bool{}) {
			indexed[entity] = true
			continue
		}
		unindexed = append(unindexed, entity)
	}

	// Direction 1: an unindexed entity must be argued.
	for _, entity := range unindexed {
		if _, argued := searchExempt[entity]; !argued {
			t.Errorf("Create%s writes no search_index row and searchExempt does not "+
				"say why.\n"+
				"    This is the identity defect: the entity is fully usable and "+
				"completely unfindable, and nothing fails.\n"+
				"    Either call s.index%s from Create%s (and from Update%s -- a "+
				"name corrected in the form and not in the index leaves search "+
				"answering with the typo), or add an argued entry to searchExempt.",
				entity, entity, entity, entity)
		}
	}

	// Direction 2: an exemption that no longer applies is a lie about the code,
	// and licenses whatever is written under that name next.
	for entity := range searchExempt {
		if indexed[entity] {
			t.Errorf("searchExempt says %s is deliberately unindexed, but Create%s "+
				"now reaches %s. Delete the entry -- a stale exemption stops "+
				"describing the code and starts excusing the next thing that "+
				"forgets.", entity, entity, searchIndexEntry)
		}
		if !contains(creators, "Create"+entity) {
			t.Errorf("searchExempt names %s, but there is no Create%s in this "+
				"package. A renamed or deleted entity leaves an entry that can "+
				"never fail, which is how a census rots.", entity, entity)
		}
	}
}

// reaches answers whether from transitively calls target.
func reaches(calls map[string]map[string]bool, from, target string, seen map[string]bool) bool {
	if seen[from] {
		return false
	}
	seen[from] = true
	for callee := range calls[from] {
		if callee == target {
			return true
		}
		if reaches(calls, callee, target, seen) {
			return true
		}
	}
	return false
}

func isSQLStoreMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "SQLStore"
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
