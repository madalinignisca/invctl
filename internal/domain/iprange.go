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

// Ranges, and the question they exist to answer: what is still free.

// IPRange is a span of addresses set aside for something that is not this
// system -- a DHCP pool, a load balancer's VIPs, a band somebody reserved by
// hand. Declared state: nothing observes a reservation.
type IPRange struct {
	ID          string  `db:"id"`
	StartText   string  `db:"start_text"`
	EndText     string  `db:"end_text"`
	AddrFamily  int     `db:"addr_family"`
	AddrStart   []byte  `db:"addr_start"`
	AddrEnd     []byte  `db:"addr_end"`
	VRFID       *string `db:"vrf_id"`
	Role        *string `db:"role"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewIPRange validates and constructs a reservation.
func NewIPRange(id, start, end string) (*IPRange, error) {
	r := &IPRange{ID: id, Lifecycle: LifecycleActive}
	if err := r.SetBounds(start, end); err != nil {
		return nil, err
	}
	return r, nil
}

// SetBounds reparses both ends and rewrites all four stored columns together,
// for the reason Prefix.SetCIDR does: the text is the label and the bytes are
// what every containment question is answered from. A range whose text and
// bytes disagree reserves a span nobody can see.
func (r *IPRange) SetBounds(start, end string) error {
	ve := &ValidationError{}
	sv, err := ParseAddr(start)
	if err != nil {
		ve.Add("start_text", "%s", err.Error())
	}
	ev, err := ParseAddr(end)
	if err != nil {
		ve.Add("end_text", "%s", err.Error())
	}
	if err := ve.OrNil(); err != nil {
		return err
	}
	if sv.Family != ev.Family {
		ve.Add("end_text", "a range cannot start in IPv%d and end in IPv%d", sv.Family, ev.Family)
		return ve
	}
	// A range that ends before it begins is not a typo the reader can see: it
	// silently reserves nothing while looking like a reservation.
	if bytes.Compare(sv.Start, ev.Start) > 0 {
		ve.Add("end_text", "%s is before the start, %s", ev.Text, sv.Text)
		return ve
	}
	r.StartText, r.EndText = sv.Text, ev.Text
	r.AddrFamily = sv.Family
	r.AddrStart, r.AddrEnd = sv.Start, ev.Start
	return nil
}

// Validate re-checks what a constructor would have.
func (r *IPRange) Validate() error {
	ve := &ValidationError{}
	if len(r.AddrStart) == 0 || len(r.AddrEnd) == 0 {
		ve.Add("start_text", "the range has no bounds")
	}
	if r.Lifecycle != LifecycleActive && r.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", r.Lifecycle)
	}
	return ve.OrNil()
}

// Size is how many addresses the range covers, both ends included.
func (r IPRange) Size() *big.Int {
	if len(r.AddrStart) == 0 || len(r.AddrEnd) == 0 {
		return big.NewInt(0)
	}
	start := new(big.Int).SetBytes(r.AddrStart)
	end := new(big.Int).SetBytes(r.AddrEnd)
	return new(big.Int).Add(new(big.Int).Sub(end, start), big.NewInt(1))
}

// AddrSpan is a closed interval of addresses, used to say what is already
// taken. Both ends are inclusive.
type AddrSpan struct {
	Start []byte
	End   []byte
}

// SpanOf makes a one-address span, for an assignment.
func SpanOf(addr []byte) AddrSpan { return AddrSpan{Start: addr, End: addr} }

// FirstFreeAddress returns the lowest address in a prefix that nothing has
// taken, and whether there was one.
//
// WHAT COUNTS AS TAKEN is the caller's to supply, and it is deliberately more
// than "an interface holds it". Under the allocation rule the tree already
// uses, a child prefix and a reservation have BOTH spoken for their space --
// the /26 you carved out is not yours to hand out one address at a time, and
// the DHCP pool will issue its addresses without asking this system first.
// Offering an address out of either is how the same address reaches two hosts
// a fortnight apart, which is the failure this whole work package exists to
// prevent.
//
// IT WALKS INTERVALS, NOT ADDRESSES. Scanning a /24 for a free address is 256
// steps and scanning a /8 is sixteen million; a v6 /64 cannot be scanned at
// all. Sorting the exclusions and stepping over them is bounded by how many
// there are, so the answer costs the same for a /30 and a /64.
func FirstFreeAddress(cidr string, taken []AddrSpan) (string, bool) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", false
	}
	pv, err := ParsePrefix(cidr)
	if err != nil {
		return "", false
	}

	lo := new(big.Int).SetBytes(pv.Start)
	hi := new(big.Int).SetBytes(pv.End)

	// IPv4 SPENDS ITS FIRST AND LAST ADDRESS on the network and the broadcast,
	// and offering either is offering an address that cannot be configured.
	// Not for a /31 or a /32: RFC 3021 gives both addresses of a /31 to the two
	// ends of a point-to-point link, and a /32 is a single host. IPv6 has no
	// broadcast at all, so nothing is deducted -- the all-zeros subnet-router
	// anycast address is reserved by convention rather than by this function,
	// and a range is the honest way to record a convention.
	if pv.Family == 4 && p.Bits() <= 30 {
		lo = new(big.Int).Add(lo, big.NewInt(1))
		hi = new(big.Int).Sub(hi, big.NewInt(1))
	}
	if lo.Cmp(hi) > 0 {
		return "", false
	}

	spans := make([]AddrSpan, 0, len(taken))
	spans = append(spans, taken...)
	sort.SliceStable(spans, func(i, j int) bool {
		return bytes.Compare(spans[i].Start, spans[j].Start) < 0
	})

	one := big.NewInt(1)
	candidate := lo
	for _, s := range spans {
		if len(s.Start) != len(pv.Start) || len(s.End) != len(pv.End) {
			continue // a span of the other family cannot overlap this prefix
		}
		start := new(big.Int).SetBytes(s.Start)
		end := new(big.Int).SetBytes(s.End)
		if end.Cmp(candidate) < 0 {
			continue // entirely below where we are already looking
		}
		if start.Cmp(candidate) > 0 {
			break // a gap opens before this span, and candidate sits in it
		}
		// Overlaps the candidate: step past it. The continue above has already
		// established end >= candidate, so end+1 is always ahead of where we
		// are -- no going backwards is possible here and no guard is needed to
		// prevent it. (A guard was written; mutation testing showed nothing
		// could reach it.)
		candidate = new(big.Int).Add(end, one)
	}
	if candidate.Cmp(hi) > 0 {
		return "", false
	}
	return textFromInt(candidate, pv.Family), true
}

// textFromInt renders an address back to its canonical text.
func textFromInt(v *big.Int, family int) string {
	width := 4
	if family == 6 {
		width = 16
	}
	b := v.Bytes()
	if len(b) > width {
		return ""
	}
	buf := make([]byte, width)
	copy(buf[width-len(b):], b)
	addr, ok := netip.AddrFromSlice(buf)
	if !ok {
		return ""
	}
	return addr.Unmap().String()
}
