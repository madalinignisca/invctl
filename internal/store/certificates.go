package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gabriel/invctl/internal/domain"
)

// Certificates, the names they cover, and where they are deployed.
//
// The SAN set is a SET TABLE, replaced wholesale inside the parent's
// transaction the way asset_environment is -- and the parent's change_log entry
// records the change, because three separate times in this codebase a set
// replacement produced no diff on the parent struct and therefore no audit
// entry at all. certificateAudit below folds the names into the audited value
// for exactly that reason.

// CertificateRow is a certificate plus what a list view needs.
type CertificateRow struct {
	domain.Certificate
	// Where it is deployed. Counted for the list; listed on the detail page.
	AssetCount   int `db:"asset_count"`
	ServiceCount int `db:"service_count"`
	// Who renews it, resolved.
	TeamCode        string `db:"team_code"`
	TeamName        string `db:"team_name"`
	ManagerRoleName string `db:"manager_role_name"`
}

const certificateSelect = `
	SELECT c.*,
	       (SELECT COUNT(*) FROM certificate_asset ca
	         WHERE ca.certificate_id = c.id AND ca.lifecycle = 'active')   AS asset_count,
	       (SELECT COUNT(*) FROM certificate_service cs
	         WHERE cs.certificate_id = c.id AND cs.lifecycle = 'active')   AS service_count,
	       COALESCE(tm.code, '') AS team_code,
	       COALESCE(tm.name, '') AS team_name,
	       COALESCE(rr.label, c.manager_role, '') AS manager_role_name
	FROM certificate c
	LEFT JOIN team tm ON tm.id = c.team_id
	LEFT JOIN responsibility_role rr ON rr.code = c.manager_role`

// CertificateFilter narrows a certificate list.
type CertificateFilter struct {
	TeamID string
	// Host finds the certificates covering a name. Matched in Go against the
	// loaded SAN sets rather than in SQL, because a wildcard match is not a
	// LIKE: `*.example.com` covers orders.example.com and must NOT cover
	// example.com or a.b.example.com, and encoding that in portable SQL would
	// be a worse lie than doing it where it can be tested.
	Host           string
	Query          string
	IncludeRetired bool
}

// ListCertificates returns certificates matching the filter, SANs attached.
func (s *SQLStore) ListCertificates(ctx context.Context, f CertificateFilter) ([]CertificateRow, error) {
	var where []string
	var args []any
	if !f.IncludeRetired {
		where = append(where, `c.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	if f.TeamID != "" {
		where = append(where, `c.team_id = ?`)
		args = append(args, f.TeamID)
	}
	if f.Query != "" {
		// ASCII-ONLY MATCHING, and `issuer` is the first field in this codebase
		// where that is likely to bite. `lower` folds bytes A-Z to match
		// SQLite's LOWER(); PostgreSQL's is locale-aware and folds the whole
		// string, so the two sides of the comparison fold asymmetrically there.
		// Measured by a database review: issuer "Bundesdruckerei ÖCA" searched
		// for "ÖCA" matches on SQLite and not on PostgreSQL.
		//
		// A subject is a hostname and ASCII in practice. An issuer is a CA's
		// display name, and for an EU estate D-TRUST, Bundesdruckerei and
		// SwissSign-style names with umlauts are ordinary. Swapping in
		// strings.ToLower does not fix it -- it moves the failure to SQLite,
		// whose LOWER() is ASCII-only either way. A folded shadow column would,
		// and is not worth a column yet. Stated rather than claimed away.
		where = append(where, `(LOWER(c.subject_cn) LIKE ? ESCAPE '\' OR LOWER(COALESCE(c.issuer, '')) LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(lower(f.Query)) + "%"
		args = append(args, like, like)
	}

	query := certificateSelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// Soonest expiry first, and undated last: a certificate with no recorded
	// expiry is not the least urgent thing in the estate, but it is the one
	// this ordering cannot rank, so it goes where a reader will still see it.
	query += ` ORDER BY CASE WHEN c.not_after IS NULL THEN 1 ELSE 0 END, c.not_after, c.subject_cn`

	var rows []CertificateRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing certificates: %w", err)
	}
	if err := s.attachSANs(ctx, rows); err != nil {
		return nil, err
	}

	if f.Host != "" {
		kept := rows[:0]
		for _, r := range rows {
			if domain.CoversHost(r.SANs, f.Host) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	// SORTED IN GO, and the SQL ORDER BY above is a hint rather than the
	// authority. expiry.go already states the rule -- "which of two rows sharing
	// a date comes first must not depend on the server's collation" -- and this
	// query did not follow it. The agreement observed in CI is an artefact of the
	// Alpine/musl PostgreSQL image, which implements no locale collation and so
	// degenerates to byte order; on a glibc or ICU-collated database this list
	// would order differently from SQLite. A database review ran the comparison
	// under three collations to show it.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		// Undated last: not the least urgent thing in the estate, but the one
		// this ordering cannot rank.
		aNull, bNull := a.NotAfter == nil, b.NotAfter == nil
		if aNull != bNull {
			return !aNull
		}
		if !aNull && derefOr(a.NotAfter, "") != derefOr(b.NotAfter, "") {
			return derefOr(a.NotAfter, "") < derefOr(b.NotAfter, "")
		}
		if a.SubjectCN != b.SubjectCN {
			return a.SubjectCN < b.SubjectCN
		}
		return a.ID < b.ID
	})
	if f.Query != "" {
		sort.SliceStable(rows, rankNames(f.Query, func(i int) string { return rows[i].SubjectCN }))
	}
	return rows, nil
}

// GetCertificate loads one certificate with its names.
func (s *SQLStore) GetCertificate(ctx context.Context, id string) (*CertificateRow, error) {
	var row CertificateRow
	if err := s.readOne(ctx, &row, certificateSelect+` WHERE c.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting certificate %s: %w", id, err)
	}
	rows := []CertificateRow{row}
	if err := s.attachSANs(ctx, rows); err != nil {
		return nil, err
	}
	return &rows[0], nil
}

// attachSANs fills in the names for a page of certificates in one query.
func (s *SQLStore) attachSANs(ctx context.Context, rows []CertificateRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, len(rows))
	index := make(map[string]int, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		index[r.ID] = i
	}
	for _, chunk := range chunkIDs(ids) {
		var found []struct {
			CertificateID string `db:"certificate_id"`
			Name          string `db:"name"`
		}
		if err := s.read(ctx, &found, `
			SELECT certificate_id, name FROM certificate_san
			WHERE certificate_id IN (`+placeholders(len(chunk))+`)
			ORDER BY certificate_id, name`, anySlice(chunk)...); err != nil {
			return fmt.Errorf("loading certificate names: %w", err)
		}
		for _, r := range found {
			if i, ok := index[r.CertificateID]; ok {
				rows[i].SANs = append(rows[i].SANs, r.Name)
			}
		}
	}
	// Sorted here rather than trusted from the ORDER BY, for the same collation
	// reason as the list itself: this decides display order.
	for i := range rows {
		sort.Strings(rows[i].SANs)
	}
	return nil
}

// certificateAudit is what the change_log records for a certificate.
//
// The SAN set is FOLDED IN, because it is replaced wholesale and would
// otherwise produce no diff on the certificate struct and therefore no audit
// entry at all -- the failure this codebase has already made three times with
// set tables. Adding a name to a certificate is a change to what it covers and
// has to be visible as one.
//
// key_ref is not here: it is redacted globally like secret_ref, and a
// certificate is exactly the entity where somebody might paste something worse
// than a path.
type certificateAudit struct {
	// EMBEDDED BY VALUE, like assetAudit and dependencyAudit. A pointer here
	// compiled, ran, and produced an audit entry containing ONLY the SANs --
	// snapshotJSON walks the struct's db-tagged fields and does not follow an
	// embedded pointer, so every column of the certificate was missing from the
	// trail. An entry that records nothing is worse than no entry, because it
	// looks like coverage. Found by mutating the key_ref redaction and noticing
	// the test did not care.
	domain.Certificate
	// Sorted and joined, so re-pasting the same names in a different order is
	// not reported as a change -- the reason dependencyAudit sorts its classes.
	SANs string `db:"sans"`
}

func auditedCertificate(c *domain.Certificate, sans []string) *certificateAudit {
	sorted := append([]string(nil), sans...)
	sort.Strings(sorted)
	return &certificateAudit{Certificate: *c, SANs: strings.Join(sorted, " ")}
}

// CreateCertificate inserts a certificate and its names.
func (s *SQLStore) CreateCertificate(ctx context.Context, actor domain.Actor, c *domain.Certificate) error {
	if err := c.Validate(); err != nil {
		return err
	}
	// c.SANs, never a separate parameter: Validate has just checked them, and a
	// second argument is a seam through which an unvalidated name reaches the
	// database. A security review demonstrated what gets in through that seam.
	names := c.SANs

	return s.write(ctx, actor, func(t *tx) error {
		// The same check assets and services make. Without it an unknown role
		// reaches the foreign key, and SQLite reports that as "FOREIGN KEY
		// constraint failed" with no column in it -- a bare 422 with no field
		// highlighted and the form contents lost. Certificates skipped it and a
		// review caught the omission.
		if err := requireRole(ctx, t, c.ManagerRole); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO certificate (id, subject_cn, issuer, fingerprint, serial,
			                         not_before, not_after, key_ref, team_id, manager_role,
			                         lifecycle, attrs, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.SubjectCN, c.Issuer, c.Fingerprint, c.Serial,
			c.NotBefore, c.NotAfter, c.KeyRef, c.TeamID, c.ManagerRole,
			c.Lifecycle, c.Attrs, c.CreatedAt, c.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating certificate")
		}
		if err := setCertificateSANs(ctx, t, c.ID, names); err != nil {
			return err
		}
		if err := t.logCreate(ctx, "certificate", c.ID, auditedCertificate(c, names)); err != nil {
			return err
		}
		return s.indexCertificate(ctx, t, c, names)
	})
}

// UpdateCertificate persists field and name changes.
func (s *SQLStore) UpdateCertificate(ctx context.Context, actor domain.Actor, c *domain.Certificate) error {
	if err := c.Validate(); err != nil {
		return err
	}
	before, err := s.GetCertificate(ctx, c.ID)
	if err != nil {
		return err
	}
	c.CreatedAt = before.CreatedAt
	c.UpdatedAt = domain.FormatTime(s.now())
	names := c.SANs

	return s.write(ctx, actor, func(t *tx) error {
		if err := requireRole(ctx, t, c.ManagerRole); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			UPDATE certificate SET subject_cn = ?, issuer = ?, fingerprint = ?, serial = ?,
			                       not_before = ?, not_after = ?, key_ref = ?, team_id = ?,
			                       manager_role = ?, lifecycle = ?, attrs = ?, updated_at = ?
			WHERE id = ?`,
			c.SubjectCN, c.Issuer, c.Fingerprint, c.Serial,
			c.NotBefore, c.NotAfter, c.KeyRef, c.TeamID, c.ManagerRole,
			c.Lifecycle, c.Attrs, c.UpdatedAt, c.ID)
		if err != nil {
			return translateWriteErr(err, "updating certificate")
		}
		if err := setCertificateSANs(ctx, t, c.ID, names); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "certificate", c.ID,
			auditedCertificate(&before.Certificate, before.SANs),
			auditedCertificate(c, names)); err != nil {
			return err
		}
		return s.indexCertificate(ctx, t, c, names)
	})
}

// RetireCertificate soft-deletes a certificate.
func (s *SQLStore) RetireCertificate(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetCertificate(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`UPDATE certificate SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring certificate")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`,
			before.Lifecycle, domain.LifecycleRetired)
		return t.log(ctx, "certificate", id, domain.ActionRetire, diff)
	})
}

// setCertificateSANs replaces the name set wholesale.
//
// Delete-then-insert inside the parent's transaction, which is what the house
// rules call correct for a set table: these rows hold the CURRENT value of
// something the certificate owns, and replacing them is not deletion. The
// parent's change_log entry records the change through certificateAudit.
func setCertificateSANs(ctx context.Context, t *tx, certificateID string, names []string) error {
	if _, err := t.exec(ctx,
		`DELETE FROM certificate_san WHERE certificate_id = ?`, certificateID); err != nil {
		return fmt.Errorf("clearing certificate names: %w", err)
	}
	for _, name := range names {
		if _, err := t.exec(ctx,
			`INSERT INTO certificate_san (certificate_id, name) VALUES (?, ?)`,
			certificateID, name); err != nil {
			return translateWriteErr(err, "recording a certificate name")
		}
	}
	return nil
}

// indexCertificate makes a certificate findable by every name it covers.
//
// The names go in the body, so pasting a hostname out of a browser warning
// lands on the certificate that covers it -- which is the search this feature
// exists to answer. The key reference is NOT indexed: it is a path to something
// secret, and a path is a lead.
func (s *SQLStore) indexCertificate(ctx context.Context, t *tx, c *domain.Certificate, names []string) error {
	body := strings.Join(names, " ")
	if c.Issuer != nil {
		body += " " + *c.Issuer
	}
	subtitle := "certificate"
	if c.NotAfter != nil {
		subtitle = "expires " + *c.NotAfter
	}
	return s.indexEntity(ctx, t, searchDoc{
		EntityType: "certificate", EntityID: c.ID,
		Title: c.SubjectCN, Subtitle: subtitle, Body: body,
	})
}

// ---------------------------------------------------------------------------
// Where it is deployed.

// CertificateDeployment is one place a certificate is used.
type CertificateDeployment struct {
	CertificateID string  `db:"certificate_id"`
	EntityID      string  `db:"entity_id"`
	EntityName    string  `db:"entity_name"`
	Note          *string `db:"note"`
	Lifecycle     string  `db:"lifecycle"`
}

// ListCertificateAssets returns the assets a certificate is deployed to.
func (s *SQLStore) ListCertificateAssets(ctx context.Context, certificateID string) ([]CertificateDeployment, error) {
	var rows []CertificateDeployment
	err := s.read(ctx, &rows, `
		SELECT ca.certificate_id, ca.asset_id AS entity_id, a.name AS entity_name,
		       ca.note, ca.lifecycle
		FROM certificate_asset ca
		JOIN asset a ON a.id = ca.asset_id
		WHERE ca.certificate_id = ? AND ca.lifecycle = ? AND a.lifecycle <> ?
		ORDER BY a.name`,
		certificateID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing certificate deployments: %w", err)
	}
	return rows, nil
}

// ListCertificateServices returns the services a certificate is deployed to.
func (s *SQLStore) ListCertificateServices(ctx context.Context, certificateID string) ([]CertificateDeployment, error) {
	var rows []CertificateDeployment
	err := s.read(ctx, &rows, `
		SELECT cs.certificate_id, cs.service_id AS entity_id, sv.code AS entity_name,
		       cs.note, cs.lifecycle
		FROM certificate_service cs
		JOIN service sv ON sv.id = cs.service_id
		WHERE cs.certificate_id = ? AND cs.lifecycle = ? AND sv.lifecycle <> ?
		ORDER BY sv.code`,
		certificateID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing certificate deployments: %w", err)
	}
	return rows, nil
}

// DeployedCertificate is one certificate as seen FROM the thing it sits on.
//
// The reverse of CertificateDeployment: that answers "where is this certificate",
// this answers "what is on this box". The second is the incident question.
type DeployedCertificate struct {
	ID        string  `db:"id"`
	SubjectCN string  `db:"subject_cn"`
	Issuer    *string `db:"issuer"`
	NotAfter  *string `db:"not_after"`
	Note      *string `db:"note"`
	TeamID    *string `db:"team_id"`
	TeamName  string  `db:"team_name"`
}

// CertificatesOnAsset lists the certificates deployed to an asset.
func (s *SQLStore) CertificatesOnAsset(ctx context.Context, assetID string) ([]DeployedCertificate, error) {
	return s.certificatesOn(ctx, "certificate_asset", "asset_id", assetID)
}

// CertificatesOnService lists the certificates deployed to a service.
func (s *SQLStore) CertificatesOnService(ctx context.Context, serviceID string) ([]DeployedCertificate, error) {
	return s.certificatesOn(ctx, "certificate_service", "service_id", serviceID)
}

// certificatesOn is shared by the two above. Table and column come from those
// two call sites and never from a request.
//
// Ordered by expiry with undated last, and sorted in Go rather than trusted from
// the ORDER BY, for the collation reason ListCertificates documents.
func (s *SQLStore) certificatesOn(ctx context.Context, table, column, entityID string) ([]DeployedCertificate, error) {
	var rows []DeployedCertificate
	err := s.read(ctx, &rows, `
		SELECT c.id, c.subject_cn, c.issuer, c.not_after, l.note, c.team_id,
		       COALESCE(tm.name, '') AS team_name
		FROM `+table+` l
		JOIN certificate c ON c.id = l.certificate_id
		LEFT JOIN team tm ON tm.id = c.team_id
		WHERE l.`+column+` = ? AND l.lifecycle = ? AND c.lifecycle <> ?`,
		entityID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing the certificates on %s: %w", entityID, err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		aNull, bNull := a.NotAfter == nil, b.NotAfter == nil
		if aNull != bNull {
			return !aNull
		}
		if !aNull && derefOr(a.NotAfter, "") != derefOr(b.NotAfter, "") {
			return derefOr(a.NotAfter, "") < derefOr(b.NotAfter, "")
		}
		if a.SubjectCN != b.SubjectCN {
			return a.SubjectCN < b.SubjectCN
		}
		return a.ID < b.ID
	})
	return rows, nil
}

// DeployCertificateToAsset records that a certificate is used on an asset.
func (s *SQLStore) DeployCertificateToAsset(ctx context.Context, actor domain.Actor,
	certificateID, assetID string, note *string) error {
	return s.deployCertificate(ctx, actor, "certificate_asset", "asset_id", certificateID, assetID, note)
}

// DeployCertificateToService records that a certificate is used on a service.
func (s *SQLStore) DeployCertificateToService(ctx context.Context, actor domain.Actor,
	certificateID, serviceID string, note *string) error {
	return s.deployCertificate(ctx, actor, "certificate_service", "service_id", certificateID, serviceID, note)
}

// deployCertificate is shared by the two above. The table and column names come
// from those two call sites and never from a request.
func (s *SQLStore) deployCertificate(ctx context.Context, actor domain.Actor,
	table, column, certificateID, entityID string, note *string) error {

	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		// What is there now, so a no-op can be recognised. An audit trail full
		// of entries saying nothing changed is worse than one without them --
		// the rule logUpdate enforces everywhere else, which a hand-built diff
		// string quietly opts out of. Both reviews caught this.
		var before struct {
			Lifecycle string  `db:"lifecycle"`
			Note      *string `db:"note"`
		}
		err := t.get(ctx, &before,
			`SELECT lifecycle, note FROM `+table+`
			 WHERE certificate_id = ? AND `+column+` = ?`, certificateID, entityID)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reading the existing deployment: %w", err)
		}

		if exists && before.Lifecycle == domain.LifecycleActive && sameNote(before.Note, note) {
			// Already deployed here, with the same note. Nothing happened.
			return nil
		}

		if exists {
			// Re-deploying somewhere it was retired from reactivates the row
			// rather than failing on the primary key: the operator's intent is
			// "it is here now", and a second row cannot exist to say so.
			if _, err := t.exec(ctx,
				`UPDATE `+table+` SET lifecycle = ?, note = ?, updated_at = ?
				 WHERE certificate_id = ? AND `+column+` = ?`,
				domain.LifecycleActive, note, at, certificateID, entityID); err != nil {
				return translateWriteErr(err, "recording a certificate deployment")
			}
		} else if _, err := t.exec(ctx,
			`INSERT INTO `+table+` (certificate_id, `+column+`, note, lifecycle, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			certificateID, entityID, note, domain.LifecycleActive, at, at); err != nil {
			return translateWriteErr(err, "recording a certificate deployment")
		}
		return t.log(ctx, "certificate", certificateID, domain.ActionUpdate,
			fmt.Sprintf(`{"deployment":{"new":%q,"note":%q}}`, entityID, derefOr(note, "")))
	})
}

// UndeployCertificateFromAsset retires a deployment, never deleting it.
func (s *SQLStore) UndeployCertificateFromAsset(ctx context.Context, actor domain.Actor,
	certificateID, assetID string) error {
	return s.undeployCertificate(ctx, actor, "certificate_asset", "asset_id", certificateID, assetID)
}

// UndeployCertificateFromService retires a deployment.
func (s *SQLStore) UndeployCertificateFromService(ctx context.Context, actor domain.Actor,
	certificateID, serviceID string) error {
	return s.undeployCertificate(ctx, actor, "certificate_service", "service_id", certificateID, serviceID)
}

func (s *SQLStore) undeployCertificate(ctx context.Context, actor domain.Actor,
	table, column, certificateID, entityID string) error {

	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx,
			`UPDATE `+table+` SET lifecycle = ?, updated_at = ?
			 WHERE certificate_id = ? AND `+column+` = ? AND lifecycle = ?`,
			domain.LifecycleRetired, at, certificateID, entityID, domain.LifecycleActive)
		if err != nil {
			return translateWriteErr(err, "retiring a certificate deployment")
		}
		// Zero rows means there was nothing here to retire. The previous version
		// logged regardless, so POSTing the retire route with two ids that had
		// never been deployed together FABRICATED a removal in the permanent
		// trail and reported success. Found by a security review.
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("checking the retirement: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("no active deployment of %s on %s: %w",
				certificateID, entityID, domain.ErrNotFound)
		}
		return t.log(ctx, "certificate", certificateID, domain.ActionUpdate,
			fmt.Sprintf(`{"deployment":{"old":%q}}`, entityID))
	})
}

// sameNote compares two optional notes, treating nil and empty as one value.
func sameNote(a, b *string) bool {
	return derefOr(a, "") == derefOr(b, "")
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
