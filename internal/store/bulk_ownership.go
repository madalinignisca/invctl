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
	"database/sql"
	"errors"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G7 piece 3: bulk assignment of the Unowned finding
// (docs/ownership-report-design.md §4, §6). Piece 2's ReassignTeamOwnership
// answers "everything this team owned goes to that team". This file answers
// the different question the report's findings actually ask: "these twelve
// unowned assets go to Network Ops", never "everything on this page" (design
// §6). It reuses ReassignTeamOwnership's machinery rather than inventing a
// second mutation path -- one change_log row per entity, one batch id shared
// by all of them, and the eligibility guard doubling as the atomic check
// (design §4) -- and shares requireActiveTeam with it so the two callers of
// "make this the new owner" cannot drift on what counts as an eligible
// target.

// OwnershipCandidate is one row an operator can select for bulk assignment:
// just enough to render a checkbox and a label. The filtered lists below
// (UnownedAssetCandidates, UnownedServiceCandidates, ...) are what "narrow by
// project or site BEFORE selecting" (design §6) actually renders, reusing
// AssetFilter/ServiceFilter/ProjectFilter and assetFilterFrom/serviceFilterFrom
// (internal/web/handlers) rather than a parallel filter type invented for this
// screen alone.
type OwnershipCandidate struct {
	ID   string
	Name string
	// Code is the human-facing code, when the entity type has one (service,
	// project, custom_field). Empty for asset and identity, which have none --
	// the template renders it only when present, matching how the read-only
	// Unowned list (ownership.go's OwnershipRow) already treats Code as
	// optional.
	Code string
}

// AssignNoLongerUnowned is BulkAssignOwnership's stale outcome: the id named
// is no longer eligible by the time the write ran (somebody else assigned it,
// or its team was restored between the report rendering and this request
// landing). NOT an error -- see ReassignStale's own comment, which this
// mirrors for the unowned case specifically (design §4's vocabulary:
// "assigned", "no_longer_unowned", "write_failed").
const AssignNoLongerUnowned = "no_longer_unowned"

// bulkAssignEntityTypes is every entity type this screen can move ownership
// of -- the WP-G7 ownership surface (design §3), minus nothing. Checked
// before entityType ever reaches a query string, since BulkAssignOwnership
// interpolates it directly into a table name below (it never comes from an
// unvalidated request value at that point -- see the handler).
var bulkAssignEntityTypes = map[string]bool{
	"asset": true, "service": true, "project": true, "identity": true, "custom_field": true,
}

// requireActiveTeam refuses a target team that does not exist or cannot act
// as an owner -- a retired team must never be selectable as a bulk-assignment
// or reassignment target (design §4, §6). Shared by ReassignTeamOwnership
// (team_reassignment.go) and BulkAssignOwnership below.
func (s *SQLStore) requireActiveTeam(ctx context.Context, teamID string) error {
	if teamID == "" {
		return fmt.Errorf("a target team is required: %w", domain.ErrInvalid)
	}
	var lifecycle string
	if err := s.readOne(ctx, &lifecycle, `SELECT lifecycle FROM team WHERE id = ?`, teamID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("target team %s does not exist: %w", teamID, domain.ErrInvalid)
		}
		return fmt.Errorf("checking target team %s: %w", teamID, err)
	}
	if lifecycle == domain.LifecycleRetired {
		return fmt.Errorf("target team %s is retired, choose an active team: %w", teamID, domain.ErrInvalid)
	}
	return nil
}

// UnownedAssetCandidates lists unowned assets matching f, for the
// bulk-assignment screen's asset group. f.Unowned is forced true regardless
// of what the caller set -- this is always "which UNOWNED assets match this
// narrowing", never a general asset list -- and ListAssets is the exact
// query /assets itself uses, so a kind/environment/device-type narrowing
// here behaves identically to the list page an operator already knows.
func (s *SQLStore) UnownedAssetCandidates(ctx context.Context, f AssetFilter) ([]OwnershipCandidate, error) {
	f.Unowned = true
	f.Limit = ownershipRowLimit
	rows, err := s.ListAssets(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("listing unowned asset candidates: %w", err)
	}
	out := make([]OwnershipCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, OwnershipCandidate{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

// UnownedServiceCandidates is UnownedAssetCandidates's sibling for services.
func (s *SQLStore) UnownedServiceCandidates(ctx context.Context, f ServiceFilter) ([]OwnershipCandidate, error) {
	f.Unowned = true
	rows, err := s.ListServices(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("listing unowned service candidates: %w", err)
	}
	if len(rows) > ownershipRowLimit {
		rows = rows[:ownershipRowLimit]
	}
	out := make([]OwnershipCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, OwnershipCandidate{ID: r.ID, Name: r.Name, Code: r.Code})
	}
	return out, nil
}

// UnownedProjectCandidates is the same for projects. Narrowed by a free-text
// query only -- a project does not nest inside a site or another project the
// way an asset or a service does, so there is no second dimension to reuse a
// filter helper for.
func (s *SQLStore) UnownedProjectCandidates(ctx context.Context, query string) ([]OwnershipCandidate, error) {
	rows, err := s.ListProjects(ctx, ProjectFilter{Query: query, Unowned: true})
	if err != nil {
		return nil, fmt.Errorf("listing unowned project candidates: %w", err)
	}
	if len(rows) > ownershipRowLimit {
		rows = rows[:ownershipRowLimit]
	}
	out := make([]OwnershipCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, OwnershipCandidate{ID: r.ID, Name: r.Name, Code: r.Code})
	}
	return out, nil
}

// UnownedIdentityCandidates lists unowned, live identities, optionally
// narrowed by a case-insensitive substring of the name. identity has no
// dedicated ListX filter type to reuse (deps.go's ListIdentities takes none),
// so this is hand-written rather than retrofitting one onto an entity type
// that has never needed it.
func (s *SQLStore) UnownedIdentityCandidates(ctx context.Context, query string) ([]OwnershipCandidate, error) {
	where := []string{`team_id IS NULL`, `lifecycle <> ?`}
	args := []any{domain.LifecycleRetired}
	if query != "" {
		where = append(where, `LOWER(name) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(lower(query))+"%")
	}
	sqlText := `SELECT id, name FROM identity` + whereClause(where) + ` ORDER BY name LIMIT ?`
	args = append(args, ownershipRowLimit)

	var out []OwnershipCandidate
	if err := s.read(ctx, &out, sqlText, args...); err != nil {
		return nil, fmt.Errorf("listing unowned identity candidates: %w", err)
	}
	return out, nil
}

// UnownedCustomFieldCandidates lists unowned, live custom fields, optionally
// narrowed by a case-insensitive substring of the label. Mirrors
// ownership.go's own unowned custom-field query (owner_team_id IS NULL AND
// retired_at IS NULL -- custom_field carries no lifecycle column, migration
// 00051) rather than a third definition of "unowned custom field".
func (s *SQLStore) UnownedCustomFieldCandidates(ctx context.Context, query string) ([]OwnershipCandidate, error) {
	where := []string{`owner_team_id IS NULL`, `retired_at IS NULL`}
	var args []any
	if query != "" {
		where = append(where, `LOWER(label) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(lower(query))+"%")
	}
	sqlText := `SELECT id, label AS name, code FROM custom_field` + whereClause(where) + ` ORDER BY label LIMIT ?`
	args = append(args, ownershipRowLimit)

	var out []OwnershipCandidate
	if err := s.read(ctx, &out, sqlText, args...); err != nil {
		return nil, fmt.Errorf("listing unowned custom field candidates: %w", err)
	}
	return out, nil
}

// dedupeIDs drops blanks and repeats, preserving first-seen order -- a form
// posting the same checkbox value twice (or an empty hidden placeholder) must
// not turn into two outcomes for one entity.
func dedupeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// BulkAssignOwnership moves the named, currently-unowned ids of one entity
// type to toTeamID -- the fix WP-G7 piece 3 offers directly on the ownership
// report.
//
// ids IS THE SUBMISSION CONTRACT, NOT A HINT (design §4's rule for
// ReassignTeamOwnership, applied here identically). Every id is one the
// operator was shown as unowned and explicitly selected -- including via
// "select all", which is a CLIENT-SIDE convenience over whatever the current
// filtered view contained (see UnownedAssetCandidates and friends), never a
// signal this function trusts at face value. No id outside this list is ever
// touched, and nothing here re-derives "the current unowned set" to act on
// instead of what was named.
//
// EACH ENTITY IS ITS OWN TRANSACTION and EACH UPDATE IS GUARDED BY
// `... WHERE <owner column> IS NULL` -- never row_version, for the same
// reason ReassignTeamOwnership never uses it: the guard IS the atomic
// eligibility check (design §4). An id that is no longer unowned by the time
// its write runs is reported as AssignNoLongerUnowned, not clobbered and not
// an error.
//
// ONE change_log ROW PER ENTITY, ALL SHARING ONE batch_id -- never one row
// for the whole batch. See ReassignTeamOwnership's doc for the full
// reasoning; it applies here without modification.
func (s *SQLStore) BulkAssignOwnership(ctx context.Context, actor domain.Actor, entityType string, ids []string, toTeamID string) ([]ReassignOutcome, error) {
	if !bulkAssignEntityTypes[entityType] {
		return nil, fmt.Errorf("bulk assigning ownership: unknown entity type %q: %w", entityType, domain.ErrInvalid)
	}
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("bulk assigning ownership: no entities were selected: %w", domain.ErrInvalid)
	}
	if err := s.requireActiveTeam(ctx, toTeamID); err != nil {
		return nil, fmt.Errorf("bulk assigning ownership: %w", err)
	}

	batchID := NewID()
	outcomes := make([]ReassignOutcome, 0, len(ids))
	for _, id := range ids {
		outcomes = append(outcomes, s.assignOneEntity(ctx, actor, entityType, id, s.bestEffortName(ctx, entityType, id), toTeamID, batchID))
	}
	return outcomes, nil
}

// bestEffortName is a display convenience only -- an id that no longer
// resolves to a row (deleted is impossible, per CLAUDE.md's soft-delete-only
// rule, but "never seen" is not) simply renders with an empty name, and
// assignOneEntity's own guard is what decides whether the write happens, not
// this lookup.
func (s *SQLStore) bestEffortName(ctx context.Context, entityType, id string) string {
	col := "name"
	if entityType == "custom_field" {
		col = "label"
	}
	if !bulkAssignEntityTypes[entityType] {
		return ""
	}
	var name string
	if err := s.readOne(ctx, &name, `SELECT `+col+` FROM `+entityType+` WHERE id = ?`, id); err != nil {
		return ""
	}
	return name
}

// assignOneEntity runs one entity's guarded UPDATE and, if it actually
// changed a row, its change_log entry, in one transaction -- the unowned
// counterpart of team_reassignment.go's reassignEntity, guarded by
// "<owner column> IS NULL" instead of "<owner column> = fromTeamID" and
// therefore an "old" value of null rather than a named team.
func (s *SQLStore) assignOneEntity(ctx context.Context, actor domain.Actor, entityType, id, name, toTeamID, batchID string) ReassignOutcome {
	var do func(t *tx) (sql.Result, error)
	diffKey := "team_id"

	switch entityType {
	case "asset":
		do = func(t *tx) (sql.Result, error) {
			return t.exec(ctx,
				`UPDATE asset SET team_id = ?, updated_at = ?, row_version = row_version + 1
				 WHERE id = ? AND team_id IS NULL`,
				toTeamID, t.at, id)
		}
	case "service":
		do = func(t *tx) (sql.Result, error) {
			return t.exec(ctx,
				`UPDATE service SET team_id = ?, updated_at = ?, row_version = row_version + 1
				 WHERE id = ? AND team_id IS NULL`,
				toTeamID, t.at, id)
		}
	case "project":
		do = func(t *tx) (sql.Result, error) {
			return t.exec(ctx,
				`UPDATE project SET team_id = ?, updated_at = ?, row_version = row_version + 1
				 WHERE id = ? AND team_id IS NULL`,
				toTeamID, t.at, id)
		}
	case "identity":
		// No updated_at, no row_version (shared/00003) -- see
		// ReassignTeamOwnership's own comment on identity; the same "guard is
		// the whole eligibility check" reasoning applies unchanged here.
		do = func(t *tx) (sql.Result, error) {
			return t.exec(ctx, `UPDATE identity SET team_id = ? WHERE id = ? AND team_id IS NULL`, toTeamID, id)
		}
	case "custom_field":
		diffKey = "owner_team_id"
		do = func(t *tx) (sql.Result, error) {
			return t.exec(ctx,
				`UPDATE custom_field SET owner_team_id = ?, row_version = row_version + 1
				 WHERE id = ? AND owner_team_id IS NULL`,
				toTeamID, id)
		}
	default:
		// Unreachable: BulkAssignOwnership validates entityType against
		// bulkAssignEntityTypes before this is ever called. Reported rather
		// than panicking, on the general principle that this package never
		// panics outside main.
		return ReassignOutcome{EntityType: entityType, EntityID: id, Name: name,
			Result: ReassignFailed, Detail: "unknown entity type"}
	}

	var affected int64
	err := s.write(ctx, actor, func(t *tx) error {
		res, err := do(t)
		if err != nil {
			return translateWriteErr(err, "assigning "+entityType+" "+id)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("checking rows affected assigning %s %s: %w", entityType, id, err)
		}
		affected = n
		if n == 0 {
			// No-op transaction, no change_log row -- logUpdate's own rule:
			// an audit trail full of "nothing happened" entries is worse than
			// one without them.
			return nil
		}
		diff := fmt.Sprintf(`{%q:{"old":null,"new":%q}}`, diffKey, toTeamID)
		return t.log(ctx, entityType, id, domain.ActionUpdate, diff, batchID)
	})
	if err != nil {
		return ReassignOutcome{EntityType: entityType, EntityID: id, Name: name,
			Result: ReassignFailed, Detail: err.Error()}
	}
	if affected == 0 {
		return ReassignOutcome{EntityType: entityType, EntityID: id, Name: name, Result: AssignNoLongerUnowned}
	}
	return ReassignOutcome{EntityType: entityType, EntityID: id, Name: name, Result: ReassignAssigned}
}
