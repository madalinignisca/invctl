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

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
)

// LoadGraph reads the whole dependency picture in a fixed number of queries.
//
// Loading everything looks wasteful next to a targeted traversal, but the
// fixed-point iteration touches every edge on every round, so a per-round
// query would be strictly worse. The estate this targets is thousands of rows.
func (s *SQLStore) LoadGraph(ctx context.Context) (*impact.Graph, error) {
	g := impact.NewGraph()

	// Retired services are excluded: they are kept for audit, not for
	// reasoning about today's outage.
	var services []domain.Service
	err := s.read(ctx, &services, `SELECT * FROM service WHERE lifecycle <> ?`, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("loading services for graph: %w", err)
	}
	for i := range services {
		g.Services[services[i].ID] = &services[i]
	}

	// Withdrawn placements are excluded, not loaded-and-ignored. A retired row
	// is not capacity the service ever had for this question, so counting it in
	// TotalInstances would make "2 of 3 lost" out of a service that has two.
	// Distinct from desired_state='disabled', which stays in the graph: a
	// disabled placement still exists and is expected back, so it is loaded and
	// marked Disabled, which is what stops it counting as lost capacity.
	var instances []domain.ServiceInstance
	if err := s.read(ctx, &instances,
		`SELECT * FROM service_instance WHERE lifecycle <> ? ORDER BY id`,
		domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("loading instances for graph: %w", err)
	}
	for _, si := range instances {
		if _, ok := g.Services[si.ServiceID]; !ok {
			continue
		}
		g.Instances[si.ServiceID] = append(g.Instances[si.ServiceID], impact.Instance{
			ID:          si.ID,
			ServiceID:   si.ServiceID,
			HostAssetID: si.HostAssetID,
			Role:        si.RoleOrEmpty(),
			Shard:       si.ShardOrEmpty(),
			Disabled:    si.DesiredState == "disabled",
		})
	}

	// Live sockets only, per the read audit in migration 00020. A withdrawn
	// socket carries no traffic, and leaving it here makes the impact engine
	// simulate an outage propagating through something that does not exist.
	var endpoints []domain.Endpoint
	if err := s.read(ctx, &endpoints,
		`SELECT * FROM endpoint WHERE lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading endpoints for graph: %w", err)
	}
	for _, e := range endpoints {
		envID := ""
		if svc, ok := g.Services[e.ServiceID]; ok {
			envID = svc.EnvironmentID
		}
		g.Endpoints[e.ID] = impact.Endpoint{
			ID: e.ID, ServiceID: e.ServiceID, Name: e.Name,
			BindScope: e.BindScope, Exposure: e.Exposure, ServiceEnvID: envID,
		}
	}

	var routes []domain.Route
	if err := s.read(ctx, &routes, `SELECT * FROM route`); err != nil {
		return nil, fmt.Errorf("loading routes for graph: %w", err)
	}
	for _, r := range routes {
		name := r.MatchType
		if r.MatchValue != nil && *r.MatchValue != "" {
			name = *r.MatchValue
		}
		g.Routes[r.ID] = impact.Route{
			ID: r.ID, FrontendEndpointID: r.FrontendEndpointID,
			BackendPoolID: r.BackendPoolID, Name: name,
		}
	}

	var members []domain.BackendMember
	if err := s.read(ctx, &members, `SELECT * FROM backend_member ORDER BY pool_id, endpoint_id`); err != nil {
		return nil, fmt.Errorf("loading backend members for graph: %w", err)
	}
	for _, m := range members {
		g.PoolMembers[m.PoolID] = append(g.PoolMembers[m.PoolID], m.EndpointID)
	}

	var deps []domain.Dependency
	// ORDER BY id, and it is load-bearing rather than tidiness. phasePropagate
	// records via/Cause/Reason from whichever edge last *changed* a consumer's
	// status, so when two providers would independently produce the same
	// terminal status -- one genuinely dead, one merely partitioned -- the row
	// order decides which explanation the operator is shown. Unordered, that is
	// engine-defined, and SQLite and Postgres are free to disagree about the
	// same data. UUIDv7 ids sort by creation time, so this is stable and cheap.
	err = s.read(ctx, &deps, `SELECT * FROM dependency WHERE lifecycle = ? ORDER BY id`, domain.LifecycleActive)
	if err != nil {
		return nil, fmt.Errorf("loading dependencies for graph: %w", err)
	}
	g.Deps = deps

	// rt_k8s resolves a k8s_workload instance to its cluster, for scenario 9's
	// cluster_ip resolution (docs/reachability-design.md). One more query, as
	// the design calls for.
	var k8sRuntimes []struct {
		InstanceID     string  `db:"instance_id"`
		ClusterAssetID *string `db:"cluster_asset_id"`
	}
	if err := s.read(ctx, &k8sRuntimes, `
		SELECT si.id AS instance_id, rt.cluster_asset_id
		FROM service_instance si
		JOIN rt_k8s rt ON rt.instance_id = si.id
		WHERE si.runtime_type = ?`, domain.RuntimeK8sWorkload); err != nil {
		return nil, fmt.Errorf("loading k8s runtime detail for graph: %w", err)
	}
	instanceService := make(map[string]string, len(instances))
	for _, si := range instances {
		instanceService[si.ID] = si.ServiceID
	}
	for _, rt := range k8sRuntimes {
		if rt.ClusterAssetID == nil || *rt.ClusterAssetID == "" {
			continue
		}
		if svcID, ok := instanceService[rt.InstanceID]; ok {
			g.ServiceClusterAssetID[svcID] = *rt.ClusterAssetID
		}
	}
	if len(g.ServiceClusterAssetID) > 0 {
		clusterIDs := make(map[string]bool, len(g.ServiceClusterAssetID))
		for _, id := range g.ServiceClusterAssetID {
			clusterIDs[id] = true
		}
		ids := make([]string, 0, len(clusterIDs))
		for id := range clusterIDs {
			ids = append(ids, id)
		}
		var nodes []struct {
			ClusterAssetID string `db:"ancestor_id"`
			NodeAssetID    string `db:"descendant_id"`
		}
		if err := s.read(ctx, &nodes, `
			SELECT c.ancestor_id, c.descendant_id
			FROM asset_closure c
			JOIN asset a ON a.id = c.descendant_id
			WHERE c.ancestor_id IN (`+placeholders(len(ids))+`) AND a.kind = ?`,
			append(anySlice(ids), domain.KindK8sNode)...); err != nil {
			return nil, fmt.Errorf("loading k8s cluster nodes for graph: %w", err)
		}
		for _, n := range nodes {
			g.ClusterNodes[n.ClusterAssetID] = append(g.ClusterNodes[n.ClusterAssetID], n.NodeAssetID)
		}
	}

	netGraph, err := s.loadNetGraph(ctx)
	if err != nil {
		return nil, err
	}
	g.Net = netGraph

	// WP-I1: the declared structures whose members are ports on assets.
	structures, err := s.loadStructures(ctx)
	if err != nil {
		return nil, err
	}
	g.Structures = structures

	// WP-E2: hosts that can carry each other's guests.
	clusters, err := s.loadClusters(ctx)
	if err != nil {
		return nil, err
	}
	g.Clusters = clusters

	return g, nil
}

// loadNetGraph runs the six net_* loads the reachability algorithm needs
// (docs/reachability-design.md), plus the attachment-scoped asset_closure
// join that resolves inheritance. It returns nil when every one of them
// comes back empty, which is what makes Inputs.Net == nil and the whole
// feature inert on an estate that has declared no topology.
func (s *SQLStore) loadNetGraph(ctx context.Context) (*impact.NetGraph, error) {
	net := impact.NewNetGraph()

	var groups []domain.NetGroup
	if err := s.read(ctx, &groups, `SELECT * FROM net_group WHERE lifecycle <> ?`, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("loading net groups for graph: %w", err)
	}
	for _, g := range groups {
		net.Groups[g.ID] = impact.NetGroupInfo{
			ID: g.ID, Code: g.Code, Availability: g.Availability,
			MinHealthy: g.MinHealthy, FailoverMode: g.FailoverMode,
		}
	}

	var members []struct {
		domain.NetGroupMember
		AssetLifecycle string `db:"asset_lifecycle"`
	}
	if err := s.read(ctx, &members, `
		SELECT m.*, a.lifecycle AS asset_lifecycle
		FROM net_group_member m
		JOIN asset a ON a.id = m.asset_id
		WHERE m.lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading net group members for graph: %w", err)
	}
	for _, m := range members {
		net.Members[m.GroupID] = append(net.Members[m.GroupID], impact.NetGroupMemberInfo{
			AssetID: m.AssetID, Role: m.Role, AssetLifecycle: m.AssetLifecycle,
		})
	}

	var uplinks []domain.NetUplink
	if err := s.read(ctx, &uplinks, `SELECT * FROM net_uplink WHERE lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading net uplinks for graph: %w", err)
	}
	for _, u := range uplinks {
		net.Uplinks = append(net.Uplinks, impact.NetUplinkInfo{
			GroupID: u.GroupID, UpstreamGroupID: u.UpstreamGroupID, Plane: u.Plane,
		})
	}

	// Circuits as connectivity edges (WP-E1b).
	//
	// DERIVED, NOT DECLARED. A circuit's two terminations are already recorded;
	// asking somebody to also declare a net_uplink for it would be asking them
	// to say the same thing twice and keep the two in agreement forever. The
	// reach model is an undirected graph of forwarder groups joined by edges,
	// so a circuit whose ends land on interfaces of assets in DIFFERENT groups
	// already is one of those edges -- this only reads it out.
	//
	// THE JOINS ARE THE SPECIFICATION, and each one excludes a case that would
	// otherwise produce a wrong edge:
	//
	//   * both sides must terminate on an INTERFACE. A termination on a site is
	//     "it arrives in this building", which names no forwarder and cannot
	//     join anything.
	//   * both interfaces' assets must be MEMBERS of a net group. A circuit
	//     landing on a host that forwards nothing is not a path between groups.
	//   * the two groups must DIFFER. Both ends inside one group is a circuit
	//     between two members of the same group, which the group already
	//     covers, and unioning a group with itself is a no-op that would only
	//     make the edge list longer.
	//   * everything must be active: a retired circuit, a retired termination
	//     and a retired membership each mean the path is not there.
	//
	// Most circuits fail the first test, and that is correct -- a transit
	// circuit ends at the provider, whose side of it this inventory does not
	// model. Those still report their cost and their contract end; they simply
	// join nothing, which is the truth about them.
	//
	// The plane is the data plane. A circuit carrying management traffic is a
	// real thing and is not distinguishable here from any other, so claiming
	// otherwise would be inventing a fact.
	var circuitEdges []struct {
		CircuitID string `db:"circuit_id"`
		Label     string `db:"label"`
		GroupA    string `db:"group_a"`
		GroupZ    string `db:"group_z"`
	}
	if err := s.read(ctx, &circuitEdges, `
		SELECT c.id AS circuit_id, c.cid AS label,
		       ma.group_id AS group_a, mz.group_id AS group_z
		FROM circuit c
		JOIN circuit_termination ta ON ta.circuit_id = c.id AND ta.side = 'a'
		JOIN circuit_termination tz ON tz.circuit_id = c.id AND tz.side = 'z'
		JOIN interface ia ON ia.id = ta.interface_id
		JOIN interface iz ON iz.id = tz.interface_id
		JOIN net_group_member ma ON ma.asset_id = ia.asset_id
		JOIN net_group_member mz ON mz.asset_id = iz.asset_id
		WHERE c.lifecycle = ?
		  AND ta.lifecycle = ? AND tz.lifecycle = ?
		  AND ma.lifecycle = ? AND mz.lifecycle = ?
		  AND ma.group_id <> mz.group_id`,
		domain.LifecycleActive, domain.LifecycleActive, domain.LifecycleActive,
		domain.LifecycleActive, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading circuit edges for graph: %w", err)
	}
	for _, e := range circuitEdges {
		net.Uplinks = append(net.Uplinks, impact.NetUplinkInfo{
			GroupID: e.GroupA, UpstreamGroupID: e.GroupZ,
			Plane:     domain.PlaneData,
			CircuitID: e.CircuitID, Label: e.Label,
		})
	}

	var anchors []domain.NetAnchor
	if err := s.read(ctx, &anchors, `SELECT * FROM net_anchor WHERE lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading net anchors for graph: %w", err)
	}
	for _, a := range anchors {
		net.Anchors = append(net.Anchors, impact.NetAnchorInfo{
			Code: a.Code, Scope: a.Scope, GroupID: a.GroupID, Plane: a.Plane, EnvironmentID: a.EnvironmentID,
		})
	}

	var attachments []domain.NetAttachment
	if err := s.read(ctx, &attachments, `SELECT * FROM net_attachment WHERE lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading net attachments for graph: %w", err)
	}
	for _, a := range attachments {
		net.Attachments = append(net.Attachments, impact.NetAttachmentInfo{
			ID: a.ID, AssetID: a.AssetID, GroupID: a.GroupID, Plane: a.Plane,
		})
	}

	var attachMembers []struct {
		AttachmentID   string `db:"attachment_id"`
		GroupID        string `db:"group_id"`
		AssetID        string `db:"asset_id"`
		AssetLifecycle string `db:"asset_lifecycle"`
	}
	if err := s.read(ctx, &attachMembers, `
		SELECT am.attachment_id, na.group_id, am.asset_id, a.lifecycle AS asset_lifecycle
		FROM net_attachment_member am
		JOIN net_attachment na ON na.id = am.attachment_id AND na.lifecycle = ?
		JOIN asset a ON a.id = am.asset_id`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading net attachment members for graph: %w", err)
	}
	for _, m := range attachMembers {
		net.AttachmentMembers[m.AttachmentID] = append(net.AttachmentMembers[m.AttachmentID], impact.NetAttachmentMemberInfo{
			AssetID: m.AssetID, AssetLifecycle: m.AssetLifecycle,
		})
	}

	// Attachment inheritance, scoped to attached subtrees only -- bounded by
	// (attached hosts x their descendants) rather than the whole estate.
	var closure []struct {
		AncestorID   string `db:"ancestor_id"`
		DescendantID string `db:"descendant_id"`
		Depth        int    `db:"depth"`
	}
	if err := s.read(ctx, &closure, `
		SELECT DISTINCT c.ancestor_id, c.descendant_id, c.depth
		FROM asset_closure c
		JOIN net_attachment na ON na.asset_id = c.ancestor_id AND na.lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading attachment closure for graph: %w", err)
	}
	for _, c := range closure {
		net.Closure = append(net.Closure, impact.ClosureRow{
			AncestorID: c.AncestorID, DescendantID: c.DescendantID, Depth: c.Depth,
		})
	}

	if net.IsEmpty() {
		return nil, nil
	}

	// Labels for the assets reachability can name. Loaded only once the graph
	// is known to be non-empty, and deliberately NOT counted by IsEmpty, so an
	// estate with no declared topology still produces Inputs.Net == nil and a
	// byte-identical answer. Scoped to attached assets and their descendants
	// rather than the whole asset table, which is the same bound the closure
	// query above already uses.
	var labels []struct {
		ID   string `db:"id"`
		Kind string `db:"kind"`
		Name string `db:"name"`
	}
	if err := s.read(ctx, &labels, `
		SELECT DISTINCT a.id, a.kind, a.name
		FROM asset a
		JOIN asset_closure c ON c.descendant_id = a.id
		JOIN net_attachment na ON na.asset_id = c.ancestor_id AND na.lifecycle = ?
		ORDER BY a.id`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading asset labels for graph: %w", err)
	}
	for _, l := range labels {
		net.AssetNames[l.ID] = impact.AssetLabel{Name: l.Name, Kind: l.Kind}
	}

	return net, nil
}

// DownInstances resolves the assets being taken away to the instances that go
// with them.
//
// The closure join is what makes "reboot this VM", "reboot this hypervisor",
// "this rack loses power" and "this PDU fails" the same operation: each one
// resolves to a set of ancestors, and anything hosted at or below them is lost.
func (s *SQLStore) DownInstances(ctx context.Context, assetIDs []string) (map[string]bool, error) {
	if len(assetIDs) == 0 {
		return map[string]bool{}, nil
	}
	var ids []string
	err := s.read(ctx, &ids, `
		SELECT si.id
		FROM service_instance si
		JOIN asset_closure c ON c.descendant_id = si.host_asset_id
		WHERE c.ancestor_id IN (`+placeholders(len(assetIDs))+`)`, anySlice(assetIDs)...)
	if err != nil {
		return nil, fmt.Errorf("resolving downed instances: %w", err)
	}
	down := make(map[string]bool, len(ids))
	for _, id := range ids {
		down[id] = true
	}
	return down, nil
}

// Simulate runs a full impact analysis for a set of assets.
// CircuitJoinsGroups reports whether this circuit is a connectivity edge --
// whether both its ends land on interfaces of assets that forward, in two
// different groups.
//
// Asked of the model rather than inferred from an empty simulation, because the
// two mean different things: a circuit that joins nothing and a circuit whose
// loss happens to break nothing look identical in a Result and need opposite
// sentences on the page.
func (s *SQLStore) CircuitJoinsGroups(ctx context.Context, circuitID string) (bool, error) {
	var n int
	// The same joins as the edge derivation above, and they must stay the same
	// -- a page saying "this joins two sites" about a circuit the engine does
	// not treat as an edge would be worse than saying nothing.
	if err := s.readOne(ctx, &n, `
		SELECT count(*)
		FROM circuit c
		JOIN circuit_termination ta ON ta.circuit_id = c.id AND ta.side = 'a'
		JOIN circuit_termination tz ON tz.circuit_id = c.id AND tz.side = 'z'
		JOIN interface ia ON ia.id = ta.interface_id
		JOIN interface iz ON iz.id = tz.interface_id
		JOIN net_group_member ma ON ma.asset_id = ia.asset_id
		JOIN net_group_member mz ON mz.asset_id = iz.asset_id
		WHERE c.id = ?
		  AND c.lifecycle = ? AND ta.lifecycle = ? AND tz.lifecycle = ?
		  AND ma.lifecycle = ? AND mz.lifecycle = ?
		  AND ma.group_id <> mz.group_id`,
		circuitID, domain.LifecycleActive, domain.LifecycleActive, domain.LifecycleActive,
		domain.LifecycleActive, domain.LifecycleActive); err != nil {
		return false, fmt.Errorf("checking whether circuit %s joins groups: %w", circuitID, err)
	}
	return n > 0, nil
}

// CircuitCut is what cutting one circuit does to connectivity.
//
// THREE OUTCOMES, AND CONFLATING ANY TWO IS A LIE THE PAGE WOULD TELL:
//
//	Joins == false          it is not an edge at all. Most circuits end at the
//	                        provider, whose side is not modelled.
//	Separates == false      it is an edge and another path survives the cut.
//	                        This is the answer redundancy is bought for.
//	Separates == true       it is the only path, and the far side is cut off.
//
// The impact Result alone cannot tell the first from the second, and cannot
// tell either from the third when nothing on the far side happens to depend on
// anything -- a partition with no consumers produces no findings, which reads
// identically to no partition. So the connectivity answer is computed here,
// from the graph, and the service consequences are reported beside it.
type CircuitCut struct {
	Joins     bool
	Separates bool
	// Groups are the codes of the two groups it joins, for the sentence.
	Groups []string
}

// CircuitCutEffect answers what cutting one circuit does.
func (s *SQLStore) CircuitCutEffect(ctx context.Context, circuitID string) (CircuitCut, error) {
	var out CircuitCut
	g, err := s.LoadGraph(ctx)
	if err != nil {
		return out, err
	}
	if g.Net == nil {
		return out, nil
	}

	// The edges this circuit contributes, and everything else.
	var ends [][2]string
	adjacency := map[string][]string{}
	for _, u := range g.Net.Uplinks {
		if u.CircuitID == circuitID {
			ends = append(ends, [2]string{u.GroupID, u.UpstreamGroupID})
			continue
		}
		// Undirected: the reach model treats an uplink as a connection, not as
		// a direction of travel, and a path that only works one way would make
		// this answer disagree with the engine's.
		adjacency[u.GroupID] = append(adjacency[u.GroupID], u.UpstreamGroupID)
		adjacency[u.UpstreamGroupID] = append(adjacency[u.UpstreamGroupID], u.GroupID)
	}
	if len(ends) == 0 {
		return out, nil
	}
	out.Joins = true
	for _, code := range []string{ends[0][0], ends[0][1]} {
		if gi, ok := g.Net.Groups[code]; ok {
			out.Groups = append(out.Groups, gi.Code)
		} else {
			out.Groups = append(out.Groups, code)
		}
	}

	// Still connected without it? A breadth-first walk over what is left.
	for _, e := range ends {
		if !connected(adjacency, e[0], e[1]) {
			out.Separates = true
			break
		}
	}
	return out, nil
}

// connected reports whether b is reachable from a over the given adjacency.
func connected(adjacency map[string][]string, a, b string) bool {
	if a == b {
		return true
	}
	seen := map[string]bool{a: true}
	queue := []string{a}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[cur] {
			if next == b {
				return true
			}
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}

func (s *SQLStore) Simulate(ctx context.Context, req impact.Request) (impact.Result, error) {
	graph, err := s.LoadGraph(ctx)
	if err != nil {
		return impact.Result{}, err
	}
	down, err := s.DownInstances(ctx, req.DownAssetIDs)
	if err != nil {
		return impact.Result{}, err
	}
	// SubtreeIDs is computed once here and feeds both DownInstances (above,
	// via the closure join it already does internally) and the forwarder-
	// down set below -- what makes "this rack loses power" downgrade the
	// forwarders inside it as a consequence of the rack, not as a separate
	// input (docs/reachability-design.md, scenario 10).
	subtree, err := s.SubtreeIDs(ctx, req.DownAssetIDs)
	if err != nil {
		return impact.Result{}, err
	}
	downAssets := make(map[string]bool, len(subtree))
	for _, id := range subtree {
		downAssets[id] = true
	}
	return impact.Analyse(graph, req, impact.Inputs{
		DownInstanceIDs: down, DownAssetIDs: downAssets, Net: graph.Net,
	}), nil
}

// loadStructures reads the declared things that exist only because ports on
// assets belong to them: VLANs, first-hop redundancy groups, overlays.
//
// WP-I1. Each of these arrived as a work package with a model, a page and its
// own findings, and none reached the impact engine -- so simulating the loss of
// a switch reported the services on it and said nothing about the broadcast
// domain that switch was the only port of. Three queries, each the obvious
// join, and an estate that has declared none of them gets an empty slice and a
// result byte-identical to a build without the feature.
//
// A row per MEMBER, not per structure: a switch with four ports in one VLAN
// appears four times, and the engine counts distinct surviving assets because
// losing the switch takes all four at once. Deduplicating here would be tidier
// and would throw away the difference between four ports on one box and four
// boxes with one each.
func (s *SQLStore) loadStructures(ctx context.Context) ([]impact.Structure, error) {
	type memberRow struct {
		Kind    string `db:"kind"`
		ID      string `db:"id"`
		Name    string `db:"name"`
		AssetID string `db:"asset_id"`
	}
	byID := map[string]*impact.Structure{}
	order := []string{}

	collect := func(rows []memberRow) {
		for _, r := range rows {
			st, ok := byID[r.ID]
			if !ok {
				st = &impact.Structure{Kind: r.Kind, ID: r.ID, Name: r.Name}
				byID[r.ID] = st
				order = append(order, r.ID)
			}
			st.AssetIDs = append(st.AssetIDs, r.AssetID)
		}
	}

	// A VLAN's members are the ports in it.
	var vlans []memberRow
	err := s.read(ctx, &vlans, `
		SELECT 'vlan' AS kind, v.id, v.name, i.asset_id
		FROM vlan v
		JOIN interface_vlan iv ON iv.vlan_id = v.id
		JOIN interface i ON i.id = iv.interface_id
		WHERE v.lifecycle <> 'retired'
		ORDER BY v.vid, i.asset_id`)
	if err != nil {
		return nil, fmt.Errorf("loading VLAN membership for the graph: %w", err)
	}
	collect(vlans)

	// A redundancy group's members are the routers in it.
	var fhrp []memberRow
	err = s.read(ctx, &fhrp, `
		SELECT 'fhrp' AS kind, g.id, g.name, i.asset_id
		FROM fhrp_group g
		JOIN fhrp_member m ON m.group_id = g.id
		JOIN interface i ON i.id = m.interface_id
		WHERE g.lifecycle <> 'retired'
		ORDER BY g.name, i.asset_id`)
	if err != nil {
		return nil, fmt.Errorf("loading redundancy membership for the graph: %w", err)
	}
	collect(fhrp)

	// An overlay's members are what terminates into it -- a port directly, or
	// every port in the VLAN that terminates. The second is the join that
	// matters: a VXLAN mapping VLAN 30 at each site is held up by the switches
	// carrying VLAN 30, and nothing else names them.
	var overlays []memberRow
	err = s.read(ctx, &overlays, `
		SELECT 'l2vpn' AS kind, l.id, l.name, i.asset_id
		FROM l2vpn l
		JOIN l2vpn_termination t ON t.l2vpn_id = l.id AND t.lifecycle <> 'retired'
		JOIN interface i ON i.id = t.interface_id
		WHERE l.lifecycle <> 'retired'
		UNION ALL
		SELECT 'l2vpn' AS kind, l.id, l.name, i.asset_id
		FROM l2vpn l
		JOIN l2vpn_termination t ON t.l2vpn_id = l.id AND t.lifecycle <> 'retired'
		JOIN interface_vlan iv ON iv.vlan_id = t.vlan_id
		JOIN interface i ON i.id = iv.interface_id
		WHERE l.lifecycle <> 'retired'`)
	if err != nil {
		return nil, fmt.Errorf("loading overlay terminations for the graph: %w", err)
	}
	collect(overlays)

	out := make([]impact.Structure, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// loadClusters reads the clusters, their members, and the guests on each member.
//
// The guests come from asset_closure, which is the same containment the engine
// already walks to take a VM down with its host -- so what cluster HA revives
// is exactly what containment condemned, and the two cannot disagree about
// which guests are on which box.
//
// DESCENDANTS, NOT CHILDREN. A guest may sit under a bridge under the host, and
// the down set is closure-expanded, so the revival has to be as well or a
// nested guest would stay down while its sibling came back.
func (s *SQLStore) loadClusters(ctx context.Context) ([]impact.Cluster, error) {
	var rows []struct {
		ID       string `db:"id"`
		Name     string `db:"name"`
		HAPolicy string `db:"ha_policy"`
		MinHosts *int   `db:"min_hosts"`
	}
	err := s.read(ctx, &rows, `
		SELECT id, name, ha_policy, min_hosts FROM cluster
		WHERE lifecycle <> 'retired' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("loading clusters for the graph: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	var members []struct {
		ClusterID string `db:"cluster_id"`
		AssetID   string `db:"asset_id"`
	}
	err = s.read(ctx, &members, `
		SELECT m.cluster_id, m.asset_id FROM cluster_member m
		JOIN asset a ON a.id = m.asset_id
		WHERE a.lifecycle <> 'retired'`)
	if err != nil {
		return nil, fmt.Errorf("loading cluster members: %w", err)
	}

	// Every live descendant of every member, excluding the member itself: a
	// host is not its own guest, and reviving it would undo the outage.
	var guests []struct {
		HostID  string `db:"host_id"`
		GuestID string `db:"guest_id"`
	}
	err = s.read(ctx, &guests, `
		SELECT c.ancestor_id AS host_id, c.descendant_id AS guest_id
		FROM asset_closure c
		JOIN cluster_member m ON m.asset_id = c.ancestor_id
		JOIN asset a ON a.id = c.descendant_id
		WHERE c.ancestor_id <> c.descendant_id AND a.lifecycle <> 'retired'`)
	if err != nil {
		return nil, fmt.Errorf("loading cluster guests: %w", err)
	}
	guestsByHost := map[string][]string{}
	for _, g := range guests {
		guestsByHost[g.HostID] = append(guestsByHost[g.HostID], g.GuestID)
	}

	byID := map[string]*impact.Cluster{}
	out := make([]impact.Cluster, 0, len(rows))
	for _, r := range rows {
		out = append(out, impact.Cluster{
			ID: r.ID, Name: r.Name, HAPolicy: r.HAPolicy, MinHosts: r.MinHosts,
			GuestsByHost: map[string][]string{},
		})
		byID[r.ID] = &out[len(out)-1]
	}
	for _, m := range members {
		c, ok := byID[m.ClusterID]
		if !ok {
			continue
		}
		c.MemberAssetIDs = append(c.MemberAssetIDs, m.AssetID)
		if g := guestsByHost[m.AssetID]; len(g) > 0 {
			c.GuestsByHost[m.AssetID] = g
		}
	}
	return out, nil
}
