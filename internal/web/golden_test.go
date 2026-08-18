// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// update regenerates the golden files under testdata/api instead of checking
// against them. Named "update" rather than "update-golden": nothing in this
// package already defines a flag by that name.
//
// Regenerate with:
//
//	go test ./internal/web/ -run TestThe -update
//
// and READ every file that changed before committing it -- a golden file
// generated without being read is a rubber stamp on whatever it just
// published. See docs/api-design.md §1 for what must never appear in one.
var update = flag.Bool("update", false, "update golden files under testdata/api")

// uuidPattern matches the lower-case hyphenated form google/uuid.String()
// produces -- the only form any id in this codebase takes (CLAUDE.md: "IDs
// are UUIDv7 as TEXT, generated in Go").
var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// normalizeGoldenJSON makes a response body comparable across test runs.
//
// Every id in the fixture estate is generated fresh by store.NewID() each
// time seed.Load runs (internal/seed/seed.go), so a golden file that pinned a
// literal id would flap on every run rather than only on a real contract
// change -- exactly the kind of golden that "only passes on the run that
// generated it" the task brief warns against. Nothing else in the published
// DTOs is generated or time-based: dto.go carries no timestamp field, so ids
// are the only value that needs normalising.
//
// ids are replaced in order of first appearance with a stable, sequential
// placeholder, not a single shared one -- so a real relationship between two
// fields in the same document (an address's asset_id equalling its asset's
// id, or a page's "next" cursor equalling the last row's "id") still shows up
// as the same placeholder in both places, and a regression that broke that
// relationship would still produce a diff.
func normalizeGoldenJSON(t *testing.T, body string) []byte {
	t.Helper()
	seen := map[string]string{}
	n := 0
	normalized := uuidPattern.ReplaceAllStringFunc(body, func(id string) string {
		if placeholder, ok := seen[id]; ok {
			return placeholder
		}
		n++
		placeholder := fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
		seen[id] = placeholder
		return placeholder
	})
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(normalized), "", "  "); err != nil {
		t.Fatalf("indenting response body for golden comparison: %v\nbody: %s", err, body)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// assertGoldenJSON compares a response body, normalised and indented, against
// the file at path. With -update it writes the file instead of comparing.
func assertGoldenJSON(t *testing.T, path, body string) {
	t.Helper()
	got := normalizeGoldenJSON(t, body)

	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("writing golden file %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v\n"+
			"run `go test ./internal/web/ -run TestThe -update` to generate it, "+
			"then READ it before committing", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("golden mismatch for %s:\n%s\n"+
			"run `go test ./internal/web/ -run TestThe -update` to regenerate, "+
			"then READ the file before committing",
			path, diffLines(string(want), string(got)))
	}
}

// diffLines is a small prefix/suffix diff, not a full unified diff: golden
// files here are short and hand-checked, so showing the differing middle
// block is enough to see what changed without pulling in a diff dependency
// this repo does not otherwise need.
func diffLines(want, got string) string {
	w := strings.Split(strings.TrimRight(want, "\n"), "\n")
	g := strings.Split(strings.TrimRight(got, "\n"), "\n")

	n := 0
	for n < len(w) && n < len(g) && w[n] == g[n] {
		n++
	}
	m := 0
	for m < len(w)-n && m < len(g)-n && w[len(w)-1-m] == g[len(g)-1-m] {
		m++
	}

	var b strings.Builder
	b.WriteString("--- want\n+++ got\n")
	for i := n; i < len(w)-m; i++ {
		fmt.Fprintf(&b, "-%s\n", w[i])
	}
	for i := n; i < len(g)-m; i++ {
		fmt.Fprintf(&b, "+%s\n", g[i])
	}
	return b.String()
}
