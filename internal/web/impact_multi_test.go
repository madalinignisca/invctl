package web_test

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/store"
)

// Simulating the loss of several assets at once.
//
// A redundant pair only tells the truth when both halves can be taken away in
// the same run: "what happens once redundancy is exhausted" is the one question
// a pair exists to answer, and it is unaskable one asset at a time. These tests
// drive that through the real router, because the risk here is not in the
// engine -- impact.Request.DownAssetIDs has always been a set -- but in the
// handler quietly disagreeing with the page about which question was asked.

// unknownAssetID is a well-formed id that resolves to nothing.
const unknownAssetID = "00000000-0000-0000-0000-000000000000"

// outageSet returns the fragment of the page that names what is being
// simulated.
//
// The assertion has to be scoped: the picker further down the same page lists
// every asset in the estate, so a bare strings.Contains for an asset name is
// satisfied by an asset that is not in the outage set at all -- which is
// exactly the confusion these tests exist to rule out.
func outageSet(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, `id="outage-set"`)
	if start < 0 {
		t.Fatal("the page does not name the outage set at all")
	}
	end := strings.Index(page[start:], "</div>")
	if end < 0 {
		t.Fatal("the outage set fragment is unterminated")
	}
	return page[start : start+end]
}

// windowForm returns the outage-length toolbar, which has to carry the rest of
// the set with it.
func windowForm(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, `id="impact-window"`)
	if start < 0 {
		t.Fatal("the page has no outage-length form")
	}
	end := strings.Index(page[start:], "</form>")
	if end < 0 {
		t.Fatal("the outage-length form is unterminated")
	}
	return page[start : start+end]
}

var hrefPattern = regexp.MustCompile(`href="([^"]+)"`)

// hrefsIn returns every link target in a fragment, unescaped the way a browser
// would read it -- html/template writes & as &amp; inside an attribute.
func hrefsIn(fragment string) []string {
	var out []string
	for _, m := range hrefPattern.FindAllStringSubmatch(fragment, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

func countChips(fragment string) int {
	return strings.Count(fragment, `class="check"`)
}

// TestImpactOutageSet covers how the repeated ?asset= parameter is read: what
// widens the set, what collapses into it, and what must refuse to render.
func TestImpactOutageSet(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	hv01 := h.refs.Assets["hv-01"]
	hv03 := h.refs.Assets["hv-03"]

	cases := []struct {
		name       string
		query      string
		wantStatus int
		// wantNamed is every asset the outage-set panel must name, and its
		// length is also the number of members the panel may show -- an extra
		// chip is as wrong as a missing one.
		wantNamed   []string
		wantAbsent  []string
		wantRemoves int
	}{
		{
			name:        "one asset behaves as it always did",
			query:       "?window=180",
			wantStatus:  http.StatusOK,
			wantNamed:   []string{"hv-01"},
			wantAbsent:  []string{"hv-03"},
			wantRemoves: 0, // nothing to remove: a simulation of nothing answers nothing
		},
		{
			name:        "a second asset widens the set",
			query:       "?window=180&asset=" + hv03,
			wantStatus:  http.StatusOK,
			wantNamed:   []string{"hv-01", "hv-03"},
			wantRemoves: 2,
		},
		{
			name:        "an extra repeating the path id collapses into it",
			query:       "?window=180&asset=" + hv01,
			wantStatus:  http.StatusOK,
			wantNamed:   []string{"hv-01"},
			wantRemoves: 0,
		},
		{
			name:        "the same extra twice is one member",
			query:       "?window=180&asset=" + hv03 + "&asset=" + hv03,
			wantStatus:  http.StatusOK,
			wantNamed:   []string{"hv-01", "hv-03"},
			wantRemoves: 2,
		},
		{
			name:        "a blank extra is ignored",
			query:       "?window=180&asset=&asset=%20",
			wantStatus:  http.StatusOK,
			wantNamed:   []string{"hv-01"},
			wantRemoves: 0,
		},
		{
			// Dropping it would report a smaller outage than the one asked
			// about, under a heading naming the outage that was wanted.
			name:       "an extra that resolves to nothing is a 404",
			query:      "?window=180&asset=" + unknownAssetID,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "an unknown extra is a 404 even alongside a real one",
			query:      "?window=180&asset=" + hv03 + "&asset=" + unknownAssetID,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.get("/assets/"+hv01+"/impact"+tc.query, false)
			text := body(t, resp)

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}

			set := outageSet(t, text)
			if n := countChips(set); n != len(tc.wantNamed) {
				t.Errorf("the outage set shows %d assets, want %d", n, len(tc.wantNamed))
			}
			for _, name := range tc.wantNamed {
				if !strings.Contains(set, ">"+name+"</a>") {
					t.Errorf("the page does not name %s as being taken away", name)
				}
			}
			for _, name := range tc.wantAbsent {
				if strings.Contains(set, ">"+name+"</a>") {
					t.Errorf("the page names %s as being taken away when it is not", name)
				}
			}
			if got := strings.Count(set, ">Remove</a>"); got != tc.wantRemoves {
				t.Errorf("the set offers %d remove links, want %d", got, tc.wantRemoves)
			}
		})
	}
}

// TestImpactOversizedSetIsRefused. Every id becomes a placeholder in the
// closure and instance queries, so an unbounded repeated parameter is a cheap
// way for a signed-in reader to make the server build an enormous statement.
// The cap is checked before any of them is looked up.
func TestImpactOversizedSetIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	var extras []string
	for i := 0; i < 40; i++ {
		extras = append(extras, "asset="+store.NewID())
	}
	resp := h.get("/assets/"+h.refs.Assets["hv-01"]+"/impact?"+strings.Join(extras, "&"), false)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

// TestImpactSetStaysBehindAuthAndInFrontOfWrite.
//
// The route is a GET that mutates nothing, so it belongs to a reader: an
// anonymous request is turned away, and a read-only account can ask the widest
// question the page allows. Adding a query parameter must not quietly turn
// simulation into a privileged action, and must not quietly open it up either.
func TestImpactSetStaysBehindAuthAndInFrontOfWrite(t *testing.T) {
	h := newHarness(t)
	path := "/assets/" + h.refs.Assets["hv-01"] + "/impact?window=180&asset=" + h.refs.Assets["hv-03"]

	resp := h.get(path, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous status = %d, want 303 to the login page", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Errorf("anonymous redirected to %q, want the login page", loc)
	}

	h.login("viewer", "viewer-password")
	page := h.get(path, false)
	text := body(t, page)
	if page.StatusCode != http.StatusOK {
		t.Fatalf("read-only status = %d, want 200", page.StatusCode)
	}
	if n := countChips(outageSet(t, text)); n != 2 {
		t.Errorf("a read-only user got a %d-asset simulation, want 2", n)
	}
}

// TestImpactSeveralAssetsExhaustRedundancy is the reason the feature exists.
//
// Vault is a three-instance quorum spread across the three hypervisors. Losing
// one leaves two of three and the service is fine -- which is what the
// single-asset page has always said. Losing two leaves one, quorum fails, and
// there is no way to see that at all unless both can be taken away in one run.
func TestImpactSeveralAssetsExhaustRedundancy(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	hv01 := h.refs.Assets["hv-01"]
	hv03 := h.refs.Assets["hv-03"]

	one := body(t, h.get("/assets/"+hv01+"/impact?window=180", true))
	if strings.Contains(one, "HashiCorp Vault") {
		t.Error("vault is reported as affected by one hypervisor despite surviving quorum")
	}

	two := body(t, h.get("/assets/"+hv01+"/impact?window=180&asset="+hv03, true))
	if !strings.Contains(two, "HashiCorp Vault") {
		t.Error("vault survived losing two of its three hypervisors; the second asset was not simulated")
	}
}

// TestImpactDuplicateIsTheSameSimulation. The collapse has to reach the engine,
// not just the heading: an id repeated in the URL must produce the same answer,
// to the byte, as the URL without it.
func TestImpactDuplicateIsTheSameSimulation(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	hv01 := h.refs.Assets["hv-01"]

	// The HTMX partial is the whole answer with none of the per-request chrome
	// -- the layout carries a freshly masked CSRF token, so full pages are
	// never byte-comparable.
	plain := body(t, h.get("/assets/"+hv01+"/impact?window=180", true))
	repeated := body(t, h.get("/assets/"+hv01+"/impact?window=180&asset="+hv01, true))

	if plain != repeated {
		t.Error("repeating the path id in ?asset= changed the answer; the set did not collapse")
	}
}

// TestImpactWindowKeepsTheRestOfTheSet. The outage length and the outage set
// are two halves of one question. Changing either must not silently reset the
// other, and the window toolbar swaps only the result panel -- so if it does
// not carry the rest of the set, the answer narrows to one asset while the
// heading above it still claims two.
func TestImpactWindowKeepsTheRestOfTheSet(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	hv01 := h.refs.Assets["hv-01"]
	hv03 := h.refs.Assets["hv-03"]

	page := body(t, h.get("/assets/"+hv01+"/impact?window=180&asset="+hv03, false))
	form := windowForm(t, page)
	if !strings.Contains(form, `name="asset" value="`+hv03+`"`) {
		t.Error("the outage-length form does not carry the rest of the set, so changing it would narrow the question")
	}

	// And the request that form issues still answers the wider question.
	longer := body(t, h.get("/assets/"+hv01+"/impact?window=2700&asset="+hv03, true))
	if !strings.Contains(longer, "HashiCorp Vault") {
		t.Error("changing the window dropped the second asset from the simulation")
	}
}

// TestImpactRemoveTakesOneAssetBackOut. The remove link is a plain navigation
// rather than a partial swap, because taking a member out changes the question
// that the heading, the breadcrumb and both toolbars all restate.
func TestImpactRemoveTakesOneAssetBackOut(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	hv01 := h.refs.Assets["hv-01"]
	hv02 := h.refs.Assets["hv-02"]
	hv03 := h.refs.Assets["hv-03"]

	page := body(t, h.get("/assets/"+hv01+"/impact?window=2700&asset="+hv02+"&asset="+hv03, false))
	set := outageSet(t, page)
	if n := countChips(set); n != 3 {
		t.Fatalf("the outage set shows %d assets, want 3", n)
	}

	// Removing the asset in the URL path has to work too, or the first thing
	// chosen would be the one thing that could never be taken back out.
	var removals []string
	for _, href := range hrefsIn(set) {
		if strings.Contains(href, "/impact?") {
			removals = append(removals, href)
		}
	}
	if len(removals) != 3 {
		t.Fatalf("found %d remove links, want one per member", len(removals))
	}

	// Each remove link must take out the member it sits beside. Counting what
	// survives is not enough: an off-by-one in the index would leave the count
	// at 2 every time and remove the wrong asset, which is the failure mode
	// that matters -- an operator believes they have narrowed the outage and
	// gets an answer about a different one.
	members := []string{hv01, hv02, hv03}
	for i, href := range removals {
		t.Run(fmt.Sprintf("member %d", i), func(t *testing.T) {
			resp := h.get(href, false)
			text := body(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("following the remove link returned %d", resp.StatusCode)
			}
			smaller := outageSet(t, text)
			if n := countChips(smaller); n != 2 {
				t.Fatalf("after removing one member the set shows %d assets, want 2", n)
			}
			removed := members[i]
			if strings.Contains(smaller, removed) {
				t.Errorf("remove link %d still leaves %s in the set; it removed something else",
					i, removed)
			}
			for j, other := range members {
				if j == i {
					continue
				}
				if !strings.Contains(smaller, other) {
					t.Errorf("remove link %d took out %s as well as (or instead of) %s",
						i, other, removed)
				}
			}
			// The window is half the question; removing an asset must not
			// quietly reset it.
			if !strings.Contains(text, `value="2700" selected`) {
				t.Error("removing an asset reset the outage window")
			}
		})
	}
}

// TestImpactPageRendersReachabilityFindings guards the demo outcome M5 exists
// to produce, at the layer an operator actually meets it.
//
// Every reachability assertion in the suite lives in internal/impact, against
// impact.Result. Forcing Inputs.Net to nil -- turning the entire feature off --
// left this whole package green, because nothing here reads the rendered page
// for a network finding. The engine can be perfect and the panel can render
// empty, and only a person opening a browser would ever know.
//
// sw-core-2 is the scenario with something to say: hv-03 is single-homed to it,
// so losing it cuts off the hypervisor and the five guests that inherit its
// attachment through asset_closure.
func TestImpactPageRendersReachabilityFindings(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/assets/"+h.refs.Assets["sw-core-2"]+"/impact?window=180", false))

	if !strings.Contains(page, "Network reachability") {
		t.Fatal("the impact page has no reachability panel at all")
	}
	if strings.Contains(page, "No network topology declared") {
		t.Fatal("the page reports no declared topology, but the seed declares it -- " +
			"the demo is back to showing a coverage banner instead of an answer")
	}

	// Named, not merely counted. The panel used to print raw UUIDv7 ids, which
	// an operator cannot act on.
	for _, name := range []string{"hv-03", "vm-sso-1", "vm-k8s-1"} {
		if !strings.Contains(page, name) {
			t.Errorf("the isolation panel does not name %s; it either did not render or "+
				"rendered opaque ids", name)
		}
	}
	if !strings.Contains(page, "sw-core") {
		t.Error("nothing names the group that blocked the path, so the finding is not actionable")
	}

	// The service consequence, and the wording that tells an operator this is a
	// live machine behind a broken path rather than a powered-off one.
	if !strings.Contains(page, "running, but network-isolated") {
		t.Error("no service explains itself as network-isolated, so an isolated-but-running " +
			"host is indistinguishable from a dead one")
	}

	// An asset inside the outage is off, not isolated. sw-core-2 itself must
	// never appear as a victim of its own loss.
	iso := page
	if i := strings.Index(iso, "Network reachability"); i >= 0 {
		iso = iso[i:]
	}
	if strings.Contains(iso, "sw-core-2") {
		t.Error("sw-core-2 is reported as isolated by its own outage; an asset that is down " +
			"is off, not cut off, and charging it twice is the double-count the liveness " +
			"rule exists to prevent")
	}
}

// TestBothEdgeFirewallsIsNotAnAllClear is the multi-asset question the picker
// exists to ask, and the one that used to answer "Nothing breaks."
//
// Losing both halves of the edge pair is a strictly larger outage than losing
// one, so it must never produce a strictly quieter page. It previously did:
// the exposure channel was dead and the redundancy note was suppressed the
// moment the group went fully down, so the total loss of the estate's only
// route out reported an unqualified all-clear.
func TestBothEdgeFirewallsIsNotAnAllClear(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	fw1, fw2 := h.refs.Assets["fw-edge-1"], h.refs.Assets["fw-edge-2"]
	one := body(t, h.get("/assets/"+fw1+"/impact?window=180", false))
	both := body(t, h.get("/assets/"+fw1+"/impact?window=180&asset="+fw2, false))

	if strings.Contains(both, "Nothing breaks") {
		t.Error("losing BOTH edge firewalls prints \"Nothing breaks\" -- a strictly larger " +
			"outage must not produce a strictly emptier report, and this is the exact " +
			"question the multi-asset picker invites an operator to ask")
	}
	if !strings.Contains(one, "no redundancy remains") {
		t.Error("losing one half of a manual-failover pair does not mention the lost redundancy")
	}
	if !strings.Contains(both, "the whole group is down") {
		t.Error("losing both halves does not say the group is down; the finding disappears " +
			"exactly when the group's condition is worst")
	}
	for _, code := range []string{"haproxy-edge", "partner-gateway"} {
		if !strings.Contains(both, code) {
			t.Errorf("%s is not reported when the estate loses its entire edge pair, "+
				"though its only path out runs through it", code)
		}
	}
}
