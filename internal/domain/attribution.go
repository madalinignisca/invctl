// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "sort"

// Dividing a shared platform between the projects standing on it (WP-J4).
//
// THERE IS NO SUCH THING AS "THE PROJECT'S SHARE", and that is the finding the
// worked example in docs/COST-ATTRIBUTION.md §5.7 was written to preserve. One
// real project came out at 5.2% of CPU, 6.25% of memory, 3.4% of block storage
// and 1.0% of bulk storage. A report offering a single blended percentage would
// have invented it, and the dimension that BINDS -- memory here -- is the one a
// blended figure hides.
//
// So this divides one dimension at a time and knows nothing about which
// dimensions exist. Storage arrives as more calls, not as more code here.
//
// EVERY SLICE RECORDS ITS BASIS. Allocation is declared and usage is observed
// (docs/AUDIT.md), so a second basis could never be a flag repointing this
// column -- it would arrive through the observation path with its own audit
// obligations. What the basis field prevents is subtler: without it a later
// switch gives two months of one meaning, a flip, and a discontinuity nobody
// can explain a year later. Cheap now, impossible to retrofit honestly.

// The bases a division can be computed on. One today, named anyway.
const (
	// BasisAllocated: what somebody agreed a workload gets. Declared.
	BasisAllocated = "allocated"
	// BasisObserved does not exist yet and is not a TODO -- it is here so the
	// stamped value is read as one of a set rather than as decoration.
	BasisObserved = "observed"
)

// IdleSubject is what the unclaimed slice is called. Not a project id, and
// deliberately not blank: a slice with no name reads as a bug.
const IdleSubject = "idle capacity"

// Claim is one subject's declared draw on one dimension.
type Claim struct {
	SubjectID string
	Subject   string
	Amount    int
}

// Share is one subject's slice of one dimension.
type Share struct {
	SubjectID string
	Subject   string
	Amount    int
	// BasisPoints is hundredths of a percent, so shares are exact integers that
	// SUM TO EXACTLY 10000. Percent as a float would not: three projects at a
	// third each round to 33.33% and a reader who adds them up finds 99.99% and
	// stops believing the page. §5.3 -- a report whose slices do not sum is
	// worse than no report, because somebody will put it in a board pack.
	BasisPoints int
}

// Percent renders the share the way it is read: 521 basis points is "5.21".
// RatioText already renders hundredths and drops trailing zeroes, which is the
// same job.
func (s Share) Percent() string { return RatioText(s.BasisPoints) }

// Division is one dimension divided between its claimants.
type Division struct {
	// Dimension and Unit name what was divided: "CPU" in "vCPU".
	Dimension string
	Unit      string
	// Sellable is what the estate can carry -- already through the survivor
	// division and the overcommit ratio, because capacity that disappears with
	// a node was never capacity anybody could sell.
	Sellable int
	// Claimed is the sum of the claims.
	Claimed int
	// Denominator is what shares are actually computed against, and it is NOT
	// always Sellable.
	//
	// WHEN MORE IS CLAIMED THAN EXISTS, THE COST STILL HAS TO LAND SOMEWHERE.
	// Dividing by Sellable there would produce shares summing past 100% and an
	// idle slice of negative capacity, which is not a thing. The denominator
	// becomes the larger of the two: idle falls to zero, the claims divide the
	// whole cost between them, and the oversubscription is reported as a
	// finding by J7 rather than smuggled into an arithmetic error here.
	Denominator int
	// Shares, largest first, and Idle separately so a caller cannot render the
	// slices and silently drop the headroom.
	Shares []Share
	Idle   Share
	// Basis is what the claims mean. See the constants.
	Basis string
}

// Total is every slice including idle, which is what a chart draws.
func (d Division) Total() []Share {
	out := make([]Share, 0, len(d.Shares)+1)
	out = append(out, d.Shares...)
	if d.Idle.Amount > 0 || len(d.Shares) == 0 {
		out = append(out, d.Idle)
	}
	return out
}

// Oversubscribed reports whether more was claimed than the estate can carry.
func (d Division) Oversubscribed() bool { return d.Claimed > d.Sellable }

// Divide apportions one dimension between its claimants.
//
// sellable is capacity AFTER the survivor division and the overcommit ratio --
// ComputeCapacity's UsableVCPU and UsableMemoryMB are exactly this, which is
// why the availability premium needs no separate multiplier anywhere: it fell
// out of a division two steps earlier (§5.2).
func Divide(dimension, unit string, sellable int, claims []Claim) Division {
	d := Division{
		Dimension: dimension, Unit: unit, Sellable: sellable,
		Basis: BasisAllocated,
	}
	for _, c := range claims {
		if c.Amount > 0 {
			d.Claimed += c.Amount
		}
	}
	d.Denominator = sellable
	if d.Claimed > d.Denominator {
		d.Denominator = d.Claimed
	}
	d.Idle = Share{Subject: IdleSubject, Amount: d.Denominator - d.Claimed}
	if d.Denominator <= 0 {
		// Nothing measured and nothing claimed. Every share would be a
		// division by zero, and an empty division says "not known" honestly.
		return d
	}

	// Basis points by largest remainder, so they sum to exactly 10000 and the
	// rounding error lands on whichever slice was closest to earning it rather
	// than on whichever happened to be last.
	amounts := make([]int, 0, len(claims)+1)
	for _, c := range claims {
		if c.Amount > 0 {
			d.Shares = append(d.Shares, Share{
				SubjectID: c.SubjectID, Subject: c.Subject, Amount: c.Amount,
			})
			amounts = append(amounts, c.Amount)
		}
	}
	amounts = append(amounts, d.Idle.Amount)
	points := apportion(10000, amounts, d.Denominator)
	for i := range d.Shares {
		d.Shares[i].BasisPoints = points[i]
	}
	d.Idle.BasisPoints = points[len(points)-1]

	// Largest first: a reader looking for who dominates a cluster should not
	// have to sort it themselves. Ties break on the name so the order is
	// stable between requests -- a table that reshuffles on refresh reads as
	// data changing when nothing has.
	sort.SliceStable(d.Shares, func(i, j int) bool {
		if d.Shares[i].Amount != d.Shares[j].Amount {
			return d.Shares[i].Amount > d.Shares[j].Amount
		}
		return d.Shares[i].Subject < d.Shares[j].Subject
	})
	return d
}

// DivideEqually splits one dimension per HEAD rather than by capacity.
//
// FOR A PER-CONSUMER COST, AND THE DIFFERENCE IS NOT PEDANTRY. A backup product
// licensed per virtual machine costs the same for a 64 GB machine as for a 2 GB
// one; dividing it by capacity share would charge the large one thirty times
// over for a single licence and the small one almost nothing. The total would
// still reconcile, which is exactly the sort of wrong number nobody catches.
//
// Each consumer counts one. A project owning three of five covered machines
// pays three fifths, and the apportionment is the same largest-remainder rule
// everything else here uses rather than a second rounding invented for this
// case -- two rounding rules in one report is how two figures that should
// agree stop agreeing.
func DivideEqually(dimension string, consumers []Claim) Division {
	heads := make([]Claim, 0, len(consumers))
	total := 0
	for _, c := range consumers {
		if c.Amount <= 0 {
			continue
		}
		heads = append(heads, c)
		total += c.Amount
	}
	// Sellable IS the head count, so there is no idle slice: a licence has no
	// unclaimed portion. Every one of them was bought for somebody.
	d := Divide(dimension, "covered", total, heads)
	return d
}

// ApportionMinor divides an amount of money across this division's slices,
// idle included, summing to EXACTLY the amount given.
//
// Apportioned from the amounts rather than from the basis points, because
// rounding twice compounds: 10000 basis points already rounded once, and
// dividing money by a rounded share loses a cent per slice on a large enough
// bill. Same largest-remainder rule, applied to the money itself.
//
// The returned slice is parallel to Total().
func (d Division) ApportionMinor(minor int64) []int64 {
	slices := d.Total()
	amounts := make([]int, len(slices))
	for i, s := range slices {
		amounts[i] = s.Amount
	}
	if d.Denominator <= 0 {
		return make([]int64, len(slices))
	}
	parts := apportionMinor(minor, amounts, d.Denominator)
	return parts
}

// Apportion is apportion, exported for callers that build their own weights --
// the cost blend weights each project by CPU and memory together and must land
// on the same 10000 the capacity shares do. One rounding rule, used everywhere:
// two in one report is how two figures that should agree stop agreeing.
func Apportion(total int, amounts []int, denominator int) []int {
	return apportion(total, amounts, denominator)
}

// ApportionMinorAcross is apportionMinor, exported for the same reason.
func ApportionMinorAcross(total int64, amounts []int, denominator int) []int64 {
	return apportionMinor(total, amounts, denominator)
}

// apportion splits total across amounts summing to denominator, by largest
// remainder. Returns a slice parallel to amounts, summing to exactly total.
func apportion(total int, amounts []int, denominator int) []int {
	out := make([]int, len(amounts))
	if denominator <= 0 {
		return out
	}
	type rem struct {
		i         int
		remainder int
	}
	rems := make([]rem, 0, len(amounts))
	assigned := 0
	for i, a := range amounts {
		exact := total * a
		out[i] = exact / denominator
		assigned += out[i]
		rems = append(rems, rem{i: i, remainder: exact % denominator})
	}
	// Hand out what rounding left over, biggest remainder first. Index breaks
	// ties so the result does not depend on sort stability.
	sort.Slice(rems, func(a, b int) bool {
		if rems[a].remainder != rems[b].remainder {
			return rems[a].remainder > rems[b].remainder
		}
		return rems[a].i < rems[b].i
	})
	for i := 0; assigned < total && i < len(rems); i++ {
		out[rems[i].i]++
		assigned++
	}
	return out
}

// apportionMinor is apportion for money, in int64 because a year of a large
// estate's spend in cents outgrows an int on a 32-bit build.
func apportionMinor(total int64, amounts []int, denominator int) []int64 {
	out := make([]int64, len(amounts))
	if denominator <= 0 {
		return out
	}
	type rem struct {
		i         int
		remainder int64
	}
	rems := make([]rem, 0, len(amounts))
	var assigned int64
	den := int64(denominator)
	for i, a := range amounts {
		exact := total * int64(a)
		out[i] = exact / den
		assigned += out[i]
		rems = append(rems, rem{i: i, remainder: exact % den})
	}
	sort.Slice(rems, func(a, b int) bool {
		if rems[a].remainder != rems[b].remainder {
			return rems[a].remainder > rems[b].remainder
		}
		return rems[a].i < rems[b].i
	})
	for i := 0; assigned < total && i < len(rems); i++ {
		out[rems[i].i]++
		assigned++
	}
	return out
}
