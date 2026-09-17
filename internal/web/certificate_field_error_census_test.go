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

// TestCertificateRefusalsCarryAFieldErrorForEveryKeyTheValidatorCanEmit is the
// guard the WP-J9 whole-branch review asked for, not the check that already
// passed.
//
// Nine implementers and nine reviewers verified the SWAP TARGET half of the
// certificate refusal ("does the 422 land in a partial that renders .Errors
// and .Spec?") and never the FIELD-KEY half ("does that partial have a hook
// for every key its handler can emit?"). certificate_form declared a target,
// declared a swap, rendered .Errors AND .Spec for four keys -- and had no
// hook at all for "sans". A SAN typo produced a 422 that swapped in cleanly,
// refilled every other field, and said NOTHING: the paste was gone, no
// message anywhere. That is a silent refusal of the exact shape this whole
// work package exists to remove, hiding behind a partial that looked, at
// partial granularity, entirely correct.
//
// So this asserts at FIELD-KEY granularity: one case per key
// domain.NewCertificate / (*Certificate).Validate can emit via ve.Add,
// covering both refusal paths (POST /certificates and POST
// /certificates/{id}) since they run different code (NewCertificate vs
// Validate) and re-render different partials (certificate_form vs the edit
// form inside certificate_panel).
//
// Each case asserts the ACTUAL message text ve.Add would attach to that key,
// not merely `class="field-error"` somewhere in the body -- an early version
// of this test asserted the weaker thing and passed on "update/sans" even
// with the "sans" hook deleted, riding on an UNRELATED incidental
// "lifecycle" error caused by the test's own form omitting that field. A
// generic presence check is exactly the partial-granularity mistake this
// test exists to move past; matching the specific text ties each case to the
// one key it claims to drive.
//
// What would be true if this were broken: a case's 422 body would be missing
// its own key's message text. Mutation proof: this fix wave's report records
// removing the "sans" hook from both forms and re-running this test --
// "create/sans" and "update/sans" went red and nothing else did.
func TestCertificateRefusalsCarryAFieldErrorForEveryKeyTheValidatorCanEmit(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Every case drives a refusal through exactly one field, so a failure
	// names the key that is silent rather than "something is wrong somewhere
	// on this form". wantText is a fragment of the exact ve.Add message for
	// that key, so a pass here can only be the intended field's own hook.
	type certCase struct {
		key      string
		extra    url.Values // merged over a baseline that would otherwise succeed
		wantText string
	}

	cases := []certCase{
		{key: "subject_cn", extra: url.Values{"subject_cn": {""}}, wantText: "is required"},
		{key: "not_after", extra: url.Values{
			"not_before": {"2030-01-01"}, "not_after": {"2020-01-01"},
		}, wantText: "cannot be before the date it becomes valid"},
		{key: "not_before", extra: url.Values{"not_before": {"not-a-date"}},
			wantText: "must be a real date"},
		{key: "sans", extra: url.Values{"sans": {"###not-a-hostname###"}},
			wantText: "is not a hostname or an IP address"},
		{key: "fingerprint", extra: url.Values{"fingerprint": {"not-hex-zz"}},
			wantText: "must be hexadecimal"},
		{key: "manager_role", extra: url.Values{"manager_role": {"operator"}}, // no team_id
			wantText: "needs a team"},
		{key: "issuer", extra: url.Values{"issuer": {strings.Repeat("a", 300)}}, // > maxIssuer (256)
			wantText: "is longer than"},
		{key: "serial", extra: url.Values{"serial": {strings.Repeat("a", 200)}}, // > maxSerial (128)
			wantText: "is longer than"},
		{key: "key_ref", extra: url.Values{"key_ref": {strings.Repeat("a", 600)}}, // > maxKeyRef (512)
			wantText: "is longer than"},
		// "lifecycle" is reachable on CREATE too -- certificateSpecFromForm
		// reads it with formValue regardless of whether the create page
		// exposes a control for it -- but the create form deliberately has
		// no lifecycle input (a new certificate is always "active"), so a
		// browser can never reach this key on create. Documented, not
		// silently skipped: see the create/update split below.
	}

	for _, tc := range cases {
		t.Run("create/"+tc.key, func(t *testing.T) {
			form := url.Values{
				"csrf_token": {h.csrfToken("/certificates")},
				"subject_cn": {"census-" + tc.key + ".example.com"},
			}
			for k, v := range tc.extra {
				form[k] = v
			}
			resp := h.post("/certificates", form, true)
			b := body(t, resp)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
			}
			assertFieldError(t, b, tc.wantText, "create", tc.key)
		})
	}

	// "lifecycle" IS reachable through the create HANDLER (a raw POST, not a
	// rendered control) even though the create FORM has no lifecycle input.
	// Said here rather than omitted: the handler answers 422 either way, and
	// today that 422 shows no message for this one key because the create
	// page never offers the control that would carry it. Not fixed here --
	// adding a lifecycle picker to "add a certificate" is a UI decision this
	// fix wave was not asked to make -- but the gap must be a stated
	// exception, not a case this table quietly forgot.
	t.Run("create/lifecycle_documented_gap", func(t *testing.T) {
		resp := h.post("/certificates", url.Values{
			"csrf_token": {h.csrfToken("/certificates")},
			"subject_cn": {"census-lifecycle-create.example.com"},
			"lifecycle":  {"not-a-real-lifecycle"},
		}, true)
		b := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
		}
		// NOT asserting a field error here: this is the one key the create
		// form has no control for, and that is the documented gap, not a
		// bug this test is silently passing over.
		_ = b
	})

	// Seed one certificate to edit, then drive the same keys through the
	// UPDATE path (Validate, not NewCertificate) and the edit form inside
	// certificate_panel, not certificate_form.
	seedResp := h.post("/certificates", url.Values{
		"csrf_token": {h.csrfToken("/certificates")},
		"subject_cn": {"census-edit-seed.example.com"},
	}, true)
	seedResp.Body.Close()
	redirect := seedResp.Header.Get("HX-Redirect")
	if redirect == "" {
		t.Fatalf("seeding the certificate to edit did not redirect; response: %+v", seedResp)
	}
	certID := strings.TrimPrefix(redirect, "/certificates/")

	// "lifecycle" IS reachable on the edit form -- ce-life is a real <select>
	// -- so it belongs in the covered table, not the documented-gap table.
	editCases := append(append([]certCase{}, cases...), certCase{
		key: "lifecycle", extra: url.Values{"lifecycle": {"not-a-real-lifecycle"}},
		wantText: "must be one of",
	})

	for _, tc := range editCases {
		t.Run("update/"+tc.key, func(t *testing.T) {
			page := body(t, h.get("/certificates/"+certID, false))
			form := url.Values{
				"csrf_token":  {h.csrfToken("/certificates/" + certID)},
				"row_version": {versionInFirstForm(t, page)},
				"subject_cn":  {"census-edit-" + tc.key + ".example.com"},
				// A REAL lifecycle by default. CertificateUpdate reads
				// "lifecycle" with formValue (never optional), so a form
				// that omits it entirely posts "", which is not one of
				// CertificateLifecycles and fires its OWN error on every
				// case in this table -- an incidental failure this test
				// caught while writing it (see the mutation note above).
				// Each case's own extra overrides this for the one case
				// that means to test lifecycle itself.
				"lifecycle": {"active"},
			}
			for k, v := range tc.extra {
				form[k] = v
			}
			resp := h.post("/certificates/"+certID, form, true)
			b := body(t, resp)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
			}
			assertFieldError(t, b, tc.wantText, "update", tc.key)
		})
	}
}

// TestCertificateEditRefusalRespectsADeliberatelyClearedSANsField is round 2
// of the same WP-J9 whole-branch review: fixing the "sans" hook (above)
// introduced a NEW way to silently revert an operator's edit, through the
// exact mechanism finding 1 was closing.
//
// splitNames returns an unappended `var out []string` on empty input --
// nil -- and nil is FALSY in html/template regardless of why the slice is
// empty. certificatePage.SubmittedSANs was originally a bare []string, so
// `{{if .SubmittedSANs}}` could not tell "the operator cleared this
// textarea on purpose" from "nothing was submitted for this field at all":
// both are nil, both read as false, and the template fell back to
// .Certificate.SANs -- the STORED value. An operator who clears "also
// covers" and fails validation on a completely unrelated field (a bad
// fingerprint, say) got their deliberate deletion silently undone, with the
// error pointing somewhere else entirely.
//
// Fixed by making SubmittedSANs a *[]string and resolving it in Go
// (certificatePage.SANsToShow), where a nil pointer and a non-nil pointer to
// an empty slice are simply not the same value -- no truthiness involved.
//
// This is deliberately its OWN test, not a case squeezed into the
// table above: every case in that table drives its refusal through a
// key with a non-empty invalid value, or leaves "sans" untouched
// entirely, so none of them can exercise "submitted empty, refused on
// something else" at all.
func TestCertificateEditRefusalRespectsADeliberatelyClearedSANsField(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Seed a certificate that actually has SANs to lose.
	seedResp := h.post("/certificates", url.Values{
		"csrf_token": {h.csrfToken("/certificates")},
		"subject_cn": {"census-clear-sans-seed.example.com"},
		"sans":       {"extra.census-clear-sans-seed.example.com"},
	}, true)
	seedResp.Body.Close()
	redirect := seedResp.Header.Get("HX-Redirect")
	if redirect == "" {
		t.Fatalf("seeding the certificate did not redirect; response: %+v", seedResp)
	}
	certID := strings.TrimPrefix(redirect, "/certificates/")

	page := body(t, h.get("/certificates/"+certID, false))
	if !strings.Contains(page, "extra.census-clear-sans-seed.example.com") {
		t.Fatalf("the seeded certificate's SAN is not on its own page; seeding failed. Page: %s", page)
	}

	// Clear "sans" deliberately (submit it as "", not omit it), and fail
	// validation on a COMPLETELY UNRELATED field.
	resp := h.post("/certificates/"+certID, url.Values{
		"csrf_token":  {h.csrfToken("/certificates/" + certID)},
		"row_version": {versionInFirstForm(t, page)},
		"subject_cn":  {"census-clear-sans-seed.example.com"},
		"lifecycle":   {"active"},
		"sans":        {""},           // the deliberate deletion
		"fingerprint": {"not-hex-zz"}, // the unrelated failure
	}, true)
	b := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
	}
	assertFieldError(t, b, "must be hexadecimal", "update", "fingerprint")

	// NOT a blanket strings.Contains(b, theOldSAN) check: certificate_panel
	// ALSO renders a read-only "Covers" summary straight off .Certificate.SANs
	// (the row this failed write never touched), and that summary is CORRECT
	// to still show the stored name -- the write did not happen. The claim
	// under test is specifically about the EDIT FORM's own textarea, the one
	// field the operator's refused submission should be reflected in.
	textarea := ceSansTextareaContent(t, b)
	if strings.TrimSpace(textarea) != "" {
		t.Errorf("ce-sans should render empty (what the operator submitted), got %q", textarea)
	}
}

// ceSansTextareaContent extracts what is between <textarea id="ce-sans" ...>
// and </textarea>.
func ceSansTextareaContent(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<textarea id="ce-sans"[^>]*>(.*?)</textarea>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no ce-sans textarea on the page")
	}
	return m[1]
}

// assertFieldError requires the exact validation message for one key to
// appear inside a field-error element -- not merely somewhere in the body,
// and not merely `class="field-error"` with no text tying it to this case.
func assertFieldError(t *testing.T, body, wantText, path, key string) {
	t.Helper()
	if !strings.Contains(body, `class="field-error"`) {
		t.Errorf("refusing %q on %s carries no field-error anywhere in the "+
			"re-rendered form -- a silent refusal. Body: %s", key, path, body)
		return
	}
	if !strings.Contains(body, wantText) {
		t.Errorf("refusing %q on %s: the form carries SOME field-error, but "+
			"not this key's own message (%q) -- the hook for %q is likely "+
			"missing, and a different key's error is what satisfied the "+
			"generic check. Body: %s", key, path, wantText, key, body)
	}
}

// versionInFirstForm reads the first row_version token on a page. The
// certificate detail page carries exactly one editable row_version (the
// certificate itself); deployment forms below it carry none.
func versionInFirstForm(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`name="row_version" value="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the certificate edit form carries no row_version")
	}
	return m[1]
}
