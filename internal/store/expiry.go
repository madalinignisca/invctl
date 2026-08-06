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
	"fmt"
	"sort"
	"time"

	"github.com/gabriel/invctl/internal/domain"
)

// What runs out, when, and who it lands on.
//
// This is the first report in the codebase whose subject is TIME rather than
// topology, and it is deliberately the whole of what an EOL date does. Nothing
// else in invctl reads `eol_date`: a date passing changes what this page says
// and changes nothing about the estate -- no lifecycle moves, no placement is
// removed, nobody is paged. The system presents state.
//
// The clock is passed in, never read here. A report whose contents differ
// between two calls in the same request is not a report, and a query that says
// "today" cannot be tested.

// ExpiryHorizonMonths is the default window: a year, which is the period a
// budget covers and roughly the notice on which hardware can be specified,
// ordered and racked.
const ExpiryHorizonMonths = 12

// ExpiringRow is one thing with a date, and enough context to decide whether
// it matters. Assets and services share the shape because the reader's question
// is the same for both.
type ExpiringRow struct {
	EntityType string `db:"entity_type"`
	ID         string `db:"id"`
	Name       string `db:"name"`
	Code       string `db:"code"`
	Kind       string `db:"kind"`
	Lifecycle  string `db:"lifecycle"`
	EOLDate    string `db:"eol_date"`

	// ServiceCount and BestTier answer "so what" for an asset: how much runs on
	// it or inside it, and the most important thing among them. A switch with
	// nothing behind it and a hypervisor carrying a tier-1 database expire the
	// same way and are not the same problem.
	//
	// For a service row these stay zero -- the service IS the workload, and its
	// own tier is in Tier below.
	ServiceCount int `db:"service_count"`
	BestTier     int `db:"best_tier"`
	Tier         int `db:"tier"`

	// The project that owns it, when one does. Resolved in Go from one lookup
	// per entity type rather than joined per row.
	ProjectID   string
	ProjectCode string
	ProjectName string

	// Derived from EOLDate and the clock handed to Expiring.
	State     string
	DaysUntil int
}

// IsExpired is a convenience for templates, which must not do arithmetic.
func (r ExpiringRow) IsExpired() bool { return r.State == domain.ExpiryExpired }

// ExpiryReport is everything the page shows, already ordered.
type ExpiryReport struct {
	// Rows: expired first, then soonest. Both halves matter and separating them
	// into two tables would hide that an item three days from expiry is more
	// urgent than one that lapsed last year and evidently nobody minded.
	Rows []ExpiringRow

	// Counts, so a summary does not have to walk the rows in a template.
	Expired int
	Soon    int
	Later   int

	// Undated is how many live assets, services and certificates carry no date. It is
	// the number that makes the rest honest: a report over four dated rows in an
	// estate of four hundred reads as "almost nothing expires", and the true
	// answer is "almost nothing is recorded".
	UndatedAssets       int
	UndatedServices     int
	UndatedCertificates int

	// Horizon is the requested window, echoed back so the page can say what it
	// covered rather than implying it covered everything.
	HorizonMonths int
	Until         string
}

// Expiring returns everything with a date on or before the horizon, plus
// everything already past, regardless of how far past.
//
// Already-expired rows are ALWAYS included whatever the window. A window is a
// question about the future; something that already lapsed is not a forecast,
// it is a fact, and dropping it because it fell outside a twelve-month view is
// how an estate accumulates unsupported hardware nobody is looking at.
func (s *SQLStore) Expiring(ctx context.Context, asOf time.Time, horizonMonths int) (*ExpiryReport, error) {
	if horizonMonths <= 0 {
		horizonMonths = ExpiryHorizonMonths
	}
	until := domain.FormatDate(asOf.AddDate(0, horizonMonths, 0))
	report := &ExpiryReport{HorizonMonths: horizonMonths, Until: until}

	// Two queries rather than a UNION: the two halves select different columns
	// (an asset has no tier, a service has no containment) and a UNION would
	// have to pad both sides with literals to line them up. Merging two typed
	// results in Go is the same shape the neighbourhood loaders use.
	var assets []ExpiringRow
	if err := s.read(ctx, &assets, `
		SELECT 'asset' AS entity_type, a.id, a.name, '' AS code, a.kind, a.lifecycle, a.eol_date,
		       0 AS service_count, 0 AS best_tier, 0 AS tier
		FROM asset a
		WHERE a.eol_date IS NOT NULL AND a.eol_date <= ? AND a.lifecycle <> ?
		ORDER BY a.eol_date, a.name`, until, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing expiring assets: %w", err)
	}

	// Certificates. The reason this report is worth opening: hardware support
	// lapses on a five-year cycle and a certificate every ninety days, so this
	// is the half that produces an actual outage.
	var certificates []ExpiringRow
	if err := s.read(ctx, &certificates, `
		SELECT 'certificate' AS entity_type, c.id, c.subject_cn AS name,
		       COALESCE(c.fingerprint, '') AS code,
		       COALESCE(c.issuer, 'certificate') AS kind, c.lifecycle,
		       c.not_after AS eol_date,
		       0 AS service_count, 0 AS best_tier, 0 AS tier
		FROM certificate c
		WHERE c.not_after IS NOT NULL AND c.not_after <= ? AND c.lifecycle <> ?
		ORDER BY c.not_after, c.subject_cn`, until, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing expiring certificates: %w", err)
	}

	var services []ExpiringRow
	if err := s.read(ctx, &services, `
		SELECT 'service' AS entity_type, s.id, s.name, s.code, s.kind, s.lifecycle, s.eol_date,
		       0 AS service_count, 0 AS best_tier, s.tier
		FROM service s
		WHERE s.eol_date IS NOT NULL AND s.eol_date <= ? AND s.lifecycle <> ?
		ORDER BY s.eol_date, s.code`, until, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing expiring services: %w", err)
	}

	if err := s.expiryWorkload(ctx, assets); err != nil {
		return nil, err
	}
	// What rides on a certificate is everywhere it is deployed: an expiring
	// wildcard on four load balancers is four outages, and the bare count is
	// what makes that legible next to a switch carrying six services.
	if err := s.expiryCertificateReach(ctx, certificates); err != nil {
		return nil, err
	}
	if err := s.expiryOwners(ctx, assets, services); err != nil {
		return nil, err
	}
	if err := s.expiryUndated(ctx, report); err != nil {
		return nil, err
	}

	report.Rows = append(append(append([]ExpiringRow{}, assets...), services...), certificates...)
	for i := range report.Rows {
		row := &report.Rows[i]
		row.State = domain.ExpiryState(&row.EOLDate, asOf, domain.ExpirySoonDays*24*time.Hour)
		row.DaysUntil, _ = domain.DaysUntil(&row.EOLDate, asOf)
		switch row.State {
		case domain.ExpiryExpired:
			report.Expired++
		case domain.ExpirySoon:
			report.Soon++
		default:
			report.Later++
		}
	}

	// Sorted in Go, after the merge and after the two ORDER BYs: which of two
	// rows sharing a date comes first must not depend on the server's collation.
	// The same reason sortAssetsForBudget exists.
	sort.SliceStable(report.Rows, func(i, j int) bool {
		a, b := report.Rows[i], report.Rows[j]
		if a.EOLDate != b.EOLDate {
			return a.EOLDate < b.EOLDate
		}
		if a.EntityType != b.EntityType {
			return a.EntityType < b.EntityType
		}
		return a.ID < b.ID
	})
	return report, nil
}

// expiryWorkload fills in how much rides on each expiring asset.
//
// Through asset_closure with depth >= 0, so a hypervisor counts what runs on
// its guests and a rack counts what runs in the whole rack -- the same reason
// the house rule says containment questions go through the closure table rather
// than a parent_id walk. depth 0 is included because a service placed directly
// on the box is as much its workload as one inside a guest.
func (s *SQLStore) expiryWorkload(ctx context.Context, assets []ExpiringRow) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]string, len(assets))
	for i, a := range assets {
		ids[i] = a.ID
	}

	type workload struct {
		AssetID  string `db:"asset_id"`
		Services int    `db:"service_count"`
		BestTier int    `db:"best_tier"`
	}
	var rows []workload
	args := append(anySlice(ids), domain.LifecycleRetired, domain.LifecycleRetired)
	if err := s.read(ctx, &rows, `
		SELECT c.ancestor_id AS asset_id,
		       COUNT(DISTINCT s.id) AS service_count,
		       MIN(s.tier) AS best_tier
		FROM asset_closure c
		JOIN service_instance si ON si.host_asset_id = c.descendant_id
		JOIN service s           ON s.id = si.service_id
		WHERE c.ancestor_id IN (`+placeholders(len(ids))+`)
		  AND si.lifecycle <> ? AND s.lifecycle <> ?
		GROUP BY c.ancestor_id
		ORDER BY c.ancestor_id`, args...); err != nil {
		return fmt.Errorf("counting what rides on expiring assets: %w", err)
	}

	byAsset := make(map[string]workload, len(rows))
	for _, r := range rows {
		byAsset[r.AssetID] = r
	}
	for i := range assets {
		if w, ok := byAsset[assets[i].ID]; ok {
			assets[i].ServiceCount, assets[i].BestTier = w.Services, w.BestTier
		}
	}
	return nil
}

// expiryCertificateReach counts the places each expiring certificate is
// deployed. Reusing ServiceCount rather than adding a field: the column is
// "what rides on it", and for a certificate that is its deployments.
func (s *SQLStore) expiryCertificateReach(ctx context.Context, certificates []ExpiringRow) error {
	if len(certificates) == 0 {
		return nil
	}
	ids := make([]string, len(certificates))
	index := make(map[string]int, len(certificates))
	for i, c := range certificates {
		ids[i] = c.ID
		index[c.ID] = i
	}
	for _, chunk := range chunkIDs(ids) {
		var rows []struct {
			CertificateID string `db:"certificate_id"`
			Places        int    `db:"places"`
		}
		// Two IN lists and two lifecycle values, in the order the placeholders
		// appear. Built explicitly rather than sliced out of one slice: the
		// clever version was unreadable and one edit away from silently
		// swapping an id for a lifecycle.
		args := make([]any, 0, 2*len(chunk)+2)
		args = append(args, anySlice(chunk)...)
		args = append(args, domain.LifecycleActive)
		args = append(args, anySlice(chunk)...)
		args = append(args, domain.LifecycleActive)

		if err := s.read(ctx, &rows, `
			SELECT id AS certificate_id, SUM(places) AS places FROM (
			  SELECT ca.certificate_id AS id, COUNT(*) AS places
			  FROM certificate_asset ca
			  WHERE ca.certificate_id IN (`+placeholders(len(chunk))+`) AND ca.lifecycle = ?
			  GROUP BY ca.certificate_id
			  UNION ALL
			  SELECT cs.certificate_id AS id, COUNT(*) AS places
			  FROM certificate_service cs
			  WHERE cs.certificate_id IN (`+placeholders(len(chunk))+`) AND cs.lifecycle = ?
			  GROUP BY cs.certificate_id
			) deployments
			GROUP BY id`, args...); err != nil {
			return fmt.Errorf("counting certificate deployments: %w", err)
		}
		for _, r := range rows {
			if i, ok := index[r.CertificateID]; ok {
				certificates[i].ServiceCount = r.Places
			}
		}
	}
	return nil
}

// expiryOwners resolves the project that owns each row, if any.
func (s *SQLStore) expiryOwners(ctx context.Context, assets, services []ExpiringRow) error {
	assetIDs := make([]string, len(assets))
	for i, a := range assets {
		assetIDs[i] = a.ID
	}
	serviceIDs := make([]string, len(services))
	for i, svc := range services {
		serviceIDs[i] = svc.ID
	}

	assetOwners, err := s.ProjectsForAssets(ctx, assetIDs)
	if err != nil {
		return err
	}
	serviceOwners, err := s.ProjectsForServices(ctx, serviceIDs)
	if err != nil {
		return err
	}

	apply := func(rows []ExpiringRow, owners map[string][]ProjectLinkRow) {
		for i := range rows {
			for _, link := range owners[rows[i].ID] {
				// Only the owner. A project that merely uses a thing is not who
				// has to replace it, and listing it here would send the renewal
				// conversation to the wrong team.
				if link.Relation != domain.ProjectOwns {
					continue
				}
				rows[i].ProjectID, rows[i].ProjectCode, rows[i].ProjectName =
					link.ProjectID, link.ProjectCode, link.ProjectName
				break
			}
		}
	}
	apply(assets, assetOwners)
	apply(services, serviceOwners)
	return nil
}

// expiryUndated counts what carries no date at all.
func (s *SQLStore) expiryUndated(ctx context.Context, report *ExpiryReport) error {
	if err := s.readOne(ctx, &report.UndatedAssets,
		`SELECT COUNT(*) FROM asset WHERE eol_date IS NULL AND lifecycle <> ?`,
		domain.LifecycleRetired); err != nil {
		return fmt.Errorf("counting undated assets: %w", err)
	}
	if err := s.readOne(ctx, &report.UndatedServices,
		`SELECT COUNT(*) FROM service WHERE eol_date IS NULL AND lifecycle <> ?`,
		domain.LifecycleRetired); err != nil {
		return fmt.Errorf("counting undated services: %w", err)
	}
	// A certificate with no recorded expiry is the worst of the three: every
	// certificate has one, so a blank means nobody looked rather than that the
	// question does not apply.
	if err := s.readOne(ctx, &report.UndatedCertificates,
		`SELECT COUNT(*) FROM certificate WHERE not_after IS NULL AND lifecycle <> ?`,
		domain.LifecycleRetired); err != nil {
		return fmt.Errorf("counting undated certificates: %w", err)
	}
	return nil
}
