// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Operator overrides of an observation -- declared state, audited like any
// other declared mutation (docs/AUDIT.md rule 14).
//
// This file is deliberately NOT internal/store/observed.go. An override is a
// person overruling a monitor: somebody decided it, so it writes a change_log
// row in the same transaction, sits behind CSRF and RequireAdmin, and is
// soft-deleted rather than removed. Nothing here writes asset_health,
// observed_transition or unmatched_observation, and nothing in observed.go
// writes health_override -- the reporter keeps recording the truth underneath
// an override, because when the override lapses you need to know what actually
// happened while it was in force.
//
// An override SHADOWS an observation at read time. Expiry is evaluated on
// every read rather than swept by a job, so a lapsed override needs no write to
// stop applying and a database nobody has touched for a week still renders the
// truth.
//
// Nothing here acts on the estate. Silencing a reading changes what a person
// sees; it does not restart, drain or promote anything (HANDOVER.md §1).

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// HealthOverrideRow is an override with its author's display name resolved.
//
// The LEFT JOIN is deliberate and matches change_log's: `actor` holds an opaque
// app_user.id, so scrubbing an account to satisfy an erasure request must leave
// the row intact and simply stop resolving to a name, rather than break the
// page or block the erasure.
type HealthOverrideRow struct {
	domain.HealthOverride
	// ActorName is empty when the id does not resolve to a live account.
	ActorName string `db:"actor_name"`
	// EntityName is the asset's name or the instance's service code. Empty
	// when the entity has since been removed.
	EntityName string `db:"entity_name"`
}

// DisplayEntity is what the Entity column renders: a name when one resolves,
// the opaque id when it does not. Never the bare type, which identifies
// nothing.
func (r HealthOverrideRow) DisplayEntity() string {
	if r.EntityName != "" {
		return r.EntityName
	}
	return r.EntityID
}

// EntityHref links to the thing being silenced, so the panel is one click from
// the page that explains it.
func (r HealthOverrideRow) EntityHref() string {
	if r.EntityType == domain.ObservableAsset {
		return "/assets/" + r.EntityID
	}
	return ""
}

// DisplayActor is what a template renders, falling back to the opaque id.
func (r HealthOverrideRow) DisplayActor() string {
	if r.ActorName != "" {
		return r.ActorName
	}
	return r.Actor
}

// ActorKind satisfies the actor_cell partial, which renders the kind beside the
// name in every view (rule 5).
//
// It is a constant rather than a column because domain.NewHealthOverride
// refuses any actor that is not a person: a monitoring credential writing its
// own override would let a compromised token silence the estate it is reporting
// on. If that ever stops being true, this needs a column, not a different
// constant.
func (r HealthOverrideRow) ActorKind() string { return domain.ActorKindUser }

// overrideSelect resolves the entity to something an operator can act on.
//
// The dashboard panel announced "2 active overrides" and then printed the
// entity TYPE in its Entity column, so two silenced machines rendered as two
// identical rows reading "asset". Somebody reading a green estate mid-incident
// could see that something was silenced and not which thing -- which is worse
// than not showing the panel, because it costs a search to find out.
//
// Both joins are keyed on entity_type as well as id, so a service_instance id
// can never accidentally match an asset row.
const overrideSelect = `
	SELECT ho.*, COALESCE(u.display_name, u.username, '') AS actor_name,
	       COALESCE(a.name, sv.code, '') AS entity_name
	FROM health_override ho
	LEFT JOIN app_user u ON u.id = ho.actor
	LEFT JOIN asset a ON a.id = ho.entity_id AND ho.entity_type = 'asset'
	LEFT JOIN service_instance si ON si.id = ho.entity_id AND ho.entity_type = 'service_instance'
	LEFT JOIN service sv ON sv.id = si.service_id`

// GetHealthOverride loads one override whatever its lifecycle, for the amend
// and clear paths.
func (s *SQLStore) GetHealthOverride(ctx context.Context, id string) (*HealthOverrideRow, error) {
	var row HealthOverrideRow
	if err := s.readOne(ctx, &row, overrideSelect+` WHERE ho.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting health override %s: %w", id, err)
	}
	return &row, nil
}

// ActiveOverride returns the override currently shadowing an entity, or nil.
//
// "Currently" is computed here, at read time: lifecycle must still be active
// AND the expiry must still be in the future. A lapsed override stops applying
// the moment the clock passes it, with no write anywhere.
func (s *SQLStore) ActiveOverride(ctx context.Context, entityType, entityID string) (*HealthOverrideRow, error) {
	var row HealthOverrideRow
	err := s.readOne(ctx, &row, overrideSelect+`
		WHERE ho.entity_type = ? AND ho.entity_id = ?
		  AND ho.lifecycle = ? AND ho.expires_at > ?`,
		entityType, entityID, domain.OverrideActive, domain.FormatTime(s.Now()))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("getting active override for %s %s: %w", entityType, entityID, err)
	}
	return &row, nil
}

// ActiveOverridesFor resolves overrides for a page's worth of entities in one
// query, so a service with forty placements does not issue forty of them.
func (s *SQLStore) ActiveOverridesFor(ctx context.Context, entityType string, ids []string) (map[string]HealthOverrideRow, error) {
	out := map[string]HealthOverrideRow{}
	if len(ids) == 0 {
		return out, nil
	}
	args := append([]any{entityType, domain.OverrideActive, domain.FormatTime(s.Now())}, anySlice(ids)...)
	var rows []HealthOverrideRow
	err := s.read(ctx, &rows, overrideSelect+`
		WHERE ho.entity_type = ? AND ho.lifecycle = ? AND ho.expires_at > ?
		  AND ho.entity_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing active overrides for %d %s(s): %w", len(ids), entityType, err)
	}
	for _, row := range rows {
		out[row.EntityID] = row
	}
	return out, nil
}

// ListActiveOverrides is "what is silenced right now, and until when" -- the
// question an incident review asks before believing any green panel.
func (s *SQLStore) ListActiveOverrides(ctx context.Context) ([]HealthOverrideRow, error) {
	var rows []HealthOverrideRow
	err := s.read(ctx, &rows, overrideSelect+`
		WHERE ho.lifecycle = ? AND ho.expires_at > ?
		ORDER BY ho.expires_at`,
		domain.OverrideActive, domain.FormatTime(s.Now()))
	if err != nil {
		return nil, fmt.Errorf("listing active overrides: %w", err)
	}
	return rows, nil
}

// CreateHealthOverride records an operator's assertion that a reading is wrong.
//
// The entity must exist. An override on nothing is a silence with no subject,
// and since entity_id carries no foreign key -- entity_type is polymorphic, so
// no single one could express it -- the check has to be here.
//
// A lapsed override still occupies the partial unique index, because expiry is
// a read-time judgement and the row is never swept. Rather than making the
// operator clear it first, creating a new override supersedes it: the lapsed
// row is cleared in the same transaction, by the same person, with its own
// change_log entry. Two audit rows, both true, and nobody has to know about an
// index to write down what they know.
//
// writeSerializable because this is check-then-act. The partial unique index is
// the structural backstop; at PostgreSQL's default isolation two operators
// racing would both read "no active override" and one would get a bare
// constraint error instead of the conflict the other half of this method
// reports.
func (s *SQLStore) CreateHealthOverride(ctx context.Context, actor domain.Actor, o *domain.HealthOverride) error {
	if err := o.Validate(); err != nil {
		return err
	}
	known, err := s.observableExists(ctx, o.EntityType, o.EntityID)
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("overriding health for %s %s: %w", o.EntityType, o.EntityID, domain.ErrNotFound)
	}

	now := s.Now()
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		existing, err := t.overrideForEntity(ctx, o.EntityType, o.EntityID)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.IsActiveAt(now) {
				return fmt.Errorf("overriding health for %s %s: it is already overridden until %s: %w",
					o.EntityType, o.EntityID, existing.ExpiresAt, domain.ErrConflict)
			}
			superseded := *existing
			if err := superseded.Clear(actor, now); err != nil {
				return fmt.Errorf("superseding lapsed override %s: %w", existing.ID, err)
			}
			if err := t.saveOverride(ctx, &superseded); err != nil {
				return err
			}
			if err := t.logUpdate(ctx, "health_override", superseded.ID, existing, &superseded); err != nil {
				return err
			}
		}

		_, err = t.exec(ctx, `
			INSERT INTO health_override (id, entity_type, entity_id, asserted_state, reason,
			                             actor, lifecycle, created_at, updated_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.ID, o.EntityType, o.EntityID, string(o.AssertedState), o.Reason,
			o.Actor, o.Lifecycle, o.CreatedAt, o.UpdatedAt, o.ExpiresAt)
		if err != nil {
			return translateWriteErr(err, "creating health override")
		}
		return t.logCreate(ctx, "health_override", o.ID, o)
	})
}

// AmendHealthOverride edits what an override asserts, why, and until when.
//
// It re-reads inside the transaction rather than trusting the round-tripped
// struct the caller built from a form: an override amended from a stale page
// would otherwise silently revert a colleague's edit and the change_log would
// attribute the revert to whoever pressed the button last. That is the same
// defect rule 1 names on the observed side, and it is just as available here.
func (s *SQLStore) AmendHealthOverride(ctx context.Context, actor domain.Actor, id string, spec domain.HealthOverrideSpec) (*domain.HealthOverride, error) {
	now := s.Now()
	var out *domain.HealthOverride
	err := s.write(ctx, actor, func(t *tx) error {
		before, err := t.overrideByID(ctx, id)
		if err != nil {
			return err
		}
		amended := *before
		if err := amended.Amend(spec, actor, now); err != nil {
			return err
		}
		if err := t.saveOverride(ctx, &amended); err != nil {
			return err
		}
		out = &amended
		return t.logUpdate(ctx, "health_override", id, before, &amended)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ClearHealthOverride ends an override early.
//
// The row stays. Soft delete applies here as everywhere else, and for a sharper
// reason than usual: "who silenced this, for how long, and why" is read after
// the fact, by somebody asking why an outage went unnoticed.
func (s *SQLStore) ClearHealthOverride(ctx context.Context, actor domain.Actor, id string) error {
	now := s.Now()
	return s.write(ctx, actor, func(t *tx) error {
		before, err := t.overrideByID(ctx, id)
		if err != nil {
			return err
		}
		cleared := *before
		if err := cleared.Clear(actor, now); err != nil {
			return err
		}
		if err := t.saveOverride(ctx, &cleared); err != nil {
			return err
		}
		return t.logUpdate(ctx, "health_override", id, before, &cleared)
	})
}

// overrideByID reads one row inside the transaction.
func (t *tx) overrideByID(ctx context.Context, id string) (*domain.HealthOverride, error) {
	var o domain.HealthOverride
	err := t.get(ctx, &o, `SELECT * FROM health_override WHERE id = ?`, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("loading health override %s: %w", id, domain.ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("loading health override %s: %w", id, err)
	}
	return &o, nil
}

// overrideForEntity reads whichever override holds the entity's slot in the
// partial unique index, lapsed or not. Whether it still applies is the caller's
// judgement, because expiry is evaluated at read time.
func (t *tx) overrideForEntity(ctx context.Context, entityType, entityID string) (*domain.HealthOverride, error) {
	var o domain.HealthOverride
	err := t.get(ctx, &o,
		`SELECT * FROM health_override
		 WHERE entity_type = ? AND entity_id = ? AND lifecycle = ?`,
		entityType, entityID, domain.OverrideActive)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("loading the active override for %s %s: %w", entityType, entityID, err)
	}
	return &o, nil
}

// saveOverride writes the three mutable columns and nothing else. entity_type,
// entity_id, actor and created_at are absent on purpose: an amendment may not
// move an override to another entity, re-attribute it, or rewrite when it
// started.
func (t *tx) saveOverride(ctx context.Context, o *domain.HealthOverride) error {
	_, err := t.exec(ctx, `
		UPDATE health_override
		SET asserted_state = ?, reason = ?, lifecycle = ?, expires_at = ?, updated_at = ?
		WHERE id = ?`,
		string(o.AssertedState), o.Reason, o.Lifecycle, o.ExpiresAt, o.UpdatedAt, o.ID)
	if err != nil {
		return translateWriteErr(err, "saving health override")
	}
	return nil
}

// OverrideExpiryChoices are the durations the form offers, shortest first.
//
// They stop below domain.MaxOverrideDuration rather than at it, so the longest
// offer is a deliberate choice rather than the ceiling by default. A person who
// genuinely wants the full day types it; the picker should not make the maximum
// the path of least resistance.
var OverrideExpiryChoices = []time.Duration{
	30 * time.Minute,
	2 * time.Hour,
	4 * time.Hour,
	8 * time.Hour,
	12 * time.Hour,
}
