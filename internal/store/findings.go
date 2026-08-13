// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/madalinignisca/invctl/internal/domain"
)

// What is wrong with this estate, gathered into one answer.
//
// WHY THIS EXISTS. Every finding here was already computed and already had a
// page: the power report knows two feeds share a UPS, the expiry report knows a
// certificate lapsed, the redundancy page knows a VRRP group has one router.
// What nobody had was the sentence somebody actually opens this software to
// hear -- "here is what needs a decision" -- because it was spread across six
// pages that each had to be visited and each of which looks calm on its own.
//
// IT DERIVES, IT DOES NOT DECIDE. Every count below comes from the same store
// method the dedicated page uses, so a finding cannot appear here and not
// there, or say something different in the two places. That is the failure mode
// a summary invites: a second implementation of "what is wrong" that drifts
// from the first, and is believed because it is on the front page.
//
// THREE SEVERITIES, AND THE DISTINCTION IS THE POINT:
//
//	Fault -- something is wrong NOW. A certificate has expired, a contract has
//	         lapsed, two supposedly redundant feeds trace to one UPS.
//	Risk  -- nothing is wrong and one failure away something is. A VRRP group
//	         with one router, a host on a single feed, a circuit with one end.
//	Gap   -- the inventory does not know. A rack with no power recorded, a VLAN
//	         with no ports, a delegation nobody has carved anything out of.
//
// Collapsing Risk into Fault would cry wolf; collapsing Gap into Risk would
// claim knowledge of something nobody has written down. The third is the one
// people leave out and it is the one that makes the other two trustworthy --
// a report that cannot say "I do not know" is a report that guesses.

// DEFINED FROM domain, not beside it. domain/fit.go has to name a severity --
// it computes findings and may not import this package -- so the spelling lives
// there and these are aliases. Two independent string literals would drift
// silently: nothing would fail to compile, and a finding would simply stop
// sorting into the right band.
const (
	// FindingFault: wrong now.
	FindingFault = domain.FindingFaultSeverity
	// FindingRisk: survivable now, not survivable after one failure.
	FindingRisk = domain.FindingRiskSeverity
	// FindingGap: not recorded, so not knowable.
	FindingGap = domain.FindingGapSeverity
)

// Finding is one line on the overview.
type Finding struct {
	Severity string
	// Count is how many things are in this state. One row per KIND of finding
	// rather than per thing: forty expiring certificates is one decision, and
	// forty rows is a page nobody reads to the bottom of.
	Count int
	// Label is the finding. Detail is the first example, so the row says
	// something concrete rather than only a number.
	Label  string
	Detail string
	Href   string
}

// severityRank orders faults above risks above gaps.
func severityRank(s string) int {
	switch s {
	case FindingFault:
		return 0
	case FindingRisk:
		return 1
	default:
		return 2
	}
}

// EstateFindings gathers what needs a decision, worst first.
//
// Every source is the page's own query. Nothing here computes a finding of its
// own, and nothing here is stored -- it is as fresh as the page it summarises,
// and there is no second copy to go stale.
func (s *SQLStore) EstateFindings(ctx context.Context) ([]Finding, error) {
	var out []Finding
	add := func(sev string, n int, label, detail, href string) {
		if n > 0 {
			out = append(out, Finding{Severity: sev, Count: n, Label: label, Detail: detail, Href: href})
		}
	}

	// Expiry: already past, and about to be. The horizon is the report's own,
	// so the two pages cannot disagree about what counts as soon.
	expiry, err := s.Expiring(ctx, s.now(), ExpiryHorizonMonths)
	if err != nil {
		return nil, fmt.Errorf("gathering expiry findings: %w", err)
	}
	var firstExpired, firstSoon string
	for _, row := range expiry.Rows {
		switch row.State {
		case domain.ExpiryExpired:
			if firstExpired == "" {
				firstExpired = fmt.Sprintf("%s lapsed on %s", row.Name, row.EOLDate)
			}
		case domain.ExpirySoon:
			if firstSoon == "" {
				firstSoon = fmt.Sprintf("%s on %s", row.Name, row.EOLDate)
			}
		}
	}
	add(FindingFault, expiry.Expired, "past its date", firstExpired, "/reports/expiry")
	add(FindingRisk, expiry.Soon, "expiring soon", firstSoon, "/reports/expiry")

	// Power. The report already separates a fault from an expected
	// convergence -- a generator behind two UPS groups is the design, and
	// reporting it as a problem would teach people to ignore the page.
	power, err := s.PowerFindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering power findings: %w", err)
	}
	faults, firstPower := 0, ""
	for _, f := range power.Findings {
		if f.Severity == PowerSeverityExpected {
			continue
		}
		faults++
		if firstPower == "" {
			firstPower = f.Name + ": " + f.Detail
		}
	}
	add(FindingFault, faults, "power convergence", firstPower, "/reports/power")
	// THE HONESTY NUMBERS. A site with no panel at all and a feed whose capacity
	// cannot be computed are not findings about the estate -- they are findings
	// about the inventory, and without them the power report reads as
	// reassurance. Three faults over four modelled assets is not a healthy
	// estate; it is an unmodelled one.
	add(FindingGap, power.UnmodelledSites, "site with no power recorded", "", "/reports/power")
	add(FindingGap, power.UnsourcedPanels, "panel with no supply above it", "", "/reports/power")

	// First-hop redundancy. One router in a group is a single point of failure
	// wearing the costume of a redundant one.
	groups, err := s.ListFHRPGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering redundancy findings: %w", err)
	}
	single, empty, firstSingle := 0, 0, ""
	for _, g := range groups {
		switch g.Redundancy() {
		case domain.FHRPSingleMember:
			single++
			if firstSingle == "" {
				firstSingle = g.Name + " has one router"
			}
		case domain.FHRPNoMembers:
			empty++
		}
	}
	add(FindingRisk, single, "redundancy group with one member", firstSingle, "/redundancy")
	add(FindingGap, empty, "redundancy group with no members", "", "/redundancy")

	// Overlays that carry nothing, or carry it to one place.
	overlays, err := s.ListL2VPNs(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering overlay findings: %w", err)
	}
	oneEnd, unattached, firstOverlay := 0, 0, ""
	for _, o := range overlays {
		switch o.Reach() {
		case domain.L2VPNOneEnd:
			oneEnd++
			if firstOverlay == "" {
				firstOverlay = o.Name + " terminates once"
			}
		case domain.L2VPNUnattached:
			unattached++
		}
	}
	add(FindingRisk, oneEnd, "overlay with one end", firstOverlay, "/overlays")
	add(FindingGap, unattached, "overlay with nothing attached", "", "/overlays")

	// A circuit with one end recorded is half a fact: somebody knows where it
	// arrives and not where it comes from.
	circuits, err := s.ListCircuits(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering circuit findings: %w", err)
	}
	unlanded, firstCircuit := 0, ""
	for _, c := range circuits {
		if !c.Landed() {
			unlanded++
			if firstCircuit == "" {
				firstCircuit = fmt.Sprintf("%s (%s) has %d of 2 ends", c.CID, c.ProviderName, c.Terminations)
			}
		}
	}
	add(FindingGap, unlanded, "circuit missing an end", firstCircuit, "/circuits")

	// A VLAN with no ports is a declared record rather than a broadcast domain.
	vlans, err := s.ListVLANs(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering VLAN findings: %w", err)
	}
	emptyVLANs, firstVLAN := 0, ""
	for _, v := range vlans {
		if v.PortCount == 0 {
			emptyVLANs++
			if firstVLAN == "" {
				firstVLAN = fmt.Sprintf("VLAN %d (%s) has no ports", v.VID, v.Name)
			}
		}
	}
	add(FindingGap, emptyVLANs, "VLAN with no ports", firstVLAN, "/vlans")

	// An untouched private range is untidy. An untouched registry allocation is
	// money, which is why only the second is counted.
	aggs, err := s.ListAggregates(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering allocation findings: %w", err)
	}
	unused, firstAgg := 0, ""
	for _, a := range aggs {
		if a.Unused() && !a.IsPrivate {
			unused++
			if firstAgg == "" {
				firstAgg = a.CIDRText + " has nothing carved out of it"
			}
		}
	}
	add(FindingGap, unused, "allocation unused", firstAgg, "/allocations")

	// Physical fit: will it go in, will the rack hold it, will it stay cool.
	//
	// Href is /assets filtered to racks rather than a report of its own. These
	// are per-cabinet answers and the cabinet is where somebody goes to act on
	// one -- a list page would be a second place to read the same sentence.
	fit, err := s.EstateFit(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering fit findings: %w", err)
	}
	add(FindingFault, fit.TooDeep, "too deep for the rack", fit.FirstTooDeep, "/assets?kind=rack")
	add(FindingFault, fit.Overloaded, "rack over its load rating", fit.FirstOverloaded, "/assets?kind=rack")
	add(FindingRisk, fit.SideStarved, "side-breathing box in a narrow cabinet", fit.FirstSideStarve, "/assets?kind=rack")
	add(FindingRisk, fit.OpposedAirflow, "neighbours breathing against each other", fit.FirstOpposed, "/assets?kind=rack")
	add(FindingGap, fit.UnmeasuredRacks, "rack with no depth recorded", "", "/assets?kind=rack")
	add(FindingGap, fit.UndeclaredAirflow, "placed box with no declared airflow", "", "/catalogue")
	// Cabling (WP-C3). A cable that cannot reach is a FAULT -- either the
	// length is wrong or somebody has a lead under tension -- while the other
	// two are risks: they work, and they make the next person's afternoon
	// worse.
	add(FindingFault, fit.ShortCables, "cable too short for the span", fit.FirstShortCable, "/assets?kind=rack")
	add(FindingRisk, fit.WrongFace, "ports facing away from the mount", fit.FirstWrongFace, "/assets?kind=rack")
	add(FindingRisk, fit.DenseLeads, "heavily cabled box in a narrow cabinet", fit.FirstDenseLeads, "/assets?kind=rack")

	sort.SliceStable(out, func(i, j int) bool {
		if a, b := severityRank(out[i].Severity), severityRank(out[j].Severity); a != b {
			return a < b
		}
		return out[i].Count > out[j].Count
	})
	return out, nil
}
