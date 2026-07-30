// Reachability (docs/reachability-design.md, M3).
//
// Phase 0, below, is linear and runs once, before phaseCapacity -- no
// iteration, no recursion, no traversal, so it provably cannot affect the
// fixed point's termination argument (engine.go's package doc). It enters the
// engine at three disjoint seams: isolation into instance liveness (Analyse),
// pairwise partition into providerHealth/routeStatuses (phasePropagate), and
// anchor loss into a reported-only finding, appended after the fixed point.
//
// A nil *NetGraph -- equivalently, an estate that has declared zero rows in
// every net_* table -- makes every function in this file a no-op. That is
// the compositional guarantee the design calls out: no topology data means
// no claim, never "everything is unreachable".

package impact

import (
	"fmt"
	"sort"

	"github.com/gabriel/invctl/internal/domain"
)

// ---------------------------------------------------------------------------
// The net graph -- loaded once by store.LoadGraph, the same shape discipline
// Graph itself uses.
// ---------------------------------------------------------------------------

// NetGroupInfo is a forwarder-graph vertex, reduced to what the algorithm
// needs. Availability/MinHealthy/FailoverMode feed domain.AvailabilityPolicy
// directly -- the same evaluator a service uses.
type NetGroupInfo struct {
	ID           string
	Code         string
	Availability string
	MinHealthy   *int
	FailoverMode *string
}

// NetGroupMemberInfo is one chassis of a group. AssetLifecycle is the
// *asset's* lifecycle (not the membership row's, which the loader has
// already filtered to 'active'): a planned or retired chassis is not
// capacity, matching net_group_member's own installed-members rule.
type NetGroupMemberInfo struct {
	AssetID        string
	Role           string
	AssetLifecycle string
}

// NetUplinkInfo is a directed group-to-group edge on one plane. Several rows
// from one group are alternate paths, never both-required.
type NetUplinkInfo struct {
	GroupID         string
	UpstreamGroupID string
	Plane           string
}

// NetAttachmentInfo is a host's attachment to a group on one plane.
type NetAttachmentInfo struct {
	ID      string
	AssetID string
	GroupID string
	Plane   string
}

// NetAttachmentMemberInfo names one pinned chassis of an attachment -- which
// chassis the cable actually lands on, not merely which group.
type NetAttachmentMemberInfo struct {
	AssetID        string
	AssetLifecycle string
}

// NetAnchorInfo is where a reachability scope enters the estate.
type NetAnchorInfo struct {
	Code          string
	Scope         string
	GroupID       string
	Plane         string
	EnvironmentID *string
}

// ClosureRow is one row of asset_closure restricted to attached ancestors:
// for an attached asset (itself included, at depth 0) every descendant and
// how far below it sits. This is what resolves "a VM has no attachment of
// its own, so it inherits its hypervisor's" without a parent_id walk.
type ClosureRow struct {
	AncestorID   string
	DescendantID string
	Depth        int
}

// NetGraph is the whole reachability picture, loaded once per analysis.
type NetGraph struct {
	Groups            map[string]NetGroupInfo
	Members           map[string][]NetGroupMemberInfo // by group id
	Uplinks           []NetUplinkInfo
	Attachments       []NetAttachmentInfo
	AttachmentMembers map[string][]NetAttachmentMemberInfo // by attachment id
	Anchors           []NetAnchorInfo
	Closure           []ClosureRow
	// AssetNames resolves an asset id to something a person can act on.
	// Isolation is reported by id, and an id is the one thing an operator
	// cannot look up on a rack at three in the morning. Empty is tolerated --
	// the report falls back to the id rather than omitting the row.
	AssetNames map[string]AssetLabel
}

// AssetLabel is the human-facing identity of an asset. Assets have no code
// column -- name is the identifier operators use ("hv-01", "sw-core-2") -- and
// kind is carried alongside so a report can say what kind of thing was cut off
// without a second lookup.
type AssetLabel struct {
	Name string
	Kind string
}

// NewNetGraph returns an empty net graph with its maps ready.
func NewNetGraph() *NetGraph {
	return &NetGraph{
		Groups:            map[string]NetGroupInfo{},
		Members:           map[string][]NetGroupMemberInfo{},
		AttachmentMembers: map[string][]NetAttachmentMemberInfo{},
		AssetNames:        map[string]AssetLabel{},
	}
}

// IsEmpty reports whether the net graph carries no declared topology at all.
// LoadGraph uses this to decide whether Inputs.Net should be nil.
func (n *NetGraph) IsEmpty() bool {
	return n == nil || (len(n.Groups) == 0 && len(n.Attachments) == 0 && len(n.Anchors) == 0)
}

// ---------------------------------------------------------------------------
// Result additions
// ---------------------------------------------------------------------------

// Cause names which of the three disjoint channels produced a ServiceImpact.
type Cause string

const (
	CauseCapacity     Cause = "capacity"
	CauseReachability Cause = "reachability"
	CauseExposure     Cause = "exposure"
	CauseDependency   Cause = "dependency"
)

// AssetIsolation names one asset cut off from its declared network on one
// plane -- reported, and (for the data plane only) already folded into
// instance liveness by the time a report reaches here.
type AssetIsolation struct {
	AssetID       string
	AssetName     string
	AssetKind     string
	Plane         string
	BlockingGroup string
}

// Label is what the report should print for this asset: its name when one is
// known, the raw id when it is not. Never blank, so a row is always readable
// even if the label lookup ever comes back empty.
func (a AssetIsolation) Label() string {
	if a.AssetName != "" {
		return a.AssetName
	}
	return a.AssetID
}

// EdgePartition names one dependency edge whose reachability, not its
// provider's own status, degraded or cut it.
type EdgePartition struct {
	DependencyID    string
	ConsumerService string
	ProviderName    string
	Reach           string // "degraded" or "down"
}

// EndpointReach is a running service with no path to a declared anchor for
// its endpoint's exposure scope.
type EndpointReach struct {
	ServiceID     string
	Code          string
	Name          string
	EndpointID    string
	EndpointName  string
	Scope         string
	Status        domain.Status
	BlockingGroup string
}

// GroupFinding is a forwarder group that survived a loss but has no
// redundancy left.
type GroupFinding struct {
	GroupCode string
	Message   string
}

// ReachCoverage reports how much of the estate has declared topology at all,
// distinct from what it says about any particular outage.
type ReachCoverage struct {
	// TopologyDeclared reports that the estate has declared some topology, as
	// distinct from every count below happening to be zero. Without it a
	// topology attached only to assets that host no services reads as "no
	// network topology declared", which is the opposite of the truth.
	TopologyDeclared            bool
	Modelled                    int
	Inherited                   int
	Unmodelled                  int
	GroupsWithoutUplinkOrAnchor int
	NoAnchorForScope            map[string]int
	ClusterUnresolved           int
}

// ---------------------------------------------------------------------------
// R1 -- group status
// ---------------------------------------------------------------------------

// installedLifecycle mirrors the R1 rule verbatim: 'planned' is deliberately
// not installed (cabling for a build that is not yet racked must not be
// reported as redundancy that physically exists), and 'retired' obviously
// is not capacity.
func installedLifecycle(lc string) bool {
	return lc == domain.LifecycleActive || lc == domain.LifecycleMaintenance || lc == domain.LifecycleDeprecated
}

// groupStatuses computes every group's status from its installed members.
// EvaluateCapacity returns StatusOK for zero instances, which would make an
// emptied group a healthy ingress -- guarded here, next to the call.
func groupStatuses(net *NetGraph, down map[string]bool) map[string]domain.Status {
	out := make(map[string]domain.Status, len(net.Groups))
	for id, g := range net.Groups {
		installed := make([]domain.InstanceHealth, 0, len(net.Members[id]))
		for _, m := range net.Members[id] {
			if !installedLifecycle(m.AssetLifecycle) {
				continue
			}
			installed = append(installed, domain.InstanceHealth{ID: m.AssetID, Role: m.Role, Alive: !down[m.AssetID]})
		}
		if len(installed) == 0 {
			out[id] = domain.StatusDown
			continue
		}
		out[id] = domain.AvailabilityPolicy{
			Availability: g.Availability, MinHealthy: g.MinHealthy, FailoverMode: g.FailoverMode,
		}.Evaluate(installed)
	}
	return out
}

// ---------------------------------------------------------------------------
// R2 -- two union-find passes per plane
// ---------------------------------------------------------------------------

// unionFind is a plain union-by-deterministic-tiebreak structure: no ranking
// by size, because the vertex counts here are tens to low hundreds and a
// deterministic representative matters far more than near-optimal height.
type unionFind struct{ parent map[string]string }

func newUnionFind(nodes []string) *unionFind {
	uf := &unionFind{parent: make(map[string]string, len(nodes))}
	for _, n := range nodes {
		uf.parent[n] = n
	}
	return uf
}

func (uf *unionFind) find(x string) string {
	root := x
	for uf.parent[root] != root {
		root = uf.parent[root]
	}
	for uf.parent[x] != root {
		uf.parent[x], x = root, uf.parent[x]
	}
	return root
}

func (uf *unionFind) union(a, b string) {
	ra, rb := uf.find(a), uf.find(b)
	if ra == rb {
		return
	}
	// Deterministic regardless of insertion order: the lexicographically
	// smaller representative wins, so the same graph always yields the same
	// component ids across repeated runs.
	if ra < rb {
		uf.parent[rb] = ra
	} else {
		uf.parent[ra] = rb
	}
}

// components unions every edge on one plane whose endpoints both satisfy
// allow, and returns each allowed vertex's representative.
func components(net *NetGraph, status map[string]domain.Status, plane string, allow func(domain.Status) bool) map[string]string {
	nodes := make([]string, 0, len(net.Groups))
	inSet := make(map[string]bool, len(net.Groups))
	for id, st := range status {
		if allow(st) {
			nodes = append(nodes, id)
			inSet[id] = true
		}
	}
	sort.Strings(nodes)
	uf := newUnionFind(nodes)

	edges := make([]NetUplinkInfo, 0, len(net.Uplinks))
	for _, u := range net.Uplinks {
		if u.Plane != plane {
			continue
		}
		if !inSet[u.GroupID] || !inSet[u.UpstreamGroupID] {
			continue
		}
		edges = append(edges, u)
	}
	// Union edges in sorted order -- stable component representatives
	// across runs, which is what determinism actually requires: not that
	// union-find never re-parents, but that the same input always produces
	// the same partition of vertices into components.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].GroupID != edges[j].GroupID {
			return edges[i].GroupID < edges[j].GroupID
		}
		return edges[i].UpstreamGroupID < edges[j].UpstreamGroupID
	})
	for _, e := range edges {
		uf.union(e.GroupID, e.UpstreamGroupID)
	}

	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		out[n] = uf.find(n)
	}
	return out
}

// ---------------------------------------------------------------------------
// R3/R4 -- attachment resolution, isolation and pairwise reach
// ---------------------------------------------------------------------------

type ancestorDepth struct {
	AssetID string
	Depth   int
}

type netKey struct {
	AssetID string
	Plane   string
}

// reachModel is the whole reachability answer for one Analyse run: group
// status, both union-find passes per plane, and the attachment inheritance
// index, all precomputed once so R3/R4 queries are cheap lookups.
type reachModel struct {
	net  *NetGraph
	down map[string]bool

	groupStatus     map[string]domain.Status
	compOptimistic  map[string]map[string]string // plane -> group id -> representative
	compPessimistic map[string]map[string]string

	attachmentsByAssetPlane map[string]map[string][]int // asset id -> plane -> attachment indices
	ancestorsByDescendant   map[string][]ancestorDepth  // sorted ascending by depth

	netOfCache    map[netKey][]string
	modelledCache map[netKey]bool
}

// buildReachModel returns nil when net is nil or declares nothing, which is
// what makes every call site's `net == nil` check sufficient on its own.
func buildReachModel(net *NetGraph, down map[string]bool) *reachModel {
	if net.IsEmpty() {
		return nil
	}
	m := &reachModel{net: net, down: down}
	m.groupStatus = groupStatuses(net, down)

	planes := []string{domain.PlaneData, domain.PlaneMgmt, domain.PlaneStorage}
	m.compOptimistic = make(map[string]map[string]string, len(planes))
	m.compPessimistic = make(map[string]map[string]string, len(planes))
	for _, p := range planes {
		m.compOptimistic[p] = components(net, m.groupStatus, p, func(s domain.Status) bool { return s != domain.StatusDown })
		m.compPessimistic[p] = components(net, m.groupStatus, p, func(s domain.Status) bool { return s == domain.StatusOK })
	}

	m.attachmentsByAssetPlane = map[string]map[string][]int{}
	for i, a := range net.Attachments {
		byPlane, ok := m.attachmentsByAssetPlane[a.AssetID]
		if !ok {
			byPlane = map[string][]int{}
			m.attachmentsByAssetPlane[a.AssetID] = byPlane
		}
		byPlane[a.Plane] = append(byPlane[a.Plane], i)
	}

	m.ancestorsByDescendant = map[string][]ancestorDepth{}
	seen := map[[2]string]bool{}
	for _, c := range net.Closure {
		key := [2]string{c.DescendantID, c.AncestorID}
		if seen[key] {
			continue
		}
		seen[key] = true
		m.ancestorsByDescendant[c.DescendantID] = append(m.ancestorsByDescendant[c.DescendantID], ancestorDepth{AssetID: c.AncestorID, Depth: c.Depth})
	}
	for k := range m.ancestorsByDescendant {
		rows := m.ancestorsByDescendant[k]
		sort.Slice(rows, func(i, j int) bool { return rows[i].Depth < rows[j].Depth })
	}

	m.netOfCache = map[netKey][]string{}
	m.modelledCache = map[netKey]bool{}
	return m
}

// liveAttachment implements R3's live(a): the group must not be down, and if
// the attachment is pinned to specific chassis, at least one installed one
// must survive. An empty pin set means "attached to the group as a whole".
func (m *reachModel) liveAttachment(idx int) bool {
	a := m.net.Attachments[idx]
	if m.groupStatus[a.GroupID] == domain.StatusDown {
		return false
	}
	if _, ok := m.net.Groups[a.GroupID]; !ok {
		return false
	}
	members := m.net.AttachmentMembers[a.ID]
	if len(members) == 0 {
		return true
	}
	for _, mem := range members {
		if installedLifecycle(mem.AssetLifecycle) && !m.down[mem.AssetID] {
			return true
		}
	}
	return false
}

// effectiveAttachments resolves nearest-ancestor-wins for (assetID, plane):
// the closest ancestor (including the asset itself at depth 0) that has any
// active attachment on that plane shadows everything further out, whether or
// not that attachment is currently live.
func (m *reachModel) effectiveAttachments(assetID, plane string) ([]int, bool) {
	candidates := m.ancestorsByDescendant[assetID]
	if len(candidates) == 0 {
		// No closure row recorded reaching this asset (e.g. it is the root
		// asked about directly and closure loading only scoped to attached
		// subtrees) -- fall back to the asset's own attachments, if any.
		if byPlane, ok := m.attachmentsByAssetPlane[assetID]; ok {
			if idxs, ok := byPlane[plane]; ok && len(idxs) > 0 {
				return idxs, true
			}
		}
		return nil, false
	}
	for _, cand := range candidates {
		byPlane, ok := m.attachmentsByAssetPlane[cand.AssetID]
		if !ok {
			continue
		}
		idxs, ok := byPlane[plane]
		if !ok || len(idxs) == 0 {
			continue
		}
		return idxs, true
	}
	return nil, false
}

// modelled reports whether x has any effective attachment on plane -- the
// whole graceful-degradation guarantee. Absence of topology data is not
// evidence of disconnection.
func (m *reachModel) modelled(assetID, plane string) bool {
	if m == nil {
		return false
	}
	key := netKey{assetID, plane}
	if v, ok := m.modelledCache[key]; ok {
		return v
	}
	_, ok := m.effectiveAttachments(assetID, plane)
	m.modelledCache[key] = ok
	return ok
}

// netOf returns the set of groups x is live-attached to on plane, deduped.
func (m *reachModel) netOf(assetID, plane string) []string {
	if m == nil {
		return nil
	}
	key := netKey{assetID, plane}
	if v, ok := m.netOfCache[key]; ok {
		return v
	}
	idxs, ok := m.effectiveAttachments(assetID, plane)
	var groups []string
	if ok {
		seen := map[string]bool{}
		for _, idx := range idxs {
			if !m.liveAttachment(idx) {
				continue
			}
			g := m.net.Attachments[idx].GroupID
			if !seen[g] {
				seen[g] = true
				groups = append(groups, g)
			}
		}
	}
	m.netOfCache[key] = groups
	return groups
}

// isolated reports whether x is modelled but has no surviving path.
func (m *reachModel) isolated(assetID, plane string) bool {
	if m == nil {
		return false
	}
	return m.modelled(assetID, plane) && len(m.netOf(assetID, plane)) == 0
}

// reach is R4: three-valued pairwise reachability plus a known flag. Unknown
// is not a status value and has no position in the lattice -- it is this
// separate boolean, so a partially-cabled estate never has to answer "where
// does Unknown sit in min/max".
func (m *reachModel) reach(x, y, plane string) (domain.Status, bool) {
	if m == nil {
		return "", false
	}
	if !m.modelled(x, plane) || !m.modelled(y, plane) {
		return "", false
	}
	if m.isolated(x, plane) || m.isolated(y, plane) {
		return domain.StatusDown, true
	}
	gx, gy := m.netOf(x, plane), m.netOf(y, plane)
	compB := m.compPessimistic[plane]
	for _, p := range gx {
		for _, q := range gy {
			if compB[p] != "" && compB[p] == compB[q] {
				return domain.StatusOK, true
			}
		}
	}
	compA := m.compOptimistic[plane]
	for _, p := range gx {
		for _, q := range gy {
			if compA[p] != "" && compA[p] == compA[q] {
				return domain.StatusDegraded, true
			}
		}
	}
	return domain.StatusDown, true
}

// reachGroup answers "can this asset still reach this forwarder group?", which
// is the question an anchor asks and the one reach() cannot answer.
//
// reach() resolves BOTH of its arguments as asset ids, through modelled() and
// netOf(), which index net_attachment.asset_id and asset_closure.descendant_id.
// A net_group id is neither, so passing one produced ok=false every single time
// and the whole exposure channel silently reported nothing -- four declared
// anchors consumed by no code at all. The asymmetry is real and needs its own
// function rather than a cast: one end is an asset that attaches to groups, the
// other IS a group.
func (m *reachModel) reachGroup(assetID, groupID, plane string) (domain.Status, bool) {
	if m == nil {
		return "", false
	}
	if !m.modelled(assetID, plane) {
		return "", false
	}
	if _, ok := m.net.Groups[groupID]; !ok {
		return "", false
	}
	// An anchor whose own group has failed is not an anchor any more, and an
	// asset cut off from every group it attaches to reaches nothing.
	if m.isolated(assetID, plane) || m.groupStatus[groupID] == domain.StatusDown {
		return domain.StatusDown, true
	}
	from := m.netOf(assetID, plane)
	compB := m.compPessimistic[plane]
	for _, g := range from {
		if compB[g] != "" && compB[g] == compB[groupID] {
			return domain.StatusOK, true
		}
	}
	// Optimistic includes degraded-but-forwarding groups, so reaching the
	// anchor only in this pass is exactly "a path exists, but only once
	// somebody promotes the standby".
	compA := m.compOptimistic[plane]
	for _, g := range from {
		if compA[g] != "" && compA[g] == compA[groupID] {
			return domain.StatusDegraded, true
		}
	}
	return domain.StatusDown, true
}

func (m *reachModel) groupCode(groupID string) string {
	if m == nil {
		return groupID
	}
	if g, ok := m.net.Groups[groupID]; ok {
		return g.Code
	}
	return groupID
}

// ---------------------------------------------------------------------------
// Seam 1 -- needs-network
// ---------------------------------------------------------------------------

// isLocalBind delegates to the domain rule rather than restating it. The
// neighbourhood diagram asks the same question -- "does this socket's traffic
// leave the box?" -- and a second copy here would let the picture and the
// engine drift into contradicting each other about the same endpoint.
func isLocalBind(scope string) bool { return domain.IsLocalBind(scope) }

// providerEndpointForDep resolves whatever a dependency points at down to the
// endpoint whose bind scope actually matters: the frontend endpoint of a
// route, or the endpoint directly.
func providerEndpointForDep(g *Graph, dep domain.Dependency) (Endpoint, bool) {
	if dep.ProviderEndpointID != nil {
		ep, ok := g.Endpoints[*dep.ProviderEndpointID]
		return ep, ok
	}
	if dep.ProviderRouteID != nil {
		if r, ok := g.Routes[*dep.ProviderRouteID]; ok {
			ep, ok := g.Endpoints[r.FrontendEndpointID]
			return ep, ok
		}
	}
	return Endpoint{}, false
}

// computeNeedsNet decides, once per service, whether losing its network
// makes any difference at all. The len(endpoints) > 0 guard is what stops a
// pure consumer with no endpoints (e.g. backup-agent) being declared immune:
// "every endpoint is local" is vacuously true for it otherwise.
func computeNeedsNet(g *Graph) map[string]bool {
	endpointsByService := map[string][]Endpoint{}
	for _, ep := range g.Endpoints {
		endpointsByService[ep.ServiceID] = append(endpointsByService[ep.ServiceID], ep)
	}
	depsByConsumer := map[string][]domain.Dependency{}
	for _, dep := range g.Deps {
		if dep.Lifecycle == domain.LifecycleRetired {
			continue
		}
		depsByConsumer[dep.ConsumerServiceID] = append(depsByConsumer[dep.ConsumerServiceID], dep)
	}

	needsNet := make(map[string]bool, len(g.Services))
	for svcID := range g.Services {
		eps := endpointsByService[svcID]
		allLocal := len(eps) > 0
		for _, ep := range eps {
			if !isLocalBind(ep.BindScope) {
				allLocal = false
				break
			}
		}
		if allLocal {
			for _, dep := range depsByConsumer[svcID] {
				pep, ok := providerEndpointForDep(g, dep)
				if !ok || !isLocalBind(pep.BindScope) {
					allLocal = false
					break
				}
			}
		}
		needsNet[svcID] = !allLocal
	}
	return needsNet
}

// ---------------------------------------------------------------------------
// Seam 2 -- pairwise partition pre-pass
// ---------------------------------------------------------------------------

// reachLevel is the pre-pass verdict for one dependency edge or route pool
// member, folded from possibly-many host pairs.
type reachLevel int

const (
	reachUnknown reachLevel = iota
	reachOK
	reachDegraded
	reachDown
)

func (l reachLevel) String() string {
	switch l {
	case reachOK:
		return "ok"
	case reachDegraded:
		return "degraded"
	case reachDown:
		return "down"
	default:
		return "unknown"
	}
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// aggregateReach folds reach(c, p) over every (consumer host, provider host)
// pair: a Better-fold per consumer (one reachable provider is enough), then
// the outer verdict is "are they all down", not a fold -- inverting either
// direction makes this return Down for every input.
//
// Deduping to component representatives before this comparison is the
// design's stated optimisation; this implementation dedupes to unique host
// ids instead, which is correct but not the O(components^2) bound the design
// describes -- acceptable at the estate size this milestone targets (one
// service_instance per k8s pod, per DECISIONS.md §1, so tens of hosts, not
// thousands), flagged here rather than silently claimed.
func aggregateReach(net *reachModel, consumers, providers []string) reachLevel {
	anyKnown, allDown, anyNotOK := false, true, false
	for _, c := range consumers {
		per := domain.StatusDown
		known := false
		for _, p := range providers {
			if s, ok := net.reach(c, p, domain.PlaneData); ok {
				per = per.Better(s)
				known = true
			}
		}
		if !known {
			continue
		}
		anyKnown = true
		if per != domain.StatusDown {
			allDown = false
		}
		if per != domain.StatusOK {
			anyNotOK = true
		}
	}
	switch {
	case !anyKnown:
		return reachUnknown
	case allDown:
		return reachDown
	case anyNotOK:
		return reachDegraded
	default:
		return reachOK
	}
}

// reachHosts resolves an endpoint to the hosts that answer for it.
// loopback/unix are handled by the caller (the local exemption); everything
// else falls back to the alive hosts of the owning service unless the
// endpoint is a Kubernetes service address with a resolvable cluster.
func reachHosts(g *Graph, net *reachModel, ep Endpoint, aliveHosts map[string][]string, coverage *ReachCoverage) []string {
	switch ep.BindScope {
	case domain.BindClusterIP, domain.BindNodePort, domain.BindIngress:
		if cluster, ok := g.ServiceClusterAssetID[ep.ServiceID]; ok && cluster != "" {
			nodes := g.ClusterNodes[cluster]
			var hosts []string
			for _, n := range nodes {
				if net == nil || !net.isolated(n, domain.PlaneData) {
					hosts = append(hosts, n)
				}
			}
			return hosts
		}
		if coverage != nil {
			coverage.ClusterUnresolved++
		}
		return aliveHosts[ep.ServiceID]
	default:
		return aliveHosts[ep.ServiceID]
	}
}

// computeDepReach is the pairwise partition pre-pass: computed once, before
// the fixed point, keyed by dependency id.
func computeDepReach(g *Graph, net *reachModel, aliveHosts map[string][]string, coverage *ReachCoverage) map[string]reachLevel {
	out := make(map[string]reachLevel, len(g.Deps))
	for _, dep := range g.Deps {
		if dep.Lifecycle == domain.LifecycleRetired {
			continue
		}
		pep, ok := providerEndpointForDep(g, dep)
		if !ok {
			continue
		}
		if isLocalBind(pep.BindScope) {
			out[dep.ID] = reachOK
			continue
		}
		// Scenario 9's stated fail-open: pod-to-pod within the same cluster
		// is a CNI tunnel, and asserting a physical partition inside a
		// cluster is a claim this model cannot support.
		if pep.BindScope == domain.BindClusterIP {
			if pc, ok := g.ServiceClusterAssetID[pep.ServiceID]; ok && pc != "" {
				if cc, ok2 := g.ServiceClusterAssetID[dep.ConsumerServiceID]; ok2 && cc == pc {
					out[dep.ID] = reachOK
					continue
				}
			}
		}
		if net == nil {
			out[dep.ID] = reachOK
			continue
		}
		consumers := dedupeStrings(aliveHosts[dep.ConsumerServiceID])
		if len(consumers) == 0 {
			// Already down by capacity; nothing new for this channel to say.
			out[dep.ID] = reachUnknown
			continue
		}
		providers := dedupeStrings(reachHosts(g, net, pep, aliveHosts, coverage))
		if len(providers) == 0 {
			out[dep.ID] = reachUnknown
			continue
		}
		out[dep.ID] = aggregateReach(net, consumers, providers)
	}
	return out
}

// computeRouteMemberReach mirrors computeDepReach at pool-member granularity,
// so "the proxy is reachable but every backend is on the far side of the
// break" is caught for network loss the same way it already is for host loss.
func computeRouteMemberReach(g *Graph, net *reachModel, aliveHosts map[string][]string) map[string]reachLevel {
	out := map[string]reachLevel{}
	if net == nil {
		return out
	}
	for routeID, r := range g.Routes {
		fep, ok := g.Endpoints[r.FrontendEndpointID]
		if !ok || isLocalBind(fep.BindScope) {
			continue
		}
		frontHosts := dedupeStrings(aliveHosts[fep.ServiceID])
		if len(frontHosts) == 0 {
			continue
		}
		for _, endpointID := range g.PoolMembers[r.BackendPoolID] {
			mep, ok := g.Endpoints[endpointID]
			if !ok || isLocalBind(mep.BindScope) {
				continue
			}
			memberHosts := dedupeStrings(reachHosts(g, net, mep, aliveHosts, nil))
			if len(memberHosts) == 0 {
				continue
			}
			out[routeID+"/"+endpointID] = aggregateReach(net, frontHosts, memberHosts)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Seam 3 -- anchors and exposure
// ---------------------------------------------------------------------------

func matchingAnchors(net *NetGraph, ep Endpoint) []NetAnchorInfo {
	var out []NetAnchorInfo
	for _, a := range net.Anchors {
		if a.Scope != ep.Exposure {
			continue
		}
		if a.Scope == "environment" && a.EnvironmentID != nil && *a.EnvironmentID != "" && *a.EnvironmentID != ep.ServiceEnvID {
			continue
		}
		out = append(out, a)
	}
	return out
}

// betterFoldAnchors folds reach(host, anchor group) with the Better identity
// (StatusDown): one reachable anchor is enough.
func betterFoldAnchors(net *reachModel, hosts []string, anchors []NetAnchorInfo) (domain.Status, string, bool) {
	best := domain.StatusDown
	known := false
	blocking := ""
	for _, h := range hosts {
		for _, a := range anchors {
			s, ok := net.reachGroup(h, a.GroupID, domain.PlaneData)
			if !ok {
				continue
			}
			if !known {
				best = s
			} else {
				best = best.Better(s)
			}
			known = true
			if s != domain.StatusOK && blocking == "" {
				blocking = net.groupCode(a.GroupID)
			}
		}
	}
	return best, blocking, known
}

// computeUnreachable is R4 applied per non-local, non-internal endpoint:
// running, but no path to its declared anchor. Reported, never propagated.
func computeUnreachable(g *Graph, net *reachModel, aliveHosts map[string][]string, statuses map[string]domain.Status, coverage *ReachCoverage) []EndpointReach {
	if net == nil {
		return nil
	}
	var out []EndpointReach
	ids := make([]string, 0, len(g.Endpoints))
	for id := range g.Endpoints {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ep := g.Endpoints[id]
		if isLocalBind(ep.BindScope) || ep.Exposure == "internal" || ep.Exposure == "" {
			continue
		}
		svc := g.Services[ep.ServiceID]
		if svc == nil {
			continue
		}
		if statuses[ep.ServiceID] == domain.StatusDown {
			continue // already down by capacity -- no duplicate finding
		}
		anchors := matchingAnchors(net.net, ep)
		if len(anchors) == 0 {
			if coverage != nil {
				coverage.NoAnchorForScope[ep.Exposure]++
			}
			continue
		}
		hosts := dedupeStrings(aliveHosts[ep.ServiceID])
		if len(hosts) == 0 {
			continue // already down by capacity
		}
		r, blocking, known := betterFoldAnchors(net, hosts, anchors)
		if !known || r == domain.StatusOK {
			continue
		}
		out = append(out, EndpointReach{
			ServiceID: ep.ServiceID, Code: svc.Code, Name: svc.Name,
			EndpointID: ep.ID, EndpointName: ep.Name, Scope: ep.Exposure,
			Status: r, BlockingGroup: blocking,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].EndpointName < out[j].EndpointName
	})
	return out
}

// computeExposureImpacts appends one ServiceImpact per service whose every
// non-local endpoint has lost its anchor -- never written into statuses,
// never propagated. existing names services already reported so this never
// duplicates an entry.
func computeExposureImpacts(g *Graph, unreachable []EndpointReach, existing map[string]bool) []ServiceImpact {
	if len(unreachable) == 0 {
		return nil
	}
	nonLocalCount := map[string]int{}
	for _, ep := range g.Endpoints {
		if isLocalBind(ep.BindScope) || ep.Exposure == "internal" || ep.Exposure == "" {
			continue
		}
		nonLocalCount[ep.ServiceID]++
	}
	unreachableCount := map[string]int{}
	worst := map[string]domain.Status{}
	reason := map[string]string{}
	for _, u := range unreachable {
		unreachableCount[u.ServiceID]++
		if cur, ok := worst[u.ServiceID]; ok {
			worst[u.ServiceID] = cur.Worse(u.Status)
		} else {
			worst[u.ServiceID] = u.Status
		}
		if _, ok := reason[u.ServiceID]; !ok {
			blocked := u.BlockingGroup
			if blocked == "" {
				blocked = "an unresolved forwarder"
			}
			reason[u.ServiceID] = fmt.Sprintf("%s requires a %s anchor; the only path runs through %s",
				u.EndpointName, u.Scope, blocked)
		}
	}

	ids := make([]string, 0, len(nonLocalCount))
	for id, n := range nonLocalCount {
		if n > 0 && unreachableCount[id] == n && !existing[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := make([]ServiceImpact, 0, len(ids))
	for _, id := range ids {
		svc := g.Services[id]
		if svc == nil {
			continue
		}
		out = append(out, ServiceImpact{
			ServiceID: id, Code: svc.Code, Name: svc.Name, Tier: svc.Tier,
			Status: worst[id], Reason: reason[id], Cause: CauseExposure,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Reporting: isolation, partitions, redundancy, coverage
// ---------------------------------------------------------------------------

func planes() []string { return []string{domain.PlaneData, domain.PlaneMgmt, domain.PlaneStorage} }

// candidateAssets is every asset id this analysis has an opinion about at
// all: instance hosts, attachment subjects and closure descendants. It is a
// reporting convenience, not a claim that every asset in the estate is
// covered -- Graph never loads the full asset table.
func candidateAssets(g *Graph, net *NetGraph) []string {
	set := map[string]bool{}
	for _, instances := range g.Instances {
		for _, inst := range instances {
			set[inst.HostAssetID] = true
		}
	}
	if net != nil {
		for _, a := range net.Attachments {
			set[a.AssetID] = true
		}
		for _, c := range net.Closure {
			set[c.DescendantID] = true
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func computeIsolated(g *Graph, net *reachModel) []AssetIsolation {
	if net == nil {
		return nil
	}
	var out []AssetIsolation
	for _, id := range candidateAssets(g, net.net) {
		// An asset inside the outage is not isolated, it is off. Charging it
		// twice is the same double-count the liveness rule exists to prevent
		// -- `alive := !down && !(needsNet && isolated)` is one && precisely so
		// a loss is counted once -- and the report simply never got the same
		// guard. Losing a rack was telling the operator that nine powered-off
		// machines had lost management reachability.
		if net.down[id] {
			continue
		}
		for _, p := range planes() {
			if !net.isolated(id, p) {
				continue
			}
			blocking := ""
			if idxs, ok := net.effectiveAttachments(id, p); ok && len(idxs) > 0 {
				blocking = net.groupCode(net.net.Attachments[idxs[0]].GroupID)
			}
			label := net.net.AssetNames[id]
			out = append(out, AssetIsolation{
				AssetID: id, AssetName: label.Name, AssetKind: label.Kind,
				Plane: p, BlockingGroup: blocking,
			})
		}
	}
	return out
}

func computePartitions(g *Graph, deps []domain.Dependency, reachOfDep map[string]reachLevel) []EdgePartition {
	if len(reachOfDep) == 0 {
		return nil
	}
	var out []EdgePartition
	for _, dep := range deps {
		lvl, ok := reachOfDep[dep.ID]
		if !ok || lvl == reachOK || lvl == reachUnknown {
			continue
		}
		consumer := g.Services[dep.ConsumerServiceID]
		if consumer == nil {
			continue
		}
		out = append(out, EdgePartition{
			DependencyID: dep.ID, ConsumerService: consumer.Code,
			ProviderName: providerDisplayName(g, dep), Reach: lvl.String(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ConsumerService != out[j].ConsumerService {
			return out[i].ConsumerService < out[j].ConsumerService
		}
		return out[i].ProviderName < out[j].ProviderName
	})
	return out
}

func providerDisplayName(g *Graph, dep domain.Dependency) string {
	if dep.ProviderEndpointID != nil {
		if ep, ok := g.Endpoints[*dep.ProviderEndpointID]; ok {
			if svc, ok := g.Services[ep.ServiceID]; ok {
				return svc.Code + "/" + ep.Name
			}
		}
	}
	if dep.ProviderRouteID != nil {
		if r, ok := g.Routes[*dep.ProviderRouteID]; ok {
			return "route " + r.Name
		}
	}
	return ""
}

func computeRedundancyLost(net *NetGraph, groupStatus map[string]domain.Status, down map[string]bool) []GroupFinding {
	if net == nil {
		return nil
	}
	ids := make([]string, 0, len(net.Groups))
	for id := range net.Groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []GroupFinding
	for _, id := range ids {
		g := net.Groups[id]
		total, surviving := 0, 0
		for _, m := range net.Members[id] {
			if !installedLifecycle(m.AssetLifecycle) {
				continue
			}
			total++
			if !down[m.AssetID] {
				surviving++
			}
		}
		if total <= 1 || surviving == total {
			continue
		}
		// A fully-down group used to be skipped here on the reasoning that it
		// was "already reported down elsewhere". That holds only for a group
		// something attaches to, which surfaces it through Isolated. Nothing
		// attaches to an edge firewall pair -- hosts attach to the core, which
		// uplinks to the edge -- so losing both halves made the last remaining
		// signal disappear at the exact moment it mattered most, and the page
		// reported a strictly larger outage more quietly than a smaller one.
		note := "one more loss and this group goes down"
		switch groupStatus[id] {
		case domain.StatusDown:
			note = "the whole group is down"
		case domain.StatusDegraded:
			note = "no redundancy remains until a standby is promoted"
		case domain.StatusOK:
			// Members were lost but the group still meets its policy, which is
			// the default note above: one more loss and it does not.
		}
		out = append(out, GroupFinding{
			GroupCode: g.Code,
			Message:   fmt.Sprintf("%s: %d of %d members surviving; %s", g.Code, surviving, total, note),
		})
	}
	return out
}

// computeCoverage measures how much of the SERVICE-BEARING estate the network
// model has an opinion about -- assets that carry a service instance, not every
// asset in the inventory. That is the right denominator for an impact report,
// because an asset hosting nothing cannot change a service's status. It is the
// wrong denominator for a sentence about the estate, so anything rendering
// these numbers must say which population they describe.
func computeCoverage(g *Graph, net *reachModel) ReachCoverage {
	cov := ReachCoverage{NoAnchorForScope: map[string]int{}}
	if net == nil {
		return cov
	}
	cov.TopologyDeclared = true
	hostIDs := map[string]bool{}
	for _, instances := range g.Instances {
		for _, inst := range instances {
			hostIDs[inst.HostAssetID] = true
		}
	}
	for id := range hostIDs {
		if byPlane, ok := net.attachmentsByAssetPlane[id]; ok {
			if idxs, ok := byPlane[domain.PlaneData]; ok && len(idxs) > 0 {
				cov.Modelled++
				continue
			}
		}
		if net.modelled(id, domain.PlaneData) {
			cov.Inherited++
		} else {
			cov.Unmodelled++
		}
	}
	for gid := range net.net.Groups {
		hasUplink := false
		for _, u := range net.net.Uplinks {
			if u.GroupID == gid || u.UpstreamGroupID == gid {
				hasUplink = true
				break
			}
		}
		hasAnchor := false
		for _, a := range net.net.Anchors {
			if a.GroupID == gid {
				hasAnchor = true
				break
			}
		}
		if !hasUplink && !hasAnchor {
			cov.GroupsWithoutUplinkOrAnchor++
		}
	}
	return cov
}
