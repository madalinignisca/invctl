package domain

import (
	"fmt"
	"net/netip"
	"strings"
)

// The four-column IP pattern (HANDOVER §4.1).
//
// Neither SQLite nor a portable subset of SQL has an inet type, so an address
// is decomposed into: canonical text (for display and exact match), a family
// discriminator, and big-endian byte bounds. BLOB comparison is bytewise in
// both engines, so "which prefix contains this address" is a plain range
// predicate over an index rather than a function call.
//
// Family must always be part of the predicate: a 4-byte v4 address would
// otherwise compare as a prefix of a 16-byte v6 range and produce nonsense.

// AddrValue is the stored form of a single host address.
type AddrValue struct {
	Text   string // canonical form, e.g. "10.20.30.5" or "2001:db8::1"
	Family int    // 4 or 6
	Start  []byte // big-endian, 4 or 16 bytes
}

// PrefixValue is the stored form of a network, with inclusive bounds.
type PrefixValue struct {
	Text   string // canonical CIDR, e.g. "10.20.30.0/24"
	Family int
	Start  []byte // network address
	End    []byte // last address in the prefix (broadcast, for v4)
	Bits   int
}

// ParseAddr normalizes a host address for storage.
//
// IPv4-mapped IPv6 input ("::ffff:10.0.0.1") is folded to real IPv4 — storing
// it as 16 bytes would make it invisible to every v4 containment query.
// A zone (%eth0) is stripped: it is a local scoping artifact, not part of the
// address identity.
func ParseAddr(s string) (AddrValue, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return AddrValue{}, fmt.Errorf("parsing address: empty")
	}
	a, err := netip.ParseAddr(trimmed)
	if err != nil {
		return AddrValue{}, fmt.Errorf("parsing address %q: %w", s, err)
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		b := a.As4()
		return AddrValue{Text: a.String(), Family: 4, Start: b[:]}, nil
	}
	b := a.As16()
	return AddrValue{Text: a.String(), Family: 6, Start: b[:]}, nil
}

// ParsePrefix normalizes a CIDR for storage and computes its inclusive bounds.
//
// The input is masked (10.20.30.5/24 becomes 10.20.30.0/24) so that the same
// network entered two ways collides on the UNIQUE constraint instead of
// silently creating a duplicate.
func ParsePrefix(s string) (PrefixValue, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return PrefixValue{}, fmt.Errorf("parsing prefix: empty")
	}
	p, err := netip.ParsePrefix(trimmed)
	if err != nil {
		return PrefixValue{}, fmt.Errorf("parsing prefix %q: %w", s, err)
	}
	p = p.Masked()
	addr := p.Addr().Unmap().WithZone("")

	var start []byte
	family := 6
	if addr.Is4() {
		b := addr.As4()
		start = b[:]
		family = 4
	} else {
		b := addr.As16()
		start = b[:]
	}

	end := broadcast(start, p.Bits())
	return PrefixValue{
		Text:   p.String(),
		Family: family,
		Start:  start,
		End:    end,
		Bits:   p.Bits(),
	}, nil
}

// broadcast returns the highest address in a prefix: the network address with
// every host bit set.
func broadcast(start []byte, bits int) []byte {
	end := make([]byte, len(start))
	copy(end, start)
	totalBits := len(start) * 8
	for i := bits; i < totalBits; i++ {
		end[i/8] |= 1 << (7 - uint(i%8))
	}
	return end
}

// ParseMAC normalizes a MAC address to lowercase colon-separated form.
// Accepts colon, hyphen, dot (Cisco) and bare-hex input, because operators
// paste all four and a lookup by MAC has to match regardless.
func ParseMAC(s string) (string, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ':', '-', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(s)))

	if len(cleaned) != 12 {
		return "", fmt.Errorf("parsing mac %q: expected 12 hex digits, got %d", s, len(cleaned))
	}
	for _, r := range cleaned {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", fmt.Errorf("parsing mac %q: %q is not a hex digit", s, r)
		}
	}

	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(cleaned[i : i+2])
	}
	return b.String(), nil
}
