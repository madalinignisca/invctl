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

// ExportAssets renders an asset list as an importable table.
//
// The header is the importer's own column set, in the order docs/IMPORT.md
// documents. A column the importer does not accept does not belong here: it
// would make the file look round-trippable and fail on load.
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
	t := Table{
		Name: "assets",
		Header: []string{
			"name", "kind", "parent", "serial", "asset_tag", "vendor", "model",
			"lifecycle", "eol_date", "environments", "team", "manager_role", "device_type",
		},
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
		t.Rows = append(t.Rows, []string{
			a.Name, a.Kind, parent,
			orEmpty1(a.Serial), orEmpty1(a.AssetTag), orEmpty1(a.Vendor), orEmpty1(a.Model),
			a.Lifecycle, orEmpty1(a.EOLDate),
			strings.Join(envs, ","),
			a.TeamCode, orEmpty1(a.ManagerRole),
			deviceRefOf(deviceRef, a.DeviceTypeID),
		})
	}
	return t, nil
}

// ExportServices renders a service list.
func ExportServices(rows []ServiceRow) Table {
	t := Table{
		Name:   "services",
		Header: []string{"code", "name", "tier", "availability", "environment", "team", "lifecycle"},
	}
	for _, svc := range rows {
		t.Rows = append(t.Rows, []string{
			svc.Code, svc.Name, strconv.Itoa(svc.Tier), svc.Availability,
			svc.EnvironmentCode, svc.TeamCode, svc.Lifecycle,
		})
	}
	return t
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
