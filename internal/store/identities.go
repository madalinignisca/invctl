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
	"time"

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
		if err := t.logCreate(ctx, "identity", i.ID, i); err != nil {
			return err
		}
		return s.indexIdentity(ctx, t, i)
	})
}

// indexIdentity makes a credential reference findable by name and realm.
//
// SECRET_REF IS DELIBERATELY ABSENT, and it is the one field on Identity a
// reader might expect to find here. It holds a PATH -- `vault://kv/prod/...` --
// never a credential, so indexing it would disclose no secret value. It would
// still be wrong. Search is the widest read surface in this product: it answers
// every authenticated reader, with no project scope and no cost gate, and a
// list of where this estate keeps its credentials is a reconnaissance map
// whether or not the values behind it are reachable from here. CLAUDE.md keeps
// secret_ref out of the audit trail on the same reasoning, and the index is a
// wider surface than the audit trail, not a narrower one.
//
// NOT REINDEXED ON RETIREMENT, unlike a team. indexTeam is reindexed by
// RetireTeam because a team's document carries `contact_ref`, and search_index
// holds only the CURRENT value -- that is what makes an erasure request
// answerable by editing the team. An identity's document is name, kind and
// realm: no personal data, nothing an erasure request reaches, so the general
// rule applies and a retired identity stays findable. It has to: "the natural
// response to a compromised credential is to retire it and create its
// replacement under the same name" (migration 00003), and an operator asking
// what happened to the old one should still be able to find it.
func (s *SQLStore) indexIdentity(ctx context.Context, t *tx, i *domain.Identity) error {
	return s.indexEntity(ctx, t, searchDoc{
		EntityType: "identity", EntityID: i.ID,
		Title: i.Name, Subtitle: i.Kind, Body: realmOrEmpty(i.Realm),
	})
}

// IdentityRow is one identity plus what the list and detail pages need beside
// it: the owning team's display fields and a count of what still names it.
type IdentityRow struct {
	domain.Identity
	TeamCode string `db:"team_code"`
	TeamName string `db:"team_name"`
	// What would notice if this credential died. Counted for the list; listed
	// on the detail page by IdentityUsage. It is what makes withdrawal an
	// informed act rather than a blind one.
	DependencyCount int `db:"dependency_count"`
	WindowsCount    int `db:"windows_count"`
}

const identitySelect = `
	SELECT i.*,
	       COALESCE(tm.code, '') AS team_code,
	       COALESCE(tm.name, '') AS team_name,
	       (SELECT COUNT(*) FROM dependency d
	         WHERE d.identity_id = i.id AND d.lifecycle <> 'retired') AS dependency_count,
	       (SELECT COUNT(*) FROM rt_windows w
	          JOIN service_instance si ON si.id = w.instance_id
	         WHERE w.logon_identity_id = i.id AND si.lifecycle <> 'retired') AS windows_count
	FROM identity i
	LEFT JOIN team tm ON tm.id = i.team_id`

// IdentityFilter narrows an identity list.
type IdentityFilter struct {
	// Query matches name or realm, substring, ASCII-folded.
	Query  string
	Kind   string
	TeamID string
	// Rotation filters on the DERIVED state, so it is applied in Go after the
	// read rather than in SQL. Two reasons, and neither is laziness. The state
	// depends on today's date, so a SQL predicate would embed a clock in a
	// query (CLAUDE.md: never NOW()); and it depends on whether the stored
	// value PARSES, which is how RotationUnreadable is reachable at all and
	// which no portable SQL expression can decide. CertificateFilter.Host does
	// the same, for the same class of reason.
	Rotation domain.RotationState
	// IncludeRetired: a withdrawn credential must stay FINDABLE, because what
	// is stored keeps displaying (RetireEnvironment's ruling, carried over).
	// What changes on retirement is only that it stops being offered as a NEW
	// choice.
	IncludeRetired bool
}

// ListIdentities lists principals, narrowed by f.
func (s *SQLStore) ListIdentities(ctx context.Context, f IdentityFilter) ([]IdentityRow, error) {
	var where []string
	var args []any
	if !f.IncludeRetired {
		where = append(where, `i.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	if f.Kind != "" {
		where = append(where, `i.kind = ?`)
		args = append(args, f.Kind)
	}
	if f.TeamID != "" {
		where = append(where, `i.team_id = ?`)
		args = append(args, f.TeamID)
	}
	if f.Query != "" {
		// ASCII-only folding, the same trade ListCertificates documents: `lower`
		// folds bytes A-Z to match SQLite's LOWER(), while PostgreSQL's is
		// locale-aware. A principal name is ASCII in practice; a realm can be a
		// Windows domain and is too.
		where = append(where, `(LOWER(i.name) LIKE ? ESCAPE '\' OR LOWER(i.realm) LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(lower(f.Query)) + "%"
		args = append(args, like, like)
	}

	query := identitySelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// Ordered in SQL as a hint and SORTED IN GO as the authority. expiry.go
	// states the rule -- "which of two rows sharing a date comes first must not
	// depend on the server's collation" -- and ListCertificates records that the
	// agreement observed in CI is an artefact of the Alpine/musl PostgreSQL
	// image, which implements no locale collation and so degenerates to byte
	// order.
	query += ` ORDER BY i.realm, i.name`

	var rows []IdentityRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing identities: %w", err)
	}

	if f.Rotation != "" {
		now := s.now()
		kept := rows[:0]
		for _, r := range rows {
			if r.RotationStatus(now) == f.Rotation {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	sort.SliceStable(rows, func(a, b int) bool {
		x, y := rows[a], rows[b]
		xr, yr := derefOr(x.Realm, ""), derefOr(y.Realm, "")
		if xr != yr {
			return xr < yr
		}
		if x.Name != y.Name {
			return x.Name < y.Name
		}
		return x.ID < y.ID
	})
	if f.Query != "" {
		sort.SliceStable(rows, rankNames(f.Query, func(i int) string { return rows[i].Name }))
	}
	return rows, nil
}

// GetIdentity returns one principal, with the same joined fields ListIdentities
// carries.
func (s *SQLStore) GetIdentity(ctx context.Context, id string) (*IdentityRow, error) {
	var row IdentityRow
	if err := s.readOne(ctx, &row, identitySelect+` WHERE i.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting identity %s: %w", id, err)
	}
	return &row, nil
}

// UpdateIdentity corrects a credential reference.
//
// NOT lifecycle, and NOT last_rotated. lifecycle is pinned from the stored row
// for the reason UpdateEnvironment and UpdateInterface pin theirs: this method
// would otherwise be a second withdrawal path with none of RetireIdentity's
// audit shape. last_rotated is pinned because it has exactly one writer and
// this is not it -- see this file's header and
// internal/store/last_rotated_source_test.go.
func (s *SQLStore) UpdateIdentity(ctx context.Context, p domain.Permit, i *domain.Identity) error {
	if err := i.Validate(); err != nil {
		return err
	}
	before, err := s.GetIdentity(ctx, i.ID)
	if err != nil {
		return err
	}
	i.Lifecycle = before.Lifecycle
	i.LastRotated = before.LastRotated

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE identity
			   SET kind = ?, name = ?, realm = ?, secret_ref = ?, rotation_days = ?,
			       team_id = ?, row_version = row_version + 1
			 WHERE id = ? AND row_version = ?`,
			i.Kind, i.Name, realmOrEmpty(i.Realm), i.SecretRef, i.RotationDays,
			i.TeamID, i.ID, i.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating identity")
		}
		if err := requireVersion(res, "identity", i.ID, &i.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "identity", i.ID, &before.Identity, i); err != nil {
			return err
		}
		// Reindexed on every edit for the same reason hardware.go reindexes a
		// corrected part number: a name fixed in the form and not in the index
		// leaves search answering with the typo forever, and the operator who
		// corrected it has no way to tell.
		return s.indexIdentity(ctx, t, i)
	})
}

// RetireIdentity withdraws a credential reference.
//
// REFUSES NOTHING AND REWRITES NOTHING, and migration 00003 states the case:
// "the natural response to a compromised credential is to retire it and create
// its replacement under the same name", which is why the uniqueness index is
// scoped to lifecycle = 'active'. Blocking retirement until every dependency
// has been re-pointed would mean, at exactly the wrong moment, that the
// compromised credential cannot be withdrawn. And rewriting
// dependency.identity_id or rt_windows.logon_identity_id automatically would
// write change_log entries attributing a dependency change to whoever clicked
// withdraw -- the misattribution RetireInterface and RetireEnvironment both
// refuse to commit.
//
// WHAT STAYS TRUE: what is STORED keeps displaying. A dependency still naming a
// retired identity shows it, marked retired. What changes is that it stops
// being offered as a NEW choice.
//
// NO RESTORE PATH, and no trap in that: the live-scoped unique index means a
// retired identity's (realm, name) is immediately available to a replacement,
// so nobody is ever stranded -- and a restore would put two live rows in
// contention for one name.
func (s *SQLStore) RetireIdentity(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetIdentity(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		// Already withdrawn: a second audit entry would claim a withdrawal that
		// did not happen. RetireEnvironment and RetireTeam do the same.
		return nil
	}
	after := before.Identity
	after.Lifecycle = domain.LifecycleRetired

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE identity SET lifecycle = ?, row_version = row_version + 1
			 WHERE id = ? AND row_version = ?`,
			domain.LifecycleRetired, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring identity")
		}
		v := before.RowVersion
		if err := requireVersion(res, "identity", id, &v); err != nil {
			return err
		}
		return t.logUpdate(ctx, "identity", id, &before.Identity, &after)
	})
}

// RecordIdentityRotation stamps the day somebody rotated this credential.
//
// THIS IS THE FEATURE, not a side effect of an edit form, because it is the
// thing somebody actually does. VerifyDependency is the precedent in every
// respect: a distinct store method and a distinct route that stamp one column
// and log through logUpdate, with action staying 'update' -- the change_log
// CHECK allows exactly create|update|delete|retire and adding a value to it
// would mean rebuilding the table on SQLite (docs/AUDIT.md rule 10).
//
// THE LOG *IS* THE HISTORY. The table keeps only the latest date; change_log
// keeps all of them, permanently, append-only, each with its actor. The detail
// page's timeline shows a rotation as a last_rotated change. Do NOT build a
// rotation-specific history query: that means a predicate inside `diff`, and
// querying inside JSON is banned (rule 15 -- fold in Go).
//
// NO ATTESTATION CHECK, deliberately, unlike VerifyDependency. Recording a
// rotation is a statement of fact about a credential, not a person putting
// their name to an edge; no column stores who rotated it, only
// change_log.actor does, and that is unforgeable by construction (rule 5).
// Applying CheckAttestationWrite would also break the seeder, which writes as
// SystemActor. Agent credentials never reach this path anyway -- rule 6
// forbids an agent-reachable handler from returning an identity row at all.
func (s *SQLStore) RecordIdentityRotation(ctx context.Context, p domain.Permit, id, date string) error {
	date = strings.TrimSpace(date)
	when, err := domain.ParseDate(date)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add("last_rotated", "must be a real date in the form %s", domain.DateFormat)
		return ve
	}
	// BACKDATED YES, POST-DATED NEVER, and the asymmetry is the whole argument.
	// Somebody rotated the credential last Tuesday and is catching up on
	// Thursday; refusing that forces them to record a date they know is wrong.
	// A PAST date can only ever make a finding worse -- it can move a
	// credential from "within window" to "overdue" and can never hide
	// anything. A FUTURE date hides an overdue finding for a rotation that has
	// not happened, which is the one direction that turns this feature into a
	// way of silencing itself. No monotonicity rule: an earlier date than the
	// stored one is allowed, because the stored one may simply have been
	// wrong, and change_log records both values and the actor.
	today := s.now().UTC().Truncate(24 * time.Hour)
	if when.After(today) {
		ve := &domain.ValidationError{}
		ve.Add("last_rotated", "a rotation cannot be recorded in the future; "+
			"today is %s", domain.FormatDate(s.now()))
		return ve
	}

	before, err := s.GetIdentity(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		// Rotating a withdrawn credential is not a thing that happened.
		// Refused before anything opens a transaction, with a field message
		// naming the identity -- the shape SetBundleMembers uses for a
		// retired bundle.
		ve := &domain.ValidationError{}
		ve.Add("last_rotated", "%s has been withdrawn", before.Name)
		return ve
	}
	if before.LastRotated != nil && *before.LastRotated == date {
		// NOTHING AT ALL: no UPDATE, no change_log row, no row_version bump.
		// Without this, diffJSON returns ok=false, logUpdate skips the entry,
		// and the UPDATE still moves row_version -- a declared-state write
		// with NO AUDIT ROW, which since WP-G1 is an authorization bypass and
		// not merely an untraceable change. Returns nil the way
		// RetireEnvironment does for an already-retired row, rather than
		// claiming a rotation that did not happen.
		return nil
	}

	after := before.Identity
	after.LastRotated = &date

	return s.write(ctx, p, func(t *tx) error {
		// NO row_version GUARD, and it BUMPS. VerifyDependency exactly: two
		// operators recording a rotation of the same credential is not a
		// conflict worth a 409. The cost is stated rather than hidden -- an
		// operator with a correction form already open gets a spurious 409
		// after somebody else records a rotation, even though the form shows
		// no field that moved. That is accepted, because the alternative (a
		// token that does not move when the row changes) is worse, and
		// because requireVersion's contract is about the ROW, not a subset of
		// its fields.
		if _, err := t.exec(ctx,
			`UPDATE identity SET last_rotated = ?, row_version = row_version + 1
			  WHERE id = ?`, date, id); err != nil {
			return translateWriteErr(err, "recording identity rotation")
		}
		return t.logUpdate(ctx, "identity", id, &before.Identity, &after)
	})
}

// IdentityUsageRows is what still names a credential.
type IdentityUsageRows struct {
	Dependencies []IdentityDependencyUse
	Windows      []IdentityWindowsUse
}

type IdentityDependencyUse struct {
	DependencyID string `db:"dependency_id"`
	ConsumerID   string `db:"consumer_id"`
	ConsumerCode string `db:"consumer_code"`
	ProviderName string `db:"provider_name"`
	AuthMethod   string `db:"auth_method"`
}

type IdentityWindowsUse struct {
	InstanceID  string `db:"instance_id"`
	ServiceID   string `db:"service_id"`
	ServiceCode string `db:"service_code"`
	ServiceName string `db:"service_name"`
}

// IdentityUsage answers "what would notice if this credential died".
//
// LIVE ROWS ONLY. A retired dependency naming this identity is history, and the
// question the withdrawal screen asks is about what is still running.
func (s *SQLStore) IdentityUsage(ctx context.Context, id string) (*IdentityUsageRows, error) {
	var deps []IdentityDependencyUse
	if err := s.read(ctx, &deps, `
		SELECT d.id AS dependency_id,
		       COALESCE(cs.id, '')   AS consumer_id,
		       COALESCE(cs.code, '') AS consumer_code,
		       COALESCE(pe.name, '') AS provider_name,
		       COALESCE(d.auth_method, '') AS auth_method
		FROM dependency d
		LEFT JOIN service  cs ON cs.id = d.consumer_service_id
		LEFT JOIN endpoint pe ON pe.id = d.provider_endpoint_id
		WHERE d.identity_id = ? AND d.lifecycle <> ?`,
		id, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing dependencies naming identity %s: %w", id, err)
	}
	var wins []IdentityWindowsUse
	if err := s.read(ctx, &wins, `
		SELECT w.instance_id AS instance_id,
		       COALESCE(sv.id, '')   AS service_id,
		       COALESCE(sv.code, '') AS service_code,
		       w.service_name        AS service_name
		FROM rt_windows w
		JOIN service_instance si ON si.id = w.instance_id
		LEFT JOIN service sv ON sv.id = si.service_id
		WHERE w.logon_identity_id = ? AND si.lifecycle <> ?`,
		id, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing windows services naming identity %s: %w", id, err)
	}
	// Sorted in Go, for the collation reason ListIdentities states. Ties broken
	// on the id so the order is total: two edges from the same consumer to the
	// same endpoint are legitimate, and an unstable order between them makes a
	// golden test flap on one engine and not the other.
	sort.SliceStable(deps, func(a, b int) bool {
		x, y := deps[a], deps[b]
		if x.ConsumerCode != y.ConsumerCode {
			return x.ConsumerCode < y.ConsumerCode
		}
		if x.ProviderName != y.ProviderName {
			return x.ProviderName < y.ProviderName
		}
		return x.DependencyID < y.DependencyID
	})
	sort.SliceStable(wins, func(a, b int) bool {
		x, y := wins[a], wins[b]
		if x.ServiceCode != y.ServiceCode {
			return x.ServiceCode < y.ServiceCode
		}
		if x.ServiceName != y.ServiceName {
			return x.ServiceName < y.ServiceName
		}
		return x.InstanceID < y.InstanceID
	})
	return &IdentityUsageRows{Dependencies: deps, Windows: wins}, nil
}
