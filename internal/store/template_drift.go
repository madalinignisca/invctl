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
		Href:     "/assets/" + rows[0].AssetID,
	}}, nil
}

// templateExtraQuery is templateDriftQuery's mirror image: it finds an
// active interface on an asset whose name the asset's device type's active
// template never declares, scoped to device types that declare a template AT
// ALL (the EXISTS clause) so a model with no template yet does not make
// every one of its assets' ordinary ports "extra".
//
// THIS IS DELIBERATELY NARROWER THAN "any unnamed port is suspicious" --
// template_drift.go's own header explains at length why an operator-added
// port beyond the model sheet is ordinary, and TestTemplateDriftFindingsIgnoresExtraInterfaces
// pins that for TemplateDriftFindings, which must keep ignoring it. This
// query exists for the opposite, narrower case a demo estate exposed: a
// device type WITH a template whose declared names do not match the
// estate's real naming (e.g. a range that expanded to "Ethernet1/1.."
// instead of the switch's actual "Ethernet1.."), so applying the template
// piles on phantom ports beside the real ones. A model with a template is
// making a specific claim about every port it should have; an asset of that
// model carrying a port outside that claim is worth a second look in a way a
// model that never declared anything is not.
const templateExtraQuery = `
	SELECT a.id AS asset_id, a.name AS asset_name, i.name AS component_name
	FROM asset a
	JOIN interface i
	  ON i.asset_id = a.id AND i.lifecycle = ?
	LEFT JOIN device_type_component dtc
	  ON dtc.device_type_id = a.device_type_id
	 AND dtc.kind = ?
	 AND dtc.lifecycle = ?
	 AND dtc.name = i.name
	WHERE a.lifecycle <> ?
	  AND a.device_type_id IS NOT NULL
	  AND dtc.id IS NULL
	  AND EXISTS (
	    SELECT 1 FROM device_type_component dtc2
	    WHERE dtc2.device_type_id = a.device_type_id
	      AND dtc2.kind = ?
	      AND dtc2.lifecycle = ?
	  )
	ORDER BY a.name, i.name`

// TemplateExtraFindings reports assets carrying an active interface that
// their device type's active template never names, scoped to device types
// that declare a template at all -- see templateExtraQuery's own comment for
// why, and TemplateDriftFindings' header for why this is a SEPARATE finding
// rather than a change to that one: extra ports are ordinary in general and
// only worth flagging against a model that made a specific claim about what
// it should have.
func (s *SQLStore) TemplateExtraFindings(ctx context.Context) ([]Finding, error) {
	var rows []templateDriftRow
	if err := s.read(ctx, &rows, templateExtraQuery,
		domain.LifecycleActive, domain.ComponentKindInterface, domain.LifecycleActive,
		domain.LifecycleRetired, domain.ComponentKindInterface, domain.LifecycleActive,
	); err != nil {
		return nil, fmt.Errorf("finding assets with more interfaces than their template declares: %w", err)
	}

	seen := make(map[string]bool, len(rows))
	var extraAssets int
	var detail string
	for _, r := range rows {
		if seen[r.AssetID] {
			continue
		}
		seen[r.AssetID] = true
		extraAssets++
		if detail == "" {
			detail = fmt.Sprintf("%s has %s, which its template does not declare", r.AssetName, r.ComponentName)
		}
	}
	if extraAssets == 0 {
		return nil, nil
	}

	return []Finding{{
		Severity: FindingGap,
		Count:    extraAssets,
		Label:    "asset with a component its device type's template does not declare",
		Detail:   detail,
		Href:     "/assets/" + rows[0].AssetID,
	}}, nil
}
