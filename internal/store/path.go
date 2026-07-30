package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/gabriel/invctl/internal/domain"
)

// PathQuery asks how THIS reaches THAT, drawn as the chain and nothing else.
//
// Where the neighbourhood answers "what does this touch", the path answers the
// other incident question: "is there a way from here to there, and what is on
// it". Each end is a service or an asset; a service resolves to the hosts of
// its active placements, because a service has no port of its own -- traffic
// leaves from wherever it actually runs.
//
// Three rules the picture must obey:
//
//   - DATA PLANE ONLY. A link with a management port on either end is never a
//     path: the OOB network reaches everything by design, and any shortest-path
//     search that can use it will, telling the operator that orders-api reaches
//     its database through the console switch. That is how every naive network
//     diagram lies.
//   - The UNION of shortest routes, per anchor host, both directions. A
//     dual-homed host has two ways into the core and drawing one of them would
//     assert less redundancy than exists. Per anchor, because a service with
//     two placements sends traffic from both hosts -- the picture must not
//     quietly keep whichever instance happens to sit closer.
//   - An anchor with no route at all is still IN the picture. A placement that
//     cannot reach the other side is the finding, and a diagram that silently
//     dropped it would show a healthy path while an instance is stranded.
type PathQuery struct {
	// Exactly one of FromServiceID / FromAssetID.
	FromServiceID string
	FromAssetID   string
	// At most one of ToServiceID / ToAssetID. Both empty asks "where does the
	// FROM side sit": the target becomes the member chassis of the data-plane
	// groups its hosts are attached to, which draws the descent to the network
	// rather than a route across it.
	ToServiceID string
	ToAssetID   string
}

// PathEnd is one resolved side.
type PathEnd struct {
	// ServiceID is set when this end is a service.
	ServiceID string
	// AssetIDs are the anchors: the asset itself, a service's placement
	// hosts, or -- for an empty To -- the attached groups' member chassis.
	// Ordered, and possibly empty: a service with no active placements has no
	// anchor, and that fact is the answer.
	AssetIDs []string
}

// PathGraph is everything the picture is drawn from. The component types are
// shared with the neighbourhood on purpose: the handler assembles them into
// the same graph shape and the whole rendering pipeline is reused unchanged.
type PathGraph struct {
	From, To PathEnd

	// Found reports whether at least one anchor pair is connected. When false,
	// Assets still carries the anchors so the page can show the stranded boxes
	// beside the words.
	Found bool
	// Hops is the shortest connected route's length in asset steps; 0 means
	// the two sides share a host.
	Hops int
	// Unrouted names anchor assets with no data-plane route to the far side --
	// the per-instance finding, distinct from Found which is about any-pair.
	Unrouted []string

	Assets       []NeighbourAsset
	Links        []NeighbourLink
	Services     []NeighbourService
	Placements   []NeighbourPlacement
	Endpoints    []NeighbourEndpoint
	Dependencies []NeighbourDependency
}

// Path resolves both ends and walks the data-plane cabling between them.
func (s *SQLStore) Path(ctx context.Context, q PathQuery) (*PathGraph, error) {
	if (q.FromServiceID == "") == (q.FromAssetID == "") {
		return nil, fmt.Errorf("path: exactly one FROM end: %w", domain.ErrInvalid)
	}
	if q.ToServiceID != "" && q.ToAssetID != "" {
		return nil, fmt.Errorf("path: at most one TO end: %w", domain.ErrInvalid)
	}

	g := &PathGraph{}
	var err error
	if g.From, err = s.resolvePathEnd(ctx, q.FromServiceID, q.FromAssetID); err != nil {
		return nil, err
	}
	networkMode := q.ToServiceID == "" && q.ToAssetID == ""
	if networkMode {
		if g.To, err = s.resolveAttachedChassis(ctx, g.From.AssetIDs); err != nil {
			return nil, err
		}
	} else if g.To, err = s.resolvePathEnd(ctx, q.ToServiceID, q.ToAssetID); err != nil {
		return nil, err
	}

	adj, err := s.dataPlaneAdjacency(ctx)
	if err != nil {
		return nil, err
	}

	marked, edges := shortestUnion(adj, g.From.AssetIDs, g.To.AssetIDs)
	g.Found = len(marked) > 0
	if g.Found {
		g.Hops = pathHops(adj, g.From.AssetIDs, g.To.AssetIDs)
	}

	// Anchors are always drawn. In network mode the To side is only the
	// members actually routed to -- the group's other chassis are not the
	// question -- but a FROM anchor is somebody's placement or the asset the
	// operator named, and it stays visible routed or not.
	drawn := map[string]bool{}
	for _, id := range marked {
		drawn[id] = true
	}
	distB := bfs(adj, g.To.AssetIDs)
	for _, id := range g.From.AssetIDs {
		if _, routed := distB[id]; !routed {
			g.Unrouted = append(g.Unrouted, id)
		}
		drawn[id] = true
	}
	if !networkMode {
		distA := bfs(adj, g.From.AssetIDs)
		for _, id := range g.To.AssetIDs {
			if _, routed := distA[id]; !routed {
				g.Unrouted = append(g.Unrouted, id)
			}
			drawn[id] = true
		}
	}
	sort.Strings(g.Unrouted)

	ids := make([]string, 0, len(drawn))
	for id := range drawn {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return g, nil
	}

	if g.Assets, err = s.pathAssets(ctx, ids, bfs(adj, g.From.AssetIDs)); err != nil {
		return nil, err
	}
	// Resolve Unrouted ids to names now that the rows are loaded.
	name := make(map[string]string, len(g.Assets))
	for _, a := range g.Assets {
		name[a.ID] = a.Name
	}
	for i, id := range g.Unrouted {
		if n, ok := name[id]; ok {
			g.Unrouted[i] = n
		}
	}
	sort.Strings(g.Unrouted)

	links, err := s.neighbourhoodLinks(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		lo, hi := orderPair(l.A.AssetID, l.B.AssetID)
		if edges[[2]string{lo, hi}] {
			g.Links = append(g.Links, l)
		}
	}

	if err := s.pathWorkloads(ctx, g, ids); err != nil {
		return nil, err
	}
	return g, nil
}

// resolvePathEnd turns one named end into its anchor assets.
func (s *SQLStore) resolvePathEnd(ctx context.Context, serviceID, assetID string) (PathEnd, error) {
	if assetID != "" {
		n, err := s.countOne(ctx, `SELECT COUNT(*) FROM asset WHERE id = ?`, assetID)
		if err != nil {
			return PathEnd{}, fmt.Errorf("resolving path asset: %w", err)
		}
		if n == 0 {
			return PathEnd{}, fmt.Errorf("path asset %s: %w", assetID, domain.ErrNotFound)
		}
		return PathEnd{AssetIDs: []string{assetID}}, nil
	}

	n, err := s.countOne(ctx, `SELECT COUNT(*) FROM service WHERE id = ?`, serviceID)
	if err != nil {
		return PathEnd{}, fmt.Errorf("resolving path service: %w", err)
	}
	if n == 0 {
		return PathEnd{}, fmt.Errorf("path service %s: %w", serviceID, domain.ErrNotFound)
	}
	end := PathEnd{ServiceID: serviceID}
	// Retired hosts are excluded: a placement whose host is gone is not a
	// place traffic leaves from. The placement itself must be live too.
	err = s.read(ctx, &end.AssetIDs, `
		SELECT DISTINCT si.host_asset_id
		FROM service_instance si
		JOIN asset a ON a.id = si.host_asset_id
		WHERE si.service_id = ? AND si.lifecycle = ? AND a.lifecycle <> ?
		ORDER BY si.host_asset_id`,
		serviceID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return PathEnd{}, fmt.Errorf("resolving placements of %s: %w", serviceID, err)
	}
	return end, nil
}

// resolveAttachedChassis is the empty-To target: the member chassis of every
// data-plane group the FROM anchors are attached to, their own containment
// ancestry included -- a VM inherits its hypervisor's attachment exactly the
// way the impact engine says it does.
func (s *SQLStore) resolveAttachedChassis(ctx context.Context, fromIDs []string) (PathEnd, error) {
	if len(fromIDs) == 0 {
		return PathEnd{}, nil
	}
	var end PathEnd
	err := s.read(ctx, &end.AssetIDs, `
		SELECT DISTINCT m.asset_id
		FROM asset_closure c
		JOIN net_attachment na ON na.asset_id = c.ancestor_id
		JOIN net_group_member m ON m.group_id = na.group_id
		WHERE c.descendant_id IN (`+placeholders(len(fromIDs))+`)
		  AND na.lifecycle = ? AND na.plane = ?
		  AND m.lifecycle = ?
		ORDER BY m.asset_id`,
		append(anySlice(fromIDs),
			domain.LifecycleActive, domain.PlaneData, domain.LifecycleActive)...)
	if err != nil {
		return PathEnd{}, fmt.Errorf("resolving attached chassis: %w", err)
	}
	return end, nil
}

// dataPlaneAdjacency loads the whole estate's cabling as an asset-level
// adjacency, minus anything touching a management port and minus self-loops.
//
// The whole estate rather than a bounded walk, deliberately: a shortest path
// needs global distances, and the link table is the smallest table that grows
// with an estate -- one row per cable. The neighbourhood's budget arguments do
// not apply to a query whose output is one pair of ids per row.
func (s *SQLStore) dataPlaneAdjacency(ctx context.Context) (map[string][]string, error) {
	type row struct {
		A     string `db:"a_asset"`
		B     string `db:"b_asset"`
		AMgmt bool   `db:"a_mgmt"`
		BMgmt bool   `db:"b_mgmt"`
	}
	var rows []row
	err := s.read(ctx, &rows, `
		SELECT ai.asset_id AS a_asset, bi.asset_id AS b_asset,
		       ai.is_mgmt AS a_mgmt, bi.is_mgmt AS b_mgmt
		FROM link l
		JOIN interface ai ON ai.id = l.a_interface_id
		JOIN interface bi ON bi.id = l.b_interface_id
		WHERE l.lifecycle = ?
		ORDER BY l.id`, domain.LifecycleActive)
	if err != nil {
		return nil, fmt.Errorf("loading the cable plant: %w", err)
	}

	adj := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, r := range rows {
		if r.AMgmt || r.BMgmt || r.A == r.B {
			continue
		}
		lo, hi := orderPair(r.A, r.B)
		if seen[[2]string{lo, hi}] {
			continue // Parallel cables are one adjacency; drawing keeps both.
		}
		seen[[2]string{lo, hi}] = true
		adj[r.A] = append(adj[r.A], r.B)
		adj[r.B] = append(adj[r.B], r.A)
	}
	// Sorted neighbours make the BFS -- and therefore the picture -- a pure
	// function of the data.
	for _, ns := range adj {
		sort.Strings(ns)
	}
	return adj, nil
}

// bfs is a multi-source breadth-first search; absent key = unreachable.
func bfs(adj map[string][]string, seeds []string) map[string]int {
	dist := make(map[string]int, len(adj))
	queue := make([]string, 0, len(seeds))
	sorted := append([]string(nil), seeds...)
	sort.Strings(sorted)
	for _, s := range sorted {
		if _, ok := dist[s]; !ok {
			dist[s] = 0
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range adj[u] {
			if _, ok := dist[v]; !ok {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
	}
	return dist
}

// shortestUnion marks every asset and every asset-pair edge lying on a
// shortest route from SOME individual anchor to the far side, in either
// direction. Per anchor rather than per side: the minimum over a side would
// keep only its closest instance, and the others' routes are equally real.
func shortestUnion(adj map[string][]string, fromIDs, toIDs []string) ([]string, map[[2]string]bool) {
	nodes := map[string]bool{}
	edges := map[[2]string]bool{}
	if len(fromIDs) == 0 || len(toIDs) == 0 {
		return nil, edges
	}

	distFrom := bfs(adj, fromIDs)
	distTo := bfs(adj, toIDs)
	mark := func(anchor string, far map[string]int) {
		dh := bfs(adj, []string{anchor})
		d, routed := 0, false
		for u, fu := range far {
			if hu, ok := dh[u]; ok && (!routed || hu+fu < d) {
				// far holds distance-to-far-side per node; anchor's route
				// length is the best through-node sum.
				d, routed = hu+fu, true
			}
		}
		if !routed {
			return
		}
		for u, hu := range dh {
			fu, ok := far[u]
			if !ok || hu+fu != d {
				continue
			}
			nodes[u] = true
			for _, v := range adj[u] {
				if fv, ok := far[v]; ok && hu+1+fv == d {
					lo, hi := orderPair(u, v)
					edges[[2]string{lo, hi}] = true
					nodes[v] = true
				}
			}
		}
	}
	for _, h := range fromIDs {
		mark(h, distTo)
	}
	for _, h := range toIDs {
		mark(h, distFrom)
	}

	out := make([]string, 0, len(nodes))
	for id := range nodes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, edges
}

// pathHops is the single shortest connected route's length.
func pathHops(adj map[string][]string, fromIDs, toIDs []string) int {
	dist := bfs(adj, fromIDs)
	best, found := 0, false
	for _, t := range toIDs {
		if d, ok := dist[t]; ok && (!found || d < best) {
			best, found = d, true
		}
	}
	return best
}

func orderPair(a, b string) (string, string) {
	if a > b {
		return b, a
	}
	return a, b
}

// pathAssets loads the drawn assets, Hop carrying the distance from the FROM
// side so the table reads in walking order.
func (s *SQLStore) pathAssets(ctx context.Context, ids []string, distFrom map[string]int) ([]NeighbourAsset, error) {
	var rows []NeighbourAsset
	err := s.read(ctx, &rows, `
		SELECT a.id, 0 AS hop, a.kind, a.name, a.lifecycle,
		       COALESCE(a.parent_id, '') AS parent_id
		FROM asset a
		WHERE a.id IN (`+placeholders(len(ids))+`)
		ORDER BY a.name, a.id`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading path assets: %w", err)
	}
	for i := range rows {
		rows[i].Hop = distFrom[rows[i].ID]
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Hop != rows[j].Hop {
			return rows[i].Hop < rows[j].Hop
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// pathWorkloads decorates the chain with the two services being asked about --
// and ONLY those two. Every other service on a transit host is somebody else's
// question; the neighbourhood is the page that answers it.
func (s *SQLStore) pathWorkloads(ctx context.Context, g *PathGraph, assetIDs []string) error {
	wanted := map[string]bool{}
	if g.From.ServiceID != "" {
		wanted[g.From.ServiceID] = true
	}
	if g.To.ServiceID != "" {
		wanted[g.To.ServiceID] = true
	}
	if len(wanted) == 0 {
		return nil
	}

	full := &NeighbourhoodGraph{}
	if err := s.neighbourhoodWorkloads(ctx, assetIDs, full); err != nil {
		return err
	}
	for _, svc := range full.Services {
		if wanted[svc.ID] {
			g.Services = append(g.Services, svc)
		}
	}
	for _, p := range full.Placements {
		if wanted[p.ServiceID] {
			g.Placements = append(g.Placements, p)
		}
	}
	for _, e := range full.Endpoints {
		if wanted[e.ServiceID] {
			g.Endpoints = append(g.Endpoints, e)
		}
	}
	for _, d := range full.Dependencies {
		if wanted[d.ConsumerServiceID] && wanted[d.ProviderServiceID] {
			g.Dependencies = append(g.Dependencies, d)
		}
	}
	return nil
}
