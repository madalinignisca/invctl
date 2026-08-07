// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
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
	// UseObservedHealth is inert until M6 -- there is nowhere yet to read
	// observed health from (asset_health lands in M6, docs/AUDIT.md). The
	// field exists now so the signature does not change again when it does.
	UseObservedHealth bool
	// SkipNetwork is the escape hatch: when true, Analyse behaves exactly as
	// if Inputs.Net were nil, regardless of what was loaded.
	SkipNetwork bool
	// ReportReachabilityOnly computes and reports reachability without letting
	// it change a status.
	//
	// The zero value is production behaviour: reachability composes into the
	// answer. This flag inverts that for the case where somebody wants to see
	// what the model concludes before trusting it -- which was the whole of M3,
	// and remains the honest way to introduce the feature to an estate whose
	// topology has just been entered and not yet checked.
	ReportReachabilityOnly bool
}

// Inputs are the down-state facts Analyse needs.
type Inputs struct {
	DownInstanceIDs map[string]bool
	// DownAssetIDs is already closure-expanded by the caller (the same
	// expansion that produced DownInstanceIDs), so a forwarder inside a
	// falling rack is downed as a consequence of the rack, not as a separate
	// input.
	DownAssetIDs map[string]bool
	// Net is nil whenever the estate has declared no topology at all -- zero
	// rows in every net_* table -- which makes every reachability step a
	// no-op and the result byte-identical to a build that never had this
	// feature.
	Net *NetGraph
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
	// Cause names which channel produced this entry: capacity (an instance is
	// actually gone), reachability (an instance is running but cut off, or the
	// path to a healthy provider is broken), dependency (propagated through an
	// edge whose provider genuinely failed) or exposure (a running service lost
	// its only path to an external/cross-env/environment anchor -- reported
	// here but never merged into statuses or propagated).
	//
	// It answers "where do I go to fix this?", which is why a partitioned edge
	// is reachability and not dependency.
	Cause Cause
	// LostToIsolation is the subset of LostInstances that are running but
	// network-isolated rather than actually down -- what lets the reason
	// string say "the host is running but cut off" instead of "lost".
	LostToIsolation int
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

	// Isolated names every asset cut off from its declared network on any
	// plane. Reporting-only: only the data-plane cut actually feeds Seam 1.
	Isolated []AssetIsolation
	// Partitions names dependency edges whose reachability, not their
	// provider's own status, degraded or cut them.
	Partitions []EdgePartition
	// Unreachable holds running services with no path to their endpoint's
	// declared anchor. Reported, never propagated (Seam 3).
	Unreachable []EndpointReach
	// RedundancyLost names forwarder groups that survived this outage but
	// have no redundancy left for the next one.
	RedundancyLost []GroupFinding
	// Coverage is the modelled/unmodelled split this run saw, so a report can
	// say honestly how much of the estate it actually has an opinion about.
	Coverage ReachCoverage
	// Structures names declared things the outage empties or reduces to one:
	// a VLAN whose last port went, a redundancy group whose last router went,
	// an overlay left terminating in one place. Reported, never propagated --
	// no service in this model declares that it needs a VLAN or a VIP, so
	// changing a status on that basis would assert a dependency nobody wrote.
	Structures []StructureFinding
}

// Analyse runs the three phases and returns the impact of the request.
//
// Phase 0 -- the reachability model -- is linear and runs once, before phase
// 2 (capacity): no iteration, no recursion, no traversal, so it provably
// cannot affect the fixed point's termination argument. Inputs.Net == nil (or
// req.SkipNetwork) makes it inert: every reachability step becomes a no-op
// and the result is byte-identical to a build that never had this feature.
func Analyse(g *Graph, req Request, in Inputs) Result {
	netInput := in.Net
	if req.SkipNetwork {
		netInput = nil
	}
	net := buildReachModel(netInput, in.DownAssetIDs)

	// Seam 1. An empty needsNet makes isolation unable to reach the alive
	// term, so phaseCapacity runs exactly as it did before reachability
	// existed. The reach model is still built, because the report below needs
	// it -- what is gated is the effect, not the computation.
	needsNet := map[string]bool{}
	if !req.ReportReachabilityOnly {
		needsNet = computeNeedsNet(g)
	}

	statuses, lost, totals, reasons, lostToIsolation, aliveHosts := phaseCapacity(g, in.DownInstanceIDs, net, needsNet)

	coverage := computeCoverage(g, net)
	// Always computed: Result.Partitions reports these whether or not they are
	// allowed to move a status.
	reachOfDep := computeDepReach(g, net, aliveHosts, &coverage)
	routeMemberReach := computeRouteMemberReach(g, net, aliveHosts)

	// Seam 2. nil maps read as the zero reachLevel, which the downgrade switch
	// ignores, so Propagate sees the provider status it always saw.
	appliedDepReach, appliedRouteReach := reachOfDep, routeMemberReach
	if req.ReportReachabilityOnly {
		appliedDepReach, appliedRouteReach = nil, nil
	}

	via, viaPartition, wontRestartIDs, iterations := phasePropagate(g, req, statuses, reasons, appliedDepReach, appliedRouteReach)

	result := Result{Iterations: iterations}
	existingIDs := map[string]bool{}
	for id, status := range statuses {
		fault, willNotRestart := wontRestartIDs[id]
		if status == domain.StatusOK && !willNotRestart {
			continue
		}
		svc := g.Services[id]
		if svc == nil {
			continue
		}
		// Cause answers "where do I go to fix this?", so it must describe
		// whatever actually decided the status being reported on this row.
		//
		// The propagated cases are tested FIRST, and the order is the whole
		// correctness argument. A service can lose instances to isolation and
		// still absorb it -- vault loses one of three nodes and quorum holds --
		// and then go degraded for an entirely unrelated dependency. Testing
		// the isolation count first labelled that row "network path, not the
		// service itself" while its Reason named a provider that was in fact
		// 100% down and listed two rows above. That is the mirror image of the
		// defect M4 fixed, pointing the operator away from a broken service
		// instead of towards a healthy one, and it was unreachable until the
		// fixture declared topology.
		//
		// via[id] is set only when a dependency WORSENED this service, and
		// status is monotonic, so a non-empty via means the edge is what
		// carried it to the status shown.
		cause := CauseCapacity
		switch {
		case via[id] != "" && viaPartition[id]:
			cause = CauseReachability
		case via[id] != "":
			cause = CauseDependency
		case lostToIsolation[id] > 0 && lostToIsolation[id] == lost[id]:
			cause = CauseReachability
		}
		si := ServiceImpact{
			ServiceID: id, Code: svc.Code, Name: svc.Name, Tier: svc.Tier,
			Status: status, Reason: reasons[id],
			LostInstances: lost[id], TotalInstances: totals[id],
			Via: via[id], Cause: cause, LostToIsolation: lostToIsolation[id],
		}
		if status != domain.StatusOK {
			result.Services = append(result.Services, si)
			existingIDs[id] = true
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
			// Cause must follow Via and Reason onto the startup edge. si.Cause
			// describes whichever edge moved the service's status, which for a
			// WontRestart row is routinely a different dependency entirely.
			startup.Cause = CauseDependency
			if fault.NetEffect != reachUnknown {
				startup.Cause = CauseReachability
			}
			result.WontRestart = append(result.WontRestart, startup)
		}
	}

	// Seam 3: anchor loss, reported and folded into Services only when every
	// non-local endpoint of a service has lost its anchor. Never written into
	// statuses, never propagated -- computed after the fixed point on purpose.
	//
	// The fold respects ReportReachabilityOnly for the same reason the other
	// two seams do. Until the anchor lookup was repaired this branch could
	// never fire, which is why it was previously ungated and why DECISIONS.md
	// claimed exposure could only ever add a row to a report: that was a
	// description of dead code, not of the design.
	unreachable := computeUnreachable(g, net, aliveHosts, statuses, &coverage)
	if !req.ReportReachabilityOnly {
		result.Services = append(result.Services, computeExposureImpacts(g, unreachable, existingIDs)...)
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

	result.Isolated = computeIsolated(g, net)
	result.Partitions = computePartitions(g, g.Deps, reachOfDep)
	result.Unreachable = unreachable
	result.RedundancyLost = computeRedundancyLost(netInput, groupStatusesOrNil(net), in.DownAssetIDs)
	// WP-I1. Declared structures the outage empties or leaves standing alone.
	// Reported beside the other findings and propagated into nothing: no
	// service here declares that it needs a VLAN or a virtual gateway, so
	// changing a status on that basis would assert a dependency nobody wrote
	// down. Inert when no structure is declared, exactly like the reach model.
	result.Structures = analyseStructures(g.Structures, in.DownAssetIDs)
	result.Coverage = coverage

	return result
}

// groupStatusesOrNil exposes the reach model's precomputed group statuses to
// computeRedundancyLost without leaking the reachModel type any further than
// it already reaches.
func groupStatusesOrNil(net *reachModel) map[string]domain.Status {
	if net == nil {
		return nil
	}
	return net.groupStatus
}

// phaseCapacity applies each service's availability policy to the instances
// that survive (HANDOVER §6 phase 2).
//
// net and needsNet implement Seam 1: an instance is alive only if it is
// neither taken down directly nor isolated on a service that actually needs
// the network. When net is nil this is exactly the pre-M3 computation --
// net.isolated is defined to return false for a nil receiver, so the
// isolation term is always false and every byte of output is unchanged.
func phaseCapacity(g *Graph, downInstanceIDs map[string]bool, net *reachModel, needsNet map[string]bool) (
	statuses map[string]domain.Status,
	lost map[string]int,
	totals map[string]int,
	reasons map[string]string,
	lostToIsolation map[string]int,
	aliveHosts map[string][]string,
) {
	statuses = make(map[string]domain.Status, len(g.Services))
	lost = make(map[string]int, len(g.Services))
	totals = make(map[string]int, len(g.Services))
	reasons = make(map[string]string, len(g.Services))
	lostToIsolation = make(map[string]int, len(g.Services))
	aliveHosts = make(map[string][]string, len(g.Services))

	for serviceID, svc := range g.Services {
		instances := g.Instances[serviceID]
		health := make([]domain.InstanceHealth, 0, len(instances))
		lostCount := 0
		isolationCount := 0
		for _, inst := range instances {
			if inst.Disabled {
				// Not expected to be running, so it is neither capacity nor a
				// loss.
				continue
			}
			downByPlacement := downInstanceIDs[inst.ID]
			isolatedNow := needsNet[serviceID] && net.isolated(inst.HostAssetID, domain.PlaneData)
			alive := !downByPlacement && !isolatedNow
			if !alive {
				lostCount++
				if !downByPlacement {
					isolationCount++
				}
			} else {
				aliveHosts[serviceID] = append(aliveHosts[serviceID], inst.HostAssetID)
			}
			health = append(health, domain.InstanceHealth{
				ID: inst.ID, Role: inst.Role, Shard: inst.Shard, Alive: alive,
			})
		}

		status := svc.EvaluateCapacity(health)
		statuses[serviceID] = status
		lost[serviceID] = lostCount
		totals[serviceID] = len(health)
		lostToIsolation[serviceID] = isolationCount
		if status != domain.StatusOK {
			reasons[serviceID] = capacityReason(svc, status, lostCount, len(health), isolationCount)
		}
	}
	return statuses, lost, totals, reasons, lostToIsolation, aliveHosts
}

func capacityReason(svc *domain.Service, status domain.Status, lost, total, lostToIsolation int) string {
	base := capacityReasonBase(svc, status, lost, total)
	switch lostToIsolation {
	case 0:
		return base
	case lost:
		return base + " (running, but network-isolated -- not powered off)"
	default:
		return fmt.Sprintf("%s (%d of those network-isolated)", base, lostToIsolation)
	}
}

func capacityReasonBase(svc *domain.Service, status domain.Status, lost, total int) string {
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
	// NetEffect is the startup edge's own network verdict. It is tracked
	// separately from the status-propagating edge because a startup dependency
	// never changes a status -- domain.NatureStartup always propagates ok -- so
	// it can never reach the via/viaPartition assignment below. Without this,
	// a WontRestart row would inherit the Cause of whichever unrelated edge
	// happened to move the service's status.
	NetEffect reachLevel
}

func phasePropagate(
	g *Graph,
	req Request,
	statuses map[string]domain.Status,
	reasons map[string]string,
	reachOfDep map[string]reachLevel,
	routeMemberReach map[string]reachLevel,
) (via map[string]string, viaPartition map[string]bool, wontRestart map[string]startupFault, iterations int) {
	via = map[string]string{}
	viaPartition = map[string]bool{}
	wontRestart = map[string]startupFault{}

	for iterations = 1; iterations <= maxIterations; iterations++ {
		changed := false

		// Provider health is recomputed each round because a route's health
		// derives from its pool, whose members' services may have changed
		// status in the previous round. routeMemberReach itself is static
		// (Seam 2's pre-pass), computed once outside this loop.
		routeStatus := routeStatuses(g, statuses, routeMemberReach)

		for _, dep := range g.Deps {
			if dep.Lifecycle == domain.LifecycleRetired {
				continue
			}
			consumer, ok := statuses[dep.ConsumerServiceID]
			if !ok {
				continue
			}

			providerStatus, ownStatus, providerName, ok := providerHealth(g, dep, statuses, routeStatus)
			if !ok {
				continue
			}

			// Seam 2: a pairwise partition downgrades the provider's status
			// before Propagate runs, so the dependency's hard/soft/startup/
			// async/optional table does all the semantic work unchanged.
			//
			// ownStatus is the provider's health with no network effect
			// anywhere in the chain, and it is kept because the downgrade is
			// deliberately lossy: a provider that is perfectly healthy but cut
			// off from this consumer arrives at Propagate looking exactly like
			// one that has crashed. That is right for deciding the consumer's
			// status and wrong for explaining it -- "pg-main is down" sends an
			// operator to inspect a service that is green, during the incident
			// when they can least afford the detour.
			switch reachOfDep[dep.ID] {
			case reachDown:
				providerStatus = domain.StatusDown
			case reachDegraded:
				providerStatus = providerStatus.Worse(domain.StatusDegraded)
			case reachUnknown, reachOK:
				// Nothing to say: either the path is fine, or there is no
				// opinion to offer. Named rather than left to a default so a
				// fourth reach level cannot be added and silently mean "fine".
			}

			// netEffect is derived from the gap between the two statuses rather
			// than from reachOfDep, because reachOfDep only describes the
			// consumer-to-provider hop. A route whose backends are unreachable
			// from its own frontend was already downgraded inside
			// routeStatuses, and that consumer's own hop is fine -- reading
			// reachOfDep alone would call that a crash and send somebody to the
			// wrong place, which is the very bug this pair exists to prevent.
			// The gap catches both shapes with one rule.
			netEffect := reachUnknown
			if providerStatus != ownStatus {
				netEffect = reachDegraded
				if providerStatus == domain.StatusDown {
					netEffect = reachDown
				}
			}

			effect := dep.Propagate(providerStatus, req.WindowSeconds)

			if _, already := wontRestart[dep.ConsumerServiceID]; effect.WontRestart && !already {
				unmet := " is unmet"
				if netEffect != reachUnknown {
					unmet = " is unreachable from here"
				}
				wontRestart[dep.ConsumerServiceID] = startupFault{
					Via: providerName,
					Reason: "startup dependency on " + providerName + unmet +
						": running now, will not come back after a restart",
					NetEffect: netEffect,
				}
				changed = true
			}

			merged := consumer.Worse(effect.Status)
			if merged != consumer {
				statuses[dep.ConsumerServiceID] = merged
				via[dep.ConsumerServiceID] = providerName
				viaPartition[dep.ConsumerServiceID] = netEffect != reachUnknown
				reasons[dep.ConsumerServiceID] = propagationReason(dep, providerStatus, providerName, netEffect)
				changed = true
			}
		}

		if !changed {
			return via, viaPartition, wontRestart, iterations
		}
	}
	return via, viaPartition, wontRestart, maxIterations
}

// tallyMember folds one backend's status into the alive/degraded counts. A
// degraded member is alive -- it is still serving, just not well.
func tallyMember(status domain.Status, alive, degraded int) (int, int) {
	switch status {
	case domain.StatusDown:
		return alive, degraded // contributes nothing
	case domain.StatusDegraded:
		return alive + 1, degraded + 1
	default:
		return alive + 1, degraded
	}
}

func poolVerdict(alive, degraded, members int) domain.Status {
	switch {
	case alive == 0:
		return domain.StatusDown
	case alive < members || degraded > 0:
		return domain.StatusDegraded
	default:
		return domain.StatusOK
	}
}

// providerHealth resolves the status of whatever a dependency points at.
//
// It returns the status twice: as the consumer experiences it, and as it would
// be with no network effect anywhere in the chain. For a direct endpoint those
// are the same value; for a route they diverge exactly when the pool's verdict
// was decided by reachability rather than by a backend actually failing.
func providerHealth(
	g *Graph,
	dep domain.Dependency,
	statuses map[string]domain.Status,
	routeStatus map[string]routeHealth,
) (status, bare domain.Status, name string, ok bool) {
	if dep.ProviderEndpointID != nil {
		ep, found := g.Endpoints[*dep.ProviderEndpointID]
		if !found {
			return domain.StatusOK, domain.StatusOK, "", false
		}
		svc := g.Services[ep.ServiceID]
		if svc == nil {
			return domain.StatusOK, domain.StatusOK, "", false
		}
		own := statuses[ep.ServiceID]
		return own, own, svc.Code + "/" + ep.Name, true
	}
	if dep.ProviderRouteID != nil {
		r, found := g.Routes[*dep.ProviderRouteID]
		if !found {
			return domain.StatusOK, domain.StatusOK, "", false
		}
		rh := routeStatus[r.ID]
		return rh.Status, rh.Bare, "route " + r.Name, true
	}
	return domain.StatusOK, domain.StatusOK, "", false
}

// routeStatuses derives each route's health from its proxy and its pool.
//
// This is what the handover means by "routes are nodes in the graph, not
// passthroughs": a proxy that is up but whose every backend sits on the host
// being rebooted is not serving, and only pool-level derivation shows that.
// routeHealth is a route's status with reachability applied, alongside what it
// would have been without it.
//
// Both are needed because a route launders the network effect: routeStatuses
// folds routeMemberReach into the pool arithmetic before phasePropagate ever
// sees the route, so a route whose backends are merely unreachable arrives
// looking exactly like one whose backends crashed. Comparing Status against
// Bare is what lets the reason string tell those apart -- the same distinction
// propagationReason draws for a direct endpoint, which would otherwise be lost
// for every consumer that reaches its provider through a proxy.
type routeHealth struct {
	Status domain.Status
	Bare   domain.Status
}

func routeStatuses(g *Graph, statuses map[string]domain.Status, routeMemberReach map[string]reachLevel) map[string]routeHealth {
	out := make(map[string]routeHealth, len(g.Routes))
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

		// The pool is scored twice over the same members: once with
		// reachability applied and once ignoring it. The pair is what makes a
		// partition distinguishable from a crash one hop further down.
		poolStatus, poolBare := domain.StatusOK, domain.StatusOK
		if len(members) > 0 {
			alive, degraded := 0, 0
			bareAlive, bareDegraded := 0, 0
			for _, endpointID := range members {
				ep, ok := g.Endpoints[endpointID]
				if !ok {
					continue
				}
				own := statuses[ep.ServiceID]
				// Seam 2 at pool-member granularity: a member's own status is
				// downgraded by its reachability from the route's frontend
				// before it is counted, so "every backend is on the far side
				// of the break" is caught the same way host loss already is.
				memberStatus := own
				switch routeMemberReach[r.ID+"/"+endpointID] {
				case reachDown:
					memberStatus = domain.StatusDown
				case reachDegraded:
					memberStatus = memberStatus.Worse(domain.StatusDegraded)
				case reachUnknown, reachOK:
					// The member's own status stands.
				}
				alive, degraded = tallyMember(memberStatus, alive, degraded)
				bareAlive, bareDegraded = tallyMember(own, bareAlive, bareDegraded)
			}
			poolStatus = poolVerdict(alive, degraded, len(members))
			poolBare = poolVerdict(bareAlive, bareDegraded, len(members))
		}
		out[id] = routeHealth{
			Status: frontend.Worse(poolStatus),
			Bare:   frontend.Worse(poolBare),
		}
	}
	return out
}

// propagationReason explains a propagated status in the consumer's terms.
//
// netEffect reports that the provider's status was forced by the network rather
// than by the provider's own health, and replaces "is down" with a description
// of the path. The two are the same fact for the consumer and completely
// different instructions for the person reading the report: one says go and fix
// the provider, the other says go and fix the path.
//
// The distinction between the two network levels is kept because they call for
// different urgency. A severed path is an outage; a path that survives only
// through a failover leg is a warning that the next failure has nowhere to go.
func propagationReason(dep domain.Dependency, providerStatus domain.Status, providerName string, netEffect reachLevel) string {
	state := string(providerStatus)
	switch netEffect {
	case reachDown:
		state = "unreachable"
	case reachDegraded:
		state = "reachable only over a degraded path"
	case reachUnknown, reachOK:
		// The provider's own status is the whole story; the network added
		// nothing, and saying otherwise would be the false lead in reverse.
	}
	switch dep.Nature {
	case domain.NatureHard:
		return "hard dependency on " + providerName + " is " + state
	case domain.NatureSoft:
		return "soft dependency on " + providerName + " is " + state +
			"; degraded but still serving"
	case domain.NatureAsync:
		if netEffect == reachDown {
			return "async dependency on " + providerName +
				" has been unreachable for longer than its tolerance window"
		}
		return "async dependency on " + providerName +
			" is down for longer than its tolerance window"
	default:
		return "dependency on " + providerName + " is " + state
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
