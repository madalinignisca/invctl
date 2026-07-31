package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Certificates through the real router, against the seeded fixture.

func TestCertificatesLeadWithWhatExpiresSoonest(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/certificates", false))

	// The fixture's five, each making one point.
	for _, subject := range []string{
		"*.example.com", "partners.example.com", "vault.internal",
		"sso.example.com", "mimir.internal",
	} {
		if !strings.Contains(page, subject) {
			t.Errorf("the certificate list is missing %s", subject)
		}
	}

	// Ordering is the feature: the already-expired internal certificate must
	// come before the one with fifty days left, and the undated one last.
	expired := strings.Index(page, "vault.internal")
	soon := strings.Index(page, "partners.example.com")
	later := strings.Index(page, "sso.example.com")
	undated := strings.Index(page, "mimir.internal")
	if expired > soon || soon > later || later > undated {
		t.Errorf("the list is not ordered by urgency: expired=%d soon=%d later=%d undated=%d",
			expired, soon, later, undated)
	}
	// A certificate nobody renews says so rather than showing a blank cell.
	if !strings.Contains(page, "unassigned") {
		t.Error("a certificate with no team does not say so")
	}
}

// TestFindingACertificateByHostThroughTheUI. The wildcard rules have to survive
// the round trip, because the filter is the reason the names are a table.
func TestFindingACertificateByHostThroughTheUI(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	covered := body(t, h.get("/certificates?host=orders.example.com", false))
	if !strings.Contains(covered, "*.example.com") {
		t.Error("the wildcard does not cover a single label beneath it")
	}
	if strings.Contains(covered, "vault.internal") {
		t.Error("an unrelated certificate was swept into a host search")
	}

	// Two labels: no TLS client accepts this, so neither does the page.
	deep := body(t, h.get("/certificates?host=a.b.example.com", false))
	if strings.Contains(deep, "*.example.com") {
		t.Error("a wildcard matched two labels")
	}
	if !strings.Contains(deep, "No certificates match") {
		t.Error("a host nothing covers does not say so plainly")
	}
}

// The expiry report is where this feature earns its place: hardware, licences
// and certificates in one list, ordered by when they bite.
func TestCertificatesAppearInTheExpiryReport(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/expiry", false))
	if !strings.Contains(page, "partners.example.com") {
		t.Error("an expiring certificate is missing from the expiry report")
	}
	if !strings.Contains(page, "vault.internal") {
		t.Error("an already-expired certificate is missing from the expiry report")
	}
	// Hardware is still there: certificates joined the report rather than
	// replacing what was in it.
	if !strings.Contains(page, "hv-01") {
		t.Error("adding certificates displaced the hardware")
	}
	// A certificate's "what rides on it" is how many places it is deployed.
	if !strings.Contains(page, "deployments") {
		t.Error("the report does not say how many places an expiring certificate is deployed")
	}
	// And the undated one is counted, not silently dropped.
	if !strings.Contains(page, "no recorded expiry") {
		t.Error("the report does not call out certificates with no expiry recorded")
	}
}

func TestCertificateDetailShowsWhereItIsDeployedAndNeverAKey(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	list := body(t, h.get("/certificates?host=orders.example.com", false))
	id := firstCertificateLink(t, list)
	page := body(t, h.get("/certificates/"+id, false))

	// Four deployments: three assets and a service.
	for _, place := range []string{"vm-proxy-1", "vm-k8s-1", "vm-k8s-2", "orders-web"} {
		if !strings.Contains(page, place) {
			t.Errorf("the certificate page does not list %s among its deployments", place)
		}
	}
	// The names it covers.
	if !strings.Contains(page, "www.example.com") {
		t.Error("the certificate page does not list the names it covers")
	}
	// The key reference is a path, and the page says what it is not.
	if !strings.Contains(page, "vault:secret/tls/wildcard-example") {
		t.Error("the key reference is not shown")
	}
	if !strings.Contains(page, "never be") {
		t.Error("the page does not say the key itself is not in the database")
	}
}

func TestCreatingACertificateAndItsValidation(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/certificates", url.Values{
		"csrf_token": {h.csrfToken("/certificates")},
		"subject_cn": {"new.example.com"},
		"issuer":     {"Let's Encrypt R3"},
		"not_after":  {"2027-01-01"},
		// A paste straight out of openssl, prefixes and all.
		"sans": {"DNS:alt.example.com, DNS:other.example.com"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating a certificate returned %d, want a redirect", resp.StatusCode)
	}

	found := body(t, h.get("/certificates?host=alt.example.com", false))
	if !strings.Contains(found, "new.example.com") {
		t.Error("a name pasted with its DNS: prefix was not stored as a name")
	}

	// A role with no team is the rule this feature inherits from teams, and it
	// must come back as a field error rather than a bare 422.
	bad := h.post("/certificates", url.Values{
		"csrf_token":   {h.csrfToken("/certificates")},
		"subject_cn":   {"broken.example.com"},
		"manager_role": {"operator"},
	}, false)
	page := body(t, bad)
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a role with no team returned %d, want 422", bad.StatusCode)
	}
	if !strings.Contains(page, `class="field-error"`) {
		t.Error("the re-rendered form carries no field error")
	}
	if !strings.Contains(page, "broken.example.com") {
		t.Error("the re-rendered form lost the subject that was typed")
	}
}

func TestCertificateWritesRequireAdmin(t *testing.T) {
	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")

	if resp := viewer.get("/certificates", false); resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Errorf("a read-only user got %d on /certificates", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	resp := viewer.post("/certificates", url.Values{
		"csrf_token": {viewer.csrfToken("/certificates")},
		"subject_cn": {"rogue.example.com"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusOK {
		t.Errorf("a read-only user created a certificate (%d)", resp.StatusCode)
	}
}

func firstCertificateLink(t *testing.T, page string) string {
	t.Helper()
	const marker = `href="/certificates/`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatal("no certificate link on the page")
	}
	rest := page[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("malformed certificate link")
	}
	return rest[:end]
}
