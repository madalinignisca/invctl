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

// What the estate declares it draws, and how much of the estate said anything.
//
// SEPARATE FROM estate_costs.go ON PURPOSE. That file totals what somebody
// PRICED; this is a figure this system DERIVED from a nameplate and a rate. A
// derived figure entering EstateCosts.Totals would make it part-declared and
// part-derived with no way to tell which, so the two never meet in one struct
// (docs/power-cost-design.md §2.4). Keeping them apart stops the ARITHMETIC
// contamination; the page's own layout is what stops a reader adding them.

// DeclaredPowerDraw sums the estate's declared load and counts what it could
// not see.
//
// THREE THINGS ARE LOAD-BEARING HERE AND EACH ONE FIXES A DOUBLE-COUNT OR A
// LIE:
//
//  1. MAX(draw_va) PER ASSET, NOT SUM. draw_va is an allocation figure: both
//     sides of a dual-fed server record the WHOLE load, because a feed is
//     correctly sized only if it can carry its partner's entire load when the
//     other side dies (power.go's allocated_va, and the fixture says so in as
//     many words at seed_hardware.go:209). SUM would return 1,800 VA for a
//     900 VA server -- a 100% overstatement across every properly-redundant
//     asset in the estate. Nothing in the schema distinguishes "my half" from
//     "the whole load recorded twice", so no aggregation over this column can
//     tell them apart; the information is not there to be recovered.
//
//  2. AN ASSET INSIDE A DRAWING ASSET CONTRIBUTES NOTHING. power_input.asset_id
//     has no kind restriction, so a VM can declare its own input through the
//     same form every asset gets. A hypervisor at 900 and a VM inside it at 100
//     is 1,000 by any naive query, even though the VM's power is virtual and
//     already in the host's wall draw. Closed through asset_closure -- never a
//     recursive parent_id walk -- so it costs one correlated subquery and does
//     not depend on nobody entering the data.
//
//  3. LIFECYCLE GATES THE ASSET AND THE INPUT, AND NOTHING ELSE. Decided, not
//     copied: PowerFindings filters feed and panel too, because a FINDING is
//     about the supply path. A retired feed under a running server is a data
//     inconsistency, not a reason to believe the server stopped drawing power.
//     AssetsLosingPower already filters exactly these two; this follows it.
//
// D3 (amended 2026-09-04): the two counts returned are how many assets
// contributed a draw and how many live inputs declared none -- NEVER a ratio
// against every live asset, and never a WHERE kind IN (...) list. asset.kind
// is an open lookup table that grows by INSERT
// (internal/domain/asset.go:114-127), and narrowing by it silently excludes a
// kind added later with no diagnostic; the adjacent PowerFindings report
// already settled this the same way (powerCoverage's UndeclaredDraw).
//
// ONE QUERY, NOT TWO. Both counts and the total come out of the same scan, so
// they cannot disagree -- two statements could straddle a concurrent write and
// report a figure that does not match its own coverage counts.
func (s *SQLStore) DeclaredPowerDraw(ctx context.Context) (domain.DeclaredDraw, error) {
	var row struct {
		TotalVA        int64 `db:"total_va"`
		Declaring      int   `db:"declaring"`
		UndeclaredDraw int   `db:"undeclared_draw"`
	}
	// COUNT(per_asset.max_draw) counts the non-NULL ones: an asset whose only
	// live inputs declare nothing has MAX() of NULL, so it does not contribute
	// to Declaring -- it is simply absent from the figure, per D3, rather than
	// counted against an invented denominator.
	//
	// undeclared_draw mirrors powerCoverage's UndeclaredDraw
	// (internal/store/power_findings.go) exactly, including its scope: every
	// LIVE power_input with no draw_va, full stop -- not narrowed by
	// containment or by its asset's lifecycle, because the gap it reports is
	// "this input needs a number typed into it", not "this input is missing
	// from the estate total".
	//
	// The derived table's alias is not decoration: PostgreSQL rejects a
	// subquery in FROM without one, and SQLite accepts it -- which is the
	// shape of every dual-engine defect this repo has had.
	err := s.readOne(ctx, &row, `
		SELECT COALESCE(SUM(per_asset.max_draw), 0) AS total_va,
		       COUNT(per_asset.max_draw)            AS declaring,
		       (SELECT COUNT(*) FROM power_input
		        WHERE draw_va IS NULL AND lifecycle <> ?)     AS undeclared_draw
		FROM (
			SELECT a.id AS asset_id, MAX(i.draw_va) AS max_draw
			FROM asset a
			LEFT JOIN power_input i
			       ON i.asset_id = a.id AND i.lifecycle <> ?
			WHERE a.lifecycle <> ?
			  AND NOT EXISTS (
			      SELECT 1
			      FROM asset_closure c
			      JOIN power_input pi ON pi.asset_id = c.ancestor_id
			      JOIN asset pa       ON pa.id = c.ancestor_id
			      WHERE c.descendant_id = a.id
			        AND c.depth > 0
			        AND pi.draw_va IS NOT NULL
			        AND pi.lifecycle <> ?
			        AND pa.lifecycle <> ?
			  )
			GROUP BY a.id
		) per_asset`,
		domain.LifecycleRetired,
		domain.LifecycleRetired, domain.LifecycleRetired,
		domain.LifecycleRetired, domain.LifecycleRetired)
	if err != nil {
		return domain.DeclaredDraw{}, fmt.Errorf("summing the estate's declared power draw: %w", err)
	}
	return domain.DeclaredDraw{
		TotalVA:        row.TotalVA,
		Declaring:      row.Declaring,
		UndeclaredDraw: row.UndeclaredDraw,
	}, nil
}
