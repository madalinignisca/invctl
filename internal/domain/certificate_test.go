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
	got := NormaliseSANs("Orders.Example.com", []string{
		"www.example.com", "ORDERS.example.com", " api.example.com. ", "", "www.example.com",
	})

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
