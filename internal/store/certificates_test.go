// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
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
	if err := s.CreateCertificate(ctx, testActor, c); err != nil {
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
			c.SANs = []string{"orders.example.com", "www.example.com"}
			if err := s.UpdateCertificate(ctx, testActor, &c); err != nil {
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
			if err := s.CreateCertificate(ctx, testActor, c); err != nil {
				t.Fatalf("creating: %v", err)
			}

			var diffs []string
			if err := s.read(ctx, &diffs,
				`SELECT diff FROM change_log WHERE entity_type = ?`, "certificate"); err != nil {
				t.Fatalf("reading: %v", err)
			}
			// Without this the loop below iterates nothing and the test passes
			// against a store that writes no audit entry at all -- the exact
			// shape a review found in this very test.
			if len(diffs) == 0 {
				t.Fatal("no audit entry was written; this test would pass over anything")
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
				err = s.CreateCertificate(ctx, testActor, c)
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
			if err := s.CreateCertificate(ctx, testActor, c); err != nil {
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

// TestAnAuditStructMayNotEmbedAPointer.
//
// The certificate audit embedded *domain.Certificate and produced entries with
// every column missing, because auditFields matched neither the anonymous-struct
// branch nor the db-tag one. It was invisible for a week and surfaced only by
// mutating an unrelated rule.
//
// auditFields now panics on the shape. This asserts the panic exists and that it
// says what to do, because the failure it replaces — an audit trail that looks
// complete and is empty — is the worst kind this codebase can have.
func TestAnAuditStructMayNotEmbedAPointer(t *testing.T) {
	type badAudit struct {
		*domain.Certificate
		Extra string `db:"extra"`
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("an anonymous pointer embed did not panic, so the silent-empty-audit " +
				"bug can be written again and nothing will say so")
		}
		message, _ := r.(string)
		if !strings.Contains(message, "embed it by value") {
			t.Errorf("the panic does not say how to fix it: %v", r)
		}
	}()

	auditFields(reflect.ValueOf(badAudit{Certificate: &domain.Certificate{}}),
		map[string]auditField{})
}

// TestUndeployingSomethingThatWasNeverDeployed.
//
// From a security review. The retire path used to log unconditionally, so
// POSTing it with two ids that had never been deployed together wrote a removal
// into the append-only trail and reported success — a fabricated fact, which is
// worse than a missing one.
func TestUndeployingSomethingThatWasNeverDeployed(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			asset := mustAsset(t, s, ctx, domain.KindServer, "lb-01", nil, env)
			id := mustCertificate(t, s, ctx, "orders.example.com", nil, "2027-01-01")

			before, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "certificate")
			if err != nil {
				t.Fatalf("counting: %v", err)
			}

			err = s.UndeployCertificateFromAsset(ctx, testActor, id, asset)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("retiring a deployment that never existed returned %v, want ErrNotFound", err)
			}

			after, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "certificate")
			if err != nil {
				t.Fatalf("counting after: %v", err)
			}
			if after != before {
				t.Errorf("change_log gained %d entries for a removal that never happened",
					after-before)
			}
		})
	}
}

// Re-recording a deployment that is already there, unchanged, writes nothing.
// An audit trail full of entries saying nothing changed is worse than one
// without them — the rule logUpdate enforces and a hand-built diff opts out of.
func TestRedeployingUnchangedWritesNoAuditEntry(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			asset := mustAsset(t, s, ctx, domain.KindServer, "lb-01", nil, env)
			id := mustCertificate(t, s, ctx, "orders.example.com", nil, "2027-01-01")

			note := "the https listener"
			if err := s.DeployCertificateToAsset(ctx, testActor, id, asset, &note); err != nil {
				t.Fatalf("deploying: %v", err)
			}
			before, _ := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "certificate")

			// The same deployment, the same note.
			if err := s.DeployCertificateToAsset(ctx, testActor, id, asset, &note); err != nil {
				t.Fatalf("re-deploying: %v", err)
			}
			same, _ := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "certificate")
			if same != before {
				t.Errorf("re-recording an unchanged deployment wrote %d audit entries", same-before)
			}

			// The control: a CHANGED note is a change and must be recorded, or
			// the no-op check has become a silence.
			other := "moved to the management listener"
			if err := s.DeployCertificateToAsset(ctx, testActor, id, asset, &other); err != nil {
				t.Fatalf("changing the note: %v", err)
			}
			changed, _ := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "certificate")
			if changed != same+1 {
				t.Errorf("changing a deployment note wrote %d entries, want 1", changed-same)
			}
		})
	}
}

// An unknown role must come back as a field error, not as a foreign-key
// violation the handler can only render as a bare 422.
func TestAnUnknownRoleOnACertificateIsAFieldError(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: "net", Name: "Network"}, s.Now())
			if err != nil {
				t.Fatalf("building the team: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating the team: %v", err)
			}

			role := "chief-certificate-officer"
			c, err := domain.NewCertificate(NewID(), domain.CertificateSpec{
				SubjectCN: "orders.example.com", TeamID: &team.ID, ManagerRole: &role,
			}, s.Now())
			if err != nil {
				t.Fatalf("the constructor rejected the shape rather than the store rejecting the value: %v", err)
			}
			err = s.CreateCertificate(ctx, testActor, c)
			if err == nil {
				t.Fatal("a role that is not in the lookup table was accepted")
			}
			if ve, ok := domain.AsValidation(err); !ok {
				t.Errorf("the error is %T, not a *ValidationError, so the form cannot "+
					"highlight the field: %v", err, err)
			} else if _, named := ve.Messages()["manager_role"]; !named {
				t.Errorf("the error does not name manager_role: %v", ve.Messages())
			}
		})
	}
}

// TestTheCascadeOnCertificateNamesIsRealButUnreachable.
//
// certificate_san carries the only ON DELETE CASCADE in the schema, and a
// database review showed that removing it from one dialect half was invisible to
// the entire suite. It pins both halves of a deliberate decision: the cascade
// works, and nothing in the application can reach it.
func TestTheCascadeOnCertificateNamesIsRealButUnreachable(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			// Deployed nowhere: the only case the cascade can fire for.
			loose := mustCertificate(t, s, ctx, "loose.example.com",
				[]string{"alt.example.com"}, "2027-01-01")
			if _, err := s.db.Writer.Exec(s.db.Writer.Rebind(
				`DELETE FROM certificate WHERE id = ?`), loose); err != nil {
				t.Fatalf("a certificate deployed nowhere could not be deleted: %v", err)
			}
			n, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM certificate_san WHERE certificate_id = ?`, loose)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 0 {
				t.Errorf("%d names outlived their certificate; the cascade did not fire", n)
			}

			// Deployed anywhere: NO ACTION on the link tables makes the delete
			// impossible, so the cascade is unreachable for any certificate the
			// estate actually uses.
			used := mustCertificate(t, s, ctx, "used.example.com", nil, "2027-01-01")
			asset := mustAsset(t, s, ctx, domain.KindServer, "lb-01", nil, env)
			if err := s.DeployCertificateToAsset(ctx, testActor, used, asset, nil); err != nil {
				t.Fatalf("deploying: %v", err)
			}
			if _, err := s.db.Writer.Exec(s.db.Writer.Rebind(
				`DELETE FROM certificate WHERE id = ?`), used); err == nil {
				t.Error("a deployed certificate was deleted; the link tables should refuse it")
			}
		})
	}
}

// Several certificates may have no fingerprint recorded. Both engines treat
// NULLs as distinct in a UNIQUE index, and normaliseFingerprint stores NULL
// rather than "" for a blank field — if it stored the empty string, the SECOND
// blank certificate would be refused on both engines.
func TestSeveralCertificatesMayHaveNoFingerprint(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			for _, subject := range []string{"a.example.com", "b.example.com", "c.example.com"} {
				blank := ""
				c, err := domain.NewCertificate(NewID(), domain.CertificateSpec{
					SubjectCN: subject, Fingerprint: &blank,
				}, s.Now())
				if err != nil {
					t.Fatalf("building %s: %v", subject, err)
				}
				if c.Fingerprint != nil {
					t.Fatalf("a blank fingerprint became %q rather than nothing; the second "+
						"such certificate would be refused", *c.Fingerprint)
				}
				if err := s.CreateCertificate(ctx, testActor, c); err != nil {
					t.Fatalf("creating %s with no fingerprint: %v", subject, err)
				}
			}
		})
	}
}

// TestExpiryCountsEveryPlaceACertificateIsDeployed.
//
// expiryCertificateReach had no test at all — `expiry_test.go` contained the word
// "certificate" zero times. Three things were unexercised: the ServiceCount
// reuse, the SUM(COUNT(*)) that returns numeric on PostgreSQL and INTEGER on
// SQLite, and the two-IN-list argument ordering its own comment worries about
// ("one edit away from silently swapping an id for a lifecycle"). Nothing would
// have caught that swap.
//
// Asymmetric counts on purpose: two assets and one service, so transposing the
// two halves of the UNION changes the answer.
func TestExpiryCountsEveryPlaceACertificateIsDeployed(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			soon := expiryDate(20)
			id := mustCertificate(t, s, ctx, "orders.example.com", nil, soon)

			for _, name := range []string{"lb-01", "lb-02"} {
				asset := mustAsset(t, s, ctx, domain.KindServer, name, nil, env)
				if err := s.DeployCertificateToAsset(ctx, testActor, id, asset, nil); err != nil {
					t.Fatalf("deploying to %s: %v", name, err)
				}
			}
			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "orders-web", Name: "Orders Web", Kind: domain.SvcWeb,
				EnvironmentID: env, Availability: domain.AvailStandalone, Tier: 2,
			}, s.Now())
			if err != nil {
				t.Fatalf("building the service: %v", err)
			}
			if err := s.CreateService(ctx, testPermit, svc); err != nil {
				t.Fatalf("creating the service: %v", err)
			}
			if err := s.DeployCertificateToService(ctx, testActor, id, svc.ID, nil); err != nil {
				t.Fatalf("deploying to the service: %v", err)
			}

			report, err := s.Expiring(ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("running the report: %v", err)
			}

			var found bool
			for _, row := range report.Rows {
				if row.EntityType != "certificate" {
					continue
				}
				found = true
				// Two assets plus one service. A transposed UNION would give a
				// different number, and so would counting one table only.
				if row.ServiceCount != 3 {
					t.Errorf("the certificate reports %d deployments, want 3 "+
						"(two assets and one service)", row.ServiceCount)
				}
				if row.Name != "orders.example.com" {
					t.Errorf("the row is named %q", row.Name)
				}
			}
			if !found {
				t.Fatal("the expiring certificate is not in the report at all")
			}

			// A RETIRED deployment stops counting: an expiring certificate that
			// was taken off a box is not still an outage there.
			assets, err := s.ListCertificateAssets(ctx, id)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if err := s.UndeployCertificateFromAsset(ctx, testActor, id, assets[0].EntityID); err != nil {
				t.Fatalf("undeploying: %v", err)
			}
			after, err := s.Expiring(ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("re-running: %v", err)
			}
			for _, row := range after.Rows {
				if row.EntityType == "certificate" && row.ServiceCount != 2 {
					t.Errorf("after retiring one deployment the count is %d, want 2",
						row.ServiceCount)
				}
			}
		})
	}
}
