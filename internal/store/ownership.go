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

	"github.com/madalinignisca/invctl/internal/domain"
)

// The ownership report (WP-G7, piece 1): what has no owner, or an owner who
// cannot act. docs/ownership-report-design.md is the binding design; this is
// its read model.
//
// READ-ONLY. Nothing in this file mutates anything -- no bulk assignment, no
// change to team retirement. Those are pieces 2 and 3 of the same work
// package and are out of scope here on purpose.
//
// NOT THE LINT ENGINE (design §7). These three conditions are findings, not
// rules: no severity, no suppression, no schedule, no exception list. The
// moment any of those is wanted it belongs in a lint engine (forbidden before
// M5 -- CLAUDE.md), and this report becomes a view over its findings or is
// deleted. The vocabulary it reads -- eligible / transitional / cannot-act --
// lives once, in domain.ClassifyTeamLifecycle, precisely so lint can consume
// it later rather than restate it.
//
// RETIRED ENTITIES ARE NOT ORPHANS. An estate with three hundred retired
// assets nobody owns is noise, not a finding: the question this report asks
// is "who looks after this", and nobody needs to look after a retired thing.
// Every query below excludes them explicitly (`lifecycle <> 'retired'`, or
// `retired_at IS NULL` for custom_field, which carries no lifecycle column at
// all -- migration 00051).
//
// FIVE HAND-WRITTEN QUERIES PER FINDING, NOT A GENERIC BUILDER. The five
// entity types differ in table name, owner column, display columns and how
// "live" is spelled, and CLAUDE.md's "hand-written SQL, no ORM" cuts against
// assembling a query string from fragments to avoid repeating five short
// SELECTs -- the five plain statements are what a reviewer can actually
// check run correctly on both engines.

// ownershipRowLimit bounds the ENTITY-LEVEL findings (unowned, cannot-act)
// PER ENTITY TYPE. An index does not save a page rendering fifty thousand
// rows (design §9) -- the limit is enforced in SQL via LIMIT, not by fetching
// everything and truncating in Go, so a pathological estate cannot make this
// report itself an outage. The true count is fetched separately so the page
// can say "showing the first N of M" honestly rather than pretend N is M.
const ownershipRowLimit = 200

// OwnershipRow is one entity-level finding -- unowned, or owned by a team
// that cannot act.
type OwnershipRow struct {
	EntityType string `db:"entity_type"`
	ID         string `db:"id"`
	Name       string `db:"name"`
	Code       string `db:"code"`
	Lifecycle  string `db:"lifecycle"`

	// The owning team, when there is one. Zero values for an UNOWNED row --
	// there is nothing to name.
	TeamID        string `db:"team_id"`
	TeamCode      string `db:"team_code"`
	TeamName      string `db:"team_name"`
	TeamLifecycle string `db:"team_lifecycle"`
}

// Eligibility classifies a "cannot act" row's owner. Computed in Go from
// TeamLifecycle rather than carried as a column -- one place decides the
// vocabulary (domain.ClassifyTeamLifecycle), not a second copy of it in SQL.
// Zero value on an unowned row, which has no team to classify.
func (r OwnershipRow) Eligibility() domain.OwnerEligibility {
	e, _ := domain.ClassifyTeamLifecycle(r.TeamLifecycle)
	return e
}

// Transitional reports whether this "cannot act" row's owner is on its way
// out rather than already gone -- the design's "arguably the most
// interesting finding" case (a DEPRECATED team still owning things), worth
// its own visual treatment rather than reading identically to a retired one.
func (r OwnershipRow) Transitional() bool { return r.Eligibility() == domain.OwnerTransitional }

// NoContactTeam is finding 3: one row per TEAM, not per entity it owns. A
// team owning forty things with no contact is one finding with one fix --
// edit the team -- not forty rows burying everything else (design §2).
type NoContactTeam struct {
	TeamID   string `db:"team_id"`
	TeamCode string `db:"team_code"`
	TeamName string `db:"team_name"`

	AssetCount       int `db:"asset_count"`
	ServiceCount     int `db:"service_count"`
	ProjectCount     int `db:"project_count"`
	IdentityCount    int `db:"identity_count"`
	CustomFieldCount int `db:"custom_field_count"`
}

// Total is what this team owns across all five entity types.
func (t NoContactTeam) Total() int {
	return t.AssetCount + t.ServiceCount + t.ProjectCount + t.IdentityCount + t.CustomFieldCount
}

// OwnershipReport is everything the page shows.
type OwnershipReport struct {
	// Unowned: team_id (or owner_team_id) IS NULL. Nobody ever said who looks
	// after this.
	Unowned          []OwnershipRow
	UnownedTotal     int
	UnownedTruncated bool

	// CannotAct: the team exists but its lifecycle says it will not answer.
	// Reported as its own section, never merged with Unowned: "never
	// answered" and "answer went stale" are different conversations with
	// different people (design §2).
	CannotAct          []OwnershipRow
	CannotActTotal     int
	CannotActTruncated bool

	// NoContact: eligible (active) teams that own at least one live entity and
	// carry no contact_ref. One row per team, never per entity (design §2).
	NoContact []NoContactTeam
}

// Empty reports whether the estate has no ownership gaps at all -- a real
// answer that the page must render plainly rather than let read as a failed
// query (design §6).
func (r *OwnershipReport) Empty() bool {
	return len(r.Unowned) == 0 && len(r.CannotAct) == 0 && len(r.NoContact) == 0
}

// OwnershipFindings runs the three conditions.
func (s *SQLStore) OwnershipFindings(ctx context.Context) (*OwnershipReport, error) {
	report := &OwnershipReport{}

	unowned, total, err := s.unownedRows(ctx)
	if err != nil {
		return nil, err
	}
	report.Unowned, report.UnownedTotal = unowned, total
	report.UnownedTruncated = total > len(unowned)

	cannotAct, total, err := s.cannotActRows(ctx)
	if err != nil {
		return nil, err
	}
	report.CannotAct, report.CannotActTotal = cannotAct, total
	report.CannotActTruncated = total > len(cannotAct)

	noContact, err := s.noContactTeams(ctx)
	if err != nil {
		return nil, err
	}
	report.NoContact = noContact

	return report, nil
}

// unownedRows is finding 1, merged across all five entity types and bounded.
//
// Each SELECT is capped with LIMIT so a single pathological entity type
// cannot exhaust the whole page budget on its own; the merge below then
// applies the same cap again across the combined set, and the two together
// are what keeps report rendering bounded regardless of which table the
// orphans are concentrated in.
func (s *SQLStore) unownedRows(ctx context.Context) ([]OwnershipRow, int, error) {
	var rows []OwnershipRow

	var assets []OwnershipRow
	if err := s.read(ctx, &assets, `
		SELECT 'asset' AS entity_type, id, name, '' AS code, lifecycle,
		       '' AS team_id, '' AS team_code, '' AS team_name, '' AS team_lifecycle
		FROM asset WHERE team_id IS NULL AND lifecycle <> ?
		ORDER BY name LIMIT ?`, domain.LifecycleRetired, ownershipRowLimit); err != nil {
		return nil, 0, fmt.Errorf("listing unowned assets: %w", err)
	}
	rows = append(rows, assets...)

	var services []OwnershipRow
	if err := s.read(ctx, &services, `
		SELECT 'service' AS entity_type, id, name, code, lifecycle,
		       '' AS team_id, '' AS team_code, '' AS team_name, '' AS team_lifecycle
		FROM service WHERE team_id IS NULL AND lifecycle <> ?
		ORDER BY name LIMIT ?`, domain.LifecycleRetired, ownershipRowLimit); err != nil {
		return nil, 0, fmt.Errorf("listing unowned services: %w", err)
	}
	rows = append(rows, services...)

	var projects []OwnershipRow
	if err := s.read(ctx, &projects, `
		SELECT 'project' AS entity_type, id, name, code, lifecycle,
		       '' AS team_id, '' AS team_code, '' AS team_name, '' AS team_lifecycle
		FROM project WHERE team_id IS NULL AND lifecycle <> ?
		ORDER BY name LIMIT ?`, domain.LifecycleRetired, ownershipRowLimit); err != nil {
		return nil, 0, fmt.Errorf("listing unowned projects: %w", err)
	}
	rows = append(rows, projects...)

	var identities []OwnershipRow
	if err := s.read(ctx, &identities, `
		SELECT 'identity' AS entity_type, id, name, '' AS code, lifecycle,
		       '' AS team_id, '' AS team_code, '' AS team_name, '' AS team_lifecycle
		FROM identity WHERE team_id IS NULL AND lifecycle <> ?
		ORDER BY name LIMIT ?`, domain.LifecycleRetired, ownershipRowLimit); err != nil {
		return nil, 0, fmt.Errorf("listing unowned identities: %w", err)
	}
	rows = append(rows, identities...)

	// custom_field carries no `lifecycle` column (migration 00051) -- a
	// retired field is retired_at IS NOT NULL, and NewCustomField's own
	// validation refuses an unowned field on create, so every unowned row
	// here is one of the pre-00054 fields a migration could not invent an
	// owner for.
	var fields []OwnershipRow
	if err := s.read(ctx, &fields, `
		SELECT 'custom_field' AS entity_type, id, label AS name, code, 'active' AS lifecycle,
		       '' AS team_id, '' AS team_code, '' AS team_name, '' AS team_lifecycle
		FROM custom_field WHERE owner_team_id IS NULL AND retired_at IS NULL
		ORDER BY label LIMIT ?`, ownershipRowLimit); err != nil {
		return nil, 0, fmt.Errorf("listing unowned custom fields: %w", err)
	}
	rows = append(rows, fields...)

	total, err := s.unownedTotal(ctx)
	if err != nil {
		return nil, 0, err
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].EntityType != rows[j].EntityType {
			return rows[i].EntityType < rows[j].EntityType
		}
		return rows[i].Name < rows[j].Name
	})
	if len(rows) > ownershipRowLimit {
		rows = rows[:ownershipRowLimit]
	}
	return rows, total, nil
}

// unownedTotal is the true count behind unownedRows, independent of the
// LIMIT each SELECT there carries -- what makes "showing the first 200 of
// 812" an honest sentence rather than a guess.
func (s *SQLStore) unownedTotal(ctx context.Context) (int, error) {
	var n int
	if err := s.readOne(ctx, &n, `SELECT
		(SELECT COUNT(*) FROM asset WHERE team_id IS NULL AND lifecycle <> ?) +
		(SELECT COUNT(*) FROM service WHERE team_id IS NULL AND lifecycle <> ?) +
		(SELECT COUNT(*) FROM project WHERE team_id IS NULL AND lifecycle <> ?) +
		(SELECT COUNT(*) FROM identity WHERE team_id IS NULL AND lifecycle <> ?) +
		(SELECT COUNT(*) FROM custom_field WHERE owner_team_id IS NULL AND retired_at IS NULL)`,
		domain.LifecycleRetired, domain.LifecycleRetired, domain.LifecycleRetired, domain.LifecycleRetired); err != nil {
		return 0, fmt.Errorf("counting unowned entities: %w", err)
	}
	return n, nil
}

// cannotActRows is finding 2: entities owned by a team whose lifecycle is not
// domain.OwnerCanAct. The IN list is built FROM domain.NonEligibleTeamLifecycles,
// never a bare `t.lifecycle = 'retired'` -- the one check this whole report
// exists to get right, because a binary test silently misses a DEPRECATED
// owner.
func (s *SQLStore) cannotActRows(ctx context.Context) ([]OwnershipRow, int, error) {
	nonEligible := domain.NonEligibleTeamLifecycles()
	inClause := placeholders(len(nonEligible))
	args := anySlice(nonEligible)

	var rows []OwnershipRow

	var assets []OwnershipRow
	assetArgs := append(append([]any{}, args...), domain.LifecycleRetired, ownershipRowLimit)
	if err := s.read(ctx, &assets, `
		SELECT 'asset' AS entity_type, a.id, a.name, '' AS code, a.lifecycle,
		       t.id AS team_id, t.code AS team_code, t.name AS team_name, t.lifecycle AS team_lifecycle
		FROM asset a JOIN team t ON t.id = a.team_id
		WHERE t.lifecycle IN (`+inClause+`) AND a.lifecycle <> ?
		ORDER BY a.name LIMIT ?`, assetArgs...); err != nil {
		return nil, 0, fmt.Errorf("listing assets whose owner cannot act: %w", err)
	}
	rows = append(rows, assets...)

	var services []OwnershipRow
	serviceArgs := append(append([]any{}, args...), domain.LifecycleRetired, ownershipRowLimit)
	if err := s.read(ctx, &services, `
		SELECT 'service' AS entity_type, s.id, s.name, s.code, s.lifecycle,
		       t.id AS team_id, t.code AS team_code, t.name AS team_name, t.lifecycle AS team_lifecycle
		FROM service s JOIN team t ON t.id = s.team_id
		WHERE t.lifecycle IN (`+inClause+`) AND s.lifecycle <> ?
		ORDER BY s.name LIMIT ?`, serviceArgs...); err != nil {
		return nil, 0, fmt.Errorf("listing services whose owner cannot act: %w", err)
	}
	rows = append(rows, services...)

	var projects []OwnershipRow
	projectArgs := append(append([]any{}, args...), domain.LifecycleRetired, ownershipRowLimit)
	if err := s.read(ctx, &projects, `
		SELECT 'project' AS entity_type, p.id, p.name, p.code, p.lifecycle,
		       t.id AS team_id, t.code AS team_code, t.name AS team_name, t.lifecycle AS team_lifecycle
		FROM project p JOIN team t ON t.id = p.team_id
		WHERE t.lifecycle IN (`+inClause+`) AND p.lifecycle <> ?
		ORDER BY p.name LIMIT ?`, projectArgs...); err != nil {
		return nil, 0, fmt.Errorf("listing projects whose owner cannot act: %w", err)
	}
	rows = append(rows, projects...)

	var identities []OwnershipRow
	identityArgs := append(append([]any{}, args...), domain.LifecycleRetired, ownershipRowLimit)
	if err := s.read(ctx, &identities, `
		SELECT 'identity' AS entity_type, i.id, i.name, '' AS code, i.lifecycle,
		       t.id AS team_id, t.code AS team_code, t.name AS team_name, t.lifecycle AS team_lifecycle
		FROM identity i JOIN team t ON t.id = i.team_id
		WHERE t.lifecycle IN (`+inClause+`) AND i.lifecycle <> ?
		ORDER BY i.name LIMIT ?`, identityArgs...); err != nil {
		return nil, 0, fmt.Errorf("listing identities whose owner cannot act: %w", err)
	}
	rows = append(rows, identities...)

	var fields []OwnershipRow
	fieldArgs := append(append([]any{}, args...), ownershipRowLimit)
	if err := s.read(ctx, &fields, `
		SELECT 'custom_field' AS entity_type, cf.id, cf.label AS name, cf.code, 'active' AS lifecycle,
		       t.id AS team_id, t.code AS team_code, t.name AS team_name, t.lifecycle AS team_lifecycle
		FROM custom_field cf JOIN team t ON t.id = cf.owner_team_id
		WHERE t.lifecycle IN (`+inClause+`) AND cf.retired_at IS NULL
		ORDER BY cf.label LIMIT ?`, fieldArgs...); err != nil {
		return nil, 0, fmt.Errorf("listing custom fields whose owner cannot act: %w", err)
	}
	rows = append(rows, fields...)

	total, err := s.cannotActTotal(ctx, nonEligible)
	if err != nil {
		return nil, 0, err
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].EntityType != rows[j].EntityType {
			return rows[i].EntityType < rows[j].EntityType
		}
		return rows[i].Name < rows[j].Name
	})
	if len(rows) > ownershipRowLimit {
		rows = rows[:ownershipRowLimit]
	}
	return rows, total, nil
}

// cannotActTotal is the true count behind cannotActRows.
func (s *SQLStore) cannotActTotal(ctx context.Context, nonEligible []string) (int, error) {
	inClause := placeholders(len(nonEligible))
	args := anySlice(nonEligible)

	var n int
	query := `SELECT
		(SELECT COUNT(*) FROM asset a JOIN team t ON t.id = a.team_id
		  WHERE t.lifecycle IN (` + inClause + `) AND a.lifecycle <> ?) +
		(SELECT COUNT(*) FROM service s JOIN team t ON t.id = s.team_id
		  WHERE t.lifecycle IN (` + inClause + `) AND s.lifecycle <> ?) +
		(SELECT COUNT(*) FROM project p JOIN team t ON t.id = p.team_id
		  WHERE t.lifecycle IN (` + inClause + `) AND p.lifecycle <> ?) +
		(SELECT COUNT(*) FROM identity i JOIN team t ON t.id = i.team_id
		  WHERE t.lifecycle IN (` + inClause + `) AND i.lifecycle <> ?) +
		(SELECT COUNT(*) FROM custom_field cf JOIN team t ON t.id = cf.owner_team_id
		  WHERE t.lifecycle IN (` + inClause + `) AND cf.retired_at IS NULL)`

	var fullArgs []any
	fullArgs = append(fullArgs, append(append([]any{}, args...), domain.LifecycleRetired)...)
	fullArgs = append(fullArgs, append(append([]any{}, args...), domain.LifecycleRetired)...)
	fullArgs = append(fullArgs, append(append([]any{}, args...), domain.LifecycleRetired)...)
	fullArgs = append(fullArgs, append(append([]any{}, args...), domain.LifecycleRetired)...)
	fullArgs = append(fullArgs, args...)

	if err := s.readOne(ctx, &n, query, fullArgs...); err != nil {
		return 0, fmt.Errorf("counting entities whose owner cannot act: %w", err)
	}
	return n, nil
}

// noContactTeams is finding 3: eligible teams with no contact_ref that own at
// least one live entity. One row per team (design §2).
//
// EligibleTeamLifecycles() names the one lifecycle worth flagging here --
// today only `active`, but read from the classification rather than
// hard-coded, matching cannotActRows's own rule. Ownership counts use the
// same correlated-subquery shape teamSelect already uses; that shape is safe
// here because it filters on team_id equality, which idx_asset_team and its
// siblings (migration 00016) already cover as a leading column.
func (s *SQLStore) noContactTeams(ctx context.Context) ([]NoContactTeam, error) {
	eligible := domain.EligibleTeamLifecycles()
	inClause := placeholders(len(eligible))

	// Placeholder order follows the query text top to bottom: the four
	// per-entity lifecycle exclusions in the SELECT list come first, then the
	// team-lifecycle IN list in the WHERE clause.
	var args []any
	args = append(args, domain.LifecycleRetired, domain.LifecycleRetired,
		domain.LifecycleRetired, domain.LifecycleRetired)
	args = append(args, anySlice(eligible)...)

	var rows []NoContactTeam
	if err := s.read(ctx, &rows, `
		SELECT t.id AS team_id, t.code AS team_code, t.name AS team_name,
		       (SELECT COUNT(*) FROM asset a WHERE a.team_id = t.id AND a.lifecycle <> ?) AS asset_count,
		       (SELECT COUNT(*) FROM service s WHERE s.team_id = t.id AND s.lifecycle <> ?) AS service_count,
		       (SELECT COUNT(*) FROM project p WHERE p.team_id = t.id AND p.lifecycle <> ?) AS project_count,
		       (SELECT COUNT(*) FROM identity i WHERE i.team_id = t.id AND i.lifecycle <> ?) AS identity_count,
		       (SELECT COUNT(*) FROM custom_field cf WHERE cf.owner_team_id = t.id AND cf.retired_at IS NULL) AS custom_field_count
		FROM team t
		WHERE t.lifecycle IN (`+inClause+`)
		  AND (t.contact_ref IS NULL OR t.contact_ref = '')
		ORDER BY t.code`, args...); err != nil {
		return nil, fmt.Errorf("listing teams with no contact: %w", err)
	}

	// Only a team that actually owns something is a finding -- an
	// unreachable team with nothing to its name has nothing at stake here.
	out := rows[:0]
	for _, r := range rows {
		if r.Total() > 0 {
			out = append(out, r)
		}
	}
	return out, nil
}
