// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"fmt"
	"net/http"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
)

// "The fibre is cut. What goes dark?"
//
// A SEPARATE ENTRY POINT BECAUSE A CIRCUIT IS NOT AN ASSET. /assets/{id}/impact
// takes a set of things out of the containment tree and asks what falls with
// them. A circuit is not in that tree: nothing is inside it, and cutting it
// removes a connectivity EDGE rather than a vertex. Routing it through the
// asset simulator would mean either teaching the closure walk about a kind of
// row that is not in it, or pretending the circuit's terminating interfaces
// went away -- which is a different and wronger question, since the ports are
// fine and it is the span between them that is gone.
//
// The result page is the same one, deliberately. An operator reading "what goes
// dark" should not have to learn a second layout because the cause was a fibre
// rather than a hypervisor.

type circuitImpactPage struct {
	Base
	Circuit *domain.Circuit
	Result  impact.Result
	// Cut is the CONNECTIVITY answer, which the impact Result cannot give on
	// its own: a partition nothing depends on produces no findings and reads
	// exactly like no partition at all.
	Cut               store.CircuitCut
	HasNetworkFinding bool
}

// CircuitImpact simulates the loss of one circuit.
func (a *App) CircuitImpact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	circuit, err := a.Store.GetCircuit(r.Context(), id)
	if err != nil {
		// A 404 rather than an empty simulation, for the reason AssetImpact
		// gives: "nothing breaks" about a scenario nobody ran is the most
		// dangerous answer this tool can produce.
		a.handleStoreError(w, r, err)
		return
	}

	result, err := a.Store.Simulate(r.Context(), impact.Request{
		CutCircuitIDs: []string{id},
		WindowSeconds: queryInt(r, "window", 180, 1, 3650),
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Whether this circuit is an edge, and whether cutting it actually
	// separates anything, are facts about the model rather than about the
	// outage -- so they are asked of the graph rather than inferred from an
	// empty result.
	cut, err := a.Store.CircuitCutEffect(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := circuitImpactPage{
		Base:    a.base(r, fmt.Sprintf("If %s is cut", circuit.CID), "circuits"),
		Circuit: circuit,
		Result:  result,
		Cut:     cut,
		HasNetworkFinding: len(result.Isolated) > 0 || len(result.Partitions) > 0 ||
			len(result.Unreachable) > 0 || len(result.RedundancyLost) > 0,
	}
	a.Render.Respond(w, r, http.StatusOK, "circuit_impact", "impact_result", data)
}
