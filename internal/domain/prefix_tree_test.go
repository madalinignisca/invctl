// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain_test

import (
	"math/big"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The numbers in this file are worked out by hand rather than by running the
// code and writing down what it said, which would assert only that it is
// consistent with itself.

func mkPrefix(t *testing.T, id, cidr string, vrf *string) domain.Prefix {
	t.Helper()
	p, err := domain.NewPrefix(id, cidr)
	if err != nil {
		t.Fatalf("building %s: %v", cidr, err)
	}
	p.VRFID = vrf
	return *p
}

func nodeByCIDR(nodes []domain.PrefixNode, cidr string) (domain.PrefixNode, bool) {
	for _, n := range nodes {
		if n.CIDRText == cidr {
			return n, true
		}
	}
	return domain.PrefixNode{}, false
}

func TestPrefixSizeCountsAddressesNotMaskBits(t *testing.T) {
	cases := []struct {
		cidr string
		want string // decimal, because a /64 is not an int64
	}{
		{"10.0.0.0/24", "256"},
		{"10.0.0.0/26", "64"},
		{"10.0.0.0/32", "1"},
		{"10.0.0.0/8", "16777216"},
		{"0.0.0.0/0", "4294967296"},
		{"2001:db8::/64", "18446744073709551616"},
		{"2001:db8::/32", "79228162514264337593543950336"},
	}
	for _, tc := range cases {
		t.Run(tc.cidr, func(t *testing.T) {
			got := domain.PrefixSize(tc.cidr)
			if got == nil {
				t.Fatalf("PrefixSize(%s) = nil", tc.cidr)
			}
			if got.String() != tc.want {
				t.Errorf("PrefixSize(%s) = %s, want %s", tc.cidr, got, tc.want)
			}
		})
	}
	if domain.PrefixSize("not-a-prefix") != nil {
		t.Error("PrefixSize accepted a non-prefix")
	}
}

// TestTheTreeNestsAndTheDepthFollows. The ordering rule is the whole trick: a
// parent must precede its children even when they share a first address, which
// is the same tie ResolveAddress got wrong.
func TestTheTreeNestsAndTheDepthFollows(t *testing.T) {
	// Deliberately supplied narrowest-first and out of order, so a tree that
	// only works on pre-sorted input fails here.
	in := []domain.Prefix{
		mkPrefix(t, "c", "10.20.0.0/26", nil), // aligned with both ancestors
		mkPrefix(t, "a", "10.20.0.0/16", nil),
		mkPrefix(t, "d", "10.20.30.0/24", nil),
		mkPrefix(t, "b", "10.20.0.0/24", nil), // aligned with the /16
	}
	nodes := domain.BuildPrefixTree(in, map[string]int{})

	want := []struct {
		cidr   string
		depth  int
		parent string // "" for a root
	}{
		{"10.20.0.0/16", 0, ""},
		{"10.20.0.0/24", 1, "a"},
		{"10.20.0.0/26", 2, "b"},
		{"10.20.30.0/24", 1, "a"},
	}
	if len(nodes) != len(want) {
		t.Fatalf("got %d nodes, want %d", len(nodes), len(want))
	}
	for i, w := range want {
		if nodes[i].CIDRText != w.cidr {
			t.Fatalf("position %d is %s, want %s. The list must come back in tree "+
				"order so the view can indent on Depth alone", i, nodes[i].CIDRText, w.cidr)
		}
		if nodes[i].Depth != w.depth {
			t.Errorf("%s depth = %d, want %d", w.cidr, nodes[i].Depth, w.depth)
		}
		switch {
		case w.parent == "" && nodes[i].ParentID != nil:
			t.Errorf("%s has parent %s, want none", w.cidr, *nodes[i].ParentID)
		case w.parent != "" && nodes[i].ParentID == nil:
			t.Errorf("%s has no parent, want %s", w.cidr, w.parent)
		case w.parent != "" && *nodes[i].ParentID != w.parent:
			t.Errorf("%s parent = %s, want %s", w.cidr, *nodes[i].ParentID, w.parent)
		}
	}
}

// TestUtilisationCountsAChildInFull is the semantic decision made visible.
//
// The worked example: a /24 is 256 addresses. It holds a /26 (64) and five
// addresses of its own. Allocation is 64 + 5 = 69, which is 26.95%. The /26
// itself holds twelve addresses, so it reads 12/64 -- and the parent does NOT
// see those twelve, because they are inside a child that is already counted
// whole. Counting them again would report 81 and there would be no size of
// estate at which that error stopped compounding.
func TestUtilisationCountsAChildInFull(t *testing.T) {
	in := []domain.Prefix{
		mkPrefix(t, "p24", "10.20.30.0/24", nil),
		mkPrefix(t, "p26", "10.20.30.0/26", nil),
	}
	// As a range scan returns it: the /24's range contains all 17, because the
	// twelve in the /26 are inside it too.
	nodes := domain.BuildPrefixTree(in, map[string]int{"p24": 17, "p26": 12})

	parent, ok := nodeByCIDR(nodes, "10.20.30.0/24")
	if !ok {
		t.Fatal("the /24 is missing")
	}
	child, ok := nodeByCIDR(nodes, "10.20.30.0/26")
	if !ok {
		t.Fatal("the /26 is missing")
	}

	if got := parent.Allocated.String(); got != "69" {
		t.Errorf("the /24 allocates %s, want 69 (64 for the child plus 5 loose). "+
			"If this is 81 the child's addresses are being counted twice; if it is "+
			"17 the child is not counting as allocated at all", got)
	}
	if parent.Addresses != 5 {
		t.Errorf("the /24 holds %d addresses directly, want 5 -- the other twelve "+
			"belong to the child", parent.Addresses)
	}
	if got := child.Allocated.String(); got != "12" {
		t.Errorf("the /26 allocates %s, want 12", got)
	}
	if got := parent.UtilPercent(); got < 26.9 || got > 27.0 {
		t.Errorf("the /24 is %.2f%% used, want ~26.95%%", got)
	}
	if parent.Children != 1 {
		t.Errorf("the /24 has %d children, want 1", parent.Children)
	}
}

// TestAFullyCarvedPrefixIsFullEvenWhenEmpty. The consequence of allocation
// semantics, and the one that looks like a bug until it is the thing that saves
// somebody: four /26s consume a /24 completely while nothing is plugged in.
func TestAFullyCarvedPrefixIsFullEvenWhenEmpty(t *testing.T) {
	in := []domain.Prefix{
		mkPrefix(t, "p", "10.20.30.0/24", nil),
		mkPrefix(t, "a", "10.20.30.0/26", nil),
		mkPrefix(t, "b", "10.20.30.64/26", nil),
		mkPrefix(t, "c", "10.20.30.128/26", nil),
		mkPrefix(t, "d", "10.20.30.192/26", nil),
	}
	nodes := domain.BuildPrefixTree(in, map[string]int{})

	parent, _ := nodeByCIDR(nodes, "10.20.30.0/24")
	if !parent.IsFull() {
		t.Errorf("a /24 carved into four /26s reports %s/%s and is not full; "+
			"there is nothing left to allocate out of it",
			parent.Allocated, parent.Size)
	}
	if got := parent.UtilPercent(); got != 100 {
		t.Errorf("utilisation = %v, want 100", got)
	}
	for _, cidr := range []string{"10.20.30.0/26", "10.20.30.64/26"} {
		child, _ := nodeByCIDR(nodes, cidr)
		if child.UtilPercent() != 0 {
			t.Errorf("%s is %v%% used, want 0 -- nothing is assigned in it", cidr, child.UtilPercent())
		}
	}
}

// TestContainmentNeverCrossesAVRF. Two tenants holding the same space is the
// reason the column exists; nesting one inside the other would build a tree
// spanning address spaces that cannot reach each other.
func TestContainmentNeverCrossesAVRF(t *testing.T) {
	a, b := "vrf-a", "vrf-b"
	in := []domain.Prefix{
		mkPrefix(t, "a8", "10.0.0.0/8", &a),
		mkPrefix(t, "b24", "10.1.2.0/24", &b),
		mkPrefix(t, "g24", "10.1.3.0/24", nil), // the global table
	}
	nodes := domain.BuildPrefixTree(in, map[string]int{})

	for _, cidr := range []string{"10.1.2.0/24", "10.1.3.0/24"} {
		n, ok := nodeByCIDR(nodes, cidr)
		if !ok {
			t.Fatalf("%s is missing", cidr)
		}
		if n.ParentID != nil {
			t.Errorf("%s was nested under %s from a different VRF; the two address "+
				"spaces cannot see each other and neither contains the other",
				cidr, *n.ParentID)
		}
	}
	root, _ := nodeByCIDR(nodes, "10.0.0.0/8")
	if root.Children != 0 {
		t.Errorf("the /8 in vrf-a claims %d children, want 0", root.Children)
	}
	if root.Allocated.Cmp(big.NewInt(0)) != 0 {
		t.Errorf("the /8 in vrf-a allocates %s, want 0", root.Allocated)
	}
}

// TestV4AndV6NeverNest. The families share the table and nothing else; a
// 4-byte range compared against a 16-byte one is meaningless.
func TestV4AndV6NeverNest(t *testing.T) {
	in := []domain.Prefix{
		mkPrefix(t, "v4", "0.0.0.0/0", nil), // covers every v4 address there is
		mkPrefix(t, "v6", "2001:db8::/32", nil),
	}
	nodes := domain.BuildPrefixTree(in, map[string]int{})
	for _, n := range nodes {
		if n.ParentID != nil {
			t.Errorf("%s was nested under a prefix of the other family", n.CIDRText)
		}
	}
}
