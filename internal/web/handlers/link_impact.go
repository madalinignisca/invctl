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

	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-B3's engine half, reaching the screen.
//
// The work package read DONE for weeks with this unbuilt: the tracer walked a
// cable end to end and nothing could ask what happens when one is severed. An
// operator's only recourse was to down an asset, which is a different and
// blunter question -- a switch losing power is not the same event as one of its
// uplinks being unplugged, and answering the second with the first overstates
// the blast radius every time.

type linkImpactPage struct {
	Base
	Link   *store.LinkEnds
	Result impact.Result
	// Cut is the connectivity answer, computed from the graph rather than
	// inferred from an empty Result -- see store.CircuitCut's doc comment on
	// why its three outcomes must never be conflated. A cable that joins
	// nothing and a cable whose loss changes nothing read identically in a
	// Result and need opposite sentences.
	Cut store.CircuitCut
	// HasImpact and HasNetworkFinding are the shared impact_result partial's
	// contract. Not optional: html/template errors on a field a struct does not
	// have, and the circuit page shipped a 500 that way because no test fetched
	// it. This page is fetched by TestCuttingACableReachesTheScreen.
	HasImpact         bool
	HasNetworkFinding bool
}

// LinkImpact simulates cutting one cable.
func (a *App) LinkImpact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, err := a.Store.GetLinkEnds(r.Context(), id)
	if err != nil {
		// 404 rather than an empty simulation, for AssetImpact's reason:
		// "nothing breaks" about a scenario nobody ran is the most dangerous
		// answer this tool can produce.
		a.handleStoreError(w, r, err)
		return
	}

	result, err := a.Store.Simulate(r.Context(), impact.Request{
		CutLinkIDs:    []string{id},
		WindowSeconds: queryInt(r, "window", 180, 1, 3650),
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	cut, err := a.Store.LinkCutEffect(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := linkImpactPage{
		Base:      a.base(r, "If "+link.Label()+" is cut", "assets"),
		Link:      link,
		Result:    result,
		Cut:       cut,
		HasImpact: len(result.Services) > 0 || len(result.WontRestart) > 0,
		HasNetworkFinding: len(result.Isolated) > 0 || len(result.Partitions) > 0 ||
			len(result.Unreachable) > 0 || len(result.RedundancyLost) > 0,
	}
	a.Render.Respond(w, r, http.StatusOK, "link_impact", "impact_result", data)
}
