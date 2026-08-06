// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"net"
	"strings"
	"time"
)

// TLS certificates: what they cover, when they stop working, and who renews
// them.
//
// THE EXPIRY IS THE POINT. Everything else here exists to make an expiry
// actionable. `not_after` is an EOL date in every way that matters, so a
// certificate flows into the same report as hardware support and licences --
// and it is the entry in that report most likely to be the reason somebody is
// reading it, because a certificate expires every ninety days rather than every
// five years.
//
// NEVER KEY MATERIAL. KeyRef holds a path or a vault reference. The public
// certificate body is not secret and is not stored either -- it is bulk with no
// query value, and a field that accepts certificate-shaped text is where a
// private key eventually gets pasted. If a code path would put key material in
// this database, stop and raise it.
//
// DECLARED. Somebody asserts that this certificate is deployed here. What a
// scanner finds actually being served is a different fact for the observed side,
// and the disagreement between the two is the interesting part.

// Certificate is one TLS certificate, wherever it is deployed.
type Certificate struct {
	ID string `db:"id"`
	// SubjectCN is what a person calls it, even when it covers a dozen names.
	SubjectCN string  `db:"subject_cn"`
	Issuer    *string `db:"issuer"`
	// Fingerprint is the certificate's identity: lowercase hex, no colons.
	// Unique in the schema, because two rows sharing one is one certificate
	// entered twice and every count downstream would double.
	Fingerprint *string `db:"fingerprint"`
	Serial      *string `db:"serial"`
	NotBefore   *string `db:"not_before"`
	NotAfter    *string `db:"not_after"`
	// KeyRef is a PATH or a vault reference. Never a key.
	KeyRef *string `db:"key_ref"`
	// Who renews it -- often not the team that runs the box it sits on.
	TeamID      *string `db:"team_id"`
	ManagerRole *string `db:"manager_role"`
	Lifecycle   string  `db:"lifecycle"`
	Attrs       string  `db:"attrs"`
	CreatedAt   string  `db:"created_at"`
	UpdatedAt   string  `db:"updated_at"`

	// SANs are every name this certificate covers, subject included, validated
	// and normalised. Not a column -- they live in certificate_san -- but they
	// are carried HERE rather than passed to the store beside the certificate,
	// because a separate parameter is a seam through which an unvalidated name
	// can reach the database. The constructor fills this in; the store reads it.
	SANs []string `db:"-"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// CertificateLifecycles reuses the estate-wide set.
var CertificateLifecycles = []string{
	LifecyclePlanned, LifecycleActive, LifecycleDeprecated, LifecycleRetired,
}

// CertificateSpec is what a caller supplies.
type CertificateSpec struct {
	SubjectCN   string
	Issuer      *string
	Fingerprint *string
	Serial      *string
	NotBefore   *string
	NotAfter    *string
	KeyRef      *string
	TeamID      *string
	ManagerRole *string
	Lifecycle   string
	// SANs are the additional names it covers. The subject is added to the
	// stored set automatically -- a reader asking "what covers this name"
	// should not have to know that one of the names lives in another column.
	SANs []string
}

// NewCertificate validates and constructs.
func NewCertificate(id string, spec CertificateSpec, now time.Time) (*Certificate, error) {
	ve := &ValidationError{}
	c := &Certificate{
		ID:        id,
		SubjectCN: strings.ToLower(checkRequired(ve, "subject_cn", spec.SubjectCN)),
		Issuer:    checkTextField(ve, "issuer", blankToNil(spec.Issuer), maxIssuer),
		Serial:    checkTextField(ve, "serial", blankToNil(spec.Serial), maxSerial),
		KeyRef:    checkTextField(ve, "key_ref", blankToNil(spec.KeyRef), maxKeyRef),
		Attrs:     "{}",
		CreatedAt: FormatTime(now),
		UpdatedAt: FormatTime(now),
	}

	c.SANs = checkSANs(ve, c.SubjectCN, spec.SANs)
	c.Fingerprint = normaliseFingerprint(ve, spec.Fingerprint)
	c.NotBefore = checkDate(ve, "not_before", spec.NotBefore)
	c.NotAfter = checkDate(ve, "not_after", spec.NotAfter)
	if c.NotBefore != nil && c.NotAfter != nil && *c.NotAfter < *c.NotBefore {
		ve.Add("not_after", "cannot be before the date it becomes valid")
	}

	c.TeamID, c.ManagerRole = checkResponsibility(ve, spec.TeamID, spec.ManagerRole)

	lifecycle := strings.TrimSpace(spec.Lifecycle)
	if lifecycle == "" {
		lifecycle = LifecycleActive
	}
	if !containsString(CertificateLifecycles, lifecycle) {
		ve.Add("lifecycle", "must be one of %s", strings.Join(CertificateLifecycles, ", "))
	}
	c.Lifecycle = lifecycle

	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate re-checks a certificate after field updates.
func (c *Certificate) Validate() error {
	ve := &ValidationError{}
	c.SubjectCN = strings.ToLower(checkRequired(ve, "subject_cn", c.SubjectCN))
	c.SANs = checkSANs(ve, c.SubjectCN, c.SANs)
	c.Fingerprint = normaliseFingerprint(ve, c.Fingerprint)
	c.NotBefore = checkDate(ve, "not_before", c.NotBefore)
	c.NotAfter = checkDate(ve, "not_after", c.NotAfter)
	if c.NotBefore != nil && c.NotAfter != nil && *c.NotAfter < *c.NotBefore {
		ve.Add("not_after", "cannot be before the date it becomes valid")
	}
	c.Issuer = checkTextField(ve, "issuer", c.Issuer, maxIssuer)
	c.Serial = checkTextField(ve, "serial", c.Serial, maxSerial)
	c.KeyRef = checkTextField(ve, "key_ref", c.KeyRef, maxKeyRef)
	c.TeamID, c.ManagerRole = checkResponsibility(ve, c.TeamID, c.ManagerRole)
	checkEnum(ve, "lifecycle", c.Lifecycle, CertificateLifecycles)
	if strings.TrimSpace(c.Attrs) == "" {
		c.Attrs = "{}"
	}
	return ve.OrNil()
}

// IsRetired reports whether this certificate has been retired.
func (c *Certificate) IsRetired() bool { return c.Lifecycle == LifecycleRetired }

// normaliseFingerprint reduces a fingerprint to lowercase hex with no
// separators, so the same certificate pasted from two tools is one row.
//
// openssl prints colons, some consoles print spaces, and browsers vary on case.
// Storing them verbatim would let one certificate occupy several rows, and the
// UNIQUE constraint that makes the fingerprint an identity would never fire.
func normaliseFingerprint(ve *ValidationError, raw *string) *string {
	if raw == nil {
		return nil
	}
	// A flag rather than a sentinel rune. strings.Map DROPS any negative return
	// rather than inserting one, so marking bad input that way deleted it
	// instead: "zz:qq" came out as the empty string and was then treated as no
	// fingerprint at all. Silently discarding what somebody typed is the worst
	// of the three possible behaviours.
	var invalid bool
	var b strings.Builder
	b.Grow(len(*raw))
	for _, r := range *raw {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			b.WriteRune(r)
		case r >= 'A' && r <= 'F':
			b.WriteRune(r + ('a' - 'A'))
		case r == ':' || r == ' ' || r == '-' || r == '\t':
			// Separators openssl, browsers and consoles each print differently.
		default:
			invalid = true
		}
	}
	cleaned := b.String()

	if invalid {
		ve.Add("fingerprint", "must be hexadecimal, like the output of openssl x509 -fingerprint")
		return raw
	}
	if cleaned == "" {
		return nil
	}
	// SHA-1 is 40 hex characters and SHA-256 is 64. Anything else is a
	// truncated paste, and a truncated fingerprint is worse than none: it looks
	// like an identity and is not one.
	if len(cleaned) != 40 && len(cleaned) != 64 {
		ve.Add("fingerprint", "should be 40 characters (SHA-1) or 64 (SHA-256); got %d", len(cleaned))
		return raw
	}
	return &cleaned
}

// Field limits. Generous enough that no real certificate is refused, tight
// enough that none of these becomes a place to paste a document.
const (
	// 253 is the maximum length of a DNS name.
	maxHostname = 253
	maxIssuer   = 256
	maxSerial   = 128
	maxKeyRef   = 512
	// MaxSANs bounds one certificate's name set. Real certificates run to a few
	// dozen; a hundred is beyond anything legitimate and short of anything that
	// would make one transaction expensive on the single SQLite writer.
	MaxSANs = 100
)

// checkTextField caps a free-text field and rejects control characters.
//
// The cap is not tidiness. Everything here is snapshotted into the append-only
// change_log, so an unbounded field is a place where a pasted document -- a
// private key, somebody's email, a support thread -- becomes permanent and
// unerasable. A limit is the only thing standing between a paste and forever.
//
// NUL is rejected specifically: PostgreSQL refuses it in TEXT and SQLite does
// not, so a NUL-carrying value is a row that saves on one engine and 500s on
// the other.
func checkTextField(ve *ValidationError, field string, value *string, max int) *string {
	if value == nil {
		return nil
	}
	v := strings.TrimSpace(*value)
	if v == "" {
		return nil
	}
	if len(v) > max {
		ve.Add(field, "is longer than %d characters", max)
		return value
	}
	for _, r := range v {
		if r == 0 || (r < 0x20 && r != '\t') {
			ve.Add(field, "contains a control character")
			return value
		}
	}
	return &v
}

// checkSANs validates and normalises the names a certificate covers.
//
// VALIDATION, NOT JUST CLEANING. This is the field an operator pastes into, and
// a paste is how key material gets into a database that swore it would hold
// none: the SAN set is stored, indexed for search, AND folded into the audited
// value, so anything accepted here is permanent. A security review demonstrated
// it -- a pasted PEM block became seven searchable rows and an unerasable
// change_log entry. Each token must now look like a hostname or an IP address.
//
// Lowercased because DNS is case-insensitive: `Orders.example.com` and
// `orders.example.com` are one name, and storing both would make "which
// certificate covers this host" depend on how somebody typed it.
func checkSANs(ve *ValidationError, subject string, sans []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(sans)+1)
	var rejected bool

	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		name = strings.TrimSuffix(name, ".") // a trailing dot is the same host
		if name == "" || seen[name] {
			return
		}
		if len(out) >= MaxSANs {
			ve.Add("sans", "more than %d names; a certificate with that many is not "+
				"what this field is for", MaxSANs)
			return
		}
		if !validSANName(name) {
			rejected = true
			ve.Add("sans", "%q is not a hostname or an IP address. This field takes the "+
				"names a certificate covers -- never a key, a certificate body or a note",
				truncateForMessage(name))
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	add(subject)
	for _, name := range sans {
		add(name)
	}

	// ALL OR NOTHING. Keeping the tokens that happen to parse while rejecting
	// the rest is how a paste gets in piecemeal: a PEM block splits into armour
	// lines that fail and base64 lines that are, letter for letter, valid
	// single-label hostnames. Half a key stored is still a key stored, and the
	// operator has to fix the field and resubmit either way.
	if rejected {
		return nil
	}
	return out
}

// validSANName reports whether a name is a hostname or an IP address.
//
// A leading `*.` wildcard label is allowed and nothing else is: a wildcard
// anywhere but the first label is not something any client honours.
func validSANName(name string) bool {
	if len(name) > maxHostname {
		return false
	}
	if net.ParseIP(name) != nil {
		return true
	}
	rest := strings.TrimPrefix(name, "*.")
	if rest == "" || rest != name && strings.Contains(rest, "*") {
		return false
	}
	labels := strings.Split(rest, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			isLetter := (r >= 'a' && r <= 'z')
			isDigit := r >= '0' && r <= '9'
			if !isLetter && !isDigit && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}

// truncateForMessage keeps a rejected value short enough to render. A pasted
// key in an error message is a pasted key on a screen.
func truncateForMessage(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:40] + "…"
}

// CoversHost reports whether a name matches one of a certificate's names,
// honouring a single leading wildcard label.
//
// `*.example.com` matches `orders.example.com` and NOT `example.com` or
// `a.b.example.com` -- which is what RFC 6125 says and, more to the point, what
// every TLS client actually does. A CMDB that answered this question more
// loosely than the clients do would tell somebody they were covered when they
// were about to serve a name error.
func CoversHost(certNames []string, host string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	if host == "" {
		return false
	}
	for _, name := range certNames {
		if name == host {
			return true
		}
		if !strings.HasPrefix(name, "*.") {
			continue
		}
		suffix := name[1:] // ".example.com"
		if !strings.HasSuffix(host, suffix) {
			continue
		}
		// Exactly one label may stand in for the wildcard.
		if label := host[:len(host)-len(suffix)]; label != "" && !strings.Contains(label, ".") {
			return true
		}
	}
	return false
}
