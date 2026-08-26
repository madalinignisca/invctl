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
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Team retirement offers the fix (WP-G7 piece 2, docs/ownership-report-design.md
// §5): before a team is disbanded, show what it looks after and offer to move
// it somewhere else in the same flow. Two calls:
//
//   - TeamOwnershipCounts: what to show on the confirmation screen.
//   - ReassignTeamOwnership: the offered fix, if the operator takes it.
//
// NEITHER IS CALLED BY RetireTeam, AND THAT IS THE WHOLE POINT (design §1).
// RetireTeam (teams.go) still retires a team that owns fifty things with no
// argument from this file -- "retire anyway" is simply calling RetireTeam
// directly, exactly as it already worked before this file existed. The
// handler composes ReassignTeamOwnership followed by RetireTeam only when the
// operator asked for that; nothing here forces the choice, and nothing here
// nulls a column on the operator's behalf. Never auto-reassign, never block
// retirement -- see teams.go's own comment on RetireTeam, which this file
// must not contradict.

// TeamOwnershipCounts is what one team looks after, across the WP-G7
// ownership surface (design §3): asset, service, project, identity,
// custom_field, live only. Built from the SAME five subqueries noContactTeams
// uses (teamOwnershipCountColumns, ownership.go) -- reusing piece 1's store
// code rather than a second query for this screen, which is what design §5
// asks for explicitly.
type TeamOwnershipCounts struct {
	AssetCount       int `db:"asset_count"`
	ServiceCount     int `db:"service_count"`
	ProjectCount     int `db:"project_count"`
	IdentityCount    int `db:"identity_count"`
	CustomFieldCount int `db:"custom_field_count"`
}

// Total decides what the confirmation screen shows: zero means nothing to
// warn about, and the page must skip straight to a plain confirmation rather
// than render an empty warning (design §5).
func (c TeamOwnershipCounts) Total() int {
	return c.AssetCount + c.ServiceCount + c.ProjectCount + c.IdentityCount + c.CustomFieldCount
}

// TeamOwnershipCounts counts what teamID currently looks after, live only --
// a retired asset owned by a team about to retire is not part of the warning,
// the same way it is not part of the ownership report itself.
func (s *SQLStore) TeamOwnershipCounts(ctx context.Context, teamID string) (*TeamOwnershipCounts, error) {
	var row TeamOwnershipCounts
	if err := s.readOne(ctx, &row, `
		SELECT`+teamOwnershipCountColumns+`
		FROM team t WHERE t.id = ?`,
		domain.LifecycleRetired, domain.LifecycleRetired, domain.LifecycleRetired, domain.LifecycleRetired,
		teamID); err != nil {
		return nil, fmt.Errorf("counting what team %s owns: %w", teamID, err)
	}
	return &row, nil
}

// Reassignment outcomes -- one per entity, never a bare count. "10 updated, 1
// skipped" cannot tell an operator whether the skipped row was a colleague's
// edit or a write failure (design §4); ReassignTeamOwnership always returns
// one of these per entity it considered.
const (
	// ReassignAssigned: the entity's team_id (or owner_team_id) moved.
	ReassignAssigned = "assigned"
	// ReassignStale: the guard that made this entity eligible -- WHERE
	// team_id = <the retiring team> -- matched zero rows by the time the
	// write ran. Somebody else reassigned it, or its owning team changed,
	// between the confirmation screen rendering and this request landing.
	// NOT an error: the operator was shown this entity as the retiring
	// team's, and it no longer is, so skipping it and saying so is the
	// correct outcome (design §4).
	ReassignStale = "no_longer_owned_by_this_team"
	// ReassignFailed: the write itself errored. Detail carries a safe,
	// translated message (translateWriteErr), never a raw driver error.
	ReassignFailed = "write_failed"
)

// ReassignOutcome is one entity's result from a bulk reassignment.
type ReassignOutcome struct {
	EntityType string
	EntityID   string
	Name       string
	Result     string
	// Detail is set only when Result is ReassignFailed.
	Detail string
}

// Assigned reports whether this entity actually moved -- a template
// convenience so "which of these are the ones that changed" does not need a
// string comparison against ReassignAssigned in every template.
func (o ReassignOutcome) Assigned() bool { return o.Result == ReassignAssigned }

// ReassignTeamOwnership moves everything fromTeamID looks after, across all
// five owned entity types, to toTeamID.
//
// EACH ENTITY IS ITS OWN TRANSACTION, NOT ONE TRANSACTION FOR THE WHOLE
// BATCH. This is deliberate, not an oversight: PostgreSQL aborts an entire
// transaction on the first statement error within it (every later statement
// then fails with "current transaction is aborted" until rollback), which
// would make ReassignFailed for one entity silently turn every entity after
// it in the same transaction into a false ReassignFailed too. Per-entity
// transactions are also the natural shape of the guarantee this function
// makes: design §4 already treats "each entity's ownership is its own
// declared-state mutation" as the reason for one change_log row per entity,
// and that independence extends to the transaction boundary around each one.
//
// EACH UPDATE IS GUARDED BY THE CONDITION THAT MADE IT ELIGIBLE --
// `WHERE team_id = fromTeamID` (or `owner_team_id` for custom_field) -- never
// by row_version, even for asset, service and project, which carry one.
// Zero rows affected means the entity is no longer this team's, which is
// ReassignStale, not an error. identity and custom_field need no row_version
// to make this atomic: the same guard is the whole eligibility check either
// way (design §4 -- this was decided against a first draft that gave
// identity a row_version it had never had, and that decision is not to be
// revisited here).
//
// ONE change_log ROW PER ENTITY, ALL SHARING ONE batch_id -- never one row
// for the whole batch (design §4: "a single row saying 'assigned 11 things'
// is not an audit trail, it is a receipt"). The batch id is generated once
// per call, so the set this call produced can be reconstructed later even
// though each row's transaction is independent.
func (s *SQLStore) ReassignTeamOwnership(ctx context.Context, actor domain.Actor, fromTeamID, toTeamID string) ([]ReassignOutcome, error) {
	if fromTeamID == "" || toTeamID == "" {
		return nil, fmt.Errorf("reassigning team ownership: %w", domain.ErrInvalid)
	}
	if fromTeamID == toTeamID {
		return nil, fmt.Errorf(
			"reassigning team ownership: the target team must differ from the retiring team: %w", domain.ErrInvalid)
	}

	// Validated ONCE, up front, rather than once per entity: a target team
	// that does not exist or is retired is refused before anything moves,
	// instead of being reported as N separate write_failed rows. Shared with
	// BulkAssignOwnership (bulk_ownership.go, WP-G7 piece 3) via
	// requireActiveTeam, so the two callers of "make this the new owner"
	// cannot drift on what counts as an eligible target.
	if err := s.requireActiveTeam(ctx, toTeamID); err != nil {
		return nil, fmt.Errorf("reassigning team ownership: %w", err)
	}

	batchID := NewID()
	var outcomes []ReassignOutcome

	type candidate struct {
		ID   string `db:"id"`
		Name string `db:"name"`
	}

	var assets []candidate
	if err := s.read(ctx, &assets,
		`SELECT id, name FROM asset WHERE team_id = ? AND lifecycle <> ? ORDER BY name`,
		fromTeamID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing assets owned by team %s: %w", fromTeamID, err)
	}
	for _, c := range assets {
		outcomes = append(outcomes, s.reassignEntity(ctx, actor, "asset", c.ID, c.Name, fromTeamID, toTeamID, batchID,
			func(t *tx) (sql.Result, error) {
				return t.exec(ctx,
					`UPDATE asset SET team_id = ?, updated_at = ?, row_version = row_version + 1
					 WHERE id = ? AND team_id = ?`,
					toTeamID, t.at, c.ID, fromTeamID)
			}))
	}

	var services []candidate
	if err := s.read(ctx, &services,
		`SELECT id, name FROM service WHERE team_id = ? AND lifecycle <> ? ORDER BY name`,
		fromTeamID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing services owned by team %s: %w", fromTeamID, err)
	}
	for _, c := range services {
		outcomes = append(outcomes, s.reassignEntity(ctx, actor, "service", c.ID, c.Name, fromTeamID, toTeamID, batchID,
			func(t *tx) (sql.Result, error) {
				return t.exec(ctx,
					`UPDATE service SET team_id = ?, updated_at = ?, row_version = row_version + 1
					 WHERE id = ? AND team_id = ?`,
					toTeamID, t.at, c.ID, fromTeamID)
			}))
	}

	var projects []candidate
	if err := s.read(ctx, &projects,
		`SELECT id, name FROM project WHERE team_id = ? AND lifecycle <> ? ORDER BY name`,
		fromTeamID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing projects owned by team %s: %w", fromTeamID, err)
	}
	for _, c := range projects {
		outcomes = append(outcomes, s.reassignEntity(ctx, actor, "project", c.ID, c.Name, fromTeamID, toTeamID, batchID,
			func(t *tx) (sql.Result, error) {
				return t.exec(ctx,
					`UPDATE project SET team_id = ?, updated_at = ?, row_version = row_version + 1
					 WHERE id = ? AND team_id = ?`,
					toTeamID, t.at, c.ID, fromTeamID)
			}))
	}

	// identity carries no updated_at and no row_version (shared/00003) -- see
	// the function doc: the WHERE team_id = fromTeamID guard is the whole
	// eligibility check, and that is deliberate, not a gap to fill in later.
	var identities []candidate
	if err := s.read(ctx, &identities,
		`SELECT id, name FROM identity WHERE team_id = ? AND lifecycle <> ? ORDER BY name`,
		fromTeamID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing identities owned by team %s: %w", fromTeamID, err)
	}
	for _, c := range identities {
		outcomes = append(outcomes, s.reassignEntity(ctx, actor, "identity", c.ID, c.Name, fromTeamID, toTeamID, batchID,
			func(t *tx) (sql.Result, error) {
				return t.exec(ctx,
					`UPDATE identity SET team_id = ? WHERE id = ? AND team_id = ?`,
					toTeamID, c.ID, fromTeamID)
			}))
	}

	// custom_field carries no updated_at (migration 00051) but does carry
	// row_version, incremented here the way UpdateCustomField does -- but the
	// guard that makes the write atomic is still owner_team_id, not
	// row_version, for the same reason it is for the other four types.
	var fields []struct {
		ID    string `db:"id"`
		Label string `db:"label"`
	}
	if err := s.read(ctx, &fields,
		`SELECT id, label FROM custom_field WHERE owner_team_id = ? AND retired_at IS NULL ORDER BY label`,
		fromTeamID); err != nil {
		return nil, fmt.Errorf("listing custom fields owned by team %s: %w", fromTeamID, err)
	}
	for _, c := range fields {
		outcomes = append(outcomes, s.reassignEntity(ctx, actor, "custom_field", c.ID, c.Label, fromTeamID, toTeamID, batchID,
			func(t *tx) (sql.Result, error) {
				return t.exec(ctx,
					`UPDATE custom_field SET owner_team_id = ?, row_version = row_version + 1
					 WHERE id = ? AND owner_team_id = ?`,
					toTeamID, c.ID, fromTeamID)
			}))
	}

	return outcomes, nil
}

// reassignEntity runs one entity's UPDATE and, if it actually changed a row,
// its change_log entry, in one transaction -- see ReassignTeamOwnership's doc
// for why this is one transaction PER ENTITY rather than one for the whole
// call.
func (s *SQLStore) reassignEntity(ctx context.Context, actor domain.Actor,
	entityType, entityID, name, fromTeamID, toTeamID, batchID string,
	do func(t *tx) (sql.Result, error)) ReassignOutcome {

	var affected int64
	err := s.write(ctx, domain.AdministratorPermit(actor), func(t *tx) error {
		res, err := do(t)
		if err != nil {
			return translateWriteErr(err, "reassigning "+entityType+" "+entityID)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("checking rows affected reassigning %s %s: %w", entityType, entityID, err)
		}
		affected = n
		if n == 0 {
			// Nothing changed -- the transaction still commits, as a no-op.
			// No change_log row: an audit trail full of "nothing happened"
			// entries is worse than one without them (logUpdate's own rule).
			return nil
		}
		diff := fmt.Sprintf(`{"team_id":{"old":%q,"new":%q}}`, fromTeamID, toTeamID)
		return t.log(ctx, entityType, entityID, domain.ActionUpdate, diff, batchID)
	})
	if err != nil {
		return ReassignOutcome{
			EntityType: entityType, EntityID: entityID, Name: name,
			Result: ReassignFailed, Detail: err.Error(),
		}
	}
	if affected == 0 {
		return ReassignOutcome{EntityType: entityType, EntityID: entityID, Name: name, Result: ReassignStale}
	}
	return ReassignOutcome{EntityType: entityType, EntityID: entityID, Name: name, Result: ReassignAssigned}
}
