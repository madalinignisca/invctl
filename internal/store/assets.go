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

// ---------- environments ----------

// CreateEnvironment inserts an environment and its audit row.
func (s *SQLStore) CreateEnvironment(ctx context.Context, actor domain.Actor, env *domain.Environment) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	env.RowVersion = 1
	return s.write(ctx, actor, func(t *tx) error {
		if err := t.requireVocabulary(ctx, vocabEnvironmentRole, "role", env.Role); err != nil {
			return err
		}
		_, err := t.exec(ctx,
			`INSERT INTO environment (id, code, name, role, in_scope, criticality, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			env.ID, env.Code, env.Name, env.Role, env.InScope, env.Criticality, env.CreatedAt, env.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating environment")
		}
		if err := t.logCreate(ctx, "environment", env.ID, env); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "environment", EntityID: env.ID,
			Title: env.Name, Subtitle: env.Code, Body: env.Role,
		})
	})
}

// UpdateEnvironment persists a modified environment.
func (s *SQLStore) UpdateEnvironment(ctx context.Context, actor domain.Actor, env *domain.Environment) error {
	if err := env.Validate(); err != nil {
		return err
	}
	before, err := s.GetEnvironment(ctx, env.ID)
	if err != nil {
		return err
	}
	env.CreatedAt = before.CreatedAt
	env.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		if err := t.requireVocabulary(ctx, vocabEnvironmentRole, "role", env.Role); err != nil {
			return err
		}
		res, err := t.exec(ctx,
			`UPDATE environment
			 SET code = ?, name = ?, role = ?, in_scope = ?, criticality = ?, updated_at = ?,
			     row_version = row_version + 1
			 WHERE id = ? AND row_version = ?`,
			env.Code, env.Name, env.Role, env.InScope, env.Criticality, env.UpdatedAt,
			env.ID, env.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating environment")
		}
		if err := requireVersion(res, "environment", env.ID, &env.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "environment", env.ID, before, env); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "environment", EntityID: env.ID,
			Title: env.Name, Subtitle: env.Code, Body: env.Role,
		})
	})
}

// GetEnvironment loads one environment by id.
func (s *SQLStore) GetEnvironment(ctx context.Context, id string) (*domain.Environment, error) {
	var env domain.Environment
	if err := s.readOne(ctx, &env, `SELECT * FROM environment WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting environment %s: %w", id, err)
	}
	return &env, nil
}

// GetEnvironmentByCode loads one environment by its short code.
func (s *SQLStore) GetEnvironmentByCode(ctx context.Context, code string) (*domain.Environment, error) {
	var env domain.Environment
	if err := s.readOne(ctx, &env, `SELECT * FROM environment WHERE code = ?`, code); err != nil {
		return nil, fmt.Errorf("getting environment %s: %w", code, err)
	}
	return &env, nil
}

// ListEnvironments returns every environment, most critical first.
func (s *SQLStore) ListEnvironments(ctx context.Context) ([]domain.Environment, error) {
	var envs []domain.Environment
	err := s.read(ctx, &envs, `SELECT * FROM environment ORDER BY criticality ASC, code ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing environments: %w", err)
	}
	return envs, nil
}

// ---------- assets ----------

// assetAudit is the audited shape of an asset: the row plus the environments it
// belongs to.
//
// Membership lives in a join table, so replacing it produced no diff on the
// asset struct and therefore no change_log entry -- an asset could be moved
// into a second environment, which is precisely what the segmentation-span
// report keys on, in complete silence. Same failure as dependency data classes;
// same fix. Codes rather than ids because an audit entry is read by people.
type assetAudit struct {
	domain.Asset
	Environments string `db:"environments"`
}

func auditedAsset(a *domain.Asset, codes []string) *assetAudit {
	sorted := append([]string(nil), codes...)
	sort.Strings(sorted)
	return &assetAudit{Asset: *a, Environments: strings.Join(sorted, ",")}
}

// environmentCodes resolves environment ids to their codes for the audit trail.
func (s *SQLStore) environmentCodes(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var codes []string
	err := s.read(ctx, &codes,
		`SELECT code FROM environment WHERE id IN (`+placeholders(len(ids))+`)`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("resolving environment codes: %w", err)
	}
	return codes, nil
}

// AssetFilter narrows a list query. Empty fields are ignored.
type AssetFilter struct {
	Kind          string
	Lifecycle     string
	EnvironmentID string
	ParentID      string
	TeamID        string
	Query         string
	// IncludeRetired defaults false: retired assets are kept forever but are
	// noise in day-to-day lists.
	IncludeRetired bool
}

// ListAssets returns assets matching the filter, with their environment codes
// preloaded for display.
func (s *SQLStore) ListAssets(ctx context.Context, f AssetFilter) ([]AssetRow, error) {
	query := `SELECT a.* FROM asset a`
	var args []any
	var where []string

	if f.EnvironmentID != "" {
		query += ` JOIN asset_environment ae ON ae.asset_id = a.id AND ae.environment_id = ?`
		args = append(args, f.EnvironmentID)
	}
	if f.Kind != "" {
		where = append(where, `a.kind = ?`)
		args = append(args, f.Kind)
	}
	if f.Lifecycle != "" {
		where = append(where, `a.lifecycle = ?`)
		args = append(args, f.Lifecycle)
	} else if !f.IncludeRetired {
		where = append(where, `a.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	if f.ParentID != "" {
		where = append(where, `a.parent_id = ?`)
		args = append(args, f.ParentID)
	}
	if f.TeamID != "" {
		where = append(where, `a.team_id = ?`)
		args = append(args, f.TeamID)
	}
	if f.Query != "" {
		// LIKE with a leading wildcard, not a full-text match: this is the
		// list page's filter box rather than the global search. LOWER() on both
		// sides makes it case-insensitive on PostgreSQL, where LIKE is not.
		//
		// ASCII ONLY, and the engines genuinely differ beyond that. `lower`
		// folds bytes A-Z to match SQLite's LOWER(); PostgreSQL's LOWER() is
		// locale-aware and folds the whole string, so the two sides of the
		// comparison fold asymmetrically there. Searching `ÖKO` finds `Ökonomie`
		// on SQLite and not on PostgreSQL -- measured by a database review. It
		// cannot be fixed by swapping in strings.ToLower, which just moves the
		// failure to the other engine; a folded shadow column would fix it and
		// is not worth a column yet. Documented rather than claimed away.
		where = append(where, `(LOWER(a.name) LIKE ? ESCAPE '\' OR LOWER(COALESCE(a.serial, '')) LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(lower(f.Query)) + "%"
		args = append(args, pattern, pattern)
	}
	query += whereClause(where) + ` ORDER BY a.kind, a.name`

	var assets []domain.Asset
	if err := s.read(ctx, &assets, query, args...); err != nil {
		return nil, fmt.Errorf("listing assets: %w", err)
	}
	rows, err := s.decorateAssets(ctx, assets)
	if err != nil {
		return nil, err
	}
	// A filtered list is a search, whatever the box is called. Somebody who
	// typed `hv-01` wants the host, not the bridge named after it -- and
	// ORDER BY kind puts `bridge` ahead of `hypervisor` every time.
	if f.Query != "" {
		sort.SliceStable(rows, rankNames(f.Query, func(i int) string { return rows[i].Name }))
	}
	return rows, nil
}

// AssetRow is an asset plus the denormalised bits every list view needs.
type AssetRow struct {
	domain.Asset
	ParentName string
	// Who looks after it. Resolved here rather than joined per row, the same
	// way environments and kind behaviour already are.
	TeamCode        string
	TeamName        string
	ManagerRoleName string
	Environments    []domain.Environment
	InstanceCount   int
	// Behaviour comes from the asset_kind lookup row rather than a switch in
	// Go, so a kind added by INSERT is usable rather than merely storable.
	// Populated by decorateAssets; the zero value is "may do nothing", which is
	// the safe reading for a row whose kind predates its vocabulary entry.
	Behaviour KindBehaviour
	// NonTransitEnvironments counts the environments that are NOT transit
	// zones. Computed here because is_transit lives on environment_role and a
	// row cannot look it up for itself.
	NonTransitEnvironments int
}

// CanHostInstances reports whether a service instance may be placed here.
func (r *AssetRow) CanHostInstances() bool { return r.Behaviour.CanHostInstances }

// IsAttachable reports whether this asset may be the subject of a
// net_attachment.
func (r *AssetRow) IsAttachable() bool { return r.Behaviour.IsAttachable }

// SpansEnvironments reports whether this asset sits in more than one
// environment -- the query the handover says will be run constantly.
//
// A transit environment does not count towards the span: brokering traffic
// between segments is a transit zone's entire purpose, so counting it would
// make every firewall a permanent false positive (docs/DECISIONS.md Q4).
func (r *AssetRow) SpansEnvironments() bool { return r.NonTransitEnvironments > 1 }

// decorateAssets attaches parent names and environment membership in two
// queries rather than one per row.
// decorateResponsibility fills in the team and role labels for a page of assets.
//
// In Go rather than as a join, because this is the same pass that already
// resolves environments (a many-to-many no join collapses cleanly) and kind
// behaviour. Adding a third mechanism to one function would not unify anything;
// services and projects join in SQL because they have no such pass.
//
// SCOPED TO THE TEAMS ACTUALLY REFERENCED. It used to read the whole `team`
// table on every list render -- harmless at organisational scale and flagged by
// two reviews anyway, because "small today" is not a property the code states.
// An IN list over the page's own rows costs nothing and removes the question.
func (s *SQLStore) decorateResponsibility(ctx context.Context, rows []AssetRow) error {
	wanted := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		if id := rows[i].TeamID; id != nil && !seen[*id] {
			seen[*id] = true
			wanted = append(wanted, *id)
		}
	}

	var teams []struct {
		ID   string `db:"id"`
		Code string `db:"code"`
		Name string `db:"name"`
	}
	// No team on any row: chunkIDs(nil) yields one empty chunk and
	// placeholders(0) yields "NULL", so the query would still run as
	// `WHERE id IN (NULL)` — valid, empty, and a pure round trip. That is the
	// COMMON case for an estate that has not filled the field in.
	//
	// Retired teams are loaded like any other: an asset legitimately points at
	// one, and that is the estate saying nobody has picked it up. Filtering on
	// lifecycle here would blank the name and hide the finding.
	for _, chunk := range chunkIDs(wanted) {
		if len(chunk) == 0 {
			continue
		}
		var part []struct {
			ID   string `db:"id"`
			Code string `db:"code"`
			Name string `db:"name"`
		}
		if err := s.read(ctx, &part,
			`SELECT id, code, name FROM team WHERE id IN (`+placeholders(len(chunk))+`)`,
			anySlice(chunk)...); err != nil {
			return fmt.Errorf("loading teams: %w", err)
		}
		teams = append(teams, part...)
	}
	byID := make(map[string]struct{ code, name string }, len(teams))
	for _, t := range teams {
		byID[t.ID] = struct{ code, name string }{t.Code, t.Name}
	}

	roles, err := s.listVocabulary(ctx, vocabResponsibilityRole)
	if err != nil {
		return err
	}
	roleLabel := make(map[string]string, len(roles))
	for _, r := range roles {
		roleLabel[r.Code] = r.Label
	}

	for i := range rows {
		if rows[i].TeamID != nil {
			if t, ok := byID[*rows[i].TeamID]; ok {
				rows[i].TeamCode, rows[i].TeamName = t.code, t.name
			}
		}
		if rows[i].ManagerRole != nil {
			// Falls back to the raw code: a role removed from the vocabulary
			// after rows referenced it should still render as something rather
			// than silently as nothing.
			if label, ok := roleLabel[*rows[i].ManagerRole]; ok {
				rows[i].ManagerRoleName = label
			} else {
				rows[i].ManagerRoleName = *rows[i].ManagerRole
			}
		}
	}
	return nil
}

func (s *SQLStore) decorateAssets(ctx context.Context, assets []domain.Asset) ([]AssetRow, error) {
	rows := make([]AssetRow, len(assets))
	ids := make([]string, 0, len(assets))
	index := make(map[string]int, len(assets))
	for i, a := range assets {
		rows[i] = AssetRow{Asset: a}
		ids = append(ids, a.ID)
		index[a.ID] = i
	}
	if len(ids) == 0 {
		return rows, nil
	}

	type membership struct {
		AssetID string `db:"asset_id"`
		domain.Environment
		// From environment_role, not from environment: whether a role brokers
		// between segments is a property of the role.
		IsTransit bool `db:"is_transit"`
	}
	var memberships []membership
	err := s.read(ctx, &memberships, `
		SELECT ae.asset_id, e.*, er.is_transit
		FROM asset_environment ae
		JOIN environment e ON e.id = ae.environment_id
		JOIN environment_role er ON er.code = e.role
		WHERE ae.asset_id IN (`+placeholders(len(ids))+`)
		ORDER BY e.code`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading asset environments: %w", err)
	}
	for _, m := range memberships {
		if i, ok := index[m.AssetID]; ok {
			rows[i].Environments = append(rows[i].Environments, m.Environment)
			if !m.IsTransit {
				rows[i].NonTransitEnvironments++
			}
		}
	}

	// Who looks after each of them. One query for the teams in play rather than
	// a join on every asset select -- there are a handful of teams and the same
	// two labels are wanted by every list view.
	if err := s.decorateResponsibility(ctx, rows); err != nil {
		return nil, err
	}

	// What each kind may do. One query for the whole vocabulary rather than one
	// per asset: it is a dozen rows and every list view needs all of them.
	var kinds []struct {
		Code string `db:"code"`
		KindBehaviour
	}
	if err := s.read(ctx, &kinds,
		`SELECT code, can_host_instances, is_attachable FROM asset_kind`); err != nil {
		return nil, fmt.Errorf("loading asset kind behaviour: %w", err)
	}
	behaviour := make(map[string]KindBehaviour, len(kinds))
	for _, k := range kinds {
		behaviour[k.Code] = k.KindBehaviour
	}
	for i := range rows {
		rows[i].Behaviour = behaviour[rows[i].Kind]
	}

	type parentName struct {
		ID   string `db:"id"`
		Name string `db:"name"`
	}
	var parents []parentName
	err = s.read(ctx, &parents, `
		SELECT DISTINCT p.id, p.name
		FROM asset a JOIN asset p ON p.id = a.parent_id
		WHERE a.id IN (`+placeholders(len(ids))+`)`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading parent names: %w", err)
	}
	byID := make(map[string]string, len(parents))
	for _, p := range parents {
		byID[p.ID] = p.Name
	}
	for i := range rows {
		if rows[i].ParentID != nil {
			rows[i].ParentName = byID[*rows[i].ParentID]
		}
	}
	return rows, nil
}

// GetAsset loads one asset with its environments attached.
func (s *SQLStore) GetAsset(ctx context.Context, id string) (*AssetRow, error) {
	var asset domain.Asset
	if err := s.readOne(ctx, &asset, `SELECT * FROM asset WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting asset %s: %w", id, err)
	}
	rows, err := s.decorateAssets(ctx, []domain.Asset{asset})
	if err != nil {
		return nil, err
	}
	return &rows[0], nil
}

// CreateAsset inserts an asset, seeds its closure rows, and audits the change.
// requireUniqueSiblingName enforces the asset natural key: a name is unique
// among an asset's LIVE siblings.
//
// The partial unique indexes in migration 00021 are the guarantee; this is the
// message. Without it a duplicate name arrives as translateWriteErr's
// ErrConflict, which the handler renders as a bare 409 -- "That conflicts with
// something that already exists" -- with no field named, no form re-rendered and
// everything the operator typed thrown away. That is the wrong shape twice over:
// a name collision is a validation failure on one field, and CLAUDE.md's answer
// to a validation failure is 422 with the form partial and what was typed still
// in it.
//
// CHECK-THEN-ACT, DELIBERATELY. Two concurrent creates can both pass this and
// the second will lose to the index, which is correct -- the constraint is the
// line that holds. This runs inside the caller's transaction and exists so the
// common case reads as a sentence rather than a status code.
//
// RETIRED ROWS DO NOT PARTICIPATE, on either side. A retired asset does not
// hold its name against a new one, and an asset being retired cannot collide
// with anything. Both halves match the index, and they have to: a pre-check
// stricter than the constraint refuses writes the database would accept.
// THE FIELD IS A PARAMETER because the same collision arrives through two
// different inputs. Typing a name into the asset form is a fault of `name`;
// choosing a destination that already holds that name is a fault of
// `parent_id`, and that is the only input the move form has. A message attached
// to a field the form does not render is a refusal with nowhere to appear,
// which this project has shipped three times.
func requireUniqueSiblingName(ctx context.Context, t *tx, field, name string, parentID *string, lifecycle, excludeID string) error {
	if lifecycle == domain.LifecycleRetired {
		return nil
	}

	// Two statements rather than one clever predicate, because `parent_id = ?`
	// never matches NULL on either engine -- the same asymmetry that makes the
	// index two indexes.
	var n int
	var err error
	if parentID == nil {
		err = t.get(ctx, &n,
			`SELECT COUNT(*) FROM asset
			 WHERE parent_id IS NULL AND name = ? AND lifecycle <> 'retired' AND id <> ?`,
			name, excludeID)
	} else {
		err = t.get(ctx, &n,
			`SELECT COUNT(*) FROM asset
			 WHERE parent_id = ? AND name = ? AND lifecycle <> 'retired' AND id <> ?`,
			*parentID, name, excludeID)
	}
	if err != nil {
		return fmt.Errorf("checking for a sibling of the same name: %w", err)
	}
	if n == 0 {
		return nil
	}

	ve := &domain.ValidationError{}
	switch {
	case field == "parent_id":
		ve.Add(field, "that destination already contains something called %q", name)
	case parentID == nil:
		ve.Add(field, "another top-level asset is already called %q", name)
	default:
		ve.Add(field, "something here is already called %q", name)
	}
	return ve
}

func (s *SQLStore) CreateAsset(ctx context.Context, actor domain.Actor, a *domain.Asset, environmentIDs []string) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	a.RowVersion = 1
	if err := a.Validate(); err != nil {
		return err
	}
	codes, err := s.environmentCodes(ctx, environmentIDs)
	if err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		return s.insertAsset(ctx, t, a, environmentIDs, codes)
	})
}

// insertAsset writes one asset inside a transaction the CALLER owns.
//
// Split out of CreateAsset so a bulk import can put many of these in ONE
// transaction. That is not a tidiness refactor: "whole-file refusal on partial
// failure" is only expressible if every row shares a transaction, and the same
// property is what makes a dry run honest -- the import runs the real writes,
// with the real vocabulary checks, the real closure maintenance and the real
// unique indexes, and then rolls back. A dry run that SIMULATES instead would be
// a second implementation of this function, and the two would disagree exactly
// when it mattered.
//
// Everything a single create does happens here, in the same order, including the
// change_log row. An import is a declared-state mutation like any other and gets
// one audit entry per asset, not one per file.
func (s *SQLStore) insertAsset(ctx context.Context, t *tx, a *domain.Asset, environmentIDs, codes []string) error {
	if err := t.requireVocabulary(ctx, vocabAssetKind, "kind", a.Kind); err != nil {
		return err
	}
	if err := requireRole(ctx, t, a.ManagerRole); err != nil {
		return err
	}
	if err := requireUniqueSiblingName(ctx, t, "name", a.Name, a.ParentID, a.Lifecycle, a.ID); err != nil {
		return err
	}
	_, err := t.exec(ctx,
		`INSERT INTO asset (id, kind, name, parent_id, serial, asset_tag, vendor, model,
		                    device_type_id, lifecycle, team_id, manager_role, eol_date, attrs,
		                    created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Kind, a.Name, a.ParentID, a.Serial, a.AssetTag, a.Vendor, a.Model,
		a.DeviceTypeID, a.Lifecycle, a.TeamID, a.ManagerRole, a.EOLDate, a.Attrs,
		a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return translateWriteErr(err, "creating asset")
	}
	if err := insertClosureForNewNode(ctx, t, a.ID, a.ParentID); err != nil {
		return err
	}
	if err := setAssetEnvironments(ctx, t, a.ID, environmentIDs); err != nil {
		return err
	}
	if err := t.logCreate(ctx, "asset", a.ID, auditedAsset(a, codes)); err != nil {
		return err
	}
	return s.indexAsset(ctx, t, a)
}

// UpdateAsset persists field changes. Reparenting is deliberately not handled
// here -- it has to rebuild the closure subtree, so it gets its own method.
func (s *SQLStore) UpdateAsset(ctx context.Context, actor domain.Actor, a *domain.Asset, environmentIDs []string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	before, err := s.GetAsset(ctx, a.ID)
	if err != nil {
		return err
	}
	a.CreatedAt = before.CreatedAt
	a.ParentID = before.ParentID // reparenting goes through ReparentAsset
	a.UpdatedAt = domain.FormatTime(s.now())

	beforeCodes := make([]string, 0, len(before.Environments))
	for _, env := range before.Environments {
		beforeCodes = append(beforeCodes, env.Code)
	}
	afterCodes := beforeCodes
	if environmentIDs != nil {
		// nil means "not managing membership here", so it is unchanged.
		afterCodes, err = s.environmentCodes(ctx, environmentIDs)
		if err != nil {
			return err
		}
	}

	return s.write(ctx, actor, func(t *tx) error {
		if err := t.requireVocabulary(ctx, vocabAssetKind, "kind", a.Kind); err != nil {
			return err
		}
		if err := requireRole(ctx, t, a.ManagerRole); err != nil {
			return err
		}
		// a.ParentID was forced to before.ParentID above -- reparenting goes
		// through ReparentAsset, which runs its own check against the DESTINATION.
		// The lifecycle is the SUBMITTED one, so un-retiring into a name somebody
		// else took while this row was retired is refused here with a message
		// rather than by the index with a 409.
		if err := requireUniqueSiblingName(ctx, t, "name", a.Name, a.ParentID, a.Lifecycle, a.ID); err != nil {
			return err
		}
		res, err := t.exec(ctx,
			`UPDATE asset SET kind = ?, name = ?, serial = ?, asset_tag = ?, vendor = ?,
			                  model = ?, device_type_id = ?, lifecycle = ?, team_id = ?,
			                  manager_role = ?, eol_date = ?, attrs = ?, updated_at = ?,
			                  row_version = row_version + 1
			 WHERE id = ? AND row_version = ?`,
			a.Kind, a.Name, a.Serial, a.AssetTag, a.Vendor, a.Model, a.DeviceTypeID,
			a.Lifecycle, a.TeamID, a.ManagerRole, a.EOLDate, a.Attrs, a.UpdatedAt,
			a.ID, a.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating asset")
		}
		if err := requireVersion(res, "asset", a.ID, &a.RowVersion); err != nil {
			return err
		}
		if err := setAssetEnvironments(ctx, t, a.ID, environmentIDs); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "asset", a.ID,
			auditedAsset(&before.Asset, beforeCodes),
			auditedAsset(a, afterCodes)); err != nil {
			return err
		}
		return s.indexAsset(ctx, t, a)
	})
}

// RetireAsset is the only form of deletion. The row stays; lifecycle moves to
// retired, and the audit trail records who did it.
//
// It also retires any live topology edge that names this asset: its
// net_group_member row, any net_attachment it owns, any net_attachment it is
// pinned to as a chassis member, and any link cabling one of its interfaces
// -- all in the same transaction, so no live edge can ever point at a retired
// box (docs/reachability-design.md, M2). net_uplink is not touched directly:
// it is a group-to-group edge with no asset_id column of its own, so retiring
// one asset out of a forwarder group is a group-health question for the
// impact engine (M3), not a row this method can cascade into.
//
// writeSerializable, not write: this method now asserts a cross-table
// invariant ("no active attachment/attachment-member points at a retired
// asset") the same way CreateNetAttachment asserts one when it inserts. At
// Postgres's default read-committed level, a plain write() here racing a
// serializable CreateNetAttachment gets no protection at all -- SSI only
// guards transactions that are themselves serializable -- and the result is
// exactly the state this method's contract says cannot exist: an active
// attachment naming a retired asset.
func (s *SQLStore) RetireAsset(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetAsset(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `UPDATE asset SET lifecycle = ?, updated_at = ?,
			                       row_version = row_version + 1 WHERE id = ?`,
			domain.LifecycleRetired, at, id)
		if err != nil {
			return translateWriteErr(err, "retiring asset")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, before.Lifecycle, domain.LifecycleRetired)
		if err := t.log(ctx, "asset", id, domain.ActionRetire, diff); err != nil {
			return err
		}
		return retireAssetTopology(ctx, t, id, at)
	})
}

// retireAssetTopology retires every live topology edge naming assetID, so
// "no live topology edge may point at a retired box" holds without a reader
// having to know to filter it out.
func retireAssetTopology(ctx context.Context, t *tx, assetID, at string) error {
	var groupMembers []struct {
		GroupID string `db:"group_id"`
	}
	if err := t.tx.SelectContext(ctx, &groupMembers, t.rebind(
		`SELECT group_id FROM net_group_member WHERE asset_id = ? AND lifecycle = ?`),
		assetID, domain.LifecycleActive); err != nil {
		return fmt.Errorf("finding net group memberships to retire: %w", err)
	}
	for _, m := range groupMembers {
		if _, err := t.exec(ctx,
			`UPDATE net_group_member SET lifecycle = ?, updated_at = ? WHERE group_id = ? AND asset_id = ?`,
			domain.LifecycleRetired, at, m.GroupID, assetID); err != nil {
			return translateWriteErr(err, "retiring net group membership")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, domain.LifecycleActive, domain.LifecycleRetired)
		if err := t.log(ctx, "net_group_member", m.GroupID+"/"+assetID, domain.ActionRetire, diff); err != nil {
			return err
		}
	}

	// Attachments to retire: this asset's own (it is the attaching host), and
	// any attachment this asset is pinned to as a chassis member. Both cases
	// retire the WHOLE net_attachment row, never just delete or retire the
	// member pin: net_attachment_member has no lifecycle column of its own on
	// purpose (see 00007_reachability.sql) precisely because deleting a pin
	// when its chassis retires would turn a single-homed host's *last*
	// surviving chassis into an empty member set -- which means "attached to
	// the group as a whole" -- promoting a host that just lost its only path
	// into reading as MORE robust than before. Retiring the declaration
	// instead is the pessimistic-safe choice; an operator can re-declare a
	// corrected attachment against the surviving chassis if one remains.
	var attachmentIDs []string
	if err := t.tx.SelectContext(ctx, &attachmentIDs, t.rebind(`
		SELECT id FROM net_attachment WHERE asset_id = ? AND lifecycle = ?
		UNION
		SELECT DISTINCT am.attachment_id
		FROM net_attachment_member am
		JOIN net_attachment na ON na.id = am.attachment_id
		WHERE am.asset_id = ? AND na.lifecycle = ?`),
		assetID, domain.LifecycleActive, assetID, domain.LifecycleActive); err != nil {
		return fmt.Errorf("finding net attachments to retire: %w", err)
	}
	if err := retireRows(ctx, t, "net_attachment", attachmentIDs, at); err != nil {
		return err
	}

	// This asset's own patched interfaces: the cables themselves become dead
	// topology the moment the asset behind either end is gone, exactly like
	// unpatching a port (docs/DECISIONS.md, 2026-07-28 decisions). `link` has
	// no updated_at column (see RetireLink), so this can't go through the
	// generic retireRows helper the rest of this cascade uses.
	linkIDs, err := selectIDs(ctx, t, `
		SELECT id FROM link
		WHERE lifecycle = ?
		  AND (a_interface_id IN (SELECT id FROM interface WHERE asset_id = ?)
		    OR b_interface_id IN (SELECT id FROM interface WHERE asset_id = ?))`,
		domain.LifecycleActive, assetID, assetID)
	if err != nil {
		return fmt.Errorf("finding links to retire: %w", err)
	}
	for _, id := range linkIDs {
		if _, err := t.exec(ctx, `UPDATE link SET lifecycle = ? WHERE id = ?`,
			domain.LifecycleRetired, id); err != nil {
			return translateWriteErr(err, "retiring link")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, domain.LifecycleActive, domain.LifecycleRetired)
		if err := t.log(ctx, "link", id, domain.ActionRetire, diff); err != nil {
			return err
		}
	}
	return nil
}

// ---------- containment ----------

// insertClosureForNewNode writes the closure rows for a leaf that has just
// been created: a self-row at depth 0, plus one row per ancestor of its
// parent. A new node has no descendants yet, so this is the cheap case.
func insertClosureForNewNode(ctx context.Context, t *tx, id string, parentID *string) error {
	if _, err := t.exec(ctx,
		`INSERT INTO asset_closure (ancestor_id, descendant_id, depth) VALUES (?, ?, 0)`,
		id, id); err != nil {
		return translateWriteErr(err, "seeding closure self row")
	}
	if parentID == nil || *parentID == "" {
		return nil
	}
	_, err := t.exec(ctx,
		`INSERT INTO asset_closure (ancestor_id, descendant_id, depth)
		 SELECT c.ancestor_id, ?, c.depth + 1
		 FROM asset_closure c
		 WHERE c.descendant_id = ?`,
		id, *parentID)
	if err != nil {
		return translateWriteErr(err, "seeding closure ancestors")
	}
	return nil
}

// ReparentAsset moves a subtree.
//
// The closure rows for the whole subtree are rebuilt rather than patched
// incrementally: incremental updates to a closure table are where these
// implementations go wrong, and a subtree here is small enough that rebuilding
// costs nothing.
func (s *SQLStore) ReparentAsset(ctx context.Context, actor domain.Actor, id string, newParentID *string) error {
	before, err := s.GetAsset(ctx, id)
	if err != nil {
		return err
	}
	if newParentID != nil && *newParentID == "" {
		newParentID = nil
	}
	if newParentID != nil && *newParentID == id {
		ve := &domain.ValidationError{}
		ve.Add("parent_id", "an asset cannot contain itself")
		return ve
	}

	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if newParentID != nil {
			// Moving a node underneath its own descendant would detach the
			// subtree into a cycle, and the closure table has no way to
			// represent that.
			var n int
			err := t.get(ctx, &n,
				`SELECT COUNT(*) FROM asset_closure WHERE ancestor_id = ? AND descendant_id = ?`,
				id, *newParentID)
			if err != nil {
				return fmt.Errorf("checking for containment cycle: %w", err)
			}
			if n > 0 {
				ve := &domain.ValidationError{}
				ve.Add("parent_id", "that asset is inside this one; moving it there would create a cycle")
				return ve
			}
		}

		// The third way into a collision, and the one that is easy to miss: the
		// name is not changing and the destination is not new, but together they
		// are. Moving esx-01 out of rack-a into rack-b, where an esx-01 already
		// sits, is refused here rather than by the index.
		if err := requireUniqueSiblingName(ctx, t, "parent_id", before.Name, newParentID, before.Lifecycle, id); err != nil {
			return err
		}

		// Detach: drop every edge from outside the subtree into it, keeping
		// the subtree's internal edges intact.
		_, err := t.exec(ctx, `
			DELETE FROM asset_closure
			WHERE descendant_id IN (SELECT descendant_id FROM asset_closure WHERE ancestor_id = ?)
			  AND ancestor_id NOT IN (SELECT descendant_id FROM asset_closure WHERE ancestor_id = ?)`,
			id, id)
		if err != nil {
			return translateWriteErr(err, "detaching closure subtree")
		}

		// Reattach: cross-join every ancestor of the new parent with every
		// descendant of the moved node.
		if newParentID != nil {
			_, err = t.exec(ctx, `
				INSERT INTO asset_closure (ancestor_id, descendant_id, depth)
				SELECT super.ancestor_id, sub.descendant_id, super.depth + sub.depth + 1
				FROM asset_closure super
				CROSS JOIN asset_closure sub
				WHERE super.descendant_id = ? AND sub.ancestor_id = ?`,
				*newParentID, id)
			if err != nil {
				return translateWriteErr(err, "reattaching closure subtree")
			}
		}

		if _, err := t.exec(ctx, `UPDATE asset SET parent_id = ?, updated_at = ?,
			                          row_version = row_version + 1 WHERE id = ?`,
			newParentID, at, id); err != nil {
			return translateWriteErr(err, "reparenting asset")
		}

		after := before.Asset
		after.ParentID = newParentID
		return t.logUpdate(ctx, "asset", id, &before.Asset, &after)
	})
}

// SubtreeIDs returns every asset at or below the given assets, including the
// assets themselves. This is the query that makes "reboot this VM", "reboot
// this hypervisor" and "this rack loses power" the same operation.
func (s *SQLStore) SubtreeIDs(ctx context.Context, assetIDs []string) ([]string, error) {
	if len(assetIDs) == 0 {
		return nil, nil
	}
	var ids []string
	err := s.read(ctx, &ids, `
		SELECT DISTINCT descendant_id FROM asset_closure
		WHERE ancestor_id IN (`+placeholders(len(assetIDs))+`)`, anySlice(assetIDs)...)
	if err != nil {
		return nil, fmt.Errorf("resolving subtree: %w", err)
	}
	return ids, nil
}

// Ancestors returns the containment path from the root down to the asset,
// excluding the asset itself. Used for breadcrumbs.
func (s *SQLStore) Ancestors(ctx context.Context, assetID string) ([]domain.Asset, error) {
	var assets []domain.Asset
	err := s.read(ctx, &assets, `
		SELECT a.* FROM asset_closure c
		JOIN asset a ON a.id = c.ancestor_id
		WHERE c.descendant_id = ? AND c.depth > 0
		ORDER BY c.depth DESC`, assetID)
	if err != nil {
		return nil, fmt.Errorf("loading ancestors of %s: %w", assetID, err)
	}
	return assets, nil
}

// Children returns the direct children of an asset.
func (s *SQLStore) Children(ctx context.Context, assetID string) ([]AssetRow, error) {
	return s.ListAssets(ctx, AssetFilter{ParentID: assetID, IncludeRetired: true})
}

// ---------- environment membership ----------

func setAssetEnvironments(ctx context.Context, t *tx, assetID string, environmentIDs []string) error {
	if environmentIDs == nil {
		return nil // caller is not managing membership in this operation
	}
	if _, err := t.exec(ctx, `DELETE FROM asset_environment WHERE asset_id = ?`, assetID); err != nil {
		return translateWriteErr(err, "clearing asset environments")
	}
	for _, envID := range environmentIDs {
		if envID == "" {
			continue
		}
		_, err := t.exec(ctx,
			`INSERT INTO asset_environment (asset_id, environment_id) VALUES (?, ?)`,
			assetID, envID)
		if err != nil {
			return translateWriteErr(err, "adding asset to environment")
		}
	}
	return nil
}

// SpanningAssets returns assets that belong to more than one non-transit
// environment. These are the segmentation exceptions worth reviewing: a
// transit zone spanning environments is doing its job, a database doing it is
// a finding.
func (s *SQLStore) SpanningAssets(ctx context.Context) ([]AssetRow, error) {
	// Retired assets are excluded: a decommissioned server that once bridged
	// two environments is history, not a live segmentation exception, and
	// leaving it on the report keeps a resolved finding permanently open.
	var ids []string
	err := s.read(ctx, &ids, `
		SELECT ae.asset_id
		FROM asset_environment ae
		JOIN environment e ON e.id = ae.environment_id
		JOIN asset a ON a.id = ae.asset_id
		WHERE e.role <> ? AND a.lifecycle <> ?
		GROUP BY ae.asset_id
		HAVING COUNT(*) > 1`, domain.EnvRoleTransit, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("finding spanning assets: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var assets []domain.Asset
	err = s.read(ctx, &assets,
		`SELECT * FROM asset WHERE id IN (`+placeholders(len(ids))+`) ORDER BY name`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading spanning assets: %w", err)
	}
	return s.decorateAssets(ctx, assets)
}

// whereClause joins predicates, returning an empty string when there are none.
func whereClause(where []string) string {
	if len(where) == 0 {
		return ""
	}
	out := " WHERE " + where[0]
	var outSb679 strings.Builder
	for _, w := range where[1:] {
		outSb679.WriteString(" AND " + w)
	}
	out += outSb679.String()
	return out
}

// lower is strings.ToLower, kept local so the LIKE-building code reads as one
// idea rather than importing strings for a single call.
func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
