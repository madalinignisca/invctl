// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Operator overrides of an observation (docs/AUDIT.md rule 14).
//
// A person who knows a reading is wrong writes one of these rather than editing
// an observed column, which the next poll would clobber thirty seconds later
// and which would be unattributed besides. So it is a declared mutation and it
// is treated as one: behind CSRF and RequireAdmin, audited in the same
// transaction, soft-deleted rather than removed.
//
// None of this acts on the estate. An override changes what a person is shown;
// it does not restart, drain, promote or silence anything outside this process.
// The reporter keeps recording the truth underneath, and the panel says so.

// overrideTarget is one entity a page can override.
//
// It carries the entity's active override rather than a boolean, so the picker
// can say a target is already overridden AND the panel beside it can render by
// whom, why and until when -- from one query and one source of truth. Rule 14
// requires every view of an overridden entity to show all four.
type overrideTarget struct {
	EntityType string
	EntityID   string
	Label      string
	Override   *store.HealthOverrideRow
}

// Overridden reports whether this target already has an active override, so the
// picker can say so before the operator submits rather than letting them
// discover it from a 409.
func (t overrideTarget) Overridden() bool { return t.Override != nil }

// Value is the composite the form submits, so one <select> carries both halves
// of a polymorphic key without a second field to keep in step with it.
func (t overrideTarget) Value() string { return t.EntityType + ":" + t.EntityID }

type durationChoice struct {
	Minutes int
	Label   string
}

// overrideFormData is the create form, renderable standalone -- which is what
// lets a 422 re-render just the form.
type overrideFormData struct {
	Base
	Errors    map[string]string
	Targets   []overrideTarget
	States    []string
	Durations []durationChoice
	Form      overrideForm
}

type overrideForm struct {
	Target        string
	AssertedState string
	Reason        string
	Minutes       int
}

// overrideDurations are what the picker offers.
//
// The longest is well short of domain.MaxOverrideDuration. A picker whose
// default option is the ceiling makes the maximum the path of least resistance,
// and a permanent override is how a real outage stays invisible for six weeks.
func overrideDurations() []durationChoice {
	out := make([]durationChoice, 0, len(store.OverrideExpiryChoices))
	for _, d := range store.OverrideExpiryChoices {
		out = append(out, durationChoice{Minutes: int(d / time.Minute), Label: durationLabel(d)})
	}
	return out
}

func durationLabel(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	if d == time.Hour {
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", int(d/time.Hour))
}

// newOverrideForm builds the create form for a page's worth of targets.
func (a *App) newOverrideForm(r *http.Request, targets []overrideTarget, messages map[string]string, form overrideForm) overrideFormData {
	if form.Minutes == 0 {
		form.Minutes = int(store.OverrideExpiryChoices[0] / time.Minute)
	}
	if form.AssertedState == "" {
		form.AssertedState = string(domain.HealthUp)
	}
	if form.Target == "" && len(targets) > 0 {
		form.Target = targets[0].Value()
	}
	return overrideFormData{
		Base:      a.base(r, "Override", ""),
		Errors:    orEmpty(messages),
		Targets:   targets,
		States:    domain.HealthStateNames(),
		Durations: overrideDurations(),
		Form:      form,
	}
}

// HealthOverrideCreate records an operator's assertion that a reading is wrong.
func (a *App) HealthOverrideCreate(w http.ResponseWriter, r *http.Request) {
	minutes, numeric := intValue(r, "minutes", 0)
	form := overrideForm{
		Target:        formValue(r, "target"),
		AssertedState: formValue(r, "asserted_state"),
		Reason:        formValue(r, "reason"),
		Minutes:       minutes,
	}

	if !numeric {
		a.respondOverrideFormError(w, r, "", "", form, notANumber("minutes"))
		return
	}

	entityType, entityID, ok := splitTarget(form.Target)
	if !ok {
		a.respondOverrideFormError(w, r, entityType, entityID, form,
			map[string]string{"target": "choose what this override applies to"})
		return
	}

	// The expiry is computed from the server clock and a bounded choice, never
	// taken as a timestamp from the form. A caller-supplied instant is a
	// caller-supplied ceiling, and the ceiling is the whole point of the rule.
	expires, minutesErr := a.overrideExpiry(form.Minutes)
	if minutesErr != nil {
		a.respondOverrideFormError(w, r, entityType, entityID, form,
			map[string]string{"minutes": minutesErr.Error()})
		return
	}

	o, err := domain.NewHealthOverride(store.NewID(), domain.HealthOverrideSpec{
		EntityType:    entityType,
		EntityID:      entityID,
		AssertedState: form.AssertedState,
		Reason:        form.Reason,
		ExpiresAt:     expires,
	}, actor(r), a.Store.Now())
	if err == nil {
		err = a.Store.CreateHealthOverride(r.Context(), actor(r), o)
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.respondOverrideFormError(w, r, entityType, entityID, form, messages)
			return
		}
		if isConflict(err) {
			a.respondOverrideFormError(w, r, entityType, entityID, form, map[string]string{
				"target": "that is already overridden; amend or clear the existing override instead",
			})
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success",
		"Override recorded. The reporters keep recording underneath it, and it lapses at "+o.ExpiresAt+".")
	render.Redirect(w, r, a.entityURL(r.Context(), entityType, entityID))
}

// HealthOverrideAmend edits what an override asserts, why, and until when.
func (a *App) HealthOverrideAmend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetHealthOverride(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	minutes, numeric := intValue(r, "minutes", 0)
	if !numeric {
		a.setFlash(r, "error", "The duration must be a whole number of minutes.")
		render.Redirect(w, r, a.entityURL(r.Context(), existing.EntityType, existing.EntityID))
		return
	}
	expires, minutesErr := a.overrideExpiry(minutes)
	if minutesErr != nil {
		a.setFlash(r, "error", minutesErr.Error())
		render.Redirect(w, r, a.entityURL(r.Context(), existing.EntityType, existing.EntityID))
		return
	}

	_, err = a.Store.AmendHealthOverride(r.Context(), actor(r), id, domain.HealthOverrideSpec{
		AssertedState: formValue(r, "asserted_state"),
		Reason:        formValue(r, "reason"),
		ExpiresAt:     expires,
	})
	if err != nil {
		// An amendment has no form of its own to re-render -- it lives inside
		// the banner on the entity's page -- so the message rides back as a
		// flash on that page, which is where the operator is looking.
		if _, ok := validationErrors(err); ok {
			a.setFlash(r, "error", firstMessage(err, "That override could not be amended."))
			render.Redirect(w, r, a.entityURL(r.Context(), existing.EntityType, existing.EntityID))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Override amended.")
	render.Redirect(w, r, a.entityURL(r.Context(), existing.EntityType, existing.EntityID))
}

// HealthOverrideClear ends an override early. The row is kept: "who silenced
// this, and for how long" outlives the silence.
func (a *App) HealthOverrideClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetHealthOverride(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if err := a.Store.ClearHealthOverride(r.Context(), actor(r), id); err != nil {
		if _, ok := validationErrors(err); ok {
			a.setFlash(r, "error", firstMessage(err, "That override could not be cleared."))
			render.Redirect(w, r, a.entityURL(r.Context(), existing.EntityType, existing.EntityID))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Override cleared. What the reporters have been saying is visible again.")
	render.Redirect(w, r, a.entityURL(r.Context(), existing.EntityType, existing.EntityID))
}

// overrideExpiry turns a bounded minute count into an instant.
func (a *App) overrideExpiry(minutes int) (time.Time, error) {
	if minutes <= 0 {
		return time.Time{}, errors.New("choose how long this override should last")
	}
	if time.Duration(minutes)*time.Minute > domain.MaxOverrideDuration {
		return time.Time{}, fmt.Errorf("an override lasts at most %s", domain.MaxOverrideDuration)
	}
	return a.Store.Now().Add(time.Duration(minutes) * time.Minute), nil
}

// respondOverrideFormError re-renders the create form with error state and a
// 422 -- never a 200 with the message buried in the body.
func (a *App) respondOverrideFormError(w http.ResponseWriter, r *http.Request, entityType, entityID string, form overrideForm, messages map[string]string) {
	targets, err := a.overrideTargetsFor(r.Context(), entityType, entityID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "override_form",
		a.newOverrideForm(r, targets, messages, form))
}

// splitTarget parses the composite the picker submits.
func splitTarget(v string) (entityType, entityID string, ok bool) {
	for i := 0; i < len(v); i++ {
		if v[i] == ':' {
			entityType, entityID = v[:i], v[i+1:]
			return entityType, entityID, entityType != "" && entityID != ""
		}
	}
	return "", "", false
}

// entityURL is where an operator should land after overriding something.
//
// A service_instance has no page of its own; it is shown on the service it
// places, so that is where the confirmation belongs.
func (a *App) entityURL(ctx context.Context, entityType, entityID string) string {
	switch entityType {
	case domain.ObservableAsset:
		return "/assets/" + entityID
	case domain.ObservableServiceInstance:
		if instance, err := a.Store.GetInstance(ctx, entityID); err == nil {
			return "/services/" + instance.ServiceID
		}
	}
	return "/"
}

// overrideTargetsFor rebuilds the picker for whichever page the operator was
// on, so a re-rendered form offers the same choices it did before the failure.
func (a *App) overrideTargetsFor(ctx context.Context, entityType, entityID string) ([]overrideTarget, error) {
	switch entityType {
	case domain.ObservableAsset:
		asset, err := a.Store.GetAsset(ctx, entityID)
		if err != nil {
			return nil, err
		}
		instances, err := a.Store.ListInstancesByHost(ctx, entityID)
		if err != nil {
			return nil, err
		}
		return a.assetOverrideTargets(ctx, asset, instances)
	case domain.ObservableServiceInstance:
		instance, err := a.Store.GetInstance(ctx, entityID)
		if err != nil {
			return nil, err
		}
		instances, err := a.Store.ListInstancesByService(ctx, instance.ServiceID)
		if err != nil {
			return nil, err
		}
		return a.instanceOverrideTargets(ctx, instances)
	}
	return nil, nil
}

// assetOverrideTargets is the asset itself plus everything running on it: the
// two things an operator looking at that page might know a reading is wrong
// about.
func (a *App) assetOverrideTargets(ctx context.Context, asset *store.AssetRow, instances []store.InstanceRow) ([]overrideTarget, error) {
	targets := []overrideTarget{{
		EntityType: domain.ObservableAsset,
		EntityID:   asset.ID,
		Label:      asset.Name + " (this " + asset.Kind + ")",
	}}
	for _, si := range instances {
		targets = append(targets, overrideTarget{
			EntityType: domain.ObservableServiceInstance,
			EntityID:   si.ID,
			Label:      si.ServiceCode + " on " + si.HostName,
		})
	}
	return a.markOverridden(ctx, targets)
}

func (a *App) instanceOverrideTargets(ctx context.Context, instances []store.InstanceRow) ([]overrideTarget, error) {
	targets := make([]overrideTarget, 0, len(instances))
	for _, si := range instances {
		targets = append(targets, overrideTarget{
			EntityType: domain.ObservableServiceInstance,
			EntityID:   si.ID,
			Label:      si.ServiceCode + " on " + si.HostName,
		})
	}
	return a.markOverridden(ctx, targets)
}

// markOverridden attaches each target's active override, in one query per
// entity type rather than one per target.
func (a *App) markOverridden(ctx context.Context, targets []overrideTarget) ([]overrideTarget, error) {
	byType := map[string][]string{}
	for _, t := range targets {
		byType[t.EntityType] = append(byType[t.EntityType], t.EntityID)
	}
	active := map[string]map[string]store.HealthOverrideRow{}
	for entityType, ids := range byType {
		found, err := a.Store.ActiveOverridesFor(ctx, entityType, ids)
		if err != nil {
			return nil, err
		}
		active[entityType] = found
	}
	for i := range targets {
		if row, ok := active[targets[i].EntityType][targets[i].EntityID]; ok {
			found := row
			targets[i].Override = &found
		}
	}
	return targets, nil
}
