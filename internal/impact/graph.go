// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package impact answers "if I take this away, what breaks?".
//
// It is written in Go rather than SQL on purpose. A service's status depends
// on the aggregate of all its inbound edges, and dependency cycles are normal
// (app -> db -> auth -> app), so this is a fixed-point iteration rather than a
// single traversal. A recursive CTE would either loop forever or need a depth
// cap that silently truncates the answer.
package impact

import (
	"github.com/madalinignisca/invctl/internal/domain"
)

// Instance is a placement, reduced to what capacity evaluation needs.
type Instance struct {
	ID          string
	ServiceID   string
	HostAssetID string
	Role        string
	Shard       string
	// Disabled instances are not expected to be running, so losing their host
	// costs nothing. Counting them as capacity would make a service look
	// healthier than it is.
	Disabled bool
}

// Endpoint is a listening socket, reduced to its owning service.
//
// BindScope, Exposure and ServiceEnvID are M3 additions, read from the
// endpoint/service rows LoadGraph already loads -- no extra query. BindScope
// gates the local exemption (loopback/unix traffic is intra-host by
// definition); Exposure gates the anchor requirement, 1:1 with
// net_anchor.scope. The two are deliberately not conflated
// (docs/reachability-design.md).
type Endpoint struct {
	ID           string
	ServiceID    string
	Name         string
	BindScope    string
	Exposure     string
	ServiceEnvID string
}

// Route maps a frontend endpoint to a backend pool. It is a node in the graph
// rather than a passthrough: its health combines the proxy's own health with
// the health of the pool behind it.
type Route struct {
	ID                 string
	FrontendEndpointID string
	BackendPoolID      string
	Name               string
}

// Graph is the whole dependency picture, loaded once per analysis.
//
// The estate this targets is thousands of rows, not millions, so loading it
// wholesale is both simpler and faster than issuing a query per iteration of
// the fixed point.
type Graph struct {
	Services    map[string]*domain.Service
	Instances   map[string][]Instance // by service id
	Endpoints   map[string]Endpoint
	Routes      map[string]Route
	PoolMembers map[string][]string // pool id -> endpoint ids
	Deps        []domain.Dependency

	// ServiceClusterAssetID and ClusterNodes exist for scenario 9's cluster_ip
	// resolution (reach.go): a k8s_workload instance's rt_k8s.cluster_asset_id
	// names the cluster, and ClusterNodes lists that cluster's k8s_node
	// descendants via asset_closure. Both nil/empty when there is no k8s
	// runtime detail in the estate, which makes the feature inert exactly like
	// every other M3 addition.
	ServiceClusterAssetID map[string]string   // service id -> cluster asset id
	ClusterNodes          map[string][]string // cluster asset id -> k8s_node descendant asset ids

	// Structures are the declared things that exist only because ports on
	// assets belong to them: VLANs, first-hop redundancy groups, overlays.
	// Empty whenever none is declared, which makes the whole feature inert on
	// an estate that has not modelled any -- the same way Net is.
	Structures []Structure

	// Net is the reachability picture LoadGraph loads alongside everything
	// else, nil whenever the estate has declared no topology at all. Kept on
	// Graph (rather than threaded separately) so "the whole dependency
	// picture, loaded once per analysis" stays true of one object.
	Net *NetGraph
}

// NewGraph returns an empty graph with its maps ready.
func NewGraph() *Graph {
	return &Graph{
		Services:              map[string]*domain.Service{},
		Instances:             map[string][]Instance{},
		Endpoints:             map[string]Endpoint{},
		Routes:                map[string]Route{},
		PoolMembers:           map[string][]string{},
		ServiceClusterAssetID: map[string]string{},
		ClusterNodes:          map[string][]string{},
	}
}

// providerServiceID resolves a dependency's provider to the service that
// actually provides the capability, following a route through to its frontend.
func (g *Graph) providerServiceID(d domain.Dependency) string {
	if d.ProviderEndpointID != nil {
		if ep, ok := g.Endpoints[*d.ProviderEndpointID]; ok {
			return ep.ServiceID
		}
		return ""
	}
	if d.ProviderRouteID != nil {
		if r, ok := g.Routes[*d.ProviderRouteID]; ok {
			if ep, ok := g.Endpoints[r.FrontendEndpointID]; ok {
				return ep.ServiceID
			}
		}
	}
	return ""
}
