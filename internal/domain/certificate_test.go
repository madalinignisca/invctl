// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"testing"
	"time"
)

var certNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// TestCoversHostMatchesTheWayAClientDoes.
//
// A CMDB that answered this more loosely than a TLS client does would tell
// somebody they were covered while they were about to serve a name error, and
// more loosely is the easy mistake: a suffix match, or a LIKE, gets every case
// below wrong in the direction that reassures.
func TestCoversHostMatchesTheWayAClientDoes(t *testing.T) {
	names := []string{"example.com", "*.example.com", "orders.example.com"}

	cases := []struct {
		host string
		want bool
	}{
		{"example.com", true},          // exact
		{"orders.example.com", true},   // exact, and also wildcard
		{"anything.example.com", true}, // one label under the wildcard
		{"EXAMPLE.COM", true},          // DNS is case-insensitive
		{"example.com.", true},         // a trailing dot is the same host

		// The three a suffix match gets wrong.
		{"a.b.example.com", false}, // two labels: a wildcard covers one
		{"notexample.com", false},  // suffix of the string, not of the name
		{"", false},

		{"other.com", false},
	}

	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			if got := CoversHost(names, c.host); got != c.want {
				t.Errorf("CoversHost(%q) = %v, want %v", c.host, got, c.want)
			}
		})
	}

	// A bare wildcard must not match the apex, which is the case people are
	// most often surprised by in production.
	if CoversHost([]string{"*.example.com"}, "example.com") {
		t.Error("a wildcard matched the apex; no TLS client does that")
	}
}

func TestNormaliseSANsAlwaysIncludesTheSubject(t *testing.T) {
	ve := &ValidationError{}
	got := checkSANs(ve, "orders.example.com", []string{
		"www.example.com", "ORDERS.example.com", " api.example.com. ", "", "www.example.com",
	})
	if err := ve.OrNil(); err != nil {
		t.Fatalf("valid names were rejected: %v", err)
	}

	want := []string{"orders.example.com", "www.example.com", "api.example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d is %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	// The subject leads, because a reader scanning the list wants the name the
	// certificate is called by first.
	if got[0] != "orders.example.com" {
		t.Errorf("the subject is not first: %v", got)
	}
}

// A fingerprint is the certificate's identity, so the same certificate pasted
// from two tools must be one value. openssl prints colons, browsers vary on
// case, consoles insert spaces.
func TestFingerprintNormalisation(t *testing.T) {
	sha1 := "AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01"
	sha256 := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	cases := []struct {
		name    string
		in      *string
		want    string
		wantErr bool
	}{
		{"absent stays absent", nil, "", false},
		{"colons and case", &sha1, "abcdef0123456789abcdef0123456789abcdef01", false},
		{"already clean", &sha256, sha256, false},
		{"spaces are separators too", strptr("ab cd ef 01 23 45 67 89 ab cd ef 01 23 45 67 89 ab cd ef 01"),
			"abcdef0123456789abcdef0123456789abcdef01", false},

		// Not hex. The first version of this silently DELETED the bad
		// characters and returned "no fingerprint at all" -- strings.Map drops
		// a negative return rather than inserting a sentinel. Discarding what
		// somebody typed is the worst of the three possible behaviours.
		{"not hexadecimal", strptr("zz:qq"), "", true},
		{"a whole sentence", strptr("see the ticket"), "", true},

		// A truncated fingerprint looks like an identity and is not one.
		{"too short", strptr("abcdef01"), "", true},
		{"between the two lengths", strptr("abcdef0123456789abcdef0123456789abcdef0123"), "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ve := &ValidationError{}
			got := normaliseFingerprint(ve, c.in)
			if err := ve.OrNil(); (err != nil) != c.wantErr {
				t.Fatalf("error = %v, want error: %v", err, c.wantErr)
			}
			if c.wantErr {
				// It must hand back what was typed rather than nil, so the
				// re-rendered form still shows it.
				if got == nil {
					t.Error("a rejected fingerprint was discarded rather than returned")
				}
				return
			}
			if c.want == "" {
				if got != nil {
					t.Errorf("got %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != c.want {
				t.Errorf("got %v, want %q", got, c.want)
			}
		})
	}
}

func TestNewCertificateRejectsWhatWouldMislead(t *testing.T) {
	before, after := "2026-06-01", "2026-01-01"

	cases := []struct {
		name string
		spec CertificateSpec
	}{
		{"no subject", CertificateSpec{}},
		// A validity window that closes before it opens: the certificate would
		// silently never read as current.
		{"expires before it starts", CertificateSpec{
			SubjectCN: "x.example.com", NotBefore: &before, NotAfter: &after}},
		{"an impossible expiry", CertificateSpec{
			SubjectCN: "x.example.com", NotAfter: strptr("2027-02-31")}},
		// A role says who in a team renews it, so it means nothing alone.
		{"a role with no team", CertificateSpec{
			SubjectCN: "x.example.com", ManagerRole: strptr("operator")}},
		{"a lifecycle nothing knows", CertificateSpec{
			SubjectCN: "x.example.com", Lifecycle: "revoked"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewCertificate("c1", c.spec, certNow); err == nil {
				t.Error("accepted")
			}
		})
	}

	// No expiry at all is ACCEPTED: it is a real state, and the report calls it
	// out rather than the constructor refusing it.
	if _, err := NewCertificate("c1", CertificateSpec{SubjectCN: "x.example.com"}, certNow); err != nil {
		t.Errorf("a certificate with no recorded expiry was rejected: %v", err)
	}
}

// TestTheNamesFieldRefusesAPaste.
//
// From a security review, and it is the finding that mattered most. The SAN
// field is what an operator pastes into, and everything accepted there is
// stored, indexed for search, AND folded into the audited value — so anything
// that gets in becomes permanent in an append-only log. A pasted PEM block
// became seven searchable rows and an unerasable change_log entry.
//
// This codebase's own migration header named the threat — "a column that accepts
// certificate-shaped text is where a key eventually gets pasted" — and then left
// the field open. Each token must now look like a hostname or an IP.
func TestTheNamesFieldRefusesAPaste(t *testing.T) {
	pem := []string{
		"-----BEGIN", "PRIVATE", "KEY-----",
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ",
		"-----END", "PRIVATE", "KEY-----",
	}
	ve := &ValidationError{}
	got := checkSANs(ve, "orders.example.com", pem)

	if err := ve.OrNil(); err == nil {
		t.Fatal("a pasted private key was accepted as a list of names")
	}
	// NOTHING is kept, not even the tokens that happen to parse. A PEM block
	// splits into armour lines that fail and base64 lines that are, letter for
	// letter, valid single-label hostnames -- so keeping the valid-looking ones
	// stores half a key. The operator fixes the field and resubmits.
	if len(got) != 0 {
		t.Errorf("kept %v; a submission containing a paste stores nothing at all", got)
	}
	// And the error must not echo the key back onto the screen in full.
	for _, f := range ve.Fields {
		if len(f.Message) > 200 {
			t.Errorf("the error message is %d characters; a pasted key in an error "+
				"message is a pasted key on a screen", len(f.Message))
		}
	}
}

func TestNamesMustLookLikeNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"a hostname", "orders.example.com", true},
		{"a wildcard", "*.example.com", true},
		{"an underscore label", "_acme-challenge.example.com", true},
		{"a single label", "localhost", true},
		{"an IPv4 address", "10.20.30.5", true},
		{"an IPv6 address", "2001:db8::1", true},

		{"a wildcard in the middle", "a.*.example.com", false},
		{"a bare wildcard", "*", false},
		{"a space", "orders example.com", false},
		{"a slash", "https://orders.example.com", false},
		{"a PEM header", "-----BEGIN", false},
		{"base64", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ", false},
		{"an email address", "alice@example.com", false},
		{"a label starting with a hyphen", "-bad.example.com", false},
		{"an empty label", "a..example.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validSANName(c.in); got != c.ok {
				t.Errorf("validSANName(%q) = %v, want %v", c.in, got, c.ok)
			}
		})
	}

	// Over the DNS limit.
	long := ""
	for len(long) < 300 {
		long += "abcdefgh."
	}
	if validSANName(long) {
		t.Error("a name longer than a DNS name is allowed")
	}
}

// Free-text fields are capped, because everything here is snapshotted into an
// append-only log and an unbounded field is where a paste becomes permanent.
func TestFreeTextFieldsAreCapped(t *testing.T) {
	huge := make([]byte, 4096)
	for i := range huge {
		huge[i] = 'a'
	}
	big := string(huge)

	for _, field := range []string{"issuer", "serial", "key_ref"} {
		t.Run(field, func(t *testing.T) {
			spec := CertificateSpec{SubjectCN: "orders.example.com"}
			switch field {
			case "issuer":
				spec.Issuer = &big
			case "serial":
				spec.Serial = &big
			case "key_ref":
				spec.KeyRef = &big
			}
			if _, err := NewCertificate("c1", spec, certNow); err == nil {
				t.Errorf("a 4096-character %s was accepted", field)
			}
		})
	}

	// A NUL byte saves on SQLite and errors on PostgreSQL, so it is refused
	// before it can become a cross-engine 500.
	if _, err := NewCertificate("c1", CertificateSpec{
		SubjectCN: "orders.example.com", Issuer: strptr("Internal\x00CA"),
	}, certNow); err == nil {
		t.Error("a NUL byte was accepted into a text field")
	}
}

// An all-separator fingerprint is absent, not invalid: somebody clearing the
// field leaves colons behind more often than they leave letters.
func TestAnAllSeparatorFingerprintIsAbsent(t *testing.T) {
	for _, in := range []string{"::::", "   ", "-- --", ""} {
		ve := &ValidationError{}
		got := normaliseFingerprint(ve, &in)
		if err := ve.OrNil(); err != nil {
			t.Errorf("%q was rejected rather than treated as absent: %v", in, err)
		}
		if got != nil {
			t.Errorf("%q became %q rather than nothing", in, *got)
		}
	}
}
