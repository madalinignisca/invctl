// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// upload posts a CSV to the import route the way a browser would.
func (h *harness) upload(csv string, dryRun bool) *http.Response {
	h.t.Helper()
	token := h.csrfToken("/import/assets")

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	if err := form.WriteField("csrf_token", token); err != nil {
		h.t.Fatalf("writing field: %v", err)
	}
	if dryRun {
		if err := form.WriteField("dry_run", "1"); err != nil {
			h.t.Fatalf("writing field: %v", err)
		}
	}
	part, err := form.CreateFormFile("file", "assets.csv")
	if err != nil {
		h.t.Fatalf("creating file part: %v", err)
	}
	if _, err := part.Write([]byte(csv)); err != nil {
		h.t.Fatalf("writing file part: %v", err)
	}
	if err := form.Close(); err != nil {
		h.t.Fatalf("closing form: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.url("/import/assets"), &buf)
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", h.server.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("POST /import/assets: %v", err)
	}
	return resp
}

const goodFile = "parent,name,kind\n" +
	",imp-dc,site\n" +
	"imp-dc,imp-rack,rack\n"

func TestImportingAFileCreatesTheAssetsAndSaysSo(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	before := h.count(`SELECT COUNT(*) FROM asset`)
	resp := h.upload(goodFile, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, page)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 2 {
		t.Errorf("created %d assets, want 2", got)
	}
	for _, want := range []string{"imp-dc", "imp-dc/imp-rack"} {
		if !strings.Contains(page, want) {
			t.Errorf("the result page does not list %q; an import that does not say what "+
				"it created leaves the operator to go and look", want)
		}
	}
}

func TestAPreviewImportWritesNothingAndSaysThatToo(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	before := h.count(`SELECT COUNT(*) FROM asset`)
	resp := h.upload(goodFile, true)
	page := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 0 {
		t.Errorf("a preview created %d assets", got)
	}
	// The page must SAY it wrote nothing. A preview that renders the same
	// "Imported" heading as a real run is worse than no preview: the operator
	// walks away believing the estate changed.
	if !strings.Contains(page, "nothing was written") {
		t.Errorf("the preview page does not say nothing was written:\n%s", page)
	}
	if !strings.Contains(page, "imp-dc/imp-rack") {
		t.Error("the preview does not list what it would have created")
	}
}

func TestARefusedFileImportsNothingAndReturns422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	before := h.count(`SELECT COUNT(*) FROM asset`)
	// Two good rows and then one with an unknown kind, so anything that leaks
	// through leaves the first two behind.
	resp := h.upload(goodFile+"imp-dc,imp-rack-2,teleporter\n", false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 -- a refused form is 422 in this codebase, "+
			"not 200 with the bad news in the body", resp.StatusCode)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 0 {
		t.Errorf("%d assets survived a refused file", got)
	}
	if !strings.Contains(page, "Nothing was imported") {
		t.Errorf("the page does not say the file was refused:\n%s", page)
	}
	// The line number is the whole point of the report.
	if !strings.Contains(page, ">4<") {
		t.Error("the report does not point at line 4, so the operator has to search the file")
	}
}

func TestAMisspelledColumnIsRefusedRatherThanDropped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	before := h.count(`SELECT COUNT(*) FROM asset`)
	resp := h.upload("parent,name,kind,lifecyle\n,imp-dc,site,active\n", false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 0 {
		t.Errorf("%d assets were created from a file with an unknown column", got)
	}
	if !strings.Contains(page, "lifecyle") {
		t.Errorf("the refusal does not quote the column it did not recognise. Silently "+
			"ignoring it would create the asset with the wrong lifecycle and report "+
			"success:\n%s", page)
	}
}

// TestImportIsAdminOnlyOnBothVerbs covers the page as well as the action.
//
// One harness, two logins -- not two harnesses. Two newHarness calls seed two
// separate databases, and a test written that way passes whatever the
// authorization rules say, which this project has already shipped three times.
func TestImportIsAdminOnlyOnBothVerbs(t *testing.T) {
	h := newHarness(t)

	h.login("viewer", "viewer-password")
	for _, path := range []string{"/import/assets"} {
		resp := h.get(path, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s as a read-only user returned %d, want 403. The page is a "+
				"write tool; rendering it offers a form whose only outcome is a refusal.",
				path, resp.StatusCode)
		}
	}

	// And the same session cannot reach the action either. Checked separately
	// because a hidden page and a protected action are different guarantees.
	before := h.count(`SELECT COUNT(*) FROM asset`)
	h.logout()
	h.login("admin", "admin-password")
	token := h.csrfToken("/import/assets")
	h.logout()
	h.login("viewer", "viewer-password")

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	_ = form.WriteField("csrf_token", token)
	part, _ := form.CreateFormFile("file", "assets.csv")
	_, _ = part.Write([]byte(goodFile))
	_ = form.Close()

	req, err := http.NewRequest(http.MethodPost, h.url("/import/assets"), &buf)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", h.server.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Error("a read-only user imported a file")
	}
	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 0 {
		t.Errorf("a read-only user created %d assets", got)
	}
}
