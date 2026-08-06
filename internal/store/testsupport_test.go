// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// assertByteRangeContainment inserts two overlapping prefixes and one address,
// then resolves the address to the most specific containing prefix using only
// portable SQL. Shared between the SQLite and PostgreSQL suites because the
// point is that the same code works on both.
func assertByteRangeContainment(t *testing.T, db *DB) {
	t.Helper()

	prefixes := []struct{ id, cidr string }{
		{"p-wide", "10.20.0.0/16"},
		{"p-narrow", "10.20.30.0/24"},
		{"p-other", "192.168.0.0/16"},
		{"p-v6", "2001:db8::/32"},
	}
	for _, p := range prefixes {
		pv, err := domain.ParsePrefix(p.cidr)
		if err != nil {
			t.Fatalf("parsing prefix %s: %v", p.cidr, err)
		}
		_, err = db.Writer.Exec(db.Rebind(
			`INSERT INTO prefix (id, cidr_text, addr_family, addr_start, addr_end)
			 VALUES (?, ?, ?, ?, ?)`),
			p.id, pv.Text, pv.Family, pv.Start, pv.End)
		if err != nil {
			t.Fatalf("inserting prefix %s: %v", p.cidr, err)
		}
	}

	// Round-trip check first: if the driver mangles bytes, every containment
	// result below would be meaningless.
	var stored []byte
	if err := db.Reader.Get(&stored, db.Rebind(`SELECT addr_start FROM prefix WHERE id = ?`), "p-narrow"); err != nil {
		t.Fatalf("reading back addr_start: %v", err)
	}
	if want := []byte{10, 20, 30, 0}; !bytes.Equal(stored, want) {
		t.Fatalf("addr_start round-trip = %v, want %v", stored, want)
	}

	cases := []struct {
		addr string
		want string
	}{
		{"10.20.30.5", "p-narrow"}, // most specific wins
		{"10.20.99.5", "p-wide"},   // only the /16 contains it
		{"192.168.1.1", "p-other"}, // different /16
		{"2001:db8::1", "p-v6"},    // v6 must not collide with v4 ranges
	}
	for _, tc := range cases {
		av, err := domain.ParseAddr(tc.addr)
		if err != nil {
			t.Fatalf("parsing addr %s: %v", tc.addr, err)
		}
		var got string
		// The ORDER BY is what makes "most specific" work: the longer
		// addr_start sorts first for mixed families, and within a family a
		// higher network address means a narrower prefix.
		err = db.Reader.Get(&got, db.Rebind(
			`SELECT id FROM prefix
			 WHERE addr_family = ? AND addr_start <= ? AND addr_end >= ?
			 ORDER BY addr_start DESC
			 LIMIT 1`),
			av.Family, av.Start, av.Start)
		if err != nil {
			t.Fatalf("resolving %s: %v", tc.addr, err)
		}
		if got != tc.want {
			t.Errorf("resolving %s = %s, want %s", tc.addr, got, tc.want)
		}
	}

	// A v4 address must never match a v6 prefix even though its 4 bytes are a
	// bytewise prefix of the 16-byte range. The family predicate is what
	// prevents it; assert the negative case explicitly.
	av, _ := domain.ParseAddr("32.1.13.184") // 0x20 0x01 0x0d 0xb8 -- 2001:db8 as v4
	var n int
	err := db.Reader.Get(&n, db.Rebind(
		`SELECT COUNT(*) FROM prefix WHERE addr_family = 6 AND addr_start <= ? AND addr_end >= ?`),
		av.Start, av.Start)
	if err != nil {
		t.Fatalf("cross-family query: %v", err)
	}
	if n != 0 {
		t.Errorf("a v4 address matched %d v6 prefixes; family predicate is not doing its job", n)
	}
}
