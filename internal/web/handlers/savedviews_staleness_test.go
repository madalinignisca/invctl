// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"net/url"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestSavedViewKeysListsTierForServices pins fix 2: tier genuinely narrows
// the service list (services.go passes queryInt(r, "tier", ...) into
// serviceFilterFrom), so a view saved with a tier filter must not silently
// widen to every tier on reopen -- which is exactly what happens if "tier"
// is missing from the allowlist that both savedViewParamsFrom reads the
// posted form through and savedViewListPath replays.
func TestSavedViewKeysListsTierForServices(t *testing.T) {
	keys := savedViewKeys[domain.SavedViewService]
	found := false
	for _, k := range keys {
		if k == "tier" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("savedViewKeys[%q] = %v, missing \"tier\"", domain.SavedViewService, keys)
	}
}

// TestSavedViewStalenessFlagsAnUnrecognisedStoredKey pins fix 3. Without
// this check, a key that is renamed or removed from savedViewKeys is
// dropped SILENTLY by savedViewListPath (it only ever reads keys still in
// the current allowlist) -- so a stored reference to a retired filter just
// widens the view's result with no explanation, exactly the tier gap fix 2
// closes, generalised so the next one does not need its own review finding.
func TestSavedViewStalenessFlagsAnUnrecognisedStoredKey(t *testing.T) {
	got := savedViewStaleness(domain.SavedViewAsset, `{"not_a_real_filter":["x"]}`, savedViewVocabulary{})
	if got == "" {
		t.Fatal("an unrecognised stored key was not flagged as stale")
	}
}

// TestSavedViewStalenessChecksDeviceTypeProjectAndTag pins fix 4: the three
// id-valued keys (device_type_id, project, tag) are the likeliest way a
// saved view goes stale -- something is deleted, not renamed -- and a
// stale reference to one of them used to show zero rows with NO
// explanation, exactly the scenario docs/saved-views-design.md §6 exists to
// prevent.
func TestSavedViewStalenessChecksDeviceTypeProjectAndTag(t *testing.T) {
	cases := []struct {
		name   string
		entity string
		params string
		vocab  savedViewVocabulary
		wantOK bool
	}{
		{
			name:   "known device type is not stale",
			entity: domain.SavedViewAsset,
			params: `{"device_type_id":["dt-1"]}`,
			vocab:  savedViewVocabulary{DeviceTypeIDs: []string{"dt-1", "dt-2"}},
			wantOK: true,
		},
		{
			name:   "deleted device type is stale",
			entity: domain.SavedViewAsset,
			params: `{"device_type_id":["dt-gone"]}`,
			vocab:  savedViewVocabulary{DeviceTypeIDs: []string{"dt-1", "dt-2"}},
			wantOK: false,
		},
		{
			name:   "known project is not stale",
			entity: domain.SavedViewService,
			params: `{"project":["proj-1"]}`,
			vocab:  savedViewVocabulary{ProjectIDs: []string{"proj-1"}},
			wantOK: true,
		},
		{
			name:   "deleted project is stale",
			entity: domain.SavedViewService,
			params: `{"project":["proj-gone"]}`,
			vocab:  savedViewVocabulary{ProjectIDs: []string{"proj-1"}},
			wantOK: false,
		},
		{
			name:   "every stored tag known is not stale",
			entity: domain.SavedViewAsset,
			params: `{"tag":["tag-1","tag-2"]}`,
			vocab:  savedViewVocabulary{TagIDs: []string{"tag-1", "tag-2", "tag-3"}},
			wantOK: true,
		},
		{
			name:   "one deleted tag among several is stale",
			entity: domain.SavedViewAsset,
			params: `{"tag":["tag-1","tag-gone"]}`,
			vocab:  savedViewVocabulary{TagIDs: []string{"tag-1", "tag-2"}},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := savedViewStaleness(tc.entity, tc.params, tc.vocab)
			if tc.wantOK && got != "" {
				t.Errorf("Stale = %q, want \"\"", got)
			}
			if !tc.wantOK && got == "" {
				t.Errorf("Stale = \"\", want a non-empty explanation")
			}
		})
	}
}

// TestSavedViewStalenessLeavesFreeTextAndFixedEnumsAlone documents the
// deliberate remainder: lifecycle, availability and retired are fixed enum
// sets a stored value can never outlive, and q is free text with nothing to
// look up against. None of the four should ever be flagged.
func TestSavedViewStalenessLeavesFreeTextAndFixedEnumsAlone(t *testing.T) {
	got := savedViewStaleness(domain.SavedViewAsset,
		`{"lifecycle":["retired"],"retired":["1"],"q":["anything at all"]}`, savedViewVocabulary{})
	if got != "" {
		t.Errorf("Stale = %q, want \"\" for lifecycle/retired/q", got)
	}
}

// TestCurrentFiltersForEmitsOnePairPerStoredValue pins the read half of fix
// 1: the "Save this view" preview must offer every applied value for a
// repeating key like tag, not just the first -- otherwise the hidden inputs
// the form actually submits lie about what filtering the operator applied.
func TestCurrentFiltersForEmitsOnePairPerStoredValue(t *testing.T) {
	q := url.Values{"tag": {"tag-1", "tag-2"}, "kind": {"server"}}
	pairs := CurrentFiltersFor(q, domain.SavedViewAsset)

	tagCount := 0
	for _, p := range pairs {
		if p.Key == "tag" {
			tagCount++
		}
	}
	if tagCount != 2 {
		t.Fatalf("CurrentFiltersFor produced %d tag pairs, want 2: %+v", tagCount, pairs)
	}
}
