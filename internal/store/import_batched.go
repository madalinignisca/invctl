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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Writing an import in batches instead of one transaction.
//
// WHY THIS REPLACED ONE BIG TRANSACTION. Whole-file-or-nothing is a property
// worth having and one transaction is not the only way to get it -- I conflated
// the two. Measured at about 1.4ms a row, twenty-five thousand assets is
// thirty-five seconds during which SQLite's single writer is held and NOBODY
// else can save anything. That is not a cost to document, it is a cost to
// remove.
//
// So: validate the whole file first against a snapshot read once, then write in
// batches of a couple of hundred. Each batch holds the writer for a few hundred
// milliseconds, and other people's saves interleave normally.
//
// WHAT THE PROMISE BECOMES, stated honestly rather than glossed. It was "the
// whole file applies or none of it does". It is now "the whole file applies or
// none of it does, unless something changes underneath you mid-import -- in
// which case we stop and tell you exactly where". The validation pass catches
// everything a write can fail on except a concurrent writer taking a name in
// the seconds between checking and writing, which is rare and reportable.
//
// It matters here because this codebase has no hard deletes: if batch forty of
// fifty fails there is no undo. Retiring what landed would leave four thousand
// retired assets, which is worse than a partial estate somebody can see and
// finish -- and finishing is safe, because import CREATES and never updates, so
// re-running the same file skips what already exists.

// ImportBatchSize is how many rows share a transaction.
//
// Two hundred is about a quarter of a second of write lock at measured speed:
// short enough that a person saving a form during an import does not notice,
// long enough that the per-transaction overhead stays irrelevant.
const ImportBatchSize = 200

// ImportAssetsBatched validates the whole file, then writes it in batches.
//
// Used by the background job. The preview still runs ImportAssets, which is one
// transaction and rolls back -- a preview is somebody standing there waiting,
// it writes nothing, and its lock is as short as the work.
func (s *SQLStore) ImportAssetsBatched(ctx context.Context, actor domain.Actor,
	rows []AssetImportRow, progress func(done int)) (*ImportReport, error) {

	report := &ImportReport{Rows: len(rows)}
	if len(rows) == 0 {
		report.Problems = append(report.Problems, ImportProblem{
			Message: "the file has a header but no rows",
		})
		return report, nil
	}

	// PARENTS BEFORE CHILDREN, by sorting on path depth. The single-transaction
	// path could defer a row and retry it in a later pass because everything was
	// uncommitted together; across batches a parent has to be committed before
	// its child is written, and depth order guarantees that without any passes
	// at all.
	ordered := append([]AssetImportRow(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.Count(ordered[i].Path(), "/") < strings.Count(ordered[j].Path(), "/")
	})

	// The snapshot, read once. Everything a row is checked against.
	var existing, teams, environments map[string]string
	var models deviceTypeIndex
	if err := s.write(ctx, actor, func(t *tx) error {
		var err error
		if existing, err = existingAssetPaths(ctx, t); err != nil {
			return err
		}
		if teams, err = teamIDsByCode(ctx, t); err != nil {
			return err
		}
		if environments, err = environmentIDsByCode(ctx, t); err != nil {
			return err
		}
		models, err = loadDeviceTypeIndex(ctx, t)
		return err
	}); err != nil {
		return nil, fmt.Errorf("reading the estate: %w", err)
	}

	// VALIDATION, whole file, before anything is written. Duplicate paths, rows
	// that collide with the estate, unresolvable parents, unreadable fields --
	// all of it, against the snapshot and the rows themselves.
	planned := map[string]bool{}
	seen := map[string]int{}
	built := make([]*domain.Asset, 0, len(ordered))
	envIDs := make([][]string, 0, len(ordered))
	envCodes := make([][]string, 0, len(ordered))
	keep := make([]AssetImportRow, 0, len(ordered))

	for _, row := range ordered {
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

		if _, clash := existing[row.Path()]; clash {
			report.Problems = append(report.Problems, ImportProblem{
				Line: row.Line, Path: row.Path(), Field: "name",
				Message: "this already exists; import creates, it does not update",
			})
			continue
		}

		var parentID *string
		if row.Parent != "" {
			if id, ok := existing[row.Parent]; ok {
				parentID = &id
			} else if !planned[row.Parent] {
				report.Problems = append(report.Problems, ImportProblem{
					Line: row.Line, Path: row.Path(), Field: "parent",
					Message: fmt.Sprintf("there is no asset at %q, and nothing in this file creates one", row.Parent),
				})
				continue
			}
		}

		asset, ids, codes, problems := buildImportedAsset(row, parentID, environments, teams, models, s.Now())
		if len(problems) > 0 {
			report.Problems = append(report.Problems, problems...)
			continue
		}
		planned[row.Path()] = true
		built = append(built, asset)
		envIDs = append(envIDs, ids)
		envCodes = append(envCodes, codes)
		keep = append(keep, row)
	}

	if len(report.Problems) > 0 {
		// Refused before a single write. Nothing to undo, and the whole list is
		// reported rather than the first fault.
		return report, nil
	}

	// THE WRITE, in batches. A parent written in an earlier batch is resolved
	// out of `created` rather than re-read, so the ordering above is what makes
	// this correct.
	created := map[string]string{}
	done := 0
	for start := 0; start < len(built); start += ImportBatchSize {
		end := min(start+ImportBatchSize, len(built))

		// Ids written INSIDE this batch, kept apart from `created` until it
		// commits. A child whose parent is three rows above it in the same
		// batch has to find that parent -- the first version only merged after
		// the commit, so every intra-batch parent looked missing -- and if the
		// batch rolls back these ids have to go with it.
		pending := map[string]string{}

		err := s.write(ctx, actor, func(t *tx) error {
			for i := start; i < end; i++ {
				row, asset := keep[i], built[i]
				if row.Parent != "" && asset.ParentID == nil {
					id, ok := created[row.Parent]
					if !ok {
						id, ok = pending[row.Parent]
					}
					if !ok {
						return fmt.Errorf("%s: parent %q was not written", row.Path(), row.Parent)
					}
					asset.ParentID = &id
				}
				if err := s.insertAsset(ctx, t, asset, envIDs[i], envCodes[i]); err != nil {
					var ve *domain.ValidationError
					if errors.As(err, &ve) {
						for _, f := range ve.Fields {
							report.Problems = append(report.Problems, ImportProblem{
								Line: row.Line, Path: row.Path(), Field: f.Field, Message: f.Message,
							})
						}
						return errRefused
					}
					return fmt.Errorf("importing %s (line %d): %w", row.Path(), row.Line, err)
				}
				pending[row.Path()] = asset.ID
			}
			return nil
		})
		if err != nil && !errors.Is(err, errRefused) {
			report.Problems = append(report.Problems, ImportProblem{
				Message: fmt.Sprintf("stopped after %d rows: %v", done, err),
			})
			report.PartialRows = done
			return report, nil
		}
		if err != nil {
			// A validation failure the snapshot could not have seen -- almost
			// always somebody else taking a name while this ran. The batch rolled
			// back; earlier ones did not.
			report.PartialRows = done
			return report, nil
		}

		for i := start; i < end; i++ {
			created[keep[i].Path()] = built[i].ID
			report.Created = append(report.Created, keep[i].Path())
		}

		done = end
		if progress != nil {
			progress(done)
		}
	}
	return report, nil
}
