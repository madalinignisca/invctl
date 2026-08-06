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
	"strings"

	"github.com/gabriel/invctl/internal/diagram"
	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// The path page: how does THIS reach THAT.
//
// The neighbourhood shows one asset's surroundings; this shows the chain
// between two things and nothing else -- each end a service or an asset, and
// with no far end at all, "where does this service actually sit". Everything
// downstream of the store query is the neighbourhood's own pipeline: the same
// element builder, the same layout, the same scene, so a path is drawn with
// exactly the vocabulary the operator already learned one page over.

// pathPage is the full page; pathView the swap target.
type pathPage struct {
	Base
	// From and To echo the request, so the pickers show what was asked.
	From, To string
	// Services and Assets fill the pickers.
	Services []store.ServiceRow
	Assets   []store.AssetRow
	View     *pathView
}

type pathView struct {
	// FromLabel and ToLabel are the question in words: "orders-api", "hv-01",
	// "its data-plane network".
	FromLabel, ToLabel string
	// Found gates every statement about route length. Without it the header
	// said "0 hops at the shortest" beside a body saying "no path to draw":
	// a Scene exists whenever there are boxes, and the two stranded sides are
	// boxes, so drawing something is not the same as having found a route.
	Found bool
	Hops  int
	Scene *svgScene
	Empty string
	Notes []string
	Rows  []adjacencyRow
}

// ServicePath renders the chain between two chosen ends. With nothing chosen
// it renders the pickers and an explanation instead of an error: an empty
// question is how the page is reached from the navigation.
func (a *App) ServicePath(w http.ResponseWriter, r *http.Request) {
	fromKind, fromID := splitEndParam(r.URL.Query().Get("from"))
	toKind, toID := splitEndParam(r.URL.Query().Get("to"))

	page := pathPage{
		Base: a.base(r, "Path", "paths"),
		From: r.URL.Query().Get("from"),
		To:   r.URL.Query().Get("to"),
	}

	// The pickers, on every render: the page IS the form.
	services, err := a.Store.ListServices(r.Context(), store.ServiceFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	assets, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	page.Services, page.Assets = services, assets

	if fromKind != "" {
		view, err := a.buildPathView(r, fromKind, fromID, toKind, toID)
		if err != nil {
			a.handleStoreError(w, r, err)
			return
		}
		page.View = view
	}

	a.Render.Respond(w, r, http.StatusOK, "path", "path_panel", page)
}

// splitEndParam parses "service:<id>" / "asset:<id>". Anything else -- absent,
// or a shape the pickers never produce -- is treated as no selection.
func splitEndParam(v string) (kind, id string) {
	kind, id, ok := strings.Cut(v, ":")
	if !ok || id == "" || (kind != "service" && kind != "asset") {
		return "", ""
	}
	return kind, id
}

func (a *App) buildPathView(r *http.Request, fromKind, fromID, toKind, toID string) (*pathView, error) {
	q := store.PathQuery{}
	if fromKind == "service" {
		q.FromServiceID = fromID
	} else {
		q.FromAssetID = fromID
	}
	switch toKind {
	case "service":
		q.ToServiceID = toID
	case "asset":
		q.ToAssetID = toID
	}

	g, err := a.Store.Path(r.Context(), q)
	if err != nil {
		return nil, err
	}

	view := &pathView{Found: g.Found, Hops: g.Hops}
	if view.FromLabel, err = a.pathEndLabel(r, fromKind, fromID); err != nil {
		return nil, err
	}
	if toKind == "" {
		view.ToLabel = "its data-plane network"
	} else if view.ToLabel, err = a.pathEndLabel(r, toKind, toID); err != nil {
		return nil, err
	}

	// The same graph shape the neighbourhood renders, so the same pipeline
	// draws it. Hops here is display-only; nothing downstream reads it.
	ng := &store.NeighbourhoodGraph{
		Assets:       g.Assets,
		Links:        g.Links,
		Services:     g.Services,
		Placements:   g.Placements,
		Endpoints:    g.Endpoints,
		Dependencies: g.Dependencies,
	}

	assetIDs := make([]string, len(ng.Assets))
	for i, n := range ng.Assets {
		assetIDs[i] = n.ID
	}
	assetHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableAsset, assetIDs)
	if err != nil {
		return nil, err
	}
	instanceIDs := make([]string, len(ng.Placements))
	for i, p := range ng.Placements {
		instanceIDs[i] = p.InstanceID
	}
	instanceHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableServiceInstance, instanceIDs)
	if err != nil {
		return nil, err
	}

	nodes, edges, selfCabled := neighbourhoodElements(ng, assetHealth, instanceHealth)
	view.Rows = adjacencyRows(edges)
	view.Notes = pathNotes(g, view, selfCabled)

	if !g.Found {
		view.Empty = pathEmptyReason(g, view)
	}
	if len(nodes) == 0 {
		return view, nil
	}

	in := diagram.Input{}
	for _, n := range nodes {
		in.Nodes = append(in.Nodes, n.node)
	}
	for _, e := range edges {
		in.Edges = append(in.Edges, e.edge)
	}
	layout, err := diagram.Build(in)
	if err != nil {
		return nil, fmt.Errorf("laying out the path: %w", err)
	}
	view.Scene = buildScene(layout, nodes, edges, nil)
	view.Scene.Title = "Path: " + view.FromLabel + " to " + view.ToLabel
	return view, nil
}

func (a *App) pathEndLabel(r *http.Request, kind, id string) (string, error) {
	if kind == "service" {
		svc, err := a.Store.GetService(r.Context(), id)
		if err != nil {
			return "", err
		}
		return svc.Code, nil
	}
	asset, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		return "", err
	}
	return asset.Name, nil
}

// pathNotes are the honest caveats, in the same voice as the neighbourhood's.
func pathNotes(g *store.PathGraph, view *pathView, selfCabled int) []string {
	var notes []string
	// Always said, because it is the one modelling decision that changes the
	// answer: the OOB network reaches everything, and a path through it would
	// be a lie about how traffic flows.
	notes = append(notes, "Data plane only — management cabling is never part of a path.")

	if g.From.ServiceID != "" && len(g.From.AssetIDs) == 0 {
		notes = append(notes, view.FromLabel+" has no active placements, so there is nowhere for its traffic to leave from.")
	}
	if g.To.ServiceID != "" && len(g.To.AssetIDs) == 0 {
		notes = append(notes, view.ToLabel+" has no active placements, so there is nothing to reach.")
	}
	if len(g.Unrouted) > 0 && g.Found {
		notes = append(notes, "No data-plane route from: "+strings.Join(g.Unrouted, ", ")+
			" — drawn unconnected, because a stranded placement is a finding, not clutter.")
	}
	if len(g.Blocked) > 0 {
		notes = append(notes, "A disabled port is declared intent, not observed health — "+
			"nobody reported this down, somebody shut it.")
	}
	if g.AssetsElided > 0 {
		notes = append(notes, pluraliseCount(g.AssetsElided, "further asset", "further assets")+
			" on the way are not drawn — the picture is capped so a service with hundreds of "+
			"placements cannot turn one page into a picture of the whole estate. The two ends are always kept.")
	}
	if selfCabled > 0 {
		notes = append(notes, pluraliseCount(selfCabled, "cable", "cables")+
			" join two ports of the same asset and are not drawn — at this zoom a node is an asset, so the line would start and end in the same box.")
	}
	return notes
}

// pathEmptyReason says why there is no chain, specifically enough to act on.
func pathEmptyReason(g *store.PathGraph, view *pathView) string {
	switch {
	case len(g.Blocked) > 0:
		// The most actionable answer this page can give: the cabling is
		// there, and a port is shut. Naming the port turns "there is no path"
		// into a thing somebody can go and fix.
		return "The cabling between " + view.FromLabel + " and " + view.ToLabel +
			" is in place, but every route runs through an administratively disabled port: " +
			strings.Join(g.Blocked, ", ") + ". Enable the port and the path exists."
	case g.From.ServiceID != "" && len(g.From.AssetIDs) == 0:
		return view.FromLabel + " has no active placements. Record where it runs and the path can be drawn."
	case g.To.ServiceID != "" && len(g.To.AssetIDs) == 0:
		return view.ToLabel + " has no active placements, so there is no far side to reach."
	case len(g.To.AssetIDs) == 0:
		return "Nothing in " + view.FromLabel + "'s containment ancestry carries a data-plane network attachment, so \"its network\" is not modelled yet. Absence of topology data is not evidence of disconnection."
	default:
		return "No active data-plane cabling joins " + view.FromLabel + " and " + view.ToLabel + ". The boxes drawn are the two sides; the gap between them is the finding."
	}
}
