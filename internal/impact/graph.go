// Package impact answers "if I take this away, what breaks?".
//
// It is written in Go rather than SQL on purpose. A service's status depends
// on the aggregate of all its inbound edges, and dependency cycles are normal
// (app -> db -> auth -> app), so this is a fixed-point iteration rather than a
// single traversal. A recursive CTE would either loop forever or need a depth
// cap that silently truncates the answer.
package impact

import (
	"github.com/gabriel/invctl/internal/domain"
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
type Endpoint struct {
	ID        string
	ServiceID string
	Name      string
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
}

// NewGraph returns an empty graph with its maps ready.
func NewGraph() *Graph {
	return &Graph{
		Services:    map[string]*domain.Service{},
		Instances:   map[string][]Instance{},
		Endpoints:   map[string]Endpoint{},
		Routes:      map[string]Route{},
		PoolMembers: map[string][]string{},
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
