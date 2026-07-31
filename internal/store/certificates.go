package store

import (
	"context"
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
	// SANs are every name it covers, subject included, in stored order.
	SANs []string
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
func (s *SQLStore) CreateCertificate(ctx context.Context, actor domain.Actor, c *domain.Certificate, sans []string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	names := domain.NormaliseSANs(c.SubjectCN, sans)

	return s.write(ctx, actor, func(t *tx) error {
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
func (s *SQLStore) UpdateCertificate(ctx context.Context, actor domain.Actor, c *domain.Certificate, sans []string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	before, err := s.GetCertificate(ctx, c.ID)
	if err != nil {
		return err
	}
	c.CreatedAt = before.CreatedAt
	c.UpdatedAt = domain.FormatTime(s.now())
	names := domain.NormaliseSANs(c.SubjectCN, sans)

	return s.write(ctx, actor, func(t *tx) error {
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
		// Re-deploying somewhere it was retired from reactivates the row rather
		// than failing on the primary key: the operator's intent is "it is here
		// now", and a second row cannot exist to say so.
		res, err := t.exec(ctx,
			`UPDATE `+table+` SET lifecycle = ?, note = ?, updated_at = ?
			 WHERE certificate_id = ? AND `+column+` = ?`,
			domain.LifecycleActive, note, at, certificateID, entityID)
		if err != nil {
			return translateWriteErr(err, "recording a certificate deployment")
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			return t.log(ctx, "certificate", certificateID, domain.ActionUpdate,
				fmt.Sprintf(`{"deployment":{"new":%q}}`, entityID))
		}

		if _, err := t.exec(ctx,
			`INSERT INTO `+table+` (certificate_id, `+column+`, note, lifecycle, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			certificateID, entityID, note, domain.LifecycleActive, at, at); err != nil {
			return translateWriteErr(err, "recording a certificate deployment")
		}
		return t.log(ctx, "certificate", certificateID, domain.ActionUpdate,
			fmt.Sprintf(`{"deployment":{"new":%q}}`, entityID))
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
		if _, err := t.exec(ctx,
			`UPDATE `+table+` SET lifecycle = ?, updated_at = ?
			 WHERE certificate_id = ? AND `+column+` = ?`,
			domain.LifecycleRetired, at, certificateID, entityID); err != nil {
			return translateWriteErr(err, "retiring a certificate deployment")
		}
		return t.log(ctx, "certificate", certificateID, domain.ActionUpdate,
			fmt.Sprintf(`{"deployment":{"old":%q}}`, entityID))
	})
}
