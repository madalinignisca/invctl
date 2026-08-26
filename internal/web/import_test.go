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
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
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

// awaitImport follows the redirect a real import returns and polls the job page
// until it stops moving.
//
// The import is a BACKGROUND JOB now: the response is "queued, here is where to
// watch", not the outcome. A test that asserted on the immediate body would be
// asserting on a redirect stub -- the same trap the power tests fell into -- so
// this waits for the job the way the page does.
func (h *harness) awaitImport(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		// Not a queued job: a malformed file is still refused synchronously,
		// while the operator is looking at the form.
		return ""
	}
	loc := resp.Header.Get("Location")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		page := body(t, h.get(loc, false))
		// The polling attribute is present only while there is something to
		// poll for, so its absence IS the finished signal the browser uses.
		if !strings.Contains(page, `hx-trigger="every`) {
			return page
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("import at %s did not finish within 20s", loc)
	return ""
}

const goodFile = "parent,name,kind\n" +
	",imp-dc,site\n" +
	"imp-dc,imp-rack,rack\n"

func TestImportingAFileCreatesTheAssetsAndSaysSo(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	before := h.count(`SELECT COUNT(*) FROM asset`)
	page := h.awaitImport(t, h.upload(goodFile, false))

	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 2 {
		t.Errorf("created %d assets, want 2", got)
	}
	if !strings.Contains(page, "Imported") {
		t.Errorf("the job page does not say it succeeded:\n%s", page)
	}
	if !strings.Contains(page, "2 rows created") {
		t.Errorf("the job page does not say how many rows it created:\n%s", page)
	}
	// And the actor is the person who uploaded it, not the process that wrote
	// the rows -- captured at submit time and carried into the job.
	if got := h.lookup(`SELECT actor_kind FROM import_job ORDER BY created_at DESC LIMIT 1`); got != "user" {
		t.Errorf("import job actor_kind = %q, want \"user\": the audit trail names the "+
			"person who uploaded the file", got)
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
	page := h.awaitImport(t, h.upload(goodFile+"imp-dc,imp-rack-2,teleporter\n", false))

	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 0 {
		t.Errorf("%d assets survived a refused file", got)
	}
	if !strings.Contains(page, "Nothing was imported") {
		t.Errorf("the job page does not say the file was refused:\n%s", page)
	}
	// The line number is the whole point of the report, and it has to survive
	// being stored on the job and read back out.
	if !strings.Contains(page, ">4<") {
		t.Error("the report does not point at line 4, so the operator has to search the file")
	}
	if got := h.lookup(`SELECT status FROM import_job ORDER BY created_at DESC LIMIT 1`); got != "refused" {
		t.Errorf("job status = %q, want \"refused\" -- the file was read and answered no, "+
			"which is not the same as the import breaking", got)
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

// TestAnImportOutlivesTheRequestThatStartedIt is the property the background
// job exists for.
func TestAnImportOutlivesTheRequestThatStartedIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	before := h.count(`SELECT COUNT(*) FROM asset`)

	resp := h.upload(goodFile, false)
	loc := resp.Header.Get("Location")
	resp.Body.Close()

	// The request is answered immediately with somewhere to watch, NOT with the
	// outcome. That is the whole point: measured at 1.4ms a row, a full file
	// would sit past every proxy timeout between a browser and this process.
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/imports/") {
		t.Fatalf("upload returned %d to %q, want a redirect to a job page",
			resp.StatusCode, loc)
	}

	// The operator leaves. Nothing here touches the job again.
	h.logout()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if h.count(`SELECT COUNT(*) FROM import_job WHERE status = 'succeeded'`) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := h.count(`SELECT COUNT(*) FROM asset`) - before; got != 2 {
		t.Errorf("created %d assets after the session ended, want 2 -- the import has to "+
			"survive the operator closing the tab", got)
	}
}

// TestAJobInterruptedByARestartSaysSoRatherThanPollingForever covers the state
// nobody thinks about until it happens at three in the morning.
func TestAJobInterruptedByARestartSaysSoRatherThanPollingForever(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// A job stuck as this process found it: written directly, because the only
	// way to produce one legitimately is to kill the process mid-import.
	h.exec(`INSERT INTO import_job (id, kind, filename, actor, actor_kind, status,
	                                rows_total, rows_done, created, created_at)
	        VALUES (?, 'assets', 'orphan.csv', 'someone', 'user', 'running', 100, 43, 0, ?)`,
		"01931111-1111-7111-8111-111111111111", "2026-08-06T22:00:00Z")

	n, err := h.store.FailStaleImportJobs(context.Background())
	if err != nil {
		t.Fatalf("clearing stale jobs: %v", err)
	}
	if n != 1 {
		t.Fatalf("cleared %d stale jobs, want 1", n)
	}

	page := body(t, h.get("/imports/01931111-1111-7111-8111-111111111111", false))
	if strings.Contains(page, `hx-trigger="every`) {
		t.Error("the page still polls for a job nobody is running; it would ask for ever")
	}
	if !strings.Contains(page, "nothing was written") {
		t.Errorf("the page does not say the interrupted import wrote nothing. It was one "+
			"transaction and it went with the process, so there is no half-import to "+
			"reason about:\n%s", page)
	}
}

// TestAnImportInProgressDoesNotWedgeEveryOtherWrite is a regression test for a
// deadlock I shipped and found on the live demo.
//
// The first version wrote progress to db.Writer while the import ran. The
// SQLite writer pool is ONE connection, held by the import's transaction, so
// the progress update queued for a connection that could only be released by
// the thing it was reporting on. The import never finished and every other
// write in the process queued behind it until a restart.
//
// So: start an import, and while it is running, do an ordinary write through
// the ordinary form. If that ever hangs again, this fails instead of a demo
// doing it.
func TestAnImportInProgressDoesNotWedgeEveryOtherWrite(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Big enough to still be running when the next request arrives.
	var file strings.Builder
	file.WriteString("parent,name,kind\n,wedge-dc,site\n")
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&file, "wedge-dc,wedge-%04d,rack\n", i)
	}
	resp := h.upload(file.String(), false)
	resp.Body.Close()

	// An unrelated write, immediately, through the real form. It must not hang.
	done := make(chan int, 1)
	go func() {
		r := h.post("/teams", url.Values{
			"csrf_token": {h.csrfToken("/teams")},
			"code":       {"wedge-test"}, "name": {"Wedge Test"},
		}, false)
		r.Body.Close()
		done <- r.StatusCode
	}()

	select {
	case code := <-done:
		if code >= 500 {
			t.Errorf("an ordinary write during an import returned %d", code)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("an ordinary write hung while an import was running. The import holds " +
			"the single SQLite writer, so nothing it does may itself need that " +
			"connection -- which is exactly how the progress update deadlocked.")
	}
}

// --- WP-G1 Task 9: the import runner carries the submitter's permit ---
//
// These three go straight at store.ImportAssetsBatched rather than through
// the queued HTTP path. The real route is admin-only (see
// TestImportIsAdminOnlyOnBothVerbs above), so every submitter the HTTP layer
// can produce today already resolves to domain.AdministratorPermit -- there
// is no logged-in path yet that reaches this handler with anything narrower.
// What Task 9 is settling is the STORE method's contract for the day a
// narrower submitter exists (project owners import too, eventually), so
// these mint the permit by hand, the way internal/web/handlers/imports.go's
// a.Authz.Permit(user) call will for a real one.

func importSubmitter(id string) domain.Actor {
	return domain.Actor{ID: id, Name: id, Kind: domain.ActorKindUser}
}

// TestAnImportRunsUnderThePermitOfWhoeverSubmittedIt proves the created
// rows' change_log entries name the submitter, not this process.
func TestAnImportRunsUnderThePermitOfWhoeverSubmittedIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	submitter := importSubmitter("import-submitter-1")
	permit := domain.AdministratorPermit(submitter)

	report, err := h.store.ImportAssetsBatched(ctx, permit,
		[]store.AssetImportRow{{Line: 2, Name: "task9-submitter-asset", Kind: "site"}}, nil)
	if err != nil {
		t.Fatalf("ImportAssetsBatched: %v", err)
	}
	if len(report.Created) != 1 {
		t.Fatalf("created %d rows, want 1: %+v", len(report.Created), report.Problems)
	}

	// Scoped to THIS row by name, not "most recent": the test clock is
	// shared with the seed fixture, so change_log.at ties do not reliably
	// order a fresh row after seeded ones.
	actorID := h.lookup(`SELECT actor FROM change_log WHERE entity_type = 'asset'
	                      AND entity_id = (SELECT id FROM asset WHERE name = 'task9-submitter-asset')`)
	actorKind := h.lookup(`SELECT actor_kind FROM change_log WHERE entity_type = 'asset'
	                       AND entity_id = (SELECT id FROM asset WHERE name = 'task9-submitter-asset')`)
	if actorID != submitter.ID {
		t.Errorf("change_log.actor = %q, want the submitter's id %q -- the import runs "+
			"under whoever uploaded the file, not the process running it", actorID, submitter.ID)
	}
	if actorKind != domain.ActorKindUser {
		t.Errorf("change_log.actor_kind = %q, want %q", actorKind, domain.ActorKindUser)
	}
}

// TestAnImportCannotCreateAnAssetOutsideTheSubmittersScope is the mutation
// target: have the runner mint domain.AdministratorPermit(work.actor) instead
// of the captured permit and this goes red, because every row would then
// succeed regardless of scope.
//
// A ScopedPermit cannot yet authorize the CREATE of a not-yet-existing row at
// all: entities is checked by id, and an asset's id is a fresh UUIDv7 minted
// inside ImportAssetsBatched, unknowable to any caller ahead of the write --
// see ScopedPermit's own doc comment in internal/domain/role.go, and Task
// 13/14, which is what will let a project owner's create actually land in
// their own project. So today every row submitted under a ScopedPermit is,
// correctly and safely, outside the submitter's scope: this test proves that
// refusal is a PER-ROW outcome (report.Problems, not a hard error) and that
// the batch still finishes rather than aborting.
func TestAnImportCannotCreateAnAssetOutsideTheSubmittersScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	submitter := importSubmitter("import-submitter-2")
	// Covers project "P" in name only: entities is nil, so nothing this
	// permit could ever be asked to authorize a CREATE for is in it.
	permit := domain.ScopedPermit(submitter, []string{"P"}, nil)

	rows := []store.AssetImportRow{
		{Line: 2, Name: "task9-scope-a", Kind: "site"},
		{Line: 3, Name: "task9-scope-b", Kind: "site"},
	}
	report, err := h.store.ImportAssetsBatched(ctx, permit, rows, nil)
	if err != nil {
		t.Fatalf("ImportAssetsBatched returned an error rather than a per-row outcome: %v", err)
	}
	if len(report.Created) != 0 {
		t.Errorf("created %v, want nothing created under a permit that covers none of it",
			report.Created)
	}
	if len(report.Problems) != len(rows) {
		t.Fatalf("got %d per-row problems, want %d (one per refused row): %+v",
			len(report.Problems), len(rows), report.Problems)
	}
	for _, p := range report.Problems {
		if !strings.Contains(p.Message, "scope") {
			t.Errorf("problem for line %d does not name authorization as the reason: %q",
				p.Line, p.Message)
		}
	}
	if n := h.count(`SELECT COUNT(*) FROM asset WHERE name IN ('task9-scope-a', 'task9-scope-b')`); n != 0 {
		t.Errorf("%d rows were created despite being refused -- the per-row outcome must "+
			"mean the row was not written, not just that the report says so", n)
	}
}

// TestAPermitCapturedAtSubmitIsNotRefreshedMidRun documents the decision:
// once a permit is minted for a submission, it authorizes that submission's
// writes for as long as the job runs, even if the submitter's role changes
// in the database before the job finishes.
//
// This is the same choice already made for the actor captured alongside it
// (see importWork's doc comment): the authorization decision was made the
// moment the operator pressed submit, and it is defensible on its own terms
// -- an operator watching a page they just acted on expects that action to
// go through. The alternative, re-deriving the permit from the database
// before every batch or every row, is worse: it would mean a demotion that
// lands mid-run silently changes what an already-running job is allowed to
// do, mid-file, which is a stranger and harder-to-reason-about failure mode
// than "the decision was made when the button was pressed".
//
// domain.AdministratorPermit does not consult the database at all -- it is a
// static decision baked in at mint time (see its doc comment in
// internal/domain/role.go) -- so demoting the underlying app_user row after
// minting and confirming the import still succeeds is a direct proof of the
// property, not an inference from it.
func TestAPermitCapturedAtSubmitIsNotRefreshedMidRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	admin, err := h.store.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("loading the seeded admin: %v", err)
	}
	subject, err := domain.NewAppUser(store.NewID(), "task9-demoted-submitter",
		domain.UserSourceLocal, h.store.Now())
	if err != nil {
		t.Fatalf("NewAppUser: %v", err)
	}
	if err := h.store.CreateUser(ctx, domain.UserActor(admin), subject); err != nil {
		t.Fatalf("creating the subject user: %v", err)
	}
	// The seeded "admin" account is an Administrator by the INV_ADMIN_USERS
	// break-glass list, not by app_user.role -- so without this, subject
	// would become the ONLY role-column Administrator, and demoting it below
	// would be refused by the last-administrator guard for an unrelated
	// reason. Granting admin the role column too, harmlessly, keeps this
	// test about the permit-freshness property rather than that guard.
	if err := h.store.SetUserRole(ctx, domain.AdministratorPermit(domain.UserActor(admin)), admin.ID, domain.RoleAdministrator); err != nil {
		t.Fatalf("granting the seeded admin the role column: %v", err)
	}
	if err := h.store.SetUserRole(ctx, domain.AdministratorPermit(domain.UserActor(admin)), subject.ID, domain.RoleAdministrator); err != nil {
		t.Fatalf("granting administrator: %v", err)
	}

	// Captured NOW, while subject is still an Administrator -- this is the
	// permit the import will run under, exactly as importWork.permit is
	// minted once at submit and never touched again.
	permit := domain.AdministratorPermit(domain.UserActor(subject))

	// Demoted BEFORE the import runs. A permit re-derived from the database
	// at write time would see this and refuse; the captured one must not.
	if err := h.store.SetUserRole(ctx, domain.AdministratorPermit(domain.UserActor(admin)), subject.ID, domain.RoleObserver); err != nil {
		t.Fatalf("demoting the subject: %v", err)
	}

	report, err := h.store.ImportAssetsBatched(ctx, permit,
		[]store.AssetImportRow{{Line: 2, Name: "task9-stale-permit-asset", Kind: "site"}}, nil)
	if err != nil {
		t.Fatalf("ImportAssetsBatched: %v", err)
	}
	if len(report.Created) != 1 {
		t.Errorf("created %d rows, want 1 -- a permit captured before the demotion must "+
			"still authorize the write it was minted for: %+v", len(report.Created), report.Problems)
	}
}
