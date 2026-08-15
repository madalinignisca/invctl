// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"strings"
	"testing"
)

// TestTheCapacityPanelRendersAndSaysWhatItCannotSee.
//
// The panel is fetched rather than trusted to compile, for the reason the
// circuit impact page taught this codebase: a template that references a field
// the page struct does not carry returns 500 for every request while every
// other test stays green.
func TestTheCapacityPanelRendersAndSaysWhatItCannotSee(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	clusters := body(t, h.get("/clusters", false))
	id := firstClusterID(t, clusters)
	page := body(t, h.get("/clusters/"+id, false))

	if !strings.Contains(page, "Capacity") {
		t.Fatal("the cluster page has no capacity panel")
	}
	if !strings.Contains(page, "Usable vCPU") {
		t.Error("no usable vCPU figure")
	}
	// The base fixture declares no sizes at all, so the panel must lead with
	// what it could not see rather than reporting a confident zero.
	if !strings.Contains(page, "floor, not a total") {
		t.Error("an unmeasured cluster does not warn that its figures are a floor")
	}
}

// firstClusterID pulls a cluster id off the list page.
func firstClusterID(t *testing.T, page string) string {
	t.Helper()
	const marker = `href="/clusters/`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatal("no cluster on the list page")
	}
	rest := page[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatal("malformed cluster link")
	}
	return rest[:j]
}
