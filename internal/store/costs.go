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
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
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
	costOnAsset   = costTable{name: "asset_cost", column: "asset_id", entity: "asset_cost", parent: "asset", catalogued: true, scoped: true}
	costOnService = costTable{name: "service_cost", column: "service_id", entity: "service_cost", parent: "service"}
	costOnProject = costTable{name: "project_cost", column: "project_id", entity: "project_cost"}
	// A circuit's life is its CONTRACT, not an end of support -- so a one-off
	// install fee amortises to the day the contract ends, which is the honest
	// horizon for money spent to get it working.
	costOnCircuit = costTable{name: "circuit_cost", column: "circuit_id", entity: "circuit_cost",
		parent: "circuit", eolColumn: "contract_end"}
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
	// eolColumn is the parent's column holding the date a one-off amortises to.
	// Empty means "eol_date", which is what assets and services call it; a
	// circuit calls it contract_end because that is what it is.
	eolColumn string
	// catalogued means the parent can name a device type, and therefore that its
	// end-of-support may be INHERITED rather than typed on the row. True only
	// for assets: a service has a date or it has none.
	catalogued bool
	// scoped means the line can declare which consumers it applies to
	// (migration 00047). Only asset costs can: a cluster's shared cost is the
	// lines on its member hosts, and that is the only pool anything divides.
	// A service, project or circuit cost attaches to something that is already
	// the unit of attribution, so the column would have no reader there.
	scoped bool
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
		column := t.eolColumn
		if column == "" {
			column = "eol_date"
		}
		eol = `p.` + column + ` AS owner_eol_date`
		join = `
		LEFT JOIN ` + t.parent + ` p ON p.id = c.` + t.column
	}
	// The SAME override rule domain.ResolveEOL states, expressed once more here
	// because amortisation happens in SQL and cannot call it.
	//
	// It is not a refinement. Reading only the asset's own column means every
	// box whose support date is INHERITED from its model amortises a one-off
	// over no life at all and reports EUR 0.00 -- and a catalogue exists exactly
	// so that most boxes inherit. The report was silently at its most wrong on
	// the estates that used the feature properly.
	if t.catalogued {
		eol = `COALESCE(p.eol_date, dt.eol_date) AS owner_eol_date`
		join += `
		LEFT JOIN device_type dt ON dt.id = p.device_type_id`
	}
	// Read as a literal for the three unscoped tables rather than left out of
	// the struct: a caller reading a service cost gets `universal`, which is
	// true of it, instead of a zero value that would fail the enum check on
	// the way back in.
	appliesTo := `'` + domain.CostUniversal + `' AS applies_to`
	if t.scoped {
		appliesTo = `c.applies_to`
	}
	return `
		SELECT c.id, c.` + t.column + ` AS owner_id, c.kind, c.period, c.amount_minor,
		       c.note, c.valid_from, c.valid_until, c.lifecycle, c.created_at, c.updated_at,
		       c.row_version, c.provider_id, ` + appliesTo + `,
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
	for _, chunk := range chunkIDs(ownerIDs) {
		var rows []CostRow
		query := t.selectSQL() + ` WHERE c.` + t.column + ` IN (` + placeholders(len(chunk)) + `)
			AND c.lifecycle = ? ORDER BY c.` + t.column + `, c.kind, c.id`
		args := append(anySlice(chunk), domain.LifecycleActive)
		if err := s.read(ctx, &rows, query, args...); err != nil {
			return nil, fmt.Errorf("loading %s: %w", t.name, err)
		}
		for _, r := range rows {
			out[r.OwnerID] = append(out[r.OwnerID], r)
		}
	}
	return out, nil
}

// AddAssetCost attaches a cost line to an asset.
func (s *SQLStore) AddAssetCost(ctx context.Context, p domain.Permit, assetID string, c *domain.Cost) error {
	return s.addCost(ctx, p, costOnAsset, assetID, c)
}

// AddServiceCost attaches a cost line to a service.
func (s *SQLStore) AddServiceCost(ctx context.Context, p domain.Permit, serviceID string, c *domain.Cost) error {
	return s.addCost(ctx, p, costOnService, serviceID, c)
}

// AddProjectCost attaches a cost line to a project directly.
func (s *SQLStore) AddProjectCost(ctx context.Context, p domain.Permit, projectID string, c *domain.Cost) error {
	return s.addCost(ctx, p, costOnProject, projectID, c)
}

func (s *SQLStore) addCost(ctx context.Context, p domain.Permit, t costTable, ownerID string, c *domain.Cost) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	c.RowVersion = 1
	if err := c.Validate(); err != nil {
		return err
	}
	return s.write(ctx, p, func(tx *tx) error {
		if err := tx.requireVocabulary(ctx, vocabCostKind, "kind", c.Kind); err != nil {
			return err
		}
		columns, values := "", ""
		args := []any{c.ID, ownerID, c.Kind, c.Period, c.AmountMinor, c.Note,
			c.ValidFrom, c.ValidUntil, c.Lifecycle, c.CreatedAt, c.UpdatedAt,
			c.ProviderID}
		if t.scoped {
			columns, values = ", applies_to", ", ?"
			args = append(args, c.AppliesTo)
		}
		_, err := tx.exec(ctx, `
			INSERT INTO `+t.name+` (id, `+t.column+`, kind, period, amount_minor, note,
			                        valid_from, valid_until, lifecycle, created_at, updated_at,
			                        provider_id`+columns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`+values+`)`, args...)
		if err != nil {
			return translateWriteErr(err, "adding a cost line")
		}
		return tx.logCreate(ctx, t.entity, c.ID, c)
	})
}

// UpdateAssetCost edits a line on an asset.
func (s *SQLStore) UpdateAssetCost(ctx context.Context, p domain.Permit, c *domain.Cost) error {
	return s.updateCost(ctx, p, costOnAsset, c)
}

// UpdateServiceCost edits a line on a service.
func (s *SQLStore) UpdateServiceCost(ctx context.Context, p domain.Permit, c *domain.Cost) error {
	return s.updateCost(ctx, p, costOnService, c)
}

// UpdateProjectCost edits a line on a project.
func (s *SQLStore) UpdateProjectCost(ctx context.Context, p domain.Permit, c *domain.Cost) error {
	return s.updateCost(ctx, p, costOnProject, c)
}

func (s *SQLStore) updateCost(ctx context.Context, p domain.Permit, t costTable, c *domain.Cost) error {
	if err := c.Validate(); err != nil {
		return err
	}
	before, err := s.getCost(ctx, t, c.ID)
	if err != nil {
		return err
	}
	// HISTORY IS NOT AMENDABLE. A retired line records a figure that was
	// withdrawn, and both the figure and the withdrawal are facts somebody may
	// be reading. Amending one silently rewrites what the estate cost in a
	// period that is already closed -- and the same call could set lifecycle
	// back to active, un-retiring it with no trace of a retirement.
	//
	// Here rather than in the handler so that a second caller cannot miss it:
	// the UI hides the button, which stops nobody who can type a URL. Correct a
	// retired line by adding the right one, the way change_log is corrected by
	// a further entry.
	if before.Lifecycle == domain.LifecycleRetired {
		return fmt.Errorf("cost line %s is retired and cannot be amended: %w", c.ID, domain.ErrConflict)
	}
	c.CreatedAt = before.CreatedAt
	c.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, p, func(tx *tx) error {
		if err := tx.requireVocabulary(ctx, vocabCostKind, "kind", c.Kind); err != nil {
			return err
		}
		scope := ""
		args := []any{c.Kind, c.Period, c.AmountMinor, c.Note,
			c.ValidFrom, c.ValidUntil, c.Lifecycle, c.UpdatedAt, c.ProviderID}
		if t.scoped {
			scope = "applies_to = ?, "
			args = append(args, c.AppliesTo)
		}
		args = append(args, c.ID, c.RowVersion)
		res, err := tx.exec(ctx, `
			UPDATE `+t.name+` SET kind = ?, period = ?, amount_minor = ?, note = ?,
			                      valid_from = ?, valid_until = ?, lifecycle = ?, updated_at = ?,
			                      provider_id = ?,
			                      `+scope+`row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, args...)
		if err != nil {
			return translateWriteErr(err, "updating a cost line")
		}
		if err := requireVersion(res, t.entity, c.ID, &c.RowVersion); err != nil {
			return err
		}
		return tx.logUpdate(ctx, t.entity, c.ID, &before.Cost, c)
	})
}

// RetireAssetCost soft-deletes a line on an asset. ownerID is the asset the
// caller believes it belongs to; a mismatch is a 404 rather than a retirement.
func (s *SQLStore) RetireAssetCost(ctx context.Context, p domain.Permit, ownerID, id string) error {
	return s.retireCost(ctx, p, costOnAsset, ownerID, id)
}

// RetireServiceCost soft-deletes a line on a service.
func (s *SQLStore) RetireServiceCost(ctx context.Context, p domain.Permit, ownerID, id string) error {
	return s.retireCost(ctx, p, costOnService, ownerID, id)
}

// RetireProjectCost soft-deletes a line on a project.
func (s *SQLStore) RetireProjectCost(ctx context.Context, p domain.Permit, ownerID, id string) error {
	return s.retireCost(ctx, p, costOnProject, ownerID, id)
}

// The circuit surface. Identical wrappers to the other three, deliberately:
// the rollup, the validity windows and the amendment behaviour are the shared
// machinery, and a circuit's monthly rate is not a different kind of money.
//
// Detached from the declaration by a blank line on purpose: it describes the
// four methods below, not the first one. Attached, it becomes ListCircuitCosts'
// doc comment and reads as though the function were named after a paragraph.

func (s *SQLStore) ListCircuitCosts(ctx context.Context, circuitID string) ([]CostRow, error) {
	return s.listCosts(ctx, costOnCircuit, circuitID)
}

func (s *SQLStore) AddCircuitCost(ctx context.Context, p domain.Permit, circuitID string, c *domain.Cost) error {
	return s.addCost(ctx, p, costOnCircuit, circuitID, c)
}

func (s *SQLStore) UpdateCircuitCost(ctx context.Context, p domain.Permit, c *domain.Cost) error {
	return s.updateCost(ctx, p, costOnCircuit, c)
}

func (s *SQLStore) RetireCircuitCost(ctx context.Context, p domain.Permit, ownerID, id string) error {
	return s.retireCost(ctx, p, costOnCircuit, ownerID, id)
}

// retireCost soft-deletes, like every other entity here.
//
// A cost that stopped being paid is not a cost that never existed, and deleting
// the row would silently rewrite what last quarter cost. Closing the validity
// window is the other correct answer and is what an operator should reach for
// when a contract ends on a known date; retiring is for a line that should never
// have been entered.
//
// The line must belong to ownerID. Without that check the route
// /assets/{id}/costs/{costID}/retire ignores {id} entirely, so an admin can
// retire a cost on one asset through a URL naming another -- the change_log
// entry would be correct while the redirect and the operator's belief about
// what they just did would not. Found by a security review.
func (s *SQLStore) retireCost(ctx context.Context, p domain.Permit, t costTable, ownerID, id string) error {
	before, err := s.getCost(ctx, t, id)
	if err != nil {
		return err
	}
	if before.OwnerID != ownerID {
		return fmt.Errorf("cost line %s does not belong to %s: %w", id, ownerID, domain.ErrNotFound)
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, p, func(tx *tx) error {
		if _, err := tx.exec(ctx,
			`UPDATE `+t.name+` SET lifecycle = ?, updated_at = ?,
			                       row_version = row_version + 1 WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring a cost line")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`,
			before.Lifecycle, domain.LifecycleRetired)
		return tx.log(ctx, t.entity, id, domain.ActionRetire, diff, "")
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

// GetCircuitCost loads one line for the edit path.
func (s *SQLStore) GetCircuitCost(ctx context.Context, id string) (*CostRow, error) {
	return s.getCost(ctx, costOnCircuit, id)
}

// Which consumers a cost line applies to (migration 00047, §5.6).
//
// SEPARATE FROM UpdateAssetCost because the set is not a field. Folding it into
// the update would mean every caller that edits an amount has to carry the
// consumer list or silently clear it -- the exact shape that has cost this
// codebase its audit trail three times. A caller that does not mention
// consumers does not change them.

// CostConsumers returns the assets a line applies to, by id.
func (s *SQLStore) CostConsumers(ctx context.Context, costID string) ([]string, error) {
	var ids []string
	if err := s.read(ctx, &ids, `
		SELECT c.asset_id
		FROM asset_cost_consumer c
		JOIN asset a ON a.id = c.asset_id
		WHERE c.cost_id = ? AND a.lifecycle <> ?
		ORDER BY a.name`, costID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading the consumers of cost %s: %w", costID, err)
	}
	return ids, nil
}

// SetCostConsumers replaces which assets a line applies to.
func (s *SQLStore) SetCostConsumers(ctx context.Context, p domain.Permit,
	costID string, assetIDs []string) error {

	before, err := s.GetAssetCost(ctx, costID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.Now())
	return s.write(ctx, p, func(t *tx) error {
		beforeAudit, err := costScopeAudit(ctx, t, &before.Cost)
		if err != nil {
			return err
		}
		if _, err := t.exec(ctx,
			`DELETE FROM asset_cost_consumer WHERE cost_id = ?`, costID); err != nil {
			return translateWriteErr(err, "clearing the consumers of a cost line")
		}
		for _, id := range assetIDs {
			if _, err := t.exec(ctx,
				`INSERT INTO asset_cost_consumer (cost_id, asset_id, created_at)
				 VALUES (?, ?, ?)`, costID, id, at); err != nil {
				return translateWriteErr(err, "naming the consumers of a cost line")
			}
		}
		afterAudit, err := costScopeAudit(ctx, t, &before.Cost)
		if err != nil {
			return err
		}
		return t.logUpdate(ctx, "asset_cost", costID, beforeAudit, afterAudit)
	})
}

// costScopeAudit is the audited shape when the consumer set changes: the line
// plus the names it applies to.
//
// Names rather than ids, because an audit entry is read by people -- the same
// choice auditedAsset makes for environment codes.
type scopedCostAudit struct {
	domain.Cost
	Consumers string `db:"consumers"`
}

// costScopeAudit reads the audited value inside the transaction, because the
// reader pool cannot see this transaction's uncommitted writes and the "after"
// snapshot would otherwise be identical to the "before" one.
func costScopeAudit(ctx context.Context, t *tx, c *domain.Cost) (*scopedCostAudit, error) {
	var names []string
	if err := t.selectAll(ctx, &names, `
		SELECT a.name
		FROM asset_cost_consumer cc
		JOIN asset a ON a.id = cc.asset_id
		WHERE cc.cost_id = ?
		ORDER BY a.name, a.id`, c.ID); err != nil {
		return nil, fmt.Errorf("reading consumers for the audit trail: %w", err)
	}
	return &scopedCostAudit{Cost: *c, Consumers: strings.Join(names, ", ")}, nil
}
