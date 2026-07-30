package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/gabriel/invctl/internal/domain"
)

// The neighbourhood walk: one asset, plus what it actually connects to, out to
// a bounded number of hops.
//
// It is deliberately NOT a picture of the estate. A diagram of everything is
// unreadable at any size and is the failure mode of every infrastructure
// diagram tool ever written; the useful question during an incident is "what
// does THIS touch", and that is what this answers.
//
// Five queries, whatever the size of the answer -- the same shape and the same
// justification LoadGraph uses. Calling ListInterfaces once per node is the
// obvious wrong turn here: it costs two queries per asset and still does not
// close the frontier, because it resolves a cable's far end without knowing
// whether that far end is in the picture.
//
// Every query orders explicitly. That is not tidiness: the layout that consumes
// this seeds its ordering from the caller's slice order, so an unordered result
// set produces a different picture on a second look at the same URL, and two
// people on the same call comparing the same link would be comparing two
// pictures. Neither engine promises an order it was not asked for, and they are
// free to disagree with each other about the same rows.

const (
	// NeighbourhoodMaxHops bounds the walk. Four hops from a core switch
	// already reaches most of a small estate; more than that is a picture of
	// everything with extra steps.
	NeighbourhoodMaxHops = 4

	// NeighbourhoodDefaultHops is far enough to leave the box and reach what it
	// is plugged into, and near enough to stay readable: from a guest that is
	// its bridge, its hypervisor, the hypervisor's switches and its sibling
	// guests.
	NeighbourhoodDefaultHops = 2

	// NeighbourhoodDefaultMaxAssets is the node budget.
	//
	// It exists because an N-hop neighbourhood of a core switch explodes, and
	// because every asset id becomes a placeholder in four later statements. It
	// is a cut, and a cut has to be REPORTED -- AssetsElided carries the count
	// so the page can say "+12 not shown". A silently missing node is a lie,
	// and this codebase is unusually strict about the difference.
	NeighbourhoodDefaultMaxAssets = 60
)

// NeighbourhoodQuery asks for one asset's neighbourhood.
type NeighbourhoodQuery struct {
	// AssetID is the anchor. Its whole containment subtree seeds the walk, so
	// asking about a hypervisor includes its guests without a second control --
	// the same expansion "if I take this away" already uses everywhere else.
	AssetID string
	// Hops is how far to walk from the seed. Clamped to [1, NeighbourhoodMaxHops].
	Hops int
	// MaxAssets is the node budget; zero means NeighbourhoodDefaultMaxAssets.
	MaxAssets int
}

func (q NeighbourhoodQuery) hops() int {
	switch {
	case q.Hops < 1:
		return 1
	case q.Hops > NeighbourhoodMaxHops:
		return NeighbourhoodMaxHops
	default:
		return q.Hops
	}
}

func (q NeighbourhoodQuery) maxAssets() int {
	if q.MaxAssets <= 0 {
		return NeighbourhoodDefaultMaxAssets
	}
	return q.MaxAssets
}

// NeighbourAsset is one box: an asset the walk reached, and how far away it is.
type NeighbourAsset struct {
	ID string `db:"id"`
	// Hop is the shortest number of steps from the seed subtree, 0 for the
	// subject and anything it contains.
	Hop       int    `db:"hop"`
	Kind      string `db:"kind"`
	Name      string `db:"name"`
	Lifecycle string `db:"lifecycle"`
	// ParentID is empty at the top level. Containment edges are derived from
	// it rather than queried: if both ends are in the node set, the edge is in
	// the picture, and no query can tell us anything the ids do not.
	ParentID string `db:"parent_id"`
}

// NeighbourPort is one end of a cable, with everything the picture needs to
// label it and nothing it does not.
type NeighbourPort struct {
	InterfaceID string
	AssetID     string
	Name        string
	FormFactor  string
	IsMgmt      bool
	// Enabled is returned, never filtered on. DeriveNetworkProposal skips a
	// disabled port because it is proposing topology; a diagram must SHOW a
	// cabled-but-disabled port, because "the wire is there and the port is
	// down" is the 03:00 answer and filtering it hides exactly the finding.
	Enabled bool
	// Master is the name of the interface this one is enslaved to -- a bond,
	// or a bridge. Empty when it stands alone. It is what makes the descent
	// legible inside a single host: hv-01/eno2 is cabled to the switch, and
	// hv-01/bond0 is what the bridge lands on, and without this the two facts
	// look unrelated.
	Master string
}

// NeighbourLink is one cable or one virtual adjacency. The two are the same row
// in the same table; what tells them apart is the form factor on the ends, not
// the medium column, which is NULL for plenty of real cables too.
type NeighbourLink struct {
	ID     string
	Medium string
	A, B   NeighbourPort
}

// NeighbourService is a logical service with at least one placement in the
// neighbourhood.
type NeighbourService struct {
	ID           string `db:"id"`
	Code         string `db:"code"`
	Name         string `db:"name"`
	Kind         string `db:"kind"`
	Availability string `db:"availability"`
	Tier         int    `db:"tier"`
	Lifecycle    string `db:"lifecycle"`
}

// NeighbourPlacement is one running copy on one host in the neighbourhood.
type NeighbourPlacement struct {
	InstanceID   string `db:"instance_id"`
	ServiceID    string `db:"service_id"`
	HostAssetID  string `db:"host_asset_id"`
	Role         string `db:"role"`
	DesiredState string `db:"desired_state"`
}

// NeighbourEndpoint is one listening socket.
//
// BoundInterfaceID is the only case that descends to a specific port. Every
// other endpoint descends to the HOST, because bind_scope 'host' with no
// address is 0.0.0.0 -- listening on all of them, which is a fact about the
// service and not a missing value. Drawing a line to one arbitrary port for
// those would be inventing a fact.
type NeighbourEndpoint struct {
	ID        string `db:"id"`
	ServiceID string `db:"service_id"`
	Name      string `db:"name"`
	L4Proto   string `db:"l4_proto"`
	Port      int    `db:"port"`
	BindScope string `db:"bind_scope"`
	Exposure  string `db:"exposure"`
	// AddrText is the pinned address, empty when the socket is not pinned.
	AddrText string `db:"addr_text"`
	// BoundInterfaceID, BoundAssetID and BoundPort are set only for a pinned
	// endpoint whose address sits on a known interface.
	BoundInterfaceID string `db:"bound_interface_id"`
	BoundAssetID     string `db:"bound_asset_id"`
	BoundPort        string `db:"bound_port"`
}

// NeighbourDependency is one declared service-to-service edge with both ends
// inside the neighbourhood.
type NeighbourDependency struct {
	ID                string `db:"id"`
	ConsumerServiceID string `db:"consumer_service_id"`
	ProviderServiceID string `db:"provider_service_id"`
	Nature            string `db:"nature"`
	// Via is 'endpoint' or 'route' -- a dependency on a load balancer's
	// frontend is not the same claim as a dependency on a backend directly,
	// and the picture should not flatten them.
	Via              string `db:"via"`
	ProviderEndpoint string `db:"provider_endpoint"`
	ProviderPort     int    `db:"provider_port"`
}

// NeighbourhoodGraph is everything the picture is drawn from.
type NeighbourhoodGraph struct {
	SubjectID string
	Hops      int

	Assets       []NeighbourAsset
	Links        []NeighbourLink
	Services     []NeighbourService
	Placements   []NeighbourPlacement
	Endpoints    []NeighbourEndpoint
	Dependencies []NeighbourDependency

	// AssetsElided is how many assets the walk reached and the node budget cut.
	AssetsElided int
	// DependenciesOutside counts declared edges with exactly one end in the
	// neighbourhood. They are not drawn -- there is nowhere to draw them to --
	// but a service whose consumers are all off-screen must not read as a
	// service nothing depends on.
	DependenciesOutside int
}

// Neighbourhood walks outwards from one asset.
//
// The walk itself is a single recursive CTE and traverses three things:
//
//   - an active DATA-PLANE link, in both orientations, which covers the
//     physical plant and the virtual adjacency alike -- a veth landing on a
//     bridge is a link row exactly like a DAC between two switches.
//
//     A management link is followed OUT OF THE SEED and no further, which is
//     the difference between a useful page and a useless one. Every host has a
//     console cable to the same out-of-band switch, so transiting them puts
//     every host two hops from every other host: measured on a 5,000-asset
//     estate, an unrestricted two-hop walk reached 5,010 assets through the
//     OOB switch against 384 without it -- and the node budget then serves an
//     alphabetical slice of the whole estate as "what this touches".
//     internal/store/path.go refuses management cabling outright for the same
//     reason ("that is how every naive network diagram lies"); a route and an
//     adjacency are different claims, and the two pages must not disagree
//     about adjacency while sharing a renderer.
//
//     Seed-only rather than never, because "what is this plugged into" plainly
//     includes the subject's own console switch. What it does not include is
//     every other box that happens to share it. The switch therefore appears
//     at hop 1 and dead-ends there, and because neighbourhoodLinks is
//     unfiltered the console cable itself is drawn -- not traversing an edge
//     and not showing it are different things;
//
//   - containment UPWARDS only. Bidirectional containment turned three hops
//     from a guest into the whole rack: up to the rack, then back down into
//     every sibling of every host in it. Upwards-only keeps the walk on the
//     path an operator is actually tracing, and the seed already covers
//     downwards by starting from the subject's whole subtree.
//
// Enslavement (interface.lag_parent_id) is deliberately NOT a hop. Every master
// pointer in this model is within one asset -- a NIC enslaved to that host's
// own bond -- so following it would join an asset to itself, which is not a
// hop and which the layout would reject as a self-edge. It travels instead on
// NeighbourPort.Master, where it belongs: it is a fact about a port, not an
// adjacency between boxes.
func (s *SQLStore) Neighbourhood(ctx context.Context, q NeighbourhoodQuery) (*NeighbourhoodGraph, error) {
	if q.AssetID == "" {
		return nil, fmt.Errorf("neighbourhood: no asset: %w", domain.ErrInvalid)
	}
	hops := q.hops()

	g := &NeighbourhoodGraph{SubjectID: q.AssetID, Hops: hops}

	assets, err := s.neighbourhoodAssets(ctx, q.AssetID, hops)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		// Every asset has a depth-0 closure row pointing at itself, so an empty
		// walk means the id resolves to nothing at all.
		return nil, fmt.Errorf("neighbourhood of %s: %w", q.AssetID, domain.ErrNotFound)
	}
	if budget := q.maxAssets(); len(assets) > budget {
		// Ordered by hop then name, so the cut always falls on the furthest
		// things. The subject is hop 0 and can never be cut.
		//
		// Re-sorted in Go before cutting, and that is not belt-and-braces: SQL
		// ORDER BY name is COLLATION-dependent, so WHICH assets survive the
		// budget would differ between a byte-ordered SQLite and an ICU- or
		// glibc-collated PostgreSQL. The dual-engine suite cannot catch it --
		// the postgres:17-alpine test container is musl-linked and its strcoll
		// is byte order, so it agrees with SQLite while a production server
		// would not. Sorting here makes the kept set a function of the data.
		sortAssetsForBudget(assets)
		g.AssetsElided = len(assets) - budget
		assets = assets[:budget]
	}
	g.Assets = assets

	ids := make([]string, len(assets))
	for i, a := range assets {
		ids[i] = a.ID
	}

	if g.Links, err = s.neighbourhoodLinks(ctx, ids); err != nil {
		return nil, err
	}
	if err := s.neighbourhoodWorkloads(ctx, ids, g); err != nil {
		return nil, err
	}
	return g, nil
}

// neighbourhoodAssets is the walk.
//
// UNION rather than UNION ALL in the recursive term bounds re-visits to one
// row per (node, hop) -- not one per node, which is what this said before it
// was measured: a four-hop walk over 5,146 assets produces 10,199 walk rows.
// Bounded at nodes x hops, never exponential. MIN(hop) then collapses the
// several depths a node can be reached at into the shortest one. The recursive reference appears exactly once, in the FROM of
// the recursive term, with no aggregate and no ORDER BY or LIMIT -- the
// intersection of what SQLite and PostgreSQL each allow.
func (s *SQLStore) neighbourhoodAssets(ctx context.Context, subjectID string, hops int) ([]NeighbourAsset, error) {
	var rows []NeighbourAsset
	err := s.read(ctx, &rows, `
		WITH RECURSIVE
		seed(asset_id) AS (
			SELECT c.descendant_id FROM asset_closure c WHERE c.ancestor_id = ?
		),
		adj(src_asset_id, dst_asset_id, is_mgmt) AS (
			SELECT ai.asset_id, bi.asset_id,
			       CASE WHEN ai.is_mgmt OR bi.is_mgmt THEN 1 ELSE 0 END
			FROM link l
			JOIN interface ai ON ai.id = l.a_interface_id
			JOIN interface bi ON bi.id = l.b_interface_id
			WHERE l.lifecycle = ?
			UNION ALL
			SELECT bi.asset_id, ai.asset_id,
			       CASE WHEN ai.is_mgmt OR bi.is_mgmt THEN 1 ELSE 0 END
			FROM link l
			JOIN interface ai ON ai.id = l.a_interface_id
			JOIN interface bi ON bi.id = l.b_interface_id
			WHERE l.lifecycle = ?
			UNION ALL
			SELECT c.descendant_id, c.ancestor_id, 0
			FROM asset_closure c WHERE c.depth = 1
		),
		walk(asset_id, hop) AS (
			SELECT s.asset_id, 0 FROM seed s
			UNION
			SELECT adj.dst_asset_id, walk.hop + 1
			FROM walk
			JOIN adj ON adj.src_asset_id = walk.asset_id
			WHERE walk.hop < ?
			  AND (adj.is_mgmt = 0 OR walk.hop = 0)
		)
		SELECT w.asset_id AS id, MIN(w.hop) AS hop, a.kind, a.name, a.lifecycle,
		       COALESCE(a.parent_id, '') AS parent_id
		FROM walk w
		JOIN asset a ON a.id = w.asset_id
		WHERE a.lifecycle <> ? OR a.id = ?
		GROUP BY w.asset_id, a.kind, a.name, a.lifecycle, a.parent_id
		ORDER BY MIN(w.hop), a.name, w.asset_id`,
		subjectID,
		domain.LifecycleActive, domain.LifecycleActive,
		hops,
		domain.LifecycleRetired, subjectID)
	if err != nil {
		return nil, fmt.Errorf("walking the neighbourhood of %s: %w", subjectID, err)
	}
	return rows, nil
}

// neighbourhoodLinks returns every active cable with BOTH ends inside the node
// set. Both ends, because that is what closes the frontier: a link with one end
// off-screen would be drawn as a stub going nowhere, and the layout has no node
// to anchor it to.
func (s *SQLStore) neighbourhoodLinks(ctx context.Context, assetIDs []string) ([]NeighbourLink, error) {
	if len(assetIDs) == 0 {
		return nil, nil
	}
	type linkRow struct {
		ID     string `db:"link_id"`
		Medium string `db:"medium"`

		AInterfaceID string `db:"a_interface_id"`
		AAssetID     string `db:"a_asset_id"`
		APort        string `db:"a_port"`
		AForm        string `db:"a_form"`
		AMgmt        bool   `db:"a_is_mgmt"`
		AEnabled     bool   `db:"a_enabled"`
		AMaster      string `db:"a_master"`

		BInterfaceID string `db:"b_interface_id"`
		BAssetID     string `db:"b_asset_id"`
		BPort        string `db:"b_port"`
		BForm        string `db:"b_form"`
		BMgmt        bool   `db:"b_is_mgmt"`
		BEnabled     bool   `db:"b_enabled"`
		BMaster      string `db:"b_master"`
	}
	list := placeholders(len(assetIDs))
	args := append([]any{domain.LifecycleActive}, anySlice(assetIDs)...)
	args = append(args, anySlice(assetIDs)...)

	var rows []linkRow
	err := s.read(ctx, &rows, `
		SELECT l.id AS link_id, COALESCE(l.medium, '') AS medium,
		       ai.id AS a_interface_id, ai.asset_id AS a_asset_id, ai.name AS a_port,
		       ai.form_factor AS a_form, ai.is_mgmt AS a_is_mgmt, ai.enabled AS a_enabled,
		       COALESCE(am.name, '') AS a_master,
		       bi.id AS b_interface_id, bi.asset_id AS b_asset_id, bi.name AS b_port,
		       bi.form_factor AS b_form, bi.is_mgmt AS b_is_mgmt, bi.enabled AS b_enabled,
		       COALESCE(bm.name, '') AS b_master
		FROM link l
		JOIN interface ai ON ai.id = l.a_interface_id
		JOIN interface bi ON bi.id = l.b_interface_id
		LEFT JOIN interface am ON am.id = ai.lag_parent_id
		LEFT JOIN interface bm ON bm.id = bi.lag_parent_id
		WHERE l.lifecycle = ?
		  AND ai.asset_id IN (`+list+`)
		  AND bi.asset_id IN (`+list+`)
		ORDER BY l.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("loading neighbourhood cabling: %w", err)
	}

	out := make([]NeighbourLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, NeighbourLink{
			ID:     r.ID,
			Medium: r.Medium,
			A: NeighbourPort{
				InterfaceID: r.AInterfaceID, AssetID: r.AAssetID, Name: r.APort,
				FormFactor: r.AForm, IsMgmt: r.AMgmt, Enabled: r.AEnabled, Master: r.AMaster,
			},
			B: NeighbourPort{
				InterfaceID: r.BInterfaceID, AssetID: r.BAssetID, Name: r.BPort,
				FormFactor: r.BForm, IsMgmt: r.BMgmt, Enabled: r.BEnabled, Master: r.BMaster,
			},
		})
	}
	return out, nil
}

// neighbourhoodWorkloads loads the service layer: what runs on these hosts,
// what it listens on, and which of those services depend on each other.
func (s *SQLStore) neighbourhoodWorkloads(ctx context.Context, assetIDs []string, g *NeighbourhoodGraph) error {
	if len(assetIDs) == 0 {
		return nil
	}

	// One row per placement, carrying its service alongside; the services are
	// de-duplicated below. A separate service query would be a sixth statement
	// to learn nothing this join does not already carry.
	type row struct {
		InstanceID   string `db:"instance_id"`
		HostAssetID  string `db:"host_asset_id"`
		Role         string `db:"role"`
		DesiredState string `db:"desired_state"`

		ServiceID    string `db:"service_id"`
		Code         string `db:"code"`
		Name         string `db:"name"`
		Kind         string `db:"kind"`
		Availability string `db:"availability"`
		Tier         int    `db:"tier"`
		Lifecycle    string `db:"lifecycle"`
	}
	args := append([]any{domain.LifecycleRetired, domain.LifecycleRetired}, anySlice(assetIDs)...)
	var rows []row
	err := s.read(ctx, &rows, `
		SELECT si.id AS instance_id, si.host_asset_id, COALESCE(si.role, '') AS role,
		       si.desired_state,
		       s.id AS service_id, s.code, s.name, s.kind, s.availability, s.tier,
		       s.lifecycle
		FROM service_instance si
		JOIN service s ON s.id = si.service_id
		WHERE si.lifecycle <> ? AND s.lifecycle <> ?
		  AND si.host_asset_id IN (`+placeholders(len(assetIDs))+`)
		ORDER BY s.code, si.id`, args...)
	if err != nil {
		return fmt.Errorf("loading neighbourhood workloads: %w", err)
	}

	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		g.Placements = append(g.Placements, NeighbourPlacement{
			InstanceID: r.InstanceID, ServiceID: r.ServiceID, HostAssetID: r.HostAssetID,
			Role: r.Role, DesiredState: r.DesiredState,
		})
		if seen[r.ServiceID] {
			continue
		}
		seen[r.ServiceID] = true
		g.Services = append(g.Services, NeighbourService{
			ID: r.ServiceID, Code: r.Code, Name: r.Name, Kind: r.Kind,
			Availability: r.Availability, Tier: r.Tier, Lifecycle: r.Lifecycle,
		})
	}
	if len(g.Services) == 0 {
		return nil
	}

	serviceIDs := make([]string, len(g.Services))
	for i, svc := range g.Services {
		serviceIDs[i] = svc.ID
	}
	list := placeholders(len(serviceIDs))

	// LEFT JOIN through ip_address to the interface, because ip_address_id is
	// normally NULL: a service that listens on every address has nothing to
	// pin to, and that is the ordinary case rather than missing data.
	if err := s.read(ctx, &g.Endpoints, `
		SELECT e.id, e.service_id, e.name, e.l4_proto, COALESCE(e.port, 0) AS port,
		       e.bind_scope, e.exposure,
		       COALESCE(ip.addr_text, '') AS addr_text,
		       COALESCE(ip.interface_id, '') AS bound_interface_id,
		       COALESCE(bi.asset_id, '') AS bound_asset_id,
		       COALESCE(bi.name, '') AS bound_port
		FROM endpoint e
		LEFT JOIN ip_address ip ON ip.id = e.ip_address_id
		LEFT JOIN interface bi ON bi.id = ip.interface_id
		WHERE e.service_id IN (`+list+`)
		ORDER BY e.service_id, e.name`, anySlice(serviceIDs)...); err != nil {
		return fmt.Errorf("loading neighbourhood endpoints: %w", err)
	}

	// Both directions are asked for and then narrowed in Go, so an edge with
	// exactly one end in the picture can be COUNTED rather than silently
	// missing. A service whose consumers are all off-screen must not read as a
	// service nothing depends on.
	depArgs := append([]any{domain.LifecycleActive}, anySlice(serviceIDs)...)
	depArgs = append(depArgs, anySlice(serviceIDs)...)
	depArgs = append(depArgs, anySlice(serviceIDs)...)
	var deps []NeighbourDependency
	if err := s.read(ctx, &deps, `
		SELECT d.id, d.consumer_service_id, d.nature,
		       COALESCE(pe.service_id, re.service_id, '') AS provider_service_id,
		       COALESCE(pe.name, re.name, '') AS provider_endpoint,
		       COALESCE(pe.port, re.port, 0) AS provider_port,
		       CASE WHEN d.provider_route_id IS NULL THEN 'endpoint' ELSE 'route' END AS via
		FROM dependency d
		LEFT JOIN endpoint pe ON pe.id = d.provider_endpoint_id
		LEFT JOIN route r ON r.id = d.provider_route_id
		LEFT JOIN endpoint re ON re.id = r.frontend_endpoint_id
		WHERE d.lifecycle = ?
		  AND (d.consumer_service_id IN (`+list+`)
		    OR pe.service_id IN (`+list+`)
		    OR re.service_id IN (`+list+`))
		ORDER BY d.id`, depArgs...); err != nil {
		return fmt.Errorf("loading neighbourhood dependencies: %w", err)
	}
	inside := make(map[string]bool, len(serviceIDs))
	for _, id := range serviceIDs {
		inside[id] = true
	}
	for _, d := range deps {
		if inside[d.ConsumerServiceID] && inside[d.ProviderServiceID] {
			g.Dependencies = append(g.Dependencies, d)
			continue
		}
		g.DependenciesOutside++
	}
	return nil
}

// sortAssetsForBudget puts assets in the order a node budget should cut them:
// nearest first, then by name as BYTES, then by id. Go's string comparison is
// bytewise, which is the point -- see the call sites.
func sortAssetsForBudget(assets []NeighbourAsset) {
	sort.SliceStable(assets, func(i, j int) bool {
		if assets[i].Hop != assets[j].Hop {
			return assets[i].Hop < assets[j].Hop
		}
		if assets[i].Name != assets[j].Name {
			return assets[i].Name < assets[j].Name
		}
		return assets[i].ID < assets[j].ID
	})
}
