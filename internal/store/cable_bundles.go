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
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Cable bundles (migration 00068, docs/cable-bundles-design.md): cables
// somebody pulled together and will replace together -- a duct, a tray, a
// trunk. A bundle groups link rows; it is a fact somebody declares, not
// anything derived from the links themselves.

// bundleMemberQuery resolves a bundle's cable_bundle_member rows to both
// cable ends for display. Deliberately NOT filtered on l.lifecycle -- a
// retired link stays in its bundle (design doc, "Rules": membership records
// what was pulled together, and a withdrawn cable is still part of that
// history), so this is the one query both ListBundleMembers and the audit
// fold share, and neither excludes it. A caller that wants only live cables
// (the impact view) filters BundleMemberRow.Lifecycle itself.
const bundleMemberQuery = `
	SELECT l.id AS link_id, l.lifecycle,
	       ia.name AS a_iface, aa.name AS a_asset_name,
	       ib.name AS b_iface, ab.name AS b_asset_name
	FROM cable_bundle_member m
	JOIN link l ON l.id = m.link_id
	JOIN interface ia ON ia.id = l.a_interface_id
	JOIN interface ib ON ib.id = l.b_interface_id
	JOIN asset aa ON aa.id = ia.asset_id
	JOIN asset ab ON ab.id = ib.asset_id
	WHERE m.bundle_id = ?
	ORDER BY aa.name, ia.name`

// BundleRow is a bundle with its membership count resolved.
type BundleRow struct {
	domain.CableBundle
	Members int `db:"members"`
}

// BundleMemberRow is one cable in a bundle, with both ends resolved -- a
// cable has no name of its own (see LinkEnds), so anything rendering one has
// to resolve both ends. Lifecycle is the LINK's, not the bundle's, so a
// caller can tell a retired member from a live one without a second query.
type BundleMemberRow struct {
	LinkID     string `db:"link_id"`
	Lifecycle  string `db:"lifecycle"`
	AIface     string `db:"a_iface"`
	AAssetName string `db:"a_asset_name"`
	BIface     string `db:"b_iface"`
	BAssetName string `db:"b_asset_name"`
}

// Label is how a bundle member is named in a sentence, the same shape as
// LinkEnds.Label.
func (r BundleMemberRow) Label() string {
	return r.AAssetName + " " + r.AIface + " – " + r.BAssetName + " " + r.BIface
}

// ListBundles returns every live bundle.
func (s *SQLStore) ListBundles(ctx context.Context) ([]BundleRow, error) {
	var rows []BundleRow
	err := s.read(ctx, &rows, `
		SELECT b.*,
		       (SELECT COUNT(*) FROM cable_bundle_member m WHERE m.bundle_id = b.id) AS members
		FROM cable_bundle b
		WHERE b.lifecycle <> 'retired'
		ORDER BY b.name`)
	if err != nil {
		return nil, fmt.Errorf("listing bundles: %w", err)
	}
	return rows, nil
}

// GetBundle loads one bundle, retired or not -- callers that need only live
// ones (ListBundles) filter for themselves, the same split GetCluster uses.
func (s *SQLStore) GetBundle(ctx context.Context, id string) (*domain.CableBundle, error) {
	var b domain.CableBundle
	if err := s.readOne(ctx, &b, `SELECT * FROM cable_bundle WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting bundle %s: %w", id, err)
	}
	return &b, nil
}

// BundleForLink finds the bundle a cable is in, if any. domain.ErrNotFound
// when it is in none -- the impact page (Task 3) treats that as "not
// bundled", not as an error.
func (s *SQLStore) BundleForLink(ctx context.Context, linkID string) (*domain.CableBundle, error) {
	var b domain.CableBundle
	err := s.readOne(ctx, &b, `
		SELECT b.* FROM cable_bundle b
		JOIN cable_bundle_member m ON m.bundle_id = b.id
		WHERE m.link_id = ?`, linkID)
	if err != nil {
		return nil, fmt.Errorf("finding bundle for link %s: %w", linkID, err)
	}
	return &b, nil
}

// ListBundleMembers returns every cable in a bundle, retired links included
// (see bundleMemberQuery).
func (s *SQLStore) ListBundleMembers(ctx context.Context, bundleID string) ([]BundleMemberRow, error) {
	var rows []BundleMemberRow
	if err := s.read(ctx, &rows, bundleMemberQuery, bundleID); err != nil {
		return nil, fmt.Errorf("listing members of bundle %s: %w", bundleID, err)
	}
	return rows, nil
}

// CandidateLinksForBundle lists every live cable this bundle's membership
// editor may offer: every live link that is not currently claimed by another
// LIVE bundle (Task 2b's one-bundle-per-cable rule), plus this bundle's own
// current live members so an unrelated save does not make them vanish from
// the picker they are already ticked in.
//
// THE PICKER IS KEPT HONEST, NOT THE ENFORCEMENT. Excluding an already-claimed
// cable here is a courtesy so the operator is not offered a choice
// SetBundleMembers will refuse anyway -- the refusal inside that same
// transaction (see its own comment) is what actually guarantees the rule; a
// TOCTOU window between this read and that write is closed by SetBundleMembers
// re-checking, not by this list being accurate a moment ago.
func (s *SQLStore) CandidateLinksForBundle(ctx context.Context, bundleID string) ([]BundleMemberRow, error) {
	var rows []BundleMemberRow
	err := s.read(ctx, &rows, `
		SELECT l.id AS link_id, l.lifecycle,
		       ia.name AS a_iface, aa.name AS a_asset_name,
		       ib.name AS b_iface, ab.name AS b_asset_name
		FROM link l
		JOIN interface ia ON ia.id = l.a_interface_id
		JOIN interface ib ON ib.id = l.b_interface_id
		JOIN asset aa ON aa.id = ia.asset_id
		JOIN asset ab ON ab.id = ib.asset_id
		WHERE l.lifecycle = 'active'
		  AND NOT EXISTS (
		    SELECT 1 FROM cable_bundle_member m
		    JOIN cable_bundle b ON b.id = m.bundle_id
		    WHERE m.link_id = l.id AND b.lifecycle <> 'retired' AND m.bundle_id <> ?
		  )
		ORDER BY aa.name, ia.name`, bundleID)
	if err != nil {
		return nil, fmt.Errorf("listing bundle %s candidates: %w", bundleID, err)
	}
	return rows, nil
}

// LinksByID resolves a set of cables to their display rows regardless of
// lifecycle or which bundle, if any, claims them.
//
// EXISTS FOR ONE READER: a refused membership save needs to redraw the picker
// with what the operator actually picked still visible and selected, even
// when that pick is a cable CandidateLinksForBundle would never offer --
// exactly the case that was just refused for being claimed elsewhere. Making
// the choice disappear on refusal would violate the house rule that a refused
// form comes back with the typed input intact, so the handler adds these rows
// into the option list it renders rather than trusting the ordinary candidate
// query to already contain them.
func (s *SQLStore) LinksByID(ctx context.Context, ids []string) ([]BundleMemberRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []BundleMemberRow
	q := `
		SELECT l.id AS link_id, l.lifecycle,
		       ia.name AS a_iface, aa.name AS a_asset_name,
		       ib.name AS b_iface, ab.name AS b_asset_name
		FROM link l
		JOIN interface ia ON ia.id = l.a_interface_id
		JOIN interface ib ON ib.id = l.b_interface_id
		JOIN asset aa ON aa.id = ia.asset_id
		JOIN asset ab ON ab.id = ib.asset_id
		WHERE l.id IN (` + placeholders(len(ids)) + `)
		ORDER BY aa.name, ia.name`
	if err := s.read(ctx, &rows, q, anySlice(ids)...); err != nil {
		return nil, fmt.Errorf("resolving cables by id: %w", err)
	}
	return rows, nil
}

// CreateBundle declares a bundle. Membership is set separately via
// SetBundleMembers -- a bundle can exist with no cables in it yet, which is
// also why it stays ScopeTopology/Administrator-only (docs/AUDIT.md): "every
// member in scope" would be vacuously true for one with none.
func (s *SQLStore) CreateBundle(ctx context.Context, p domain.Permit, b *domain.CableBundle) error {
	// The row the INSERT is about to write is version 1 regardless of what
	// the caller's struct carries -- CreateDependency's guard, for the same
	// reason: a caller that creates and then updates the same struct must not
	// compare a stale token against itself.
	b.RowVersion = 1
	if err := b.Validate(); err != nil {
		return err
	}
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO cable_bundle (id, code, name, description, lifecycle,
			                          created_at, updated_at, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.Code, b.Name, b.Description, b.Lifecycle,
			b.CreatedAt, b.UpdatedAt, b.RowVersion)
		if err != nil {
			return translateWriteErr(err, "creating bundle")
		}
		if err := t.logCreate(ctx, "cable_bundle", b.ID, b); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "cable_bundle", EntityID: b.ID,
			Title: b.Name, Subtitle: b.Code, Body: b.Code + " " + b.Name,
		})
	})
}

// UpdateBundle corrects a bundle's descriptive attributes: code, name,
// description. NOT lifecycle.
//
// Lifecycle is CARRIED FROM THE STORED ROW, never taken from the caller, and
// the UPDATE statement below does not name the column at all. This exact
// method shape was, on the branch this design follows, a second withdrawal
// path AND a revival path at once: a submitted "active" silently brought back
// a withdrawn row through the ordinary correction form. Pinning b.Lifecycle
// here before Validate and before logUpdate closes both directions --
// the SQL can't write a lifecycle change because it never sends the column,
// and the audit entry can't claim one because the struct being diffed
// against "before" already agrees with it.
func (s *SQLStore) UpdateBundle(ctx context.Context, p domain.Permit, b *domain.CableBundle) error {
	before, err := s.GetBundle(ctx, b.ID)
	if err != nil {
		return err
	}
	b.Lifecycle = before.Lifecycle
	if err := b.Validate(); err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	b.UpdatedAt = at
	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE cable_bundle SET code = ?, name = ?, description = ?,
			                        updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			b.Code, b.Name, b.Description, at, b.ID, b.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating bundle")
		}
		if err := requireVersion(res, "cable_bundle", b.ID, &b.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "cable_bundle", b.ID, before, b); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "cable_bundle", EntityID: b.ID,
			Title: b.Name, Subtitle: b.Code, Body: b.Code + " " + b.Name,
		})
	})
}

// RetireBundle withdraws a bundle. NO CASCADE: "we stopped managing these
// cables together" is not "somebody pulled the cables", so this never
// touches cable_bundle_member and never refuses on a non-empty bundle --
// cascading would write change_log entries attributing cable removals to
// whoever withdrew the grouping, the misattribution RetireInterface and
// RetireEnvironment both refuse (design doc, "Rules").
func (s *SQLStore) RetireBundle(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetBundle(ctx, id)
	if err != nil {
		return err
	}
	if before.IsRetired() {
		return nil
	}
	at := domain.FormatTime(s.now())
	after := *before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = at

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE cable_bundle SET lifecycle = 'retired', updated_at = ?,
			                        row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring bundle")
		}
		if err := requireVersion(res, "cable_bundle", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "cable_bundle", id, before, &after)
	})
}

// SetBundleMembers replaces a bundle's whole membership.
//
// The SEVENTH set table in this schema (cluster_member, dependency's data
// classes, asset's environments/custom fields/tags -- see auditedCluster and
// assetAudit), and the rule that has bitten this repo three times, twice on
// rows deciding audit scope, has not changed: replaced wholesale inside the
// bundle's transaction, and folded into the bundle's audited value so
// logUpdate's struct diff actually sees the change. Copying the pattern is
// not trusted on its own -- TestSetBundleMembersProducesAnAuditDiff mutates
// the fold out and watches the test go red.
func (s *SQLStore) SetBundleMembers(ctx context.Context, p domain.Permit,
	bundleID string, linkIDs []string) error {

	bundle, err := s.GetBundle(ctx, bundleID)
	if err != nil {
		return err
	}
	// A withdrawn bundle's membership is history, not something to keep
	// editing (Task 2b) -- editing a grouping that has already been
	// withdrawn is meaningless. Refused here, before anything opens a
	// transaction, the same shape requireLiveNetGroup uses.
	if bundle.IsRetired() {
		ve := &domain.ValidationError{}
		ve.Add("bundle_id", "that bundle has been withdrawn")
		return ve
	}
	beforeLabels, err := s.bundleMemberLabels(ctx, bundleID)
	if err != nil {
		return err
	}
	before := auditedBundle(bundle, beforeLabels)
	at := domain.FormatTime(s.now())

	// A set: duplicate ids in the caller's slice collapse to one membership
	// row instead of tripping the (bundle_id, link_id) PRIMARY KEY on the
	// second INSERT.
	seen := make(map[string]bool, len(linkIDs))
	unique := make([]string, 0, len(linkIDs))
	for _, id := range linkIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}

	return s.write(ctx, p, func(t *tx) error {
		// ONE BUNDLE PER CABLE, SCOPED TO LIVE BUNDLES (Task 2b) -- no longer
		// a database constraint (see migration 00068's header), because the
		// rule needs the parent's lifecycle and an index can't see across
		// tables. Checked here instead, in the same transaction: a cable in
		// a RETIRED bundle is free to join a new one (that's the whole point
		// -- a duct gets replaced and its cables re-pulled), a cable in a
		// LIVE bundle is still refused with a field message naming which one.
		if len(unique) > 0 {
			var conflicts []struct {
				LinkID     string `db:"link_id"`
				BundleCode string `db:"bundle_code"`
			}
			q := `SELECT m.link_id, b.code AS bundle_code
			      FROM cable_bundle_member m
			      JOIN cable_bundle b ON b.id = m.bundle_id
			      WHERE m.bundle_id <> ? AND b.lifecycle <> 'retired'
			        AND m.link_id IN (` + placeholders(len(unique)) + `)`
			args := append([]any{bundleID}, anySlice(unique)...)
			if err := t.selectAll(ctx, &conflicts, q, args...); err != nil {
				return fmt.Errorf("checking bundle membership conflicts: %w", err)
			}
			if len(conflicts) > 0 {
				c := conflicts[0]
				return fmt.Errorf("cable %s is already in bundle %s: %w",
					c.LinkID, c.BundleCode, domain.ErrConflict)
			}
		}

		if _, err := t.exec(ctx,
			`DELETE FROM cable_bundle_member WHERE bundle_id = ?`, bundleID); err != nil {
			return translateWriteErr(err, "clearing bundle membership")
		}
		for _, id := range unique {
			if _, err := t.exec(ctx,
				`INSERT INTO cable_bundle_member (bundle_id, link_id) VALUES (?, ?)`,
				bundleID, id); err != nil {
				return translateWriteErr(err, "setting bundle membership")
			}
		}
		if _, err := t.exec(ctx, `
			UPDATE cable_bundle SET updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, at, bundleID); err != nil {
			return translateWriteErr(err, "touching bundle")
		}

		afterLabels, err := bundleMemberLabelsTx(ctx, t, bundleID)
		if err != nil {
			return err
		}
		updated := *bundle
		updated.UpdatedAt = at
		updated.RowVersion = bundle.RowVersion + 1
		return t.logUpdate(ctx, "cable_bundle", bundleID, before, auditedBundle(&updated, afterLabels))
	})
}

// bundleAudit is the audited shape of a bundle: the row plus which cables are
// in it. The `db` tag is what makes the fold work -- diffJSON compares
// db-tagged fields and ignores everything else, the same shape as
// clusterAudit and dependencyAudit.
type bundleAudit struct {
	domain.CableBundle
	Members string `db:"members"`
}

func auditedBundle(b *domain.CableBundle, labels []string) *bundleAudit {
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	return &bundleAudit{CableBundle: *b, Members: strings.Join(sorted, ",")}
}

// bundleMemberLabels resolves a bundle's current membership to sentence-form
// cable labels for the audit fold, via the reader pool -- called BEFORE the
// write transaction opens, so there is nothing yet for it to race with.
func (s *SQLStore) bundleMemberLabels(ctx context.Context, bundleID string) ([]string, error) {
	var rows []BundleMemberRow
	if err := s.read(ctx, &rows, bundleMemberQuery, bundleID); err != nil {
		return nil, fmt.Errorf("listing bundle %s members: %w", bundleID, err)
	}
	return labelsOf(rows), nil
}

// bundleMemberLabelsTx is bundleMemberLabels read through the write
// transaction instead of the reader pool -- required for the "after" half of
// SetBundleMembers's fold, since the membership rows just written are not
// committed yet and the separate reader pool cannot see them.
func bundleMemberLabelsTx(ctx context.Context, t *tx, bundleID string) ([]string, error) {
	var rows []BundleMemberRow
	if err := t.selectAll(ctx, &rows, bundleMemberQuery, bundleID); err != nil {
		return nil, fmt.Errorf("listing bundle %s members: %w", bundleID, err)
	}
	return labelsOf(rows), nil
}

func labelsOf(rows []BundleMemberRow) []string {
	labels := make([]string, 0, len(rows))
	for _, r := range rows {
		labels = append(labels, r.Label())
	}
	return labels
}
