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

// Teams: who looks after part of the estate.
//
// Shaped like projects, because the two answer adjacent questions and a reader
// who has learned one page should not have to learn the other. They are separate
// axes on purpose: a PROJECT says what a thing is for, a TEAM says who looks
// after it, and the same box routinely has different answers to each.

// TeamRow is a team plus what a list view needs without a query per row.
type TeamRow struct {
	domain.Team
	// What it is answerable for. Counted rather than listed, because the list
	// belongs on the detail page and the count is what makes a team worth
	// clicking.
	AssetCount   int `db:"asset_count"`
	ServiceCount int `db:"service_count"`
	ProjectCount int `db:"project_count"`
	// A team renews certificates, and an expiring one is the most urgent thing
	// it can be answerable for -- so the count belongs beside the others.
	CertificateCount int `db:"certificate_count"`
}

const teamSelect = `
	SELECT t.*,
	       (SELECT COUNT(*) FROM asset a
	         WHERE a.team_id = t.id AND a.lifecycle <> 'retired')   AS asset_count,
	       (SELECT COUNT(*) FROM service s
	         WHERE s.team_id = t.id AND s.lifecycle <> 'retired')   AS service_count,
	       (SELECT COUNT(*) FROM project p
	         WHERE p.team_id = t.id AND p.lifecycle <> 'retired')   AS project_count,
	       (SELECT COUNT(*) FROM certificate c
	         WHERE c.team_id = t.id AND c.lifecycle <> 'retired')   AS certificate_count
	FROM team t`

// TeamFilter narrows a team list.
type TeamFilter struct {
	Query          string
	IncludeRetired bool
}

// ListTeams returns teams matching the filter.
func (s *SQLStore) ListTeams(ctx context.Context, f TeamFilter) ([]TeamRow, error) {
	var where []string
	var args []any
	if !f.IncludeRetired {
		where = append(where, `t.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	if f.Query != "" {
		// LOWER on both sides: SQLite's LIKE is case-insensitive for ASCII and
		// PostgreSQL's is not. ListProjects was the one filter that missed this
		// and a review caught it; not repeating the mistake here.
		where = append(where, `(LOWER(t.code) LIKE ? ESCAPE '\' OR LOWER(t.name) LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(lower(f.Query)) + "%"
		args = append(args, like, like)
	}

	query := teamSelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY t.code`

	var rows []TeamRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}
	if f.Query != "" {
		sort.SliceStable(rows, rankNames(f.Query, func(i int) string { return rows[i].Code }))
	}
	return rows, nil
}

// TeamOptions is the picker: who exists, and nothing about what they own.
//
// ListTeams carries three correlated subquery counts across asset, service and
// project — measured at 112 ms and 88,687 buffer hits on a 200k-asset estate,
// to render 27 rows. responsibilityOptions was calling it on every asset form,
// every service form and every project list, for a <select> that renders two
// strings per row. Found by a database review.
//
// Retired teams are excluded here and only here: a picker offers what somebody
// may choose NOW, while every list and detail view still shows a disbanded team
// beside what it used to look after, because that is a finding.
func (s *SQLStore) TeamOptions(ctx context.Context) ([]TeamRow, error) {
	var rows []TeamRow
	if err := s.read(ctx, &rows,
		`SELECT * FROM team WHERE lifecycle <> ? ORDER BY code`,
		domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing teams for a picker: %w", err)
	}
	return rows, nil
}

// GetTeam loads one team.
func (s *SQLStore) GetTeam(ctx context.Context, id string) (*TeamRow, error) {
	var row TeamRow
	if err := s.readOne(ctx, &row, teamSelect+` WHERE t.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting team %s: %w", id, err)
	}
	return &row, nil
}

// CreateTeam inserts a team.
func (s *SQLStore) CreateTeam(ctx context.Context, actor domain.Actor, t *domain.Team) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	t.RowVersion = 1
	if err := t.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(tx *tx) error {
		_, err := tx.exec(ctx, `
			INSERT INTO team (id, code, name, description, contact_ref, lifecycle,
			                  created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Code, t.Name, t.Description, t.ContactRef, t.Lifecycle,
			t.CreatedAt, t.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating team")
		}
		if err := tx.logCreate(ctx, "team", t.ID, t); err != nil {
			return err
		}
		return s.indexTeam(ctx, tx, t)
	})
}

// UpdateTeam persists field changes.
func (s *SQLStore) UpdateTeam(ctx context.Context, actor domain.Actor, t *domain.Team) error {
	if err := t.Validate(); err != nil {
		return err
	}
	before, err := s.GetTeam(ctx, t.ID)
	if err != nil {
		return err
	}
	t.CreatedAt = before.CreatedAt
	t.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(tx *tx) error {
		res, err := tx.exec(ctx, `
			UPDATE team SET code = ?, name = ?, description = ?, contact_ref = ?,
			                lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			t.Code, t.Name, t.Description, t.ContactRef, t.Lifecycle, t.UpdatedAt,
			t.ID, t.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating team")
		}
		if err := requireVersion(res, "team", t.ID, &t.RowVersion); err != nil {
			return err
		}
		if err := tx.logUpdate(ctx, "team", t.ID, &before.Team, t); err != nil {
			return err
		}
		return s.indexTeam(ctx, tx, t)
	})
}

// RetireTeam disbands a team.
//
// It does NOT clear the team from what it looked after. A retired team is how
// the estate says "this used to be theirs and nobody has picked it up", which is
// a finding; silently nulling the column would erase the question along with the
// answer. The pages render a retired team plainly so the gap is visible.
func (s *SQLStore) RetireTeam(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetTeam(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(tx *tx) error {
		if _, err := tx.exec(ctx,
			`UPDATE team SET lifecycle = ?, updated_at = ?,
			                 row_version = row_version + 1 WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring team")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`,
			before.Lifecycle, domain.LifecycleRetired)
		if err := tx.log(ctx, "team", id, domain.ActionRetire, diff, ""); err != nil {
			return err
		}
		// Reindexed, which retirement elsewhere in this codebase does not need
		// to do. A team's search document carries its contact, and search_index
		// holds only the CURRENT value -- that is the property that makes an
		// erasure request answerable by editing the team rather than by
		// rewriting history. A retired team left in the index would keep its
		// last contact discoverable with no screen offering to change it.
		// Found by a security review.
		retired := before.Team
		retired.Lifecycle = domain.LifecycleRetired
		retired.ContactRef = nil
		return s.indexTeam(ctx, tx, &retired)
	})
}

// indexTeam makes a team findable by code, name and contact.
//
// The contact goes in the body on purpose: "who is platform@example.com" is a
// question people arrive with, holding a header from an alert they did not
// recognise. It is a group address by rule, so nothing personal is indexed.
func (s *SQLStore) indexTeam(ctx context.Context, t *tx, team *domain.Team) error {
	body := ""
	if team.ContactRef != nil {
		body = *team.ContactRef
	}
	return s.indexEntity(ctx, t, searchDoc{
		EntityType: "team", EntityID: team.ID,
		Title: team.Name, Subtitle: team.Code, Body: body,
	})
}

// requireRole checks a manager role against its lookup table before the write.
//
// manager_role was the only lookup-backed column in the codebase that did not
// do this -- kind, form_factor, role, data_class, engine and cost_kind all do.
// Without it an unknown value reached the foreign key, and SQLite reports that
// as "FOREIGN KEY constraint failed" with no column in it, so translateWriteErr
// can only turn it into a bare 422 with no field highlighted and the form
// contents lost. That is precisely the failure requireVocabulary exists to
// prevent. Found by a database review.
func requireRole(ctx context.Context, t *tx, role *string) error {
	if role == nil || *role == "" {
		return nil
	}
	return t.requireVocabulary(ctx, vocabResponsibilityRole, "manager_role", *role)
}

// ResponsibilityRoles lists the capacities a team can hold. Descriptive: nothing
// branches on the value, so an estate that wants `dpo` adds one as data.
func (s *SQLStore) ResponsibilityRoles(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabResponsibilityRole)
}
