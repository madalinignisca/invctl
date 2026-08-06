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

	"github.com/madalinignisca/invctl/internal/diagram"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// The environment map: everything in one environment, all three layers --
// scope 3 of the diagram feature, the walkthrough view. The neighbourhood
// centres on an asset and the path on a pair; this has no centre at all, so
// there is no locked layer and no anchor: the toggles are the whole steering
// wheel, and they matter most here because this is the picture that gets
// dense first.

type envMapPage struct {
	Base
	Environment *domain.Environment
	View        diagramView
}

// EnvironmentMap renders one environment's layered picture.
func (a *App) EnvironmentMap(w http.ResponseWriter, r *http.Request) {
	env, err := a.Store.GetEnvironment(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	g, err := a.Store.EnvironmentMap(r.Context(), env.ID, 0)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	assetIDs := make([]string, len(g.Assets))
	for i, n := range g.Assets {
		assetIDs[i] = n.ID
	}
	assetHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableAsset, assetIDs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	instanceIDs := make([]string, len(g.Placements))
	for i, p := range g.Placements {
		instanceIDs[i] = p.InstanceID
	}
	instanceHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableServiceInstance, instanceIDs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// nil: no locked layer. This picture has no subject to anchor, so every
	// band is genuinely optional.
	view, err := buildEnvMapView(g, selectedLayers(r, nil), assetHealth, instanceHealth)
	if err != nil {
		a.serverError(w, r, fmt.Errorf("laying out the %s map: %w", env.Code, err))
		return
	}
	view.Action = "/environments/" + env.ID + "/map"
	view.Heading = "The " + env.Code + " estate"
	view.Note = "Three transport realities, stacked — every asset in the environment, " +
		"not a walk from anywhere."

	a.Render.Respond(w, r, http.StatusOK, "envmap", "envmap_panel", envMapPage{
		Base:        a.base(r, "Map: "+env.Code, "environments"),
		Environment: env,
		View:        view,
	})
}

// buildEnvMapView is buildDiagramView without a subject: no locked layer, no
// anchor to keep through orphan pruning -- an asset the filter stranded is
// still in the environment, so it stays drawn rather than pruned. Boxes with
// no lines are a fact about the estate here, not filter debris.
func buildEnvMapView(g *store.NeighbourhoodGraph,
	layers map[diagram.Layer]bool,
	assetHealth, instanceHealth map[string]store.EntityHealth) (diagramView, error) {

	view := diagramView{}
	nodes, edges, selfCabled := neighbourhoodElements(g, assetHealth, instanceHealth)

	nodesPerLayer := map[diagram.Layer]int{}
	edgesPerLayer := map[diagram.Layer]int{}
	for _, n := range nodes {
		nodesPerLayer[n.node.Layer]++
	}
	for _, e := range edges {
		edgesPerLayer[e.edge.Layer]++
	}
	for _, l := range diagramLayers {
		view.Layers = append(view.Layers, layerChoice{
			Name:  l.Layer.String(),
			Label: l.Label,
			Note:  l.Note,
			On:    layers[l.Layer],
			Nodes: nodesPerLayer[l.Layer],
			Edges: edgesPerLayer[l.Layer],
		})
	}

	kept, hiddenNodes := filterNodes(nodes, layers)
	present := make(map[string]bool, len(kept))
	for _, n := range kept {
		present[n.node.ID] = true
	}
	keptEdges, hiddenEdges := filterEdges(edges, present)

	view.Rows = adjacencyRows(keptEdges)
	view.Notes = neighbourhoodNotes(g, hiddenNodes, hiddenEdges, 0, selfCabled)

	if len(kept) == 0 {
		view.Empty = "Nothing in this environment survives the layer filter. Turn a layer back on."
		return view, nil
	}
	if len(g.Assets) == 0 {
		view.Empty = "Nothing is recorded in this environment yet."
		return view, nil
	}

	in := diagram.Input{}
	for _, n := range kept {
		in.Nodes = append(in.Nodes, n.node)
	}
	for _, e := range keptEdges {
		in.Edges = append(in.Edges, e.edge)
	}
	layout, err := diagram.Build(in)
	if err != nil {
		return view, err
	}
	view.Scene = buildScene(layout, kept, keptEdges, nil)
	return view, nil
}
