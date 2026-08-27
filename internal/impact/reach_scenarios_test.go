// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
)

// ---------------------------------------------------------------------------
// The design's numbered scenarios, asserted against the M5 fixture.
//
// docs/reachability-design.md argues the reachability model is correct by
// walking fourteen named scenarios. Five of them are worked all the way to a
// printed report (lines 602-663) against exactly the topology internal/seed now
// ships. This file turns that argument into a test: one table entry per
// scenario, each naming the guarantee it defends, so a failure at 03:00 says
// which claim in the design stopped being true rather than merely printing a
// diff.
//
// Two rules govern every expectation below.
//
//  1. The expectation comes from the DESIGN, never from a run. Where the doc
//     and the built engine disagree, the disagreement is recorded in the
//     scenario's comment with both readings, and the assertion follows whichever
//     one is actually load-bearing -- never a number copied out of a report
//     because it happened to be what came back.
//
//  2. Where a scenario cannot be expressed at all, it is skipped with the
//     reason, and never approximated by a weaker assertion dressed up as the
//     real one. Scenarios 1, 2 and 6 lose half their expectation this way, and
//     scenario 9 all of it; each carries a tripwire that fails the moment the
//     underlying defect is fixed, so the skip cannot outlive it silently.
//
// Scenarios 1-11 are recovered from explicit "Scenario N" citations in the
// design. 12, 13 and 14 have no citation anywhere in the repository -- they are
// reconstructions, marked as such, and every assertion in them is derived from
// a quoted design rule rather than from a guessed report.
// ---------------------------------------------------------------------------

// scenarioCase is one row of the table.
type scenarioCase struct {
	// number and title identify the scenario in the design doc, so a failing
	// subtest names the guarantee before anyone opens a file.
	number int
	title  string
	// guarantee is the design claim this row defends, in the design's own
	// terms. It is printed on every failure.
	guarantee string
	// evidence cites where in docs/reachability-design.md the claim lives.
	evidence string
	// skip, when set, is the reason this scenario cannot be asserted today.
	skip string
	// setup prepares the fixture beyond the seeded M5 topology and returns any
	// asset ids it created, keyed by a name the check can use.
	setup func(t *testing.T, f *fixture) map[string]string
	// down is the simulated outage, by seeded asset name.
	down []string
	// check asserts the scenario. A few scenarios run further simulations of
	// their own (a mirror outage, or the same outage with the topology retired);
	// each gets its own fixture, so mutating it inside check is safe.
	check func(t *testing.T, s *scenarioRun)
}

// scenarioRun is one scenario's fixture and its primary result.
type scenarioRun struct {
	tc     scenarioCase
	f      *fixture
	result impact.Result
	byCode map[string]impact.ServiceImpact
	extra  map[string]string // asset ids created by setup
}

func TestDesignScenarios(t *testing.T) {
	for _, tc := range designScenarios() {
		t.Run(fmt.Sprintf("scenario_%02d_%s", tc.number, slug(tc.title)), func(t *testing.T) {
			if tc.skip != "" {
				t.Skipf("scenario %d (%s) cannot be asserted: %s", tc.number, tc.title, tc.skip)
			}
			f := newFixture(t)
			extra := map[string]string{}
			if tc.setup != nil {
				if got := tc.setup(t, f); got != nil {
					extra = got
				}
			}
			result, byCode := f.simulate(t, 180, tc.down...)
			tc.check(t, &scenarioRun{tc: tc, f: f, result: result, byCode: byCode, extra: extra})
		})
	}
}

func slug(title string) string {
	r := strings.NewReplacer(" ", "_", ",", "", "'", "", "--", "-", "/", "_")
	return r.Replace(title)
}

// ---------------------------------------------------------------------------
// Failure reporting: every message states the invariant that broke.
// ---------------------------------------------------------------------------

// broke reports a violated design guarantee. The message deliberately leads
// with the guarantee and its citation: the person reading this is mid-incident
// or mid-bisect, and "Services[2] != want" tells them nothing about which
// property of the model stopped holding.
func (s *scenarioRun) broke(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Errorf("scenario %d (%s) violated\n  GUARANTEE: %s\n  EVIDENCE:  %s\n  BROKE:     %s",
		s.tc.number, s.tc.title, s.tc.guarantee, s.tc.evidence, fmt.Sprintf(format, args...))
}

func (s *scenarioRun) brokeFatal(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf("scenario %d (%s) violated\n  GUARANTEE: %s\n  EVIDENCE:  %s\n  BROKE:     %s",
		s.tc.number, s.tc.title, s.tc.guarantee, s.tc.evidence, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------
// Assertion helpers over impact.Result
// ---------------------------------------------------------------------------

// wantService is one expected entry of Result.Services. Status and Cause are
// always checked; the rest only when set, so a scenario asserts the fields its
// guarantee is actually about and stays silent about the others.
type wantService struct {
	Status       domain.Status
	Cause        impact.Cause
	Via          string // checked when non-empty
	ReasonHas    string // checked when non-empty
	ReasonHasNot string // checked when non-empty
	// Lost/Total/ToIsolation are checked as a group, only when Total > 0. The
	// arithmetic is the scenario's subject in 4, 10 and 11 and irrelevant
	// elsewhere.
	Lost, Total, ToIsolation int
}

// assertServices checks Result.Services as an exact set: a service reported
// that the design does not name is as much a failure as one missing, because
// an impact report that lists extra services is the failure mode that gets
// impact reports ignored.
func (s *scenarioRun) assertServices(t *testing.T, want map[string]wantService) {
	t.Helper()
	// Sorted, so a failure reads the same way twice: a bisect that reorders its
	// own output costs more time than it saves.
	codes := make([]string, 0, len(want))
	for code := range want {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		w := want[code]
		got, ok := s.byCode[code]
		if !ok {
			s.broke(t, "%s is not reported affected at all, want %s; the whole report was %v",
				code, w.Status, codesOf(s.result.Services))
			continue
		}
		if got.Status != w.Status {
			s.broke(t, "%s status = %s, want %s (reason %q)", code, got.Status, w.Status, got.Reason)
		}
		if got.Cause != w.Cause {
			s.broke(t, "%s cause = %q, want %q -- Cause answers \"where do I go to fix this?\", "+
				"so getting it wrong sends an operator to the wrong half of the estate (reason %q, via %q)",
				code, got.Cause, w.Cause, got.Reason, got.Via)
		}
		if w.Via != "" && got.Via != w.Via {
			s.broke(t, "%s Via = %q, want %q -- Via names the edge that carried the status", code, got.Via, w.Via)
		}
		if w.ReasonHas != "" && !strings.Contains(got.Reason, w.ReasonHas) {
			s.broke(t, "%s reason = %q, want it to contain %q", code, got.Reason, w.ReasonHas)
		}
		if w.ReasonHasNot != "" && strings.Contains(got.Reason, w.ReasonHasNot) {
			s.broke(t, "%s reason = %q, want it NOT to contain %q", code, got.Reason, w.ReasonHasNot)
		}
		if w.Total > 0 {
			if got.LostInstances != w.Lost || got.TotalInstances != w.Total {
				s.broke(t, "%s lost %d of %d instances, want %d of %d -- the capacity arithmetic is "+
					"what lets a surprising status be checked rather than merely believed",
					code, got.LostInstances, got.TotalInstances, w.Lost, w.Total)
			}
			if got.LostToIsolation != w.ToIsolation {
				s.broke(t, "%s LostToIsolation = %d, want %d -- this is the sub-count that lets the "+
					"report say \"running but cut off\" instead of \"lost\"",
					code, got.LostToIsolation, w.ToIsolation)
			}
		}
	}
	for _, got := range s.result.Services {
		if _, expected := want[got.Code]; !expected {
			s.broke(t, "%s is reported %s (cause %s, reason %q) but the design names no effect on it",
				got.Code, got.Status, got.Cause, got.Reason)
		}
	}
}

func (s *scenarioRun) assertNoServices(t *testing.T, why string) {
	t.Helper()
	if len(s.result.Services) != 0 {
		s.broke(t, "%d services reported affected, want none -- %s: %v",
			len(s.result.Services), why, codesOf(s.result.Services))
	}
}

// assetName resolves an asset id back to its fixture name for readable
// failures. Result.Isolated carries opaque ids, and a diff of UUIDs is exactly
// the failure message this file exists to avoid.
func (s *scenarioRun) assetName(id string) string {
	for name, assetID := range s.f.refs.Assets {
		if assetID == id {
			return name
		}
	}
	for name, assetID := range s.extra {
		if assetID == id {
			return name
		}
	}
	return id
}

func (s *scenarioRun) isolatedNames(plane string) []string {
	var out []string
	for _, iso := range s.result.Isolated {
		if iso.Plane == plane {
			out = append(out, s.assetName(iso.AssetID))
		}
	}
	sort.Strings(out)
	return out
}

// assertIsolated checks the isolated set on one plane exactly, and that every
// entry blames the group the design names.
func (s *scenarioRun) assertIsolated(t *testing.T, plane string, blockingGroup string, want []string) {
	t.Helper()
	got := s.isolatedNames(plane)
	sorted := append([]string{}, want...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		s.broke(t, "isolated on plane %s = %v, want %v", plane, got, sorted)
	}
	for _, iso := range s.result.Isolated {
		if iso.Plane != plane {
			continue
		}
		if iso.BlockingGroup != blockingGroup {
			s.broke(t, "%s is isolated on %s blamed on group %q, want %q -- the blocking group is "+
				"the only actionable field in an isolation finding",
				s.assetName(iso.AssetID), plane, iso.BlockingGroup, blockingGroup)
		}
	}
}

func (s *scenarioRun) assertNothingIsolated(t *testing.T, why string) {
	t.Helper()
	if len(s.result.Isolated) != 0 {
		var got []string
		for _, iso := range s.result.Isolated {
			got = append(got, s.assetName(iso.AssetID)+"/"+iso.Plane)
		}
		sort.Strings(got)
		s.broke(t, "%d assets reported isolated, want none -- %s: %v", len(s.result.Isolated), why, got)
	}
}

func partitionKeys(in []impact.EdgePartition) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, fmt.Sprintf("%s -> %s = %s", p.ConsumerService, p.ProviderName, p.Reach))
	}
	sort.Strings(out)
	return out
}

// assertPartitions checks Result.Partitions as an exact set of
// "consumer -> provider = reach" strings.
func (s *scenarioRun) assertPartitions(t *testing.T, want []string) {
	t.Helper()
	got := partitionKeys(s.result.Partitions)
	sorted := append([]string{}, want...)
	sort.Strings(sorted)
	if strings.Join(got, " | ") != strings.Join(sorted, " | ") {
		s.broke(t, "partitioned edges = %v, want %v", got, sorted)
	}
}

func (s *scenarioRun) assertNoPartitions(t *testing.T, why string) {
	t.Helper()
	if len(s.result.Partitions) != 0 {
		s.broke(t, "%d dependency edges reported partitioned, want none -- %s: %v",
			len(s.result.Partitions), why, partitionKeys(s.result.Partitions))
	}
}

// wantGroup is one expected Result.RedundancyLost entry.
//
// PromotionPending is the design's own distinction and the only observable
// difference between scenarios 1 and 2: a group evaluated DEGRADED survives
// only until somebody promotes a standby by hand, while a group evaluated OK
// simply has no spare left. The message must say which, because "1 of 2
// surviving" alone does not tell an operator whether anything is currently
// broken.
type wantGroup struct {
	Code             string
	Surviving, Total int
	PromotionPending bool
}

func (s *scenarioRun) assertRedundancyLost(t *testing.T, want []wantGroup) {
	t.Helper()
	if len(s.result.RedundancyLost) != len(want) {
		var got []string
		for _, g := range s.result.RedundancyLost {
			got = append(got, g.GroupCode)
		}
		s.brokeFatal(t, "RedundancyLost holds %d entries %v, want %d", len(s.result.RedundancyLost), got, len(want))
	}
	byGroup := map[string]impact.GroupFinding{}
	for _, g := range s.result.RedundancyLost {
		byGroup[g.GroupCode] = g
	}
	for _, w := range want {
		g, ok := byGroup[w.Code]
		if !ok {
			s.brokeFatal(t, "no RedundancyLost entry for group %q", w.Code)
		}
		count := fmt.Sprintf("%d of %d members surviving", w.Surviving, w.Total)
		if !strings.Contains(g.Message, count) {
			s.broke(t, "RedundancyLost[%s] = %q, want it to state %q", w.Code, g.Message, count)
		}
		mentionsPromotion := strings.Contains(g.Message, "promot")
		if mentionsPromotion != w.PromotionPending {
			if w.PromotionPending {
				s.broke(t, "RedundancyLost[%s] = %q, but this group evaluated DEGRADED: the message must "+
					"say the surviving path depends on a standby being promoted by hand", w.Code, g.Message)
			} else {
				s.broke(t, "RedundancyLost[%s] = %q, but this group evaluated OK: nothing needs promoting, "+
					"and saying so turns a warning about the NEXT failure into a false alarm about this one",
					w.Code, g.Message)
			}
		}
	}
}

func (s *scenarioRun) assertCounts(t *testing.T, wontRestart, cycles, iterations int) {
	t.Helper()
	if len(s.result.WontRestart) != wontRestart {
		s.broke(t, "WontRestart holds %d entries %v, want %d",
			len(s.result.WontRestart), wontRestartCodes(s.result), wontRestart)
	}
	if len(s.result.Cycles) != cycles {
		s.broke(t, "Cycles holds %d entries %v, want %d", len(s.result.Cycles), s.result.Cycles, cycles)
	}
	if s.result.Iterations != iterations {
		s.broke(t, "the fixed point took %d iterations, want %d -- a changed iteration count means the "+
			"propagation shape changed, even when every status happens to land the same way",
			s.result.Iterations, iterations)
	}
}

// wantReach is one expected row of the exposure channel.
type wantReach struct {
	Code          string
	EndpointName  string
	Scope         string
	Status        domain.Status
	BlockingGroup string
}

// assertUnreachable checks the exposure channel exactly: same rows, same order,
// no extras. This channel was dead for three milestones -- betterFoldAnchors
// asked reach() to resolve a net_group id as an asset id, so it returned
// ok=false every time and the four declared anchors were consumed by nothing.
// Asserting the contents rather than merely a non-zero count is what stops it
// dying quietly a second time.
func (s *scenarioRun) assertUnreachable(t *testing.T, want []wantReach) {
	t.Helper()
	if len(s.result.Unreachable) != len(want) {
		s.broke(t, "Unreachable has %d rows, want %d\n  got:  %+v\n  want: %+v",
			len(s.result.Unreachable), len(want), s.result.Unreachable, want)
		return
	}
	for i, w := range want {
		got := s.result.Unreachable[i]
		if got.Code != w.Code || got.EndpointName != w.EndpointName || got.Scope != w.Scope ||
			got.Status != w.Status || got.BlockingGroup != w.BlockingGroup {
			s.broke(t, "Unreachable[%d] = %s/%s scope=%s %s blocked=%s, want %s/%s scope=%s %s blocked=%s",
				i, got.Code, got.EndpointName, got.Scope, got.Status, got.BlockingGroup,
				w.Code, w.EndpointName, w.Scope, w.Status, w.BlockingGroup)
		}
	}
}

// assertNoUnreachable is the negative form, and it carries a why so a future
// reader can tell "correctly silent" from "silently broken again".
func (s *scenarioRun) assertNoUnreachable(t *testing.T, why string) {
	t.Helper()
	if len(s.result.Unreachable) != 0 {
		s.broke(t, "Unreachable has %d rows, want none -- %s\n  got: %+v",
			len(s.result.Unreachable), why, s.result.Unreachable)
	}
}

// theNineInternalServices is every service the design says must be untouched by
// a break at the estate's edge (docs/reachability-design.md:625).
var theNineInternalServices = []string{
	"orders-api", "orders-web", "sso", "vault", "pgsql-core",
	"rabbitmq", "mimir-ingester", "haproxy-edge", "backup-agent",
}

// assertNoInternalCascade is the load-bearing half of scenario 6, and it holds
// under BOTH readings of the report: today the exposure channel is inert and
// Services is empty; under the design it holds two entries, both Cause=exposure.
// Either way, no service may be moved by a break it sits entirely on one side
// of, and no exposure finding may propagate into a dependent.
func (s *scenarioRun) assertNoInternalCascade(t *testing.T) {
	t.Helper()
	for _, si := range s.result.Services {
		if si.Cause == impact.CauseExposure {
			continue // reported, never propagated -- allowed
		}
		s.broke(t, "%s is reported %s with cause %s, but every consumer and provider host in this "+
			"outage resolves to the same forwarder group on the same side of the break. Reachability is "+
			"computed per dependency EDGE, never per service, so a provider stays reachable to any "+
			"consumer on its own side (reason %q)", si.Code, si.Status, si.Cause, si.Reason)
	}
	for _, code := range theNineInternalServices {
		if si, ok := s.byCode[code]; ok && si.Cause != impact.CauseExposure {
			s.broke(t, "internal service %s changed status (%s, %q)", code, si.Status, si.Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// The table
// ---------------------------------------------------------------------------

func designScenarios() []scenarioCase {
	return []scenarioCase{
		// -------------------------------------------------------------------
		{
			number: 1,
			title:  "active-passive edge pair, primary lost, promotion is manual",
			guarantee: "losing the primary of an active_passive pair whose failover_mode is 'manual' " +
				"evaluates DEGRADED, not ok and not down: the standby can take over, but only after a " +
				"human promotes it, so the group is in the optimistic union-find pass and not the " +
				"pessimistic one, and no internal traffic is affected",
			evidence: "docs/reachability-design.md:524 (HA table), :531, :602-625 (worked example)",
			down:     []string{"fw-edge-1"},
			check: func(t *testing.T, s *scenarioRun) {
				// The assertable half. fw-edge is degraded, which the message
				// variant is the only observable proof of.
				s.assertRedundancyLost(t, []wantGroup{
					{Code: "fw-edge", Surviving: 1, Total: 2, PromotionPending: true},
				})

				// Nothing is isolated: no attachment loses a member, because no
				// host attaches to fw-edge at all (design :606).
				s.assertNothingIsolated(t, "no host attaches to the edge pair; every attachment is to sw-core")
				s.assertNoPartitions(t, "every consumer and provider resolves to sw-core, in the same pessimistic component")
				s.assertCounts(t, 0, 0, 1)

				// Scenario 6 rides on the same run: the nine internal services
				// keep serving each other.
				s.assertNoInternalCascade(t)

				// ...and the half the design cares most about: nothing inside
				// changed, and the way out is now one failure from gone.
				s.assertServices(t, map[string]wantService{
					"haproxy-edge": {Status: domain.StatusDegraded, Cause: impact.CauseExposure,
						ReasonHas: "requires a external anchor"},
					"partner-gateway": {Status: domain.StatusDegraded, Cause: impact.CauseExposure,
						ReasonHas: "requires a cross_env anchor"},
				})
				s.assertUnreachable(t, []wantReach{
					{Code: "haproxy-edge", EndpointName: "https", Scope: "external",
						Status: domain.StatusDegraded, BlockingGroup: "fw-edge"},
					{Code: "partner-gateway", EndpointName: "https", Scope: "cross_env",
						Status: domain.StatusDegraded, BlockingGroup: "fw-edge"},
				})
			},
		},

		// -------------------------------------------------------------------
		{
			number: 2,
			title:  "same pair with automatic failover is a blip, not an outage",
			guarantee: "the identical loss with failover_mode='auto' evaluates OK: the group is in BOTH " +
				"union-find passes, every path through it stays ok, no service is affected, and the only " +
				"finding is a RedundancyLost note that must NOT claim anything needs promoting",
			evidence: "docs/reachability-design.md:525 (HA table), :531, :625",
			// There is no UpdateNetGroup in the store -- failover_mode is not
			// editable after creation -- and net_group.code is a plain UNIQUE,
			// not partial on lifecycle, so retiring the seeded group does not
			// free its code. The M5 shape is therefore rebuilt verbatim under
			// test-scoped codes with the single value flipped. Everything else
			// (kind, availability, members and their primary/standby roles,
			// uplink, all three attachments and their pins, all four anchors) is
			// identical to internal/seed/seed_topology.go, so the comparison
			// against scenario 1 isolates failover_mode and nothing else.
			setup: func(t *testing.T, f *fixture) map[string]string {
				rebuildM5Topology(t, f, domain.FailoverAuto)
				return nil
			},
			down: []string{"fw-edge-1"},
			check: func(t *testing.T, s *scenarioRun) {
				s.assertNoServices(t, "an automatic failover is a blip; nobody has to be paged and nothing changes status")
				s.assertNothingIsolated(t, "no host attaches to the edge pair")
				s.assertNoPartitions(t, "the group is in both passes, so no edge's reach changes")
				s.assertCounts(t, 0, 0, 1)
				s.assertRedundancyLost(t, []wantGroup{
					{Code: autoEdgeGroupCode, Surviving: 1, Total: 2, PromotionPending: false},
				})

				// This is the design's stated difference between scenarios 1
				// and 2, and it is the substantive one: with automatic failover
				// the group stays in BOTH union-find passes, so every anchor
				// behind it is still fully reachable and nothing is exposed.
				// Scenario 1 reports two exposed services here; this reports
				// none. Without it the two scenarios would differ only by the
				// wording of a redundancy note.
				s.assertNoUnreachable(t, "an automatic failover keeps the group in the pessimistic pass, "+
					"so the path to every anchor behind it stays ok")
			},
		},

		// -------------------------------------------------------------------
		{
			number: 3,
			title:  "MC-LAG core pair, lose one chassis, dual-homed hosts survive",
			guarantee: "an active_active group with min_healthy=1 that keeps one of two chassis evaluates " +
				"OK, so traffic survives; and no host is cut off, because every attachment still has one " +
				"live pinned chassis. No MC-LAG-specific code path exists -- the second " +
				"net_attachment_member row does all of the work",
			evidence: "docs/reachability-design.md:536-547, :627-639 (worked example)",
			down:     []string{"sw-core-1"},
			check: func(t *testing.T, s *scenarioRun) {
				s.assertNoServices(t, "1 of 2 surviving meets min_healthy=1, and hv-03 was never pinned to sw-core-1")
				s.assertNothingIsolated(t, "hv-01 and hv-02 keep sw-core-2; hv-03 is pinned to sw-core-2 only")
				s.assertNoPartitions(t, "one live group means one component")
				s.assertCounts(t, 0, 0, 1)
				s.assertRedundancyLost(t, []wantGroup{
					{Code: "sw-core", Surviving: 1, Total: 2, PromotionPending: false},
				})
				// sw-oob has neither an uplink nor an anchor, which is what an
				// out-of-band switch honestly is. Coverage says so out loud
				// rather than letting a silently unreferenced group look modelled.
				if got := s.result.Coverage.GroupsWithoutUplinkOrAnchor; got != 1 {
					s.broke(t, "Coverage.GroupsWithoutUplinkOrAnchor = %d, want 1 (sw-oob has neither)", got)
				}
			},
		},

		// -------------------------------------------------------------------
		{
			number: 4,
			title:  "the mirror: the group is OK and the single-homed host is still cut off",
			guarantee: "losing the OTHER chassis leaves the group at 1-of-2 and therefore OK, while hv-03's " +
				"pinned member set {sw-core-2} is exhausted, so hv-03 and everything it contains is " +
				"ISOLATED. This is what no single-nullable-column design can express: group health and " +
				"host reachability are different questions with different answers in the same run",
			evidence: "docs/reachability-design.md:549-559 (the trap table), :641 (worked mirror case)",
			down:     []string{"sw-core-2"},
			check: func(t *testing.T, s *scenarioRun) {
				s.assertServices(t, map[string]wantService{
					// Directly isolated: running, not powered off.
					"sso": {Status: domain.StatusDown, Cause: impact.CauseReachability,
						Lost: 1, Total: 1, ToIsolation: 1,
						ReasonHas: "running, but network-isolated"},
					"partner-gateway": {Status: domain.StatusDown, Cause: impact.CauseReachability,
						Lost: 1, Total: 1, ToIsolation: 1,
						ReasonHas: "running, but network-isolated"},
					"mimir-ingester": {Status: domain.StatusDown, Cause: impact.CauseReachability,
						Lost: 2, Total: 2, ToIsolation: 2,
						ReasonHas: "at least one shard has no surviving replica"},
					"orders-api-dev": {Status: domain.StatusDown, Cause: impact.CauseReachability,
						Lost: 1, Total: 1, ToIsolation: 1,
						ReasonHas: "running, but network-isolated"},
					// Propagated. vault's own capacity verdict is OK (scenario
					// 11); it is degraded only through the soft edge to sso.
					//
					// Cause is DEPENDENCY even though vault also lost an
					// instance to isolation, because that loss did not decide
					// this status -- quorum held at 2 of 3 -- and Cause must
					// describe whatever did. The proof is orders-api two lines
					// down: same edge, same provider, byte-identical Reason and
					// Via, zero instances lost. If vault read as reachability
					// the two rows would give contradictory guidance about one
					// provider, and the row would say "the network, not the
					// service" about sso, which is 100% down and listed above.
					// Follow Via to sso and sso's own row says reachability;
					// that is where the network fact belongs.
					"vault": {Status: domain.StatusDegraded, Cause: impact.CauseDependency,
						Via: "sso/https", Lost: 1, Total: 3, ToIsolation: 1},
					"orders-api": {Status: domain.StatusDegraded, Cause: impact.CauseDependency,
						Via: "sso/https", Lost: 0, Total: 2, ToIsolation: 0},
					"orders-web": {Status: domain.StatusDegraded, Cause: impact.CauseDependency,
						Via: "orders-api/http", Lost: 0, Total: 1, ToIsolation: 0},
				})

				// The six the design names by hand -- hv-03 plus the five guests
				// that own no attachment row and inherit through asset_closure --
				// and, since the virtual layer landed, hv-03's bridge as well.
				//
				// hv-03-br0 is here because it is a real asset contained by
				// hv-03, and the guarantee this scenario defends says "hv-03 AND
				// EVERYTHING IT CONTAINS is isolated". Nothing about the engine
				// changed; the estate gained a seventh asset behind the same
				// exhausted pin, and it inherits the attachment by exactly the
				// mechanism the five guests do. The list is a census of the
				// fixture, not a design constant, so it grows when the fixture
				// does.
				//
				// Worth a second look at some point, and deliberately NOT
				// changed here: a bridge is neither a workload host nor a
				// forwarder anyone can go and touch, so listing it beside the
				// hypervisor it lives inside adds a row an operator cannot act
				// on. Narrowing computeIsolated -- to assets that host instances
				// or own an attachment, say -- would drop it and keep all six
				// originals, but that is an impact-engine semantics change and
				// belongs in its own decision, not in a seed change.
				s.assertIsolated(t, domain.PlaneData, "sw-core", []string{
					"hv-03", "hv-03-br0", "vm-vault-3", "vm-sso-1", "vm-k8s-1", "vm-k8s-2", "vm-dev-1",
				})

				// This is isolation, not partition: the host has no surviving
				// path at all, so its instances die at Seam 1 and the dependency
				// edges are never asked about.
				s.assertNoPartitions(t, "hv-03 has no live attachment, so its instances are dead before any edge is evaluated")
				s.assertCounts(t, 1, 1, 2)
				if len(s.result.WontRestart) == 1 && s.result.WontRestart[0].Code != "orders-web" {
					s.broke(t, "WontRestart names %q, want orders-web (its startup edge to sso)", s.result.WontRestart[0].Code)
				}
				s.assertRedundancyLost(t, []wantGroup{
					{Code: "sw-core", Surviving: 1, Total: 2, PromotionPending: false},
				})

				// The group is OK even while a host behind it is cut off. That
				// pairing is the entire point of the scenario, so assert it
				// rather than inferring it from the message text alone.
				if len(s.result.RedundancyLost) == 1 && strings.Contains(s.result.RedundancyLost[0].Message, "promot") {
					s.broke(t, "sw-core reads as needing a promotion; it is active_active at 1-of-2 and therefore OK")
				}
			},
		},

		// -------------------------------------------------------------------
		{
			number: 5,
			title:  "a LAG with two cables to one chassis is not redundancy",
			guarantee: "two cables from one host into the SAME chassis collapse to one " +
				"net_attachment_member row -- asset_id is in the primary key -- so they are correctly not " +
				"counted as redundancy: losing that chassis isolates the host even though the group " +
				"survives at 1-of-2, and losing the other chassis leaves it untouched",
			evidence: "docs/reachability-design.md:557 (trap table), :559, :176-191 (net_attachment_member)",
			// The M5 fixture spec at :598 declares no such host, so this
			// scenario builds one. The duplicate pin is attempted first,
			// because "two cables collapse to one row" is itself part of the
			// claim and is enforced by the primary key rather than by any Go
			// code that could be tested another way.
			setup: buildLAGgedHost,
			down:  []string{"sw-core-1"},
			check: func(t *testing.T, s *scenarioRun) {
				lagHost := s.extra["lag-host"]

				if _, ok := s.byCode["lag-svc"]; !ok {
					s.brokeFatal(t, "the service on the LAGged host is not reported affected at all; "+
						"both its cables land on sw-core-1, so it has no surviving path: %v",
						codesOf(s.result.Services))
				}
				got := s.byCode["lag-svc"]
				if got.Status != domain.StatusDown || got.Cause != impact.CauseReachability {
					s.broke(t, "lag-svc = %s/%s, want down/reachability", got.Status, got.Cause)
				}
				if got.LostToIsolation != got.LostInstances {
					s.broke(t, "lag-svc lost %d instances of which %d to isolation; the host is running and "+
						"cut off, so every loss here is an isolation loss, not a placement one",
						got.LostInstances, got.LostToIsolation)
				}

				var isolated bool
				for _, iso := range s.result.Isolated {
					if iso.AssetID != lagHost {
						continue
					}
					isolated = true
					if iso.Plane != domain.PlaneData || iso.BlockingGroup != "sw-core" {
						s.broke(t, "the LAGged host is isolated on plane %q blamed on %q, want data/sw-core",
							iso.Plane, iso.BlockingGroup)
					}
				}
				if !isolated {
					s.broke(t, "the LAGged host is not reported isolated; two cables to sw-core-1 are one "+
						"member, and that member is gone")
				}

				// The contrast row from the same table: hv-01 is dual-homed and
				// stays live. If it did not, the assertion above would be about
				// the group being down, not about the pin.
				for _, iso := range s.result.Isolated {
					if s.assetName(iso.AssetID) == "hv-01" {
						s.broke(t, "hv-01 is dual-homed {sw-core-1, sw-core-2} and must stay live in BOTH "+
							"directions; if it is isolated, the group went down and this scenario proves nothing")
					}
				}

				// Coverage counts this host as modelled rather than inherited:
				// it owns a direct data-plane attachment instead of inheriting
				// its parent's. The second modelled host is hv-01, which
				// carries the fixture's bare-metal backup agent and its own
				// attachment.
				if got := s.result.Coverage.Modelled; got != 2 {
					s.broke(t, "Coverage.Modelled = %d, want 2 -- the LAGged host and hv-01 each own a "+
						"direct data-plane attachment and carry an instance, which is exactly what "+
						"Modelled counts", got)
				}

				// The mirror: lose the other chassis and the host is fine.
				mirror, mirrorByCode := s.f.simulate(t, 180, "sw-core-2")
				if _, affected := mirrorByCode["lag-svc"]; affected {
					s.broke(t, "losing sw-core-2 affected lag-svc, whose only cables land on sw-core-1")
				}
				for _, iso := range mirror.Isolated {
					if iso.AssetID == lagHost {
						s.broke(t, "losing sw-core-2 isolated the host LAGged to sw-core-1")
					}
					if s.assetName(iso.AssetID) == "hv-01" {
						s.broke(t, "hv-01 is dual-homed and must stay live losing sw-core-2 as well")
					}
				}
			},
		},

		// -------------------------------------------------------------------
		{
			number: 6,
			title:  "reachability is a relation, not a property",
			guarantee: "a break at the estate's edge changes nothing for services on the same side of it. " +
				"Reachability is computed per dependency EDGE, never per service, so a provider stays " +
				"perfectly reachable to any consumer on its own side; and exposure loss is appended to " +
				"Result.Services after the fixed point, never written into statuses, so it cannot cascade " +
				"through a hard dependency",
			evidence: "docs/reachability-design.md:581, :583-584 (Channel 3), :625; internal/impact/engine.go:225-229",
			down:     []string{"fw-edge-1"},
			check: func(t *testing.T, s *scenarioRun) {
				// Form (a): the M5 fixture. Every consumer and provider host
				// resolves to sw-core, in the same pessimistic component.
				s.assertNoInternalCascade(t)
				s.assertNoPartitions(t, "no dependency edge crosses the break")
				s.assertNothingIsolated(t, "no host attaches to fw-edge")

				// The load-bearing one: partner-gateway depends HARD on the
				// route through haproxy-edge, whose external anchor is the thing
				// the design says is lost here. That must not cascade to down.
				if gw, ok := s.byCode["partner-gateway"]; ok {
					if gw.Status == domain.StatusDown {
						s.broke(t, "partner-gateway is DOWN. Its hard dependency runs through haproxy-edge, "+
							"which lost an anchor and is still serving from inside. Marking it down cascades a "+
							"falsehood through the route and turns the report into an alarm storm -- the exact "+
							"failure mode that gets impact reports ignored (reason %q)", gw.Reason)
					}
					if gw.Cause != impact.CauseExposure {
						s.broke(t, "partner-gateway is reported with cause %q; the only channel allowed to "+
							"name it in this outage is exposure, which is reported and never propagated", gw.Cause)
					}
				}

				// Form (b), the upstream-only shape -- two hosts behind one
				// access group, uplinked to an edge group neither attaches to --
				// is asserted by TestPairwiseSameSideStillReachable in
				// reach_test.go against a purpose-built topology. It is not
				// duplicated here: it needs its own three-group topology, and
				// declaring one would mean retiring the seeded fixture this
				// scenario is about.

				// The positive half, and it is what makes the scenario mean
				// something: the two edge-facing services ARE affected while
				// the nine internal ones carry on. Reachability is a relation,
				// so losing the way out is not the same as losing the estate.
				s.assertUnreachable(t, []wantReach{
					{Code: "haproxy-edge", EndpointName: "https", Scope: "external",
						Status: domain.StatusDegraded, BlockingGroup: "fw-edge"},
					{Code: "partner-gateway", EndpointName: "https", Scope: "cross_env",
						Status: domain.StatusDegraded, BlockingGroup: "fw-edge"},
				})
			},
		},

		// -------------------------------------------------------------------
		{
			number: 7,
			title:  "management switch loss leaves the estate unmanageable and no service affected",
			guarantee: "both planes are evaluated in the same run: the data plane decides status, the " +
				"management plane is report-only. Losing a switch that nothing attaches to on the data " +
				"plane moves no status at all, and every isolation finding it produces is on plane=mgmt",
			evidence: "docs/reachability-design.md:649-659, :350, :149-158",
			down:     []string{"sw-oob-1"},
			check: func(t *testing.T, s *scenarioRun) {
				s.assertNoServices(t, "no data-plane attachment, uplink or anchor references sw-oob")
				s.assertNoPartitions(t, "the data-plane graph is untouched")
				s.assertCounts(t, 0, 0, 1)
				if len(s.result.RedundancyLost) != 0 {
					s.broke(t, "RedundancyLost holds %d entries; sw-oob is a group of one and is now down, "+
						"which is reported as loss and not as lost redundancy", len(s.result.RedundancyLost))
				}

				// The doc's printed block (:653-657) says "Isolated (3) hv-01,
				// hv-02, hv-03". That is wrong for the built engine, and the
				// engine is right: the twelve VMs inherit their hypervisor's
				// management attachment through asset_closure exactly as they
				// inherit the data one, and inheritance is the whole point of
				// resolving attachments by nearest ancestor. So the invariant
				// asserted here is the one the scenario is actually about --
				// every finding is on the management plane and none on the data
				// plane -- not a count of 3.
				if len(s.result.Isolated) == 0 {
					s.brokeFatal(t, "nothing reported isolated; the three hypervisors' management NICs all "+
						"land on sw-oob-1 and it is gone")
				}
				for _, iso := range s.result.Isolated {
					if iso.Plane != domain.PlaneMgmt {
						s.broke(t, "%s is isolated on plane %q; losing a management switch must not touch "+
							"the data plane, and derivation sets plane from interface.is_mgmt on the HOST "+
							"side of the cable precisely so the seed's management cabling can never "+
							"masquerade as a data path", s.assetName(iso.AssetID), iso.Plane)
					}
					if iso.BlockingGroup != "sw-oob" {
						s.broke(t, "%s is isolated blamed on %q, want sw-oob", s.assetName(iso.AssetID), iso.BlockingGroup)
					}
				}
				mgmt := s.isolatedNames(domain.PlaneMgmt)
				for _, hv := range []string{"hv-01", "hv-02", "hv-03"} {
					if !contains(mgmt, hv) {
						s.broke(t, "%s is not reported management-isolated; the design names all three "+
							"hypervisors explicitly (got %v)", hv, mgmt)
					}
				}
			},
		},

		// -------------------------------------------------------------------
		{
			number: 8,
			title:  "bind_scope, not exposure, gates the local exemption",
			guarantee: "the local exemption is decided by bind_scope and nothing else. An endpoint that is " +
				"exposure=internal but bind_scope=host is a host port, genuinely network-affected between " +
				"hosts, and is correctly NOT exempt; an endpoint bound to a unix socket is intra-host by " +
				"definition and never receives a reachability downgrade. Using exposure for this (as " +
				"Proposal 4's environment floor does) fails this scenario",
			evidence: "docs/reachability-design.md:587, :408; internal/impact/reach.go:726-728",
			setup:    splitEstateWithALocalSocket,
			down:     []string{"fw-edge-1"},
			check: func(t *testing.T, s *scenarioRun) {
				s.assertServices(t, map[string]wantService{
					// The route's pool members orders-api/http and
					// orders-web/http are both bind_scope=host,
					// exposure=internal, and both sit on the far side of the
					// break from the proxy that fronts them. They ARE
					// downgraded; if exposure gated the exemption they would not
					// be, and partner-gateway would report nothing.
					"partner-gateway": {Status: domain.StatusDown, Cause: impact.CauseReachability,
						Via: "route orders.example.com", ReasonHas: "unreachable", ReasonHasNot: "is down"},
					"orders-api": {Status: domain.StatusDegraded, Cause: impact.CauseReachability,
						Via: "sso/https", ReasonHas: "unreachable"},
					"orders-web": {Status: domain.StatusDegraded, Cause: impact.CauseDependency,
						Via: "orders-api/http"},
				})

				s.assertPartitions(t, []string{
					"orders-api -> mimir-ingester/grpc = down",
					"orders-api -> rabbitmq/amqp = down",
					"orders-api -> sso/https = down",
					"orders-web -> sso/https = down",
					"vault -> sso/https = degraded",
				})

				// The counter-assertion that pins the rule in the other
				// direction. setup declared a unix-socket endpoint on orders-web
				// (side B) and an edge to it from sso (side A) -- a dependency
				// that crosses the break and is exempt anyway, because
				// isLocalBind short-circuits it to reachOK before any host pair
				// is compared. Without such an edge the rule is untested: the
				// seeded pgsql-core/local endpoint exists but nothing depends
				// on it, so asserting its absence from Partitions is vacuous.
				for _, p := range s.result.Partitions {
					if strings.HasSuffix(p.ProviderName, "/local-socket") || strings.HasSuffix(p.ProviderName, "/local") {
						s.broke(t, "%s -> %s is reported partitioned, but that endpoint is bound to a unix "+
							"socket: loopback and unix traffic is intra-host by definition and can never be "+
							"cut by a network break", p.ConsumerService, p.ProviderName)
					}
				}
				if si, ok := s.byCode["sso"]; ok {
					s.broke(t, "sso is reported %s (%q). Its only edge across the break is to a unix socket, "+
						"which is exempt", si.Status, si.Reason)
				}

				// And the premise: nothing here is isolated, so every finding
				// above is a partition proper rather than a host that lost its
				// last cable.
				s.assertNothingIsolated(t, "both halves keep a live attachment; this is a partition, not isolation")
			},
		},

		// -------------------------------------------------------------------
		{
			number: 9,
			title:  "kubernetes service addressing",
			guarantee: "for a cluster_ip/node_port/ingress endpoint, reachHosts resolves the CLUSTER's " +
				"k8s_node descendants rather than the alive instance hosts; an unresolvable cluster falls " +
				"back to instance hosts and increments Coverage.ClusterUnresolved; and pod-to-pod inside " +
				"one cluster fails OPEN, because a physical partition inside a CNI overlay is a claim this " +
				"model refuses to make",
			evidence: "docs/reachability-design.md:315, :432-435; internal/impact/reach.go:730-740",
			skip: "the fixture cannot express any of the three arms. internal/seed/seed_services.go:208 sets " +
				"rt_k8s.cluster_asset_id to the instance's HOST (vm-k8s-1 / vm-k8s-2, both kind='k8s_node'), " +
				"not to an asset of kind='cluster'. store/graph.go then resolves ClusterNodes by selecting " +
				"k8s_node descendants of that ancestor, so each 'cluster' degenerates to the single node it " +
				"already is: (a) cluster resolution is indistinguishable from instance-host resolution, " +
				"(b) ClusterUnresolved can never increment because cluster_asset_id is always set, and " +
				"(c) no consumer/provider pair shares a cluster, so the pod-to-pod fail-open at " +
				"reach.go:733-739 is never reached. M5 needs a real kind='cluster' asset parenting vm-k8s-1 " +
				"and vm-k8s-2 before any arm is assertable. Separately, the rt_k8s join at " +
				"store/graph.go:107-111 has no ORDER BY while ServiceClusterAssetID is a last-write-wins " +
				"map, so which of mimir-ingester's two nodes becomes 'the cluster' is engine-defined -- the " +
				"same portability hazard docs/DECISIONS.md already fixed for the dependency load.",
			check: func(t *testing.T, s *scenarioRun) {},
		},

		// -------------------------------------------------------------------
		{
			number: 10,
			title:  "rack loss: containment and reachability from one expansion, with no double count",
			guarantee: "SubtreeIDs is computed ONCE in Simulate and feeds both DownInstances and the " +
				"forwarder down set, so the chassis inside a falling rack go down as a CONSEQUENCE of the " +
				"rack rather than as a separate input; and because instance liveness is a single " +
				"`!down && !(needsNet && isolated)`, an instance already lost to placement is never " +
				"additionally charged to isolation. The rack answer is therefore unchanged by the whole " +
				"feature, with RedundancyLost added",
			evidence: "docs/reachability-design.md:501, :574, :661-663; internal/store/graph.go:307-311",
			down:     []string{"rack-a1"},
			check: func(t *testing.T, s *scenarioRun) {
				downByPlacement := wantService{Status: domain.StatusDown, Cause: impact.CauseCapacity}
				s.assertServices(t, map[string]wantService{
					"haproxy-edge": downByPlacement,
					"pgsql-core":   downByPlacement,
					"vault":        downByPlacement,
					"orders-api":   downByPlacement,
					"orders-web":   downByPlacement,
					"rabbitmq":     downByPlacement,
					"backup-agent": downByPlacement,
					"sso": {Status: domain.StatusDown, Cause: impact.CauseDependency,
						Via: "pgsql-core/sql"},
					"partner-gateway": {Status: domain.StatusDown, Cause: impact.CauseDependency,
						Via: "route orders.example.com"},
				})

				// The idempotence assertion. Every instance lost here was lost
				// because its hypervisor fell with the rack; not one of them may
				// also be counted as network-isolated, and no service may be
				// explained by reachability.
				for _, si := range s.result.Services {
					if si.LostToIsolation != 0 {
						s.broke(t, "%s reports %d of its %d lost instances as network-isolated. They were "+
							"powered off with the rack. `alive := !down && !(needsNet && isolated)` is a "+
							"single &&, so an instance already lost to placement must never be charged to "+
							"isolation a second time", si.Code, si.LostToIsolation, si.LostInstances)
					}
					if si.Cause == impact.CauseReachability {
						s.broke(t, "%s is explained by reachability (%q); a rack losing power is a capacity "+
							"and dependency story, and pointing an operator at the network during it is "+
							"actively harmful", si.Code, si.Reason)
					}
				}

				s.assertCounts(t, 0, 2, 2)
				s.assertNoPartitions(t, "every surviving host is still one component")

				// sw-core loses sw-core-1 and fw-edge loses fw-edge-1 as a
				// consequence of the rack, and each reports the variant its own
				// availability policy produces.
				s.assertRedundancyLost(t, []wantGroup{
					{Code: "sw-core", Surviving: 1, Total: 2, PromotionPending: false},
					{Code: "fw-edge", Surviving: 1, Total: 2, PromotionPending: true},
				})

				// hv-03 is in rack-b1 and keeps its attachment to sw-core-2, so
				// nothing is data-isolated. sw-oob-1 also sits in rack-a1, so
				// the whole estate loses management reachability -- report-only.
				if names := s.isolatedNames(domain.PlaneData); len(names) != 0 {
					s.broke(t, "%v are reported data-isolated; hv-03 sits in rack-b1 and its attachment to "+
						"sw-core-2 survives", names)
				}
				if len(s.isolatedNames(domain.PlaneMgmt)) == 0 {
					s.broke(t, "nothing is management-isolated, but sw-oob-1 sits in rack-a1 and is gone "+
						"with it")
				}

				// The rack alone cannot actually prove the idempotence rule, and
				// saying so is more useful than pretending otherwise: hv-01 and
				// hv-02 are dual-homed and sw-core survives at 1-of-2, so
				// nothing inside rack-a1 is both down AND isolated, and the
				// assertion above is satisfied vacuously. Taking the whole site
				// is the case where every host is both. If the liveness term
				// were two conditions instead of one `&&`, every service below
				// would report its losses a second time as isolation losses and
				// every explanation would flip from capacity to reachability --
				// telling an operator whose datacentre lost power to go and look
				// at the network.
				site, siteByCode := s.f.simulate(t, 180, "dc-oslo")
				if len(siteByCode) == 0 {
					s.brokeFatal(t, "premise broken: losing the site affected nothing")
				}
				for _, si := range site.Services {
					code := si.Code
					if si.LostToIsolation != 0 {
						s.broke(t, "losing the whole site, %s reports %d of %d lost instances as "+
							"network-isolated. Every host in the site is down by placement AND has no "+
							"surviving attachment; `alive := !down && !(needsNet && isolated)` is one "+
							"expression precisely so the second condition cannot charge again for what the "+
							"first already took", code, si.LostToIsolation, si.LostInstances)
					}
					if si.Cause != impact.CauseCapacity {
						s.broke(t, "losing the whole site, %s is explained by %q; the machines are off, "+
							"not cut off", code, si.Cause)
					}
				}

				// "Today's rack-a1 answer is unchanged" is the claim, so assert
				// it directly against a run with no topology at all rather than
				// against remembered numbers.
				before := snapshotOf(s.result)
				clearSeededTopology(t, s.f)
				after, _ := s.f.simulate(t, 180, "rack-a1")
				assertSameSnapshot(t, "scenario 10: rack-a1 with and without declared topology",
					before, snapshotOf(after))
			},
		},

		// -------------------------------------------------------------------
		{
			number: 11,
			title:  "the availability policy runs on the surviving-AND-reachable set",
			guarantee: "EvaluateCapacity is called with its existing signature over instances whose Alive " +
				"is `!down && !(needsNet && isolated)`, so every policy -- quorum, sharded, standalone, " +
				"active_active -- applies unchanged to the set that is both surviving and reachable. This " +
				"is what a per-edge-only model structurally cannot do, and the reason string must say " +
				"\"running but cut off\" rather than \"lost\"",
			evidence: "docs/reachability-design.md:574, :641; internal/impact/engine.go:292-336",
			down:     []string{"sw-core-2"},
			check: func(t *testing.T, s *scenarioRun) {
				// vault: quorum over 3, one instance isolated. 2 surviving >=
				// floor(3/2)+1 = 2, so the CAPACITY verdict is ok -- which is
				// observable as vault carrying a PROPAGATION reason rather than
				// a quorum one. Its reported status is degraded, and it got
				// there through the soft edge to sso.
				vault, ok := s.byCode["vault"]
				if !ok {
					s.brokeFatal(t, "vault is not reported at all; it should be degraded through its soft edge to sso")
				}
				if vault.LostInstances != 1 || vault.TotalInstances != 3 || vault.LostToIsolation != 1 {
					s.broke(t, "vault lost %d of %d (%d isolated), want 1 of 3 with 1 isolated",
						vault.LostInstances, vault.TotalInstances, vault.LostToIsolation)
				}
				if vault.Status != domain.StatusDegraded {
					s.broke(t, "vault is %s, want degraded: quorum arithmetic over the surviving-and-reachable "+
						"set is 2 of 3, which meets the floor of 2, so capacity did not take it down", vault.Status)
				}
				if strings.Contains(vault.Reason, "quorum needs") {
					s.broke(t, "vault's reason is the capacity one (%q); its quorum held, and the status came "+
						"down the soft edge to sso. Reporting the capacity reason here would tell an operator "+
						"to go and look at Vault's cluster, which is healthy", vault.Reason)
				}
				if vault.Via != "sso/https" {
					s.broke(t, "vault Via = %q, want sso/https", vault.Via)
				}

				// The policies that did not hold, each for its own reason.
				if sso := s.byCode["sso"]; sso.Status != domain.StatusDown || sso.Cause != impact.CauseReachability {
					s.broke(t, "sso = %s/%s, want down/reachability: standalone, one instance, isolated",
						sso.Status, sso.Cause)
				}
				if m := s.byCode["mimir-ingester"]; !strings.Contains(m.Reason, "at least one shard has no surviving replica") {
					s.broke(t, "mimir-ingester reason = %q, want the sharded policy's own verdict -- both "+
						"shards live on hv-03, so no shard has a surviving replica", m.Reason)
				}
				if api := s.byCode["orders-api"]; api.LostInstances != 0 {
					s.broke(t, "orders-api lost %d instances; both replicas are on vm-app-1 under hv-01, "+
						"which is untouched, so its active_active capacity is intact and it is degraded "+
						"only by propagation", api.LostInstances)
				}
				if dev := s.byCode["orders-api-dev"]; dev.Status != domain.StatusDown {
					s.broke(t, "orders-api-dev = %s, want down: standalone, single instance on hv-03", dev.Status)
				}

				// The phrasing rule, both arms. Equal counts say the machines are
				// running; unequal counts say how many of the losses were the
				// network's doing. Getting this wrong tells somebody to go and
				// power a machine back on that never went off.
				if sso := s.byCode["sso"]; !strings.HasSuffix(sso.Reason, "(running, but network-isolated -- not powered off)") {
					s.broke(t, "sso reason = %q; every one of its lost instances is isolated rather than "+
						"powered off, and the reason must say so", sso.Reason)
				}
				_, partialByCode := s.f.simulate(t, 180, "sw-core-2", "vm-vault-1")
				partial := partialByCode["vault"]
				if partial.LostInstances != 2 || partial.LostToIsolation != 1 {
					s.brokeFatal(t, "premise broken: taking sw-core-2 and vm-vault-1 together should leave "+
						"vault at 2 lost of which 1 isolated, got %d and %d",
						partial.LostInstances, partial.LostToIsolation)
				}
				if !strings.Contains(partial.Reason, "(1 of those network-isolated)") {
					s.broke(t, "vault reason = %q; one instance was powered off and one was cut off, and "+
						"the report must distinguish them", partial.Reason)
				}
			},
		},

		// -------------------------------------------------------------------
		// 12-14 are reconstructions. No "Scenario 12/13/14" citation exists
		// anywhere in the repository -- every commit touching the design doc and
		// every dangling stash was checked. The titles are inferred; the
		// assertions under them are each derived from a rule the design states
		// in its own words, cited per assertion.
		// -------------------------------------------------------------------
		{
			number: 12,
			title:  "a partially-cabled estate makes no claim",
			guarantee: "an asset with no attachment anywhere in its containment ancestry is UNMODELLED: " +
				"never isolated, never partitioned, never counted against anything, no matter what else " +
				"went down. And an estate with no topology at all reduces Analyse to the function it was " +
				"before this feature existed. Absence of topology data is not evidence of disconnection",
			evidence: "docs/reachability-design.md:46, :366, :592; internal/impact/reach.go:171-178, :440-451" +
				" -- RECONSTRUCTED scenario number; the rule itself is quoted verbatim from :46",
			setup: buildUnmodelledHost,
			// Both core chassis: sw-core goes down outright, so everything
			// modelled on the data plane is isolated. If the unmodelled host is
			// going to be swept up, it is here.
			down: []string{"sw-core-1", "sw-core-2"},
			check: func(t *testing.T, s *scenarioRun) {
				host := s.extra["unmodelled-host"]
				for _, iso := range s.result.Isolated {
					if iso.AssetID == host {
						s.broke(t, "the unmodelled host is reported isolated on plane %q. It has no "+
							"net_attachment anywhere in its ancestry, so modelled(x, plane) is false and "+
							"isolated() is false by construction. Reporting it is how a half-cabled estate "+
							"gets told five services are down the first time anyone touches a switch", iso.Plane)
					}
				}
				if si, ok := s.byCode["unmodelled-svc"]; ok {
					s.broke(t, "the unmodelled host's service is reported %s (%q); no claim can be made about it",
						si.Status, si.Reason)
				}
				for _, p := range s.result.Partitions {
					if p.ConsumerService == "unmodelled-svc" {
						s.broke(t, "an edge of the unmodelled service is reported partitioned; reach() "+
							"returns known=false for it, aggregateReach folds to reachUnknown, and the "+
							"switch in phasePropagate ignores a zero reachLevel")
					}
				}
				// Two: this scenario's own manufactured host, and the
				// fixture's half-installed srv-backup-proxy-1, which carries a
				// log-shipper instance and has a management attachment but no
				// data-plane one. Both are the honest report of how much of the
				// estate this run has an opinion about -- and the second is
				// there precisely so the diagrams have an unmodelled host to
				// draw.
				if got := s.result.Coverage.Unmodelled; got != 2 {
					s.broke(t, "Coverage.Unmodelled = %d, want 2 -- this scenario's host plus "+
						"the fixture's uncabled one", got)
				}

				// The trap this scenario exists to record for the next test
				// author: computeCoverage iterates service-instance HOSTS, and
				// on this fixture almost every one is a VM inheriting its
				// hypervisor's attachment -- Inherited 12. Modelled is exactly
				// 1: hv-01 hosts the bare-metal backup agent AND owns its own
				// attachment, the one placement where the two coincide. A test
				// asserting Modelled tracks the number of attachments, or of
				// instances, fails against a correct estate.
				if s.result.Coverage.Modelled != 1 || s.result.Coverage.Inherited != 12 {
					s.broke(t, "Coverage says modelled=%d inherited=%d, want 1 and 12: every instance host "+
						"except hv-01 is a VM reaching its network by inheritance, and hv-01 -- the "+
						"bare-metal placement -- owns its attachment directly",
						s.result.Coverage.Modelled, s.result.Coverage.Inherited)
				}

				// The degenerate form: zero rows in every net_* table makes
				// Inputs.Net nil and the whole feature inert.
				clearSeededTopology(t, s.f)
				bare, _ := s.f.simulate(t, 180, "rack-a1")
				if n := len(bare.Isolated) + len(bare.Partitions) + len(bare.Unreachable) + len(bare.RedundancyLost); n != 0 {
					s.broke(t, "an estate with no declared topology produced %d reachability findings; "+
						"Inputs.Net == nil is the compositional guarantee in one line", n)
				}
				if cov := bare.Coverage; cov.Modelled+cov.Inherited+cov.Unmodelled+cov.GroupsWithoutUplinkOrAnchor+cov.ClusterUnresolved != 0 {
					s.broke(t, "coverage on an untopologied estate = %+v, want zero-valued", cov)
				}
				if len(bare.Services) != 9 {
					s.broke(t, "rack-a1 on an untopologied estate affected %d services, want the same 9 a "+
						"build without this feature reported", len(bare.Services))
				}
			},
		},

		// -------------------------------------------------------------------
		{
			number: 13,
			title:  "a redundant fabric with a peer link is a cycle in the forwarder graph",
			guarantee: "termination is a design guarantee rather than a hope: union-find has no recursion, " +
				"no traversal, no memo table and no cycle special-case, and Phase 0 runs once outside the " +
				"fixed point. A cycle in the FORWARDER graph must never appear in Result.Cycles, which " +
				"carries dependency cycles only; and component representatives are deterministic, so " +
				"repeated runs of the same input are identical",
			evidence: "docs/reachability-design.md:598 (the M5 spec calls for a deliberate peer-link cycle), " +
				":36, :348; internal/impact/reach.go:244-302" +
				" -- RECONSTRUCTED scenario number; each assertion is derived from the quoted rules",
			// The seeded fixture declares one uplink, sw-core -> fw-edge, so it
			// carries no forwarder cycle. Adding the reverse edge on the same
			// plane makes one, which is what a redundant fabric genuinely looks
			// like.
			setup: func(t *testing.T, f *fixture) map[string]string {
				up, err := domain.NewNetUplink(store.NewID(),
					f.refs.NetGroups["fw-edge"], f.refs.NetGroups["sw-core"], domain.PlaneData, f.store.Now())
				if err != nil {
					t.Fatalf("building the reverse uplink: %v", err)
				}
				if err := f.store.CreateNetUplink(f.ctx, domain.AdministratorPermit(domain.SystemActor), up); err != nil {
					t.Fatalf("creating the reverse uplink: %v", err)
				}
				return nil
			},
			down: []string{"sw-core-2"},
			check: func(t *testing.T, s *scenarioRun) {
				// Reaching here at all is the termination proof: f.simulate ran
				// the analysis to completion with a cycle in the graph.
				//
				// Result.Cycles must carry the dependency loop and nothing else.
				// A forwarder loop in this list would send an operator looking
				// for a service that depends on a switch.
				var got []string
				for _, c := range s.result.Cycles {
					got = append(got, strings.Join(c, ">"))
				}
				sort.Strings(got)
				if strings.Join(got, " | ") != "sso>vault>sso" {
					s.broke(t, "Cycles = %v, want exactly the dependency loop [sso vault sso]. "+
						"Result.Cycles carries dependency cycles only; a forwarder loop must never reach it", got)
				}

				// Determinism: the same input, repeatedly, over a graph that now
				// has two ways round.
				first := snapshotOf(s.result)
				for i := 0; i < 4; i++ {
					again, _ := s.f.simulate(t, 180, "sw-core-2")
					assertSameSnapshot(t, "scenario 13: repeated run over a cyclic forwarder graph",
						first, snapshotOf(again))
				}
				firstIso := s.isolatedNames(domain.PlaneData)
				for i := 0; i < 4; i++ {
					again, _ := s.f.simulate(t, 180, "sw-core-2")
					run := &scenarioRun{tc: s.tc, f: s.f, result: again}
					if strings.Join(run.isolatedNames(domain.PlaneData), ",") != strings.Join(firstIso, ",") {
						s.broke(t, "the isolated set changed between identical runs: %v then %v. Component "+
							"representatives are chosen by sorting edges and taking the lexicographically "+
							"smaller root precisely so this cannot happen",
							firstIso, run.isolatedNames(domain.PlaneData))
					}
				}

				// And the cycle changed nothing: the second edge joins two
				// groups that were already in one component. Same census as
				// scenario 4, hv-03's bridge included -- see the note there.
				s.assertIsolated(t, domain.PlaneData, "sw-core", []string{
					"hv-03", "hv-03-br0", "vm-vault-3", "vm-sso-1", "vm-k8s-1", "vm-k8s-2", "vm-dev-1",
				})
			},
		},

		// -------------------------------------------------------------------
		{
			number: 14,
			title:  "member-level uplink diversity is the one consciously optimistic error",
			guarantee: "uplinks are group-to-group. The M5 cable plant runs sw-core-1 to fw-edge-1 and " +
				"sw-core-2 to fw-edge-2, so losing fw-edge-1 and sw-core-2 together really does disconnect " +
				"sw-core-1 upward -- and this model reports it OPTIMISTICALLY, scoring the fabric as if " +
				"the mesh were full. The design accepts that bounded error at the forwarder-to-forwarder " +
				"layer and applies the member-level gate only to host attachments. This test pins the " +
				"documented WRONG answer so that a future member-level model is a deliberate change " +
				"rather than a surprise",
			evidence: "docs/reachability-design.md:563, :705" +
				" -- RECONSTRUCTED scenario number; the limitation is quoted verbatim from :563",
			down: []string{"fw-edge-1", "sw-core-2"},
			check: func(t *testing.T, s *scenarioRun) {
				// Both groups survive their own arithmetic: sw-core is 1-of-2
				// against min_healthy 1 (ok), fw-edge is a manual pair that lost
				// its primary (degraded). Neither is down, so the group-to-group
				// uplink is still active with both endpoints alive and the two
				// groups remain one component.
				s.assertRedundancyLost(t, []wantGroup{
					{Code: "sw-core", Surviving: 1, Total: 2, PromotionPending: false},
					{Code: "fw-edge", Surviving: 1, Total: 2, PromotionPending: true},
				})

				// The optimistic error, stated as what is NOT reported. hv-01
				// and hv-02 land on sw-core-1, which is genuinely cut off from
				// the edge; the model says nothing about it.
				for _, iso := range s.result.Isolated {
					switch name := s.assetName(iso.AssetID); name {
					case "hv-01", "hv-02", "vm-vault-1", "vm-vault-2", "vm-db-1", "vm-db-2",
						"vm-proxy-1", "vm-app-1", "vm-queue-1":
						s.broke(t, "%s is reported isolated. That would be the pessimistic answer; the "+
							"design's stated behaviour is the opposite, and a change here means the "+
							"member-level model arrived without the known limit at :705 being revisited", name)
					}
				}
				s.assertNoPartitions(t, "the two groups stay in one component, so no edge's reach changes")
				for _, code := range []string{"pgsql-core", "rabbitmq", "backup-agent"} {
					if si, ok := s.byCode[code]; ok {
						s.broke(t, "%s (hosted entirely on hv-01/hv-02, behind sw-core-1) is reported %s "+
							"with cause %s; the model does not know sw-core-1 lost its uplink", code, si.Status, si.Cause)
					}
				}

				// The member-level gate that IS applied: host attachments. hv-03
				// is single-homed to sw-core-2 and is correctly cut off. This is
				// the contrast that makes the scenario about uplinks rather than
				// about the model being blind everywhere.
				if !contains(s.isolatedNames(domain.PlaneData), "hv-03") {
					s.broke(t, "hv-03 is not isolated. The member-level gate applies at host attachments "+
						"even though it does not at uplinks, and losing that distinction would mean the "+
						"optimism had spread")
				}

				// THE DOCUMENTED WRONG ANSWER, pinned deliberately.
				//
				// Physically sw-core-1 has no upward path left at all: its own
				// uplink partner fw-edge-1 is down and the other core is down.
				// The truthful verdict for anything on hv-01/hv-02 is therefore
				// "cut off from the internet". The model instead scores the
				// group-to-group uplink as alive-because-both-groups-are-alive
				// and reports merely DEGRADED, via the optimistic pass, because
				// fw-edge still has a standby.
				//
				// Asserting Degraded rather than Down is the whole point: it is
				// the error the design knowingly accepts at the forwarder layer
				// (:563), and pinning it means a future member-level model
				// arrives as a deliberate change with this test failing loudly,
				// rather than as a silent improvement nobody reviewed.
				s.assertUnreachable(t, []wantReach{
					{Code: "haproxy-edge", EndpointName: "https", Scope: "external",
						Status: domain.StatusDegraded, BlockingGroup: "fw-edge"},
				})
				if si, ok := s.byCode["haproxy-edge"]; ok && si.Status == domain.StatusDown {
					s.broke(t, "haproxy-edge is reported DOWN. That is the truthful member-level answer, "+
						"which means the optimistic error at :563 has been fixed -- revisit the known "+
						"limit and this scenario together rather than just deleting the assertion")
				}
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Scenario setups
// ---------------------------------------------------------------------------

// autoEdgeGroupCode is scenario 2's rebuilt edge pair. Codes are test-scoped
// because net_group.code is a plain UNIQUE rather than partial on lifecycle, so
// retiring the seeded group does not free "fw-edge".
const autoEdgeGroupCode = "auto-fw-edge"

// rebuildM5Topology re-declares the seeded topology with one value changed.
//
// It exists because failover_mode is not editable: there is no UpdateNetGroup,
// which is the right call for a column whose value silently decides whether an
// outage is an incident or a note. The rebuild is verbatim -- same kinds, same
// availability, same primary/standby roles, same uplink, same three attachments
// with the same pins, same four anchor scopes -- so a comparison against
// scenario 1 isolates failover_mode and nothing else.
func rebuildM5Topology(t *testing.T, f *fixture, failoverMode string) {
	t.Helper()
	clearSeededTopology(t, f)
	now := f.store.Now()
	minHealthy := 1

	mkGroup := func(spec domain.NetGroupSpec) string {
		g, err := domain.NewNetGroup(store.NewID(), spec, now)
		if err != nil {
			t.Fatalf("building net group %s: %v", spec.Code, err)
		}
		if err := f.store.CreateNetGroup(f.ctx, domain.AdministratorPermit(domain.SystemActor), g); err != nil {
			t.Fatalf("creating net group %s: %v", spec.Code, err)
		}
		return g.ID
	}
	core := mkGroup(domain.NetGroupSpec{
		Code: "auto-sw-core", Name: "Core switch pair", Kind: domain.NetGroupMCLAG,
		Role: domain.NetRoleCore, Availability: domain.AvailActiveActive, MinHealthy: &minHealthy,
	})
	edge := mkGroup(domain.NetGroupSpec{
		Code: autoEdgeGroupCode, Name: "Edge firewall pair", Kind: domain.NetGroupHAPair,
		Role: domain.NetRoleEdge, Availability: domain.AvailActivePassive, FailoverMode: &failoverMode,
	})

	for _, m := range []struct{ group, asset, role string }{
		{core, "sw-core-1", "member"},
		{core, "sw-core-2", "member"},
		{edge, "fw-edge-1", domain.RolePrimary},
		{edge, "fw-edge-2", domain.RoleStandby},
	} {
		member, err := domain.NewNetGroupMember(m.group, f.refs.Assets[m.asset], m.role, now)
		if err != nil {
			t.Fatalf("building member %s: %v", m.asset, err)
		}
		if err := f.store.AddNetGroupMember(f.ctx, domain.AdministratorPermit(domain.SystemActor), member); err != nil {
			t.Fatalf("adding member %s: %v", m.asset, err)
		}
	}

	up, err := domain.NewNetUplink(store.NewID(), core, edge, domain.PlaneData, now)
	if err != nil {
		t.Fatalf("building uplink: %v", err)
	}
	if err := f.store.CreateNetUplink(f.ctx, domain.AdministratorPermit(domain.SystemActor), up); err != nil {
		t.Fatalf("creating uplink: %v", err)
	}

	for _, a := range []struct {
		host string
		pins []string
	}{
		{"hv-01", []string{"sw-core-1", "sw-core-2"}},
		{"hv-02", []string{"sw-core-1", "sw-core-2"}},
		{"hv-03", []string{"sw-core-2"}},
	} {
		attachPinned(t, f, f.refs.Assets[a.host], core, a.pins...)
	}

	for _, a := range []struct{ code, scope, group, env string }{
		{"auto-internet", "external", edge, ""},
		{"auto-partner-transit", "cross_env", edge, ""},
		{"auto-prod-net", "environment", core, "prod"},
		{"auto-dev-net", "environment", core, "dev"},
	} {
		anchor, err := domain.NewNetAnchor(store.NewID(), a.code, a.code, a.scope, a.group, now)
		if err != nil {
			t.Fatalf("building anchor %s: %v", a.code, err)
		}
		if a.env != "" {
			envID := f.refs.Environments[a.env]
			anchor.EnvironmentID = &envID
		}
		if err := f.store.CreateNetAnchor(f.ctx, domain.AdministratorPermit(domain.SystemActor), anchor); err != nil {
			t.Fatalf("creating anchor %s: %v", a.code, err)
		}
	}
}

// attachPinned attaches an asset to a group on the data plane, pinned to the
// named chassis.
func attachPinned(t *testing.T, f *fixture, assetID, groupID string, chassis ...string) string {
	t.Helper()
	att, err := domain.NewNetAttachment(store.NewID(), assetID, groupID, domain.PlaneData, f.store.Now())
	if err != nil {
		t.Fatalf("building attachment: %v", err)
	}
	members := make([]domain.NetAttachmentMember, 0, len(chassis))
	for _, name := range chassis {
		m, err := domain.NewNetAttachmentMember(att.ID, f.refs.Assets[name], nil)
		if err != nil {
			t.Fatalf("building attachment member %s: %v", name, err)
		}
		members = append(members, *m)
	}
	if err := f.store.CreateNetAttachment(f.ctx, domain.AdministratorPermit(domain.SystemActor), att, members); err != nil {
		t.Fatalf("creating attachment: %v", err)
	}
	return att.ID
}

// buildLAGgedHost is scenario 5's fixture: a host whose two cables both land on
// sw-core-1.
//
// The design's claim is that the primary key on (attachment_id, asset_id)
// collapses the pair into one member row, so the first thing this does is
// attempt both pins and require the store to refuse -- "two cables to one
// chassis is one member" is enforced by that key and nowhere else. The
// attachment is then declared with the single member the estate really has.
func buildLAGgedHost(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	now := f.store.Now()
	rackB := f.refs.Assets["rack-b1"]
	host, err := domain.NewAsset(store.NewID(), domain.KindServer, "lag-host", &rackB, now)
	if err != nil {
		t.Fatalf("building the LAGged host: %v", err)
	}
	if err := f.store.CreateAsset(f.ctx, domain.AdministratorPermit(domain.SystemActor), host, nil); err != nil {
		t.Fatalf("creating the LAGged host: %v", err)
	}

	doomed, err := domain.NewNetAttachment(store.NewID(), host.ID, f.refs.NetGroups["sw-core"], domain.PlaneData, now)
	if err != nil {
		t.Fatalf("building the two-cable attachment: %v", err)
	}
	var twice []domain.NetAttachmentMember
	for i := 0; i < 2; i++ {
		m, err := domain.NewNetAttachmentMember(doomed.ID, f.refs.Assets["sw-core-1"], nil)
		if err != nil {
			t.Fatalf("building attachment member: %v", err)
		}
		twice = append(twice, *m)
	}
	if err := f.store.CreateNetAttachment(f.ctx, domain.AdministratorPermit(domain.SystemActor), doomed, twice); err == nil {
		t.Error("declaring two cables from one host into the same chassis was accepted as two member " +
			"rows. PRIMARY KEY (attachment_id, asset_id) must collapse them into one, which is what " +
			"makes a LAG to a single chassis correctly not count as redundancy")
	} else if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("duplicate pin rejected with an unexpected error: %v", err)
	}

	attachPinned(t, f, host.ID, f.refs.NetGroups["sw-core"], "sw-core-1")

	svc := mustSimpleService(t, f, "lag-svc", "LAG host service")
	mustInstance(t, f, svc, host.ID)
	// An endpoint, so computeNeedsNet does not declare the service immune: a
	// service with no endpoints and no dependencies is vacuously local.
	mustEndpoint(t, f, svc, "lag-http")

	return map[string]string{"lag-host": host.ID}
}

// buildUnmodelledHost is scenario 12's fixture: a host under rack-b1, which has
// no attachment anywhere in its ancestry, carrying a service of its own.
func buildUnmodelledHost(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	rackB := f.refs.Assets["rack-b1"]
	host, err := domain.NewAsset(store.NewID(), domain.KindServer, "unmodelled-host", &rackB, f.store.Now())
	if err != nil {
		t.Fatalf("building the unmodelled host: %v", err)
	}
	if err := f.store.CreateAsset(f.ctx, domain.AdministratorPermit(domain.SystemActor), host, nil); err != nil {
		t.Fatalf("creating the unmodelled host: %v", err)
	}
	svc := mustSimpleService(t, f, "unmodelled-svc", "Service on an uncabled host")
	mustInstance(t, f, svc, host.ID)
	mustEndpoint(t, f, svc, "unmodelled-http")
	return map[string]string{"unmodelled-host": host.ID}
}

// splitEstateWithALocalSocket is scenario 8's fixture.
//
// declareSplitTopology puts hv-02 and hv-03 on one side of a transit group and
// hv-01 on the other, so haproxy-edge (vm-proxy-1, hv-02) is separated from the
// two backends it fronts (vm-app-1, hv-01) -- both of which are
// bind_scope=host, exposure=internal, and therefore the endpoints whose
// treatment the scenario is about.
//
// The unix endpoint and the edge to it are added because the OTHER half of the
// rule is untestable against the seed as it stands: the fixture has one
// unix-bound endpoint (pgsql-core/local) and no dependency points at it, so
// asserting its absence from Result.Partitions proves nothing. This declares an
// edge that genuinely crosses the break and must still be exempt.
func splitEstateWithALocalSocket(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	declareSplitTopology(t, f, []string{"hv-02", "hv-03"}, []string{"hv-01"})

	socket := &domain.Endpoint{
		ID: store.NewID(), ServiceID: f.refs.Services["orders-web"], Name: "local-socket",
		L4Proto: domain.ProtoUnix, UnixPath: strPtr("/var/run/orders-web.sock"),
		BindScope: domain.BindUnix, TLSMode: "none", Exposure: "internal",
	}
	if err := f.store.CreateEndpoint(f.ctx, domain.AdministratorPermit(domain.SystemActor), socket); err != nil {
		t.Fatalf("creating the unix endpoint: %v", err)
	}
	dep, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
		ConsumerServiceID:  f.refs.Services["sso"],
		ProviderEndpointID: &socket.ID,
		// Optional, so this edge can never move a status and the service
		// expectations above stay about the endpoints the scenario is for. What
		// it can still do is appear in Result.Partitions if the local exemption
		// is ever decided by exposure instead of bind_scope.
		Nature:      domain.NatureOptional,
		FailureMode: "test-only edge across the break to a unix socket",
	}, f.store.Now())
	if err != nil {
		t.Fatalf("building the unix dependency: %v", err)
	}
	if err := f.store.CreateDependency(f.ctx, domain.AdministratorPermit(domain.SystemActor), dep, nil); err != nil {
		t.Fatalf("creating the unix dependency: %v", err)
	}
	return nil
}

func strPtr(s string) *string { return &s }
