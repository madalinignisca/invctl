// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Demo observations, staged through the real write path.
//
// The fixture deliberately seeds no health of its own (seed_services.go says
// why: a fabricated reading needs a fabricated reporter and cadence, and
// inventing provenance is the one thing M6 exists to prevent). That is right
// for a fixture and useless for a presentation -- with nothing reporting, the
// reporters panel, staleness, flap compression, the drift queue and the
// transition timeline all render their empty states, and a visitor sees none of
// the milestone.
//
// So this does not insert rows. Every reading below goes through
// store.ObservedRecorder.RecordObservation -- the same function the webhook
// calls -- with a real agent actor and a real environment scope, so it is
// subject to the same validation, the same monotonicity rule, the same
// transition-only logging and the same flap arithmetic as a live collector.
// Nothing here can produce a state the production path could not.
//
// The reporters are honestly machines: they carry agent actor kinds and appear
// as `agent` beside every value they wrote, which is what rule 5 requires and
// what stops a demo reading as though a person asserted any of it.
//
// Off unless INV_SEED_OBSERVATIONS is set, so the honest empty state remains
// what an operator gets on a real deployment.

package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Demo reporter identities. They match the credentials the Makefile configures
// in INV_AGENT_TOKENS, so the reporters panel lines up with what the running
// server would actually accept -- a panel naming a credential the server does
// not know is worse than an empty one.
const (
	ReporterProd = "mon-prod"
	ReporterOOB  = "mon-oob"
)

// observations stages a plausible estate: mostly healthy, one real outage with
// an onset worth reading, one collector that has stopped, one entity flapping,
// one report about something the inventory does not have, and one operator
// override shadowing a reading they know is wrong.
//
// Each is chosen because it makes a different panel say something. A demo where
// everything is green proves only that the page renders.
func (b *builder) observations() {
	prod, err := domain.AgentActor(ReporterProd)
	if err != nil {
		b.fail(fmt.Errorf("building the demo prod reporter: %w", err))
		return
	}
	oob, err := domain.AgentActor(ReporterOOB)
	if err != nil {
		b.fail(fmt.Errorf("building the demo oob reporter: %w", err))
		return
	}
	// Scoped to every environment the demo estate uses, because the fixture
	// places sw-core-1 in {prod, dev} and rule 6 requires a credential to cover
	// ALL of an entity's environments, not merely one of them.
	scope, err := domain.NewEnvironmentScope([]string{"prod", "dev", "transit"})
	if err != nil {
		b.fail(fmt.Errorf("building the demo reporter scope: %w", err))
		return
	}

	// A frozen wall clock the staging can walk forward. RecordObservation reads
	// the store's clock for last_report_at and state_since, so history has to be
	// written from the past forwards rather than backdated afterwards --
	// backdating would mean touching the columns directly, which is exactly the
	// thing this file refuses to do.
	base := b.now.Add(-6 * time.Hour)
	clock := &demoClock{at: base}
	recorder := store.NewObservedRecorder(b.store.WithClock(clock.Now))

	report := func(actor domain.Actor, entityType, entityID string, state domain.HealthState, interval int) {
		if b.err != nil {
			return
		}
		_, err := recorder.RecordObservation(b.ctx, actor, scope, domain.ObservationSpec{
			EntityType:      entityType,
			EntityID:        entityID,
			State:           string(state),
			ReportedAt:      domain.FormatTime(clock.at),
			IntervalSeconds: interval,
		})
		if err != nil {
			b.fail(fmt.Errorf("staging demo observation for %s %s: %w", entityType, entityID, err))
		}
	}

	// ---- a healthy baseline, so "down" means something by contrast ---------
	healthy := []string{"hv-01", "hv-02", "sw-core-1", "sw-core-2", "fw-edge-1", "fw-edge-2"}
	for _, name := range healthy {
		report(prod, domain.ObservableAsset, b.refs.Assets[name], domain.HealthUp, 30)
	}
	// The management plane has its own collector, which is what makes the
	// mgmt/data split visible rather than a claim in a document.
	for _, name := range []string{"hv-01", "hv-02", "hv-03"} {
		report(oob, domain.ObservableAsset, b.refs.Assets[name], domain.HealthUp, 60)
	}

	// ---- one entity genuinely down, 40 minutes ago ------------------------
	//
	// The onset is the point. "down since 20:55" is the question an incident
	// starts with, and a reading that only says "down, polled just now" answers
	// the wrong one.
	clock.advance(5 * time.Hour)
	for _, name := range healthy {
		report(prod, domain.ObservableAsset, b.refs.Assets[name], domain.HealthUp, 30)
	}
	clock.advance(20 * time.Minute)
	report(prod, domain.ObservableAsset, b.refs.Assets["hv-03"], domain.HealthDown, 30)

	// ---- a collector that stopped ----------------------------------------
	//
	// mon-oob reported at the top of the window and never again. Rule 8 renders
	// its readings as unknown past three intervals and the reporters panel calls
	// it out once, rather than a dozen entities quietly going green.

	// ---- keep reporting right up to the present --------------------------
	//
	// This is the part a first attempt got wrong, and it is worth stating why.
	//
	// Staging history that ENDS in the past makes every reading stale: a
	// 30-second cadence is unbelievable after 90 seconds (rule 8), so a demo
	// whose last report was half an hour ago shows a dead estate and a dead
	// collector, not a down host. The three timestamps exist precisely so those
	// are different facts -- state_since holds the onset while last_report_at
	// keeps moving -- and a repeat report is what exercises the distinction.
	//
	// So hv-03 keeps reporting `down` up to now. Its onset stays at the
	// transition; only its freshness moves.
	clock.at = b.now.Add(-90 * time.Second)
	for _, name := range healthy {
		report(prod, domain.ObservableAsset, b.refs.Assets[name], domain.HealthUp, 30)
	}
	report(prod, domain.ObservableAsset, b.refs.Assets["hv-03"], domain.HealthDown, 30)
	clock.at = b.now.Add(-20 * time.Second)
	for _, name := range healthy {
		report(prod, domain.ObservableAsset, b.refs.Assets[name], domain.HealthUp, 30)
	}
	report(prod, domain.ObservableAsset, b.refs.Assets["hv-03"], domain.HealthDown, 30)

	// mon-oob is deliberately NOT brought forward. It reported at the top of the
	// window and stopped, which is what makes it render as one silent collector
	// rather than three entities drifting quietly to unknown.

	// ---- one entity flapping ---------------------------------------------
	//
	// Enough transitions inside FlapWindow to trip compression, so the timeline
	// shows a flap episode and says how much it covered. vm-queue-1 is a good
	// subject: a single-instance service, so the oscillation is visible without
	// any capacity arithmetic getting in the way.
	queue := b.refs.Assets["vm-queue-1"]
	clock.at = b.now.Add(-4 * time.Minute)
	for i := range 8 {
		state := domain.HealthDown
		if i%2 == 1 {
			state = domain.HealthUp
		}
		clock.advance(25 * time.Second)
		report(prod, domain.ObservableAsset, queue, state, 30)
	}

	// ---- something the inventory does not have ----------------------------
	//
	// Rule 6: an asset the estate has and the inventory does not is a finding,
	// not noise. The report is refused (404) and queued as drift, which is what
	// the dashboard panel shows. The error is expected and deliberately not
	// treated as a seeding failure.
	clock.at = b.now.Add(-30 * time.Second)
	_, _ = recorder.RecordObservation(b.ctx, prod, scope, domain.ObservationSpec{
		EntityType:      domain.ObservableAsset,
		EntityID:        "hv-04-rack-b2",
		State:           string(domain.HealthUp),
		ReportedAt:      domain.FormatTime(clock.at),
		IntervalSeconds: 30,
	})

	// Rule 4 keeps a report that changes nothing OFF the write path: the
	// last_report_at bump is buffered and flushed on an interval, so the
	// heartbeats above are still in memory. The server's own flusher owns a
	// different recorder and will never see this one, so without an explicit
	// flush every staged reading keeps the timestamp of the last transition --
	// which, at a 30-second cadence, renders the whole estate stale and makes
	// mon-prod look as dead as mon-oob. That is exactly what the first version
	// of this file shipped.
	if _, err := recorder.Flush(b.ctx); err != nil {
		b.fail(fmt.Errorf("flushing staged demo observations: %w", err))
	}
}

// StageDemoOverride records the demo's one operator override.
//
// Separate from observations() and called later, because an override is
// DECLARED state written by a person (rule 14) and the operator account does
// not exist yet when the inventory is seeded -- ensureAdmin runs after
// loadDemoData. Calling it from the builder silently produced no override at
// all, which is the failure this split fixes.
//
// It shadows a reading rather than mutating it: the reporter keeps recording
// the truth underneath, which is what an incident review needs afterwards.
func StageDemoOverride(ctx context.Context, s *store.SQLStore, username string) error {
	admin, err := s.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("looking up %s for the demo override: %w", username, err)
	}
	// The flapping queue host: an override on something visibly unstable is the
	// case an operator actually meets.
	var entityID string
	if err := s.DB().Reader.GetContext(ctx, &entityID, s.DB().Rebind(
		`SELECT id FROM asset WHERE name = ?`), "vm-queue-1"); err != nil {
		return fmt.Errorf("finding the demo override subject: %w", err)
	}

	now := s.Now()
	o, err := domain.NewHealthOverride(store.NewID(), domain.HealthOverrideSpec{
		EntityType:    domain.ObservableAsset,
		EntityID:      entityID,
		AssertedState: string(domain.HealthUp),
		Reason:        "probe checks the wrong port; the queue is serving. INC-4102",
		ExpiresAt:     now.Add(4 * time.Hour),
	}, domain.UserActor(admin), now)
	if err != nil {
		return fmt.Errorf("building the demo override: %w", err)
	}
	if err := s.CreateHealthOverride(ctx, domain.AdministratorPermit(domain.UserActor(admin)), o); err != nil {
		return fmt.Errorf("recording the demo override: %w", err)
	}
	return nil
}

// demoClock walks forward through the staging window.
//
// Deliberately not the wall clock: the readings need an onset in the past for
// "down since" to say anything, and the only honest way to get one is to write
// the history in order rather than to backdate a row afterwards.
type demoClock struct{ at time.Time }

func (c *demoClock) Now() time.Time { return c.at }

func (c *demoClock) advance(d time.Duration) { c.at = c.at.Add(d) }
