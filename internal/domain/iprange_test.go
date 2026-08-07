// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain_test

import (
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// spanFor turns a CIDR into the interval it occupies, the way a child prefix
// reaches the allocator.
func spanFor(t *testing.T, cidr string) domain.AddrSpan {
	t.Helper()
	pv, err := domain.ParsePrefix(cidr)
	if err != nil {
		t.Fatalf("parsing %s: %v", cidr, err)
	}
	return domain.AddrSpan{Start: pv.Start, End: pv.End}
}

func spanForAddr(t *testing.T, addr string) domain.AddrSpan {
	t.Helper()
	av, err := domain.ParseAddr(addr)
	if err != nil {
		t.Fatalf("parsing %s: %v", addr, err)
	}
	return domain.SpanOf(av.Start)
}

// TestFirstFreeAddressSkipsWhatIsSpokenFor. Every case here is an address the
// allocator must NOT offer, and each is a different reason.
func TestFirstFreeAddressSkipsWhatIsSpokenFor(t *testing.T) {
	tests := []struct {
		name  string
		cidr  string
		taken []string // CIDRs and bare addresses, mixed as the caller supplies them
		want  string
		none  bool
	}{
		{
			name: "an empty v4 network starts at .1, never at the network address",
			cidr: "10.20.30.0/24",
			want: "10.20.30.1",
		},
		{
			name:  "assigned addresses are stepped over",
			cidr:  "10.20.30.0/24",
			taken: []string{"10.20.30.1", "10.20.30.2", "10.20.30.3"},
			want:  "10.20.30.4",
		},
		{
			name:  "a gap in the middle is found",
			cidr:  "10.20.30.0/24",
			taken: []string{"10.20.30.1", "10.20.30.3"},
			want:  "10.20.30.2",
		},
		{
			// The delegation rule: a carved-out child is not ours to hand out.
			name:  "a child prefix is delegated, not available",
			cidr:  "10.20.30.0/24",
			taken: []string{"10.20.30.0/26"},
			want:  "10.20.30.64",
		},
		{
			// DHCP will issue from the pool without asking this system.
			name:  "a reservation is skipped whole",
			cidr:  "10.20.30.0/24",
			taken: []string{"10.20.30.1", "10.20.30.2-10.20.30.199"},
			want:  "10.20.30.200",
		},
		{
			name:  "overlapping exclusions do not walk backwards",
			cidr:  "10.20.30.0/24",
			taken: []string{"10.20.30.1-10.20.30.100", "10.20.30.10-10.20.30.20", "10.20.30.5"},
			want:  "10.20.30.101",
		},
		{
			name:  "unsorted input is sorted first",
			cidr:  "10.20.30.0/24",
			taken: []string{"10.20.30.3", "10.20.30.1", "10.20.30.2"},
			want:  "10.20.30.4",
		},
		{
			// The broadcast is not offered, so a /24 full to .254 is full.
			name:  "a v4 network full to the broadcast has nothing left",
			cidr:  "10.20.30.0/30",
			taken: []string{"10.20.30.1", "10.20.30.2"},
			none:  true,
		},
		{
			// RFC 3021: both addresses of a /31 are usable.
			name: "a /31 offers its first address, because point-to-point has no broadcast",
			cidr: "10.20.30.0/31",
			want: "10.20.30.0",
		},
		{
			name:  "a /31 with one end taken offers the other",
			cidr:  "10.20.30.0/31",
			taken: []string{"10.20.30.0"},
			want:  "10.20.30.1",
		},
		{
			name: "a /32 is a single host and offers it",
			cidr: "10.20.30.7/32",
			want: "10.20.30.7",
		},
		{
			// No broadcast in v6, so the first address of the subnet is offered.
			name: "v6 does not deduct a network or broadcast address",
			cidr: "2001:db8::/64",
			want: "2001:db8::",
		},
		{
			name:  "v6 steps over an assignment",
			cidr:  "2001:db8::/64",
			taken: []string{"2001:db8::", "2001:db8::1"},
			want:  "2001:db8::2",
		},
		{
			// A /8 has sixteen million addresses and this must not scan them.
			name:  "a large prefix answers from its exclusions, not its size",
			cidr:  "10.0.0.0/8",
			taken: []string{"10.0.0.0/16"},
			want:  "10.1.0.0",
		},
		{
			// ::a14:1e01 and 10.20.30.1 ARE THE SAME INTEGER, 169090561. Compare
			// the numbers and the v6 address silently reserves the v4 one. The
			// earlier version of this case used 2001:db8::/64, whose value is
			// astronomically larger than any v4 address, so it exited the loop
			// early and proved nothing -- it passed with the width check removed.
			name:  "a v6 address sharing a v4 address's integer excludes nothing",
			cidr:  "10.20.30.0/24",
			taken: []string{"::a14:1e01"},
			want:  "10.20.30.1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var taken []domain.AddrSpan
			for _, s := range tc.taken {
				switch {
				case containsRune(s, '-'):
					lo, hi := splitRange(s)
					a, b := spanForAddr(t, lo), spanForAddr(t, hi)
					taken = append(taken, domain.AddrSpan{Start: a.Start, End: b.End})
				case containsRune(s, '/'):
					taken = append(taken, spanFor(t, s))
				default:
					taken = append(taken, spanForAddr(t, s))
				}
			}
			got, ok := domain.FirstFreeAddress(tc.cidr, taken)
			if tc.none {
				if ok {
					t.Fatalf("offered %s, but everything usable is taken", got)
				}
				return
			}
			if !ok {
				t.Fatalf("found nothing free in %s, want %s", tc.cidr, tc.want)
			}
			if got != tc.want {
				t.Errorf("first free in %s = %s, want %s", tc.cidr, got, tc.want)
			}
		})
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func splitRange(s string) (string, string) {
	for i, c := range s {
		if c == '-' {
			return s[:i], s[i+1:]
		}
	}
	return s, s
}

func TestARangeMustNotEndBeforeItBegins(t *testing.T) {
	if _, err := domain.NewIPRange("r1", "10.20.30.200", "10.20.30.100"); err == nil {
		t.Fatal("a backwards range was accepted; it reserves nothing while looking " +
			"like a reservation")
	} else if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid so the handler returns 422", err)
	}

	if _, err := domain.NewIPRange("r2", "10.20.30.1", "2001:db8::1"); err == nil {
		t.Fatal("a range starting in v4 and ending in v6 was accepted")
	}
}

func TestARangeKnowsHowManyAddressesItHolds(t *testing.T) {
	cases := []struct{ start, end, want string }{
		{"10.20.30.100", "10.20.30.199", "100"},
		{"10.20.30.5", "10.20.30.5", "1"},
		{"2001:db8::", "2001:db8::ffff", "65536"},
	}
	for _, tc := range cases {
		r, err := domain.NewIPRange("r", tc.start, tc.end)
		if err != nil {
			t.Fatalf("building %s-%s: %v", tc.start, tc.end, err)
		}
		if got := r.Size().String(); got != tc.want {
			t.Errorf("%s-%s holds %s, want %s", tc.start, tc.end, got, tc.want)
		}
	}
}
