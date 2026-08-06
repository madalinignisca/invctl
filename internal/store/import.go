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
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Bulk import of assets from a CSV file.
//
// The reason this exists at all: until migration 00021 an asset had no name a
// file could use. It has one now -- unique among its live siblings -- and a path
// through the containment tree spells it out, so `dc-a/rack-1/esx-01` names
// exactly one asset without anybody knowing a UUID.
//
// THREE PROPERTIES, and each one is a decision rather than an implementation
// detail:
//
//  1. CREATE ONLY. A row naming an asset that already exists is a collision and
//     is reported, not applied. Update-by-import is a much larger question about
//     the audit trail -- a file that silently rewrites four hundred assets writes
//     four hundred change_log rows nobody reviewed -- and loosening this later is
//     additive in a way that tightening it would not be.
//
//  2. WHOLE-FILE REFUSAL. Every row shares one transaction. A partially applied
//     import is the worst outcome available: the operator cannot tell what landed,
//     and re-running it collides with its own successful half.
//
//  3. THE DRY RUN IS THE REAL THING, ROLLED BACK. It runs the actual inserts,
//     against the actual vocabulary checks, closure maintenance and unique
//     indexes, inside a transaction that is then discarded. The alternative --
//     simulating what would happen -- is a second implementation of the write
//     path, and the two would agree right up until the moment a difference
//     mattered.

// assetImportColumns are the headers a file may carry. `name` and `kind` are
// required; everything else is optional and an absent column is not an error.
//
// DELIBERATELY NOT EVERY COLUMN. Team and manager role are missing because both
// are references that need their own natural key to be nameable in a file, and
// guessing one now would be a second key to live with. Attributes are missing
// because attrs is opaque by design. Both are additive later.
var assetImportColumns = map[string]bool{
	"parent": true, "name": true, "kind": true, "serial": true,
	"asset_tag": true, "vendor": true, "model": true,
	"lifecycle": true, "eol_date": true, "environments": true,
}

// AssetImportRow is one line of the file, before anything has been resolved.
type AssetImportRow struct {
	// Line is the line number in the uploaded file, counting the header, so a
	// problem can be pointed at rather than described.
	Line int
	// Parent is a path through the containment tree, empty for a top-level
	// asset.
	Parent       string
	Name         string
	Kind         string
	Serial       string
	AssetTag     string
	Vendor       string
	Model        string
	Lifecycle    string
	EOLDate      string
	Environments string
}

// Path is where this row's asset would sit, which is also its natural key.
func (r AssetImportRow) Path() string {
	if r.Parent == "" {
		return r.Name
	}
	return r.Parent + "/" + r.Name
}

// ImportProblem is one reason a file was not applied.
//
// Line and Path are both carried because they answer different questions: the
// line is where to go and fix it, the path is what it was trying to say.
type ImportProblem struct {
	Line    int
	Path    string
	Field   string
	Message string
}

// ImportReport is what an import run produces, applied or not.
type ImportReport struct {
	// Filename is what the operator uploaded, echoed back so a report read
	// later says which file it is about.
	Filename string
	// DryRun records whether the transaction was rolled back. It is on the
	// report rather than only on the request so that a rendered result can
	// never claim to have written something it discarded.
	DryRun bool
	// Created lists the paths that were, or would be, created -- in the order
	// they were written, which is parents before children.
	Created []string
	// Problems is every reason the file was refused. It is deliberately the
	// WHOLE list rather than the first failure: an operator fixing a
	// four-hundred-line file one error per upload is an operator who gives up.
	Problems []ImportProblem
	// Rows is how many data rows the file held, so "0 created, 0 problems" can
	// be distinguished from an empty file.
	Rows int
}

// Applied reports whether anything was actually written.
func (r *ImportReport) Applied() bool {
	return !r.DryRun && len(r.Problems) == 0 && len(r.Created) > 0
}

// ParseAssetCSV reads an asset import file.
//
// Header-driven rather than positional: a file whose columns are in a different
// order is a file somebody exported from a spreadsheet, not a mistake. An
// unknown column IS an error, because the alternative is silently ignoring the
// one column somebody cared about -- a misspelled `lifecyle` that vanishes is
// exactly the silent-fallback shape this codebase keeps finding.
func ParseAssetCSV(r io.Reader) ([]AssetImportRow, []ImportProblem) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	// Rows may legitimately differ in length only if the file is malformed;
	// FieldsPerRecord left at 0 makes the header set the expectation and the
	// reader report any row that disagrees.

	records, err := reader.ReadAll()
	if err != nil {
		return nil, []ImportProblem{{
			Line:    0,
			Message: fmt.Sprintf("this file could not be read as CSV: %v", err),
		}}
	}
	if len(records) == 0 {
		return nil, []ImportProblem{{Line: 0, Message: "the file is empty"}}
	}

	var problems []ImportProblem
	index := map[string]int{}
	for i, raw := range records[0] {
		name := strings.ToLower(strings.TrimSpace(raw))
		name = strings.TrimPrefix(name, "\ufeff") // a spreadsheet's byte-order mark
		if name == "" {
			continue
		}
		if !assetImportColumns[name] {
			problems = append(problems, ImportProblem{
				Line: 1, Field: name,
				Message: fmt.Sprintf("there is no asset column called %q; known columns are %s",
					name, knownColumns()),
			})
			continue
		}
		if _, dup := index[name]; dup {
			problems = append(problems, ImportProblem{
				Line: 1, Field: name,
				Message: fmt.Sprintf("the column %q appears twice", name),
			})
			continue
		}
		index[name] = i
	}
	for _, required := range []string{"name", "kind"} {
		if _, ok := index[required]; !ok {
			problems = append(problems, ImportProblem{
				Line: 1, Field: required,
				Message: fmt.Sprintf("the file has no %q column, and an asset cannot be created without one", required),
			})
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}

	at := func(record []string, column string) string {
		i, ok := index[column]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}

	rows := make([]AssetImportRow, 0, len(records)-1)
	for n, record := range records[1:] {
		line := n + 2 // one-based, and the header is line 1
		row := AssetImportRow{
			Line:         line,
			Parent:       strings.Trim(at(record, "parent"), "/"),
			Name:         at(record, "name"),
			Kind:         at(record, "kind"),
			Serial:       at(record, "serial"),
			AssetTag:     at(record, "asset_tag"),
			Vendor:       at(record, "vendor"),
			Model:        at(record, "model"),
			Lifecycle:    at(record, "lifecycle"),
			EOLDate:      at(record, "eol_date"),
			Environments: at(record, "environments"),
		}
		// A wholly blank line is a spreadsheet artefact, not an intention.
		if row == (AssetImportRow{Line: line}) {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func knownColumns() string {
	out := make([]string, 0, len(assetImportColumns))
	for name := range assetImportColumns {
		out = append(out, name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// errDryRun unwinds the transaction after a successful dry run. It never
// reaches a caller: ImportAssets recognises it and returns the report instead.
var errDryRun = errors.New("import: dry run, rolling back")

// errRefused unwinds the transaction when the file had problems. Same idea.
var errRefused = errors.New("import: file refused")

// ImportAssets applies an asset file, or reports why it will not.
//
// Returns a report and, separately, an error. The two mean different things and
// conflating them is how an import surface starts lying: the ERROR is "this run
// could not be carried out" -- the database was unreachable, the actor could not
// write. A file full of unresolvable parents is not an error, it is a successful
// run whose answer is no, and the report carries the reasons.
func (s *SQLStore) ImportAssets(ctx context.Context, actor domain.Actor, rows []AssetImportRow, dryRun bool) (*ImportReport, error) {
	report := &ImportReport{DryRun: dryRun, Rows: len(rows)}
	if len(rows) == 0 {
		report.Problems = append(report.Problems, ImportProblem{
			Message: "the file has a header but no rows",
		})
		return report, nil
	}

	// Duplicate paths WITHIN the file, before touching the database. Two rows
	// claiming dc-a/rack-1 cannot both be right, and the unique index would
	// report the second one as colliding with the first -- true, but it would
	// read as though the estate already contained it.
	seen := map[string]int{}
	for _, row := range rows {
		if row.Name == "" {
			report.Problems = append(report.Problems, ImportProblem{
				Line: row.Line, Field: "name", Message: "this row has no name",
			})
			continue
		}
		if first, dup := seen[row.Path()]; dup {
			report.Problems = append(report.Problems, ImportProblem{
				Line: row.Line, Path: row.Path(), Field: "name",
				Message: fmt.Sprintf("line %d already claims this path", first),
			})
			continue
		}
		seen[row.Path()] = row.Line
	}

	err := s.write(ctx, actor, func(t *tx) error {
		existing, err := existingAssetPaths(ctx, t)
		if err != nil {
			return err
		}
		environments, err := environmentIDsByCode(ctx, t)
		if err != nil {
			return err
		}

		// Rows resolve against the estate PLUS everything created so far in
		// this run, so a file may introduce a site and the racks inside it. The
		// order rows appear in is not required to be topological -- people sort
		// spreadsheets by name -- so this makes repeated passes and stops when
		// a pass resolves nothing new.
		pending := append([]AssetImportRow(nil), rows...)
		created := map[string]string{} // path -> new id

		for len(pending) > 0 {
			var deferred []AssetImportRow
			progress := false

			for _, row := range pending {
				if row.Name == "" {
					continue // already reported above
				}
				var parentID *string
				if row.Parent != "" {
					id, ok := existing[row.Parent]
					if !ok {
						id, ok = created[row.Parent]
					}
					if !ok {
						deferred = append(deferred, row)
						continue
					}
					parentID = &id
				}

				progress = true
				if _, clash := existing[row.Path()]; clash {
					report.Problems = append(report.Problems, ImportProblem{
						Line: row.Line, Path: row.Path(), Field: "name",
						Message: "this already exists; import creates, it does not update",
					})
					continue
				}

				asset, envIDs, envCodes, problems := buildImportedAsset(row, parentID, environments, s.Now())
				if len(problems) > 0 {
					report.Problems = append(report.Problems, problems...)
					continue
				}
				if err := s.insertAsset(ctx, t, asset, envIDs, envCodes); err != nil {
					var ve *domain.ValidationError
					if errors.As(err, &ve) {
						for _, f := range ve.Fields {
							report.Problems = append(report.Problems, ImportProblem{
								Line: row.Line, Path: row.Path(),
								Field: f.Field, Message: f.Message,
							})
						}
						continue
					}
					return fmt.Errorf("importing %s (line %d): %w", row.Path(), row.Line, err)
				}
				created[row.Path()] = asset.ID
				report.Created = append(report.Created, row.Path())
			}

			if !progress {
				// Everything left names a parent that does not exist and is not
				// being created. Report each one rather than the set, so the
				// operator sees which line to fix.
				for _, row := range deferred {
					report.Problems = append(report.Problems, ImportProblem{
						Line: row.Line, Path: row.Path(), Field: "parent",
						Message: fmt.Sprintf("there is no asset at %q, and nothing in this file creates one", row.Parent),
					})
				}
				break
			}
			pending = deferred
		}

		if len(report.Problems) > 0 {
			return errRefused
		}
		if dryRun {
			return errDryRun
		}
		return nil
	})

	switch {
	case errors.Is(err, errDryRun), errors.Is(err, errRefused):
		// Both are how this function unwinds a transaction on purpose. The
		// report already says which.
	case err != nil:
		return nil, err
	}

	// A refused file created nothing, whatever the loop managed before it hit
	// the problem. Saying otherwise would be the report describing a
	// transaction that no longer exists.
	if len(report.Problems) > 0 {
		report.Created = nil
	}
	return report, nil
}

// buildImportedAsset turns a row into an asset, collecting EVERY field problem
// rather than stopping at the first.
//
// One row with four bad fields should produce four lines in the report. The
// domain constructor already works that way; this keeps the same contract at the
// file level, for the same reason -- an operator who has to re-upload once per
// mistake stops using the feature.
func buildImportedAsset(row AssetImportRow, parentID *string, environments map[string]string, now time.Time) (*domain.Asset, []string, []string, []ImportProblem) {
	var problems []ImportProblem
	add := func(field, format string, args ...any) {
		problems = append(problems, ImportProblem{
			Line: row.Line, Path: row.Path(), Field: field,
			Message: fmt.Sprintf(format, args...),
		})
	}

	asset, err := domain.NewAsset(NewID(), row.Kind, row.Name, parentID, now)
	if err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			for _, f := range ve.Fields {
				add(f.Field, "%s", f.Message)
			}
		} else {
			add("", "%s", err.Error())
		}
		return nil, nil, nil, problems
	}

	asset.Serial = optionalText(row.Serial)
	asset.AssetTag = optionalText(row.AssetTag)
	asset.Vendor = optionalText(row.Vendor)
	asset.Model = optionalText(row.Model)
	asset.EOLDate = optionalText(row.EOLDate)
	if row.Lifecycle != "" {
		asset.Lifecycle = row.Lifecycle
	}

	// Validate covers lifecycle and the EOL date shape. The DB CHECK behind it
	// is the second line of defence, and a constraint failure mid-file would
	// report a line number and a driver message rather than a field.
	if err := asset.Validate(); err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			for _, f := range ve.Fields {
				add(f.Field, "%s", f.Message)
			}
		} else {
			add("", "%s", err.Error())
		}
	}

	var envIDs, envCodes []string
	for _, code := range splitList(row.Environments) {
		id, ok := environments[strings.ToLower(code)]
		if !ok {
			add("environments", "there is no environment with the code %q", code)
			continue
		}
		envIDs = append(envIDs, id)
		envCodes = append(envCodes, code)
	}

	if len(problems) > 0 {
		return nil, nil, nil, problems
	}
	return asset, envIDs, envCodes, nil
}

// splitList reads a comma-separated cell, dropping blanks.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// existingAssetPaths maps every LIVE asset's containment path to its id.
//
// Live only, and that matches the natural key exactly: a retired asset does not
// hold its name against the index, so it must not hold it against an import
// either. If it did, the file would be refused for colliding with something the
// database would have accepted.
//
// Built in Go from a flat read rather than with a recursive query, because a
// recursive CTE is the kind of SQL that diverges between the two engines and
// this is the one thing that must not.
func existingAssetPaths(ctx context.Context, t *tx) (map[string]string, error) {
	var rows []struct {
		ID       string  `db:"id"`
		Name     string  `db:"name"`
		ParentID *string `db:"parent_id"`
	}
	err := t.selectAll(ctx, &rows,
		`SELECT id, name, parent_id FROM asset WHERE lifecycle <> ?`, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("reading the existing tree: %w", err)
	}

	type node struct {
		name     string
		parentID *string
	}
	nodes := make(map[string]node, len(rows))
	for _, r := range rows {
		nodes[r.ID] = node{name: r.Name, parentID: r.ParentID}
	}

	paths := make(map[string]string, len(rows))
	for id := range nodes {
		var parts []string
		// The depth bound is a cycle guard. asset_closure and ReparentAsset
		// already make a cycle unreachable, but a walk that trusts that is a
		// walk that hangs the import if it is ever wrong.
		for cur, depth := id, 0; depth < 64; depth++ {
			n, ok := nodes[cur]
			if !ok {
				break // a retired ancestor: the path is not resolvable, so skip it
			}
			parts = append(parts, n.name)
			if n.parentID == nil {
				for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
					parts[i], parts[j] = parts[j], parts[i]
				}
				paths[strings.Join(parts, "/")] = id
				break
			}
			cur = *n.parentID
		}
	}
	return paths, nil
}

// environmentIDsByCode maps lower-cased environment codes to ids, so a file can
// say `prod` without knowing a UUID.
func environmentIDsByCode(ctx context.Context, t *tx) (map[string]string, error) {
	var rows []struct {
		ID   string `db:"id"`
		Code string `db:"code"`
	}
	if err := t.selectAll(ctx, &rows, `SELECT id, code FROM environment`); err != nil {
		return nil, fmt.Errorf("reading environments: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.Code)] = r.ID
	}
	return out, nil
}
