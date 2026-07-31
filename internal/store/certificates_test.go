package store

import (
	"context"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

func mustCertificate(t *testing.T, s *SQLStore, ctx context.Context, subject string,
	sans []string, notAfter string) string {
	t.Helper()
	spec := domain.CertificateSpec{SubjectCN: subject, SANs: sans}
	if notAfter != "" {
		spec.NotAfter = &notAfter
	}
	c, err := domain.NewCertificate(NewID(), spec, s.Now())
	if err != nil {
		t.Fatalf("building certificate %s: %v", subject, err)
	}
	if err := s.CreateCertificate(ctx, testActor, c, sans); err != nil {
		t.Fatalf("creating certificate %s: %v", subject, err)
	}
	return c.ID
}

// TestFindingACertificateByTheNameItCovers is the query this feature exists to
// answer, and the reason the names are a child table rather than a string.
//
// Every assertion carries its opposite: a wildcard must cover one label and NOT
// two, and must NOT cover the apex. A suffix match — the easy implementation —
// gets all three wrong in the direction that reassures somebody they are covered.
func TestFindingACertificateByTheNameItCovers(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			wildcard := mustCertificate(t, s, ctx, "*.example.com",
				[]string{"example.com"}, "2027-01-01")
			exact := mustCertificate(t, s, ctx, "internal.test",
				nil, "2027-01-01")

			find := func(host string) []string {
				t.Helper()
				rows, err := s.ListCertificates(ctx, CertificateFilter{Host: host})
				if err != nil {
					t.Fatalf("searching for %s: %v", host, err)
				}
				out := make([]string, len(rows))
				for i, r := range rows {
					out[i] = r.ID
				}
				return out
			}

			if got := find("orders.example.com"); len(got) != 1 || got[0] != wildcard {
				t.Errorf("orders.example.com found %v, want just the wildcard", got)
			}
			// The apex is listed explicitly, so it matches — through the SAN,
			// not through the wildcard.
			if got := find("example.com"); len(got) != 1 || got[0] != wildcard {
				t.Errorf("example.com found %v, want the wildcard via its SAN", got)
			}
			// Two labels: no TLS client accepts this and neither does the report.
			if got := find("a.b.example.com"); len(got) != 0 {
				t.Errorf("a.b.example.com matched %v; a wildcard covers one label", got)
			}
			// The control: an unrelated certificate is never swept in.
			if got := find("internal.test"); len(got) != 1 || got[0] != exact {
				t.Errorf("internal.test found %v, want just the exact certificate", got)
			}
			if got := find("nothing.here"); len(got) != 0 {
				t.Errorf("an unknown host matched %v", got)
			}
		})
	}
}

// The subject is stored among the names, so a reader asking what covers a host
// does not have to know that one of the names lives in another column.
func TestTheSubjectIsStoredAmongTheNames(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := mustCertificate(t, s, ctx, "orders.example.com",
				[]string{"www.example.com"}, "2027-01-01")

			row, err := s.GetCertificate(ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			var seen bool
			for _, n := range row.SANs {
				if n == "orders.example.com" {
					seen = true
				}
			}
			if !seen {
				t.Errorf("the subject is not among the stored names: %v", row.SANs)
			}
			if len(row.SANs) != 2 {
				t.Errorf("names = %v, want the subject and the one SAN", row.SANs)
			}
		})
	}
}

// TestChangingTheNamesIsAudited.
//
// certificate_san is a SET TABLE, replaced wholesale — which is exactly the
// shape that produced no audit entry three separate times in this codebase,
// because the parent struct did not change. certificateAudit folds the names in
// so a change to what a certificate covers cannot produce an empty diff.
func TestChangingTheNamesIsAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := mustCertificate(t, s, ctx, "orders.example.com", nil, "2027-01-01")

			before, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "certificate")
			if err != nil {
				t.Fatalf("counting: %v", err)
			}

			row, err := s.GetCertificate(ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			// ONLY the names change; every column on the certificate itself is
			// untouched. Without the fold this update produces an empty diff.
			c := row.Certificate
			if err := s.UpdateCertificate(ctx, testActor, &c, []string{"www.example.com"}); err != nil {
				t.Fatalf("updating the names: %v", err)
			}

			after, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "certificate")
			if err != nil {
				t.Fatalf("counting after: %v", err)
			}
			if after != before+1 {
				t.Fatalf("change_log went %d -> %d; a name change produced no audit entry",
					before, after)
			}

			var diff string
			if err := s.readOne(ctx, &diff,
				`SELECT diff FROM change_log WHERE entity_type = ? AND entity_id = ?
				 ORDER BY at DESC, id DESC`, "certificate", id); err != nil {
				t.Fatalf("reading the entry: %v", err)
			}
			if !strings.Contains(diff, "www.example.com") {
				t.Errorf("the audit entry does not say what the names became: %s", diff)
			}
		})
	}
}

// A key reference is a path, and the path never reaches the permanent trail —
// the same treatment secret_ref gets, and a certificate is the entity where
// somebody is likeliest to paste something worse than a path.
func TestAKeyReferenceNeverReachesTheAuditTrail(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			secret := "vault:secret/tls/orders"
			c, err := domain.NewCertificate(NewID(), domain.CertificateSpec{
				SubjectCN: "orders.example.com", KeyRef: &secret,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateCertificate(ctx, testActor, c, nil); err != nil {
				t.Fatalf("creating: %v", err)
			}

			var diffs []string
			if err := s.read(ctx, &diffs,
				`SELECT diff FROM change_log WHERE entity_type = ?`, "certificate"); err != nil {
				t.Fatalf("reading: %v", err)
			}
			for _, d := range diffs {
				if strings.Contains(d, secret) {
					t.Errorf("the key reference is in the permanent trail: %s", d)
				}
			}

			// The control: the row itself keeps it, because that one is
			// correctable and the operator needs to know where the key is.
			row, err := s.GetCertificate(ctx, c.ID)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if row.KeyRef == nil || *row.KeyRef != secret {
				t.Errorf("the certificate lost its key reference: %v", row.KeyRef)
			}
		})
	}
}

// Deployments are soft-retired, never deleted, and re-deploying somewhere it was
// removed from reactivates the row rather than failing on the primary key.
func TestDeploymentsAreRetiredAndCanComeBack(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			asset := mustAsset(t, s, ctx, domain.KindServer, "lb-01", nil, env)
			id := mustCertificate(t, s, ctx, "orders.example.com", nil, "2027-01-01")

			if err := s.DeployCertificateToAsset(ctx, testActor, id, asset, nil); err != nil {
				t.Fatalf("deploying: %v", err)
			}
			if got, _ := s.ListCertificateAssets(ctx, id); len(got) != 1 {
				t.Fatalf("deployment not recorded")
			}

			if err := s.UndeployCertificateFromAsset(ctx, testActor, id, asset); err != nil {
				t.Fatalf("undeploying: %v", err)
			}
			if got, _ := s.ListCertificateAssets(ctx, id); len(got) != 0 {
				t.Error("a retired deployment is still listed as current")
			}
			// Never deleted: the row survives so the history does.
			n, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM certificate_asset WHERE certificate_id = ?`, id)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 1 {
				t.Errorf("the deployment row was deleted rather than retired (%d rows)", n)
			}

			// And it can come back without tripping the primary key.
			if err := s.DeployCertificateToAsset(ctx, testActor, id, asset, nil); err != nil {
				t.Fatalf("re-deploying: %v", err)
			}
			if got, _ := s.ListCertificateAssets(ctx, id); len(got) != 1 {
				t.Error("re-deploying did not reactivate the row")
			}
		})
	}
}

// A fingerprint identifies a certificate, so the schema refuses a second row
// carrying one that already exists — otherwise one certificate entered twice
// doubles every count downstream.
func TestAFingerprintIsAnIdentity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			fp := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

			for i, subject := range []string{"a.example.com", "b.example.com"} {
				c, err := domain.NewCertificate(NewID(), domain.CertificateSpec{
					SubjectCN: subject, Fingerprint: &fp,
				}, s.Now())
				if err != nil {
					t.Fatalf("building: %v", err)
				}
				err = s.CreateCertificate(ctx, testActor, c, nil)
				if i == 0 && err != nil {
					t.Fatalf("the first certificate was refused: %v", err)
				}
				if i == 1 && err == nil {
					t.Error("two certificates share one fingerprint; every count downstream doubles")
				}
			}
		})
	}
}

// TestTheCertificateSnapshotIsComplete.
//
// certificateAudit embedded *domain.Certificate rather than domain.Certificate.
// It compiled, it ran, and it produced an audit entry containing ONLY the SANs:
// snapshotJSON walks db-tagged fields and does not follow an embedded pointer,
// so every column of the certificate was absent from the permanent trail.
//
// Nothing caught it. TestChangingTheNamesIsAudited passed because the SANs are
// on the outer struct; TestAKeyReferenceNeverReachesTheAuditTrail passed for the
// wrong reason entirely — the key was missing because EVERYTHING was missing,
// and deleting the redaction rule did not make it fail. Found by mutating that
// rule and noticing the test did not care.
//
// An audit entry that records nothing is worse than no entry, because it looks
// like coverage. So this asserts the columns are present by name.
func TestTheCertificateSnapshotIsComplete(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			key, issuer, notAfter := "vault:secret/tls/orders", "Internal CA", "2027-01-01"
			c, err := domain.NewCertificate(NewID(), domain.CertificateSpec{
				SubjectCN: "orders.example.com", KeyRef: &key,
				Issuer: &issuer, NotAfter: &notAfter,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateCertificate(ctx, testActor, c, []string{"www.example.com"}); err != nil {
				t.Fatalf("creating: %v", err)
			}

			var diff string
			if err := s.readOne(ctx, &diff,
				`SELECT diff FROM change_log WHERE entity_type = ?`, "certificate"); err != nil {
				t.Fatalf("reading the entry: %v", err)
			}

			// Every column the certificate owns, by name.
			for _, column := range []string{
				"id", "subject_cn", "issuer", "fingerprint", "serial",
				"not_before", "not_after", "key_ref", "team_id", "manager_role",
				"lifecycle", "attrs", "created_at", "sans",
			} {
				if !strings.Contains(diff, `"`+column+`"`) {
					t.Errorf("the snapshot is missing %q: %s", column, diff)
				}
			}
			// And the values that must be there, and the one that must not.
			if !strings.Contains(diff, "orders.example.com") {
				t.Error("the snapshot does not record the subject")
			}
			if !strings.Contains(diff, notAfter) {
				t.Error("the snapshot does not record the expiry")
			}
			if strings.Contains(diff, key) {
				t.Errorf("the key reference is in the permanent trail: %s", diff)
			}
			if !strings.Contains(diff, "[redacted]") {
				t.Error("the key reference is absent rather than redacted; the trail should " +
					"say that a key reference exists without saying what it is")
			}
		})
	}
}
