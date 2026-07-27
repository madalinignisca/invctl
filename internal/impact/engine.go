package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gabriel/invctl/internal/domain"
)

// maxIterations guards the fixed point. Status is monotonic within a run
// (ok -> degraded -> down, never back), so the loop provably terminates in at
// most one round per service; this is a backstop against a future change
// breaking monotonicity, not an expected limit.
const maxIterations = 20

// Request describes the outage to simulate.
type Request struct {
	// DownAssetIDs are the assets being taken away. Everything contained by
	// them goes too -- resolving that is the caller's job, via the closure
	// table, so that "reboot this VM" and "this rack loses power" arrive here
	// in the same shape.
	DownAssetIDs []string
	// WindowSeconds is how long the outage lasts. It matters only for async
	// dependencies, where a 3-minute reboot and a 45-minute one genuinely
	// differ for a consumer with a buffer.
	WindowSeconds int
}

// ServiceImpact is one service's outcome.
type ServiceImpact struct {
	ServiceID string
	Code      string
	Name      string
	Tier      int
	Status    domain.Status
	// Reason explains the status in the terms the operator cares about.
	Reason string
	// LostInstances and TotalInstances show the capacity arithmetic that
	// produced the status, so a surprising result can be checked rather than
	// merely believed.
	LostInstances  int
	TotalInstances int
	// Via names the dependency that propagated the status, empty when the
	// service was hit directly by losing its own instances.
	Via string
}

// Result is the whole answer.
type Result struct {
	// Services holds every affected service, worst first.
	Services []ServiceImpact
	// WontRestart holds services that are serving now but have an unmet
	// startup dependency -- they will not come back if anything restarts
	// them. This is the highest-value output and the one a status-only report
	// hides completely.
	WontRestart []ServiceImpact
	// Cycles are dependency cycles found while ordering. A cycle is a real
	// finding to report, not a bug to suppress.
	Cycles [][]string
	// SafeOrder is a suggested shutdown order, leaf-first: shut down the
	// things nothing depends on before the things they depend on.
	SafeOrder []string
	// Iterations is how many propagation rounds the fixed point needed.
	Iterations int
}

// Analyse runs the three phases and returns the impact of the request.
func Analyse(g *Graph, req Request, downInstanceIDs map[string]bool) Result {
	statuses, lost, totals, reasons := phaseCapacity(g, downInstanceIDs)

	via, wontRestartIDs, iterations := phasePropagate(g, req, statuses, reasons)

	result := Result{Iterations: iterations}
	for id, status := range statuses {
		fault, willNotRestart := wontRestartIDs[id]
		if status == domain.StatusOK && !willNotRestart {
			continue
		}
		svc := g.Services[id]
		if svc == nil {
			continue
		}
		si := ServiceImpact{
			ServiceID: id, Code: svc.Code, Name: svc.Name, Tier: svc.Tier,
			Status: status, Reason: reasons[id],
			LostInstances: lost[id], TotalInstances: totals[id],
			Via: via[id],
		}
		if status != domain.StatusOK {
			result.Services = append(result.Services, si)
		}
		// A service that is already down does not belong on the landmine list.
		// The whole point of WontRestart is "this is serving now, and you will
		// not notice the problem until something restarts it" -- something
		// already reported as down is neither serving nor a surprise, and
		// listing it twice trains people to skim the section that matters most.
		if willNotRestart && status != domain.StatusDown {
			// The startup entry names the unmet startup dependency, which is
			// often not the edge that set the status.
			startup := si
			startup.Via = fault.Via
			startup.Reason = fault.Reason
			result.WontRestart = append(result.WontRestart, startup)
		}
	}

	sortImpacts(result.Services)
	sortImpacts(result.WontRestart)

	affected := make(map[string]bool, len(result.Services))
	for _, s := range result.Services {
		affected[s.ServiceID] = true
	}
	result.SafeOrder, _ = shutdownOrder(g, affected)
	// Cycles are reported across every nature, not just hard edges: an
	// app -> db -> auth -> app loop is a real finding whether or not each hop
	// transmits a full outage, and the shutdown order only ever sees hard
	// edges.
	result.Cycles = detectCycles(g, affected)
	return result
}

// phaseCapacity applies each service's availability policy to the instances
// that survive (HANDOVER §6 phase 2).
func phaseCapacity(g *Graph, downInstanceIDs map[string]bool) (
	statuses map[string]domain.Status,
	lost map[string]int,
	totals map[string]int,
	reasons map[string]string,
) {
	statuses = make(map[string]domain.Status, len(g.Services))
	lost = make(map[string]int, len(g.Services))
	totals = make(map[string]int, len(g.Services))
	reasons = make(map[string]string, len(g.Services))

	for serviceID, svc := range g.Services {
		instances := g.Instances[serviceID]
		health := make([]domain.InstanceHealth, 0, len(instances))
		lostCount := 0
		for _, inst := range instances {
			if inst.Disabled {
				// Not expected to be running, so it is neither capacity nor a
				// loss.
				continue
			}
			alive := !downInstanceIDs[inst.ID]
			if !alive {
				lostCount++
			}
			health = append(health, domain.InstanceHealth{
				ID: inst.ID, Role: inst.Role, Shard: inst.Shard, Alive: alive,
			})
		}

		status := svc.EvaluateCapacity(health)
		statuses[serviceID] = status
		lost[serviceID] = lostCount
		totals[serviceID] = len(health)
		if status != domain.StatusOK {
			reasons[serviceID] = capacityReason(svc, status, lostCount, len(health))
		}
	}
	return statuses, lost, totals, reasons
}

func capacityReason(svc *domain.Service, status domain.Status, lost, total int) string {
	surviving := total - lost
	switch svc.Availability {
	case domain.AvailQuorum:
		return fmt.Sprintf("lost %d of %d instances; quorum needs %d", lost, total, total/2+1)
	case domain.AvailActivePassive:
		if status == domain.StatusDegraded {
			return "primary lost; a standby can take over but promotion is manual"
		}
		return fmt.Sprintf("lost %d of %d instances", lost, total)
	case domain.AvailActiveActive:
		min := 1
		if svc.MinHealthy != nil {
			min = *svc.MinHealthy
		}
		return fmt.Sprintf("%d of %d instances surviving; policy needs %d", surviving, total, min)
	case domain.AvailSharded:
		return "at least one shard has no surviving replica"
	default:
		return fmt.Sprintf("lost %d of %d instances", lost, total)
	}
}

// phasePropagate iterates the dependency edges until no status changes
// (HANDOVER §6 phase 3).
// startupFault records why a service will not come back. It is kept separate
// from the status reason on purpose: a service can be degraded because of one
// edge and unable to restart because of an entirely different one, and
// reporting the status edge in the landmine list points the operator at the
// wrong dependency.
type startupFault struct {
	Via    string
	Reason string
}

func phasePropagate(
	g *Graph,
	req Request,
	statuses map[string]domain.Status,
	reasons map[string]string,
) (via map[string]string, wontRestart map[string]startupFault, iterations int) {
	via = map[string]string{}
	wontRestart = map[string]startupFault{}

	for iterations = 1; iterations <= maxIterations; iterations++ {
		changed := false

		// Provider health is recomputed each round because a route's health
		// derives from its pool, whose members' services may have changed
		// status in the previous round.
		routeStatus := routeStatuses(g, statuses)

		for _, dep := range g.Deps {
			if dep.Lifecycle == domain.LifecycleRetired {
				continue
			}
			consumer, ok := statuses[dep.ConsumerServiceID]
			if !ok {
				continue
			}

			providerStatus, providerName, ok := providerHealth(g, dep, statuses, routeStatus)
			if !ok {
				continue
			}

			effect := dep.Propagate(providerStatus, req.WindowSeconds)

			if _, already := wontRestart[dep.ConsumerServiceID]; effect.WontRestart && !already {
				wontRestart[dep.ConsumerServiceID] = startupFault{
					Via: providerName,
					Reason: "startup dependency on " + providerName +
						" is unmet: running now, will not come back after a restart",
				}
				changed = true
			}

			merged := consumer.Worse(effect.Status)
			if merged != consumer {
				statuses[dep.ConsumerServiceID] = merged
				via[dep.ConsumerServiceID] = providerName
				reasons[dep.ConsumerServiceID] = propagationReason(dep, providerStatus, providerName)
				changed = true
			}
		}

		if !changed {
			return via, wontRestart, iterations
		}
	}
	return via, wontRestart, maxIterations
}

// providerHealth resolves the status of whatever a dependency points at.
func providerHealth(
	g *Graph,
	dep domain.Dependency,
	statuses map[string]domain.Status,
	routeStatus map[string]domain.Status,
) (domain.Status, string, bool) {
	if dep.ProviderEndpointID != nil {
		ep, ok := g.Endpoints[*dep.ProviderEndpointID]
		if !ok {
			return domain.StatusOK, "", false
		}
		svc := g.Services[ep.ServiceID]
		if svc == nil {
			return domain.StatusOK, "", false
		}
		return statuses[ep.ServiceID], svc.Code + "/" + ep.Name, true
	}
	if dep.ProviderRouteID != nil {
		r, ok := g.Routes[*dep.ProviderRouteID]
		if !ok {
			return domain.StatusOK, "", false
		}
		return routeStatus[r.ID], "route " + r.Name, true
	}
	return domain.StatusOK, "", false
}

// routeStatuses derives each route's health from its proxy and its pool.
//
// This is what the handover means by "routes are nodes in the graph, not
// passthroughs": a proxy that is up but whose every backend sits on the host
// being rebooted is not serving, and only pool-level derivation shows that.
func routeStatuses(g *Graph, statuses map[string]domain.Status) map[string]domain.Status {
	out := make(map[string]domain.Status, len(g.Routes))
	for id, r := range g.Routes {
		// The proxy terminating the route.
		frontend := domain.StatusOK
		if ep, ok := g.Endpoints[r.FrontendEndpointID]; ok {
			frontend = statuses[ep.ServiceID]
		}

		// Members whose service is not in the graph -- retired, most often --
		// are dropped from the arithmetic entirely rather than counted. An
		// unknown service id would otherwise fall through to the default arm
		// below and be scored as alive, so a pool with one live backend and
		// one retired one would report degraded when the live backend died,
		// instead of down.
		members := make([]string, 0, len(g.PoolMembers[r.BackendPoolID]))
		for _, endpointID := range g.PoolMembers[r.BackendPoolID] {
			ep, ok := g.Endpoints[endpointID]
			if !ok {
				continue
			}
			if _, known := g.Services[ep.ServiceID]; !known {
				continue
			}
			members = append(members, endpointID)
		}

		poolStatus := domain.StatusOK
		if len(members) > 0 {
			alive, degraded := 0, 0
			for _, endpointID := range members {
				ep, ok := g.Endpoints[endpointID]
				if !ok {
					continue
				}
				switch statuses[ep.ServiceID] {
				case domain.StatusDown:
					// contributes nothing
				case domain.StatusDegraded:
					degraded++
					alive++
				default:
					alive++
				}
			}
			switch {
			case alive == 0:
				poolStatus = domain.StatusDown
			case alive < len(members) || degraded > 0:
				poolStatus = domain.StatusDegraded
			}
		}
		out[id] = frontend.Worse(poolStatus)
	}
	return out
}

func propagationReason(dep domain.Dependency, providerStatus domain.Status, providerName string) string {
	switch dep.Nature {
	case domain.NatureHard:
		return "hard dependency on " + providerName + " is " + string(providerStatus)
	case domain.NatureSoft:
		return "soft dependency on " + providerName + " is " + string(providerStatus) +
			"; degraded but still serving"
	case domain.NatureAsync:
		return "async dependency on " + providerName +
			" is down for longer than its tolerance window"
	default:
		return "dependency on " + providerName + " is " + string(providerStatus)
	}
}

// shutdownOrder returns a leaf-first ordering of the affected services over
// hard edges, plus any cycles found.
//
// Leaf-first means consumers before providers: stop the thing that depends on
// the database before stopping the database.
func shutdownOrder(g *Graph, affected map[string]bool) ([]string, [][]string) {
	// Build the consumer -> provider adjacency restricted to hard edges
	// between affected services.
	providers := map[string][]string{}
	for _, dep := range g.Deps {
		if dep.Nature != domain.NatureHard || dep.Lifecycle == domain.LifecycleRetired {
			continue
		}
		consumer := dep.ConsumerServiceID
		provider := g.providerServiceID(dep)
		if provider == "" || provider == consumer {
			continue
		}
		if !affected[consumer] || !affected[provider] {
			continue
		}
		providers[consumer] = append(providers[consumer], provider)
	}

	nodes := make([]string, 0, len(affected))
	for id := range affected {
		nodes = append(nodes, id)
	}
	// Deterministic input order, so the same graph always yields the same
	// suggested order rather than whatever map iteration produced today.
	sort.Slice(nodes, func(i, j int) bool { return codeOf(g, nodes[i]) < codeOf(g, nodes[j]) })

	const (
		white = 0 // unvisited
		grey  = 1 // on the current DFS path
		black = 2 // finished
	)
	colour := make(map[string]int, len(nodes))
	var order []string
	var cycles [][]string
	var path []string

	var visit func(string)
	visit = func(node string) {
		switch colour[node] {
		case grey:
			// Back edge: everything from the first occurrence of this node
			// onwards is the cycle.
			for i, n := range path {
				if n == node {
					cycle := append([]string{}, path[i:]...)
					cycles = append(cycles, append(cycle, node))
					break
				}
			}
			return
		case black:
			return
		}

		colour[node] = grey
		path = append(path, node)

		deps := providers[node]
		sort.Slice(deps, func(i, j int) bool { return codeOf(g, deps[i]) < codeOf(g, deps[j]) })
		for _, provider := range deps {
			visit(provider)
		}

		path = path[:len(path)-1]
		colour[node] = black
		// Post-order: providers are appended before their consumers, so
		// reversing at the end gives consumers first.
		order = append(order, node)
	}

	for _, node := range nodes {
		visit(node)
	}

	// order is provider-first; shutdown wants consumers first.
	reversed := make([]string, len(order))
	for i, id := range order {
		reversed[len(order)-1-i] = codeOf(g, id)
	}

	named := make([][]string, 0, len(cycles))
	for _, cycle := range cycles {
		names := make([]string, len(cycle))
		for i, id := range cycle {
			names[i] = codeOf(g, id)
		}
		named = append(named, names)
	}
	return reversed, named
}

func codeOf(g *Graph, serviceID string) string {
	if svc, ok := g.Services[serviceID]; ok {
		return svc.Code
	}
	return serviceID
}

// sortImpacts orders by severity, then tier, then code, so the worst and most
// important thing is always at the top.
func sortImpacts(impacts []ServiceImpact) {
	sort.Slice(impacts, func(i, j int) bool {
		a, b := impacts[i], impacts[j]
		if a.Status != b.Status {
			return statusRank(a.Status) > statusRank(b.Status)
		}
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		return a.Code < b.Code
	})
}

func statusRank(s domain.Status) int {
	switch s {
	case domain.StatusDown:
		return 2
	case domain.StatusDegraded:
		return 1
	default:
		return 0
	}
}

// detectCycles finds dependency cycles among the affected services, over edges
// of every nature.
//
// A cycle is a finding, not a bug: app -> db -> auth -> app happens in real
// estates and is worth telling someone about, because it means there is no
// clean order in which to bring the group back up.
func detectCycles(g *Graph, affected map[string]bool) [][]string {
	providers := map[string][]string{}
	nodes := make([]string, 0, len(affected))
	for id := range affected {
		nodes = append(nodes, id)
	}
	sort.Slice(nodes, func(i, j int) bool { return codeOf(g, nodes[i]) < codeOf(g, nodes[j]) })

	for _, dep := range g.Deps {
		if dep.Lifecycle == domain.LifecycleRetired {
			continue
		}
		consumer := dep.ConsumerServiceID
		provider := g.providerServiceID(dep)
		if provider == "" || provider == consumer {
			continue
		}
		if !affected[consumer] || !affected[provider] {
			continue
		}
		providers[consumer] = append(providers[consumer], provider)
	}

	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := make(map[string]int, len(nodes))
	var path []string
	var cycles [][]string
	seen := map[string]bool{}

	var visit func(string)
	visit = func(node string) {
		switch colour[node] {
		case grey:
			for i, n := range path {
				if n != node {
					continue
				}
				cycle := append([]string{}, path[i:]...)
				cycle = append(cycle, node)
				names := make([]string, len(cycle))
				for j, id := range cycle {
					names[j] = codeOf(g, id)
				}
				// Normalise so the same loop discovered from two entry
				// points is reported once.
				key := cycleKey(names)
				if !seen[key] {
					seen[key] = true
					cycles = append(cycles, names)
				}
				break
			}
			return
		case black:
			return
		}
		colour[node] = grey
		path = append(path, node)

		next := providers[node]
		sort.Slice(next, func(i, j int) bool { return codeOf(g, next[i]) < codeOf(g, next[j]) })
		for _, provider := range next {
			visit(provider)
		}

		path = path[:len(path)-1]
		colour[node] = black
	}

	for _, node := range nodes {
		visit(node)
	}
	return cycles
}

// cycleKey rotates a cycle so that its lexicographically smallest member comes
// first, giving the same key regardless of where the traversal entered it.
func cycleKey(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, ">")
	}
	// The last element repeats the first; drop it before rotating.
	ring := names[:len(names)-1]
	minIdx := 0
	for i, n := range ring {
		if n < ring[minIdx] {
			minIdx = i
		}
	}
	rotated := make([]string, 0, len(ring))
	for i := range ring {
		rotated = append(rotated, ring[(minIdx+i)%len(ring)])
	}
	return strings.Join(rotated, ">")
}
