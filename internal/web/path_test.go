package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// The path page, driven through the real router against the seeded estate.
// The store's own suite proves the walk; what belongs here is the stack: URL
// in, picture and words out, and the entry points a person actually uses.

func TestPathPageRendersItsPickersEmpty(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/paths", false))
	if !strings.Contains(page, "How does this reach that?") {
		t.Fatal("the page did not render")
	}
	for _, want := range []string{`name="from"`, `name="to"`, "Nothing asked yet"} {
		if !strings.Contains(page, want) {
			t.Errorf("an empty question is missing %q; the page should offer the pickers "+
				"and explain itself, not error", want)
		}
	}
	if strings.Contains(page, "<svg viewBox=") {
		t.Error("a diagram was drawn for no question at all")
	}
}

func TestPathBetweenTwoServicesDrawsTheChain(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	from := "service:" + h.refs.Services["orders-api"]
	to := "service:" + h.refs.Services["pgsql-core"]
	page := body(t, h.get("/paths?from="+from+"&to="+to, false))
	svg := mainSVG(t, page)
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}

	// Both ends' hosts, and the transport between them. orders-api runs on
	// vm-app-1 (hv-01), pgsql-core on vm-db-1 (hv-01) and vm-db-2 (hv-02) --
	// the union has to keep BOTH placements' routes.
	for _, want := range []string{"vm-app-1", "vm-db-1", "vm-db-2"} {
		if !strings.Contains(svg, want) {
			t.Errorf("the chain is missing %q", want)
		}
	}
	// The declared dependency edge rides on top, with its arrowhead.
	if !strings.Contains(svg, `marker-end="url(#arrow-dep`) {
		t.Error("the declared dependency between the two ends is not drawn with its arrow")
	}
	// And no dependency of an unrelated service: the path decorates its own
	// two ends only.
	if strings.Contains(svg, "backup-agent") {
		t.Error("a service that is neither end was drawn onto the chain")
	}

	// The URL is the question: both pickers come back selected.
	if !strings.Contains(page, `value="`+from+`" selected`) ||
		!strings.Contains(page, `value="`+to+`" selected`) {
		t.Error("the pickers do not restate the URL's question")
	}
}

func TestPathWhereAServiceSits(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/paths?from=service:"+h.refs.Services["orders-api"], false))
	svg := mainSVG(t, page)
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}
	if !strings.Contains(page, "its data-plane network") {
		t.Error("the empty far end is not named for what it means")
	}
	// The descent lands on the core chassis the host attaches through.
	if !strings.Contains(svg, "sw-core-1") && !strings.Contains(svg, "sw-core-2") {
		t.Error("the descent does not reach any attached chassis")
	}
}

func TestPathSaysWhyThereIsNone(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// pdu-a1 carries no data-plane cabling in the fixture, so there is no
	// route from a guest to it -- and the page must say that, not draw an
	// empty canvas.
	page := body(t, h.get("/paths?from=service:"+h.refs.Services["orders-api"]+
		"&to=asset:"+h.refs.Assets["pdu-a1"], false))
	if !strings.Contains(page, "No active data-plane cabling joins") {
		t.Error("a missing path is not explained")
	}
	if !strings.Contains(page, "Data plane only") {
		t.Error("the one modelling decision that changes the answer is not stated")
	}

	// And it must not claim a route length at the same time. The stranded
	// sides ARE boxes, so a scene exists; gating the header on the scene made
	// the page say "0 hops at the shortest" directly above "no path to draw".
	// A picture that contradicts itself is worse than no picture.
	if strings.Contains(page, "at the shortest") {
		t.Error("the header claims a shortest route on a page that says there is none")
	}

	// The contrast, on a question that does have an answer.
	found := body(t, h.get("/paths?from=service:"+h.refs.Services["orders-api"]+
		"&to=service:"+h.refs.Services["pgsql-core"], false))
	if !strings.Contains(found, "at the shortest") {
		t.Error("a real path does not state its length; the gate is too tight")
	}
}

// TestPathNamesAStrandedPlacement.
//
// The demo point the fixture's half-installed box exists for: log-shipper runs
// on two hosts, one properly cabled and one whose data uplink was never
// patched. The routed instance gets a chain; the stranded one is drawn as a box
// connected to nothing AND named in a note, because a placement that cannot
// reach what it is supposed to reach is the finding rather than clutter to
// tidy away.
//
// It also demonstrates the data-plane rule on real data instead of asserting
// it: srv-backup-proxy-1 HAS a cable -- to the console switch -- and still has
// no path.
func TestPathNamesAStrandedPlacement(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/paths?from=service:"+h.refs.Services["log-shipper"]+
		"&to=service:"+h.refs.Services["pgsql-core"], false))
	svg := mainSVG(t, page)
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}

	// A route exists (from the cabled instance), so the page is not the
	// no-path case -- and the stranded host is still in the picture.
	if !strings.Contains(page, "at the shortest") {
		t.Error("no route reported; the cabled instance should reach the database")
	}
	if !strings.Contains(svg, "srv-backup-proxy-1") {
		t.Error("the stranded host was pruned out of the picture; a placement that " +
			"cannot reach anything is exactly what must stay visible")
	}
	if !strings.Contains(page, "No data-plane route from: srv-backup-proxy-1") {
		t.Error("the stranded placement is drawn but not named; a reader would have to " +
			"notice the missing line by eye")
	}
	// The console cable must not appear as a path.
	if strings.Contains(svg, "sw-oob-1") {
		t.Error("the console switch is on the path; the management plane leaked in")
	}
}

func TestPathEntryPoints(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	svc := body(t, h.get("/services/"+h.refs.Services["orders-api"], false))
	if !strings.Contains(svc, "/paths?from=service:"+h.refs.Services["orders-api"]) {
		t.Error("the service page does not link to its own path question; the diagram " +
			"lesson was that a feature nobody can find does not exist")
	}
	home := body(t, h.get("/paths", false))
	if !strings.Contains(home, `href="/paths"`) {
		t.Error("the navigation rail does not carry the page")
	}
}

func TestPathAccessAndBadEnds(t *testing.T) {
	h := newHarness(t)

	anon := h.get("/paths", false)
	anon.Body.Close()
	if anon.StatusCode != http.StatusSeeOther {
		t.Errorf("an anonymous request returned %d, want a redirect to the login page", anon.StatusCode)
	}

	h.login("viewer", "viewer-password")
	ok := h.get("/paths?from=service:"+h.refs.Services["orders-api"], false)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("a read-only user got %d, want 200: the path is a read", ok.StatusCode)
	}

	missing := h.get("/paths?from=service:00000000-0000-0000-0000-000000000000", false)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown service end returned %d, want 404", missing.StatusCode)
	}

	// Garbage that the pickers never produce reads as "no selection", because
	// a hand-mangled URL is not worth a five-hundred.
	garbled := h.get("/paths?from=gibberish", false)
	garbled.Body.Close()
	if garbled.StatusCode != http.StatusOK {
		t.Errorf("a malformed end returned %d, want the picker page", garbled.StatusCode)
	}
}

func TestPathIsDeterministicThroughTheStack(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	url := "/paths?from=service:" + h.refs.Services["orders-api"] +
		"&to=service:" + h.refs.Services["pgsql-core"]
	first := mainSVG(t, body(t, h.get(url, true)))
	second := mainSVG(t, body(t, h.get(url, true)))
	if first == "" || first != second {
		t.Error("the same question produced two different pictures")
	}
}
