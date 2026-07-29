package store

import (
	"context"
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/impact"
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

	var endpoints []domain.Endpoint
	if err := s.read(ctx, &endpoints, `SELECT * FROM endpoint`); err != nil {
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
