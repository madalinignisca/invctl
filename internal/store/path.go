// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

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
//   - A DISABLED port carries no route. `enabled` is declared intent -- an
//     operator saying "this port is administratively down" -- and traffic does
//     not cross it, so a path through one would be a false claim. The
//     neighbourhood deliberately DRAWS a cabled-but-disabled port ("the wire is
//     there and the port is down" is the 03:00 answer); asserting a route
//     through it is a different claim, and this file makes the stricter one.
//     When the only route runs through a disabled port, that is not "no path"
//     -- it is a specific, actionable finding, and Blocked names the ports.
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
	// Empty when there is no far side at all: "unrouted" would be a claim
	// about a comparison nobody made. The handler's note is guarded on Found
	// for the same reason.
	Unrouted []string
	// AssetsElided is how many drawn assets the node budget cut. Same contract
	// as the neighbourhood's: a cut nobody is told about is indistinguishable
	// from a fact that does not exist, so the page must report it.
	AssetsElided int
	// Blocked names the administratively-disabled ports that are the ONLY
	// thing standing between the two sides: set when no live route exists but
	// one would if those ports were enabled. "No path" and "no path because
	// somebody shut this port" send an operator to two different places, so
	// the page must not flatten them.
	Blocked []string

	Assets       []NeighbourAsset
	Links        []NeighbourLink
	Services     []NeighbourService
	Placements   []NeighbourPlacement
	Endpoints    []NeighbourEndpoint
	Dependencies []NeighbourDependency
}

// PathDefaultMaxAssets bounds the drawn set.
//
// This did not exist until a review measured what happens without it: a
// service with 2,500 placements on a 5,000-asset estate produced a 46-second
// request and a 7.4MB page. The SQL was never the problem (a few ms); the cost
// is one breadth-first search per anchor plus a layout quadratic in edges.
//
// The comment this replaces argued the neighbourhood's budget rationale --
// "every asset id becomes a placeholder in four later statements" -- did not
// apply here. It was wrong: the drawn set feeds exactly those four statements.
// Same number as the neighbourhood, for the same reason, and cut ends first so
// the two anchors always survive.
const PathDefaultMaxAssets = 60

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
		if g.To, err = s.resolveAttachedChassis(ctx, q.FromServiceID, q.FromAssetID); err != nil {
			return nil, err
		}
		// An anchor cannot be its own destination. A forwarder that both
		// attaches to a group and is a member of it -- expressible, if not
		// currently in any fixture -- would otherwise appear on both sides and
		// produce a zero-hop "path" from a box to itself, which is a picture of
		// nothing. Dropping it leaves the OTHER chassis, which is the real
		// answer; if it leaves none, the handler's "its network is not
		// modelled" wording covers it, because a box that is its own network
		// has nothing to reach either.
		g.To.AssetIDs = withoutAnchors(g.To.AssetIDs, g.From.AssetIDs)
	} else if g.To, err = s.resolvePathEnd(ctx, q.ToServiceID, q.ToAssetID); err != nil {
		return nil, err
	}

	adj, patched, err := s.dataPlaneAdjacency(ctx)
	if err != nil {
		return nil, err
	}

	marked, edges := shortestUnion(adj, g.From.AssetIDs, g.To.AssetIDs)
	g.Found = len(marked) > 0
	if g.Found {
		g.Hops = pathHops(adj, g.From.AssetIDs, g.To.AssetIDs)
	} else if blocked, err := s.blockingPorts(ctx, patched, g); err != nil {
		return nil, err
	} else {
		// Only on the failure path, so the ordinary request pays nothing for
		// it: if a route exists once disabled ports are allowed, the disabled
		// ports ARE the answer.
		g.Blocked = blocked
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
		// Only when there IS a far side. With an unattached FROM end the To
		// side is empty, every anchor is trivially "unrouted", and saying so
		// would be a claim about a comparison that was never made -- the
		// handler says "its network is not modelled yet" instead.
		if len(g.To.AssetIDs) > 0 {
			if _, routed := distB[id]; !routed {
				g.Unrouted = append(g.Unrouted, id)
			}
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
	// The budget. Anchors are kept whatever it says -- they are the question --
	// and the cut falls on transit, furthest first, so a truncated picture is
	// still a picture of the two ends.
	if len(ids) > PathDefaultMaxAssets {
		anchor := make(map[string]bool, len(g.From.AssetIDs)+len(g.To.AssetIDs))
		for _, id := range append(append([]string{}, g.From.AssetIDs...), g.To.AssetIDs...) {
			anchor[id] = true
		}
		dist := bfs(adj, g.From.AssetIDs)
		sort.SliceStable(ids, func(i, j int) bool {
			ai, aj := anchor[ids[i]], anchor[ids[j]]
			if ai != aj {
				return ai
			}
			di, okI := dist[ids[i]]
			dj, okJ := dist[ids[j]]
			if okI != okJ {
				return okI
			}
			if di != dj {
				return di < dj
			}
			return ids[i] < ids[j]
		})
		g.AssetsElided = len(ids) - PathDefaultMaxAssets
		ids = ids[:PathDefaultMaxAssets]
		sort.Strings(ids)
		kept := make(map[string]bool, len(ids))
		for _, id := range ids {
			kept[id] = true
		}
		filtered := g.Unrouted[:0]
		for _, id := range g.Unrouted {
			if kept[id] {
				filtered = append(filtered, id)
			}
		}
		g.Unrouted = filtered
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
		return nil, fmt.Errorf("decorating the path with its services: %w", err)
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
//
// The anchors arrive as a SUB-SELECT rather than as a spliced list of ids, and
// that is a measured decision, not a style one. Splicing them flipped SQLite's
// plan somewhere between 500 and 1000 terms -- from driving the closure index
// to scanning every attachment for every member for every term -- and the same
// query went from 31ms to 21 SECONDS on a 5,000-asset estate. PostgreSQL held
// at 17ms throughout, so a dual-engine suite on a small fixture cannot see it.
// As a sub-select: 1ms on SQLite, and no placeholder count that grows with the
// estate.
func (s *SQLStore) resolveAttachedChassis(ctx context.Context, serviceID, assetID string) (PathEnd, error) {
	var end PathEnd
	var err error
	if assetID != "" {
		err = s.read(ctx, &end.AssetIDs, `
			SELECT DISTINCT m.asset_id
			FROM asset_closure c
			JOIN net_attachment na ON na.asset_id = c.ancestor_id
			JOIN net_group_member m ON m.group_id = na.group_id
			WHERE c.descendant_id = ?
			  AND na.lifecycle = ? AND na.plane = ?
			  AND m.lifecycle = ?
			ORDER BY m.asset_id`,
			assetID, domain.LifecycleActive, domain.PlaneData, domain.LifecycleActive)
	} else {
		err = s.read(ctx, &end.AssetIDs, `
			SELECT DISTINCT m.asset_id
			FROM asset_closure c
			JOIN net_attachment na ON na.asset_id = c.ancestor_id
			JOIN net_group_member m ON m.group_id = na.group_id
			WHERE c.descendant_id IN (
			        SELECT si.host_asset_id FROM service_instance si
			        JOIN asset a ON a.id = si.host_asset_id
			        WHERE si.service_id = ? AND si.lifecycle = ? AND a.lifecycle <> ?)
			  AND na.lifecycle = ? AND na.plane = ?
			  AND m.lifecycle = ?
			ORDER BY m.asset_id`,
			serviceID, domain.LifecycleActive, domain.LifecycleRetired,
			domain.LifecycleActive, domain.PlaneData, domain.LifecycleActive)
	}
	if err != nil {
		return PathEnd{}, fmt.Errorf("resolving attached chassis: %w", err)
	}
	return end, nil
}

// dataPlaneAdjacency loads the whole estate's cabling as an asset-level
// adjacency, minus anything touching a management port and minus self-loops.
//
// The whole estate rather than a bounded walk, deliberately: a shortest path
// needs global distances. The read is linear -- one row per cable, plus the
// interface table hashed twice for the two joins, so O(links + 2*interfaces),
// measured at 13ms for 15,000 links on PostgreSQL. It is the DRAWN set that
// needs a budget, and PathDefaultMaxAssets is it.
//
// The management and self-loop filters are in the WHERE clause rather than in
// Go because there is no reason to carry rows across the wire to drop them;
// there is no ORDER BY because nothing downstream depends on link order (the
// adjacency lists are sorted below, and collapsing parallel cables to one
// adjacency is order-insensitive).
func (s *SQLStore) dataPlaneAdjacency(ctx context.Context) (live, patched map[string][]string, err error) {
	type row struct {
		A       string `db:"a_asset"`
		B       string `db:"b_asset"`
		Enabled bool   `db:"both_enabled"`
	}
	var rows []row
	err = s.read(ctx, &rows, `
		SELECT ai.asset_id AS a_asset, bi.asset_id AS b_asset,
		       CASE WHEN ai.enabled AND bi.enabled THEN TRUE ELSE FALSE END AS both_enabled
		FROM link l
		JOIN interface ai ON ai.id = l.a_interface_id
		JOIN interface bi ON bi.id = l.b_interface_id
		WHERE l.lifecycle = ?
		  AND ai.is_mgmt = FALSE AND bi.is_mgmt = FALSE
		  AND ai.asset_id <> bi.asset_id`, domain.LifecycleActive)
	if err != nil {
		return nil, nil, fmt.Errorf("loading the cable plant: %w", err)
	}

	live, patched = map[string][]string{}, map[string][]string{}
	seenLive := map[[2]string]bool{}
	seenPatched := map[[2]string]bool{}
	add := func(adj map[string][]string, seen map[[2]string]bool, a, b string) {
		lo, hi := orderPair(a, b)
		if seen[[2]string{lo, hi}] {
			return // Parallel cables are one adjacency; drawing keeps both.
		}
		seen[[2]string{lo, hi}] = true
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}
	for _, r := range rows {
		// patched is every cable; live is only those whose both ends are
		// administratively up. A parallel pair where one cable is disabled and
		// the other is not still yields a live adjacency, which is correct --
		// that is what the second cable is for.
		add(patched, seenPatched, r.A, r.B)
		if r.Enabled {
			add(live, seenLive, r.A, r.B)
		}
	}
	// Sorted neighbours make the BFS -- and therefore the picture -- a pure
	// function of the data.
	for _, ns := range live {
		sort.Strings(ns)
	}
	for _, ns := range patched {
		sort.Strings(ns)
	}
	return live, patched, nil
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
// and ONLY those two in the OUTPUT. The query underneath still loads every
// placement, endpoint and dependency on every drawn host and the filtering
// happens here, which is the opposite of what the previous version of this
// comment implied. It is reuse rather than a leak: the same loader serves the
// neighbourhood, and narrowing it by service id would be a second query shape
// to keep portable for a saving the node budget already bounds.
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

// blockingPorts answers "would there be a route if nobody had shut a port?"
//
// Called only when the live adjacency found nothing. It re-runs the same
// marking over the patched adjacency -- every cable, disabled or not -- and if
// that succeeds, names the disabled ports lying on the route it found. Those
// ports are the difference between the two answers, which makes them the
// finding rather than a footnote.
//
// Returns nil when the two sides are genuinely uncabled, which is the ordinary
// "no path" and needs no further explanation.
func (s *SQLStore) blockingPorts(ctx context.Context, patched map[string][]string, g *PathGraph) ([]string, error) {
	if len(g.From.AssetIDs) == 0 || len(g.To.AssetIDs) == 0 {
		return nil, nil
	}
	marked, edges := shortestUnion(patched, g.From.AssetIDs, g.To.AssetIDs)
	if len(marked) == 0 {
		return nil, nil // No cabling either way: nothing to blame on a port.
	}
	if len(marked) > PathDefaultMaxAssets {
		marked = marked[:PathDefaultMaxAssets]
	}
	links, err := s.neighbourhoodLinks(ctx, marked)
	if err != nil {
		return nil, fmt.Errorf("finding the ports that block the path: %w", err)
	}
	// Asset names, because "eno2 (disabled)" without its box is useless at
	// 03:00. One small query, and only on this path.
	rows, err := s.pathAssets(ctx, marked, nil)
	if err != nil {
		return nil, fmt.Errorf("naming the assets that block the path: %w", err)
	}
	name := make(map[string]string, len(rows))
	for _, a := range rows {
		name[a.ID] = a.Name
	}

	// A pair is blocked only if EVERY cable joining it is down -- one live
	// cable of a parallel pair means that hop was never the problem.
	type tally struct{ total, down int }
	byPair := map[[2]string]*tally{}
	label := map[[2]string][]string{}
	for _, l := range links {
		lo, hi := orderPair(l.A.AssetID, l.B.AssetID)
		key := [2]string{lo, hi}
		if !edges[key] {
			continue
		}
		t := byPair[key]
		if t == nil {
			t = &tally{}
			byPair[key] = t
		}
		t.total++
		if !l.A.Enabled || !l.B.Enabled {
			t.down++
			label[key] = append(label[key],
				portLabel(name[l.A.AssetID], l.A)+" ↔ "+portLabel(name[l.B.AssetID], l.B))
		}
	}

	var out []string
	for key, t := range byPair {
		if t.down > 0 && t.down == t.total {
			out = append(out, label[key]...)
		}
	}
	sort.Strings(out)
	return out, nil
}

// portLabel names one end of a cable, marking the end that is down -- the
// operator needs the box AND the port, and needs to know which end to go and
// look at.
func portLabel(asset string, p NeighbourPort) string {
	if asset == "" {
		asset = "?"
	}
	if !p.Enabled {
		return asset + "/" + p.Name + " (disabled)"
	}
	return asset + "/" + p.Name
}

// withoutAnchors removes ids that are already on the other side of the
// question. Order is preserved: these lists seed a layout.
func withoutAnchors(ids, anchors []string) []string {
	if len(ids) == 0 || len(anchors) == 0 {
		return ids
	}
	drop := make(map[string]bool, len(anchors))
	for _, a := range anchors {
		drop[a] = true
	}
	out := ids[:0]
	for _, id := range ids {
		if !drop[id] {
			out = append(out, id)
		}
	}
	return out
}
