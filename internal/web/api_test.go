// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web"
	"github.com/madalinignisca/invctl/internal/web/handlers"
)

// Task 9 mounts the read-only, token-scoped inventory API built by earlier
// WP-A2 tasks. These tests cover the mounting site, not the handlers behind
// it: whether the surface exists at all, and whether it can be reached by
// the wrong kind of credential.

// TestTheAPIIsNotMountedWithoutACredential: an estate with no integrations
// must not carry the read surface, exactly as the machine-facing route is
// not mounted without a monitoring credential.
func TestTheAPIIsNotMountedWithoutACredential(t *testing.T) {
	h := newHarness(t) // no readers configured
	resp := h.get("/api/v1/assets", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d, want 404: an estate with no integrations must not carry the read surface",
			resp.StatusCode)
	}
}

// TestTheAPIRefusesABrowserSession: a read route that also accepted a
// session would let an operator's browser credentials satisfy a machine
// surface, which docs/AUDIT.md rule 6 refuses outright rather than
// resolving in either direction -- even when the request also carries a
// valid bearer token.
func TestTheAPIRefusesABrowserSession(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	h.login("viewer", "viewer-password") // establishes the session cookie on h.client's jar

	req := h.request(http.MethodGet, "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer "+readerAllToken)
	resp := h.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 for a request carrying both a session and a token", resp.StatusCode)
	}
}

// TestAnAgentTokenIsRefusedByTheAPI: a monitoring credential is a different
// principal type from a reader (docs/AUDIT.md rule 6), and must not read the
// inventory just because it can write observations.
func TestAnAgentTokenIsRefusedByTheAPI(t *testing.T) {
	h := newHarnessWithReaders(t, testAgentCredentials(), testReaderCredentials())
	req := h.request(http.MethodGet, "/api/v1/assets", nil)
	req.Header.Set("Authorization", "Bearer "+agentProdToken) // a real monitoring credential
	resp := h.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a monitoring credential must not read the inventory", resp.StatusCode)
	}
}

// TestAnAPITokenIsRefusedByObservations: the reverse of the above -- a
// read-only credential must not be able to write an observation.
func TestAnAPITokenIsRefusedByObservations(t *testing.T) {
	h := newHarnessWithReaders(t, testAgentCredentials(), testReaderCredentials())
	req := h.request(http.MethodPost, "/observations", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+readerAllToken)
	resp := h.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a read credential must not write an observation", resp.StatusCode)
	}
}

// TestAReaderCredentialReachesTheMountedAPI proves the mount itself works end
// to end for a valid credential, and that the same request answered twice is
// byte-for-byte identical -- the property apiGet exists to make cheap to
// assert.
func TestAReaderCredentialReachesTheMountedAPI(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())

	first := h.apiGet(t, "/api/v1/environments", readerAllToken)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 for a valid reader credential: %s", first.StatusCode, first.Body)
	}
	second := h.apiGet(t, "/api/v1/environments", readerAllToken)
	if second.StatusCode != first.StatusCode || second.Body != first.Body {
		t.Errorf("two reads of the same route disagree:\n  first:  %d %s\n  second: %d %s",
			first.StatusCode, first.Body, second.StatusCode, second.Body)
	}
}

// TestADevScopedReaderStillAuthenticates: a credential scoped to a single
// environment is still a valid credential at the mounting layer -- narrowing
// what it may see, checked deep in the store (an earlier task), is a
// different concern from whether it is let in at all (this one).
func TestADevScopedReaderStillAuthenticates(t *testing.T) {
	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())

	resp := h.apiGet(t, "/api/v1/environments", readerDevToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 for a validly scoped reader credential: %s", resp.StatusCode, resp.Body)
	}
}

// TestNoAPIRouteIsAWriteRoute walks the registered pattern table rather than
// trusting the diff. WP-A2 says "no write routes" and this is what makes the
// sentence true a year from now, whoever next edits routes.go.
func TestNoAPIRouteIsAWriteRoute(t *testing.T) {
	patterns := web.RegisteredAPIPatternsForTest()
	if len(patterns) == 0 {
		t.Fatal("the read surface's route table is empty; this test would pass vacuously")
	}
	for _, pattern := range patterns {
		if !strings.Contains(pattern, web.APIPrefix) {
			t.Errorf("%q does not carry %s; the route table has drifted from its own prefix",
				pattern, web.APIPrefix)
			continue
		}
		if !strings.HasPrefix(pattern, "GET ") {
			t.Errorf("%q is not a GET; the read surface has no write routes, ever", pattern)
		}
	}
}

// TestOnlyObservationsIsCSRFExempt: routes.go builds the exemption with
// middleware.ExactPath specifically so that /api/v1 cannot inherit it.
// Asserted against the exemption list itself, not the intent behind it -- and
// against a genuinely enabled agent surface, since an exemption list built
// from a disabled one would trivially be empty and prove nothing about the
// list this task is guarding.
func TestOnlyObservationsIsCSRFExempt(t *testing.T) {
	h := newHarness(t)

	registry, err := auth.NewAgentRegistry(testAgentCredentials())
	if err != nil {
		t.Fatalf("building agent registry: %v", err)
	}
	agents := &web.AgentSurface{
		Registry:      registry,
		Handler:       handlers.NewObservationAPI(store.NewObservedRecorder(h.store)),
		SessionCookie: "invctl_session",
	}

	exempt := web.CSRFExemptionsForTest(agents)
	if len(exempt) != 1 || string(exempt[0]) != web.ObservationsPath {
		t.Fatalf("csrf exemptions are %v; only %s may ever be exempt", exempt, web.ObservationsPath)
	}

	// And the empty case: no agent surface must exempt nothing.
	if exempt := web.CSRFExemptionsForTest(nil); len(exempt) != 0 {
		t.Fatalf("a nil agent surface exempted %v; want no exemptions at all", exempt)
	}
}

// ---------------------------------------------------------------------------
// Task 10: end-to-end behaviour through the real router, and the golden
// shapes the published contract is pinned to.
//
// internal/api's own suite (handlers_test.go, security_log_test.go) already
// proves these properties at the handler level, called directly against a
// *http.Request built by hand. What is missing -- and what belongs here,
// where the real router, the real RequireReader guard and the real CSRF/rate
// limit middleware sit in front of the handler -- is that the same
// properties survive the whole stack. security_log_test.go's own comment
// says as much: "a later task adds the end-to-end version through the real
// router... the property belongs to this task's code and the gap is
// demonstrated, not theoretical."
// ---------------------------------------------------------------------------

// assertHeadersEqual compares two response header maps in full, not just
// Content-Type -- Task 6 spent five review rounds establishing that an
// out-of-scope 404 and a fabricated-id 404 must be indistinguishable, and a
// header that differed between the two (a stray Content-Length from a
// differently-sized body, a debug header added to only one path) would
// reopen exactly the existence oracle the identical body closes.
//
// Date is removed from both before comparing: it is a wall-clock artifact of
// when net/http wrote each response, not a property of the response itself,
// and asserting on it would make this test flake across a one-second
// boundary despite the two responses being identical in every way that
// matters. Set-Cookie is removed too, for a session-layer reason rather than
// a §3 one: nosurf issues a fresh csrf_token cookie only on the first request
// a client's cookie jar has not already seen one for, so a caller comparing
// "the very first request this session ever made" against a later one would
// see a Set-Cookie difference that has nothing to do with scope -- the
// caller is expected to warm the jar with an unrelated request first (see
// TestAnOutOfScopeAssetIsIndistinguishableFromAnAbsentOne), and this removal
// is what keeps that warm-up from being load-bearing for every future caller
// of this helper too.
func assertHeadersEqual(t *testing.T, a, b http.Header) {
	t.Helper()
	ac, bc := a.Clone(), b.Clone()
	ac.Del("Date")
	bc.Del("Date")
	ac.Del("Set-Cookie")
	bc.Del("Set-Cookie")
	if !reflect.DeepEqual(ac, bc) {
		t.Fatalf("the two responses' headers differ:\n  a: %v\n  b: %v", ac, bc)
	}
}

// TestAnOutOfScopeAssetIsIndistinguishableFromAnAbsentOne is the end-to-end
// form of docs/api-design.md §3's "absence, not refusal" rule: an
// out-of-scope id and a fabricated one must produce the same status, the
// same body AND the same headers, through the real router.
func TestAnOutOfScopeAssetIsIndistinguishableFromAnAbsentOne(t *testing.T) {
	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())

	outOfScope := h.refs.Assets["sw-core-1"] // in {prod, dev}, so a {dev} token must miss it
	fabricated := uuid.Must(uuid.NewV7()).String()

	// Warms the client's cookie jar so neither of the two comparison requests
	// below is "the first request this session ever made" -- see
	// assertHeadersEqual's comment on why that would otherwise put a
	// Set-Cookie difference on only one side that has nothing to do with §3.
	h.apiGet(t, "/api/v1/environments", readerDevToken)

	a := h.apiGet(t, "/api/v1/assets/"+outOfScope, readerDevToken)
	b := h.apiGet(t, "/api/v1/assets/"+fabricated, readerDevToken)

	if a.StatusCode != http.StatusNotFound || b.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d and %d, want 404 and 404", a.StatusCode, b.StatusCode)
	}
	if a.Body != b.Body {
		t.Fatalf("the two 404 bodies differ (%q vs %q); a difference is an existence oracle "+
			"that lets a dev token enumerate which ids name real production assets", a.Body, b.Body)
	}
	assertHeadersEqual(t, a.Header, b.Header)
}

// TestAScopeMissIsLoggedEvenThoughItCannotBeInTheResponse: the 404 above is
// deliberately unhelpful to the client, so the operator's log is the only
// place a misconfigured INV_API_SCOPES is visible. This asserts both halves
// of that compensation -- the positive case fires, names the credential and
// never carries the token; the negative case, a fabricated id, never fires
// it, because if both looked the same in the log it would answer nothing
// about which one an operator is looking at.
//
// The sink is syncBuffer, not a plain bytes.Buffer: the server logs from its
// own request-handling goroutine, and a bare buffer races under -race.
func TestAScopeMissIsLoggedEvenThoughItCannotBeInTheResponse(t *testing.T) {
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())
	h.apiGet(t, "/api/v1/assets/"+h.refs.Assets["sw-core-1"], readerDevToken)

	logged := buf.String()
	if !strings.Contains(logged, auth.EventReaderScopeDenied) {
		t.Fatalf("a scope miss must be logged as %s; got %q", auth.EventReaderScopeDenied, logged)
	}
	if !strings.Contains(logged, readerDevID) {
		t.Fatalf("the log line must name the credential so an operator can fix the scope; got %q", logged)
	}
	if strings.Contains(logged, readerDevToken) {
		t.Fatal("the log line must never carry the token")
	}

	buf.Reset()
	fabricated := uuid.Must(uuid.NewV7()).String()
	h.apiGet(t, "/api/v1/assets/"+fabricated, readerDevToken)
	if logged := buf.String(); strings.Contains(logged, auth.EventReaderScopeDenied) {
		t.Fatalf("a fabricated id must never log a scope-denied event, which is reserved for a genuine "+
			"scope miss so an operator can tell the two apart; got %q", logged)
	}
}

// TestTheAssetShapeIsTheGoldenShape pins the published Asset DTO shape.
func TestTheAssetShapeIsTheGoldenShape(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/assets?limit=1", readerAllToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", got.StatusCode, got.Body)
	}
	assertGoldenJSON(t, "testdata/api/asset.json", got.Body)
}

// TestTheServiceShapeIsTheGoldenShape pins the published Service DTO shape.
func TestTheServiceShapeIsTheGoldenShape(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/services?limit=1", readerAllToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", got.StatusCode, got.Body)
	}
	assertGoldenJSON(t, "testdata/api/service.json", got.Body)
}

// TestTheAddressShapeIsTheGoldenShape pins the published Address DTO shape.
func TestTheAddressShapeIsTheGoldenShape(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/addresses?limit=1", readerAllToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", got.StatusCode, got.Body)
	}
	assertGoldenJSON(t, "testdata/api/address.json", got.Body)
}

// TestTheEnvironmentShapeIsTheGoldenShape pins the published Environment DTO
// shape. Unpaginated -- the collection is bounded and small -- so this is the
// only golden of the four that is not limited to one row.
func TestTheEnvironmentShapeIsTheGoldenShape(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/environments", readerAllToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", got.StatusCode, got.Body)
	}
	assertGoldenJSON(t, "testdata/api/environment.json", got.Body)
}

// TestTheAnsibleViewIsTheGoldenShape pins the published Ansible dynamic
// inventory shape.
func TestTheAnsibleViewIsTheGoldenShape(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/ansible", readerAllToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", got.StatusCode, got.Body)
	}
	assertGoldenJSON(t, "testdata/api/ansible.json", got.Body)
}

// TestEveryResponseIsNoStore: a cached inventory outlives the scope that
// produced it, so every response on the surface -- success or refusal --
// must carry Cache-Control: no-store.
func TestEveryResponseIsNoStore(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	for _, path := range []string{
		"/api/v1/assets", "/api/v1/services", "/api/v1/addresses",
		"/api/v1/environments", "/api/v1/ansible",
	} {
		got := h.apiGet(t, path, readerAllToken)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("%s: got %d, want 200: %s", path, got.StatusCode, got.Body)
		}
		if cc := got.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: got Cache-Control %q, want no-store -- a cached inventory "+
				"outlives the scope that produced it", path, cc)
		}
	}

	// And a refusal: the no-store rule is not just for a 200.
	refused := h.apiGet(t, "/api/v1/assets?kind=banana", readerAllToken)
	if refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", refused.StatusCode)
	}
	if cc := refused.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("a 400 got Cache-Control %q, want no-store", cc)
	}
}

// TestAMalformedCursorReturns400OverHTTP: ParsePageRequest's refusal reaches
// the client as a real 400, not just as a Go error in a handler test.
func TestAMalformedCursorReturns400OverHTTP(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/assets?after=not-a-cursor", readerAllToken)
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: a client paginating with a corrupted cursor must be told, "+
			"not silently restarted at page one forever", got.StatusCode)
	}
}

// TestAnUnknownKindReturns400: a filter value that is not a real asset kind
// is a 400, not an empty collection -- an empty collection there would be
// indistinguishable from a legitimate empty answer.
func TestAnUnknownKindReturns400(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/assets?kind=banana", readerAllToken)
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 -- an empty collection here is indistinguishable "+
			"from a legitimate empty answer", got.StatusCode)
	}
}

// TestAnUnknownQueryParameterNameReturns400 pins Ruling AC: an unrecognised
// parameter NAME is refused, and the refusal names the offending parameter --
// url.Values.Get silently taking the first of several values, or a typo'd
// name being silently ignored, is the same silent-fallback shape a malformed
// cursor would be.
func TestAnUnknownQueryParameterNameReturns400(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	got := h.apiGet(t, "/api/v1/assets?envelope=prod", readerAllToken)
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for an unknown query parameter name: %s", got.StatusCode, got.Body)
	}
	if !strings.Contains(got.Body, "envelope") {
		t.Fatalf("the 400 must name the offending parameter; got %q", got.Body)
	}
}

// TestAnOutOfScopeEnvFilterIs400WhetherOrNotItExists pins Controller ruling
// AD (docs/api-design.md §4), which amended WP-A2's original spec during Task
// 7. The task 10 brief this test replaces asserted the opposite behaviour --
// that ?env=prod on a {dev} token returned 200 with an empty collection.
// That rule is now wrong: it created an existence oracle over the
// environment vocabulary (an unknown code was 400, a real but out-of-scope
// one was 200-empty, so any token could enumerate every environment name by
// dictionary), and it meant a consumer whose INV_API_SCOPES had been edited
// out from under it received a plausible, well-formed, EMPTY inventory with a
// 200 and no signal at all -- exactly the silent-fallback shape §6 refuses.
//
// env is now validated against the credential's OWN scope, never against the
// estate: in scope filters and returns 200, out of scope is 400 whether or
// not the value names a real environment at all. The two 400 bodies below
// must be byte-identical -- a real, out-of-scope code producing a different
// message from a fabricated one would reopen the exact oracle this rule
// exists to close.
func TestAnOutOfScopeEnvFilterIs400WhetherOrNotItExists(t *testing.T) {
	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())

	real := h.apiGet(t, "/api/v1/assets?env=prod", readerDevToken)    // prod exists; just not in {dev}'s scope
	fake := h.apiGet(t, "/api/v1/assets?env=nowhere", readerDevToken) // does not exist at all

	if real.StatusCode != http.StatusBadRequest {
		t.Fatalf("env=prod on a {dev} token: got %d, want 400 (prod is not in this credential's "+
			"scope, even though it names a real environment): %s", real.StatusCode, real.Body)
	}
	if fake.StatusCode != http.StatusBadRequest {
		t.Fatalf("env=nowhere: got %d, want 400: %s", fake.StatusCode, fake.Body)
	}
	if real.Body != fake.Body {
		t.Fatalf("the two 400s differ (%q vs %q); a real out-of-scope env producing a different "+
			"message from a fabricated one would let a token discover which env codes are real "+
			"by dictionary", real.Body, fake.Body)
	}
}

// TestPagingTheWholeEstateTerminates walks every page of /api/v1/assets and
// checks the loop bound (the cursor advancing, per docs/api-design.md §4)
// holds through the real router, not just against APIListAssets directly.
func TestPagingTheWholeEstateTerminates(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())
	seen := map[string]bool{}
	path := "/api/v1/assets?limit=2"
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("paging did not terminate; the cursor is not advancing")
		}
		got := h.apiGet(t, path, readerAllToken)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("page %d: got %d: %s", pages, got.StatusCode, got.Body)
		}
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Next *string `json:"next"`
		}
		if err := json.Unmarshal([]byte(got.Body), &page); err != nil {
			t.Fatalf("decoding page %d: %v", pages, err)
		}
		for _, a := range page.Data {
			if seen[a.ID] {
				t.Fatalf("asset %s appeared twice while paging", a.ID)
			}
			seen[a.ID] = true
		}
		if page.Next == nil {
			break
		}
		path = "/api/v1/assets?limit=2&after=" + *page.Next
	}
	if len(seen) == 0 {
		t.Fatal("paged the whole estate and saw nothing")
	}
}

// TestAnEmptyCollectionMarshalsAsAnEmptyArrayOverHTTP: an out-of-scope kind
// filter for a token that legitimately sees nothing of that kind must still
// answer "data":[], never "data":null -- through the real router and its
// JSON encoding, not just Page[T]'s own marshalling.
func TestAnEmptyCollectionMarshalsAsAnEmptyArrayOverHTTP(t *testing.T) {
	h := newHarnessWithReaders(t, nil, devOnlyReaderCredentials())
	// A dev-only token asking for prod-only devices by kind: nothing that
	// kind is BOTH in the estate AND wholly inside {dev}'s scope, so the
	// legitimate answer is an empty page, not a refusal.
	got := h.apiGet(t, "/api/v1/assets?kind=firewall", readerDevToken)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", got.StatusCode, got.Body)
	}
	if !strings.Contains(got.Body, `"data":[]`) {
		t.Errorf("an empty collection must marshal as [] and never as null; got %q", got.Body)
	}
}

// TestCustomFieldsNeverReachTheAPI is WP-A4's guard on WP-A2's contract: an
// exact, hand-written list of json tags. A custom field must never appear in
// /api/v1 -- an integration must not depend on a field one of this estate's
// administrators can retire at will.
//
// SELF-VERIFYING, PER RULING AT. /api/v1/assets?limit=1 is
// `ORDER BY a.id LIMIT ?` (internal/store/api.go), so it always returns the
// lexicographically smallest id -- today that happens to be dc-oslo, the
// first asset seed.Load creates, and NOT any asset a hardcoded name like
// "hv-01" would point the value at. Setting the custom value on a
// hardcoded id would make the "a value reached /api/v1" half of this test
// pass regardless of whether the API actually leaked one, silently, the
// moment seeding order ever changed -- exactly the failure mode this test
// exists to catch, one level down. So the test asks the API which asset it
// actually returns FIRST, and only then attaches the value to that one.
func TestCustomFieldsNeverReachTheAPI(t *testing.T) {
	h := newHarnessWithReaders(t, nil, testReaderCredentials())

	probe := h.apiGet(t, "/api/v1/assets?limit=1", readerAllToken)
	if probe.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 discovering the probe subject: %s", probe.StatusCode, probe.Body)
	}
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(probe.Body), &page); err != nil {
		t.Fatalf("decoding the probe response: %v", err)
	}
	if len(page.Data) == 0 || page.Data[0].ID == "" {
		// A test that found no subject to target would otherwise fall
		// through with nothing left to check and still report PASS.
		t.Fatal("the probe returned no asset id, so this test has no subject to attach a value to")
	}
	subjectID := page.Data[0].ID

	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	mustSetValueViaHTTP(t, h, subjectID, id, "IT-42")

	// Signed out before the second API call: TestTheAPIRefusesABrowserSession
	// pins a separate, deliberate invariant -- a session cookie refuses the
	// API outright even carrying a valid bearer token, on top of and before
	// this guard ever gets a chance to matter. Carrying the admin's session
	// into apiGet would test that refusal by accident, not this one.
	token := h.csrfToken("/")
	logout := h.post("/logout", url.Values{"csrf_token": {token}}, false)
	logout.Body.Close()

	got := h.apiGet(t, "/api/v1/assets?limit=1", readerAllToken)
	if strings.Contains(got.Body, "cost_centre") || strings.Contains(got.Body, "IT-42") {
		t.Fatalf("a custom field reached /api/v1: %s", got.Body)
	}
	// The SAME golden the DTO shape test pins, deliberately: after defining a
	// field and giving the query's own first subject a value, the published
	// shape must come back byte-unchanged. A second golden here would let
	// this test pass by asserting against a copy nobody keeps honest.
	assertGoldenJSON(t, "testdata/api/asset.json", got.Body)
}
