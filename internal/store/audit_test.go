// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/gabriel/invctl/internal/domain"
)

// Tests for docs/AUDIT.md rule 15: the audit views must survive volume, and the
// incident timeline is a read-side fold of the two ledgers.
//
// Both engines. The UNION ALL is the part most worth running twice: it is the
// one query in this repository whose column types are decided by union
// resolution rather than by a table definition, and PostgreSQL and SQLite reach
// that decision by different routes.

// TestTimelineFoldsBothLedgersInOneOrdering.
//
// The declared change at 09:58 and the transition to `down` at 10:01 are the
// whole answer to "what changed just before this broke", and they are invisible
// if the two ledgers are read on separate pages. Rule 10 keeps them in separate
// tables at write time; rule 15 folds them at read time.
func TestTimelineFoldsBothLedgersInOneOrdering(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// A declared change, then an observed transition three minutes later.
			f.clock.Advance(time.Hour)
			asset, err := f.store.GetAsset(f.ctx, f.assetID)
			if err != nil {
				t.Fatalf("getting asset: %v", err)
			}
			updated := asset.Asset
			updated.Vendor = strptr("Dell")
			if err := f.store.UpdateAsset(f.ctx, testActor, &updated, nil); err != nil {
				t.Fatalf("updating asset: %v", err)
			}
			declaredAt := domain.FormatTime(f.clock.Now())

			f.clock.Advance(3 * time.Minute)
			f.record(t, f.report("down", f.clock.Now()))
			observedAt := domain.FormatTime(f.clock.Now())

			entries, err := f.store.EntityTimeline(f.ctx, "asset", f.assetID, 20)
			if err != nil {
				t.Fatalf("reading timeline: %v", err)
			}
			if len(entries) < 2 {
				t.Fatalf("timeline has %d entries; want the create, the update and the transition", len(entries))
			}

			// Newest first, and the two ledgers interleave by timestamp rather
			// than appearing as two blocks.
			if entries[0].Source != TimelineObserved || entries[0].At != observedAt {
				t.Fatalf("newest entry = %+v; want the observed transition at %s", entries[0], observedAt)
			}
			if entries[0].ToState != string(domain.HealthDown) {
				t.Errorf("transition to_state = %q, want down", entries[0].ToState)
			}
			if entries[1].Source != TimelineDeclared || entries[1].At != declaredAt {
				t.Fatalf("second entry = %+v; want the declared update at %s", entries[1], declaredAt)
			}
			for i := 1; i < len(entries); i++ {
				if entries[i-1].At < entries[i].At {
					t.Fatalf("timeline is not newest-first at %d: %s before %s",
						i, entries[i-1].At, entries[i].At)
				}
			}

			// Rule 5: an observed row still renders an actor and a kind, derived
			// from the reporter rather than invented.
			if entries[0].DisplayActor() != "prom-a" || entries[0].ActorKind() != domain.ActorKindAgent {
				t.Errorf("observed row actor = %q/%q, want prom-a/agent",
					entries[0].DisplayActor(), entries[0].ActorKind())
			}
			if entries[1].ActorKind() != domain.ActorKindUser {
				t.Errorf("declared row actor kind = %q, want user", entries[1].ActorKind())
			}
			// A transition carries no diff, and a change carries no state.
			if entries[0].Detail != "" {
				t.Errorf("observed row detail = %q, want empty", entries[0].Detail)
			}
			if entries[1].ToState != "" || entries[1].Reporter != "" {
				t.Errorf("declared row carries observed columns: %+v", entries[1])
			}
		})
	}
}

// TestTimelineIncludesOneHopDeclaredNeighbours.
//
// "What changed just before this broke" is almost never a change to the thing
// that broke. The host was retired, the port was unpatched, somebody re-pointed
// the dependency. A timeline that only shows the entity's own history answers
// the easy half of the question.
func TestTimelineIncludesOneHopDeclaredNeighbours(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// The instance's service is what we ask about; the change is to the
			// asset hosting it, which is one hop across the placement.
			instance, err := f.store.GetInstance(f.ctx, f.instanceID)
			if err != nil {
				t.Fatalf("getting instance: %v", err)
			}
			serviceID := instance.ServiceID

			f.clock.Advance(time.Hour)
			if err := f.store.RetireAsset(f.ctx, testActor, f.assetID); err != nil {
				t.Fatalf("retiring host: %v", err)
			}

			own, err := f.store.EntityTimeline(f.ctx, "service", serviceID, 50)
			if err != nil {
				t.Fatalf("reading the narrow timeline: %v", err)
			}
			for _, entry := range own {
				if entry.EntityType == "asset" {
					t.Fatalf("the narrow timeline leaked a neighbour: %+v", entry)
				}
			}

			folded, refs, err := f.store.TimelineForEntityAndNeighbours(f.ctx, "service", serviceID, 50)
			if err != nil {
				t.Fatalf("reading the folded timeline: %v", err)
			}
			if refs[0].Type != "service" || refs[0].ID != serviceID {
				t.Fatalf("neighbour set does not start with the subject: %+v", refs[0])
			}

			var sawHostRetire bool
			for _, entry := range folded {
				if entry.EntityType == "asset" && entry.EntityID == f.assetID &&
					entry.Label == domain.ActionRetire {
					sawHostRetire = true
					if entry.Subject {
						t.Error("a neighbour's change is marked as the subject's own")
					}
					if entry.Relation != RelationHost {
						t.Errorf("host relation = %q, want %q", entry.Relation, RelationHost)
					}
					if entry.EntityLabel != "app-01" {
						t.Errorf("host label = %q, want app-01; an operator cannot act on a bare uuid", entry.EntityLabel)
					}
				}
			}
			if !sawHostRetire {
				t.Fatal("the folded timeline does not show the host being retired: that is the 03:00 question")
			}

			// The placement is an edge, so the observed transitions of the
			// instance running this service belong on the service's timeline too.
			f.clock.Advance(time.Minute)
			spec := f.report("down", f.clock.Now())
			spec.EntityType = domain.ObservableServiceInstance
			spec.EntityID = f.instanceID
			f.record(t, spec)

			folded, _, err = f.store.TimelineForEntityAndNeighbours(f.ctx, "service", serviceID, 50)
			if err != nil {
				t.Fatalf("re-reading the folded timeline: %v", err)
			}
			if folded[0].Source != TimelineObserved || folded[0].EntityID != f.instanceID {
				t.Fatalf("newest folded entry = %+v; want the instance transition", folded[0])
			}
		})
	}
}

// TestChangePagingWalksTheLogWithoutGapsOrRepeats.
//
// Cursor paging rather than OFFSET, because the audit trail is written to
// constantly and an offset silently skips or repeats rows when something lands
// between two page requests. The property under test is the one that matters:
// walking every page must visit every entry exactly once.
func TestChangePagingWalksTheLogWithoutGapsOrRepeats(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// Enough entries to need several pages, some sharing a timestamp so
			// the (at, id) tie-break is actually exercised.
			envID := mustEnvironment(t, f.store, f.ctx, "extra", domain.EnvRoleProduction)
			for i := 0; i < 12; i++ {
				if i%3 == 0 {
					f.clock.Advance(time.Minute)
				}
				mustAsset(t, f.store, f.ctx, domain.KindServer, assetName(i), nil, envID)
			}

			total, err := f.store.ListChanges(f.ctx, ChangeFilter{Limit: 1})
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if total.Total < 12 {
				t.Fatalf("total = %d, want at least the 12 assets just created", total.Total)
			}

			seen := map[string]int{}
			pages := 0
			filter := ChangeFilter{Limit: 5}
			for {
				page, err := f.store.ListChanges(f.ctx, filter)
				if err != nil {
					t.Fatalf("page %d: %v", pages, err)
				}
				if page.Total != total.Total {
					t.Errorf("page %d reports total %d, want %d: the cursor must not change the count",
						pages, page.Total, total.Total)
				}
				for _, c := range page.Changes {
					seen[c.ID]++
				}
				pages++
				if page.Older == nil {
					if len(page.Changes) == 0 && pages > 1 {
						t.Error("the last page came back empty; a dead 'older' link was offered")
					}
					break
				}
				if pages > 20 {
					t.Fatal("paging did not terminate")
				}
				filter.Before = page.Older
			}

			if len(seen) != total.Total {
				t.Errorf("walked %d distinct entries over %d pages, want %d", len(seen), pages, total.Total)
			}
			for id, n := range seen {
				if n != 1 {
					t.Errorf("entry %s appeared %d times", id, n)
				}
			}

			// And back up again: paging towards newer entries returns the first
			// page, still newest-first.
			first, err := f.store.ListChanges(f.ctx, ChangeFilter{Limit: 5})
			if err != nil {
				t.Fatalf("re-reading the first page: %v", err)
			}
			if first.Newer != nil {
				t.Error("the newest page offers a 'newer' link")
			}
			second, err := f.store.ListChanges(f.ctx, ChangeFilter{Limit: 5, Before: first.Older})
			if err != nil {
				t.Fatalf("second page: %v", err)
			}
			back, err := f.store.ListChanges(f.ctx, ChangeFilter{Limit: 5, After: second.Newer})
			if err != nil {
				t.Fatalf("paging back up: %v", err)
			}
			if len(back.Changes) != len(first.Changes) {
				t.Fatalf("paging back up returned %d rows, want %d", len(back.Changes), len(first.Changes))
			}
			for i := range back.Changes {
				if back.Changes[i].ID != first.Changes[i].ID {
					t.Fatalf("row %d after paging back = %s, want %s", i, back.Changes[i].ID, first.Changes[i].ID)
				}
			}
			for i := 1; i < len(back.Changes); i++ {
				if back.Changes[i-1].At < back.Changes[i].At {
					t.Fatal("a page read upwards is not rendered newest-first")
				}
			}
		})
	}
}

func assetName(i int) string {
	return "bulk-" + string(rune('a'+i%26)) + "-" + domain.FormatTime(time.Unix(int64(i), 0).UTC())
}

// TestChangeRangeAndEntityFilterNarrowTheLog: the date range is what stops a
// busy Tuesday burying the entry that explains an incident.
func TestChangeRangeAndEntityFilterNarrowTheLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)
			envID := mustEnvironment(t, f.store, f.ctx, "extra", domain.EnvRoleProduction)

			f.clock.Advance(48 * time.Hour)
			windowStart := domain.FormatTime(f.clock.Now())
			mustAsset(t, f.store, f.ctx, domain.KindServer, "in-window-1", nil, envID)
			f.clock.Advance(time.Hour)
			mustAsset(t, f.store, f.ctx, domain.KindServer, "in-window-2", nil, envID)
			windowEnd := domain.FormatTime(f.clock.Now())
			f.clock.Advance(48 * time.Hour)
			mustAsset(t, f.store, f.ctx, domain.KindServer, "after-window", nil, envID)

			page, err := f.store.ListChanges(f.ctx, ChangeFilter{
				EntityType: "asset", From: windowStart, To: windowEnd, Limit: 50,
			})
			if err != nil {
				t.Fatalf("filtered page: %v", err)
			}
			if page.Total != 2 {
				t.Fatalf("range total = %d, want 2", page.Total)
			}
			for _, c := range page.Changes {
				if c.EntityType != "asset" {
					t.Errorf("entity filter leaked a %s row", c.EntityType)
				}
				if c.At < windowStart || c.At > windowEnd {
					t.Errorf("entry at %s is outside [%s, %s]", c.At, windowStart, windowEnd)
				}
			}

			// The environment create is inside the window too, so an unfiltered
			// range is strictly larger -- which is what proves the entity filter
			// did something.
			unfiltered, err := f.store.ListChanges(f.ctx, ChangeFilter{From: windowStart, To: windowEnd, Limit: 50})
			if err != nil {
				t.Fatalf("unfiltered range: %v", err)
			}
			if unfiltered.Total < page.Total {
				t.Errorf("unfiltered range total %d is smaller than the filtered %d", unfiltered.Total, page.Total)
			}
		})
	}
}

// TestReporterSilenceIsOneEventNotAThousand.
//
// The second half of rule 8. Per-entity staleness alone turns a dead collector
// into a thousand entities going quietly unknown, which is a thousand things to
// notice and therefore nothing anybody notices.
func TestReporterSilenceIsOneEventNotAThousand(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			f.record(t, f.report("up", f.clock.Now()))
			instance := f.report("up", f.clock.Now())
			instance.EntityType = domain.ObservableServiceInstance
			instance.EntityID = f.instanceID
			f.record(t, instance)

			status, err := f.store.ListReporterStatus(f.ctx)
			if err != nil {
				t.Fatalf("listing reporter status: %v", err)
			}
			if len(status) != 1 {
				t.Fatalf("got %d reporters, want 1: two entities watched by one collector is one line", len(status))
			}
			if status[0].Entities != 2 {
				t.Errorf("entity count = %d, want 2", status[0].Entities)
			}
			if status[0].Silent {
				t.Error("a reporter that just reported is silent")
			}

			// Three intervals of nothing. Every entity goes unknown, and the
			// collector reports as silent exactly once.
			f.clock.Advance(4 * 30 * time.Second)

			status, err = f.store.ListReporterStatus(f.ctx)
			if err != nil {
				t.Fatalf("re-listing reporter status: %v", err)
			}
			if len(status) != 1 || !status[0].Silent {
				t.Fatalf("reporter status after four intervals = %+v; want one silent reporter", status)
			}
			if status[0].SilentSince == "" {
				t.Error("a silent reporter does not say since when")
			}

			readings, err := f.store.GetObservedState(f.ctx, domain.ObservableAsset, f.assetID)
			if err != nil {
				t.Fatalf("reading observed state: %v", err)
			}
			if len(readings) != 1 {
				t.Fatalf("got %d readings, want 1", len(readings))
			}
			if readings[0].Display != domain.HealthUnknown {
				t.Errorf("a stale reading displays %q; rule 8 says unknown, never the last value", readings[0].Display)
			}
			if readings[0].StaleSince == "" {
				t.Error("a stale reading does not say since when")
			}
			// The stored state is untouched: staleness is computed, never written.
			if readings[0].State != domain.HealthUp {
				t.Errorf("stored state = %q, want up: staleness is a read-time judgement", readings[0].State)
			}
		})
	}
}

// TestObservedNoiseCannotEvictDeclaredHistory.
//
// The entity timeline is the panel answering "what changed just before this
// broke", and observed rows are unboundedly noisier than declared ones by
// construction. With one LIMIT across both ledgers, a perfectly ordinary
// incident -- forty instances each transitioning once -- pushed every declared
// row off the page, including the configuration change that caused the cascade.
// Rule 15 names this failure on /changes; sharing a budget merely relocated it
// onto the screen an incident review actually opens.
func TestObservedNoiseCannotEvictDeclaredHistory(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			assetID := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-timeline", nil, envID)

			// One declared change, then a burst of ordinary observed noise --
			// deliberately below FlapThreshold per reporter, so none of it is
			// compressed away and the pressure is real.
			a, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("reading asset: %v", err)
			}
			a.Vendor = strptr("Dell")
			if err := s.UpdateAsset(ctx, testActor, &a.Asset, nil); err != nil {
				t.Fatalf("updating asset: %v", err)
			}

			// Generated through the real observed path rather than by
			// inserting rows, so the noise is exactly the shape a live estate
			// produces.
			scope, err := domain.NewEnvironmentScope([]string{"prod"})
			if err != nil {
				t.Fatalf("building scope: %v", err)
			}
			rec := NewObservedRecorder(s)
			const reporters, each = 6, 4
			at := s.now()
			for r := 0; r < reporters; r++ {
				agent, err := domain.AgentActor(fmt.Sprintf("prom-%d", r))
				if err != nil {
					t.Fatalf("building agent actor: %v", err)
				}
				for i := 0; i < each; i++ {
					state := domain.HealthDown
					if i%2 == 1 {
						state = domain.HealthUp
					}
					at = at.Add(time.Second)
					if _, err := rec.RecordObservation(ctx, agent, scope, domain.ObservationSpec{
						EntityType:      domain.ObservableAsset,
						EntityID:        assetID,
						State:           string(state),
						ReportedAt:      domain.FormatTime(at),
						IntervalSeconds: 30,
					}); err != nil {
						t.Fatalf("recording observation: %v", err)
					}
				}
			}

			entries, err := s.Timeline(ctx, []EntityRef{{Type: "asset", ID: assetID}}, 20)
			if err != nil {
				t.Fatalf("reading timeline: %v", err)
			}

			declared, observed := 0, 0
			for _, e := range entries {
				switch e.Source {
				case TimelineDeclared:
					declared++
				case TimelineObserved:
					observed++
				}
			}
			if declared == 0 {
				t.Errorf("%d observed rows evicted every declared row from the timeline; "+
					"the configuration change that caused an incident is exactly what this "+
					"panel exists to show", observed)
			}
			if observed == 0 {
				t.Error("no observed rows survived either, so the split starved the wrong side")
			}
		})
	}
}
