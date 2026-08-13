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

// The hardware catalogue. See migration 00022 for why it exists and
// domain/hardware.go for the override rule on dates.

// ---------- manufacturers ----------

// ManufacturerRow is a manufacturer with the counts a list needs.
type ManufacturerRow struct {
	domain.Manufacturer
	// DeviceTypes is how many live models are catalogued under this maker, so a
	// list can say which entries are carrying weight and which were added and
	// forgotten.
	DeviceTypes int `db:"device_types"`
}

// CreateManufacturer inserts a manufacturer and its audit row.
func (s *SQLStore) CreateManufacturer(ctx context.Context, actor domain.Actor, m *domain.Manufacturer) error {
	m.RowVersion = 1
	if err := m.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO manufacturer (id, code, name, support_ref, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.Code, m.Name, m.SupportRef, m.Lifecycle, m.CreatedAt, m.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating manufacturer")
		}
		return t.logCreate(ctx, "manufacturer", m.ID, m)
	})
}

// UpdateManufacturer persists field changes.
func (s *SQLStore) UpdateManufacturer(ctx context.Context, actor domain.Actor, m *domain.Manufacturer) error {
	if err := m.Validate(); err != nil {
		return err
	}
	before, err := s.GetManufacturer(ctx, m.ID)
	if err != nil {
		return err
	}
	m.CreatedAt = before.CreatedAt
	m.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE manufacturer
			SET code = ?, name = ?, support_ref = ?, lifecycle = ?, updated_at = ?,
			    row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			m.Code, m.Name, m.SupportRef, m.Lifecycle, m.UpdatedAt, m.ID, m.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating manufacturer")
		}
		if err := requireVersion(res, "manufacturer", m.ID, &m.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "manufacturer", m.ID, &before.Manufacturer, m)
	})
}

// GetManufacturer loads one by id.
func (s *SQLStore) GetManufacturer(ctx context.Context, id string) (*ManufacturerRow, error) {
	var row ManufacturerRow
	err := s.readOne(ctx, &row, `
		SELECT m.*, (SELECT COUNT(*) FROM device_type d
		             WHERE d.manufacturer_id = m.id AND d.lifecycle <> ?) AS device_types
		FROM manufacturer m WHERE m.id = ?`, domain.LifecycleRetired, id)
	if err != nil {
		return nil, fmt.Errorf("getting manufacturer %s: %w", id, err)
	}
	return &row, nil
}

// ListManufacturers returns the catalogue's makers, by name.
func (s *SQLStore) ListManufacturers(ctx context.Context, includeRetired bool) ([]ManufacturerRow, error) {
	query := `
		SELECT m.*, (SELECT COUNT(*) FROM device_type d
		             WHERE d.manufacturer_id = m.id AND d.lifecycle <> ?) AS device_types
		FROM manufacturer m`
	args := []any{domain.LifecycleRetired}
	if !includeRetired {
		query += ` WHERE m.lifecycle <> ?`
		args = append(args, domain.LifecycleRetired)
	}
	query += ` ORDER BY m.name`

	var rows []ManufacturerRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing manufacturers: %w", err)
	}
	return rows, nil
}

// RetireManufacturer soft-deletes a maker.
//
// REFUSED WHILE IT STILL HAS LIVE MODELS, the same way a vocabulary term in use
// cannot be removed. Retiring it anyway would leave device types pointing at a
// maker the lists no longer show -- and a model whose manufacturer has silently
// vanished from every picker is a model nobody can find to fix.
func (s *SQLStore) RetireManufacturer(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetManufacturer(ctx, id)
	if err != nil {
		return err
	}
	if before.DeviceTypes > 0 {
		return fmt.Errorf("manufacturer %s still has %d live %s: %w",
			before.Code, before.DeviceTypes,
			pluralWord(before.DeviceTypes, "model", "models"), domain.ErrConflict)
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`UPDATE manufacturer SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring manufacturer")
		}
		after := before.Manufacturer
		after.Lifecycle = domain.LifecycleRetired
		after.UpdatedAt = at
		return t.logUpdate(ctx, "manufacturer", id, &before.Manufacturer, &after)
	})
}

// ---------- device types ----------

// DeviceTypeRow is a model with its maker and how many boxes are of it.
type DeviceTypeRow struct {
	domain.DeviceType
	ManufacturerCode string `db:"manufacturer_code"`
	ManufacturerName string `db:"manufacturer_name"`
	// Assets is how many LIVE assets are of this model. It is what turns an EOL
	// date into a number somebody has to act on: a model lapsing next March is a
	// diary note, and forty of them is a project.
	Assets int `db:"assets"`
}

// Path is the natural key a file or a URL can use: dell/r650.
func (d DeviceTypeRow) Path() string { return d.ManufacturerCode + "/" + d.Model }

const deviceTypeSelect = `
	SELECT d.*, m.code AS manufacturer_code, m.name AS manufacturer_name,
	       (SELECT COUNT(*) FROM asset a
	        WHERE a.device_type_id = d.id AND a.lifecycle <> '` + domain.LifecycleRetired + `') AS assets
	FROM device_type d
	JOIN manufacturer m ON m.id = d.manufacturer_id`

// CreateDeviceType inserts a model and its audit row.
func (s *SQLStore) CreateDeviceType(ctx context.Context, actor domain.Actor, d *domain.DeviceType) error {
	d.RowVersion = 1
	if err := d.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		maker, err := requireLiveManufacturer(ctx, t, d.ManufacturerID)
		if err != nil {
			return err
		}
		return s.insertDeviceType(ctx, t, d, maker)
	})
}

// insertDeviceType writes one model inside a transaction the CALLER owns.
//
// Split out for the same reason insertAsset is: a catalogue import puts many of
// these in one transaction, which is what makes whole-file refusal expressible
// and what lets a dry run run the REAL writes and roll them back. The importer
// therefore exercises this exact path -- the audit row, the search index and the
// unique index included -- rather than a parallel one that would agree until it
// mattered.
func (s *SQLStore) insertDeviceType(ctx context.Context, t *tx, d *domain.DeviceType, manufacturerName string) error {
	_, err := t.exec(ctx, `
		INSERT INTO device_type (id, manufacturer_id, model, part_number, u_height,
		                         full_depth, depth_mm, weight_grams, airflow, port_face,
		                         eol_date, notes, lifecycle, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.ManufacturerID, d.Model, d.PartNumber, d.UHeight,
		d.FullDepth, d.DepthMM, d.WeightGrams, d.Airflow, d.PortFace,
		d.EOLDate, d.Notes, d.Lifecycle, d.CreatedAt, d.UpdatedAt)
	if err != nil {
		return translateWriteErr(err, "creating device type")
	}
	if err := t.logCreate(ctx, "device_type", d.ID, d); err != nil {
		return err
	}
	return s.indexDeviceType(ctx, t, d, manufacturerName)
}

// UpdateDeviceType persists field changes.
//
// The manufacturer is NOT editable here, for the reason the asset editors do not
// move a parent: it decides what the model IS, and moving a model between makers
// silently re-labels every asset of it. Correct a mis-filed model by retiring it
// and cataloguing the right one.
func (s *SQLStore) UpdateDeviceType(ctx context.Context, actor domain.Actor, d *domain.DeviceType) error {
	before, err := s.GetDeviceType(ctx, d.ID)
	if err != nil {
		return err
	}
	d.ManufacturerID = before.ManufacturerID
	if err := d.Validate(); err != nil {
		return err
	}
	d.CreatedAt = before.CreatedAt
	d.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE device_type
			SET model = ?, part_number = ?, u_height = ?, full_depth = ?,
			    depth_mm = ?, weight_grams = ?, airflow = ?, port_face = ?, eol_date = ?,
			    notes = ?, lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			d.Model, d.PartNumber, d.UHeight, d.FullDepth,
			d.DepthMM, d.WeightGrams, d.Airflow, d.PortFace, d.EOLDate,
			d.Notes, d.Lifecycle, d.UpdatedAt, d.ID, d.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating device type")
		}
		if err := requireVersion(res, "device_type", d.ID, &d.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "device_type", d.ID, &before.DeviceType, d); err != nil {
			return err
		}
		// Reindexed on every edit: a part number corrected in the form and not
		// in the index is a part number that still finds nothing.
		return s.indexDeviceType(ctx, t, d, before.ManufacturerName)
	})
}

// GetDeviceType loads one model by id.
func (s *SQLStore) GetDeviceType(ctx context.Context, id string) (*DeviceTypeRow, error) {
	var row DeviceTypeRow
	if err := s.readOne(ctx, &row, deviceTypeSelect+` WHERE d.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting device type %s: %w", id, err)
	}
	return &row, nil
}

// DeviceTypeFilter narrows the catalogue list.
type DeviceTypeFilter struct {
	ManufacturerID string
	IncludeRetired bool
}

// ListDeviceTypes returns the catalogue, by maker then model.
func (s *SQLStore) ListDeviceTypes(ctx context.Context, f DeviceTypeFilter) ([]DeviceTypeRow, error) {
	query := deviceTypeSelect
	var args []any
	var where []string
	if f.ManufacturerID != "" {
		where = append(where, `d.manufacturer_id = ?`)
		args = append(args, f.ManufacturerID)
	}
	if !f.IncludeRetired {
		where = append(where, `d.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	query += whereClause(where) + ` ORDER BY m.name, d.model`

	var rows []DeviceTypeRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing device types: %w", err)
	}
	return rows, nil
}

// RetireDeviceType soft-deletes a model.
//
// ALLOWED WHILE ASSETS STILL POINT AT IT, unlike retiring a manufacturer with
// live models. The cases are genuinely different: a retired model is exactly how
// you record "we no longer buy these", and the boxes already racked are the
// reason you wanted to say so. The link stays, the asset page keeps naming the
// model, and the inherited EOL date keeps resolving -- withdrawing it would
// quietly blank the expiry date on every asset of that model, which is the
// opposite of what retiring it meant.
func (s *SQLStore) RetireDeviceType(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetDeviceType(ctx, id)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`UPDATE device_type SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring device type")
		}
		after := before.DeviceType
		after.Lifecycle = domain.LifecycleRetired
		after.UpdatedAt = at
		return t.logUpdate(ctx, "device_type", id, &before.DeviceType, &after)
	})
}

// requireLiveManufacturer refuses a model filed under a maker that is not
// there, and returns its name for the search index.
func requireLiveManufacturer(ctx context.Context, t *tx, id string) (string, error) {
	var row struct {
		Name      string `db:"name"`
		Lifecycle string `db:"lifecycle"`
	}
	err := t.get(ctx, &row, `SELECT name, lifecycle FROM manufacturer WHERE id = ?`, id)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add("manufacturer_id", "choose a manufacturer")
		return "", ve
	}
	if row.Lifecycle == domain.LifecycleRetired {
		ve := &domain.ValidationError{}
		ve.Add("manufacturer_id", "that manufacturer has been retired")
		return "", ve
	}
	return row.Name, nil
}

// pluralWord picks a noun form for a message.
func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
