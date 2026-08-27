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
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The inflation series (WP-J2): reference data, typed by a person.

// ListInflationRates returns every year on record, oldest first.
func (s *SQLStore) ListInflationRates(ctx context.Context) ([]domain.InflationRate, error) {
	// The column is TEXT (see migration 00043) and the domain type is an int,
	// so the conversion happens here at the boundary rather than leaking a
	// storage decision into the arithmetic.
	var raw []struct {
		Year        string  `db:"year"`
		BasisPoints int     `db:"basis_points"`
		Source      *string `db:"source"`
		CreatedAt   string  `db:"created_at"`
		UpdatedAt   string  `db:"updated_at"`
		RowVersion  int64   `db:"row_version"`
	}
	if err := s.read(ctx, &raw,
		`SELECT year, basis_points, source, created_at, updated_at, row_version
		 FROM inflation_rate ORDER BY year`); err != nil {
		return nil, fmt.Errorf("listing inflation rates: %w", err)
	}
	rows := make([]domain.InflationRate, 0, len(raw))
	for _, r := range raw {
		y, err := strconv.Atoi(r.Year)
		if err != nil {
			return nil, fmt.Errorf("inflation year %q is not a number: %w", r.Year, err)
		}
		rows = append(rows, domain.InflationRate{
			Year: y, BasisPoints: r.BasisPoints, Source: r.Source,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, RowVersion: r.RowVersion,
		})
	}
	return rows, nil
}

// InflationSeries returns the table in the shape the arithmetic wants.
func (s *SQLStore) InflationSeries(ctx context.Context) (domain.InflationSeries, error) {
	rows, err := s.ListInflationRates(ctx)
	if err != nil {
		return nil, err
	}
	out := make(domain.InflationSeries, len(rows))
	for _, r := range rows {
		out[r.Year] = r.BasisPoints
	}
	return out, nil
}

// SetInflationRate records or corrects one year.
//
// UPSERT RATHER THAN ADD AND EDIT, because a year is the key and there is only
// ever one figure for it. Two rows for 2024 would make every comparison depend
// on which one a query happened to read first.
//
// Corrected in place rather than superseded, and the difference from a cost
// line is worth stating: a cost that changes is TWO facts -- one price until a
// date, another after it -- while a published index revised for 2024 was always
// one figure that somebody had wrong. Amending it is the honest shape, and
// change_log keeps what it was.
func (s *SQLStore) SetInflationRate(ctx context.Context, p domain.Permit, r *domain.InflationRate) error {
	if err := r.Validate(); err != nil {
		return err
	}
	now := domain.FormatTime(s.now())
	r.UpdatedAt = now

	return s.write(ctx, p, func(t *tx) error {
		year := strconv.Itoa(r.Year)
		var before domain.InflationRate
		existed := true
		if err := t.get(ctx, &before,
			`SELECT CAST(year AS INTEGER) AS year, basis_points, source,
			        created_at, updated_at, row_version
			 FROM inflation_rate WHERE year = ?`, year); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("reading the existing rate: %w", err)
			}
			existed = false
		}
		if !existed {
			r.CreatedAt = now
			if _, err := t.exec(ctx, `
				INSERT INTO inflation_rate (year, basis_points, source, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)`,
				year, r.BasisPoints, r.Source, r.CreatedAt, r.UpdatedAt); err != nil {
				return translateWriteErr(err, "recording an inflation rate")
			}
			return t.logCreate(ctx, "inflation_rate", year, r)
		}
		r.CreatedAt = before.CreatedAt
		if _, err := t.exec(ctx, `
			UPDATE inflation_rate SET basis_points = ?, source = ?, updated_at = ?,
			                          row_version = row_version + 1
			WHERE year = ?`,
			r.BasisPoints, r.Source, r.UpdatedAt, year); err != nil {
			return translateWriteErr(err, "correcting an inflation rate")
		}
		return t.logUpdate(ctx, "inflation_rate", year, &before, r)
	})
}

// DeleteInflationRate is deliberately absent.
//
// Reference data follows the same rule as everything else here: nothing is hard
// deleted. A year recorded in error is CORRECTED, and a year that should never
// have been entered is set to the figure that is true. Removing it would make
// every comparison spanning it silently treat inflation as zero -- which
// flatters the supplier, and does so invisibly.
