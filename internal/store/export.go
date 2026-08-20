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
	"strconv"
	"strings"

	"github.com/madalinignisca/invctl/internal/csvsafe"
	"github.com/madalinignisca/invctl/internal/domain"
)

// Rows on their way out as CSV.
//
// THE COLUMNS ARE CHOSEN SO THE IMPORTER CAN READ THEM BACK, which is the whole
// design and is nearly free once decided. An export that names its parent by
// UUID is a file you can look at; one that names it by the same PATH the
// importer resolves is a file you can edit in a spreadsheet and load again.
// That turns export/edit/re-import into bulk editing without a single line of
// code for bulk editing.
//
// It is deliberately NOT a serialisation of the row. Internal ids, row_version
// and timestamps are absent because they mean nothing outside this database and
// their presence would invite somebody to edit one.

// Table is a rendered result set: a header and its rows, in order.
type Table struct {
	// Name becomes the filename, so it says what the file is six months later
	// in somebody's downloads folder.
	Name   string
	Header []string
	Rows   [][]string
}

// The render.Table interface, so the web layer can write one without importing
// this package's concrete type into its signature. Detached from the
// declaration: it describes all three methods, not CSVName.

func (t Table) CSVName() string     { return t.Name }
func (t Table) CSVHeader() []string { return t.Header }
func (t Table) CSVRows() [][]string { return t.Rows }

// AssetPaths returns every live asset's containment path, keyed by id.
//
// Shared with the importer's own resolver by intent rather than by code: this
// reads the same shape for the opposite direction, and the round trip is only
// true while the two agree about what a path is.
func (s *SQLStore) AssetPaths(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		ID       string  `db:"id"`
		Name     string  `db:"name"`
		ParentID *string `db:"parent_id"`
	}
	if err := s.read(ctx, &rows,
		`SELECT id, name, parent_id FROM asset WHERE lifecycle <> ?`,
		domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading the asset tree: %w", err)
	}
	type node struct {
		name     string
		parentID *string
	}
	nodes := make(map[string]node, len(rows))
	for _, r := range rows {
		nodes[r.ID] = node{name: r.Name, parentID: r.ParentID}
	}
	out := make(map[string]string, len(rows))
	for id := range nodes {
		var parts []string
		// The same 64-deep cycle guard the importer uses, for the same reason:
		// a walk that trusts the closure table is a walk that hangs if the
		// closure table is ever wrong.
		for cur, depth := id, 0; depth < 64; depth++ {
			n, ok := nodes[cur]
			if !ok {
				break
			}
			parts = append(parts, n.name)
			if n.parentID == nil {
				for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
					parts[i], parts[j] = parts[j], parts[i]
				}
				out[id] = strings.Join(parts, "/")
				break
			}
			cur = *n.parentID
		}
	}
	return out, nil
}

// customExportColumn is one custom-field column of an export: which
// definition it renders and the header it renders under.
type customExportColumn struct {
	FieldID string
	Code    string
	Header  string
}

// customFieldColumns returns one column per custom_field definition of
// entityType -- live and retired together, per docs/custom-fields-design.md
// §7 -- and every value those columns need for exactly the entity ids given,
// keyed by field id and then entity id.
//
// ONE QUERY PER ID CHUNK, EACH OF WHICH NAMES BOTH THE COLUMN SET AND THE
// VALUES IN THE SAME STATEMENT. The alternative -- list definitions, then
// separately list values -- is exactly the shape this work package's own
// design doc names as the failure that cost eight fix rounds: a payload
// assembled from two reads taken at different moments, where a field created
// or retired between them leaves the header and the cells describing two
// different instants. A single SELECT is one snapshot on both engines even
// with no explicit transaction, so folding the value lookup into the same
// LEFT JOIN that produces the column list closes the gap structurally rather
// than by discipline.
//
// The LEFT JOIN starts from custom_field, not from custom_field_value, so a
// field with no value among these ids still produces its column -- the whole
// point of exporting live AND retired fields is that a column exists whether
// or not this particular page happens to hold a value for it. Chunked by
// idChunkSize for the same reason every other IN (...) list here is: a driver
// parameter limit is not a fictional concern once the id list is unbounded.
// Every chunk repeats the full column set (cheap at the tens-of-fields scale
// design.md §4 already accepts for the registry's own per-field COUNT), and
// only the first chunk's columns are kept -- later chunks are guaranteed to
// name the same fields in the same order, because ORDER BY cf.code is
// deterministic and entity_type does not vary within one call.
//
// ORDER BY cf.code IS THE STABLE COLUMN ORDER. Two exports of an unchanged
// set of field definitions produce byte-identical header order, so a diff
// between them is a diff of real content -- the same property ExportAssets
// already gives environments and ExportCircuits gives nothing else to sort.
//
// EVERY HEADER CARRIES ITS CODE, UNCONDITIONALLY -- design.md's Ruling AN,
// and there is no disambiguation pass left to call: a header built from
// "Label (code)" (or "Label (retired YYYY-MM-DD) (code)") depends on nothing
// but the one field it names and, for a retired one, when it was retired.
// Ruling AM tried appending the code only on a detected collision and that
// failed twice over: the appended form could itself collide with an
// untouched field that already happened to look like "Label (code)", and
// worse, a NEW field arriving with a colliding label silently renamed an
// EXISTING field's header from "Label" to "Label (code)" with nothing about
// that field having changed -- exactly what a diff or a header-keyed tool
// reads as a column removed and another added, denting §7's own promise
// that the export is where an operator goes to see what they still hold.
// Appending unconditionally removes both failure modes for a header's
// dependence on OTHER fields: it never changes because something else was
// created, retired or restored. It also outlives a rename -- Ruling M made
// label editable forever, so a header built from label alone moves whenever
// an administrator renames a field, and code is the thing that does not.
//
// THE CLAIM IS PRECISELY THIS MUCH AND NO MORE: a LIVE field's header cannot
// collide with any other field's, because code is unique among LIVE rows
// (custom_field_live_code_key, migration 00051, a PARTIAL index). It says
// nothing stronger, because nothing stronger is true. An earlier version of
// this comment claimed collision was impossible outright, and a real engine
// proved it wrong on two counts: §6 explicitly touts reusing a code after
// retirement as intended behaviour, so two RETIRED fields sharing a code AND
// a label are ordinary use, not abuse, and their headers were identical
// before Ruling AO added the retirement date below. And a LIVE field
// labelled literally "Support (retired)" with code "support" rendered
// identically to a RETIRED field labelled "Support" with the same code,
// because the marker was plain text with nothing tying it to an actual
// retirement.
//
// THE RETIREMENT DATE (Ruling AO) closes the second case completely: a live
// label a human chose to imitate the marker can happen to equal
// "Support (retired)" but cannot also equal a specific calendar date this
// field was never retired on, because that string names an event, not a
// phrase anyone types by hand. It closes the first case down to exactly one
// residual line, accepted rather than engineered away: two fields sharing a
// code, a label, AND a retirement date truncated to the same UTC day still
// collide. The field id would close it too but is unreadable in a header,
// and forbidding the substring "(retired)" in a label is a domain change
// into a task this one does not own -- the date closes the case that
// actually matters (b) for free and narrows the other (a) to a coincidence
// nobody has to design against.
func (s *SQLStore) customFieldColumns(ctx context.Context, entityType string, ids []string) ([]customExportColumn, map[string]map[string]string, error) {
	var cols []customExportColumn
	seen := make(map[string]bool)
	values := make(map[string]map[string]string)

	for _, chunk := range chunkIDs(ids) {
		var scanned []struct {
			FieldID   string  `db:"field_id"`
			Code      string  `db:"code"`
			Label     string  `db:"label"`
			RetiredAt *string `db:"retired_at"`
			EntityID  *string `db:"entity_id"`
			ValueText *string `db:"value_text"`
		}
		args := anySlice(chunk)
		args = append(args, entityType)
		query := `
			SELECT cf.id AS field_id, cf.code AS code, cf.label AS label, cf.retired_at AS retired_at,
			       cv.entity_id AS entity_id, cv.value_text AS value_text
			FROM custom_field cf
			LEFT JOIN custom_field_value cv
			  ON cv.field_id = cf.id AND cv.entity_id IN (` + placeholders(len(chunk)) + `)
			WHERE cf.entity_type = ?
			ORDER BY cf.code`
		if err := s.read(ctx, &scanned, query, args...); err != nil {
			return nil, nil, fmt.Errorf("reading custom field columns for %s export: %w", entityType, err)
		}
		for _, r := range scanned {
			if !seen[r.FieldID] {
				seen[r.FieldID] = true
				header := r.Label + " (" + r.Code + ")"
				if r.RetiredAt != nil {
					header = r.Label + " (retired " + retiredDate(*r.RetiredAt) + ") (" + r.Code + ")"
				}
				cols = append(cols, customExportColumn{FieldID: r.FieldID, Code: r.Code, Header: header})
			}
			if r.EntityID == nil || r.ValueText == nil {
				continue
			}
			byField, ok := values[r.FieldID]
			if !ok {
				byField = make(map[string]string)
				values[r.FieldID] = byField
			}
			byField[*r.EntityID] = *r.ValueText
		}
	}
	return cols, values, nil
}

// retiredDate extracts the calendar day (YYYY-MM-DD) from a retired_at
// timestamp for a retired column's header -- design.md's Ruling AO. Stored
// timestamps are RFC3339 UTC written by domain.FormatTime (never by the
// database, per this repo's timestamp rule), so the date is always the first
// ten bytes; the length guard is defensive rather than expected to fire, and
// falls back to the raw value rather than panicking on a slice bound if it
// ever does.
func retiredDate(retiredAt string) string {
	if len(retiredAt) < 10 {
		return retiredAt
	}
	return retiredAt[:10]
}

// customFieldCells renders one entity's custom-field cells in cols' order,
// each defused through csvsafe.Cell.
//
// NOT DEFENCE IN DEPTH AGAINST A SECOND CONSUMER OF Table -- there isn't one.
// Every Table this package builds is read exactly once, by render.CSV, which
// already defuses every cell of every row (including these) at the boundary
// where the file is written. This call exists because
// TestACustomValueIsDefusedLikeEveryOtherCell asserts against ExportAssets'
// and ExportServices' RETURNED Table directly, never going through
// render.CSV -- so the Table itself has to already carry a defused value for
// that assertion to mean anything. If that test, or the reason it bypasses
// render.CSV, ever goes away, this call can go with it; until then, deleting
// it as a tidy-up breaks a test without leaving any clue why it existed,
// which is the whole reason this comment is here instead of "belt and
// braces".
func customFieldCells(cols []customExportColumn, values map[string]map[string]string, entityID string) []string {
	cells := make([]string, len(cols))
	for i, c := range cols {
		if byField, ok := values[c.FieldID]; ok {
			cells[i] = csvsafe.Cell(byField[entityID])
		}
	}
	return cells
}

// ExportAssets renders an asset list as an importable table.
//
// The header is the importer's own column set, in the order docs/IMPORT.md
// documents, followed by one column per custom field definition -- see
// customFieldColumns. A column the importer does not accept does not belong
// among the first group: it would make the file look round-trippable and fail
// on load. The custom-field columns are exempt from that rule by design
// (design.md §7): they are not in scope for the importer at all.
func (s *SQLStore) ExportAssets(ctx context.Context, rows []AssetRow) (Table, error) {
	paths, err := s.AssetPaths(ctx)
	if err != nil {
		return Table{}, err
	}
	// AssetRow.DeviceTypeLabel is the DISPLAY name -- "Hewlett Packard
	// Enterprise ProLiant DL380 Gen11" -- and the importer resolves
	// `manufacturer_code/model`. Emitting the label would produce a file that
	// looks round-trippable and fails on load, which is worse than omitting the
	// column, so the real reference is resolved here.
	var refs []struct {
		ID  string `db:"id"`
		Ref string `db:"ref"`
	}
	if err := s.read(ctx, &refs, `
		SELECT dt.id AS id, mf.code || '/' || dt.model AS ref
		FROM device_type dt JOIN manufacturer mf ON mf.id = dt.manufacturer_id`); err != nil {
		return Table{}, fmt.Errorf("reading device type references: %w", err)
	}
	deviceRef := make(map[string]string, len(refs))
	for _, r := range refs {
		deviceRef[r.ID] = r.Ref
	}

	ids := make([]string, len(rows))
	for i, a := range rows {
		ids[i] = a.ID
	}
	cfCols, cfValues, err := s.customFieldColumns(ctx, domain.CustomFieldEntityAsset, ids)
	if err != nil {
		return Table{}, err
	}

	t := Table{
		Name: "assets",
		Header: append([]string{
			"name", "kind", "parent", "serial", "asset_tag", "vendor", "model",
			"lifecycle", "eol_date", "environments", "team", "manager_role", "device_type",
		}, headersOf(cfCols)...),
	}
	for _, a := range rows {
		parent := ""
		if a.ParentID != nil {
			parent = paths[*a.ParentID]
		}
		envs := make([]string, 0, len(a.Environments))
		for _, e := range a.Environments {
			envs = append(envs, e.Code)
		}
		sort.Strings(envs) // stable across runs, so a diff of two exports is real
		row := []string{
			a.Name, a.Kind, parent,
			orEmpty1(a.Serial), orEmpty1(a.AssetTag), orEmpty1(a.Vendor), orEmpty1(a.Model),
			a.Lifecycle, orEmpty1(a.EOLDate),
			strings.Join(envs, ","),
			a.TeamCode, orEmpty1(a.ManagerRole),
			deviceRefOf(deviceRef, a.DeviceTypeID),
		}
		row = append(row, customFieldCells(cfCols, cfValues, a.ID)...)
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

// headersOf extracts the header strings of a column list, in order.
func headersOf(cols []customExportColumn) []string {
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.Header
	}
	return headers
}

// ExportServices renders a service list, with one column per custom field
// definition appended -- live and retired together, same as ExportAssets.
//
// A METHOD, NOT A FREE FUNCTION, and that is the signature change this task
// owns: it needs the database to resolve the custom-field column set, exactly
// the reason ExportAssets already takes a context.
func (s *SQLStore) ExportServices(ctx context.Context, rows []ServiceRow) (Table, error) {
	ids := make([]string, len(rows))
	for i, svc := range rows {
		ids[i] = svc.ID
	}
	cfCols, cfValues, err := s.customFieldColumns(ctx, domain.CustomFieldEntityService, ids)
	if err != nil {
		return Table{}, err
	}

	t := Table{
		Name: "services",
		Header: append([]string{"code", "name", "tier", "availability", "environment", "team", "lifecycle"},
			headersOf(cfCols)...),
	}
	for _, svc := range rows {
		row := []string{
			svc.Code, svc.Name, strconv.Itoa(svc.Tier), svc.Availability,
			svc.EnvironmentCode, svc.TeamCode, svc.Lifecycle,
		}
		row = append(row, customFieldCells(cfCols, cfValues, svc.ID)...)
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

// ExportCircuits renders a circuit list.
func ExportCircuits(rows []CircuitRow) Table {
	t := Table{
		Name: "circuits",
		Header: []string{"cid", "provider", "service_type", "commit_mbps",
			"install_date", "contract_end", "lifecycle"},
	}
	for _, c := range rows {
		commit := ""
		if c.CommitMbps != nil {
			commit = strconv.Itoa(*c.CommitMbps)
		}
		t.Rows = append(t.Rows, []string{
			c.CID, c.ProviderName, orEmpty1(c.ServiceType), commit,
			orEmpty1(c.InstallDate), orEmpty1(c.ContractEnd), c.Lifecycle,
		})
	}
	return t
}

// ExportPrefixes renders the prefix tree.
//
// THE TREE ROWS, not the flat ones, because that is what the page shows and a
// file that disagreed with the screen it was downloaded from would be worse
// than no file. Depth is emitted as a column rather than as indentation: a
// spreadsheet sorts and filters, and leading spaces in a cidr would break both.
//
// No importer accepts prefixes yet, so the columns are chosen to be READ rather
// than to round-trip -- and saying so here stops somebody assuming otherwise.
func ExportPrefixes(rows []PrefixTreeRow) Table {
	t := Table{
		Name: "prefixes",
		Header: []string{"cidr", "depth", "vrf", "role", "environment", "vlan",
			"addresses", "utilisation_percent", "next_free"},
	}
	for _, p := range rows {
		vlan := p.VLANName
		if p.VLANVID != nil {
			vlan = strings.TrimSpace(strconv.Itoa(*p.VLANVID) + " " + p.VLANName)
		}
		next := ""
		if p.HasNextFree {
			next = p.NextFree
		}
		t.Rows = append(t.Rows, []string{
			p.CIDRText, strconv.Itoa(p.Depth), p.VRFName, orEmpty1(p.Role),
			p.EnvironmentCode, vlan, strconv.Itoa(p.Addresses),
			strconv.FormatFloat(p.UtilPercent(), 'f', 1, 64), next,
		})
	}
	return t
}

// deviceRefOf resolves an asset's catalogued model to the importer's form.
func deviceRefOf(refs map[string]string, id *string) string {
	if id == nil {
		return ""
	}
	return refs[*id]
}

func orEmpty1(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
