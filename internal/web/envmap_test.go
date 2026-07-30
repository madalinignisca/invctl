package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// The environment map through the real router. The store suite owns
// membership and the budget; here it is the stack: the page renders the
// seeded prod estate, the toggles steer it, and a person can find it.

func TestEnvironmentMapRendersTheWholeEstate(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/environments/"+h.refs.Environments["prod"]+"/map", false))
	svg := mainSVG(t, page)
	if svg == "" {
		t.Fatal("no diagram was drawn")
	}
	// One box from each band, and the two diagrams' shared vocabulary.
	for _, want := range []string{"orders-api", "hv-01-br0", "sw-core-1", "edge-cable", "edge-descent"} {
		if !strings.Contains(svg, want) {
			t.Errorf("the map is missing %q", want)
		}
	}

	physicalOnly := mainSVG(t, body(t, h.get("/environments/"+h.refs.Environments["prod"]+
		"/map?filter=1&layer=physical", true)))
	if strings.Contains(physicalOnly, "orders-api") || strings.Contains(physicalOnly, "hv-01-br0") {
		t.Error("the layer filter did not remove the other bands")
	}
	if !strings.Contains(physicalOnly, "sw-core-1") {
		t.Error("the physical layer went missing along with the others")
	}
}

func TestEnvironmentMapIsFindable(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	list := body(t, h.get("/environments", false))
	if !strings.Contains(list, "/environments/"+h.refs.Environments["prod"]+"/map") {
		t.Error("the environments list does not link to the map; a feature nobody can " +
			"find does not exist")
	}
}

func TestEnvironmentMapAccessAndUnknown(t *testing.T) {
	h := newHarness(t)

	anon := h.get("/environments/"+h.refs.Environments["prod"]+"/map", false)
	anon.Body.Close()
	if anon.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous got %d, want a redirect to login", anon.StatusCode)
	}

	h.login("viewer", "viewer-password")
	ok := h.get("/environments/"+h.refs.Environments["prod"]+"/map", false)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("a read-only user got %d, want 200: the map is a read", ok.StatusCode)
	}
	missing := h.get("/environments/00000000-0000-0000-0000-000000000000/map", false)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown environment returned %d, want 404", missing.StatusCode)
	}
}
