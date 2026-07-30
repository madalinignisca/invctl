package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gabriel/invctl/internal/diagram"
	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// The layered neighbourhood diagram.
//
// One asset, what it actually connects to, and the three transport realities
// that connection lives in stacked one above the other, so that a line in the
// service layer can be followed DOWN to a line in the physical one. That
// descent is the whole point; without it this would be three unrelated pictures
// sharing a page.
//
// It is a VIEW and nothing else. There is no affordance anywhere on it that
// changes the estate, or the model of the estate: no "fix this cabling", no
// derive-and-apply. invctl presents state and never acts on it, and a diagram
// is the purest case of that rule.
//
// Three decisions are worth stating because they are easy to undo by accident:
//
//   - The SVG is generated in Go and rendered through html/template like every
//     other view. SVG is XML, so escaping matters exactly as much as it does in
//     HTML; nothing here concatenates markup and nothing is ever marked
//     template.HTML. An asset called `R&D <core>` is a legal asset name and it
//     has to render as an asset name rather than as a broken document.
//   - The layer toggles and the hop count are QUERY PARAMETERS and the whole
//     panel re-renders server-side. They are part of the question, and the
//     question is restated in the toolbar, the legend, the notes and the table.
//     Swapping only the picture would leave every one of those describing a
//     neighbourhood nobody is looking at -- the same defect the impact page's
//     window selector was rewritten to avoid. Either every restatement
//     re-renders or none may, so the swap target contains all of them.
//   - Alpine holds zoom and pan, which are local UI state and nothing else. It
//     does not fetch, and it holds no fact about the estate.

// The layer names, as they appear in a URL and as a CSS class suffix. They are
// the diagram package's own vocabulary (diagram.Layer.String()) so that a
// toggle, a band and a stylesheet rule cannot drift apart.
var diagramLayers = []struct {
	Layer diagram.Layer
	Label string
	// Note explains what the layer contains, for the legend. An operator who
	// has never seen this page should be able to tell what a band means
	// without being told out loud.
	Note string
}{
	{diagram.LayerService, "Service", "listening sockets and the declared dependencies between them"},
	{diagram.LayerVirtual, "Virtual", "guests, bridges and the veths between them — adjacency inside a host"},
	{diagram.LayerPhysical, "Physical", "wires between ports on physical devices"},
}

// virtualKinds are the asset kinds drawn in the virtual band.
//
// The vocabulary decides, not the ports: an asset's kind is what an operator
// declared it to be, and a VM with no interfaces recorded yet is still a VM. A
// kind added by INSERT that nobody classified here lands in the physical band,
// which is the safe reading -- it is a box, drawn at the bottom, rather than a
// box silently promoted into a layer it was never considered for.
var virtualKinds = map[string]bool{
	domain.KindVM:      true,
	domain.KindK8sNode: true,
	domain.KindBridge:  true,
	domain.KindCluster: true,
}

// assetLayer places one box.
func assetLayer(kind string) diagram.Layer {
	if virtualKinds[kind] {
		return diagram.LayerVirtual
	}
	return diagram.LayerPhysical
}

// virtualForms are the interface form factors that make an adjacency virtual
// rather than physical.
//
// This is the discriminator the fixture's own comment names: medium and
// length_m are NULL on every virtual link in the seed, but they are NULL on
// plenty of real cables too, so reading NULL as "virtual" would be right by
// accident. A veth has a virtual port on both ends and a bridge uplink lands on
// a bond; a cable has a physical connector on both ends.
var virtualForms = map[string]bool{
	domain.FFVirtual:  true,
	domain.FFLAG:      true,
	domain.FFLoopback: true,
}

func linkLayer(a, b store.NeighbourPort) diagram.Layer {
	if virtualForms[a.FormFactor] || virtualForms[b.FormFactor] {
		return diagram.LayerVirtual
	}
	return diagram.LayerPhysical
}

// ---------------------------------------------------------------------------
// The handler

// neighbourhoodHopMin is the shortest useful walk: the subject's own subtree
// plus one step out of it.
const neighbourhoodHopMin = 1

type neighbourhoodPage struct {
	Base
	Asset     *store.AssetRow
	Ancestors []domain.Asset
	View      diagramView
}

// diagramView is the whole swap target: the question, the picture, the legend,
// the caveats and the table. One struct, one fragment, one restatement.
type diagramView struct {
	Hops       int
	HopChoices []int
	Layers     []layerChoice

	// Scene is nil when there is nothing to draw, in which case Empty says why
	// in words. A blank canvas is not an answer.
	Scene *svgScene
	Empty string
	// Notes are the honest caveats: what the node budget cut, what the layer
	// filter hid, which declared edges reach outside the picture. A cut nobody
	// is told about is indistinguishable from a fact that does not exist.
	Notes []string
	// Rows is the same graph as a table. It is not a fallback bolted on: every
	// other page in this tool is a table, this would otherwise be the one thing
	// a screen reader gets nothing from, and "I need to paste this into a
	// ticket" is answered by text and never by a picture.
	Rows []adjacencyRow
}

// layerChoice is one toggle, with the counts that make an empty layer legible
// as empty rather than as absent.
type layerChoice struct {
	Name  string
	Label string
	Note  string
	On    bool
	// Locked is set on the subject's own layer. Hiding the asset the picture is
	// built around would leave the diagram with no anchor and the operator with
	// no way back, so that one toggle stays on and says why.
	Locked bool
	Nodes  int
	Edges  int
}

// adjacencyRow is one edge, in words.
type adjacencyRow struct {
	Layer  string
	Kind   string
	From   string
	FromAt string
	To     string
	ToAt   string
	Detail string
}

// AssetNeighbourhood renders the layered diagram for one asset.
func (a *App) AssetNeighbourhood(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	asset, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	ancestors, err := a.Store.Ancestors(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	hops := clampHops(queryInt(r, "hops", store.NeighbourhoodDefaultHops))
	graph, err := a.Store.Neighbourhood(r.Context(), store.NeighbourhoodQuery{
		AssetID: id, Hops: hops,
	})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	// Observed health, so a box that is down is the same colour here as it is
	// on every other page. Two calls for the whole picture rather than two per
	// node.
	assetIDs := make([]string, len(graph.Assets))
	for i, n := range graph.Assets {
		assetIDs[i] = n.ID
	}
	assetHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableAsset, assetIDs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	instanceIDs := make([]string, len(graph.Placements))
	for i, p := range graph.Placements {
		instanceIDs[i] = p.InstanceID
	}
	instanceHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableServiceInstance, instanceIDs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	view, err := buildDiagramView(graph, asset, selectedLayers(r, assetLayer(asset.Kind)),
		assetHealth, instanceHealth)
	if err != nil {
		a.serverError(w, r, fmt.Errorf("laying out the neighbourhood of %s: %w", id, err))
		return
	}

	a.Render.Respond(w, r, http.StatusOK, "neighbourhood", "neighbourhood_panel", neighbourhoodPage{
		Base:      a.base(r, "Neighbourhood: "+asset.Name, "assets"),
		Asset:     asset,
		Ancestors: ancestors,
		View:      view,
	})
}

func clampHops(n int) int {
	switch {
	case n < neighbourhoodHopMin:
		return neighbourhoodHopMin
	case n > store.NeighbourhoodMaxHops:
		return store.NeighbourhoodMaxHops
	default:
		return n
	}
}

// selectedLayers reads the toggles out of the URL.
//
// A bare URL with no `layer` parameter shows everything, so a link pasted into
// a ticket without the toolbar's help is the complete picture. `filter=1` is
// what the toolbar adds when it submits, and it is the difference between "the
// caller said nothing" and "the caller unticked every box" -- without it,
// clearing the last checkbox would silently turn every layer back on and the
// page would look like it had ignored the click.
//
// The subject's own layer is always included. It is the anchor: a picture built
// around an asset that is not in it is not a picture of that asset.
func selectedLayers(r *http.Request, subject diagram.Layer) map[diagram.Layer]bool {
	on := map[diagram.Layer]bool{}
	if r.URL.Query().Get("filter") != "1" {
		for _, l := range diagram.LayersTopDown() {
			on[l] = true
		}
		return on
	}
	for _, name := range queryStrings(r, "layer") {
		for _, l := range diagram.LayersTopDown() {
			if l.String() == name {
				on[l] = true
			}
		}
	}
	on[subject] = true
	return on
}

// ---------------------------------------------------------------------------
// Assembling the picture

// buildDiagramView turns the store's graph into nodes, edges, a layout and a
// table.
func buildDiagramView(g *store.NeighbourhoodGraph, subject *store.AssetRow,
	layers map[diagram.Layer]bool,
	assetHealth, instanceHealth map[string]store.EntityHealth) (diagramView, error) {

	view := diagramView{Hops: g.Hops, HopChoices: hopChoices()}

	nodes, edges, selfCabled := neighbourhoodElements(g, assetHealth, instanceHealth)

	// Counts are taken BEFORE filtering, so a toggle can say what turning it on
	// would give and an empty layer reads as empty rather than as absent.
	nodesPerLayer := map[diagram.Layer]int{}
	edgesPerLayer := map[diagram.Layer]int{}
	for _, n := range nodes {
		nodesPerLayer[n.node.Layer]++
	}
	for _, e := range edges {
		edgesPerLayer[e.edge.Layer]++
	}
	subjectLayer := assetLayer(subject.Kind)
	for _, l := range diagramLayers {
		view.Layers = append(view.Layers, layerChoice{
			Name:   l.Layer.String(),
			Label:  l.Label,
			Note:   l.Note,
			On:     layers[l.Layer],
			Locked: l.Layer == subjectLayer,
			Nodes:  nodesPerLayer[l.Layer],
			Edges:  edgesPerLayer[l.Layer],
		})
	}

	subjectNode := nodeID("a", subject.ID)
	kept, hiddenNodes := filterNodes(nodes, layers)
	present := make(map[string]bool, len(kept))
	for _, n := range kept {
		present[n.node.ID] = true
	}
	keptEdges, hiddenEdges := filterEdges(edges, present)
	kept, orphans := pruneOrphans(kept, keptEdges, subjectNode)

	view.Rows = adjacencyRows(keptEdges)
	view.Notes = neighbourhoodNotes(g, hiddenNodes, hiddenEdges, orphans, selfCabled)

	if len(keptEdges) == 0 {
		view.Empty = emptyReason(g, subject, hiddenNodes)
	}
	if len(kept) <= 1 && len(keptEdges) == 0 {
		// One box and no lines is not a diagram, and drawing it beside the
		// words "nothing connects to this" would contradict itself. The words
		// are the answer.
		return view, nil
	}

	in := diagram.Input{Subject: subjectNode}
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
	view.Scene = buildScene(layout, kept, keptEdges, subject)
	return view, nil
}

func hopChoices() []int {
	out := make([]int, 0, store.NeighbourhoodMaxHops)
	for i := neighbourhoodHopMin; i <= store.NeighbourhoodMaxHops; i++ {
		out = append(out, i)
	}
	return out
}

// viewNode is a laid-out node plus everything the renderer needs that the
// layout deliberately knows nothing about: colour, link target, tooltip.
type viewNode struct {
	node   diagram.Node
	href   string
	title  string
	state  string
	detail string
}

// viewEdge is the same for an edge.
type viewEdge struct {
	edge   diagram.Edge
	title  string
	from   string
	to     string
	fromAt string
	toAt   string
	detail string
	// nature is the dependency's failure semantics, and only a dependency has
	// one. It becomes a stroke pattern rather than a hue: the four hues in this
	// palette are already spent on status, and a down service drawn in
	// "optional" would be ambiguous in exactly the way a diagram must not be.
	nature string
}

// Edge kinds. They are separate because they are separate claims, and a
// layered diagram whose adjacency and whose descent share a stroke is three
// unrelated pictures with lines on top.
const (
	// edgeCable is a wire, or a veth: an adjacency between two ports.
	edgeCable = "cable"
	// edgeContains is containment. Not a transport reality at all, which is why
	// it is drawn faintly -- it is there to explain why a box is in the
	// picture, not to suggest traffic flows along it.
	edgeContains = "contains"
	// edgeDescent is a listening socket resolving down to the host or the port
	// that carries it. This is the line the whole feature exists to draw.
	edgeDescent = "descent"
	// edgeLocal is a placement whose sockets never leave the host: loopback and
	// unix. It asserts "this runs here" and makes NO reachability claim, which
	// is why it is not a descent -- the impact engine exempts exactly these
	// binds from network reasoning and the picture must not contradict it.
	edgeLocal = "local"
	// edgeDependency is a declared service-to-service edge.
	edgeDependency = "dependency"
)

func nodeID(prefix, id string) string { return prefix + ":" + id }

// neighbourhoodElements turns the store graph into diagram nodes and edges.
//
// Deterministic throughout: every loop walks a slice the store ordered, and
// nothing here ranges a map in a way that can reach the output. Two people on
// the same call opening the same URL have to see the same picture.
func neighbourhoodElements(g *store.NeighbourhoodGraph,
	assetHealth, instanceHealth map[string]store.EntityHealth) ([]viewNode, []viewEdge, int) {

	var nodes []viewNode
	var edges []viewEdge
	// Cables joining two ports of one asset. Counted so the notes can say the
	// picture is short of a connection the database holds.
	selfCabled := 0

	assetName := make(map[string]string, len(g.Assets))
	inSet := make(map[string]bool, len(g.Assets))
	for _, asset := range g.Assets {
		assetName[asset.ID] = asset.Name
		inSet[asset.ID] = true
	}

	for _, asset := range g.Assets {
		state, note := observedState(assetHealth[asset.ID])
		title := fmt.Sprintf("%s — %s, %s at %s",
			asset.Name, asset.Kind, healthPhrase(state, note), hopPhrase(asset.Hop))
		if asset.Lifecycle != domain.LifecycleActive {
			title += " · " + asset.Lifecycle
		}
		nodes = append(nodes, viewNode{
			node: diagram.Node{
				ID: nodeID("a", asset.ID), Label: asset.Name, Kind: asset.Kind,
				Layer: assetLayer(asset.Kind), Status: state,
			},
			href:   "/assets/" + asset.ID,
			title:  title,
			state:  state,
			detail: asset.Kind,
		})
	}

	// Containment, derived from parent ids rather than queried: an edge exists
	// exactly when both ends are already in the picture.
	for _, asset := range g.Assets {
		if asset.ParentID == "" || !inSet[asset.ParentID] {
			continue
		}
		edges = append(edges, viewEdge{
			edge: diagram.Edge{
				From: nodeID("a", asset.ID), To: nodeID("a", asset.ParentID),
				Kind: edgeContains, Layer: assetLayer(asset.Kind),
			},
			title:  fmt.Sprintf("%s is inside %s", asset.Name, assetName[asset.ParentID]),
			from:   asset.Name,
			to:     assetName[asset.ParentID],
			detail: "containment",
		})
	}

	for _, l := range g.Links {
		// A cable between two ports of the SAME asset is legal in the schema and
		// offered by the UI: link only forbids an interface patched to itself,
		// and ListAvailableInterfaces does not exclude same-asset ports. It is a
		// real thing to record -- a loopback plug, two NICs bridged externally,
		// a patch panel doubling back -- but as a diagram edge it is a self
		// edge, which diagram.Build rejects outright. That turned one such cable
		// into an HTTP 500 for the asset AND every asset within N hops of it.
		//
		// Skipped rather than drawn: a line from a box to itself carries no
		// information at this zoom, where a node is an asset rather than a port.
		// It is counted into the notes below so the picture does not silently
		// disagree with the database.
		if l.A.AssetID == l.B.AssetID {
			selfCabled++
			continue
		}
		layer := linkLayer(l.A, l.B)
		detail := cableDetail(l)
		edges = append(edges, viewEdge{
			edge: diagram.Edge{
				From: nodeID("a", l.A.AssetID), To: nodeID("a", l.B.AssetID),
				Kind: edgeCable, Layer: layer,
			},
			title: fmt.Sprintf("%s/%s ↔ %s/%s — %s",
				assetName[l.A.AssetID], l.A.Name, assetName[l.B.AssetID], l.B.Name, detail),
			from:   assetName[l.A.AssetID],
			to:     assetName[l.B.AssetID],
			fromAt: portLabel(l.A),
			toAt:   portLabel(l.B),
			detail: detail,
		})
	}

	// The service band, and the descent out of it.
	serviceName := make(map[string]string, len(g.Services))
	for _, svc := range g.Services {
		serviceName[svc.ID] = svc.Code
	}
	endpointsFor := make(map[string][]store.NeighbourEndpoint, len(g.Services))
	for _, ep := range g.Endpoints {
		endpointsFor[ep.ServiceID] = append(endpointsFor[ep.ServiceID], ep)
	}
	placementsFor := make(map[string][]store.NeighbourPlacement, len(g.Services))
	for _, p := range g.Placements {
		placementsFor[p.ServiceID] = append(placementsFor[p.ServiceID], p)
	}

	for _, svc := range g.Services {
		placements := placementsFor[svc.ID]
		state, note := placementState(placements, instanceHealth)
		title := fmt.Sprintf("%s (%s) — %s, %s, %s",
			svc.Code, svc.Name, svc.Kind, svc.Availability, healthPhrase(state, note))
		nodes = append(nodes, viewNode{
			node: diagram.Node{
				ID: nodeID("s", svc.ID), Label: svc.Code, Kind: svc.Kind,
				Layer: diagram.LayerService, Status: state,
			},
			href:   "/services/" + svc.ID,
			title:  title,
			state:  state,
			detail: svc.Kind,
		})

		for _, target := range descents(placements, endpointsFor[svc.ID], inSet) {
			edges = append(edges, viewEdge{
				edge: diagram.Edge{
					From: nodeID("s", svc.ID), To: nodeID("a", target.assetID),
					Kind: target.kind, Layer: diagram.LayerService,
				},
				title: fmt.Sprintf("%s on %s — %s",
					svc.Code, assetName[target.assetID], target.detail),
				from:   svc.Code,
				to:     assetName[target.assetID],
				toAt:   target.port,
				detail: target.detail,
			})
		}
	}

	for _, d := range g.Dependencies {
		if d.ConsumerServiceID == d.ProviderServiceID {
			// A service depending on its own endpoint is a legal row and not a
			// line: the layout has no way to draw a loop and there is nothing
			// to learn from one.
			continue
		}
		detail := fmt.Sprintf("%s · %s", d.Nature, providerLabel(d))
		edges = append(edges, viewEdge{
			edge: diagram.Edge{
				From: nodeID("s", d.ConsumerServiceID), To: nodeID("s", d.ProviderServiceID),
				Kind: edgeDependency, Layer: diagram.LayerService,
			},
			title: fmt.Sprintf("%s depends on %s — %s",
				serviceName[d.ConsumerServiceID], serviceName[d.ProviderServiceID], detail),
			from:   serviceName[d.ConsumerServiceID],
			to:     serviceName[d.ProviderServiceID],
			toAt:   providerLabel(d),
			detail: detail,
			nature: d.Nature,
		})
	}

	return nodes, edges, selfCabled
}

// descentTarget is one line out of a service node.
type descentTarget struct {
	assetID string
	port    string
	kind    string
	detail  string
}

// descents resolves a service down to the assets that carry it.
//
// Two tiers, and the difference is not cosmetic:
//
//   - A PINNED endpoint (ip_address_id set, resolving to a known interface)
//     descends to that exact port, and from there the physical band shows the
//     exact cable. That is the strongest claim the model can make.
//   - Everything else descends to the HOST. bind_scope 'host' with no address
//     is 0.0.0.0 -- listening on all of them -- so there is no single port to
//     point at, and picking one would be inventing a fact. The line lands on
//     the box and says "all addresses", which is what is true.
//
// A socket bound to loopback or a unix path descends to NOTHING over the
// network: that traffic is intra-host by definition, and the impact engine
// exempts exactly these binds from network reasoning. The placement is still
// drawn, as a local edge, because the service really does run there -- but it
// is a different claim with a different stroke, and it must never be read as a
// path a packet takes.
func descents(placements []store.NeighbourPlacement,
	endpoints []store.NeighbourEndpoint, inSet map[string]bool) []descentTarget {

	byAsset := map[string]*descentTarget{}
	var order []string
	add := func(assetID, port, kind, detail string) {
		if assetID == "" || !inSet[assetID] {
			return
		}
		existing, ok := byAsset[assetID]
		if !ok {
			byAsset[assetID] = &descentTarget{assetID: assetID, port: port, kind: kind, detail: detail}
			order = append(order, assetID)
			return
		}
		// One line per (service, host). Two lines between the same pair would
		// say "two paths" where the truth is one box with two sockets on it.
		if existing.kind == edgeLocal && kind == edgeDescent {
			existing.kind, existing.port, existing.detail = kind, port, detail
			return
		}
		if existing.kind == kind {
			existing.detail += ", " + detail
			if existing.port == "" {
				existing.port = port
			}
		}
	}

	for _, p := range placements {
		if len(endpoints) == 0 {
			// A service with no listening socket is a pure consumer -- a backup
			// agent, a batch job. It is not a gap and it must not be dropped:
			// it runs here, and losing this host loses it.
			add(p.HostAssetID, "", edgeLocal, "no listening socket")
			continue
		}
		for _, ep := range endpoints {
			switch {
			case domain.IsLocalBind(ep.BindScope):
				add(p.HostAssetID, "", edgeLocal, ep.Name+" "+ep.BindScope)
			case ep.BoundInterfaceID != "" && ep.BoundAssetID != "":
				add(ep.BoundAssetID, ep.BoundPort, edgeDescent,
					fmt.Sprintf("%s %s:%d on %s", ep.Name, ep.AddrText, ep.Port, ep.BoundPort))
			default:
				add(p.HostAssetID, "", edgeDescent,
					fmt.Sprintf("%s :%d · %s", ep.Name, ep.Port, bindPhrase(ep.BindScope)))
			}
		}
	}

	out := make([]descentTarget, 0, len(order))
	for _, id := range order {
		out = append(out, *byAsset[id])
	}
	return out
}

// bindPhrase says what a bind scope means in words, so that "all addresses" is
// on the screen rather than a blank cell. A blank reads as missing data; this
// is a fact about the service.
func bindPhrase(scope string) string {
	switch scope {
	case domain.BindHost:
		return "all addresses on the host"
	case domain.BindVIP:
		return "a virtual address"
	case domain.BindClusterIP:
		return "a cluster address"
	case domain.BindNodePort:
		return "every node in the cluster"
	case domain.BindIngress:
		return "an ingress"
	default:
		return scope
	}
}

func providerLabel(d store.NeighbourDependency) string {
	label := d.ProviderEndpoint
	if label == "" {
		label = d.Via
	}
	if d.ProviderPort > 0 {
		label += fmt.Sprintf(":%d", d.ProviderPort)
	}
	if d.Via == "route" {
		label += " (via a route)"
	}
	return label
}

func portLabel(p store.NeighbourPort) string {
	label := p.Name
	if p.Master != "" {
		// The master is what joins a cable to a bridge's uplink inside one
		// host. Without it, "eno2 goes to the switch" and "the bridge lands on
		// bond0" look like unrelated facts about the same box.
		label += " ▸ " + p.Master
	}
	return label
}

func cableDetail(l store.NeighbourLink) string {
	parts := []string{}
	if l.Medium != "" {
		parts = append(parts, l.Medium)
	}
	if !l.A.Enabled || !l.B.Enabled {
		// Shown, never filtered. "The wire is there and the port is down" is
		// the answer somebody came to this page for.
		parts = append(parts, "a port is disabled")
	}
	if l.A.IsMgmt || l.B.IsMgmt {
		parts = append(parts, "management")
	}
	if len(parts) == 0 {
		return "cabled"
	}
	return strings.Join(parts, " · ")
}

func hopPhrase(hop int) string {
	if hop == 0 {
		return "the centre"
	}
	return fmt.Sprintf("%d hop(s)", hop)
}

func healthPhrase(state, note string) string {
	if note == "" {
		return state
	}
	return state + " (" + note + ")"
}

// observedState reduces one entity's readings to the single thing a box in a
// picture can carry -- and refuses to when that would pick a winner.
//
// docs/AUDIT.md rule 2: two monitors that disagree stay two rows, and nothing
// computes a verdict from them. So a disagreement renders as `unknown` with the
// disagreement named, never as whichever reading happened to be worse. An
// unobserved entity is `unknown` too, and `unknown` is deliberately muted
// rather than alarming: "nobody is watching this" calls for looking at the
// collector, not at the box.
func observedState(h store.EntityHealth) (state, note string) {
	if h.Overridden() {
		return string(h.AssertedState()), "operator override"
	}
	if !h.Observed() {
		return string(domain.HealthUnknown), "not observed"
	}
	if h.Disagree() {
		return string(domain.HealthUnknown), "reporters disagree"
	}
	for _, r := range h.Readings {
		if !r.Stale {
			return string(r.Display), ""
		}
	}
	return string(domain.HealthUnknown), "stale"
}

// placementState folds a service's placements IN THIS NEIGHBOURHOOD, and only
// when they agree. A partly-down service is a question for the impact engine,
// which answers it with availability policy; guessing at it here would put a
// number on the screen that nothing computed.
func placementState(placements []store.NeighbourPlacement,
	health map[string]store.EntityHealth) (state, note string) {

	if len(placements) == 0 {
		return string(domain.HealthUnknown), "no placement here"
	}
	first, firstNote := observedState(health[placements[0].InstanceID])
	for _, p := range placements[1:] {
		other, _ := observedState(health[p.InstanceID])
		if other != first {
			return string(domain.HealthUnknown), "placements report differently"
		}
	}
	if len(placements) > 1 {
		count := fmt.Sprintf("%d placements here", len(placements))
		if firstNote != "" {
			return first, firstNote + ", " + count
		}
		return first, count
	}
	return first, firstNote
}

// ---------------------------------------------------------------------------
// Filtering, notes and the table

func filterNodes(nodes []viewNode, layers map[diagram.Layer]bool) (kept []viewNode, hidden int) {
	for _, n := range nodes {
		if layers[n.node.Layer] {
			kept = append(kept, n)
			continue
		}
		hidden++
	}
	return kept, hidden
}

// filterEdges drops anything with an endpoint that is no longer drawn. An edge
// to a node that is not in the picture has nowhere to land, and the layout
// rejects it rather than drawing half a line -- correctly, because half a line
// is a claim about somewhere the reader cannot see.
func filterEdges(edges []viewEdge, present map[string]bool) (kept []viewEdge, hidden int) {
	for _, e := range edges {
		if present[e.edge.From] && present[e.edge.To] {
			kept = append(kept, e)
			continue
		}
		hidden++
	}
	return kept, hidden
}

// pruneOrphans drops nodes nothing joins to anything.
//
// This is a diagram of CONNECTIVITY, and a box with no line on it says nothing
// about connectivity -- it is the walk's arithmetic showing through, usually
// because whatever joined it to the picture was in a hidden layer or past the
// node budget. It is dropped rather than drawn, and counted rather than
// dropped silently. The subject is never pruned: the picture is built around it
// and an operator who filtered their way to an empty page needs it to get back.
func pruneOrphans(nodes []viewNode, edges []viewEdge, subjectID string) (kept []viewNode, pruned int) {
	joined := make(map[string]bool, len(edges)*2)
	for _, e := range edges {
		joined[e.edge.From] = true
		joined[e.edge.To] = true
	}
	for _, n := range nodes {
		if joined[n.node.ID] || n.node.ID == subjectID {
			kept = append(kept, n)
			continue
		}
		pruned++
	}
	return kept, pruned
}

func neighbourhoodNotes(g *store.NeighbourhoodGraph, hiddenNodes, hiddenEdges, orphans, selfCabled int) []string {
	var notes []string
	if orphans > 0 {
		notes = append(notes, fmt.Sprintf(
			"%s in this neighbourhood connect to nothing that is drawn and are left out. "+
				"They are in the asset list; they are not in this picture.",
			pluraliseCount(orphans, "asset or service", "assets or services")))
	}
	if g.AssetsElided > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d further assets were reached and are not drawn — the picture is capped at %d. "+
				"Reduce the hop count to see a complete neighbourhood.",
			g.AssetsElided, store.NeighbourhoodDefaultMaxAssets))
	}
	if hiddenNodes > 0 || hiddenEdges > 0 {
		notes = append(notes, fmt.Sprintf(
			"The layer filter is hiding %s and %s.",
			pluraliseCount(hiddenNodes, "node", "nodes"),
			pluraliseCount(hiddenEdges, "connection", "connections")))
	}
	if selfCabled > 0 {
		notes = append(notes, fmt.Sprintf(
			"%s join two ports of the same asset and are not drawn — at this zoom a node is an "+
				"asset, so the line would start and end in the same box. Open the asset to see "+
				"its ports.",
			pluraliseCount(selfCabled, "cable", "cables")))
	}
	if g.DependenciesOutside > 0 {
		notes = append(notes, fmt.Sprintf(
			"%s reach a service outside this neighbourhood and are not drawn — there is nowhere "+
				"to draw them to. Widen the hops, or open the service.",
			pluraliseCount(g.DependenciesOutside, "declared dependency", "declared dependencies")))
	}
	return notes
}

func pluraliseCount(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// emptyReason says why there is nothing to draw, in words. A blank canvas and
// an asset with no recorded connectivity look identical and mean completely
// different things.
func emptyReason(g *store.NeighbourhoodGraph, subject *store.AssetRow, hiddenNodes int) string {
	switch {
	case hiddenNodes > 0:
		return "Every connection in this neighbourhood is in a layer you have turned off."
	case len(g.Links) == 0 && len(g.Services) == 0 && len(g.Assets) == 1:
		return "Nothing is cabled to " + subject.Name + ", nothing contains it, and nothing runs " +
			"on it. That is a finding rather than a blank page: an asset with no recorded " +
			"connectivity is an asset nobody has finished modelling."
	default:
		return "Nothing in this neighbourhood connects to anything else at this hop count. " +
			"Try widening it."
	}
}

// adjacencyRows is the picture as a table -- the same edges, in reading order,
// with every name in full rather than truncated to fit a box.
func adjacencyRows(edges []viewEdge) []adjacencyRow {
	rows := make([]adjacencyRow, 0, len(edges))
	for _, e := range edges {
		rows = append(rows, adjacencyRow{
			Layer:  e.edge.Layer.String(),
			Kind:   e.edge.Kind,
			From:   e.from,
			FromAt: e.fromAt,
			To:     e.to,
			ToAt:   e.toAt,
			Detail: e.detail,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Layer != rows[j].Layer {
			// Service, virtual, physical -- the order the bands are stacked in,
			// so the table reads the same way down the page as the picture does.
			return layerRank(rows[i].Layer) < layerRank(rows[j].Layer)
		}
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		if rows[i].From != rows[j].From {
			return rows[i].From < rows[j].From
		}
		return rows[i].To < rows[j].To
	})
	return rows
}

func layerRank(name string) int {
	for i, l := range diagramLayers {
		if l.Layer.String() == name {
			return i
		}
	}
	return len(diagramLayers)
}
