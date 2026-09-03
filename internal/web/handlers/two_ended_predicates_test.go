// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import "testing"

// scopedCovers is a minimal stand-in for Base.CanWriteEntity: true only for
// the ids listed, false for everything else -- including an entityType this
// test never names, which is deliberate: canWriteDependency/canWriteLink
// must pass entityType through unchanged, not assume it.
func scopedCovers(entityType string, ids ...string) func(string, string) bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(gotType, gotID string) bool {
		return gotType == entityType && set[gotID]
	}
}

// TestCanWriteDependencyIsTwoEnded pins fix-b item 5's extracted helper
// directly: canWriteDependency must require BOTH the consumer service and
// the provider service, never either alone.
func TestCanWriteDependencyIsTwoEnded(t *testing.T) {
	covers := scopedCovers("service", "svc-a", "svc-b")

	cases := []struct {
		name               string
		consumer, provider string
		want               bool
	}{
		{"both covered", "svc-a", "svc-b", true},
		{"consumer only", "svc-a", "svc-foreign", false},
		{"provider only", "svc-foreign", "svc-b", false},
		{"neither", "svc-foreign", "svc-other-foreign", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canWriteDependency(covers, tc.consumer, tc.provider); got != tc.want {
				t.Errorf("canWriteDependency(%q, %q) = %v, want %v", tc.consumer, tc.provider, got, tc.want)
			}
		})
	}
}

// TestCanWriteLinkIsTwoEnded is canWriteDependency's test, mirrored for
// canWriteLink -- both ends are assets, not services, and the entityType
// asked for must be "asset" (canWriteLink asking for "service" or
// "interface" instead would pass against a covers stub scoped to the wrong
// type and fail silently against the real permit).
func TestCanWriteLinkIsTwoEnded(t *testing.T) {
	covers := scopedCovers("asset", "asset-a", "asset-b")

	cases := []struct {
		name       string
		near, peer string
		want       bool
	}{
		{"both covered", "asset-a", "asset-b", true},
		{"near only", "asset-a", "asset-foreign", false},
		{"peer only", "asset-foreign", "asset-b", false},
		{"neither", "asset-foreign", "asset-other-foreign", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canWriteLink(covers, tc.near, tc.peer); got != tc.want {
				t.Errorf("canWriteLink(%q, %q) = %v, want %v", tc.near, tc.peer, got, tc.want)
			}
		})
	}
}

// TestCanWriteLinkAsksForAsset catches a mutation canWriteDependency and
// canWriteLink could each hide: swapping "asset" for "service" (or vice
// versa) inside the function body. A covers stub scoped to the OTHER
// entityType would refuse everything, so this alone would not distinguish
// "wrong type" from "wrong id" -- it is paired with the "both covered" cases
// above, which only pass if the entityType string threaded through matches
// what the stub was scoped to.
func TestCanWriteLinkAsksForAsset(t *testing.T) {
	// Scoped to "service", not "asset" -- canWriteLink must still ask for
	// "asset" and therefore get refused, proving it does not silently accept
	// whatever entityType a caller's covers function happens to recognise.
	servicesOnly := scopedCovers("service", "asset-a", "asset-b")
	if got := canWriteLink(servicesOnly, "asset-a", "asset-b"); got {
		t.Error("canWriteLink returned true against a covers function scoped to \"service\" -- " +
			"it must ask for \"asset\" specifically")
	}
}

// TestPickerHintDistinguishesEmptyEstateFromFiltering pins fix-b item 2's
// core distinction directly: total == 0 (the estate has nothing) must never
// produce the "belongs to someone else" wording, which is false for an
// Administrator whose permit covers everything; a genuine partial filter
// must produce neither the empty-estate nor the all-excluded wording, but a
// count; and no filtering at all (filtered == total, both nonzero) must
// produce no hint.
func TestPickerHintDistinguishesEmptyEstateFromFiltering(t *testing.T) {
	const (
		emptyEstate = "estate has nothing"
		allExcluded = "nothing you can offer"
		someFmt     = "showing %d of %d"
	)
	cases := []struct {
		name            string
		filtered, total int
		want            string
	}{
		{"genuinely empty estate", 0, 0, emptyEstate},
		{"everything filtered out", 0, 5, allExcluded},
		{"partially filtered", 2, 5, "showing 2 of 5"},
		{"unfiltered, nonzero", 5, 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickerHint(tc.filtered, tc.total, emptyEstate, allExcluded, someFmt)
			if got != tc.want {
				t.Errorf("pickerHint(%d, %d, ...) = %q, want %q", tc.filtered, tc.total, got, tc.want)
			}
		})
	}
}
