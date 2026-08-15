// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestRepricingThroughTheFormKeepsBothFigures.
//
// THE ROUND TRIP IS THE ASSERTION. The store keeps both figures; this proves an
// operator can actually reach that behaviour, which is the half that was
// missing for the entire life of the project -- the schema held windows nobody
// could fill, because the only button on offer overwrote the amount.
func TestRepricingThroughTheFormKeepsBothFigures(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	page := body(t, h.get("/assets/"+id, false))

	// A recurring line offers Reprice; a one-off must not, because a payment
	// that happened has no "from now on".
	if !strings.Contains(page, "reprice=") {
		t.Fatal("no recurring cost line offers a reprice")
	}
	costID := firstRepriceID(t, page)

	form := body(t, h.get("/assets/"+id+"?reprice="+costID, false))
	if !strings.Contains(form, `name="effective_from"`) {
		t.Fatal("the reprice form has no effective-from field")
	}
	// The old figure is shown and not editable: that it survives untouched is
	// the entire point of the verb.
	if strings.Contains(form, `name="amount" value=`) {
		t.Error("the reprice form pre-fills the amount; it must ask for the NEW one")
	}

	resp := h.post("/assets/"+id+"/costs/"+costID+"/reprice", url.Values{
		"csrf_token":     {h.csrfToken("/assets/" + id)},
		"amount":         {"1410.00"},
		"effective_from": {"2027-01-01"},
		"note":           {"annual uplift"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("repricing returned %d, want a redirect", resp.StatusCode)
	}

	after := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(after, "2027-01-01") {
		t.Error("the new line's start date is not shown")
	}
	if !strings.Contains(after, "2026-12-31") {
		t.Error("the superseded line was not closed the day before the new one starts")
	}
	if !strings.Contains(after, "annual uplift") {
		t.Error("the reason for the rise is not recorded against the new line")
	}
}

// TestARetiredLineOffersNoReprice. History is not amendable, and a withdrawn
// figure cannot be superseded -- it was already taken back.
func TestAOneOffOffersNoReprice(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))

	// The acquisition line is a one-off. Split into rows and check the one
	// carrying it -- plainly, because Go's RE2 has no lookahead and the regex
	// that expressed "a <tr> containing once but not spanning </tr>" panicked
	// at compile time rather than failing a match.
	// EACH CHUNK IS TRUNCATED AT ITS OWN </tr>, and that matters. Splitting on
	// "<tr" alone leaves the LAST row running on into the add-a-cost form,
	// whose period dropdown contains <option value="once">once</option> -- so
	// the yearly row appeared to be a one-off offering a reprice, and the test
	// failed against code that was correct.
	found := false
	for _, chunk := range strings.Split(page, "<tr") {
		row := chunk
		if end := strings.Index(row, "</tr>"); end >= 0 {
			row = row[:end]
		}
		if !strings.Contains(row, ">once<") {
			continue
		}
		found = true
		if strings.Contains(row, "reprice=") {
			t.Error("a one-off offers a reprice; a payment that happened has no new rate")
		}
	}
	if !found {
		t.Skip("no one-off line on this fixture asset")
	}
}

// firstRepriceID pulls the first cost line offered for repricing.
func firstRepriceID(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`reprice=([0-9a-f-]{36})`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no reprice id on the page")
	}
	return m[1]
}
