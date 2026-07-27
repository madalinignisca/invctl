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

	var instances []domain.ServiceInstance
	if err := s.read(ctx, &instances, `SELECT * FROM service_instance`); err != nil {
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
		g.Endpoints[e.ID] = impact.Endpoint{ID: e.ID, ServiceID: e.ServiceID, Name: e.Name}
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
	if err := s.read(ctx, &members, `SELECT * FROM backend_member`); err != nil {
		return nil, fmt.Errorf("loading backend members for graph: %w", err)
	}
	for _, m := range members {
		g.PoolMembers[m.PoolID] = append(g.PoolMembers[m.PoolID], m.EndpointID)
	}

	var deps []domain.Dependency
	err = s.read(ctx, &deps, `SELECT * FROM dependency WHERE lifecycle = ?`, domain.LifecycleActive)
	if err != nil {
		return nil, fmt.Errorf("loading dependencies for graph: %w", err)
	}
	g.Deps = deps

	return g, nil
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
	return impact.Analyse(graph, req, down), nil
}
