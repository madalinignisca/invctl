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

	"github.com/madalinignisca/invctl/internal/domain"
)

// Credential references: what a service authenticates as, who looks after it,
// and when somebody last rotated it.
//
// secret_ref holds a PATH, never the material. The audit trail is already
// covered and must not be "improved": domain.RedactedFields holds secret_ref
// globally and both snapshotJSON and diffJSON honour it, so change_log records
// THAT it changed and never what to (TestSnapshotRedactsSecretRef,
// TestSecretRefNeverReachesTheAuditTrail). The READ path is a separate gate and
// is not free -- it lives in the handler's view model (identities.go in
// internal/web/handlers), Administrator-only, exactly where depRowData.SecretRef
// lives and for the reason that field's comment gives.

// realmOrEmpty normalises an unset realm to the empty string.
//
// identity.realm became NOT NULL in 00003 because a NULL one made
// UNIQUE (realm, name) silently not fire -- NULL <> NULL, so two realm-less
// identities called 'svc-orders' were both accepted, which is exactly the pair
// the constraint existed to stop. Normalising here rather than at every call
// site keeps "no realm" expressible in Go as a nil pointer.
func realmOrEmpty(realm *string) string {
	if realm == nil {
		return ""
	}
	return *realm
}

// CreateIdentity inserts a principal.
func (s *SQLStore) CreateIdentity(ctx context.Context, p domain.Permit, i *domain.Identity) error {
	return s.write(ctx, p, func(t *tx) error {
		// last_rotated IS DELIBERATELY ABSENT from this column list and must
		// stay absent. It has exactly one writer, RecordIdentityRotation
		// (WP-J8, docs/identity-surface-design.md): declaring a credential that
		// already exists and recording when it was last rotated are TWO ACTS
		// and two audit entries, both of which are true. A create that also
		// stamped the date would bury the rotation inside a create snapshot
		// where no reader looking for a rotation will ever find it.
		// internal/store/last_rotated_source_test.go fails on any other writer.
		//
		// row_version is written explicitly rather than left to DEFAULT 1, so
		// the Go struct and the row agree from the first read -- the same shape
		// every other create in this package uses.
		_, err := t.exec(ctx, `
			INSERT INTO identity (id, kind, name, realm, secret_ref, rotation_days,
			                      team_id, lifecycle, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.ID, i.Kind, i.Name, realmOrEmpty(i.Realm), i.SecretRef, i.RotationDays,
			i.TeamID, i.Lifecycle, i.RowVersion)
		if err != nil {
			return translateWriteErr(err, "creating identity")
		}
		return t.logCreate(ctx, "identity", i.ID, i)
	})
}

// ListIdentities returns every principal.
func (s *SQLStore) ListIdentities(ctx context.Context) ([]domain.Identity, error) {
	var identities []domain.Identity
	if err := s.read(ctx, &identities, `SELECT * FROM identity ORDER BY realm, name`); err != nil {
		return nil, fmt.Errorf("listing identities: %w", err)
	}
	return identities, nil
}
