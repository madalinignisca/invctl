// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package csvsafe defuses a CSV cell a spreadsheet would treat as a live
// formula.
//
// IT EXISTS SO THE LOGIC IS WRITTEN ONCE. internal/web/render.CSV already
// defused every cell of every export at the boundary where a Table becomes a
// downloaded file. WP-A4's custom field values are built in internal/store,
// which must never import internal/web (store is the lower layer), so the
// one place both sides can reach is a small package neither of them owns.
package csvsafe

// Cell defuses a cell a spreadsheet would evaluate as a formula.
//
// Excel and LibreOffice evaluate a cell beginning with = + - or @, so a value
// somebody typed freely -- an asset name, a custom field's text -- becomes
// code the moment a colleague opens the file. The database is right to store
// it verbatim; the defusing belongs at the boundary where text stops being
// data and starts being a spreadsheet.
//
// A leading apostrophe is the conventional fix: a spreadsheet reads it as
// "treat the rest as text" and drops it on display, so the cell still reads
// correctly to a human. Nothing is removed -- an export that silently altered
// a value would be worse than the problem it solves.
//
// IDEMPOTENT, AND THAT MATTERS HERE SPECIFICALLY: a custom field value is
// defused once where it enters a Table (internal/store) and, like every other
// cell, again -- harmlessly -- wherever that Table is written out as a CSV
// file (render.CSV). A cell already carrying the leading apostrophe does not
// match any of the trigger bytes below, so the second pass is a no-op.
func Cell(cell string) string {
	if cell == "" {
		return cell
	}
	switch cell[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + cell
	}
	return cell
}
