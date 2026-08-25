// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"context"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-G4a piece 3: the two tag pickers a filtered list page needs, which are
// deliberately NOT the same list (docs/tags-design.md §2, §4a, §5):
//
//   - The FILTER picker offers every tag, retired included -- "a retired tag
//     can still be FILTERED on" (design.md §2) because things still carry
//     it, and hiding it from the filter would make an already-tagged entity
//     unfindable by the very label it carries.
//   - The APPLY picker offers only live tags -- a retired tag "must not be
//     offered for new application" (design.md §4a), the same rule piece 2's
//     entity-page picker (entitytags.go's loadEntityTagsPanel) already
//     enforces for Offerable.
//
// Loaded once per list-page render and shared by AssetList and ServiceList
// rather than each parsing store.ListTags's result its own way.
func (a *App) loadTagListOptions(ctx context.Context) (filterTags []store.TagRow, applyTags []domain.Tag, err error) {
	all, err := a.Store.ListTags(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	live := make([]domain.Tag, 0, len(all))
	for _, t := range all {
		if !t.IsRetired() {
			live = append(live, t.Tag)
		}
	}
	return all, live, nil
}
