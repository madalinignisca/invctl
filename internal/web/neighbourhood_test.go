package web_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// The layered neighbourhood diagram, driven through the real router.
//
// The things worth protecting here are all properties of the whole stack: that
// the state of the question lives in the URL and comes back out of the HTML,
// that a layer toggle changes the picture rather than a stylesheet, that a
// hostile asset name is escaped as XML rather than shipped as markup, and that
// an asset nothing touches gets a sentence instead of a blank canvas.

// mainSVG extracts the diagram itself, so an assertion about the picture cannot
// accidentally be satisfied by the legend, the table or the page furniture.
func mainSVG(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, "<svg viewBox=")
	if start < 0 {
		return ""
	}
	end := strings.Index(page[start:], "</svg>")
	if end < 0 {
		t.Fatal("the diagram has no closing tag")
	}
	return page[start : start+end+len("</svg>")]
}

// TestNeighbourhoodPageRenders walks the picture the seeded estate produces:
// the guest, the bridge it is cabled to, the hypervisor behind that, and the
// services running on it -- all three bands, from one URL.
func TestNeighbourhoodPageRenders(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["vm-app-1"]
	page := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=2", false))

	if !strings.Contains(page, "</html>") {
		t.Fatal("the page did not render completely")
	}
	if strings.Contains(page, "<no value>") {
		t.Error("a template references a missing field")
	}

	svg := mainSVG(t, page)
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}

	// All three transport realities, in one picture. The virtual band is the
	// one that only exists because a bridge is an asset in its own right.
	for _, want := range []string{"vm-app-1", "hv-01-br0", "hv-01"} {
		if !strings.Contains(svg, want) {
			t.Errorf("the diagram does not contain %q", want)
		}
	}
	for _, band := range []string{"Service", "Virtual", "Physical"} {
		if !strings.Contains(svg, ">"+band+"</text>") {
			t.Errorf("the %s band is not labelled", band)
		}
	}

	// The descent -- the line the whole feature exists to draw -- and the
	// adjacency underneath it are different strokes, not the same one.
	if !strings.Contains(svg, "edge-descent") {
		t.Error("no service descends to anything; the layers are not stacked")
	}
	if !strings.Contains(svg, "edge-cable") {
		t.Error("no adjacency is drawn")
	}

	// The accessible name and the table beside it. A diagram that replaced the
	// table would be a regression, not a feature.
	if !strings.Contains(svg, `role="img"`) || !strings.Contains(svg, "<desc") {
		t.Error("the diagram has no accessible description")
	}
	if !strings.Contains(page, `<h2>Connections</h2>`) {
		t.Error("the adjacency table is missing")
	}

	// And it is a view: the fragment carries no mutation at all. Checked on
	// the fragment rather than the page so the layout's own sign-out form is
	// not mistaken for one.
	fragment := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=2", true))
	for _, mutation := range []string{`method="post"`, "hx-post=", "hx-delete=", "csrf_token"} {
		if strings.Contains(fragment, mutation) {
			t.Errorf("the diagram fragment offers %s; it is supposed to present state, not change it",
				mutation)
		}
	}
}

// TestNeighbourhoodIsLinkedFromTheAsset. A page nobody can reach is a page
// nobody uses, and this one belongs beside the impact link.
func TestNeighbourhoodIsLinkedFromTheAsset(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, "/assets/"+assetID+"/neighbourhood") {
		t.Error("the asset page does not link to its neighbourhood diagram")
	}
	if !strings.Contains(page, "/assets/"+assetID+"/impact") {
		t.Error("the impact link disappeared")
	}
}

// TestNeighbourhoodHopCountIsRespected. The hop count is part of the question,
// and it has to reach the walk rather than only the heading.
func TestNeighbourhoodHopCountIsRespected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["vm-app-1"]

	near := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=1", true))
	far := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=3", true))

	if strings.Contains(near, "sw-core-1") {
		t.Error("one hop from a guest reached the core switch; the bound is not applied")
	}
	if !strings.Contains(far, "sw-core-1") {
		t.Error("three hops from a guest did not reach the core switch it is cabled behind")
	}

	// An out-of-range value is clamped rather than trusted: every id in the
	// answer becomes a placeholder in four later statements.
	huge := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=999", true))
	if !strings.Contains(huge, `value="4" selected`) {
		t.Error("an absurd hop count was not clamped back to the maximum")
	}
	silly := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=notanumber", true))
	if !strings.Contains(silly, `value="2" selected`) {
		t.Error("an unparseable hop count did not fall back to the default")
	}
}

// TestNeighbourhoodLayerToggleChangesTheSVG. The toggles are a server
// re-render, not a stylesheet trick: turning a layer off has to remove it from
// the document, because hiding a band client-side leaves a hole in the layout
// and a viewBox describing a picture that is no longer drawn.
func TestNeighbourhoodLayerToggleChangesTheSVG(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]

	all := mainSVG(t, body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=2", true)))
	if !strings.Contains(all, "orders-api") {
		t.Fatal("the unfiltered diagram has no service layer to turn off")
	}
	if !strings.Contains(all, "hv-01-br0") {
		t.Fatal("the unfiltered diagram has no virtual layer to turn off")
	}

	physicalOnly := mainSVG(t, body(t,
		h.get("/assets/"+assetID+"/neighbourhood?hops=2&filter=1&layer=physical", true)))
	if strings.Contains(physicalOnly, "orders-api") {
		t.Error("a service is still drawn with the service layer turned off")
	}
	if strings.Contains(physicalOnly, "hv-01-br0") {
		t.Error("a bridge is still drawn with the virtual layer turned off")
	}
	if !strings.Contains(physicalOnly, "sw-core-1") {
		t.Error("the physical layer was removed along with the others")
	}
	// The band collapses rather than leaving an empty stripe.
	if strings.Contains(physicalOnly, ">Virtual</text>") {
		t.Error("an empty virtual band is still drawn")
	}

	// Turning everything off cannot hide the asset the picture is built
	// around, and the page says so rather than appearing to ignore the click.
	none := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=2&filter=1", true))
	if !strings.Contains(none, "hv-01") {
		t.Error("unticking every layer hid the subject of the diagram")
	}
	if !strings.Contains(none, "disabled") {
		t.Error("the subject's own layer toggle is not locked")
	}
}

// TestNeighbourhoodURLRoundTripsItsState. An incident tool's diagram is worth
// little if it cannot be sent to a colleague: every control has to come back
// out of the HTML in the state the URL put it in.
func TestNeighbourhoodURLRoundTripsItsState(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["hv-01"]
	page := body(t, h.get(
		"/assets/"+assetID+"/neighbourhood?hops=3&filter=1&layer=physical&layer=service", false))

	if !strings.Contains(page, `value="3" selected`) {
		t.Error("the hop count did not come back out of the URL")
	}
	for _, want := range []string{
		`value="physical"`, `value="service"`, `value="virtual"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the %s toggle is missing", want)
		}
	}
	// The virtual toggle must be the unchecked one, and the other two checked.
	checked := checkedLayers(page)
	if !checked["physical"] || !checked["service"] {
		t.Errorf("checked layers = %v, want physical and service", checked)
	}
	if checked["virtual"] {
		t.Errorf("virtual came back checked despite being absent from the URL: %v", checked)
	}

	// The swap pushes the URL, so what is on screen and what is in the address
	// bar are the same question.
	if !strings.Contains(page, `hx-push-url="true"`) {
		t.Error("the toolbar does not push the URL; a filtered view could not be pasted into a ticket")
	}
	// And it works without JavaScript: the same form is a plain GET.
	if !strings.Contains(page, `action="/assets/`+assetID+`/neighbourhood"`) {
		t.Error("the toolbar has no plain-GET fallback")
	}
}

// checkedLayers reads the layer checkboxes back out of the rendered form.
func checkedLayers(page string) map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{"physical", "virtual", "service"} {
		marker := `name="layer" value="` + name + `"`
		i := strings.Index(page, marker)
		if i < 0 {
			continue
		}
		rest := page[i:]
		end := strings.Index(rest, ">")
		if end < 0 {
			continue
		}
		out[name] = strings.Contains(rest[:end], "checked")
	}
	return out
}

// TestNeighbourhoodHTMXBranching: the same URL serves a fragment to HTMX and a
// whole page to a browser, and the fragment re-establishes its own swap target.
func TestNeighbourhoodHTMXBranching(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	assetID := h.refs.Assets["vm-app-1"]

	full := body(t, h.get("/assets/"+assetID+"/neighbourhood", false))
	if !strings.Contains(full, "<!doctype html>") {
		t.Error("a browser navigation did not receive a full page")
	}
	if !strings.Contains(full, `id="neighbourhood"`) {
		t.Error("the full page does not contain the swap target")
	}

	fragment := body(t, h.get("/assets/"+assetID+"/neighbourhood", true))
	if strings.Contains(fragment, "<!doctype html>") {
		t.Error("an HTMX request received a full page")
	}
	if !strings.Contains(fragment, `id="neighbourhood"`) {
		t.Error("the fragment does not re-establish its own swap target")
	}
	// The whole question is inside the fragment. If the toolbar were outside
	// it, changing the hop count would leave the controls describing a
	// neighbourhood nobody is looking at.
	if !strings.Contains(fragment, `name="hops"`) || !strings.Contains(fragment, `name="layer"`) {
		t.Error("the controls are outside the swap target; the question and the answer can drift apart")
	}
}

// TestNeighbourhoodEscapesAHostileLabel. SVG is XML, and an asset name is
// operator-supplied text. Nothing here concatenates markup and nothing is
// marked template.HTML, so the proof is that the rendered diagram still parses
// as XML with the name intact and inert.
func TestNeighbourhoodEscapesAHostileLabel(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	// Short enough that the label is not truncated, so both the drawn text and
	// the verbatim <title> carry the whole thing.
	const hostile = `R&D <b>"1"</b>`
	parent := h.refs.Assets["hv-01"]
	asset, err := domain.NewAsset(store.NewID(), domain.KindVM, hostile, &parent, h.store.Now())
	if err != nil {
		t.Fatalf("building the asset: %v", err)
	}
	if err := h.store.CreateAsset(ctx, domain.SystemActor, asset, nil); err != nil {
		t.Fatalf("creating the asset: %v", err)
	}

	page := body(t, h.get("/assets/"+asset.ID+"/neighbourhood?hops=1", false))
	svg := mainSVG(t, page)
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}

	if strings.Contains(page, "<b>") {
		t.Error("the asset name was emitted as markup")
	}
	if !strings.Contains(svg, "R&amp;D") {
		t.Errorf("the ampersand in the name was not escaped: %s", svg)
	}

	// The real assertion: it is still well-formed XML. A broken escape here
	// does not merely look wrong, it stops the browser drawing anything at all.
	decoder := xml.NewDecoder(strings.NewReader(svg))
	// The document uses attribute names carrying a colon (Alpine's x-bind), so
	// namespace resolution is deliberately not what is under test here.
	decoder.Strict = true
	decoder.Entity = xml.HTMLEntity
	var found bool
	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the rendered diagram is not well-formed XML: %v", err)
		}
		if chars, ok := tok.(xml.CharData); ok && strings.Contains(string(chars), hostile) {
			// Unescaped by the parser, which is what proves the name survived
			// as text rather than as markup.
			found = true
		}
	}
	if !found {
		t.Errorf("the hostile name did not survive as text anywhere in the diagram")
	}
}

// TestNeighbourhoodOfAnIsolatedAssetIsNotBlank. An asset nothing touches gets
// a sentence saying so. A blank canvas and an unmodelled asset look identical
// and mean completely different things.
func TestNeighbourhoodOfAnIsolatedAssetIsNotBlank(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	asset, err := domain.NewAsset(store.NewID(), domain.KindStorage, "nas-unmodelled", nil, h.store.Now())
	if err != nil {
		t.Fatalf("building the asset: %v", err)
	}
	if err := h.store.CreateAsset(ctx, domain.SystemActor, asset, nil); err != nil {
		t.Fatalf("creating the asset: %v", err)
	}

	page := body(t, h.get("/assets/"+asset.ID+"/neighbourhood", false))
	if svg := mainSVG(t, page); svg != "" {
		t.Error("a single box with no lines was drawn as though it were a diagram")
	}
	if !strings.Contains(page, "Nothing to draw here.") {
		t.Error("an isolated asset produced a blank canvas rather than an explanation")
	}
	if !strings.Contains(page, "nobody has finished modelling") {
		t.Error("the empty state does not say that no connectivity is a finding")
	}
}

// TestNeighbourhoodStatesItsElisions. The node budget is a cut, and a cut
// nobody is told about is indistinguishable from a fact that does not exist.
func TestNeighbourhoodStatesItsElisions(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// hv-01's guests run services that depend on services elsewhere in the
	// estate, so the "reaches outside" note has something real to report.
	assetID := h.refs.Assets["vm-app-1"]
	page := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=1", false))
	if !strings.Contains(page, "outside this neighbourhood") {
		t.Error("declared dependencies that leave the picture are not reported; a service whose " +
			"providers are all off-screen would read as depending on nothing")
	}

	// And the layer filter says what it hid rather than quietly shrinking the
	// picture.
	filtered := body(t, h.get("/assets/"+assetID+"/neighbourhood?hops=2&filter=1&layer=virtual", false))
	if !strings.Contains(filtered, "layer filter is hiding") {
		t.Error("the layer filter does not report what it removed")
	}
}

// TestNeighbourhoodRequiresASession and refuses an id that resolves to nothing.
func TestNeighbourhoodAccessAndUnknownAsset(t *testing.T) {
	h := newHarness(t)

	anon := h.get("/assets/"+h.refs.Assets["hv-01"]+"/neighbourhood", false)
	anon.Body.Close()
	if anon.StatusCode != http.StatusSeeOther {
		t.Errorf("an anonymous request returned %d, want a redirect to the login page", anon.StatusCode)
	}

	h.login("viewer", "viewer-password")
	ok := h.get("/assets/"+h.refs.Assets["hv-01"]+"/neighbourhood", false)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("a read-only user got %d, want 200: the diagram is a read", ok.StatusCode)
	}

	missing := h.get("/assets/00000000-0000-0000-0000-000000000000/neighbourhood", false)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown asset returned %d, want 404", missing.StatusCode)
	}
}

// TestNeighbourhoodSurvivesACableBetweenTwoPortsOfOneAsset.
//
// Two ports of one chassis patched together is legal in the schema, offered by
// the patch form, and real: a loop cable for a test, or a stacking link between
// two line cards recorded as one asset. At this zoom a node is an ASSET, so that
// cable is a self edge, and diagram.Build refuses a self edge outright -- it
// cannot lay one out and will not draw a lie. The refusal came back as an
// unhandled error, so the page 500ed.
//
// The blast radius is the point. It is not only that asset's own diagram: the
// cable appears in the neighbourhood of everything within N hops of it, so one
// loop cable takes out the diagram for a whole rack. Asserted from the
// neighbour's side for exactly that reason.
func TestNeighbourhoodSurvivesACableBetweenTwoPortsOfOneAsset(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	looped := mustServerAssetWeb(t, h, "loop-01")
	a := mustInterfaceWeb(t, h, looped, "eth0")
	b := mustInterfaceWeb(t, h, looped, "eth1")
	self, err := domain.NewLink(store.NewID(), a, b)
	if err != nil {
		t.Fatalf("building the loop cable: %v", err)
	}
	if err := h.store.CreateLink(ctx, domain.SystemActor, self); err != nil {
		t.Fatalf("cabling two ports of one asset -- if this is now refused, the "+
			"defect moved rather than went away: %v", err)
	}

	// A neighbour, so the asset is reachable from somewhere else and the loop
	// cable lands in a picture that is not its own.
	neighbour := mustServerAssetWeb(t, h, "peer-01")
	c := mustInterfaceWeb(t, h, looped, "eth2")
	d := mustInterfaceWeb(t, h, neighbour, "eth0")
	across, err := domain.NewLink(store.NewID(), c, d)
	if err != nil {
		t.Fatalf("building the cable to the neighbour: %v", err)
	}
	if err := h.store.CreateLink(ctx, domain.SystemActor, across); err != nil {
		t.Fatalf("cabling to the neighbour: %v", err)
	}

	for _, tc := range []struct{ name, id string }{
		{"the looped asset's own diagram", looped},
		{"a neighbour's diagram, which merely contains the loop", neighbour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.get("/assets/"+tc.id+"/neighbourhood?hops=2", false)
			page := body(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("got %d, want 200: one loop cable should not cost an operator "+
					"the diagram during an incident", resp.StatusCode)
			}
			if svg := mainSVG(t, page); svg == "" {
				t.Error("the page rendered but the diagram did not")
			}
		})
	}

	// And the picture says what it left out. A connection the database holds and
	// the diagram does not draw is exactly the gap that makes someone trust a
	// picture more than it deserves.
	page := body(t, h.get("/assets/"+looped+"/neighbourhood?hops=2", false))
	if !strings.Contains(page, "two ports of the same asset") {
		t.Error("the undrawn loop cable is not mentioned; a reader would conclude the " +
			"port is unpatched")
	}
}

// TestParallelCablesAreEachDrawnAndLabelled.
//
// Two cables between the same pair of chassis is redundancy, which is the
// specific fact an operator opens this page to check. The layout fans them apart
// so they do not read as one line, and each needs its own hover text naming the
// ports -- "which of the two is down" is the next question after "there are two".
//
// The renderer used to attach titles by looking each edge up in a map keyed by
// its endpoints, and parallel edges share those endpoints, so one title
// overwrote the other and both lines claimed the same pair of ports. Two lines
// were drawn, which is why this looked right.
func TestParallelCablesAreEachDrawnAndLabelled(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	left := mustServerAssetWeb(t, h, "bonded-a")
	right := mustServerAssetWeb(t, h, "bonded-b")
	for i, ports := range [][2]string{{"eth0", "eth0"}, {"eth1", "eth1"}} {
		a := mustInterfaceWeb(t, h, left, ports[0])
		b := mustInterfaceWeb(t, h, right, ports[1])
		l, err := domain.NewLink(store.NewID(), a, b)
		if err != nil {
			t.Fatalf("building cable %d: %v", i, err)
		}
		if err := h.store.CreateLink(ctx, domain.SystemActor, l); err != nil {
			t.Fatalf("creating cable %d: %v", i, err)
		}
	}

	svg := mainSVG(t, body(t, h.get("/assets/"+left+"/neighbourhood?hops=1", false)))
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}

	paths := pathTitles(svg)
	if len(paths) < 2 {
		t.Fatalf("the diagram draws %d lines, want at least the two cables: %s", len(paths), svg)
	}
	// Both ports are named eth0/eth1 on each side, so the titles differ only by
	// which pair they name -- which is the whole point.
	seen := map[string]int{}
	for _, title := range paths {
		seen[title]++
	}
	for title, n := range seen {
		if n > 1 && strings.Contains(title, "eth") {
			t.Errorf("%d lines carry the identical hover text %q, so the operator cannot "+
				"tell which cable is which", n, title)
		}
	}
	var withEth int
	for _, title := range paths {
		if strings.Contains(title, "eth0") || strings.Contains(title, "eth1") {
			withEth++
		}
	}
	if withEth < 2 {
		t.Errorf("only %d of the drawn lines name a port; a fanned pair with one label "+
			"is a picture of one cable with a decoration: %v", withEth, paths)
	}
}

// pathTitles is the <title> of every <path> in the SVG, in document order.
func pathTitles(svg string) []string {
	var out []string
	for rest := svg; ; {
		i := strings.Index(rest, "<path ")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		open := strings.Index(rest, "<title>")
		end := strings.Index(rest, "</title>")
		next := strings.Index(rest[1:], "<path ")
		if open < 0 || end < open || (next >= 0 && open > next+1) {
			out = append(out, "") // A path with no title of its own.
			rest = rest[1:]
			continue
		}
		out = append(out, rest[open+len("<title>"):end])
		rest = rest[end:]
	}
}

// TestDependencyEdgesCarryAnArrowhead.
//
// A dependency has a direction and, until this, the picture carried it only in
// hover text -- two services joined by a plain line read as "related", not as
// "orders-api depends on pgsql-core". The consumer's line now ends in an
// arrowhead at its provider. Only dependencies get one: a cable is undirected,
// and an arrow on it would assert a flow direction that is not in the
// database.
func TestDependencyEdgesCarryAnArrowhead(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// hv-01 at two hops pulls in the service band with its declared
	// dependencies -- the fixture's orders-api chain.
	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"]+"/neighbourhood?hops=2", false))
	svg := mainSVG(t, page)
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}

	if !strings.Contains(svg, `<marker id="arrow-dep"`) {
		t.Error("the arrowhead def is missing; every marker-end reference dangles")
	}
	if !strings.Contains(svg, `marker-end="url(#arrow-dep`) {
		t.Error("no dependency edge references an arrowhead; direction is only in hover text again")
	}
	for _, m := range regexp.MustCompile(`class="([^"]*)"[^>]*marker-end`).FindAllStringSubmatch(svg, -1) {
		if !strings.Contains(m[1], "edge-dependency") {
			t.Errorf("an edge of class %q carries an arrowhead; only a dependency has "+
				"a direction the geometry does not already show", m[1])
		}
	}
}

// TestNeighbourhoodLocalBindDrawsNoNetworkLine. docs/reachability-design.md:
// a loopback or unix socket's traffic is intra-host by definition, and the
// impact engine exempts exactly those binds from network reasoning. The picture
// must not contradict the engine by drawing a line that implies traversal.
func TestNeighbourhoodLocalBindDrawsNoNetworkLine(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()

	// orders-api/admin is the fixture's loopback endpoint.
	endpoints, err := h.store.ListEndpointsByService(ctx, h.refs.Services["orders-api"])
	if err != nil {
		t.Fatalf("listing endpoints: %v", err)
	}
	var hasLocal bool
	for _, ep := range endpoints {
		if domain.IsLocalBind(ep.BindScope) {
			hasLocal = true
		}
	}
	if !hasLocal {
		t.Skip("the fixture has no loopback endpoint to assert against")
	}

	page := body(t, h.get("/assets/"+h.refs.Assets["vm-app-1"]+"/neighbourhood?hops=1", false))
	// The host binding still descends; the loopback one contributes nothing
	// beyond it. What must never happen is a descent whose only justification
	// is a loopback socket, so the table has to name the bind scope it drew.
	if !strings.Contains(page, "all addresses on the host") {
		t.Error("an unpinned host binding is not labelled; a blank reads as missing data " +
			"where the truth is that it listens on every address")
	}
	if strings.Contains(page, "loopback</td>") {
		t.Error("a loopback socket was drawn as a network descent")
	}
}

// TestNeighbourhoodIsDeterministic. Two people on the same call opening the
// same URL have to see the same picture, or they are comparing two diagrams.
func TestNeighbourhoodIsDeterministic(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	path := "/assets/" + h.refs.Assets["hv-01"] + "/neighbourhood?hops=2"
	first := mainSVG(t, body(t, h.get(path, true)))
	for i := 0; i < 5; i++ {
		again := mainSVG(t, body(t, h.get(path, true)))
		if again != first {
			t.Fatalf("run %d produced a different diagram from run 0", i+1)
		}
	}
}

// TestNeighbourhoodEscapesAHostileLabelInTheTable keeps the text fallback
// honest too: the table is the thing somebody pastes into a ticket.
func TestNeighbourhoodTableCarriesFullNames(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"]+"/neighbourhood?hops=2", false))
	// Port names are what make the physical layer useful, and they are only in
	// the table -- an edge label centred on a line is the worst collider in a
	// layered diagram, so the picture puts them in a tooltip and the table
	// prints them.
	if !strings.Contains(page, "eno2") {
		t.Error("the connections table does not carry port names")
	}
	// The bond a NIC is enslaved to travels with the port, or the cable to the
	// switch and the bridge's uplink read as unrelated facts about one box.
	if !strings.Contains(page, "bond0") {
		t.Error("the enslaving bond is not shown beside the port")
	}
}
