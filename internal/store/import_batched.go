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
//
// Takes a domain.Permit, not a domain.Actor -- WP-G1 Task 9. This runs on
// context.Background(), long after the request that queued it is gone, so
// there is no session to derive an authorization decision from at write time.
// The caller (internal/web/handlers/import_runner.go) mints the permit ONCE,
// from whoever submitted the file, and hands it here unchanged: minting
// domain.AdministratorPermit internally -- the way the not-yet-converted
// methods in this package still do -- would turn "upload a CSV" into the
// route by which a project owner acquires estate-wide write, which is exactly
// the privilege escalation this task exists to close off before Task 10's
// mass conversion buries it under 148 other call sites.
func (s *SQLStore) ImportAssetsBatched(ctx context.Context, permit domain.Permit,
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
	if err := s.write(ctx, permit, func(t *tx) error {
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
		// wrote tracks which offsets in [start,end) actually landed, so the
		// bookkeeping below never counts a row the permit refused as created.
		wrote := make([]bool, end-start)

		err := s.write(ctx, permit, func(t *tx) error {
			for i := start; i < end; i++ {
				row, asset := keep[i], built[i]

				// CHECKED BEFORE insertAsset RUNS, not caught after. insertAsset's
				// own INSERT executes before its logCreate call reaches this same
				// Covers gate (tx.log is the audit chokepoint, and it necessarily
				// runs last -- see store.go's doc comment on log). A transaction
				// has no per-statement undo here: if the row's INSERT had already
				// run before a refusal surfaced, skipping the error and continuing
				// the loop would leave that INSERT uncommitted-but-executed inside
				// a transaction this loop goes on to commit -- an authorization
				// refusal that failed to prevent the write it refused. Asking the
				// same question the audit gate would ask, before doing anything
				// that needs undoing, is what makes "skip this row, keep the
				// batch" safe rather than a hole.
				if !permit.Covers("asset", asset.ID) {
					report.Problems = append(report.Problems, ImportProblem{
						Line: row.Line, Path: row.Path(),
						Message: "outside the submitter's permitted scope; not created",
					})
					continue
				}

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
				wrote[i-start] = true
			}
			return nil
		})
		// A BATCH THAT FAILED IS A STOP, not an error returned to the caller.
		// Earlier batches committed, so this is a real outcome the operator has
		// to be told about precisely -- how many rows landed -- rather than an
		// error that loses that number on its way up.
		switch {
		case errors.Is(err, errRefused):
			// A row the snapshot said was fine and the database refused: almost
			// always somebody else taking a name while this ran. The reasons are
			// already on the report; this batch rolled back, earlier ones did not.
			report.PartialRows = done
			return report, nil
		case err != nil:
			report.Problems = append(report.Problems, ImportProblem{
				Message: fmt.Sprintf("stopped after %d rows: %v", done, err),
			})
			report.PartialRows = done
			return report, nil
		}

		for i := start; i < end; i++ {
			if !wrote[i-start] {
				// Refused for this row alone -- see the errors.Is(err,
				// domain.ErrForbidden) branch above. Not created, not counted,
				// and NOT available as a parent for a later row.
				continue
			}
			created[keep[i].Path()] = built[i].ID
			report.Created = append(report.Created, keep[i].Path())
			done++
		}

		if progress != nil {
			progress(done)
		}
	}
	return report, nil
}

// ImportDeviceTypesBatched validates a catalogue file, then writes it in
// batches.
//
// Simpler than the asset version because a model has no parent: every row
// references a manufacturer that must already exist, so there is nothing to
// order and nothing to resolve between batches. The reason it batches anyway is
// the same one, and it is not about the size of a catalogue -- it is that an
// import must not be the one write in this application that can hold the
// database against everybody else. A rule with an exception in it is a rule
// somebody has to remember.
// ImportDeviceTypesBatched validates a catalogue file, then writes it in
// batches. Takes a domain.Permit for the same reason ImportAssetsBatched
// does -- see that function's doc comment.
func (s *SQLStore) ImportDeviceTypesBatched(ctx context.Context, permit domain.Permit,
	rows []DeviceTypeImportRow, progress func(done int)) (*ImportReport, error) {

	report := &ImportReport{Rows: len(rows)}
	if len(rows) == 0 {
		report.Problems = append(report.Problems, ImportProblem{
			Message: "the file has a header but no rows",
		})
		return report, nil
	}

	var makers map[string]manufacturerRef
	var existing map[string]string
	if err := s.write(ctx, permit, func(t *tx) error {
		var err error
		if makers, err = manufacturersByCode(ctx, t); err != nil {
			return err
		}
		existing, err = existingDeviceTypePaths(ctx, t)
		return err
	}); err != nil {
		return nil, fmt.Errorf("reading the catalogue: %w", err)
	}

	seen := map[string]int{}
	built := make([]*domain.DeviceType, 0, len(rows))
	names := make([]string, 0, len(rows))
	keep := make([]DeviceTypeImportRow, 0, len(rows))

	for _, row := range rows {
		maker, known := makers[row.Manufacturer]
		if problems := validateDeviceTypeRow(row, known); len(problems) > 0 {
			report.Problems = append(report.Problems, problems...)
			continue
		}
		if first, dup := seen[row.Path()]; dup {
			report.Problems = append(report.Problems, ImportProblem{
				Line: row.Line, Path: row.Path(), Field: "model",
				Message: fmt.Sprintf("line %d already claims this model", first),
			})
			continue
		}
		seen[row.Path()] = row.Line

		if _, clash := existing[row.Path()]; clash {
			report.Problems = append(report.Problems, ImportProblem{
				Line: row.Line, Path: row.Path(), Field: "model",
				Message: "this model is already catalogued; import creates, it does not update",
			})
			continue
		}

		d, problems := buildImportedDeviceType(row, maker.id, s.Now())
		if len(problems) > 0 {
			report.Problems = append(report.Problems, problems...)
			continue
		}
		built = append(built, d)
		names = append(names, maker.name)
		keep = append(keep, row)
	}

	if len(report.Problems) > 0 {
		return report, nil
	}

	done := 0
	for start := 0; start < len(built); start += ImportBatchSize {
		end := min(start+ImportBatchSize, len(built))

		wrote := make([]bool, end-start)

		err := s.write(ctx, permit, func(t *tx) error {
			for i := start; i < end; i++ {
				// See ImportAssetsBatched's identical check: asked BEFORE the
				// insert runs, because tx.log's Covers gate fires only at the
				// end of insertDeviceType, after the row is already written
				// inside this open transaction -- too late to skip just one
				// row without also undoing the INSERT that already ran.
				if !permit.Covers("device_type", built[i].ID) {
					report.Problems = append(report.Problems, ImportProblem{
						Line: keep[i].Line, Path: keep[i].Path(),
						Message: "outside the submitter's permitted scope; not created",
					})
					continue
				}
				if err := s.insertDeviceType(ctx, t, built[i], names[i]); err != nil {
					var ve *domain.ValidationError
					if errors.As(err, &ve) {
						for _, f := range ve.Fields {
							report.Problems = append(report.Problems, ImportProblem{
								Line: keep[i].Line, Path: keep[i].Path(),
								Field: f.Field, Message: f.Message,
							})
						}
						return errRefused
					}
					return fmt.Errorf("importing %s (line %d): %w", keep[i].Path(), keep[i].Line, err)
				}
				wrote[i-start] = true
			}
			return nil
		})
		switch {
		case errors.Is(err, errRefused):
			report.PartialRows = done
			return report, nil
		case err != nil:
			report.Problems = append(report.Problems, ImportProblem{
				Message: fmt.Sprintf("stopped after %d rows: %v", done, err),
			})
			report.PartialRows = done
			return report, nil
		}

		for i := start; i < end; i++ {
			if !wrote[i-start] {
				continue
			}
			report.Created = append(report.Created, keep[i].Path())
			done++
		}
		if progress != nil {
			progress(done)
		}
	}
	return report, nil
}
