package web_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web"
	"github.com/gabriel/invctl/internal/web/handlers"
	"github.com/gabriel/invctl/internal/web/middleware"
)

// Adversarial tests for the one route a machine credential can reach
// (docs/AUDIT.md rule 6). These are written from the attacker's side: each one
// asks what a stolen, misconfigured or over-scoped token can do, and asserts
// that the answer is "nothing, and it was recorded".
//
// They drive the real router over real HTTP against a real database, because
// every property here is a property of the whole stack. A handler tested in
// isolation would demonstrate none of them -- the CSRF exemption, the session
// refusal and the rate limit are all middleware, and the 403 is in the store.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// agentClient is a client with no cookie jar: a collector is not a browser, and
// a test that shared the UI's jar would silently be testing a session.
func agentClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// observe posts a batch to the machine-facing route as one credential.
func (h *harness) observe(token, body string) *http.Response {
	h.t.Helper()
	return h.agentRequest(http.MethodPost, web.ObservationsPath, token, "application/json", body, agentClient())
}

func (h *harness) agentRequest(method, path, token, contentType, body string, client *http.Client) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.url(path), strings.NewReader(body))
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// oneObservation builds a single-entry batch.
func oneObservation(entityType, entityID, state string) string {
	return batchOf(fmt.Sprintf(
		`{"entity_type":%q,"entity_id":%q,"state":%q,"reported_at":"2026-07-28T09:00:00Z","interval_seconds":30}`,
		entityType, entityID, state))
}

func batchOf(items ...string) string {
	return `{"observations":[` + strings.Join(items, ",") + `]}`
}

// agentBody reads a JSON response body into a map, failing loudly if it is not
// JSON -- a machine-facing route that answers with an HTML error page is itself
// a defect.
func (h *harness) agentBody(resp *http.Response) map[string]any {
	h.t.Helper()
	raw := body(h.t, resp)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		h.t.Fatalf("response is not JSON (%v): %s", err, raw)
	}
	return out
}

// count runs a scalar query against the reader pool.
func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	reader := h.store.DB().Reader
	if err := reader.Get(&n, reader.Rebind(query), args...); err != nil {
		h.t.Fatalf("counting (%s): %v", query, err)
	}
	return n
}

func (h *harness) asset(name string) string {
	h.t.Helper()
	id, ok := h.refs.Assets[name]
	if !ok {
		h.t.Fatalf("no seeded asset named %s", name)
	}
	return id
}

// ---------------------------------------------------------------------------
// The happy path, so the refusals below mean something
// ---------------------------------------------------------------------------

// TestObservationIsRecordedAgainstItsCredential. Attribution is server-derived
// (rule 5): the reporter on the stored reading is the credential that
// authenticated, and the audit trail is untouched (rule 3) because a heartbeat
// is not a decision.
func TestObservationIsRecordedAgainstItsCredential(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")

	before := h.count(`SELECT COUNT(*) FROM change_log`)
	resp := h.observe(agentProdToken, oneObservation("asset", assetID, "down"))
	payload := h.agentBody(resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, payload)
	}
	if applied, _ := payload["applied"].(float64); applied != 1 {
		t.Errorf("applied = %v, want 1", payload["applied"])
	}

	if got := h.count(
		`SELECT COUNT(*) FROM asset_health WHERE entity_id = ? AND reporter = ? AND state = ?`,
		assetID, agentProdID, "down"); got != 1 {
		t.Errorf("%d readings for %s from %s, want 1", got, assetID, agentProdID)
	}
	// The first observation is a transition and is logged as one -- it is the
	// entry an incident reviewer looks for.
	if got := h.count(`SELECT COUNT(*) FROM observed_transition WHERE entity_id = ?`, assetID); got != 1 {
		t.Errorf("%d transition rows, want 1", got)
	}
	// Rule 3: never into change_log. Not one row, not even the create.
	if after := h.count(`SELECT COUNT(*) FROM change_log`); after != before {
		t.Errorf("change_log grew from %d to %d: an observation reached the audit trail", before, after)
	}
	if got := h.count(`SELECT COUNT(*) FROM change_log WHERE actor_kind = ?`, "agent"); got != 0 {
		t.Errorf("%d change_log rows written by an agent actor, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Principal confusion
// ---------------------------------------------------------------------------

// TestObservationRejectsARequestCarryingASession. Rule 6: "The route rejects a
// request carrying a session cookie." Confusion between principal types is the
// vulnerability -- this route is CSRF-exempt, so a version of it that also
// honoured a session would be a CSRF-exempt authenticated write.
func TestObservationRejectsARequestCarryingASession(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.asset("vm-app-1")

	// h.client carries the jar, so this request has both a valid session and a
	// valid bearer token. Neither rescues the other.
	resp := h.agentRequest(http.MethodPost, web.ObservationsPath, agentProdToken,
		"application/json", oneObservation("asset", assetID, "up"), h.client)
	payload := h.agentBody(resp)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %v", resp.StatusCode, payload)
	}
	if msg, _ := payload["error"].(string); !strings.Contains(msg, "browser session") {
		t.Errorf("error = %q, want it to name the session as the problem", msg)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset_health`); got != 0 {
		t.Errorf("%d readings written for a session-bearing request, want 0", got)
	}
}

// TestObservationRejectsAnExpiredSessionCookieToo. A session that no longer
// resolves populates no user, so a check that only looked at the context would
// wave this through -- and a browser is a browser whether or not its cookie
// still works.
func TestObservationRejectsAnExpiredSessionCookieToo(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")

	req, err := http.NewRequest(http.MethodPost, h.url(web.ObservationsPath),
		strings.NewReader(oneObservation("asset", assetID, "up")))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentProdToken)
	req.AddCookie(&http.Cookie{Name: "invctl_session", Value: "long-since-expired"})

	resp, err := agentClient().Do(req)
	if err != nil {
		t.Fatalf("posting: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %v", resp.StatusCode, h.agentBody(resp))
	}
	resp.Body.Close()
	if got := h.count(`SELECT COUNT(*) FROM asset_health`); got != 0 {
		t.Errorf("%d readings written, want 0", got)
	}
}

// TestObservationRejectsABadToken. One message for absent, malformed and wrong,
// for the same reason sign-in has one message for "no such user" and "wrong
// password": the difference is only useful to somebody guessing.
func TestObservationRejectsABadToken(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")
	payload := oneObservation("asset", assetID, "up")

	cases := []struct{ name, header string }{
		{"no authorization header", ""},
		{"the wrong token", "Bearer not-the-token-at-all-0000000000"},
		{"a prefix of a real token", "Bearer " + agentProdToken[:len(agentProdToken)-1]},
		{"a real token with something appended", "Bearer " + agentProdToken + "x"},
		{"the token with no scheme", agentProdToken},
		{"the wrong scheme", "Basic " + agentProdToken},
		{"an empty bearer", "Bearer "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, h.url(web.ObservationsPath), strings.NewReader(payload))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := agentClient().Do(req)
			if err != nil {
				t.Fatalf("posting: %v", err)
			}
			text := body(t, resp)

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", resp.StatusCode, text)
			}
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("no WWW-Authenticate header on a 401")
			}
			// The refusal must not confirm anything about the token it saw.
			for _, leak := range []string{agentProdToken, agentDevToken, agentProdID, agentDevID} {
				if strings.Contains(text, leak) {
					t.Errorf("the rejection body contains %q", leak)
				}
			}
		})
	}

	if got := h.count(`SELECT COUNT(*) FROM asset_health`); got != 0 {
		t.Errorf("%d readings written by unauthenticated requests, want 0", got)
	}
}

// TestCredentialCannotWriteAsAnother. Rule 5: attribution is never read from
// the request body or a header.
//
// The strongest form of that is structural: the wire carries the token alone,
// so identity is derived from the secret rather than claimed beside it and
// there is no field in which one credential can name another. What is left to
// test is that the fields somebody would reach for are refused rather than
// ignored, and that what lands is attributed to the credential that
// authenticated.
func TestCredentialCannotWriteAsAnother(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")

	// Every one of these is a field a caller might expect to be honoured. All
	// of them are 422 naming the field, not a silently dropped value -- a
	// silent drop is indistinguishable, from the caller's side, from being
	// obeyed.
	forged := []string{"reporter", "actor", "source", "confidence", "last_report_at", "state_since"}
	for _, field := range forged {
		t.Run(field, func(t *testing.T) {
			item := fmt.Sprintf(
				`{"entity_type":"asset","entity_id":%q,"state":"up","reported_at":"2026-07-28T09:00:00Z",`+
					`"interval_seconds":30,%q:%q}`, assetID, field, agentDevID)
			resp := h.observe(agentProdToken, batchOf(item))
			payload := h.agentBody(resp)

			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %v", resp.StatusCode, payload)
			}
			if got, _ := payload["field"].(string); got != field {
				t.Errorf("field = %q, want %q", got, field)
			}
		})
	}

	// And the reading that does land carries the authenticating credential.
	resp := h.observe(agentProdToken, oneObservation("asset", assetID, "up"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, h.agentBody(resp))
	}
	resp.Body.Close()
	if got := h.count(`SELECT COUNT(*) FROM asset_health WHERE reporter = ?`, agentProdID); got != 1 {
		t.Errorf("%d readings attributed to %s, want 1", got, agentProdID)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset_health WHERE reporter <> ?`, agentProdID); got != 0 {
		t.Errorf("%d readings attributed to some other reporter, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Environment scope
// ---------------------------------------------------------------------------

// TestObservationOutsideTheCredentialsScopeIsForbidden. Rule 6: "A credential
// is scoped to an explicit set of environments -- a dev collector's token must
// not be able to write health for an in_scope environment -- and an
// out-of-scope report is 403 and recorded."
//
// The fixture makes this concrete: the development zone is the one environment
// with in_scope = FALSE, and vm-dev-1 is the only asset in it.
func TestObservationOutsideTheCredentialsScopeIsForbidden(t *testing.T) {
	prodAsset := "vm-app-1"
	devAsset := "vm-dev-1"

	cases := []struct {
		name    string
		token   string
		asset   string
		wantOK  bool
		wantWhy string
	}{
		{name: "dev collector reaching into production", token: agentDevToken, asset: prodAsset},
		// The case the table originally missed, and the one that was open:
		// every asset here belonged to exactly ONE environment, so a scope
		// check that asked "does the credential cover ANY of them?" passed
		// every test while permitting the escalation the rule names. sw-core-1
		// is in {prod, dev} and prod is in_scope, so a laptop-resident dev
		// token could assert that a production core switch was up or down.
		// A reading is visible in every environment the entity is in, so the
		// credential has to cover all of them, not the most permissive one.
		{name: "dev collector reaching an asset that is also in production",
			token: agentDevToken, asset: "sw-core-1"},
		{name: "production collector reaching that same shared asset",
			token: agentProdToken, asset: "sw-core-1"},
		{name: "production collector reaching into development", token: agentProdToken, asset: devAsset},
		{name: "dev collector inside its own zone", token: agentDevToken, asset: devAsset, wantOK: true},
		{name: "production collector inside its own zone", token: agentProdToken, asset: prodAsset, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			assetID := h.asset(tc.asset)

			resp := h.observe(tc.token, oneObservation("asset", assetID, "down"))
			payload := h.agentBody(resp)

			if tc.wantOK {
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want 200: %v", resp.StatusCode, payload)
				}
				if got := h.count(`SELECT COUNT(*) FROM asset_health WHERE entity_id = ?`, assetID); got != 1 {
					t.Errorf("%d readings, want 1", got)
				}
				return
			}

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %v", resp.StatusCode, payload)
			}
			if msg, _ := payload["error"].(string); !strings.Contains(msg, "scoped") {
				t.Errorf("error = %q, want it to name the scope", msg)
			}
			// Refused means nothing was written -- not the reading, not a
			// transition, and not a drift entry either: the entity exists, so
			// this is an authorization finding rather than inventory drift.
			for _, table := range []string{"asset_health", "observed_transition", "unmatched_observation"} {
				if got := h.count(`SELECT COUNT(*) FROM ` + table); got != 0 {
					t.Errorf("%d rows in %s after a refused report, want 0", got, table)
				}
			}
		})
	}
}

// TestScopeFollowsAWorkloadToItsHost. A service_instance has no environment of
// its own; it inherits the environments of the asset it is placed on, because
// the placement is the physical fact. Without that, every workload would be
// outside every scope and rule 6 would be unimplementable for the entity type
// that matters most during an incident.
func TestScopeFollowsAWorkloadToItsHost(t *testing.T) {
	h := newHarness(t)

	var instanceID string
	reader := h.store.DB().Reader
	if err := reader.Get(&instanceID, reader.Rebind(
		`SELECT si.id FROM service_instance si WHERE si.host_asset_id = ?`), h.asset("vm-app-1")); err != nil {
		t.Fatalf("finding a seeded placement: %v", err)
	}

	resp := h.observe(agentDevToken, oneObservation("service_instance", instanceID, "down"))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dev token on a production workload: status = %d, want 403: %v",
			resp.StatusCode, h.agentBody(resp))
	}
	resp.Body.Close()

	resp = h.observe(agentProdToken, oneObservation("service_instance", instanceID, "down"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("production token on a production workload: status = %d, want 200: %v",
			resp.StatusCode, h.agentBody(resp))
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Unknown entities
// ---------------------------------------------------------------------------

// TestObservationForAnUnknownEntityIs404AndCreatesNothing. Rule 6: "An
// observation for an unknown entity is 404, never created: INSERT ... ON
// CONFLICT is the natural verb and it turns a deliberately narrow token into an
// inventory-write vector." It is queued as drift rather than dropped, because
// an asset the estate has and the inventory does not is a finding.
func TestObservationForAnUnknownEntityIs404AndCreatesNothing(t *testing.T) {
	h := newHarness(t)
	ghost := store.NewID()

	assetsBefore := h.count(`SELECT COUNT(*) FROM asset`)
	instancesBefore := h.count(`SELECT COUNT(*) FROM service_instance`)

	resp := h.observe(agentProdToken, oneObservation("asset", ghost, "down"))
	payload := h.agentBody(resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %v", resp.StatusCode, payload)
	}
	if h.count(`SELECT COUNT(*) FROM asset`) != assetsBefore {
		t.Error("an observation created an asset")
	}
	if h.count(`SELECT COUNT(*) FROM service_instance`) != instancesBefore {
		t.Error("an observation created a service instance")
	}
	if got := h.count(`SELECT COUNT(*) FROM asset_health WHERE entity_id = ?`, ghost); got != 0 {
		t.Errorf("%d readings for an entity that does not exist, want 0", got)
	}
	// Not dropped: it is drift, and drift is the finding.
	if got := h.count(`SELECT COUNT(*) FROM unmatched_observation WHERE entity_ref = ?`, ghost); got != 1 {
		t.Errorf("%d rows in the drift queue, want 1", got)
	}

	// Repeats count rather than accumulate, or a misconfigured collector floods
	// the queue nobody then reads.
	resp = h.observe(agentProdToken, oneObservation("asset", ghost, "down"))
	resp.Body.Close()
	if got := h.count(`SELECT COUNT(*) FROM unmatched_observation`); got != 1 {
		t.Errorf("%d drift rows after two identical unmatched reports, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Size caps
// ---------------------------------------------------------------------------

// TestObservationRejectsAnOversizedBodyOrBatch. Rule 6 asks for both caps, and
// neither implies the other: the body cap alone still admits several hundred
// observations, and the batch cap alone still admits a decoder fed megabytes.
func TestObservationRejectsAnOversizedBodyOrBatch(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")

	t.Run("body", func(t *testing.T) {
		// Well-formed JSON, simply far too much of it: the cap has to bite on
		// bytes read rather than on anything about the shape.
		padding := strings.Repeat("a", handlers.MaxObservationBody+1024)
		payload := batchOf(fmt.Sprintf(
			`{"entity_type":"asset","entity_id":%q,"state":"up","reported_at":"2026-07-28T09:00:00Z",`+
				`"interval_seconds":30,"observation_id":%q}`, assetID, padding))

		resp := h.observe(agentProdToken, payload)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413: %v", resp.StatusCode, h.agentBody(resp))
		}
		resp.Body.Close()
	})

	t.Run("batch", func(t *testing.T) {
		item := fmt.Sprintf(
			`{"entity_type":"asset","entity_id":%q,"state":"up","reported_at":"2026-07-28T09:00:00Z","interval_seconds":30}`,
			assetID)
		items := make([]string, handlers.MaxObservationBatch+1)
		for i := range items {
			items[i] = item
		}
		resp := h.observe(agentProdToken, batchOf(items...))
		payload := h.agentBody(resp)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413: %v", resp.StatusCode, payload)
		}
	})

	if got := h.count(`SELECT COUNT(*) FROM asset_health`); got != 0 {
		t.Errorf("%d readings written by an oversized request, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Vocabulary (rule 13)
// ---------------------------------------------------------------------------

// TestUnmappedStateIs422WithTheValueEchoed. Rule 13: "Vendor vocabularies
// (firing, NotReady, 2) are mapped at the adapter per reporter; an unmapped
// value is 422 with the offending value echoed, never coerced, never stored
// raw."
//
// The mapping is per reporter, and the fixture proves that rather than assuming
// it: `firing` is a state for the Alertmanager credential and nonsense for the
// other two, and `up` is the reverse.
func TestUnmappedStateIs422WithTheValueEchoed(t *testing.T) {
	cases := []struct {
		name      string
		token     string
		state     string
		wantOK    bool
		wantState string
	}{
		{name: "alertmanager firing means down", token: agentPromToken, state: "firing", wantOK: true, wantState: "down"},
		{name: "alertmanager pending means degraded", token: agentPromToken, state: "pending", wantOK: true, wantState: "degraded"},
		{name: "canonical up from a canonical reporter", token: agentProdToken, state: "up", wantOK: true, wantState: "up"},
		{name: "alertmanager does not speak up", token: agentPromToken, state: "up"},
		{name: "a canonical reporter does not speak firing", token: agentProdToken, state: "firing"},
		{name: "outright nonsense", token: agentProdToken, state: "kaputt"},
		{name: "an empty state", token: agentProdToken, state: ""},
		{name: "a near miss on case", token: agentProdToken, state: "UP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			assetID := h.asset("vm-app-1")

			resp := h.observe(tc.token, oneObservation("asset", assetID, tc.state))
			payload := h.agentBody(resp)

			if tc.wantOK {
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want 200: %v", resp.StatusCode, payload)
				}
				if got := h.count(
					`SELECT COUNT(*) FROM asset_health WHERE entity_id = ? AND state = ?`,
					assetID, tc.wantState); got != 1 {
					t.Errorf("no reading stored as %q -- the vocabulary did not map %q", tc.wantState, tc.state)
				}
				return
			}

			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %v", resp.StatusCode, payload)
			}
			if got, _ := payload["value"].(string); got != tc.state {
				t.Errorf("value = %q, want the offending value %q echoed back", got, tc.state)
			}
			if got, _ := payload["field"].(string); got != "state" {
				t.Errorf("field = %q, want state", got)
			}
			// Never coerced and never stored raw: not as the vendor's word, not
			// as unknown, not at all.
			if got := h.count(`SELECT COUNT(*) FROM asset_health`); got != 0 {
				t.Errorf("%d readings stored for an unmapped state, want 0", got)
			}
			if got := h.count(`SELECT COUNT(*) FROM observed_transition`); got != 0 {
				t.Errorf("%d transitions stored for an unmapped state, want 0", got)
			}
		})
	}
}

// TestAnUnmappedStateIsEchoedButTruncated. The echoed value is
// attacker-controlled and lands in the response and in the caller's logs; the
// rejection must not become the amplification.
func TestAnUnmappedStateIsEchoedButTruncated(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")

	huge := strings.Repeat("z", 4096)
	resp := h.observe(agentProdToken, oneObservation("asset", assetID, huge))
	payload := h.agentBody(resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %v", resp.StatusCode, payload)
	}
	echoed, _ := payload["value"].(string)
	if len(echoed) >= len(huge) {
		t.Errorf("the offending value was echoed back in full (%d bytes)", len(echoed))
	}
	if !strings.HasPrefix(echoed, "zzz") {
		t.Errorf("value = %q, want the beginning of the offending value", echoed)
	}
}

// ---------------------------------------------------------------------------
// CSRF exemption
// ---------------------------------------------------------------------------

// TestCSRFExemptionCoversOnlyTheObservationPath. Rule 6: the exemption is
// registered "for that exact path -- never a prefix or glob, or the planned
// /api/inventory inherits it for free".
//
// A CSRF rejection is a 400 from the middleware, before the mux ever sees the
// request. So an unexempted path answers 400 and the exempt one answers
// something from further in -- which is the whole distinction being asserted.
func TestCSRFExemptionCoversOnlyTheObservationPath(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")
	payload := oneObservation("asset", assetID, "up")

	// The exact path: exempt, so the request reaches RequireAgent and is
	// answered on its merits.
	resp := h.observe(agentProdToken, payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the exact path returned %d, want 200: %v", resp.StatusCode, h.agentBody(resp))
	}
	resp.Body.Close()

	// Every neighbour of it, and the path rule 6 names as the one that must not
	// inherit the exemption.
	siblings := []string{
		"/api/inventory",
		web.ObservationsPath + "/",
		web.ObservationsPath + "/extra",
		web.ObservationsPath + "x",
		"/observation",
		"/",
	}
	for _, path := range siblings {
		t.Run(path, func(t *testing.T) {
			resp := h.agentRequest(http.MethodPost, path, agentProdToken, "application/json", payload, agentClient())
			text := body(t, resp)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("POST %s returned %d, want 400 from CSRF -- it inherited the exemption: %s",
					path, resp.StatusCode, text)
			}
			if !strings.Contains(text, "session expired") {
				t.Errorf("POST %s was refused by something other than CSRF: %s", path, text)
			}
		})
	}
}

// TestCSRFExemptionIsRegisteredByExactPathOnly reads the middleware itself.
//
// The routing test above proves today's behaviour; this one protects the
// mechanism. nosurf offers ExemptGlob, ExemptRegexp, ExemptFunc and the
// variadic ExemptPaths, and four of those five would hand /api/inventory the
// exemption the moment somebody reached for the convenient one. A test that
// only checked sibling paths would pass on the day of the change and fail
// months later, on whichever sibling nobody thought to enumerate.
func TestCSRFExemptionIsRegisteredByExactPathOnly(t *testing.T) {
	const file = "middleware/middleware.go"
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0) // no comments: they name what they forbid
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	forbidden := map[string]bool{
		"ExemptGlob": true, "ExemptGlobs": true,
		"ExemptRegexp": true, "ExemptRegexps": true,
		"ExemptFunc": true, "ExemptPaths": true,
	}
	sawExemptPath := false
	ast.Inspect(parsed, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch {
		case forbidden[sel.Sel.Name]:
			t.Errorf("%s calls %s: the CSRF exemption must be an exact path, "+
				"or the planned /api/inventory inherits it for free",
				file, sel.Sel.Name)
		case sel.Sel.Name == "ExemptPath":
			sawExemptPath = true
		}
		return true
	})
	if !sawExemptPath {
		t.Errorf("%s never calls ExemptPath: this test is no longer watching anything", file)
	}
}

// ---------------------------------------------------------------------------
// What a credential cannot reach
// ---------------------------------------------------------------------------

// TestAnAgentTokenReachesNothingElse. Rule 6: "No handler reachable by an agent
// principal returns an identity row or anything derived from secret_ref."
//
// The strongest statement of that is that there is nothing else to reach. A
// bearer token is not a session, so every authenticated route answers as it
// would for an anonymous caller, and the seeded Vault paths appear nowhere.
func TestAnAgentTokenReachesNothingElse(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")
	client := agentClient()

	reads := []string{
		"/", "/assets", "/services", "/environments", "/changes", "/network",
		"/prefixes", "/reports/spanning", "/search?q=vault", "/search?q=svc-sso",
		"/assets/" + assetID, "/assets/" + assetID + "/impact",
	}
	for _, path := range reads {
		resp := h.agentRequest(http.MethodGet, path, agentProdToken, "", "", client)
		text := body(t, resp)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s returned 200 to a monitoring credential", path)
		}
		assertNoSecretPaths(t, path, text)
	}

	writes := []string{
		"/assets/" + assetID + "/retire",
		"/environments",
		"/network/derive",
	}
	for _, path := range writes {
		resp := h.agentRequest(http.MethodPost, path, agentProdToken,
			"application/x-www-form-urlencoded", url.Values{"code": {"x"}}.Encode(), client)
		text := body(t, resp)
		// CSRF refuses it first, which is exactly right: the exemption is one
		// exact path and this is not it.
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
			t.Errorf("POST %s returned %d to a monitoring credential", path, resp.StatusCode)
		}
		assertNoSecretPaths(t, path, text)
	}

	// And the one route it can reach returns nothing derived from an identity.
	resp := h.observe(agentProdToken, oneObservation("asset", assetID, "up"))
	text := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, text)
	}
	assertNoSecretPaths(t, web.ObservationsPath, text)

	// The estate is not retired, disabled or otherwise changed by any of the
	// above: this system presents state, it never acts on it.
	if got := h.count(`SELECT COUNT(*) FROM asset WHERE lifecycle <> ?`, "active"); got != 0 {
		t.Errorf("%d assets are no longer active after a credential poked at every route", got)
	}
}

// assertNoSecretPaths checks a response for anything derived from
// identity.secret_ref. The seeded values are Vault-style kv/ paths.
func assertNoSecretPaths(t *testing.T, path, text string) {
	t.Helper()
	for _, marker := range []string{"secret_ref", "kv/prod/", "kv/dev/", "svc-sso"} {
		if strings.Contains(text, marker) {
			t.Errorf("the response to %s contains %q", path, marker)
		}
	}
}

// TestObservationRouteRefusesOtherMethods. One route means one method: a GET
// that returned readings would be a second, unreviewed, machine-facing surface.
func TestObservationRouteRefusesOtherMethods(t *testing.T) {
	h := newHarness(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		resp := h.agentRequest(method, web.ObservationsPath, agentProdToken, "application/json", "", agentClient())
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s %s returned 200", method, web.ObservationsPath)
		}
	}
}

// TestObservationRouteRequiresJSON. A form post would mean the endpoint could
// be reached from a browser form, and a browser form is the shape CSRF protects
// against -- on the one route where CSRF is switched off.
func TestObservationRouteRequiresJSON(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")

	resp := h.agentRequest(http.MethodPost, web.ObservationsPath, agentProdToken,
		"application/x-www-form-urlencoded", oneObservation("asset", assetID, "up"), agentClient())
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// TestCredentialsAreRateLimitedIndependently. Rule 6 asks for a per-credential
// limit, and "per credential" is the part worth testing: a shared bucket would
// let one noisy or hostile collector deny every other one.
func TestCredentialsAreRateLimitedIndependently(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")
	payload := oneObservation("asset", assetID, "up")

	attempts := 2 * middleware.AgentBurst
	throttled := false
	for i := 0; i < attempts; i++ {
		resp := h.observe(agentProdToken, payload)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Fatalf("a credential was never throttled in %d requests", attempts)
	}

	// A different credential is untouched by the first one's exhaustion.
	resp := h.observe(agentPromToken, oneObservation("asset", assetID, "inactive"))
	status := resp.StatusCode
	resp.Body.Close()
	if status == http.StatusTooManyRequests {
		t.Error("one credential's rate limit throttled another: the buckets are shared")
	}
}

// TestFailedAuthenticationIsThrottledWithoutLockingOutACredential. Brute force
// has to cost something, and the cost must not be borne by the collectors that
// have the right token -- which is why failures share a bucket that valid
// credentials never touch.
func TestFailedAuthenticationIsThrottledWithoutLockingOutACredential(t *testing.T) {
	h := newHarness(t)
	assetID := h.asset("vm-app-1")
	payload := oneObservation("asset", assetID, "up")

	throttled := false
	for i := 0; i < 4*middleware.UnauthenticatedBurst; i++ {
		resp := h.observe(fmt.Sprintf("guess-%030d", i), payload)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Fatal("guessing was never throttled")
	}

	resp := h.observe(agentProdToken, payload)
	status := resp.StatusCode
	resp.Body.Close()
	if status != http.StatusOK {
		t.Errorf("a valid credential got %d after somebody else guessed: guessing can lock out a collector", status)
	}
}

// ---------------------------------------------------------------------------
// The route only exists when credentials do
// ---------------------------------------------------------------------------

// TestNoCredentialsMeansNoRoute. An estate that is not reporting yet should not
// be carrying the surface of one that is -- and with no route there is no CSRF
// exemption either, so the path falls back to being protected like every other.
func TestNoCredentialsMeansNoRoute(t *testing.T) {
	h := newHarnessWithoutAgents(t)

	resp := h.agentRequest(http.MethodPost, web.ObservationsPath, agentProdToken,
		"application/json", oneObservation("asset", h.asset("vm-app-1"), "up"), agentClient())
	text := body(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 from CSRF: with no credentials the path is not exempt: %s",
			resp.StatusCode, text)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset_health`); got != 0 {
		t.Errorf("%d readings written against a router with no agent route", got)
	}
}
