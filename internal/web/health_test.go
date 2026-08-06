// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	webassets "github.com/madalinignisca/invctl/web"
)

// What a person sees: docs/AUDIT.md rules 5, 8, 14 and 15, driven over real
// HTTP against the real router.
//
// These are view tests on purpose. Every rule here is about what an operator
// reads at 03:00, and a store test cannot show that the page renders a state
// without its reporter, or that a stale reading still shows its last value, or
// that an override is invisible on the page an incident review opens.

// ---------------------------------------------------------------------------
// Harness additions
// ---------------------------------------------------------------------------

// insertHealth writes a reading straight into the observed table.
//
// Deliberately not through the webhook: these tests need a reading that is
// already old, and the alternative is either a sleep or a clock injected
// through the whole HTTP stack. The row shape is the one RecordObservation
// writes, and rule 8's arithmetic is what is under test rather than the write
// path, which internal/store covers.
func (h *harness) insertHealth(entityType, entityID, reporter, state string, lastReport time.Time, interval int) {
	h.t.Helper()
	at := domain.FormatTime(lastReport)
	writer := h.store.DB().Writer
	_, err := writer.Exec(writer.Rebind(`
		INSERT INTO asset_health (entity_type, entity_id, reporter, state, state_since,
		                          reported_at, last_report_at, interval_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		entityType, entityID, reporter, state, at, at, at, interval)
	if err != nil {
		h.t.Fatalf("inserting health for %s %s: %v", entityType, entityID, err)
	}
}

// instanceOn returns one workload placed on an asset, so a test can name a
// service_instance without hard-coding a fixture id.
func (h *harness) instanceOn(assetName string) store.InstanceRow {
	h.t.Helper()
	rows, err := h.store.ListInstancesByHost(context.Background(), h.asset(assetName))
	if err != nil {
		h.t.Fatalf("listing instances on %s: %v", assetName, err)
	}
	if len(rows) == 0 {
		h.t.Fatalf("no instances placed on %s; this test needs one", assetName)
	}
	return rows[0]
}

// logout ends the current session, so a test can sign in as somebody else.
// GET /login redirects while a session exists, and the harness reads its CSRF
// token from that page.
func (h *harness) logout() {
	h.t.Helper()
	resp := h.post("/logout", url.Values{"csrf_token": {h.csrfToken("/assets")}}, false)
	resp.Body.Close()
}

// overridePost records an override as the signed-in operator.
func (h *harness) overridePost(target, state, reason string, minutes int) *http.Response {
	h.t.Helper()
	token := h.csrfToken("/assets/" + h.asset("vm-app-1"))
	return h.post("/overrides", url.Values{
		"csrf_token":     {token},
		"target":         {target},
		"asserted_state": {state},
		"reason":         {reason},
		"minutes":        {itoa(minutes)},
	}, false)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ---------------------------------------------------------------------------
// Rule 14: the override is declared state, and every view shows it
// ---------------------------------------------------------------------------

// TestOverrideIsADeclaredMutationBehindCSRFAndAdmin.
//
// An override silences a reading. If a read-only account or a forged
// cross-site POST could write one, the estate could be quietly muted by
// somebody with no write access at all.
func TestOverrideIsADeclaredMutationBehindCSRFAndAdmin(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")
	target := domain.ObservableAsset + ":" + assetID

	// A reader cannot.
	h.login("viewer", "viewer-password")
	token := h.csrfToken("/assets/" + assetID)
	resp := h.post("/overrides", url.Values{
		"csrf_token":     {token},
		"target":         {target},
		"asserted_state": {"up"},
		"reason":         {"should not be recorded"},
		"minutes":        {"60"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read-only account overriding a reading returned %d, want 403", resp.StatusCode)
	}

	// Nor can a request without a CSRF token.
	h.logout()
	h.login("admin", "admin-password")
	resp = h.post("/overrides", url.Values{
		"target":         {target},
		"asserted_state": {"up"},
		"reason":         {"no token"},
		"minutes":        {"60"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an override without a CSRF token returned %d, want 400", resp.StatusCode)
	}
	if n := h.count(`SELECT COUNT(*) FROM health_override`); n != 0 {
		t.Fatalf("%d override(s) were written by requests that should have been refused", n)
	}

	// An admin can, and it is audited.
	resp = h.overridePost(target, "up", "probe checks the wrong port, INC-4102", 120)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recording an override returned %d, want 303", resp.StatusCode)
	}
	if n := h.count(`SELECT COUNT(*) FROM health_override WHERE lifecycle = ?`, domain.OverrideActive); n != 1 {
		t.Fatalf("%d active overrides, want 1", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "health_override"); n != 1 {
		t.Errorf("%d change_log rows for the override, want 1: an override is a declared mutation", n)
	}
	// And nothing observed was written by a declared act.
	if n := h.count(`SELECT COUNT(*) FROM observed_transition`); n != 0 {
		t.Errorf("recording an override wrote %d observed transition(s)", n)
	}
}

// TestEveryViewOfAnOverriddenEntityShowsWhoWhyAndUntilWhen.
//
// Rule 14 is explicit: by whom, why, and until when, in every view. An
// override that is invisible on the page an incident review opens is how a real
// outage stays unnoticed for six weeks.
func TestEveryViewOfAnOverriddenEntityShowsWhoWhyAndUntilWhen(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")

	// A reporter says the box is down.
	resp := h.observe(agentProdToken, oneObservation(domain.ObservableAsset, assetID, "down"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the observation returned %d, want 200", resp.StatusCode)
	}

	h.login("admin", "admin-password")
	const reason = "probe checks the wrong port, INC-4102"
	resp = h.overridePost(domain.ObservableAsset+":"+assetID, "up", reason, 120)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recording the override returned %d, want 303", resp.StatusCode)
	}

	var expiresAt string
	reader := h.store.DB().Reader
	if err := reader.Get(&expiresAt, reader.Rebind(
		`SELECT expires_at FROM health_override WHERE entity_id = ?`), assetID); err != nil {
		t.Fatalf("reading the override back: %v", err)
	}

	page := body(t, h.get("/assets/"+assetID, false))
	for _, want := range []string{
		">overridden<",          // that it is overridden at all
		reason,                  // why
		"admin",                 // by whom, resolved from the opaque id
		">user<",                // rule 5: the kind beside the name
		"until",                 // for how long
		"reported underneath",   // and that the reporter is still recording
		`pill pill-down">down<`, // ...the reading itself, unshadowed
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the asset page does not show %q; rule 14 wants who, why and until when", want)
		}
	}

	// The reason, anchored to the BANNER rather than to the page.
	//
	// A bare Contains(page, reason) was presence-not-completeness: signed in as
	// admin the reason also appears inside the amend form's <input value="...">,
	// so deleting it from the banner left this test green while a read-only
	// operator -- who gets no amend form, because it is behind {{if .CanWrite}}
	// -- saw "overridden", "by admin", "until <T>" and no reason at all. Rule 14
	// says every view, and the view without an edit form is exactly the one a
	// presence check stops covering.
	bannerReason := `margin-top:3px">` + reason + `</div>`
	if !strings.Contains(page, bannerReason) {
		t.Errorf("the override banner itself does not carry the reason; it may only be surviving "+
			"inside the admin-only amend form, which a read-only operator never sees.\n"+
			"looked for: %s", bannerReason)
	}

	// The landing page too: somebody reading a green estate needs to know how
	// much of that green was asserted rather than observed.
	dashboard := body(t, h.get("/", false))
	if !strings.Contains(dashboard, "active override") {
		t.Error("the dashboard does not say anything is currently overridden")
	}
	if !strings.Contains(dashboard, reason) {
		t.Error("the dashboard lists an override without its reason")
	}

	// And the observation underneath is untouched: an override shadows, it does
	// not mutate. The reporter keeps recording the truth, because when the
	// override lapses you need to know what actually happened.
	var stored string
	if err := reader.Get(&stored, reader.Rebind(
		`SELECT state FROM asset_health WHERE entity_id = ?`), assetID); err != nil {
		t.Fatalf("reading the observation back: %v", err)
	}
	if stored != "down" {
		t.Errorf("the stored observation is now %q; an override must never mutate it", stored)
	}
}

// TestOverrideWithoutAReasonIsA422WithTheFormRerendered. The reason is
// mandatory because whoever reads it during the next incident is not the person
// who wrote it.
func TestOverrideWithoutAReasonIsA422WithTheFormRerendered(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.asset("vm-app-1")

	token := h.csrfToken("/assets/" + assetID)
	resp := h.post("/overrides", url.Values{
		"csrf_token":     {token},
		"target":         {domain.ObservableAsset + ":" + assetID},
		"asserted_state": {"up"},
		"reason":         {"   "},
		"minutes":        {"60"},
	}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an override with no reason returned %d, want 422", resp.StatusCode)
	}
	page := body(t, resp)
	if !strings.Contains(page, `id="override-form"`) {
		t.Error("the 422 did not re-render the form partial")
	}
	if !strings.Contains(page, "field-error") {
		t.Error("the re-rendered form carries no error state")
	}
	if n := h.count(`SELECT COUNT(*) FROM health_override`); n != 0 {
		t.Errorf("%d override(s) written despite the validation failure", n)
	}
}

// TestAnOverrideCanBeAmendedAndCleared, both audited, and clearing keeps the
// row -- "who silenced this, and for how long" outlives the silence.
func TestAnOverrideCanBeAmendedAndCleared(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.asset("vm-app-1")

	resp := h.overridePost(domain.ObservableAsset+":"+assetID, "up", "first reason", 60)
	resp.Body.Close()

	var id string
	reader := h.store.DB().Reader
	if err := reader.Get(&id, reader.Rebind(`SELECT id FROM health_override WHERE entity_id = ?`), assetID); err != nil {
		t.Fatalf("reading the override id: %v", err)
	}

	token := h.csrfToken("/assets/" + assetID)
	resp = h.post("/overrides/"+id, url.Values{
		"csrf_token":     {token},
		"asserted_state": {"degraded"},
		"reason":         {"partially recovered, still not trusting the probe"},
		"minutes":        {"30"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("amending returned %d, want 303", resp.StatusCode)
	}

	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, "partially recovered") {
		t.Error("the amended reason is not shown")
	}

	resp = h.post("/overrides/"+id+"/clear", url.Values{"csrf_token": {token}}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("clearing returned %d, want 303", resp.StatusCode)
	}

	if n := h.count(`SELECT COUNT(*) FROM health_override WHERE id = ?`, id); n != 1 {
		t.Error("clearing an override deleted the row; soft delete applies here too")
	}
	if n := h.count(`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, "health_override"); n != 3 {
		t.Errorf("%d change_log rows, want 3: create, amend and clear are each a declared mutation", n)
	}
	page = body(t, h.get("/assets/"+assetID, false))
	if strings.Contains(page, ">overridden<") {
		t.Error("a cleared override is still shadowing the reading")
	}
}

// ---------------------------------------------------------------------------
// Rule 8: staleness, computed at read time, in every view
// ---------------------------------------------------------------------------

// TestAStaleReadingRendersUnknownNotItsLastValue.
//
// The failure this prevents is the quiet one: an intruder's first act is
// killing the collector, and under transition-only logging a dead collector and
// a healthy estate are otherwise the same picture, forever.
func TestAStaleReadingRendersUnknownNotItsLastValue(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.asset("vm-app-1")

	// Four intervals of silence: past the three-interval horizon.
	h.insertHealth(domain.ObservableAsset, assetID, "prom-a", "down",
		time.Now().UTC().Add(-4*30*time.Second), 30)

	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, `pill pill-muted">unknown<`) {
		t.Error("a stale reading does not render as unknown")
	}
	if !strings.Contains(page, "stale since") {
		t.Error("a stale reading does not say since when it stopped being believed")
	}
	if strings.Contains(page, `pill pill-down">down<`) {
		t.Error("a stale reading still renders its last value; rule 8 says unknown, never the last value")
	}
	if !strings.Contains(page, "prom-a") {
		t.Error("the reading is rendered without naming its reporter")
	}

	// Positive control: a fresh reading from the same reporter renders its
	// actual state, so the assertions above are about staleness rather than
	// about the panel never rendering a state at all.
	fresh := newHarness(t)
	fresh.login("admin", "admin-password")
	freshID := fresh.asset("vm-app-1")
	fresh.insertHealth(domain.ObservableAsset, freshID, "prom-a", "down", time.Now().UTC(), 30)
	page = body(t, fresh.get("/assets/"+freshID, false))
	if !strings.Contains(page, `pill pill-down">down<`) {
		t.Error("a fresh reading does not render its state")
	}
	if strings.Contains(page, "stale since") {
		t.Error("a fresh reading is reported as stale")
	}
}

// TestTheTimelineAccountsForACompressedEpisode drives rule 9 end to end over
// HTTP -- six oscillations through the observation webhook open an episode, and
// a seventh report to a value the episode has not seen escapes compression and
// closes it.
//
// Compression is only acceptable while it is visible, so the timeline has to
// say that an episode happened and how much churn it covered. A bracket
// rendered as a bare state change would read as one transition where there
// were several, which is the suppression rule 9 forbids wearing the label of
// the compression it permits.
func TestTheTimelineAccountsForACompressedEpisode(t *testing.T) {
	h := newHarness(t)
	instance := h.instanceOn("vm-app-1")

	// Alternating up/down, then a value the oscillation never touched. The
	// reporter's clock advances; the server's does not need to, because the
	// window is five minutes and this batch lands inside a second of it.
	var items []string
	states := []string{"up", "down", "up", "down", "up", "down", "degraded"}
	for i, state := range states {
		items = append(items, fmt.Sprintf(
			`{"entity_type":%q,"entity_id":%q,"state":%q,"reported_at":%q,"interval_seconds":30}`,
			domain.ObservableServiceInstance, instance.ID, state,
			domain.FormatTime(time.Date(2026, 7, 28, 9, i, 0, 0, time.UTC))))
	}
	resp := h.observe(agentProdToken, batchOf(items...))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the observation batch returned %d, want 200", resp.StatusCode)
	}

	h.login("admin", "admin-password")
	page := body(t, h.get("/services/"+instance.ServiceID, false))

	if !strings.Contains(page, "flap_open") {
		t.Error("the timeline does not show that compression started")
	}
	if !strings.Contains(page, "flap_close") {
		t.Error("the timeline does not show that compression ended")
	}
	if !strings.Contains(page, "episode covered 2 transitions") {
		t.Error("the closing row does not say how much churn the episode covered; " +
			"compression a reader cannot size is suppression")
	}
	// The escape hatch, on the page: the novel value is its own row rather than
	// being absorbed into the episode.
	//
	// Anchored to the transition markup, not to the bare word. A service page
	// contains "degraded" twice before any observation exists -- the override
	// form's <option> and the pill-degraded class used by the flapping badge --
	// so `Contains(page, "degraded")` was true with the escape hatch entirely
	// disabled and no timeline row for the novel value at all.
	if !strings.Contains(page, "up → degraded") {
		t.Error("the transition to a value the episode had not seen is not on the timeline as " +
			"its own row; a novel value is by definition not part of the oscillation being " +
			"compressed, and that is what stops a stolen token hiding behind a tripped threshold")
	}
}

// insertFlappingHealth writes a reading with an open flap episode on it.
//
// Same reasoning as insertHealth: the store suite drives the compression
// itself, and what is under test here is only what the page does with the
// result. Nothing else in the web suite exercises the flapping branch of the
// health partial, and an unexercised template branch fails at render time in
// front of whoever opened the page.
func (h *harness) insertFlappingHealth(entityType, entityID, reporter, state string,
	lastReport time.Time, interval, flapCount int, first, seen string) {
	h.t.Helper()
	at := domain.FormatTime(lastReport)
	writer := h.store.DB().Writer
	_, err := writer.Exec(writer.Rebind(`
		INSERT INTO asset_health (entity_type, entity_id, reporter, state, state_since,
		                          reported_at, last_report_at, interval_seconds,
		                          flap_since, flap_count, flap_first_state, flap_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		entityType, entityID, reporter, state, at, at, at, interval,
		domain.FormatTime(lastReport.Add(-2*time.Minute)), flapCount, first, seen)
	if err != nil {
		h.t.Fatalf("inserting flapping health for %s %s: %v", entityType, entityID, err)
	}
}

// TestFlappingIsShownBesideTheStateNotInsteadOfIt is rule 9's closing sentence,
// which is a view requirement and cannot be tested anywhere else.
//
// A page that replaced the state with "flapping" would tell an operator that a
// box which died twenty minutes ago is merely noisy, and that is the reading
// somebody acts on during an incident. The badge also has to say how much is
// being compressed, because compression is only acceptable while it is visible.
func TestFlappingIsShownBesideTheStateNotInsteadOfIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.asset("vm-app-1")

	h.insertFlappingHealth(domain.ObservableAsset, assetID, "prom-a", "down",
		time.Now().UTC(), 30, 9, "up", "up,down")

	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, `pill pill-down">down<`) {
		t.Error("a flapping entity does not render its current state; " +
			"flapping is shown alongside the state, never instead of it")
	}
	if !strings.Contains(page, "flapping") {
		t.Error("a flapping entity does not say it is flapping")
	}
	if !strings.Contains(page, "flapping · 9") {
		t.Error("the badge does not say how many transitions are being compressed")
	}
	if !strings.Contains(page, "prom-a") {
		t.Error("the reading is rendered without naming its reporter")
	}

	// Negative control: a settled reading carries no badge, so the assertion
	// above is about the episode rather than about the word appearing in some
	// static piece of page furniture.
	settled := newHarness(t)
	settled.login("admin", "admin-password")
	settledID := settled.asset("vm-app-1")
	settled.insertHealth(domain.ObservableAsset, settledID, "prom-a", "down", time.Now().UTC(), 30)
	if strings.Contains(body(t, settled.get("/assets/"+settledID, false)), "flapping") {
		t.Error("a settled reading is rendered as flapping")
	}
}

// TestAnUnobservedEntitySaysSoRatherThanLookingHealthy. An empty result is not
// health: nothing has ever reported on it, and the page must say that rather
// than default to up or render an empty cell.
func TestAnUnobservedEntitySaysSoRatherThanLookingHealthy(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/assets/"+h.asset("vm-app-1"), false))
	if !strings.Contains(page, ">unobserved<") {
		t.Error("an entity nothing watches does not say it is unobserved")
	}
}

// TestASilentReporterIsOneEventNotAThousand.
//
// Per-entity staleness alone turns a dead collector into a thousand entities
// going quietly unknown, which is a thousand things to notice and therefore
// nothing anybody notices.
func TestASilentReporterIsOneEventNotAThousand(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	quiet := time.Now().UTC().Add(-10 * time.Minute)
	h.insertHealth(domain.ObservableAsset, h.asset("vm-app-1"), "prom-a", "up", quiet, 30)
	h.insertHealth(domain.ObservableAsset, h.asset("vm-db-1"), "prom-a", "up", quiet, 30)
	h.insertHealth(domain.ObservableAsset, h.asset("hv-01"), "prom-a", "up", quiet, 30)

	dashboard := body(t, h.get("/", false))

	// The invariant is per COLLECTOR, not per row: prom-a watches three things
	// and must account for exactly one silent entry however many entities it
	// covers. The other configured credentials contribute one each because
	// they have never checked in at all -- "never started" and "started and
	// stopped" are different findings and the panel shows both, which is why
	// this counts prom-a's own row rather than the total.
	// prom-a wrote rows without being a configured credential -- a collector
	// whose entry was removed but whose readings remain -- so the expected
	// total is the three configured ids, none of which has ever checked in,
	// plus prom-a itself.
	want := len(testAgentCredentials()) + 1
	if n := strings.Count(dashboard, ">silent<"); n != want {
		t.Errorf("the dashboard reports %d silent entries, want %d: one dead collector watching "+
			"three things counts once, and each configured credential that never checked in "+
			"counts once", n, want)
	}
	if n := strings.Count(dashboard, "prom-a"); n != 1 {
		t.Errorf("prom-a appears %d times in the reporters panel, want exactly 1: a dead collector "+
			"is one alertable event, not one per entity it was watching", n)
	}
	if !strings.Contains(dashboard, "prom-a") {
		t.Error("the reporter panel does not name the collector that stopped")
	}
	// Rule 5 again: a reporter is a machine, and the panel says so.
	if !strings.Contains(dashboard, ">agent<") {
		t.Error("the reporter panel does not mark a collector as a machine rather than a person")
	}
}

// ---------------------------------------------------------------------------
// Rule 15: the audit views must survive volume
// ---------------------------------------------------------------------------

// TestChangesPagesWithoutLosingItsFilter.
//
// Losing the filter on page two answers a different question under the same
// heading, which during an incident is worse than showing nothing.
func TestChangesPagesWithoutLosingItsFilter(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Enough entries to need a second page whatever the fixture happens to
	// contain. Written through the store rather than over HTTP because the
	// subject here is the pager, not the create form.
	ctx := context.Background()
	for i := 0; i < store.DefaultChangePageSize+5; i++ {
		env, err := domain.NewEnvironment(store.NewID(), "bulk"+itoa(i), "Bulk "+itoa(i),
			domain.EnvRoleProduction, false, 3, h.store.Now())
		if err != nil {
			t.Fatalf("building environment %d: %v", i, err)
		}
		if err := h.store.CreateEnvironment(ctx, domain.SystemActor, env); err != nil {
			t.Fatalf("creating environment %d: %v", i, err)
		}
	}

	page := body(t, h.get("/changes?entity_type=environment", false))
	older := extractHref(t, page, "Older")
	if older == "" {
		t.Fatal("no 'Older' link on a log with more entries than one page")
	}
	parsed, err := url.Parse(older)
	if err != nil {
		t.Fatalf("parsing the pager link %q: %v", older, err)
	}
	if got := parsed.Query().Get("entity_type"); got != "environment" {
		t.Errorf("the pager link dropped the filter: entity_type=%q", got)
	}
	if parsed.Query().Get("before") == "" {
		t.Error("the pager link carries no cursor")
	}

	second := body(t, h.get(older, false))
	if !strings.Contains(second, "environment") {
		t.Error("the second page does not honour the entity filter")
	}
	if !strings.Contains(second, "Newer") {
		t.Error("the second page offers no way back up")
	}
	// A filter that matched nothing would also produce a page with no rows, so
	// check the count is reported rather than assuming.
	if strings.Contains(second, "Nothing in that range") {
		t.Error("the second page is empty; a dead 'Older' link was offered")
	}
}

// TestChangesRejectsAMalformedDateRange. A filter is a form, and a form that
// silently ignores what was typed is worse than one that refuses.
func TestChangesRejectsAMalformedDateRange(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.get("/changes?from=last+tuesday", true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a malformed date returned %d, want 422", resp.StatusCode)
	}
	page := body(t, resp)
	if !strings.Contains(page, "field-error") {
		t.Error("the 422 does not say which field is wrong")
	}
	if !strings.Contains(page, `id="change-log"`) {
		t.Error("the 422 did not re-render the filter partial")
	}
}

// TestTheEntityTimelineFoldsBothLedgersAndTheNeighbourhood.
//
// "What changed just before this broke" is almost never a change to the thing
// that broke, and it is invisible if the declared and observed ledgers are read
// on separate pages.
func TestTheEntityTimelineFoldsBothLedgersAndTheNeighbourhood(t *testing.T) {
	h := newHarness(t)
	instance := h.instanceOn("vm-app-1")

	resp := h.observe(agentProdToken,
		oneObservation(domain.ObservableServiceInstance, instance.ID, "down"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the observation returned %d, want 200", resp.StatusCode)
	}

	h.login("admin", "admin-password")
	token := h.csrfToken("/assets/" + instance.HostAssetID)
	resp = h.post("/assets/"+instance.HostAssetID+"/retire", url.Values{"csrf_token": {token}}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("retiring the host returned %d, want 303", resp.StatusCode)
	}

	page := body(t, h.get("/services/"+instance.ServiceID, false))
	if !strings.Contains(page, ">observed<") {
		t.Error("the service timeline does not show the observed transition of its own placement")
	}
	if !strings.Contains(page, ">retire<") {
		t.Error("the service timeline does not show the host being retired: that is the 03:00 question")
	}
	if !strings.Contains(page, instance.HostName) {
		t.Errorf("the timeline names the neighbour by id rather than by name (%s)", instance.HostName)
	}
	if !strings.Contains(page, "runs on") {
		t.Error("the timeline does not say how the neighbour relates to this service")
	}
	// Rule 5 on the view an incident review actually opens: an observed row
	// carries its reporter and the fact that a machine wrote it.
	if !strings.Contains(page, ">agent<") {
		t.Error("an observed timeline row does not mark its writer as a machine")
	}
	if !strings.Contains(page, ">user<") {
		t.Error("a declared timeline row does not mark its writer as a person")
	}
}

// extractHref pulls the first href whose anchor text contains label.
func extractHref(t *testing.T, page, label string) string {
	t.Helper()
	for _, chunk := range strings.Split(page, "<a ") {
		if !strings.Contains(chunk, label) {
			continue
		}
		start := strings.Index(chunk, `href="`)
		if start < 0 {
			continue
		}
		rest := chunk[start+len(`href="`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			continue
		}
		// html/template escapes `&` and `+` inside an href; a browser undoes
		// that while parsing, and a test that does not would silently truncate
		// the query at the first entity reference.
		return html.UnescapeString(rest[:end])
	}
	return ""
}

// ---------------------------------------------------------------------------
// Rule 5: an actor is never rendered without its kind
// ---------------------------------------------------------------------------

// TestNoTemplateRendersAnActorWithoutItsKind.
//
// `change_log.actor` is free TEXT and a machine writes into the same column, so
// a name on its own does not establish that a person did something. The four
// pre-existing views were fixed by routing them through one partial; this test
// is what stops the fifth from being written by hand.
//
// It reads the templates rather than the rendered output on purpose:
// TestAuditTrailAlwaysShowsWhoAndWhatKind proves the current pages are right,
// and proves nothing about a page added next month whose fixture happens not to
// exercise the actor cell.
func TestNoTemplateRendersAnActorWithoutItsKind(t *testing.T) {
	// The one file allowed to render an actor directly: it is where the shared
	// cell is defined, and it renders the kind beside the name.
	const actorCellHome = "templates/partials/rows.html"

	files, err := fs.Glob(webassets.FS, "templates/*/*.html")
	if err != nil {
		t.Fatalf("listing templates: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no templates found; this test would pass vacuously")
	}

	for _, name := range files {
		raw, err := fs.ReadFile(webassets.FS, name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		text := string(raw)
		if name == actorCellHome {
			if !strings.Contains(text, ".ActorKind") {
				t.Errorf("%s defines actor_cell but no longer renders the kind", name)
			}
			continue
		}
		for _, forbidden := range []string{"DisplayActor", ".Actor}}", ".ActorName"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s renders %s directly; use {{template \"actor_cell\" .}} so the "+
					"kind is rendered beside the name and a monitor cannot be mistaken for a person",
					name, forbidden)
			}
		}
	}
}
