package domain

import (
	"bytes"
	"testing"
)

func TestParseAddr(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantText   string
		wantFamily int
		wantStart  []byte
		wantErr    bool
	}{
		{
			name: "ipv4", input: "10.20.30.5",
			wantText: "10.20.30.5", wantFamily: 4, wantStart: []byte{10, 20, 30, 5},
		},
		{
			name: "ipv4 with surrounding whitespace", input: "  10.20.30.5\t",
			wantText: "10.20.30.5", wantFamily: 4, wantStart: []byte{10, 20, 30, 5},
		},
		{
			name: "ipv6 is canonicalised", input: "2001:0DB8:0000:0000:0000:0000:0000:0001",
			wantText: "2001:db8::1", wantFamily: 6,
			wantStart: []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		},
		{
			// Storing a v4-mapped address as 16 bytes would make it invisible
			// to every v4 containment query, so it has to be folded.
			name: "ipv4-mapped ipv6 folds to v4", input: "::ffff:10.0.0.1",
			wantText: "10.0.0.1", wantFamily: 4, wantStart: []byte{10, 0, 0, 1},
		},
		{
			// A zone is local scoping, not part of the address identity.
			name: "zone is stripped", input: "fe80::1%eth0",
			wantText: "fe80::1", wantFamily: 6,
			wantStart: []byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		},
		{name: "empty", input: "", wantErr: true},
		{name: "not an address", input: "hv-01", wantErr: true},
		{name: "cidr is not an address", input: "10.20.30.0/24", wantErr: true},
		{name: "octet out of range", input: "10.20.30.256", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAddr(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseAddr(%q) succeeded, want an error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAddr(%q): %v", tc.input, err)
			}
			if got.Text != tc.wantText {
				t.Errorf("text = %q, want %q", got.Text, tc.wantText)
			}
			if got.Family != tc.wantFamily {
				t.Errorf("family = %d, want %d", got.Family, tc.wantFamily)
			}
			if !bytes.Equal(got.Start, tc.wantStart) {
				t.Errorf("start = %v, want %v", got.Start, tc.wantStart)
			}
		})
	}
}

func TestParsePrefix(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantText  string
		wantStart []byte
		wantEnd   []byte
		wantErr   bool
	}{
		{
			name: "ipv4 /24", input: "10.20.30.0/24",
			wantText:  "10.20.30.0/24",
			wantStart: []byte{10, 20, 30, 0}, wantEnd: []byte{10, 20, 30, 255},
		},
		{
			// Masking means the same network entered two ways collides on the
			// UNIQUE constraint instead of silently duplicating.
			name: "host bits are masked off", input: "10.20.30.5/24",
			wantText:  "10.20.30.0/24",
			wantStart: []byte{10, 20, 30, 0}, wantEnd: []byte{10, 20, 30, 255},
		},
		{
			name: "ipv4 /16", input: "10.20.0.0/16",
			wantText:  "10.20.0.0/16",
			wantStart: []byte{10, 20, 0, 0}, wantEnd: []byte{10, 20, 255, 255},
		},
		{
			name: "ipv4 /32 is a single host", input: "10.20.30.5/32",
			wantText:  "10.20.30.5/32",
			wantStart: []byte{10, 20, 30, 5}, wantEnd: []byte{10, 20, 30, 5},
		},
		{
			name: "ipv4 /0 covers everything", input: "0.0.0.0/0",
			wantText:  "0.0.0.0/0",
			wantStart: []byte{0, 0, 0, 0}, wantEnd: []byte{255, 255, 255, 255},
		},
		{
			name: "ipv6 /64", input: "2001:db8::/64",
			wantText:  "2001:db8::/64",
			wantStart: []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			wantEnd: []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0,
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		},
		{name: "empty", input: "", wantErr: true},
		{name: "bare address is not a prefix", input: "10.20.30.5", wantErr: true},
		{name: "prefix length out of range", input: "10.20.30.0/33", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePrefix(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePrefix(%q) succeeded, want an error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePrefix(%q): %v", tc.input, err)
			}
			if got.Text != tc.wantText {
				t.Errorf("text = %q, want %q", got.Text, tc.wantText)
			}
			if !bytes.Equal(got.Start, tc.wantStart) {
				t.Errorf("start = %v, want %v", got.Start, tc.wantStart)
			}
			if !bytes.Equal(got.End, tc.wantEnd) {
				t.Errorf("end = %v, want %v", got.End, tc.wantEnd)
			}
			// The invariant the whole range scan depends on.
			if bytes.Compare(got.Start, got.End) > 0 {
				t.Errorf("start %v sorts after end %v", got.Start, got.End)
			}
		})
	}
}

// TestPrefixBoundsOrderLexicographically is the property the containment query
// relies on: a more specific prefix inside a wider one must have a network
// address that sorts at or after the wider one's.
func TestPrefixBoundsOrderLexicographically(t *testing.T) {
	wide, err := ParsePrefix("10.20.0.0/16")
	if err != nil {
		t.Fatalf("parsing wide prefix: %v", err)
	}
	narrow, err := ParsePrefix("10.20.30.0/24")
	if err != nil {
		t.Fatalf("parsing narrow prefix: %v", err)
	}
	addr, err := ParseAddr("10.20.30.5")
	if err != nil {
		t.Fatalf("parsing address: %v", err)
	}

	for _, p := range []PrefixValue{wide, narrow} {
		if bytes.Compare(p.Start, addr.Start) > 0 || bytes.Compare(p.End, addr.Start) < 0 {
			t.Errorf("%s should contain %s", p.Text, addr.Text)
		}
	}
	if bytes.Compare(narrow.Start, wide.Start) <= 0 {
		t.Errorf("narrow start %v should sort after wide start %v; "+
			"ORDER BY addr_start DESC would pick the wrong prefix", narrow.Start, wide.Start)
	}
}

func TestParseMAC(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "colon separated", input: "aa:bb:cc:00:01:01", want: "aa:bb:cc:00:01:01"},
		{name: "uppercase is lowered", input: "AA:BB:CC:00:01:01", want: "aa:bb:cc:00:01:01"},
		{name: "hyphen separated", input: "AA-BB-CC-00-01-01", want: "aa:bb:cc:00:01:01"},
		{name: "cisco dotted", input: "aabb.cc00.0101", want: "aa:bb:cc:00:01:01"},
		{name: "bare hex", input: "aabbcc000101", want: "aa:bb:cc:00:01:01"},
		{name: "too short", input: "aa:bb:cc", wantErr: true},
		{name: "too long", input: "aa:bb:cc:00:01:01:02", wantErr: true},
		{name: "not hex", input: "gg:bb:cc:00:01:01", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMAC(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseMAC(%q) = %q, want an error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMAC(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseMAC(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
