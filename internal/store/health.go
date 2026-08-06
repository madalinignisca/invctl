// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The health read side: what a person sees when they look at an entity.
//
// Three obligations meet here, and none of them is optional.
//
// Rule 8 -- staleness is computed at read time, in every view. A value older
// than three of its reporter's declared intervals renders as `unknown (stale
// since T)`, never as its last value, because an intruder's first act is
// killing the collector and under transition-only logging a dead collector and
// a healthy estate are otherwise the same picture forever. That arithmetic
// lives in domain.AssetHealth and is applied by newObservedReading; what this
// file adds is the other half of the same rule -- a reporter that stops
// entirely must be ONE alertable event rather than a thousand entities quietly
// going green, so ListReporterStatus aggregates by reporter and says so once.
//
// Rule 14 -- an override shadows an observation at read time and never mutates
// it. EntityHealth therefore carries both: the override, and the readings
// underneath it, unaltered. The reporter keeps recording the truth while the
// override is in force, because when it lapses you need to know what actually
// happened.
//
// Rule 2 -- no verdict is computed. Two monitors that disagree stay two rows.
// Reducing them to one number here would be observed state deciding something
// on its own, made invisibly, and rule 2 is explicit that wiring observed state
// into a computed answer is an architecture decision requiring sign-off rather
// than an implementation detail.
//
// Everything here is a read, and reads of asset_health are permitted outside
// internal/store/observed.go -- that file owns the WRITES. Nothing in this file
// writes anything, and nothing in it acts on the estate: showing a state is not
// changing one.

package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gabriel/invctl/internal/domain"
)

// EntityHealth is everything a view needs to render one entity's health
// honestly: every reporter's current reading, and the override shadowing them
// if there is one.
type EntityHealth struct {
	EntityType string
	EntityID   string
	// Readings is one row per reporter, staleness already applied, in reporter
	// order. Deliberately not reduced to a single state.
	Readings []ObservedReading
	// Override is the active, unexpired operator override, or nil.
	Override *HealthOverrideRow
}

// Overridden reports whether an operator is currently shadowing the readings.
func (h EntityHealth) Overridden() bool { return h.Override != nil }

// Observed reports whether anything has ever reported on this entity. An empty
// result is not health: an entity nothing watches is unobserved, and a view
// must say so rather than defaulting to up.
func (h EntityHealth) Observed() bool { return len(h.Readings) > 0 }

// AssertedState is what the override says, and is only meaningful when there is
// one. It is the value a view shows in place of the readings -- never instead
// of showing that an override exists.
func (h EntityHealth) AssertedState() domain.HealthState {
	if h.Override == nil {
		return domain.HealthUnknown
	}
	return h.Override.AssertedState
}

// Disagree reports whether the fresh readings do not all say the same thing.
//
// It is shown, never resolved. Two monitors disagreeing about one asset is a
// finding in itself -- usually one of them is watching the wrong thing -- and
// picking a winner would hide the only evidence that the question exists.
func (h EntityHealth) Disagree() bool {
	var first domain.HealthState
	seen := false
	for _, r := range h.Readings {
		if r.Stale {
			continue
		}
		if !seen {
			first, seen = r.Display, true
			continue
		}
		if r.Display != first {
			return true
		}
	}
	return false
}

// GetEntityHealth loads the readings and the override for one entity.
func (s *SQLStore) GetEntityHealth(ctx context.Context, entityType, entityID string) (*EntityHealth, error) {
	readings, err := s.GetObservedState(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	override, err := s.ActiveOverride(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	return &EntityHealth{
		EntityType: entityType,
		EntityID:   entityID,
		Readings:   readings,
		Override:   override,
	}, nil
}

// EntityHealthFor loads health for a page's worth of entities in two queries
// rather than two per row.
//
// Every requested id gets an entry, including the ones nothing has ever
// reported on. A missing map key and an unobserved entity render identically in
// a template and mean completely different things, so the difference is removed
// here rather than left for a caller to get wrong.
func (s *SQLStore) EntityHealthFor(ctx context.Context, entityType string, ids []string) (map[string]EntityHealth, error) {
	out := make(map[string]EntityHealth, len(ids))
	for _, id := range ids {
		out[id] = EntityHealth{EntityType: entityType, EntityID: id}
	}
	if len(ids) == 0 {
		return out, nil
	}

	args := append([]any{entityType}, anySlice(ids)...)
	var rows []domain.AssetHealth
	err := s.read(ctx, &rows, `
		SELECT * FROM asset_health
		WHERE entity_type = ? AND entity_id IN (`+placeholders(len(ids))+`)
		ORDER BY entity_id, reporter`, args...)
	if err != nil {
		return nil, fmt.Errorf("getting observed state for %d %s(s): %w", len(ids), entityType, err)
	}

	now := s.Now()
	for _, h := range rows {
		entry, ok := out[h.EntityID]
		if !ok {
			// A row for an id nobody asked about cannot happen given the IN
			// clause, but silently dropping it would be the wrong instinct.
			entry = EntityHealth{EntityType: entityType, EntityID: h.EntityID}
		}
		entry.Readings = append(entry.Readings, newObservedReading(h, now))
		out[h.EntityID] = entry
	}

	overrides, err := s.ActiveOverridesFor(ctx, entityType, ids)
	if err != nil {
		return nil, err
	}
	for id, override := range overrides {
		entry := out[id]
		row := override
		entry.Override = &row
		out[id] = entry
	}
	return out, nil
}

// ReporterStatus is one monitoring credential's aggregate condition.
//
// This exists because of the second half of rule 8. Per-entity staleness alone
// turns a dead collector into a thousand entities going quietly unknown, which
// is a thousand things to notice and therefore nothing anybody notices. A
// reporter that has stopped is one line.
type ReporterStatus struct {
	Reporter string `db:"reporter"`
	// Entities is how many rows this reporter currently maintains.
	Entities int `db:"entity_count"`
	// LastReportAt is the newest server-clock report from this reporter, across
	// everything it watches.
	LastReportAt string `db:"last_report_at"`
	// IntervalSeconds is the SLOWEST cadence the reporter has declared. The
	// slowest rather than the fastest on purpose: a reporter that watches one
	// thing every 30s and another every 10 minutes has not gone quiet until the
	// slow one is overdue, and a false "the collector is dead" during an
	// incident costs more than a late one.
	IntervalSeconds *int `db:"interval_seconds"`

	// Silent is true when nothing has arrived within three of those intervals.
	Silent bool
	// SilentSince is when the reporter stopped being believed. Empty while it
	// is reporting normally.
	SilentSince string
}

// StaleAfter is the instant this reporter stops being believed, and whether
// that instant can be computed at all.
func (r ReporterStatus) StaleAfter() (time.Time, bool) {
	if r.IntervalSeconds == nil || *r.IntervalSeconds <= 0 {
		return time.Time{}, false
	}
	last, err := domain.ParseTime(r.LastReportAt)
	if err != nil {
		return time.Time{}, false
	}
	return last.Add(time.Duration(domain.StaleAfterIntervals) * time.Duration(*r.IntervalSeconds) * time.Second), true
}

// ListReporterStatus is "is anything still watching?", answered once per
// credential rather than once per entity.
//
// MIN(interval_seconds), not MAX. A reporter declares a cadence per reading, so
// one entity watched lazily and one watched closely give the same credential
// two horizons; the tightest promise is the one that proves it is alive. Taking
// the loosest let a single slow row hide a dead collector for as long as that
// row's cadence allowed -- the two halves of rule 8 then contradicted each
// other on one screen, with the fast-cadence entity correctly stale while the
// reporters panel showed the same credential as reporting.
//
// The cap on a declared cadence (domain.MaxIntervalSeconds) bounds the other
// end: MIN cannot be gamed downwards to make a reporter look alive, because a
// smaller interval means a TIGHTER deadline, not a looser one.
func (s *SQLStore) ListReporterStatus(ctx context.Context) ([]ReporterStatus, error) {
	var rows []ReporterStatus
	err := s.read(ctx, &rows, `
		SELECT reporter AS reporter,
		       COUNT(*) AS entity_count,
		       MAX(last_report_at) AS last_report_at,
		       MIN(interval_seconds) AS interval_seconds
		FROM asset_health
		GROUP BY reporter
		ORDER BY reporter`)
	if err != nil {
		return nil, fmt.Errorf("listing reporter status: %w", err)
	}

	now := s.Now().UTC()
	for i := range rows {
		horizon, ok := rows[i].StaleAfter()
		if !ok {
			// No computable horizon is not evidence of freshness. A reporter
			// that never declared a cadence can never be shown to have stopped,
			// which is the failure this rule exists to prevent, so it is called
			// silent and the operator goes and looks.
			rows[i].Silent = true
			rows[i].SilentSince = rows[i].LastReportAt
			continue
		}
		if now.After(horizon) {
			rows[i].Silent = true
			rows[i].SilentSince = domain.FormatTime(horizon)
		}
	}
	return rows, nil
}

// WithConfiguredReporters folds in credentials that have never written a row.
//
// ListReporterStatus groups over asset_health, so it can only see reporters
// that have reported. A credential provisioned and never deployed, or one
// removed from the estate whose rows were later pruned, is invisible to it --
// and the panel exists precisely so that a collector going quiet is one
// alertable event rather than a thousand entities drifting green. "Never
// checked in" and "checked in and stopped" are different findings and both
// belong on the screen.
//
// Configured ids come from the same registry the startup username-collision
// check uses, so the panel cannot disagree with what the server will accept.
func WithConfiguredReporters(rows []ReporterStatus, configured []string) []ReporterStatus {
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.Reporter] = true
	}
	for _, id := range configured {
		if seen[id] {
			continue
		}
		rows = append(rows, ReporterStatus{
			Reporter: id,
			Silent:   true,
			// No SilentSince: there is no instant it stopped, because it never
			// started. A view must show that difference rather than inventing
			// a timestamp for it.
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Reporter < rows[j].Reporter })
	return rows
}
