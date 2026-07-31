package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gabriel/invctl/internal/domain"
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
}

const teamSelect = `
	SELECT t.*,
	       (SELECT COUNT(*) FROM asset a
	         WHERE a.team_id = t.id AND a.lifecycle <> 'retired')   AS asset_count,
	       (SELECT COUNT(*) FROM service s
	         WHERE s.team_id = t.id AND s.lifecycle <> 'retired')   AS service_count,
	       (SELECT COUNT(*) FROM project p
	         WHERE p.team_id = t.id AND p.lifecycle <> 'retired')   AS project_count
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
		_, err := tx.exec(ctx, `
			UPDATE team SET code = ?, name = ?, description = ?, contact_ref = ?,
			                lifecycle = ?, updated_at = ?
			WHERE id = ?`,
			t.Code, t.Name, t.Description, t.ContactRef, t.Lifecycle, t.UpdatedAt, t.ID)
		if err != nil {
			return translateWriteErr(err, "updating team")
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
			`UPDATE team SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring team")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`,
			before.Lifecycle, domain.LifecycleRetired)
		return tx.log(ctx, "team", id, domain.ActionRetire, diff)
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

// ResponsibilityRoles lists the capacities a team can hold. Descriptive: nothing
// branches on the value, so an estate that wants `dpo` adds one as data.
func (s *SQLStore) ResponsibilityRoles(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabResponsibilityRole)
}
