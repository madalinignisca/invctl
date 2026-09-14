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

// The counterpart to a decision made elsewhere, not a feature of its own.
//
// A device type's component template is instantiated onto an asset once, at
// creation (Task 4) or by an explicit backfill (Task 5, ApplyTemplate). An
// edit to the template AFTER that point never reaches back and rewrites the
// asset -- doing so would put a change into change_log attributed to nobody,
// since no operator touched that asset. "Drift reported as a finding
// instead" is the other half of that decision; without this file the
// "instead" is empty.
//
// ONLY MISSING COMPONENTS ARE DRIFT HERE, NOT EXTRA ONES. An asset can
// legitimately carry an interface its device type's template never named --
// an operator adding a real port the model sheet does not (or a template
// that never covered every port to begin with) is ordinary and correct, not
// a discrepancy to flag. Reporting it would teach people to ignore the page,
// the same reasoning EstateFindings already applies to expected power
// convergence. Only "the model declares eth1 and this asset has never had
// it" is reported, and only as FindingGap: the estate did not fail, the
// inventory simply does not know why this one instance disagrees with its
// type -- maybe the template gained eth1 after the asset was built, maybe an
// operator withdrew it on purpose. Either way, it is not knowable from here.

// templateDriftRow is one (asset, missing component) pair -- the unit the
// query below produces, before Go folds it down to one row per KIND of
// finding.
type templateDriftRow struct {
	AssetID       string `db:"asset_id"`
	AssetName     string `db:"asset_name"`
	ComponentName string `db:"component_name"`
}

// templateDriftQuery is a single anti-join, not a query per asset. A row
// survives the LEFT JOIN's WHERE clause exactly when the asset's device type
// names an active interface component that the asset has no interface row
// for, of any lifecycle -- matching ApplyTemplate's own definition of
// "already accounted for" (see that function's doc comment on why a retired
// name is not missing). The database does the matching with its own indexes
// on device_type_component(device_type_id) and interface(asset_id, name)
// rather than this package pulling every interface row into memory to
// compute the same anti-join in Go: PowerFindings' one-query-then-analyse
// shape fits a chain that needs graph logic no SQL reader would want to
// maintain, but "does this name exist for this asset" is exactly what a join
// is for, and doing it here keeps this linear in components-plus-interfaces
// rather than reading the whole estate to answer one question about a
// fraction of it.
const templateDriftQuery = `
	SELECT a.id AS asset_id, a.name AS asset_name, dtc.name AS component_name
	FROM asset a
	JOIN device_type_component dtc
	  ON dtc.device_type_id = a.device_type_id
	 AND dtc.kind = ?
	 AND dtc.lifecycle = ?
	LEFT JOIN interface i
	  ON i.asset_id = a.id AND i.name = dtc.name
	WHERE a.lifecycle <> ? AND i.id IS NULL
	ORDER BY a.name, dtc.name`

// TemplateDriftFindings reports assets whose device type declares an active
// interface component the asset itself has never recorded, one row per KIND
// exactly like every other source EstateFindings gathers -- forty drifting
// assets is one decision, not forty rows nobody reads to the bottom of. See
// this file's own header for why only MISSING components count, never extra
// ones, and why the severity is always FindingGap.
func (s *SQLStore) TemplateDriftFindings(ctx context.Context) ([]Finding, error) {
	var rows []templateDriftRow
	if err := s.read(ctx, &rows, templateDriftQuery,
		domain.ComponentKindInterface, domain.LifecycleActive, domain.LifecycleRetired,
	); err != nil {
		return nil, fmt.Errorf("finding device type template drift: %w", err)
	}

	seen := make(map[string]bool, len(rows))
	var driftingAssets int
	var detail string
	for _, r := range rows {
		if seen[r.AssetID] {
			continue
		}
		seen[r.AssetID] = true
		driftingAssets++
		if detail == "" {
			detail = fmt.Sprintf("%s is missing %s", r.AssetName, r.ComponentName)
		}
	}
	if driftingAssets == 0 {
		return nil, nil
	}

	return []Finding{{
		Severity: FindingGap,
		Count:    driftingAssets,
		Label:    "asset missing a component its device type declares",
		Detail:   detail,
		Href:     "/assets",
	}}, nil
}
