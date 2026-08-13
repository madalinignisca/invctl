// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"bytes"
	"math/big"
	"net/netip"
	"sort"
)

// The prefix tree, and the arithmetic behind "how full is this network".
//
// WHY THIS IS GO AND NOT SQL. Containment is a range comparison the database
// does well, and it is already indexed. Everything AFTER that is arithmetic on
// address counts, and a /64 holds 18,446,744,073,709,551,616 of them -- which
// is not a BIGINT, is not subtractable as a BLOB, and is not portable in any
// expression both engines accept. So the query fetches ranges and this computes.
// The same division of labour the four-column pattern already uses: normalise in
// Go, scan bytes in SQL.
//
// UTILISATION MEANS ALLOCATED, NOT OCCUPIED. A child prefix counts against its
// parent IN FULL, however empty it happens to be. Carving a /26 out of a /24
// spends that space whether or not anything is plugged in yet, and the question
// somebody actually asks -- "what can I still carve out of here?" -- is answered
// by allocation. Counting only assigned addresses would report a fully
// subnetted /24 as almost empty, which is the reading that leads to handing the
// same range out twice.
//
// The consequence is worth stating because it looks like a bug when first seen:
// a parent can read 100% while every child in it reads 0%. That is correct and
// it is the point -- the space is gone, nobody has plugged anything in yet.

// PrefixNode is one prefix with everything the tree view derives about it.
// Nothing here is stored: it is recomputed on every read, for the same reason
// cost rollups and project footprints are.
type PrefixNode struct {
	Prefix
	// Depth is how many ancestors it has, for indenting.
	Depth int
	// ParentID is the narrowest prefix strictly containing it, within the same
	// VRF. Nil for a root.
	ParentID *string
	// Size is how many addresses the mask covers.
	Size *big.Int
	// Allocated is child prefixes in full, plus addresses sitting directly in
	// this prefix rather than in one of its children.
	Allocated *big.Int
	// Addresses is how many ip_address rows fall directly in this prefix and
	// not in any child of it.
	Addresses int
	// Children is how many direct children it has.
	Children int
}

// UtilPercent is allocation as a percentage, 0-100. A prefix whose size could
// not be determined reports 0 rather than dividing by zero.
func (n PrefixNode) UtilPercent() float64 {
	if n.Size == nil || n.Size.Sign() == 0 || n.Allocated == nil {
		return 0
	}
	num := new(big.Float).SetInt(n.Allocated)
	den := new(big.Float).SetInt(n.Size)
	pct, _ := new(big.Float).Quo(num, den).Float64()
	return pct * 100
}

// IsFull reports whether every address is spoken for. Compared exactly rather
// than against a rounded percentage: 99.6% of a /24 rounds to 100 and still has
// a free address in it, and telling somebody a network is full when it is not
// is how a range gets abandoned with space left in it.
func (n PrefixNode) IsFull() bool {
	return n.Size != nil && n.Allocated != nil && n.Allocated.Cmp(n.Size) >= 0
}

// PrefixSize is how many addresses a CIDR covers, from its mask.
func PrefixSize(cidr string) *big.Int {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil
	}
	bits := 32
	if p.Addr().Is6() && !p.Addr().Is4In6() {
		bits = 128
	}
	host := bits - p.Bits()
	if host < 0 {
		return nil
	}
	return new(big.Int).Lsh(big.NewInt(1), uint(host))
}

// BuildPrefixTree resolves parents, depth and utilisation over a flat list.
//
// addressesInRange maps a prefix id to how many addresses fall anywhere inside
// it, children included -- which is what a range scan naturally returns. The
// per-prefix figure this exposes is the direct one, derived by subtracting the
// direct children's totals, because an address inside a child is that child's
// to report and counting it at every level up the chain would make a /8 claim
// the whole estate.
//
// The returned slice is in tree order: each prefix immediately followed by its
// descendants, so a template can render it by indenting on Depth alone.
func BuildPrefixTree(prefixes []Prefix, addressesInRange map[string]int) []PrefixNode {
	nodes := make([]PrefixNode, 0, len(prefixes))
	for _, p := range prefixes {
		nodes = append(nodes, PrefixNode{Prefix: p, Size: PrefixSize(p.CIDRText)})
	}

	// Tree order directly: a VRF at a time, then family, then start ascending
	// with the WIDER prefix first when starts tie. A parent therefore always
	// precedes its children and sits immediately before them.
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if ak, bk := vrfKey(a.VRFID), vrfKey(b.VRFID); ak != bk {
			return ak < bk
		}
		if a.AddrFamily != b.AddrFamily {
			return a.AddrFamily < b.AddrFamily
		}
		if c := bytes.Compare(a.AddrStart, b.AddrStart); c != 0 {
			return c < 0
		}
		// Equal starts: the one that ends LATER is the parent, so it goes first.
		return bytes.Compare(a.AddrEnd, b.AddrEnd) > 0
	})

	// A stack of open ancestors. Because the list is in tree order, the parent
	// of the current prefix is the nearest entry still containing it.
	var stack []int
	directAddrs := make([]int, len(nodes))
	for i := range nodes {
		for len(stack) > 0 && !contains(nodes[stack[len(stack)-1]], nodes[i]) {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			parent := stack[len(stack)-1]
			id := nodes[parent].ID
			nodes[i].ParentID = &id
			nodes[i].Depth = nodes[parent].Depth + 1
			nodes[parent].Children++
		}
		directAddrs[i] = addressesInRange[nodes[i].ID]
		stack = append(stack, i)
	}

	// Allocation, and the direct address count. Both need the DIRECT children,
	// so this is a second pass -- Children is only final once the first has run.
	allocated := make([]*big.Int, len(nodes))
	for i := range nodes {
		allocated[i] = new(big.Int)
	}
	for i := range nodes {
		if nodes[i].ParentID == nil {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if nodes[j].ID != *nodes[i].ParentID {
				continue
			}
			if nodes[i].Size != nil {
				allocated[j].Add(allocated[j], nodes[i].Size)
			}
			// An address inside a child belongs to the child, not to us.
			directAddrs[j] -= addressesInRange[nodes[i].ID]
			break
		}
	}
	for i := range nodes {
		if directAddrs[i] < 0 {
			// Only reachable if the counts and the ranges disagree, which means
			// a prefix's text and bytes have drifted apart. Clamp rather than
			// render a negative: SetCIDR exists to stop that, and a utilisation
			// figure is not the place to discover it has failed.
			directAddrs[i] = 0
		}
		nodes[i].Addresses = directAddrs[i]
		nodes[i].Allocated = new(big.Int).Add(allocated[i], big.NewInt(int64(directAddrs[i])))
	}
	return nodes
}

// contains reports whether outer strictly contains inner: same VRF, same
// family, and a range that covers it without being the same range.
//
// SAME VRF IS NOT OPTIONAL. Two tenants may both hold 10.0.0.0/8, and letting
// one parent the other's subnets would build a tree that mixes address spaces
// which cannot see each other -- an answer that is not merely untidy but wrong.
func contains(outer, inner PrefixNode) bool {
	if vrfKey(outer.VRFID) != vrfKey(inner.VRFID) || outer.AddrFamily != inner.AddrFamily {
		return false
	}
	if bytes.Compare(outer.AddrStart, inner.AddrStart) > 0 ||
		bytes.Compare(outer.AddrEnd, inner.AddrEnd) < 0 {
		return false
	}
	// Identical ranges are siblings, not parent and child. Within one VRF the
	// unique index forbids them; across the tree they can only be the same row.
	sameRange := bytes.Equal(outer.AddrStart, inner.AddrStart) &&
		bytes.Equal(outer.AddrEnd, inner.AddrEnd)
	return !sameRange
}

// vrfKey makes a nil VRF comparable. The empty string is the global table, and
// no VRF id can collide with it because ids are UUIDs.
func vrfKey(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}
