package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/gabriel/invctl/internal/domain"
)

// What a project actually consists of, and what it is standing on that it does
// not own.
//
// DERIVED, NEVER MATERIALISED. If a project owns a hypervisor then the guests
// inside it and the services running on them are part of what that project
// costs and what breaks when it breaks -- but nobody declared them, so no row
// says they belong to it. Everything in this file is computed at read time and
// stored nowhere. Writing it back would make a derived fact indistinguishable
// from a declared one in change_log, which is exactly the laundering AUDIT.md
// rule 7 exists to prevent, and it would go stale the moment a VM moved.
//
// DERIVATION FOLLOWS `owns` ONLY. What is inside a hypervisor this project
// merely USES is somebody else's footprint, not this one's. That is the whole
// reason the two relations are asymmetric, and getting it wrong would quietly
// attribute another team's estate -- and later, another team's costs -- to
// whoever happened to declare a `uses` link first.

// ProjectFootprint is what a project consists of: what it declared, and what
// that implies.
type ProjectFootprint struct {
	ProjectID string

	// Declared. Ordered, and separated by relation because the difference is
	// the point: owning is a cost and a responsibility, using is a dependency.
	OwnedAssetIDs   []string
	UsedAssetIDs    []string
	OwnedServiceIDs []string
	UsedServiceIDs  []string

	// ImpliedAssets are assets INSIDE an owned asset -- guests of an owned
	// hypervisor, hardware in an owned rack. Never includes the owned assets
	// themselves.
	ImpliedAssets []NeighbourAsset
	// ImpliedServices run on an owned or implied asset without being declared
	// as the project's. A service the project owns outright is not repeated
	// here; this is the set somebody would be surprised by.
	ImpliedServices []NeighbourService

	// Conflicts are implied assets that another project owns DIRECTLY. A VM
	// inside our hypervisor that the platform team owns is not ours to count,
	// and silently absorbing it would double-count it the day cost lands.
	Conflicts []FootprintConflict

	// AssetsElided is how many implied assets the node budget cut. Reported
	// rather than silently dropped, like every other cut in this codebase.
	AssetsElided int
}

// FootprintConflict is an implied asset with a declared owner elsewhere.
type FootprintConflict struct {
	AssetID     string `db:"asset_id"`
	AssetName   string `db:"asset_name"`
	ProjectID   string `db:"project_id"`
	ProjectCode string `db:"project_code"`
	ProjectName string `db:"project_name"`
}

// AllAssetIDs is the project's whole asset surface: owned, used and implied.
// It is what a map of the project draws.
func (f *ProjectFootprint) AllAssetIDs() []string {
	size := len(f.OwnedAssetIDs) + len(f.UsedAssetIDs) + len(f.ImpliedAssets)
	seen := make(map[string]bool, size)
	out := make([]string, 0, size)
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range f.OwnedAssetIDs {
		add(id)
	}
	for _, id := range f.UsedAssetIDs {
		add(id)
	}
	for _, a := range f.ImpliedAssets {
		add(a.ID)
	}
	sort.Strings(out)
	return out
}

// responsibleServiceIDs is the set whose dependencies this project is
// answerable for: what it owns, plus what runs on what it owns.
//
// Deliberately NOT including `uses` services. If a project merely uses shared
// PostgreSQL it must not inherit PostgreSQL's entire upstream dependency list
// as its own hidden dependencies -- that is a panel nobody reads by the second
// week, and the same failure as a reachability report that lists everything so
// nothing stands out.
func (f *ProjectFootprint) responsibleServiceIDs() []string {
	size := len(f.OwnedServiceIDs) + len(f.ImpliedServices)
	seen := make(map[string]bool, size)
	out := make([]string, 0, size)
	for _, id := range f.OwnedServiceIDs {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, svc := range f.ImpliedServices {
		if !seen[svc.ID] {
			seen[svc.ID] = true
			out = append(out, svc.ID)
		}
	}
	sort.Strings(out)
	return out
}

// ProjectFootprint computes what a project consists of.
func (s *SQLStore) ProjectFootprint(ctx context.Context, projectID string, maxAssets int) (*ProjectFootprint, error) {
	if maxAssets <= 0 {
		maxAssets = NeighbourhoodDefaultMaxAssets
	}
	f := &ProjectFootprint{ProjectID: projectID}

	assets, err := s.ListProjectAssets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		if a.Relation == domain.ProjectOwns {
			f.OwnedAssetIDs = append(f.OwnedAssetIDs, a.AssetID)
			continue
		}
		f.UsedAssetIDs = append(f.UsedAssetIDs, a.AssetID)
	}
	services, err := s.ListProjectServices(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, svc := range services {
		if svc.Relation == domain.ProjectOwns {
			f.OwnedServiceIDs = append(f.OwnedServiceIDs, svc.ServiceID)
			continue
		}
		f.UsedServiceIDs = append(f.UsedServiceIDs, svc.ServiceID)
	}

	if len(f.OwnedAssetIDs) > 0 {
		if err := s.impliedAssets(ctx, f, maxAssets); err != nil {
			return nil, err
		}
	}
	if err := s.impliedServices(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

// impliedAssets walks containment downward from every owned asset.
//
// depth > 0 excludes the owned assets themselves, which are already declared.
// MIN(depth) collapses the several paths a descendant can be reached by, the
// same way neighbourhoodAssets collapses hops -- an asset inside two owned
// racks is one asset, not two.
func (s *SQLStore) impliedAssets(ctx context.Context, f *ProjectFootprint, maxAssets int) error {
	list := placeholders(len(f.OwnedAssetIDs))
	args := append(anySlice(f.OwnedAssetIDs), domain.LifecycleRetired)

	var rows []NeighbourAsset
	err := s.read(ctx, &rows, `
		SELECT c.descendant_id AS id, MIN(c.depth) AS hop,
		       a.kind, a.name, a.lifecycle, COALESCE(a.parent_id, '') AS parent_id
		FROM asset_closure c
		JOIN asset a ON a.id = c.descendant_id
		WHERE c.ancestor_id IN (`+list+`)
		  AND c.depth > 0
		  AND a.lifecycle <> ?
		GROUP BY c.descendant_id, a.kind, a.name, a.lifecycle, a.parent_id
		ORDER BY a.name, c.descendant_id`, args...)
	if err != nil {
		return fmt.Errorf("deriving the footprint of %s: %w", f.ProjectID, err)
	}

	// An asset can be owned directly AND sit inside another owned asset. It is
	// declared, so it is not "implied" -- listing it twice would make the
	// footprint look bigger than the estate.
	owned := make(map[string]bool, len(f.OwnedAssetIDs))
	for _, id := range f.OwnedAssetIDs {
		owned[id] = true
	}
	kept := rows[:0]
	for _, r := range rows {
		if !owned[r.ID] {
			kept = append(kept, r)
		}
	}
	rows = kept

	if len(rows) > maxAssets {
		// Sorted in Go before the cut, so which assets survive does not depend
		// on the server's collation. See sortAssetsForBudget.
		sortAssetsForBudget(rows)
		f.AssetsElided = len(rows) - maxAssets
		rows = rows[:maxAssets]
	}
	f.ImpliedAssets = rows

	return s.footprintConflicts(ctx, f)
}

// footprintConflicts finds implied assets another project owns outright.
func (s *SQLStore) footprintConflicts(ctx context.Context, f *ProjectFootprint) error {
	if len(f.ImpliedAssets) == 0 {
		return nil
	}
	ids := make([]string, len(f.ImpliedAssets))
	for i, a := range f.ImpliedAssets {
		ids[i] = a.ID
	}
	args := append(anySlice(ids),
		domain.ProjectOwns, domain.LifecycleActive, domain.LifecycleRetired, f.ProjectID)

	err := s.read(ctx, &f.Conflicts, `
		SELECT pa.asset_id, a.name AS asset_name,
		       p.id AS project_id, p.code AS project_code, p.name AS project_name
		FROM project_asset pa
		JOIN asset a   ON a.id = pa.asset_id
		JOIN project p ON p.id = pa.project_id
		WHERE pa.asset_id IN (`+placeholders(len(ids))+`)
		  AND pa.relation = ? AND pa.lifecycle = ?
		  AND p.lifecycle <> ? AND pa.project_id <> ?
		ORDER BY a.name, pa.asset_id`, args...)
	if err != nil {
		return fmt.Errorf("checking footprint conflicts: %w", err)
	}
	return nil
}

// impliedServices finds services running on owned or implied assets that the
// project has not declared.
func (s *SQLStore) impliedServices(ctx context.Context, f *ProjectFootprint) error {
	hosts := make([]string, 0, len(f.OwnedAssetIDs)+len(f.ImpliedAssets))
	hosts = append(hosts, f.OwnedAssetIDs...)
	for _, a := range f.ImpliedAssets {
		hosts = append(hosts, a.ID)
	}
	if len(hosts) == 0 {
		return nil
	}

	// The neighbourhood's own workload loader, unchanged. Rewriting it here
	// would be a second query shape to keep portable for no new fact.
	var g NeighbourhoodGraph
	if err := s.neighbourhoodWorkloads(ctx, hosts, &g); err != nil {
		return fmt.Errorf("deriving the services of %s: %w", f.ProjectID, err)
	}

	declared := make(map[string]bool, len(f.OwnedServiceIDs)+len(f.UsedServiceIDs))
	for _, id := range f.OwnedServiceIDs {
		declared[id] = true
	}
	for _, id := range f.UsedServiceIDs {
		declared[id] = true
	}
	for _, svc := range g.Services {
		if !declared[svc.ID] {
			f.ImpliedServices = append(f.ImpliedServices, svc)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// What the project leans on that it does not own.

// ExternalDependency is one declared edge leaving the project's own services.
type ExternalDependency struct {
	DependencyID     string `db:"dependency_id"`
	Nature           string `db:"nature"`
	Via              string `db:"via"`
	ConsumerID       string `db:"consumer_id"`
	ConsumerCode     string `db:"consumer_code"`
	ConsumerName     string `db:"consumer_name"`
	ProviderID       string `db:"provider_id"`
	ProviderCode     string `db:"provider_code"`
	ProviderName     string `db:"provider_name"`
	ProviderTeam     string `db:"provider_team"`
	ProviderEndpoint string `db:"provider_endpoint"`

	// OwningProject names the project that owns the provider, when one does.
	OwningProjectID   string
	OwningProjectCode string
	OwningProjectName string
}

// ProjectDependencyReport is the governance answer: what this project needs
// that somebody else is responsible for.
type ProjectDependencyReport struct {
	// External: the provider belongs to another project. Actionable -- there
	// is a team to talk to and an owner to hold to an SLA.
	External []ExternalDependency
	// Unowned: nobody has claimed the provider at all. Worse than external,
	// because there is no one to ask.
	Unowned []ExternalDependency
	// Shared: the project already declared a `uses` link to the provider. Known
	// and accounted for, so it is reported separately rather than as a finding
	// -- a panel that cries about things you already wrote down is a panel
	// nobody reads twice.
	Shared []ExternalDependency
}

// Total is how many edges leave the project's own services.
func (r *ProjectDependencyReport) Total() int {
	return len(r.External) + len(r.Unowned) + len(r.Shared)
}

// ProjectExternalDependencies classifies every declared dependency whose
// consumer is one of the project's own services.
func (s *SQLStore) ProjectExternalDependencies(ctx context.Context, f *ProjectFootprint) (*ProjectDependencyReport, error) {
	report := &ProjectDependencyReport{}
	consumers := f.responsibleServiceIDs()
	if len(consumers) == 0 {
		return report, nil
	}

	args := append([]any{domain.LifecycleActive}, anySlice(consumers)...)
	var edges []ExternalDependency
	err := s.read(ctx, &edges, `
		SELECT d.id AS dependency_id, d.nature,
		       CASE WHEN d.provider_route_id IS NULL THEN 'endpoint' ELSE 'route' END AS via,
		       d.consumer_service_id AS consumer_id, cs.code AS consumer_code, cs.name AS consumer_name,
		       COALESCE(pes.id, res.id, '')         AS provider_id,
		       COALESCE(pes.code, res.code, '')     AS provider_code,
		       COALESCE(pes.name, res.name, '')     AS provider_name,
		       COALESCE(pes.owner_team, res.owner_team, '') AS provider_team,
		       COALESCE(pe.name, re.name, '')       AS provider_endpoint
		FROM dependency d
		JOIN service cs       ON cs.id  = d.consumer_service_id
		LEFT JOIN endpoint pe ON pe.id  = d.provider_endpoint_id
		LEFT JOIN service pes ON pes.id = pe.service_id
		LEFT JOIN route r     ON r.id   = d.provider_route_id
		LEFT JOIN endpoint re ON re.id  = r.frontend_endpoint_id
		LEFT JOIN service res ON res.id = re.service_id
		WHERE d.lifecycle = ?
		  AND d.consumer_service_id IN (`+placeholders(len(consumers))+`)
		ORDER BY d.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("loading the dependencies of %s: %w", f.ProjectID, err)
	}

	// Which project owns each provider. One query, then the classification
	// happens in Go -- the same shape neighbourhoodWorkloads uses, and what
	// AUDIT.md means by folding in Go rather than in SQL.
	providerIDs := make([]string, 0, len(edges))
	seen := map[string]bool{}
	for _, e := range edges {
		if e.ProviderID != "" && !seen[e.ProviderID] {
			seen[e.ProviderID] = true
			providerIDs = append(providerIDs, e.ProviderID)
		}
	}
	owners, err := s.ownersOfServices(ctx, providerIDs)
	if err != nil {
		return nil, err
	}

	// A provider inside the project's own responsibility is not a dependency
	// on anybody else -- it is the project depending on itself.
	internal := make(map[string]bool, len(consumers))
	for _, id := range consumers {
		internal[id] = true
	}
	shared := make(map[string]bool, len(f.UsedServiceIDs))
	for _, id := range f.UsedServiceIDs {
		shared[id] = true
	}

	for _, e := range edges {
		if e.ProviderID == "" || internal[e.ProviderID] {
			continue
		}
		if owner, ok := owners[e.ProviderID]; ok {
			e.OwningProjectID, e.OwningProjectCode, e.OwningProjectName =
				owner.ProjectID, owner.ProjectCode, owner.ProjectName
		}
		switch {
		case shared[e.ProviderID]:
			report.Shared = append(report.Shared, e)
		case e.OwningProjectID != "":
			report.External = append(report.External, e)
		default:
			report.Unowned = append(report.Unowned, e)
		}
	}
	return report, nil
}

// ownersOfServices maps service id to the project that owns it, if any.
func (s *SQLStore) ownersOfServices(ctx context.Context, serviceIDs []string) (map[string]ProjectLinkRow, error) {
	if len(serviceIDs) == 0 {
		return map[string]ProjectLinkRow{}, nil
	}
	args := append(anySlice(serviceIDs),
		domain.ProjectOwns, domain.LifecycleActive, domain.LifecycleRetired)

	var rows []ProjectLinkRow
	err := s.read(ctx, &rows, `
		SELECT ps.service_id AS entity_id, ps.project_id, ps.relation, p.code, p.name
		FROM project_service ps
		JOIN project p ON p.id = ps.project_id
		WHERE ps.service_id IN (`+placeholders(len(serviceIDs))+`)
		  AND ps.relation = ? AND ps.lifecycle = ? AND p.lifecycle <> ?
		ORDER BY ps.service_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("resolving service owners: %w", err)
	}
	// At most one owner per service, by the partial unique index, so the last
	// write wins is not a question that can arise.
	out := make(map[string]ProjectLinkRow, len(rows))
	for _, r := range rows {
		out[r.EntityID] = r
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The picture.

// ProjectMap draws the project's asset surface, in the same three bands and
// through the same pipeline as the environment map.
//
// It is a MEMBERSHIP picture, not a walk: what is drawn is what the project
// declared plus what that implies, and links are drawn only when both ends are
// in that set. A cable to somebody else's switch is not part of this project's
// picture even though it is part of its reality -- the neighbourhood is the
// page that crosses that boundary.
func (s *SQLStore) ProjectMap(ctx context.Context, projectID string, maxAssets int) (*NeighbourhoodGraph, error) {
	f, err := s.ProjectFootprint(ctx, projectID, maxAssets)
	if err != nil {
		return nil, err
	}
	g := &NeighbourhoodGraph{AssetsElided: f.AssetsElided}

	ids := f.AllAssetIDs()
	if len(ids) == 0 {
		return g, nil
	}
	if err := s.read(ctx, &g.Assets, `
		SELECT a.id, 0 AS hop, a.kind, a.name, a.lifecycle,
		       COALESCE(a.parent_id, '') AS parent_id
		FROM asset a
		WHERE a.id IN (`+placeholders(len(ids))+`) AND a.lifecycle <> ?
		ORDER BY a.name, a.id`,
		append(anySlice(ids), domain.LifecycleRetired)...); err != nil {
		return nil, fmt.Errorf("loading the assets of project %s: %w", projectID, err)
	}
	if len(g.Assets) == 0 {
		return g, nil
	}

	present := make([]string, len(g.Assets))
	for i, a := range g.Assets {
		present[i] = a.ID
	}
	if g.Links, err = s.neighbourhoodLinks(ctx, present); err != nil {
		return nil, err
	}
	if err := s.neighbourhoodWorkloads(ctx, present, g); err != nil {
		return nil, err
	}
	return g, nil
}
