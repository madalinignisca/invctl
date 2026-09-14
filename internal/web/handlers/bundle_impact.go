// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"net/http"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
)

// "The backhoe went through the duct. What goes dark?"
//
// A bundle's own cut view, Task 3 of docs/cable-bundles-design.md: cutting a
// bundle is cutting every LIVE cable pulled through it at once, not one
// strand at a time -- which is the whole reason /links/{id}/impact is
// wrong by omission for a cable somebody bundled. See store.BundleCutEffect
// and store.SQLStore.cutEffect's doc comment for why this rides the same
// walker as a single cable's cut rather than a second implementation.

type bundleImpactPage struct {
	Base
	Bundle  *domain.CableBundle
	Members []store.BundleMemberRow
	Result  impact.Result
	// Cut is the connectivity answer for the WHOLE bundle -- see
	// store.CircuitCut's doc comment on its three outcomes, and
	// store.BundleCutEffect on why only live members count towards it.
	Cut store.CircuitCut
	// HasImpact and HasNetworkFinding are the shared impact_result partial's
	// contract -- see linkImpactPage's doc comment on why these are not
	// optional.
	HasImpact         bool
	HasNetworkFinding bool
}

// BundleImpact simulates cutting a whole bundle: every live cable in it, at
// once.
func (a *App) BundleImpact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bundle, err := a.Store.GetBundle(r.Context(), id)
	if err != nil {
		// 404 rather than an empty simulation, for AssetImpact's reason:
		// "nothing breaks" about a scenario nobody ran is the most dangerous
		// answer this tool can produce.
		a.handleStoreError(w, r, err)
		return
	}

	members, err := a.Store.ListBundleMembers(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Only LIVE members are cut. A retired cable stays in the bundle as
	// history (Task 2's rule) but a backhoe through the duct today does not
	// re-sever a cable somebody already unplugged.
	var liveLinkIDs []string
	for _, m := range members {
		if m.Lifecycle == domain.LifecycleActive {
			liveLinkIDs = append(liveLinkIDs, m.LinkID)
		}
	}

	result, err := a.Store.Simulate(r.Context(), impact.Request{
		CutLinkIDs:    liveLinkIDs,
		WindowSeconds: queryInt(r, "window", 180, 1, 3650),
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	cut, err := a.Store.BundleCutEffect(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := bundleImpactPage{
		Base:      a.base(r, "If "+bundle.Name+" is cut", "assets"),
		Bundle:    bundle,
		Members:   members,
		Result:    result,
		Cut:       cut,
		HasImpact: len(result.Services) > 0 || len(result.WontRestart) > 0,
		HasNetworkFinding: len(result.Isolated) > 0 || len(result.Partitions) > 0 ||
			len(result.Unreachable) > 0 || len(result.RedundancyLost) > 0,
	}
	a.Render.Respond(w, r, http.StatusOK, "bundle_impact", "impact_result", data)
}
