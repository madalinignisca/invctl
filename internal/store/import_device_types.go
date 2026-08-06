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
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Bulk import of the hardware catalogue.
//
// Same three properties as the asset importer and the same machinery: create
// only, whole file or nothing, and a dry run that performs the real writes and
// rolls them back. What differs is the natural key and one reference.
//
// THE MANUFACTURER MUST ALREADY EXIST. A row naming an unknown one is refused,
// and it is worth saying why rather than quietly creating it from a bare code:
// a manufacturer created that way would have a code and no name, and the
// catalogue would fill with entries nobody chose. It is also the rule the asset
// importer already follows for teams and environments -- a reference to another
// KIND of thing must resolve; only the same kind (an asset's parent) may be
// created within the file.

// deviceTypeImportColumns are the headers a catalogue file may carry.
var deviceTypeImportColumns = map[string]bool{
	"manufacturer": true, "model": true, "part_number": true,
	"u_height": true, "full_depth": true, "eol_date": true,
	"notes": true, "lifecycle": true,
}

// DeviceTypeImportRow is one line of a catalogue file.
type DeviceTypeImportRow struct {
	Line int
	// Manufacturer is a manufacturer CODE, not a name: the code is the natural
	// key, and "Dell" versus "Dell Inc." is exactly the ambiguity a code exists
	// to remove.
	Manufacturer string
	Model        string
	PartNumber   string
	UHeight      string
	FullDepth    string
	EOLDate      string
	Notes        string
	Lifecycle    string
}

// Path is the natural key: dell/r650.
func (r DeviceTypeImportRow) Path() string { return r.Manufacturer + "/" + r.Model }

// ParseDeviceTypeCSV reads a catalogue import file.
func ParseDeviceTypeCSV(r io.Reader) ([]DeviceTypeImportRow, []ImportProblem) {
	data, at, problems := readCSVHeader(r, "device type", deviceTypeImportColumns,
		[]string{"manufacturer", "model"})
	if len(problems) > 0 {
		return nil, problems
	}

	rows := make([]DeviceTypeImportRow, 0, len(data))
	for n, record := range data {
		line := n + 2 // one-based, and the header is line 1
		row := DeviceTypeImportRow{
			Line:         line,
			Manufacturer: strings.ToLower(at(record, "manufacturer")),
			Model:        at(record, "model"),
			PartNumber:   at(record, "part_number"),
			UHeight:      at(record, "u_height"),
			FullDepth:    at(record, "full_depth"),
			EOLDate:      at(record, "eol_date"),
			Notes:        at(record, "notes"),
			Lifecycle:    at(record, "lifecycle"),
		}
		if row == (DeviceTypeImportRow{Line: line}) {
			continue // a blank line is a spreadsheet artefact, not an intention
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ImportDeviceTypes applies a catalogue file, or reports why it will not.
func (s *SQLStore) ImportDeviceTypes(ctx context.Context, actor domain.Actor,
	rows []DeviceTypeImportRow, dryRun bool) (*ImportReport, error) {

	report := &ImportReport{DryRun: dryRun, Rows: len(rows)}
	if len(rows) == 0 {
		report.Problems = append(report.Problems, ImportProblem{
			Message: "the file has a header but no rows",
		})
		return report, nil
	}

	// Duplicates within the file, before touching the database. Two rows
	// claiming dell/r650 cannot both be right, and leaving it to the unique
	// index would report the second as colliding with the estate -- sending the
	// operator to look for a row that is in their own file.
	seen := map[string]int{}
	for _, row := range rows {
		if row.Model == "" || row.Manufacturer == "" {
			continue // reported per-row below, where the field is known
		}
		if first, dup := seen[row.Path()]; dup {
			report.Problems = append(report.Problems, ImportProblem{
				Line: row.Line, Path: row.Path(), Field: "model",
				Message: fmt.Sprintf("line %d already claims this model", first),
			})
			continue
		}
		seen[row.Path()] = row.Line
	}

	err := s.write(ctx, actor, func(t *tx) error {
		makers, err := manufacturersByCode(ctx, t)
		if err != nil {
			return err
		}
		existing, err := existingDeviceTypePaths(ctx, t)
		if err != nil {
			return err
		}

		for _, row := range rows {
			maker, known := makers[row.Manufacturer]
			problems := validateDeviceTypeRow(row, known)
			if len(problems) > 0 {
				report.Problems = append(report.Problems, problems...)
				continue
			}
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
			if err := s.insertDeviceType(ctx, t, d, maker.name); err != nil {
				var ve *domain.ValidationError
				if errors.As(err, &ve) {
					for _, f := range ve.Fields {
						report.Problems = append(report.Problems, ImportProblem{
							Line: row.Line, Path: row.Path(), Field: f.Field, Message: f.Message,
						})
					}
					continue
				}
				return fmt.Errorf("importing %s (line %d): %w", row.Path(), row.Line, err)
			}
			report.Created = append(report.Created, row.Path())
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
		// Both are how this unwinds a transaction on purpose.
	case err != nil:
		return nil, err
	}
	if len(report.Problems) > 0 {
		report.Created = nil
	}
	return report, nil
}

// validateDeviceTypeRow checks the things a domain constructor cannot, because
// they are strings from a file rather than typed values.
func validateDeviceTypeRow(row DeviceTypeImportRow, knownManufacturer bool) []ImportProblem {
	var problems []ImportProblem
	add := func(field, format string, args ...any) {
		problems = append(problems, ImportProblem{
			Line: row.Line, Path: row.Path(), Field: field,
			Message: fmt.Sprintf(format, args...),
		})
	}
	if row.Manufacturer == "" {
		add("manufacturer", "this row names no manufacturer")
	} else if !knownManufacturer {
		add("manufacturer", "there is no manufacturer with the code %q; add it in the catalogue first",
			row.Manufacturer)
	}
	if row.Model == "" {
		add("model", "this row has no model")
	}
	return problems
}

// buildImportedDeviceType turns a row into a device type, collecting every
// problem rather than stopping at the first.
func buildImportedDeviceType(row DeviceTypeImportRow, manufacturerID string, now time.Time) (
	*domain.DeviceType, []ImportProblem) {

	var problems []ImportProblem
	add := func(field, format string, args ...any) {
		problems = append(problems, ImportProblem{
			Line: row.Line, Path: row.Path(), Field: field,
			Message: fmt.Sprintf(format, args...),
		})
	}

	// A rack height that is not a number is REFUSED, never quietly dropped. A
	// model silently stored with no height occupies nothing in every future
	// elevation calculation, and nothing on screen would say why.
	var height *int
	if row.UHeight != "" {
		n, err := strconv.Atoi(row.UHeight)
		if err != nil {
			add("u_height", "%q is not a whole number of rack units", row.UHeight)
		} else {
			height = &n
		}
	}

	full, err := parseImportBool(row.FullDepth)
	if err != nil {
		add("full_depth", "%s", err.Error())
	}

	if len(problems) > 0 {
		return nil, problems
	}

	d, err := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
		ManufacturerID: manufacturerID,
		Model:          row.Model,
		PartNumber:     optionalText(row.PartNumber),
		UHeight:        height,
		FullDepth:      full,
		EOLDate:        optionalText(row.EOLDate),
		Notes:          optionalText(row.Notes),
		Lifecycle:      row.Lifecycle,
	}, now)
	if err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			for _, f := range ve.Fields {
				add(f.Field, "%s", f.Message)
			}
		} else {
			add("", "%s", err.Error())
		}
		return nil, problems
	}
	return d, nil
}

// parseImportBool reads a yes/no cell.
//
// EMPTY IS FALSE; ANYTHING UNRECOGNISED IS AN ERROR. The tempting version
// returns false for whatever it does not understand, which turns "Yes " with a
// stray character, or a localised "ja", into a quiet no -- and a full-depth
// chassis recorded as half-depth is wrong in a way nobody notices until a rack
// diagram is built on it. The accepted spellings are the ones a spreadsheet
// actually produces.
func parseImportBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return false, nil
	case "true", "yes", "y", "1":
		return true, nil
	case "false", "no", "n", "0":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a yes or a no; use true/false, yes/no or 1/0", raw)
}

type manufacturerRef struct {
	id   string
	name string
}

// manufacturersByCode maps lower-cased codes to id and name.
//
// Retired makers included, deliberately. A catalogue file listing a model from
// a manufacturer somebody has since stopped buying from is still an accurate
// record of what is racked, and refusing it would make the operator resurrect
// the maker or drop the row -- both of which put a worse answer in the
// inventory than the one the file was carrying.
func manufacturersByCode(ctx context.Context, t *tx) (map[string]manufacturerRef, error) {
	var rows []struct {
		ID   string `db:"id"`
		Code string `db:"code"`
		Name string `db:"name"`
	}
	if err := t.selectAll(ctx, &rows, `SELECT id, code, name FROM manufacturer`); err != nil {
		return nil, fmt.Errorf("reading manufacturers: %w", err)
	}
	out := make(map[string]manufacturerRef, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.Code)] = manufacturerRef{id: r.ID, name: r.Name}
	}
	return out, nil
}

// existingDeviceTypePaths maps every LIVE model's manufacturer/model path to
// its id.
//
// Live only, matching the partial unique index exactly: a retired model does
// not hold its name against the database, so it must not hold it against a file
// either -- refusing here would reject a write the engine would have accepted.
func existingDeviceTypePaths(ctx context.Context, t *tx) (map[string]string, error) {
	var rows []struct {
		ID    string `db:"id"`
		Model string `db:"model"`
		Code  string `db:"code"`
	}
	err := t.selectAll(ctx, &rows, `
		SELECT d.id, d.model, m.code
		FROM device_type d
		JOIN manufacturer m ON m.id = d.manufacturer_id
		WHERE d.lifecycle <> ?`, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("reading the catalogue: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.Code)+"/"+r.Model] = r.ID
	}
	return out, nil
}

// deviceTypeIndex resolves a `manufacturer/model` path from an import file to a
// catalogued model.
//
// CASE-INSENSITIVE, WITH AN EXPLICIT REFUSAL WHEN THAT IS AMBIGUOUS. The unique
// index on device_type is case-sensitive, so "PowerEdge R650" and "poweredge
// r650" can both exist under one maker. Matching exactly would be defensible and
// would also mean a file typed in lower case silently finds nothing; matching
// loosely and picking one would be worse, because it would pick silently. So it
// folds case and, in the one pathological situation where that matches two
// models, says so and refuses rather than choosing.
type deviceTypeIndex struct {
	byPath map[string][]string // lower-cased path -> ids
}

func (i deviceTypeIndex) lookup(path string) (id string, found, ambiguous bool) {
	ids := i.byPath[strings.ToLower(path)]
	switch len(ids) {
	case 0:
		return "", false, false
	case 1:
		return ids[0], true, false
	default:
		return "", true, true
	}
}

// loadDeviceTypeIndex reads the LIVE catalogue.
//
// Live only, matching what the pickers offer and what the partial unique index
// governs: a retired model is one the estate has stopped buying, and pointing
// new assets at it from a file would be the file re-adopting it.
func loadDeviceTypeIndex(ctx context.Context, t *tx) (deviceTypeIndex, error) {
	var rows []struct {
		ID    string `db:"id"`
		Model string `db:"model"`
		Code  string `db:"code"`
	}
	err := t.selectAll(ctx, &rows, `
		SELECT d.id, d.model, m.code
		FROM device_type d
		JOIN manufacturer m ON m.id = d.manufacturer_id
		WHERE d.lifecycle <> ?`, domain.LifecycleRetired)
	if err != nil {
		return deviceTypeIndex{}, fmt.Errorf("reading the catalogue: %w", err)
	}
	idx := deviceTypeIndex{byPath: make(map[string][]string, len(rows))}
	for _, r := range rows {
		key := strings.ToLower(r.Code + "/" + r.Model)
		idx.byPath[key] = append(idx.byPath[key], r.ID)
	}
	return idx, nil
}
