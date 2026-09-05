// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheRetiredDrawVAHintIsGoneEverywhere is R4/item 18. §2.1 named FOUR
// places that fail to distinguish "my half" from "the whole load recorded
// twice" -- the schema, the form, checkDraw and the column comment -- and the
// first fix round changed only the form. The phrase "a nameplate or allocated
// figure somebody typed" survived verbatim in the other three plus a fourth
// place §2.1 never enumerated (docs/AUDIT.md), and docs/ROADMAP.md quoted the
// retired form hint as though it were still current.
//
// This is the guard the round-2 review found missing: every other fix that
// round got a test, and the one whose entire deliverable was a sentence did
// not. Pinned here rather than beside any one of the five files, because the
// defect this guards against is exactly one of them drifting back to the old
// wording while the others move on.
func TestTheRetiredDrawVAHintIsGoneEverywhere(t *testing.T) {
	root := repoRoot(t)

	retired := "a nameplate or allocated figure somebody typed"
	canonical := "the whole nameplate load of this asset, not a share of it and not what it passes on to something downstream"

	files := []string{
		"internal/store/migrations/sqlite/00023_power.sql",
		"internal/store/migrations/postgres/00023_power.sql",
		"internal/domain/power.go",
		"internal/domain/classification.go",
		"docs/AUDIT.md",
	}

	for _, f := range files {
		body := normalizeComment(read(t, root, f))
		if strings.Contains(body, retired) {
			t.Errorf("%s still contains the retired hint %q -- D7's ambiguity survives here", f, retired)
		}
		if !strings.Contains(body, canonical) {
			t.Errorf("%s does not carry the canonical whole-load wording %q", f, canonical)
		}
	}

	// docs/ROADMAP.md is a decision LOG: it may still quote the retired hint as
	// history ("the form said so at the time"), but must not present it in a
	// way that reads as the current wording.
	roadmap := read(t, root, "docs/ROADMAP.md")
	if !strings.Contains(roadmap, "said so at the time") {
		t.Error("docs/ROADMAP.md quotes the retired form hint without marking it as historical; " +
			"a reader would take it as the current wording")
	}
}

// normalizeComment collapses line-wrapped prose -- across "-- " SQL comment
// continuations and "// " Go comment continuations, each possibly indented --
// into one run of spaces, so a phrase pinned as a single sentence can be found
// regardless of where the source happened to wrap it.
func normalizeComment(s string) string {
	var words []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "--")
		line = strings.TrimPrefix(line, "//")
		words = append(words, strings.Fields(line)...)
	}
	return strings.Join(words, " ")
}

func read(t *testing.T, root, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("finding the repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}
