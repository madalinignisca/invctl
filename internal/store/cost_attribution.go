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

// Dividing a cluster's money, not just its capacity (WP-J4, §5.1–§5.6).
//
// THE POOL IS THE COST LINES ON THE MEMBER HOSTS. That is what "the cost of the
// cluster" means: the machines it is made of. A line on a GUEST is excluded on
// purpose -- it is already attached to the thing being attributed, so dividing
// it again across the cluster would charge every other project a share of one
// project's backup licence.
//
// RUN RATE AND CAPITAL STAY SEPARATE ALL THE WAY THROUGH, because cost.go is
// emphatic that folding a one-off into a monthly figure is a lie. Both are
// divided by the same shares and reported as two numbers, and the sum is
// labelled as what it is rather than presented as an invoice.
//
// NOTHING IS DIVIDED WITHOUT A DECLARED SPLIT. A hypervisor's price buys cores
// and memory together while a project holds a different percentage of each, and
// there is no arithmetic that separates them -- only a judgement. Migration
// 00048 argues the choice; what matters here is that an undeclared split
// produces a stated gap rather than a plausible number.

// CostShare is one subject's slice of a cluster's cost.
type CostShare struct {
	SubjectID string
	Subject   string
	// BasisPoints is the blended share: the CPU and memory shares weighted by
	// the cluster's declared split.
	BasisPoints int
	// CPUPoints and MemoryPoints are the two shares it was blended from, kept
	// so a reader can check the arithmetic rather than take it on trust. Same
	// rule the physical-fit findings follow.
	CPUPoints    int
	MemoryPoints int
	// RunRateMinor is this subject's share of the monthly run rate;
	// AmortisedMinor its share of capital spread over the life of the hardware.
	// Never added together silently -- see TotalMinor.
	RunRateMinor   int64
	AmortisedMinor int64
	// DirectMinor is cost attributed to this subject ALONE rather than divided:
	// the conditional and per-consumer lines naming only its machines.
	DirectMinor int64
}

// TotalMinor is the run rate plus the capital spread plus what was attributed
// directly. Named rather than implied: a reader who wants the run rate alone
// has it, and one who wants everything gets a figure that says it includes
// amortised capital.
func (s CostShare) TotalMinor() int64 {
	return s.RunRateMinor + s.AmortisedMinor + s.DirectMinor
}

// CostAttribution is a cluster's money divided between its claimants.
type CostAttribution struct {
	On        string
	ClusterID string
	Cluster   string

	// SplitCPU is the declared percent of the pool attributable to CPU, or nil.
	SplitCPU *int

	// The pool, before division.
	RunRateMinor   int64
	AmortisedMinor int64
	// DirectMinor is the part of the pool that named its own consumers and
	// therefore never entered the division.
	DirectMinor int64
	// UnattributableMinor is cost on a line whose scope names nobody. NOT
	// silently spread across everybody: a conditional line with no consumers is
	// a declaration somebody started and did not finish, and treating it as
	// universal would be a default wearing a declaration's clothes.
	UnattributableMinor int64

	Shares []CostShare
	// Idle is the share of the pool that reached no project, which is headroom
	// somebody is paying for. §5.3: whatever does not reach a project is shown,
	// never dropped.
	Idle CostShare

	// Gaps are the reasons this report is less than complete, in the words a
	// reader needs to act on them.
	Gaps []string
}

// Divisible reports whether the money could be divided at all.
func (c CostAttribution) Divisible() bool { return c.SplitCPU != nil }

// PoolMinor is everything the cluster costs per month, however it was
// attributed.
func (c CostAttribution) PoolMinor() int64 {
	return c.RunRateMinor + c.AmortisedMinor + c.DirectMinor + c.UnattributableMinor
}

// CostAttributionFor divides a cluster's cost between the projects on it.
func (s *SQLStore) CostAttributionFor(ctx context.Context, clusterID, on string) (*CostAttribution, error) {
	cluster, err := s.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if on == "" {
		on = domain.FormatDate(s.Now())
	}
	out := &CostAttribution{
		On: on, ClusterID: clusterID, Cluster: cluster.Name,
		SplitCPU: cluster.CostSplitCPU,
	}

	// The capacity shares first: the money rides on them.
	shares, err := s.AttributionFor(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	cpu, memory := domain.Division{}, domain.Division{}
	for _, d := range shares.Divisions {
		switch d.Dimension {
		case "CPU":
			cpu = d
		case "Memory":
			memory = d
		}
	}

	owners, names, err := s.ownersOf(ctx)
	if err != nil {
		return nil, err
	}

	hosts, err := s.ListClusterHosts(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("reading hosts for cost attribution: %w", err)
	}

	// direct[projectID] is cost a scoped line put on one project outright.
	direct := map[string]int64{}
	for _, h := range hosts {
		lines, err := s.ListAssetCosts(ctx, h.AssetID)
		if err != nil {
			return nil, fmt.Errorf("reading costs of %s: %w", h.AssetName, err)
		}
		host, err := s.GetAsset(ctx, h.AssetID)
		if err != nil {
			return nil, err
		}
		for i := range lines {
			line := lines[i].Cost
			if !line.AppliesOn(on) {
				continue
			}
			runRate := line.MonthlyMinor()
			amortised, _ := line.AmortisedMonthlyMinor(host.EOLDate, on)
			if runRate == 0 && amortised == 0 {
				continue
			}

			if line.AppliesTo == domain.CostUniversal {
				out.RunRateMinor += runRate
				out.AmortisedMinor += amortised
				continue
			}

			// A scoped line: who does it name?
			consumers, err := s.CostConsumers(ctx, line.ID)
			if err != nil {
				return nil, err
			}
			if len(consumers) == 0 {
				out.UnattributableMinor += runRate + amortised
				out.Gaps = append(out.Gaps, fmt.Sprintf(
					"a %s cost on %s names no consumers, so it is attributed to nobody",
					line.AppliesTo, h.AssetName))
				continue
			}
			parts, divided, err := s.divideScoped(ctx, line, consumers,
				runRate+amortised, owners, cluster.CostSplitCPU)
			if err != nil {
				return nil, err
			}
			if !divided {
				out.UnattributableMinor += runRate + amortised
				out.Gaps = append(out.Gaps, fmt.Sprintf(
					"a %s cost on %s covers machines that cannot be weighed against "+
						"each other yet, so it is attributed to nobody",
					line.AppliesTo, h.AssetName))
				continue
			}
			for id, part := range parts {
				direct[id] += part
			}
		}
	}

	for _, part := range direct {
		out.DirectMinor += part
	}

	// Without a declared split there is nothing honest to divide the pool by,
	// so the shares carry only what was attributed directly and the report says
	// why. The capacity shares are unaffected and still answer "who holds 12%
	// of this cluster", which needs no money at all.
	if cluster.CostSplitCPU == nil {
		if out.RunRateMinor > 0 || out.AmortisedMinor > 0 {
			out.Gaps = append(out.Gaps,
				"nobody has declared how much of this cluster's cost is CPU and how "+
					"much is memory, so its shared cost is not divided")
		}
	}

	out.Shares, out.Idle = s.blend(cpu, memory, cluster.CostSplitCPU,
		out.RunRateMinor, out.AmortisedMinor, direct, names)
	return out, nil
}

// divideScoped splits one conditional or per-consumer line across the projects
// owning the machines it names.
//
// CONDITIONAL DIVIDES BY WHAT THOSE MACHINES HOLD; PER-CONSUMER DIVIDES PER
// HEAD. The difference is §5.6's whole point: a per-VM backup licence costs the
// same for a 64 GB machine as for a 2 GB one, and dividing it by capacity would
// charge the large one many times over for a single licence while reconciling
// perfectly. Collapsing the two into one rule would be the silent difference
// this whole section exists to prevent.
//
// The second return says whether it could be divided at all. A conditional line
// needs the cluster's declared split for the same reason the universal pool
// does -- it is weighing cores against memory -- so without one it is
// unattributable rather than quietly divided by heads.
func (s *SQLStore) divideScoped(ctx context.Context, line domain.Cost, consumers []string,
	minor int64, owners map[string]string, splitCPU *int) (map[string]int64, bool, error) {

	claims := make([]domain.Claim, 0, len(consumers))
	order := []string{}

	if line.AppliesTo == domain.CostPerConsumer {
		// Per head: every named machine counts one, whatever its size.
		byProject := map[string]int{}
		for _, assetID := range consumers {
			id := owners[assetID]
			if _, seen := byProject[id]; !seen {
				order = append(order, id)
			}
			byProject[id]++
		}
		for _, id := range order {
			claims = append(claims, domain.Claim{SubjectID: id, Amount: byProject[id]})
		}
		return apportionTo(domain.DivideEqually("covered", claims), minor), true, nil
	}

	// Conditional: by what the named machines hold, within the named set.
	if splitCPU == nil {
		return nil, false, nil
	}
	sizes, err := s.allocationsOf(ctx, consumers)
	if err != nil {
		return nil, false, err
	}
	totalVCPU, totalMemory := 0, 0
	for _, sz := range sizes {
		totalVCPU += sz[0]
		totalMemory += sz[1]
	}
	if totalVCPU == 0 && totalMemory == 0 {
		// The licence covers machines nobody has measured. Not divided by
		// heads as a consolation: that would be a different rule producing a
		// number indistinguishable from a computed one.
		return nil, false, nil
	}

	// Each machine's share WITHIN THE NAMED SET, on each dimension, blended by
	// the same split the universal pool uses -- so a licence and the hardware
	// under it weigh cores against memory identically.
	weights := map[string]int{}
	for _, assetID := range consumers {
		sz := sizes[assetID]
		var w int
		if totalVCPU > 0 {
			w += *splitCPU * sz[0] * 10000 / totalVCPU
		}
		if totalMemory > 0 {
			w += (100 - *splitCPU) * sz[1] * 10000 / totalMemory
		}
		id := owners[assetID]
		if _, seen := weights[id]; !seen {
			order = append(order, id)
		}
		weights[id] += w
	}
	for _, id := range order {
		claims = append(claims, domain.Claim{SubjectID: id, Amount: weights[id]})
	}
	total := 0
	for _, c := range claims {
		total += c.Amount
	}
	return apportionTo(domain.Divide("covered", "share", total, claims), minor), true, nil
}

// apportionTo turns a division into money per subject.
func apportionTo(d domain.Division, minor int64) map[string]int64 {
	out := map[string]int64{}
	parts := d.ApportionMinor(minor)
	for i, sh := range d.Total() {
		if i < len(parts) {
			out[sh.SubjectID] += parts[i]
		}
	}
	return out
}

// allocationsOf reads what each named machine was allocated: vCPU and memory.
func (s *SQLStore) allocationsOf(ctx context.Context, assetIDs []string) (map[string][2]int, error) {
	out := map[string][2]int{}
	if len(assetIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ID       string `db:"id"`
		VCPU     *int   `db:"vcpu_allocated"`
		MemoryMB *int   `db:"memory_allocated_mb"`
	}
	if err := s.read(ctx, &rows, `
		SELECT id, vcpu_allocated, memory_allocated_mb
		FROM asset WHERE id IN (`+placeholders(len(assetIDs))+`)`,
		anySlice(assetIDs)...); err != nil {
		return nil, fmt.Errorf("reading the sizes of covered machines: %w", err)
	}
	for _, r := range rows {
		var pair [2]int
		if r.VCPU != nil {
			pair[0] = *r.VCPU
		}
		if r.MemoryMB != nil {
			pair[1] = *r.MemoryMB
		}
		out[r.ID] = pair
	}
	return out, nil
}

// blend weights each project's CPU and memory shares by the declared split and
// apportions the pool across the result.
func (s *SQLStore) blend(cpu, memory domain.Division, splitCPU *int,
	runRate, amortised int64, direct map[string]int64,
	names map[string]string) ([]CostShare, CostShare) {

	// Every subject appearing in either dimension, or carrying a direct cost.
	points := map[string][2]int{}
	subject := map[string]string{}
	note := func(id, name string, dim int, bp int) {
		p := points[id]
		p[dim] = bp
		points[id] = p
		if name != "" {
			subject[id] = name
		}
	}
	for _, sh := range cpu.Total() {
		note(sh.SubjectID, sh.Subject, 0, sh.BasisPoints)
	}
	for _, sh := range memory.Total() {
		note(sh.SubjectID, sh.Subject, 1, sh.BasisPoints)
	}
	for id := range direct {
		if _, seen := points[id]; !seen {
			points[id] = [2]int{}
			if id == "" {
				subject[id] = domain.UnattributedSubject
			} else {
				subject[id] = names[id]
			}
		}
	}

	ids := make([]string, 0, len(points))
	for id := range points {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// The blend, in basis points, and it must still sum to 10000 -- so it is
	// apportioned rather than computed per subject and rounded independently.
	weights := make([]int, len(ids))
	for i, id := range ids {
		p := points[id]
		if splitCPU == nil {
			continue
		}
		weights[i] = p[0]**splitCPU + p[1]*(100-*splitCPU)
	}
	blended := apportionPoints(weights)

	runParts := apportionInto(runRate, blended)
	amortParts := apportionInto(amortised, blended)

	shares := make([]CostShare, 0, len(ids))
	var idle CostShare
	for i, id := range ids {
		p := points[id]
		name := subject[id]
		if name == "" {
			name = names[id]
		}
		sh := CostShare{
			SubjectID: id, Subject: name,
			CPUPoints: p[0], MemoryPoints: p[1],
			BasisPoints:    blended[i],
			RunRateMinor:   runParts[i],
			AmortisedMinor: amortParts[i],
			DirectMinor:    direct[id],
		}
		if name == domain.IdleSubject {
			idle = sh
			continue
		}
		shares = append(shares, sh)
	}
	sort.SliceStable(shares, func(a, b int) bool {
		if shares[a].TotalMinor() != shares[b].TotalMinor() {
			return shares[a].TotalMinor() > shares[b].TotalMinor()
		}
		return shares[a].Subject < shares[b].Subject
	})
	return shares, idle
}

// apportionPoints scales weights so they sum to exactly 10000.
func apportionPoints(weights []int) []int {
	total := 0
	for _, w := range weights {
		total += w
	}
	if total == 0 {
		return make([]int, len(weights))
	}
	return domain.Apportion(10000, weights, total)
}

// apportionInto splits money across shares already expressed in basis points.
func apportionInto(minor int64, points []int) []int64 {
	total := 0
	for _, p := range points {
		total += p
	}
	if total == 0 || minor == 0 {
		return make([]int64, len(points))
	}
	return domain.ApportionMinorAcross(minor, points, total)
}
