package store

import (
	"context"
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
)

// Cost lines against assets, services and projects.
//
// THREE PARENT TABLES, ONE IMPLEMENTATION. asset_cost, service_cost and
// project_cost have identical shapes and different foreign keys, so the SQL is
// generated from a table/column pair that comes from a CONSTANT in this file and
// never from a request. That is the same construction projectsFor uses, and it
// buys real referential integrity: a polymorphic entity_id would let a typo in
// an import invent a row that inflates a total and belongs to nothing.
//
// EVERY MUTATION IS AUDITED. A cost line is declared state -- somebody read an
// invoice and typed it -- so create, update and retire each write a change_log
// row in the same transaction, under its own entity type. A cost line is not a
// set replaced wholesale like asset_environment; it has an id and a lifecycle of
// its own, so it is audited the way a dependency is rather than folded into its
// parent's diff.

// The three cost surfaces. Values, not strings from a caller.
var (
	costOnAsset   = costTable{name: "asset_cost", column: "asset_id", entity: "asset_cost", parent: "asset"}
	costOnService = costTable{name: "service_cost", column: "service_id", entity: "service_cost", parent: "service"}
	costOnProject = costTable{name: "project_cost", column: "project_id", entity: "project_cost"}
)

type costTable struct {
	name   string
	column string
	entity string // change_log entity_type
	// parent is the table holding the thing this cost is attached to, joined
	// only to read its eol_date. Empty for project_cost: a project has no
	// end-of-support, so a one-off attached to one can never be amortised --
	// which is correct. A setup fee for a SaaS subscription bought nothing with
	// a life.
	parent string
}

// CostRow is a cost line with the label of its kind resolved, so a list view
// renders without a query per row.
type CostRow struct {
	domain.Cost
	OwnerID   string `db:"owner_id"`
	KindLabel string `db:"kind_label"`
	// OwnerEOLDate is the end-of-support of the thing this cost is attached to,
	// carried here so a one-off can be amortised over its life without a second
	// query per row. Always nil for a project cost.
	OwnerEOLDate *string `db:"owner_eol_date"`
}

func (t costTable) selectSQL() string {
	eol, join := `NULL AS owner_eol_date`, ``
	if t.parent != "" {
		eol = `p.eol_date AS owner_eol_date`
		join = `
		LEFT JOIN ` + t.parent + ` p ON p.id = c.` + t.column
	}
	return `
		SELECT c.id, c.` + t.column + ` AS owner_id, c.kind, c.period, c.amount_minor,
		       c.note, c.valid_from, c.valid_until, c.lifecycle, c.created_at, c.updated_at,
		       COALESCE(k.label, c.kind) AS kind_label,
		       ` + eol + `
		FROM ` + t.name + ` c
		LEFT JOIN cost_kind k ON k.code = c.kind` + join
}

// ListAssetCosts returns every cost line on an asset, retired ones included.
//
// Retired lines are returned rather than filtered because the caller that
// renders them wants to show them struck through, and the callers that total
// them go through AppliesOn, which excludes retired. A store method that
// silently dropped them would make "why is this total wrong" unanswerable from
// the page.
func (s *SQLStore) ListAssetCosts(ctx context.Context, assetID string) ([]CostRow, error) {
	return s.listCosts(ctx, costOnAsset, assetID)
}

// ListServiceCosts is the same for a service.
func (s *SQLStore) ListServiceCosts(ctx context.Context, serviceID string) ([]CostRow, error) {
	return s.listCosts(ctx, costOnService, serviceID)
}

// ListProjectCosts returns the lines attached to the project itself -- the money
// that belongs to no box and no service anybody here runs.
func (s *SQLStore) ListProjectCosts(ctx context.Context, projectID string) ([]CostRow, error) {
	return s.listCosts(ctx, costOnProject, projectID)
}

func (s *SQLStore) listCosts(ctx context.Context, t costTable, ownerID string) ([]CostRow, error) {
	var rows []CostRow
	query := t.selectSQL() + ` WHERE c.` + t.column + ` = ?
		ORDER BY c.lifecycle, c.valid_from DESC, c.kind, c.id`
	if err := s.read(ctx, &rows, query, ownerID); err != nil {
		return nil, fmt.Errorf("listing %s for %s: %w", t.name, ownerID, err)
	}
	return rows, nil
}

// costsFor loads every line for a set of owners at once, keyed by owner. The
// rollup needs hundreds of these and a query per entity would be the classic
// N+1 on a page a manager refreshes.
func (s *SQLStore) costsFor(ctx context.Context, t costTable, ownerIDs []string) (map[string][]CostRow, error) {
	out := map[string][]CostRow{}
	if len(ownerIDs) == 0 {
		return out, nil
	}
	var rows []CostRow
	query := t.selectSQL() + ` WHERE c.` + t.column + ` IN (` + placeholders(len(ownerIDs)) + `)
		AND c.lifecycle = ? ORDER BY c.` + t.column + `, c.kind, c.id`
	args := append(anySlice(ownerIDs), domain.LifecycleActive)
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("loading %s: %w", t.name, err)
	}
	for _, r := range rows {
		out[r.OwnerID] = append(out[r.OwnerID], r)
	}
	return out, nil
}

// AddAssetCost attaches a cost line to an asset.
func (s *SQLStore) AddAssetCost(ctx context.Context, actor domain.Actor, assetID string, c *domain.Cost) error {
	return s.addCost(ctx, actor, costOnAsset, assetID, c)
}

// AddServiceCost attaches a cost line to a service.
func (s *SQLStore) AddServiceCost(ctx context.Context, actor domain.Actor, serviceID string, c *domain.Cost) error {
	return s.addCost(ctx, actor, costOnService, serviceID, c)
}

// AddProjectCost attaches a cost line to a project directly.
func (s *SQLStore) AddProjectCost(ctx context.Context, actor domain.Actor, projectID string, c *domain.Cost) error {
	return s.addCost(ctx, actor, costOnProject, projectID, c)
}

func (s *SQLStore) addCost(ctx context.Context, actor domain.Actor, t costTable, ownerID string, c *domain.Cost) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(tx *tx) error {
		if err := tx.requireVocabulary(ctx, vocabCostKind, "kind", c.Kind); err != nil {
			return err
		}
		_, err := tx.exec(ctx, `
			INSERT INTO `+t.name+` (id, `+t.column+`, kind, period, amount_minor, note,
			                        valid_from, valid_until, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, ownerID, c.Kind, c.Period, c.AmountMinor, c.Note,
			c.ValidFrom, c.ValidUntil, c.Lifecycle, c.CreatedAt, c.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "adding a cost line")
		}
		return tx.logCreate(ctx, t.entity, c.ID, c)
	})
}

// UpdateAssetCost edits a line on an asset.
func (s *SQLStore) UpdateAssetCost(ctx context.Context, actor domain.Actor, c *domain.Cost) error {
	return s.updateCost(ctx, actor, costOnAsset, c)
}

// UpdateServiceCost edits a line on a service.
func (s *SQLStore) UpdateServiceCost(ctx context.Context, actor domain.Actor, c *domain.Cost) error {
	return s.updateCost(ctx, actor, costOnService, c)
}

// UpdateProjectCost edits a line on a project.
func (s *SQLStore) UpdateProjectCost(ctx context.Context, actor domain.Actor, c *domain.Cost) error {
	return s.updateCost(ctx, actor, costOnProject, c)
}

func (s *SQLStore) updateCost(ctx context.Context, actor domain.Actor, t costTable, c *domain.Cost) error {
	if err := c.Validate(); err != nil {
		return err
	}
	before, err := s.getCost(ctx, t, c.ID)
	if err != nil {
		return err
	}
	c.CreatedAt = before.CreatedAt
	c.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(tx *tx) error {
		if err := tx.requireVocabulary(ctx, vocabCostKind, "kind", c.Kind); err != nil {
			return err
		}
		_, err := tx.exec(ctx, `
			UPDATE `+t.name+` SET kind = ?, period = ?, amount_minor = ?, note = ?,
			                      valid_from = ?, valid_until = ?, lifecycle = ?, updated_at = ?
			WHERE id = ?`,
			c.Kind, c.Period, c.AmountMinor, c.Note,
			c.ValidFrom, c.ValidUntil, c.Lifecycle, c.UpdatedAt, c.ID)
		if err != nil {
			return translateWriteErr(err, "updating a cost line")
		}
		return tx.logUpdate(ctx, t.entity, c.ID, &before.Cost, c)
	})
}

// RetireAssetCost soft-deletes a line on an asset.
func (s *SQLStore) RetireAssetCost(ctx context.Context, actor domain.Actor, id string) error {
	return s.retireCost(ctx, actor, costOnAsset, id)
}

// RetireServiceCost soft-deletes a line on a service.
func (s *SQLStore) RetireServiceCost(ctx context.Context, actor domain.Actor, id string) error {
	return s.retireCost(ctx, actor, costOnService, id)
}

// RetireProjectCost soft-deletes a line on a project.
func (s *SQLStore) RetireProjectCost(ctx context.Context, actor domain.Actor, id string) error {
	return s.retireCost(ctx, actor, costOnProject, id)
}

// retireCost soft-deletes, like every other entity here.
//
// A cost that stopped being paid is not a cost that never existed, and deleting
// the row would silently rewrite what last quarter cost. Closing the validity
// window is the other correct answer and is what an operator should reach for
// when a contract ends on a known date; retiring is for a line that should never
// have been entered.
func (s *SQLStore) retireCost(ctx context.Context, actor domain.Actor, t costTable, id string) error {
	before, err := s.getCost(ctx, t, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(tx *tx) error {
		if _, err := tx.exec(ctx,
			`UPDATE `+t.name+` SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring a cost line")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`,
			before.Lifecycle, domain.LifecycleRetired)
		return tx.log(ctx, t.entity, id, domain.ActionRetire, diff)
	})
}

// GetAssetCost loads one line on an asset.
func (s *SQLStore) GetAssetCost(ctx context.Context, id string) (*CostRow, error) {
	return s.getCost(ctx, costOnAsset, id)
}

// GetServiceCost loads one line on a service.
func (s *SQLStore) GetServiceCost(ctx context.Context, id string) (*CostRow, error) {
	return s.getCost(ctx, costOnService, id)
}

// GetProjectCost loads one line on a project.
func (s *SQLStore) GetProjectCost(ctx context.Context, id string) (*CostRow, error) {
	return s.getCost(ctx, costOnProject, id)
}

func (s *SQLStore) getCost(ctx context.Context, t costTable, id string) (*CostRow, error) {
	var row CostRow
	if err := s.readOne(ctx, &row, t.selectSQL()+` WHERE c.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting cost line %s: %w", id, err)
	}
	return &row, nil
}

// TotalCosts folds a set of lines into the totals, counting only what is in
// force on the given date and amortising each one-off over the life of the thing
// it bought.
func TotalCosts(rows []CostRow, on string) domain.CostTotals {
	var totals domain.CostTotals
	for i := range rows {
		if rows[i].AppliesOn(on) {
			totals.Add(&rows[i].Cost, rows[i].OwnerEOLDate, on)
		}
	}
	return totals
}
