# HTMX swap targets — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every `hx-post` element in `web/templates` declares `hx-target` **and** `hx-swap`
on itself, so a refusal lands where the operator can see it instead of nesting a whole panel
inside the control they clicked. Three handlers that answer a validation failure by
re-rendering a *list* start re-rendering *the form*, because targeting them without that
makes the refusal invisible rather than merely ugly. A new static census keeps it true.

**Architecture:** No new routes, no schema change, no dependency. Three handlers grow a
second named region (`certificate_form`, `team_create_form`, `user_form`) reached through the
same `render.Respond` they already use. `handleStoreError`'s `ErrInvalid` branch stops
destroying the caller's form and becomes an out-of-band flash with `HX-Reswap: none`. The
remaining 35 elements are markup: ten name the partial their handler re-renders, twenty-eight
declare `hx-target="this" hx-swap="none"` because their handler has no in-band body on any
path. `internal/web/hx_target_test.go` is the census, landing last and starting empty.

**Tech Stack:** Go 1.26, `html/template` + `go:embed`, `net/http.ServeMux`, HTMX 2.0.4
vendored at `web/static/htmx.min.js`, Alpine 3.x, SQLite (`modernc.org/sqlite`) and
PostgreSQL. Playwright for the browser half (`tests/e2e/`, opt-in, never part of `make test`).

**Spec:** `docs/htmx-swap-targets-design.md` — **the binding authority. Where this plan and
the spec disagree, the spec wins**, except on the five points listed in "Where this plan
departs from the spec" below, each of which was signed off before implementation began.

## Global Constraints

Copied verbatim from the spec's "Global constraints" section. Every task's requirements
implicitly include these.

- **Server-rendered HTML only. No JSON to the UI.** Handlers return HTML fragments.
- Stack locked: `net/http.ServeMux`, `html/template` embedded via `go:embed`, HTMX
  2.x and Alpine.js 3.x vendored into `web/static` — never a CDN.
- **Alpine is local UI state only** — dropdowns, modals, tab selection,
  disable-on-submit. It does not fetch, does not hold domain state, does not talk to
  the server. No inline `<script>` beyond `x-data`.
- **No business logic in a template.** Escaping is `html/template`'s job; never
  concatenate HTML, never `template.HTML` on anything derived from user input.
- **A partial must be renderable standalone.** If it only works once its parent has
  rendered, it is wrong. (Rule 2 above is a direct consequence.)
- **Validation failure returns HTTP 422 with the form partial re-rendered** — never a
  200 with an error buried in the body.
- Every mutating handler branches on `HX-Request` through `render.Respond`; success
  redirects with `HX-Redirect`, never a 302.
- **Every non-GET route is behind CSRF and `RequireWrite`** (or
  `RequireAdministrator` where the surface genuinely needs one). Nothing in this work
  adds, removes or moves a route.
- **AGPL-3.0-only header on every new file** (`.go`, `.html`, `.css`, `.js`), with a
  **blank line** before the package clause in Go or the licence becomes the package
  doc. `internal/license` fails otherwise.
- **No new dependency.**
- **`make test` green on both engines.** `go test ./...` alone silently skips the
  Postgres half.

Operational additions, from `CLAUDE.md` and this repo's build notes:

- `make test` brings the Postgres container up itself and carries the 30m timeout the
  suite needs. **Capture its exit code directly, never through a pipeline** — the status
  you read from `make test | tail` is `tail`'s, and a red gate reports green.
- `make lint` needs `/usr/local/go/bin` and `$HOME/go/bin` on PATH.
- One commit per step, message saying WHY, ending with
  `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- **Every commit leaves the tree green**, not merely building. This repo has censuses that
  read the live route table and the template tree; a commit that compiles and fails
  `TestEveryRespondNamesATemplateThatResolves` is a failed step, not a formatting detail.

## Where this plan departs from the spec, and why

Five rulings taken to Gabriel on 2026-09-16 before a line was written. Each is a fact about
the code the spec did not have.

1. **`UserCreate` is a third instance of the certificates/teams defect** and is in scope as
   Task 3. `users.go:186` re-renders `user_list_panel`; `user_form` is a separate
   `{{define}}` included at `pages/user_list.html:25`. `user_form`'s root already carries
   `id="user-form"`, so only the handler moves.
2. **Ruling 1's "drops a `ListCertificates` query" is struck.** `Respond`'s non-HTMX branch
   renders the whole page, and `pages/certificate_list.html` dereferences `.Certificates`
   and `.Filter`. A reduced struct would show "No certificates match." to a JavaScript-off
   operator. The query stays; only the region name changes.
3. **The 38 split into 10 and 28.** Ten elements have a handler that can emit a 422 fragment
   and name it. Twenty-eight have a handler that answers only `render.Redirect`,
   `setFlash`+`Redirect` or `handleStoreError` on every path, and they declare
   `hx-target="this" hx-swap="none"` — a declaration, not an exemption (spec Rule 1), argued
   from the handler per element (spec Rule 4). `hxTargetExempt` is `{}` at merge regardless.
4. **The browser-evidence list changes.** Health override clear is dropped (its refusal is
   flash-and-redirect; it is on `refusalFlashExceptions` by name, has no form and no typed
   value, and its target is `this`). User create replaces it. Team reassign-retire is driven
   with `form.noValidate = true`, because `<select required>` blocks the only non-destructive
   refusal and choosing a valid target permanently retires a team.
5. **Spec counts corrected:** 74 `hx-target` / 75 `hx-swap` tree-wide, not 78/78. The 52 are
   **verified pairwise clean** — 0 carry a target without a swap — so none joins scope.

## File Structure

**Create:**
- `internal/web/hx_target_test.go` — the census (Task 7)
- `tests/e2e/specs/refusal-targets.spec.js` — the browser half (Task 8)

**Modify:**
- `internal/web/handlers/certificates.go` — `renderCertificateList` gains a region parameter;
  `CertificateCreate` refuses into `certificate_form`
- `internal/web/handlers/teams.go` — same shape for `renderTeamList` / `TeamCreate`
- `internal/web/handlers/users.go` — same shape for `renderUserList` / `UserCreate`
- `internal/web/handlers/app.go` — `handleStoreError`'s `ErrInvalid` branch
- `internal/web/handlers/savedviews.go:128-142` — the comment describing that branch
- `internal/store/vocabulary.go:311-317` — the comment describing that branch
- `web/templates/partials/certificates.html` — `id="certificate-form"`, 6 elements
- `web/templates/pages/team_list.html` — the create form moves out
- `web/templates/partials/teams.html` — gains `team_create_form`, 4 elements
- `web/templates/partials/user_form.html` — 1 element
- `web/templates/pages/asset_detail.html` (6), `pages/net_group_detail.html` (4),
  `partials/costs.html` (3), `pages/prefix_list.html` (2), `pages/bundle_detail.html` (1),
  `pages/fhrp_detail.html` (1), `pages/network_list.html` (1), `pages/service_detail.html` (1),
  `partials/health.html` (1), `partials/journal.html` (1), `pages/catalogue.html` (1),
  `pages/environment_list.html` (1), `partials/custom_field_form.html` (1),
  `partials/rows.html` (1), `partials/tag_form.html` (1)
- `internal/web/certificates_test.go`, `internal/web/teams_test.go`,
  `internal/web/users_test.go` — the new behavioural tests
- `docs/ROADMAP.md`, `docs/E2E.md`

## The 38, resolved

Line numbers are as at `bc74a71` and **shift as tasks land** — each element is keyed by file
and by the action it posts to, which does not shift. Class 1 targets were read off the
handler, named here, and each was checked for failure mode (c): the id is present on every
render that can show the element.

### Class 1 — the handler can emit a 422 fragment (10)

| element | route | handler → region | target | (c) check |
|---|---|---|---|---|
| `partials/certificates.html:66` | `POST /certificates` | Task 1 → `certificate_form` | `#certificate-form` | form and id are the same `{{define}}` root |
| `partials/certificates.html:203` | `…/assets/{id}/retire` | `afterCertificateWrite`→`renderCertificate`→`certificate_panel` | `#certificate` | `#certificate` is `certificate_panel`'s unconditional root (l.144); the form is inside `{{if $.IsAdmin}}` *within* it |
| `partials/certificates.html:219` | `…/services/{id}/retire` | same | `#certificate` | same |
| `partials/certificates.html:244` | `…/{id}/assets` | `afterCertificateWrite`→`certificate_panel` | `#certificate` | same |
| `partials/certificates.html:262` | `…/{id}/services` | same | `#certificate` | same |
| `partials/certificates.html:282` | `POST /certificates/{id}` | `CertificateUpdate`→`renderCertificate` | `#certificate` | same |
| team create (moves to `partials/teams.html`) | `POST /teams` | Task 2 → `team_create_form` | `#team-create-form` | form and id are the same `{{define}}` root |
| `partials/teams.html:173` | `POST /teams/{id}` | `TeamUpdate`→`renderTeam`→`team_panel` | `#team` | `#team` is `team_panel`'s unconditional root (l.52) |
| `partials/teams.html:272` | `…/{id}/reassign-retire` | `TeamReassignAndRetire`→`renderTeamRetireConfirm` | `#team-retire-confirm` | root of the `{{define}}` (l.222), outside the `{{if eq .Counts.Total 0}}` the form sits in |
| `partials/user_form.html:17` | `POST /users` | Task 3 → `user_form` | `#user-form` | id already on the `{{define}}` root (l.15) |

### Class 2 — no in-band body on any path (28) → `hx-target="this" hx-swap="none"`

| element | route | what the handler actually answers |
|---|---|---|
| `pages/asset_detail.html:55` | `/assets/{id}/retire` | `AssetRetire`: flash+`Redirect` \| `handleStoreError` |
| `pages/asset_detail.html:182` | `…/apply-template` | `AssetApplyTemplate`: flash+`Redirect` \| `handleStoreError` |
| `pages/asset_detail.html:279` | `/addresses/{id}/retire` | `IPAddressRetire`: flash+`Redirect` \| `handleStoreError` |
| `pages/asset_detail.html:376` | `/interfaces/{id}/retire` | `InterfaceRetire`: flash+`Redirect` \| `handleStoreError` |
| `pages/asset_detail.html:384` | `/links/{id}/retire` | `LinkRetire`: `Redirect` \| `handleStoreError` |
| `pages/asset_detail.html:708` | `/assets/{id}/storage` | `AssetStorageClaim`: flash+`Redirect` on every branch |
| `pages/bundle_detail.html:113` | `/bundles/{id}/retire` | `BundleRetire`: flash+`Redirect` |
| `pages/fhrp_detail.html:211` | `/redundancy/{id}/retire` | `FHRPRetire`: flash+`Redirect` |
| `pages/net_group_detail.html:46` | `…/members/{assetID}/retire` | `NetworkGroupMemberRetire`: flash+`Redirect` |
| `pages/net_group_detail.html:83` | `…/uplinks/{id}/retire` | `NetworkUplinkRetire`: flash+`Redirect` |
| `pages/net_group_detail.html:123` | `…/attachments/{id}/retire` | `NetworkAttachmentRetire`: flash+`Redirect` |
| `pages/net_group_detail.html:250` | `/network/groups/{id}/retire` | `NetworkGroupRetire`: flash+`Redirect` |
| `pages/network_list.html:171` | `/network/anchors/{id}/retire` | `NetworkAnchorRetire`: flash+`Redirect` |
| `pages/prefix_list.html:105` | `/prefixes/{id}/retire` | `PrefixRetire`: flash+`Redirect` |
| `pages/prefix_list.html:187` | `/ip-ranges/{id}/retire` | `IPRangeRetire`: flash+`Redirect` |
| `pages/service_detail.html:197` | `/endpoints/{id}/retire` | `EndpointRetire`: flash+`Redirect` |
| `partials/costs.html:183` | `{{$.Action}}/{id}/reprice` | `afterCostWrite`: flash+`Redirect` (already on `refusalFlashExceptions`) |
| `partials/costs.html:236` | `{{$.Action}}/{id}/retire` | same |
| `partials/costs.html:253` | `{{.Action}}` (add) | same |
| `partials/health.html:167` | `/overrides/{id}/clear` | `HealthOverrideClear`: flash+`Redirect` (already on `refusalFlashExceptions`) |
| `partials/journal.html:48` | `…/journal/{id}/retire` | `JournalRetire`: flash+`Redirect` |
| `partials/teams.html:235` | `/teams/{id}/retire` | `TeamRetire`: `Redirect` \| `handleStoreError` |
| `partials/teams.html:298` | `/teams/{id}/retire` | same |
| `pages/catalogue.html:354` | `…/components/{id}/retire` | `DeviceTypeComponentRetire`: flash+`Redirect` |
| `pages/environment_list.html:95` | `/environments/{id}/retire` | `EnvironmentRetire`: flash+`Redirect` |
| `partials/custom_field_form.html:93` | `/custom-fields/{id}/retire` | `CustomFieldRetire`: flash+`Redirect` |
| `partials/rows.html:158` | `/dependencies/{id}/retire` | `DependencyRetire`: flash+`Redirect` |
| `partials/tag_form.html:59` | `/tags/{id}/retire` | `TagRetire`: flash+`Redirect` |

10 + 28 = 38.

---

### Task 0: Re-measure the census before changing anything

Nothing in this task is committed. It exists so the numbers this plan rests on are measured
today rather than inherited from a file written yesterday.

- [ ] **Step 1: Re-run the pairwise scan**

```bash
cd /home/gabriel/apps/infra-inventory
mkdir -p /tmp/claude-1000/hx && cat > /tmp/claude-1000/hx/scan.py <<'PY'
import re, glob, os
root='web/templates'
files=[]
for d in ['layouts','pages','partials']:
    files += sorted(glob.glob(os.path.join(root,d,'*.html')))
tagre=re.compile(r'<(form|button)\b', re.I)
for f in files:
    src=open(f).read()
    lines=src.split('\n'); offs=[]; n=0
    for ln,l in enumerate(lines,1):
        offs.append((n,ln)); n+=len(l)+1
    def lineof(pos):
        lo=1
        for o,ln in offs:
            if o<=pos: lo=ln
            else: break
        return lo
    for m in tagre.finditer(src):
        j=src.find('>', m.start())
        if j<0: continue
        tag=src[m.start():j+1]
        if 'hx-post=' not in tag: continue
        t=re.search(r'hx-target="([^"]*)"', tag)
        s=re.search(r'hx-swap="([^"]*)"', tag)
        print(f'{f}:{lineof(m.start())}\t{m.group(1)}\ttarget={t.group(1) if t else "-"}\tswap={s.group(1) if s else "-"}')
PY
python3 /tmp/claude-1000/hx/scan.py > /tmp/claude-1000/hx/before.txt
wc -l < /tmp/claude-1000/hx/before.txt                       # expect 90
grep -c 'target=-' /tmp/claude-1000/hx/before.txt            # expect 38
grep -v 'target=-' /tmp/claude-1000/hx/before.txt | grep -c 'swap=-'   # expect 0
```

Expected: `90`, `38`, `0`. **The third number is the one the spec could not answer.** If it is
not 0, those elements join the class-1/class-2 triage above and this plan's element table is
short — stop and raise it rather than patching them in silently.

Keep `before.txt`. Task 6 diffs against it.

- [ ] **Step 2: Prove the `ErrInvalid` vector Task 4 needs, BEFORE changing the code**

Task 4 rewrites `handleStoreError`'s `ErrInvalid` branch. A test for it is worthless unless
the request it sends actually reaches that branch — and almost every handler in this package
catches `domain.ErrInvalid` locally (`bulkApplyTag`, `OwnershipAssign`, `AssetStorageClaim`,
`TeamReassignAndRetire` all do). Confirm the vector against the **unmodified** code:

```bash
cat > /tmp/claude-1000/hx/vector_test.go <<'GO'
package web_test
// TEMPORARY. Delete after running; it exists to prove a request reaches
// handleStoreError's ErrInvalid branch before that branch is rewritten.
import ("net/url";"testing";"github.com/google/uuid")
func TestVectorReachesErrInvalid(t *testing.T) {
	h := newHarness(t); h.login("admin", "admin-password")
	certID := h.lookup(`SELECT id FROM certificate LIMIT 1`)
	if certID == "" { t.Fatal("no certificate in the fixture") }
	resp := h.post("/certificates/"+certID+"/services", url.Values{
		"csrf_token": {h.csrfToken("/certificates/" + certID)},
		"service_id": {uuid.NewString()},   // no such service: a foreign-key violation
	}, true)
	t.Fatalf("status=%d body=%q reswap=%q", resp.StatusCode, body(t, resp),
		resp.Header.Get("HX-Reswap"))
}
GO
cp /tmp/claude-1000/hx/vector_test.go internal/web/vector_test.go
make test 2>&1 | grep -A2 TestVectorReachesErrInvalid ; rm internal/web/vector_test.go
```

Expected today: `status=422 body="That request was not valid.\n" reswap=""`.

`deployCertificate` (`store/certificates.go:508`) inserts straight into `certificate_service`;
an unknown `service_id` is a foreign-key violation, which `translateWriteErr`
(`store/store.go:387`) maps to `domain.ErrInvalid`, and `afterCertificateWrite`'s
`validationErrors` check does not recognise a wrapped sentinel, so it falls through. SQLite
enforces the key because `foreign_keys = ON` is set on every connection (CLAUDE.md).

**If it comes back 403 or 404**, the store authorises the entity before inserting and this
vector is wrong — find another and record which, rather than writing a test that passes for
the wrong reason.

---

### Task 1: Certificates — the create form re-renders itself

The handler is the defect. `CertificateCreate` answers a validation failure with
`renderCertificateList(…, 422, errs, spec)`, which `Respond`s **`certificate_list_panel`** —
the list. The form that renders `.Errors` and re-fills `.Spec` is `certificate_form`, included
separately by `pages/certificate_list.html:50`. CLAUDE.md is literal: *"Validation errors
re-render **the form partial**."*

This lands **before any of the 38 gets a target**, because targeting the create form at
`#certificate-list` replaces the list cleanly, never touches the form, and makes the refusal
completely invisible — strictly worse than today's mangled nesting.

**Files:** `internal/web/handlers/certificates.go`, `web/templates/partials/certificates.html`,
`internal/web/certificates_test.go`

**Interfaces:**
- `renderCertificateListPage(w, r, status, errs, spec, region string)` — assembles the page
  once, renders one of its two swappable regions
- `renderCertificateList` and `refuseCertificateCreate` are the two thin callers

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/certificates_test.go`:

```go
// TestACertificateRefusalRerendersTheFormAndNotTheList.
//
// POSTED WITH HX-Request TRUE, and that is the whole point. The existing case in
// TestCertificateCreateAndTheValidationPath posts with htmx=false, so it gets the
// whole page back -- which contains the form AND the list, so the wrong region
// name is invisible from it. That test has passed throughout the life of the bug.
//
// What would be true if this were broken: the 422 body would be the list panel.
// Once the form declares hx-target="#certificate-form", htmx would swap a list
// into a form-shaped hole, or -- if the target were #certificate-list instead --
// redraw the list, leave the form untouched, and show the operator nothing at
// all. Both are silent refusals, which is the failure this work package exists
// to remove.
func TestACertificateRefusalRerendersTheFormAndNotTheList(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/certificates", url.Values{
		"csrf_token": {h.csrfToken("/certificates")},
		"subject_cn": {"backwards.example.com"},
		"not_before": {"2030-01-01"},
		"not_after":  {"2020-01-01"}, // THE FAILURE: expiry before validity
	}, true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
	}
	if !strings.Contains(b, `id="certificate-form"`) {
		t.Errorf("the 422 body is not the create form. The form declares "+
			"hx-target=\"#certificate-form\", so this lands nowhere. Body: %s", b)
	}
	if strings.Contains(b, `id="certificate-list"`) {
		t.Errorf("the 422 body is the LIST panel -- the pre-existing deviation "+
			"this task exists to fix. Body: %s", b)
	}
	if !strings.Contains(b, `class="field-error"`) {
		t.Errorf("the re-rendered form carries no field error, so the operator is "+
			"handed back a form that looks accepted. Body: %s", b)
	}
	if !strings.Contains(b, "backwards.example.com") {
		t.Errorf("the re-rendered form lost the subject that was typed. Body: %s", b)
	}
}

// TestACertificateCreateStillRedirectsOnSuccess is the other half, and it is the
// evidence behind this whole work package's claim that success paths cannot
// change.
//
// render.Redirect answers an HX-Request with 204 + HX-Redirect. htmx 2.0.4
// handles that header in handleAjaxResponse and RETURNS before it resolves a
// target or picks a swap style -- so no value of hx-target or hx-swap can reach
// a successful create. Asserted here rather than argued from htmx's
// documentation, because "the docs say so" is what this repo's own comments warn
// against.
func TestACertificateCreateStillRedirectsOnSuccess(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/certificates", url.Values{
		"csrf_token": {h.csrfToken("/certificates")},
		"subject_cn": {"target-sweep.example.com"},
	}, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a successful create answered %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); !strings.HasPrefix(got, "/certificates/") {
		t.Errorf("HX-Redirect = %q, want /certificates/<id>. Without it htmx would "+
			"swap the response body into #certificate-form and the operator would "+
			"sit on the list looking at a form that appears unsubmitted", got)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/web/ -run 'TestACertificateRefusal|TestACertificateCreateStillRedirects' -count=1
```

`TestACertificateRefusal…` must fail on `id="certificate-form"` being absent **and** on
`id="certificate-list"` being present. `TestACertificateCreateStillRedirects…` must already
pass — it is a regression guard, not a red-to-green step, and observing it green now is what
makes it evidence later.

- [ ] **Step 3: Give `certificate_form` a wrapping id**

`web/templates/partials/certificates.html:64`, replace `<div class="panel">` with:

```html
{{define "certificate_form"}}
{{/* A STABLE ID BECAUSE THIS PARTIAL IS ITS OWN REFUSAL TARGET, not decoration.
     CertificateCreate used to answer a validation failure by re-rendering
     certificate_list_panel -- the LIST -- which does not contain this form, so
     the errors and the re-filled values below were rendered into a fragment
     nobody could see. CLAUDE.md: "Validation errors re-render the form partial."
     partials/user_row.html:59 is the shape. */}}
<div class="panel" id="certificate-form">
```

And the form open tag (`:66`):

```html
  <form class="panel-body" method="post" action="/certificates" hx-post="/certificates"
        hx-target="#certificate-form" hx-swap="outerHTML">
```

- [ ] **Step 4: Split the render path**

`internal/web/handlers/certificates.go`, replace `renderCertificateList` (l.55-80) with:

```go
// renderCertificateListPage assembles the certificates page and renders ONE of
// its two swappable regions: the list, or the create form.
//
// THE QUERY IS NOT OPTIONAL ON THE REFUSAL PATH, and the design document's
// suggestion that a form-only refusal "drops a ListCertificates query" is wrong
// -- ruled on 2026-09-16. Respond renders the WHOLE PAGE when the request is
// not an HX-Request, and pages/certificate_list.html dereferences .Certificates
// and .Filter. Handing it a struct with an empty list would show "No
// certificates match." to a JavaScript-off operator looking at a populated
// estate. One extra read on a path that only runs when somebody typed something
// wrong is the correct trade.
func (a *App) renderCertificateListPage(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.CertificateSpec, region string) {

	q := r.URL.Query()
	filter := store.CertificateFilter{
		Query:  q.Get("q"),
		Host:   q.Get("host"),
		TeamID: q.Get("team"),
	}
	certificates, err := a.Store.ListCertificates(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	teams, roles := a.responsibilityOptions(r)

	a.Render.Respond(w, r, status, "certificate_list", region, certificateListPage{
		Base:         a.base(r, "Certificates", "certificates"),
		Errors:       orEmpty(errs),
		Certificates: certificates,
		Teams:        teams,
		Roles:        roles,
		Spec:         spec,
		Filter:       filter,
	})
}

// renderCertificateList is a read: the list is what changed.
func (a *App) renderCertificateList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.CertificateSpec) {
	a.renderCertificateListPage(w, r, status, errs, spec, "certificate_list_panel")
}

// refuseCertificateCreate is a refusal: THE FORM is what changed, and it is the
// only place .Errors and .Spec are rendered. See certificate_form's own comment.
func (a *App) refuseCertificateCreate(w http.ResponseWriter, r *http.Request,
	errs map[string]string, spec domain.CertificateSpec) {
	a.renderCertificateListPage(w, r, http.StatusUnprocessableEntity, errs, spec,
		"certificate_form")
}
```

`CertificateCreate` (l.91):

```go
		if errs, ok := validationErrors(err); ok {
			a.refuseCertificateCreate(w, r, errs, spec)
			return
		}
```

- [ ] **Step 5: Run the suite**

```bash
make test; echo "make test exit: $?"
```

`TestEveryRespondNamesATemplateThatResolves` must stay green — `certificate_form` is defined
in `partials/certificates.html`, so it is in that test's `shared` set and resolves from the
`certificate_list` page set through `pagePartial`. If it goes red, the `{{define}}` name and
the `Respond` argument have drifted apart.

- [ ] **Step 6: Prove each guard can fail**

| Mutation | Test that must go red |
|---|---|
| Change `refuseCertificateCreate`'s region back to `"certificate_list_panel"` | `TestACertificateRefusalRerendersTheFormAndNotTheList`, on **both** the missing `certificate-form` and the present `certificate-list` |
| Delete `id="certificate-form"` from `partials/certificates.html` | the same test, on the first assertion only — and note the direction: the region name is right and the id is gone, which is failure mode (c). The second assertion stays green, which is how you tell the two mutations apart |
| Change `render.Redirect` in `CertificateCreate` to `a.Render.Respond(…200…)` | `TestACertificateCreateStillRedirectsOnSuccess` |
| Drop `hx-target`/`hx-swap` from the form tag | **nothing yet** — the census does not exist until Task 7. Stated so nobody reads this table as claiming coverage it has not got |

- [ ] **Step 7: Commit**

```bash
git add -A && make lint
git commit   # why: a refusal rendered into a fragment the operator never sees is
             # the same as no refusal; the form is what holds the error and the
             # typed value, so the form is what a 422 re-renders
```

---

### Task 2: Teams — the create form re-renders itself

Identical shape. `TeamCreate` answers 422 with `renderTeamList` → `team_list_panel`
(`#team-list`); the create form is at `pages/team_list.html:39`, outside it.

The form **moves into `partials/teams.html`** as `{{define "team_create_form"}}`, beside
`team_list_panel` and `team_panel`. `certificate_form` and `user_form` both already live in
`partials/`; keeping all four team regions in one file is how the next reader finds them.

**Files:** `internal/web/handlers/teams.go`, `web/templates/pages/team_list.html`,
`web/templates/partials/teams.html`, `internal/web/teams_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/teams_test.go`:

```go
// TestATeamRefusalRerendersTheCreateFormAndNotTheRoster.
//
// HX-Request true. TestCreatingATeamAndTheValidationPath above posts with
// htmx=false and therefore reads a whole page, which contains both the roster
// and the form -- so it cannot see which region the handler chose, and has not
// been able to for the life of this bug.
//
// A SINGLE SPACE, not an empty string, and that is deliberate: <input required>
// refuses to submit an empty field client-side, so an empty name is a refusal no
// browser can produce. checkRequired trims, so " " is refused by the server and
// accepted by the browser -- the same refusal the E2E spec drives, reached the
// same way.
func TestATeamRefusalRerendersTheCreateFormAndNotTheRoster(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/teams", url.Values{
		"csrf_token": {h.csrfToken("/teams")},
		"code":       {"netsec"},
		"name":       {" "}, // THE FAILURE: whitespace is not a name
	}, true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
	}
	if !strings.Contains(b, `id="team-create-form"`) {
		t.Errorf("the 422 body is not the create form. Body: %s", b)
	}
	if strings.Contains(b, `id="team-list"`) {
		t.Errorf("the 422 body is the ROSTER panel, which carries no error state "+
			"and no typed value -- targeted at #team-list it would redraw cleanly "+
			"and say nothing. Body: %s", b)
	}
	if !strings.Contains(b, `class="field-error"`) {
		t.Errorf("the re-rendered form carries no field error. Body: %s", b)
	}
	if !strings.Contains(b, "netsec") {
		t.Errorf("the re-rendered form lost the code that was typed. Body: %s", b)
	}
}

// TestATeamCreateStillRedirectsOnSuccess -- see the certificates counterpart for
// why this is asserted rather than argued from htmx's documentation.
func TestATeamCreateStillRedirectsOnSuccess(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/teams", url.Values{
		"csrf_token": {h.csrfToken("/teams")},
		"code":       {"target-sweep"},
		"name":       {"Target Sweep"},
	}, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a successful create answered %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); !strings.HasPrefix(got, "/teams/") {
		t.Errorf("HX-Redirect = %q, want /teams/<id>", got)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/web/ -run 'TestATeamRefusal|TestATeamCreateStillRedirects' -count=1
```

- [ ] **Step 3: Move the form into the partial**

Delete `pages/team_list.html:36-68` (the whole `{{if .IsAdmin}} … {{end}}` block) and replace
with:

```html
{{if .IsAdmin}}
{{template "team_create_form" .}}
{{end}}
```

Append to `web/templates/partials/teams.html`, after `team_list_panel`:

```html
{{/* The create form, extracted from pages/team_list.html so it can be its own
     refusal target.

     TeamCreate used to answer a validation failure by re-rendering
     team_list_panel -- the ROSTER -- which does not contain this form. The
     .Errors and .Spec below were rendered into a fragment the operator never
     saw, and on an HX-Request the roster was all that came back. Same defect as
     certificate_form and user_form; CLAUDE.md: "Validation errors re-render the
     form partial with error state and return HTTP 422."

     Moved here rather than left on the page because its two siblings (team_panel,
     team_retire_confirm) are here and a reader looking for "where does a team
     refusal render" should find all three in one file. */}}
{{define "team_create_form"}}
<div class="panel" id="team-create-form">
  <div class="panel-head"><h2>Add a team</h2></div>
  <form class="panel-body" method="post" action="/teams" hx-post="/teams"
        hx-target="#team-create-form" hx-swap="outerHTML">
    <input type="hidden" name="csrf_token" value="{{.CSRF}}">
    <div class="field-row">
      <div class="field">
        <label for="t-code">Code</label>
        <input id="t-code" class="mono" type="text" name="code" value="{{.Spec.Code}}" required>
        {{with index .Errors "code"}}<div class="field-error">{{.}}</div>{{end}}
      </div>
      <div class="field">
        <label for="t-name">Name</label>
        <input id="t-name" type="text" name="name" value="{{.Spec.Name}}" required>
        {{with index .Errors "name"}}<div class="field-error">{{.}}</div>{{end}}
      </div>
      <div class="field">
        <label for="t-contact">Contact</label>
        <input id="t-contact" class="mono" type="text" name="contact_ref"
               value="{{deref .Spec.ContactRef}}"
               placeholder="platform@example.com · #platform-oncall · QUEUE-INFRA">
        <div class="field-hint">
          A group address, a queue or a channel — <strong>never a person</strong>.
          This database is kept forever and its audit trail cannot be edited.
        </div>
      </div>
    </div>
    <div class="field">
      <label for="t-desc">Description</label>
      <input id="t-desc" type="text" name="description" value="{{deref .Spec.Description}}"
             placeholder="what this team is answerable for">
    </div>
    <button class="btn btn-primary" type="submit">Add team</button>
  </form>
</div>
{{end}}
```

Note the two `value="{{deref .Spec…}}"` additions on contact and description: today those
fields are re-rendered **empty** after a refusal, so an operator who filled them in loses
them. The move is the moment to fix it, because the whole point of the region change is that
what was typed comes back.

- [ ] **Step 4: Split the render path**

`internal/web/handlers/teams.go`, replacing `renderTeamList` (l.53-69):

```go
// renderTeamListPage assembles the teams page and renders ONE of its two
// swappable regions. See renderCertificateListPage for why the ListTeams query
// stays on the refusal path.
func (a *App) renderTeamListPage(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.TeamSpec, region string) {

	query := r.URL.Query().Get("q")
	teams, err := a.Store.ListTeams(r.Context(), store.TeamFilter{Query: query})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, status, "team_list", region, teamListPage{
		Base:   a.base(r, "Teams", "teams"),
		Errors: orEmpty(errs),
		Teams:  teams,
		Spec:   spec,
		Query:  query,
	})
}

func (a *App) renderTeamList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.TeamSpec) {
	a.renderTeamListPage(w, r, status, errs, spec, "team_list_panel")
}

// refuseTeamCreate re-renders THE FORM, which is where .Errors and .Spec are.
func (a *App) refuseTeamCreate(w http.ResponseWriter, r *http.Request,
	errs map[string]string, spec domain.TeamSpec) {
	a.renderTeamListPage(w, r, http.StatusUnprocessableEntity, errs, spec, "team_create_form")
}
```

`TeamCreate` (l.86):

```go
		if errs, ok := validationErrors(err); ok {
			a.refuseTeamCreate(w, r, errs, spec)
			return
		}
```

- [ ] **Step 5: Run the suite, including the E2E contract this moves**

```bash
make test; echo "make test exit: $?"
grep -n 'form\[action="/teams"\]' tests/e2e/specs/user-administration.spec.js
```

`tests/e2e/specs/user-administration.spec.js:150,167` asserts `form[action="/teams"]` is
visible for a writer and absent for an observer. The moved form keeps `action="/teams"` and
keeps its `{{if .IsAdmin}}` gate, so both assertions hold. **Confirm this by reading, and say
so** — an E2E suite is not in `make test` and will not tell you.

- [ ] **Step 6: Prove each guard can fail**

| Mutation | Test that must go red |
|---|---|
| `refuseTeamCreate`'s region back to `"team_list_panel"` | `TestATeamRefusalRerendersTheCreateFormAndNotTheRoster`, on both region assertions |
| Delete `id="team-create-form"` | same test, first assertion only (failure mode (c) in isolation) |
| Drop `value="{{.Spec.Code}}"` from `#t-code` | same test, the `netsec` assertion — the one that says the operator keeps what they typed |
| `render.Redirect` → `Respond(…200…)` in `TeamCreate` | `TestATeamCreateStillRedirectsOnSuccess` |

- [ ] **Step 7: Commit**

```bash
git add -A && make lint
git commit   # why: same defect as certificates -- the roster has nowhere to put a
             # field error, so a 422 that renders the roster is a refusal nobody
             # is told about; and the contact/description fields were being
             # blanked on every refusal, which is what "re-render the form" is for
```

---

### Task 3: Users — the create form re-renders itself

**This task is not in the spec. It was found while reading the handlers behind the 38 and
signed off on 2026-09-16; see "Where this plan departs from the spec", item 1.** `UserCreate`
has four 422 sites, all calling `renderUserList` → `user_list_panel`. `user_form` is at
`pages/user_list.html:25`, outside it. Its root already carries `id="user-form"`, so this is
the smallest of the three.

**Files:** `internal/web/handlers/users.go`, `web/templates/partials/user_form.html`,
`internal/web/users_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/users_test.go`:

```go
// TestAUserRefusalRerendersTheCreateFormAndNotTheRoster.
//
// The third instance of the certificates/teams defect, found by reading every
// handler behind the untargeted forms rather than by trusting the brief's list
// of two. user_form already carries id="user-form" -- the markup was right and
// the handler named the wrong region, which is why nothing pointed at it.
//
// A SHORT PASSWORD, because it needs no fixture and no second account: the
// length check is UserCreate's own, before any store call, so this test writes
// nothing at all. <input type="password" required> has no minlength, so a
// browser submits it happily -- the same refusal the E2E spec drives.
func TestAUserRefusalRerendersTheCreateFormAndNotTheRoster(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/users", url.Values{
		"csrf_token": {h.csrfToken("/users")},
		"username":   {"sweep-tester"},
		"password":   {"short"}, // THE FAILURE: under minPasswordLength
	}, true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
	}
	if !strings.Contains(b, `id="user-form"`) {
		t.Errorf("the 422 body is not the create form. Body: %s", b)
	}
	if strings.Contains(b, `id="user-list"`) {
		t.Errorf("the 422 body is the ROSTER. Body: %s", b)
	}
	if !strings.Contains(b, `class="field-error"`) {
		t.Errorf("the re-rendered form carries no field error. Body: %s", b)
	}
	if !strings.Contains(b, "sweep-tester") {
		t.Errorf("the re-rendered form lost the username that was typed. Body: %s", b)
	}
	// NOTHING WAS CREATED. A refusal that half-wrote would be a worse bug than
	// the one being fixed, and this is the cheapest place to say so.
	if got := h.count(`SELECT COUNT(*) FROM app_user WHERE username = ?`, "sweep-tester"); got != 0 {
		t.Errorf("a refused create left %d app_user row(s) behind", got)
	}
}

// TestAUserCreateStillRedirectsOnSuccess -- see the certificates counterpart.
func TestAUserCreateStillRedirectsOnSuccess(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/users", url.Values{
		"csrf_token": {h.csrfToken("/users")},
		"username":   {"sweep-accepted"},
		"password":   {"a-sufficiently-long-password"},
	}, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a successful create answered %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got != "/users" {
		t.Errorf("HX-Redirect = %q, want /users", got)
	}
}
```

Check the roster panel's id before running: the assertion `id="user-list"` must match the
actual root of `user_list_panel`. If it is something else, use that string — an assertion
against an id that does not exist anywhere can never fail and is decoration.

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/web/ -run 'TestAUserRefusal|TestAUserCreateStillRedirects' -count=1
```

- [ ] **Step 3: The form declares its target**

`web/templates/partials/user_form.html:17`:

```html
  {{/* This partial IS its own refusal target. UserCreate used to answer all four
       of its 422s with user_list_panel -- the roster -- which holds no .Errors
       and no .FormUsername, so the refusal arrived somewhere nobody could see it.
       The id below was already here; only the handler was looking elsewhere. */}}
  <form class="panel-body" method="post" action="/users" hx-post="/users"
        hx-target="#user-form" hx-swap="outerHTML">
```

- [ ] **Step 4: Split the render path**

`internal/web/handlers/users.go`, `renderUserList` (l.149) gains a trailing `region string`
parameter and passes it to `Respond` in place of the literal `"user_list_panel"`. Add:

```go
// refuseUserCreate re-renders THE FORM. See user_form.html's own comment.
func (a *App) refuseUserCreate(w http.ResponseWriter, r *http.Request,
	errs map[string]string, username string) {
	a.renderUserList(w, r, http.StatusUnprocessableEntity, errs, username, "user_form")
}
```

All four 422 sites in `UserCreate` (l.203, 208, 216, 231) become `a.refuseUserCreate(w, r, …)`.
`UserList`'s call passes `"user_list_panel"`. **Every other caller of `renderUserList` keeps
`"user_list_panel"`** — grep for them; `respondUserMutation` renders a row and is untouched.

- [ ] **Step 5: Run the suite**

```bash
make test; echo "make test exit: $?"
```

- [ ] **Step 6: Prove each guard can fail**

| Mutation | Test that must go red |
|---|---|
| `refuseUserCreate`'s region → `"user_list_panel"` | `TestAUserRefusalRerendersTheCreateFormAndNotTheRoster`, both region assertions |
| Delete `id="user-form"` from `partials/user_form.html:15` | same test, first assertion only |
| Convert one of the four 422 sites back to `renderUserList(…, "user_list_panel")` — use the `len(password) < minPasswordLength` one at l.208 | same test. **This mutation matters most**: three of the four sites can be fixed and one missed, and only the vector that reaches the missed one goes red. Confirm the test's vector reaches l.208 specifically |
| `render.Redirect` → 200 in `UserCreate` | `TestAUserCreateStillRedirectsOnSuccess` |

- [ ] **Step 7: Commit**

```bash
git add -A && make lint
git commit   # why: the third handler with this defect, found by reading all 38
             # rather than the two the brief named; the markup was already right
```

---

### Task 4: `handleStoreError`'s `ErrInvalid` stops destroying the form

`ErrInvalid` is not a field validation failure — it is an invariant violation raised deep in
the store (*"a pool cannot hold itself"*, *"references something that does not exist"*) with
no field to attach an error to. Today it answers 422 with `http.Error`'s plain text, and
app.js force-swaps on **status**, so that sentence is swapped in like any partial. Untargeted
it wipes the form's insides. **Once targets exist it replaces a whole panel with one line of
text** — and for the retire forms that panel is the page's main content. Declaring a target
strictly enlarges the damage, which is why this lands before the sweep.

`HX-Reswap` appears nowhere in this codebase outside the vendored `htmx.min.js`. It is
supported in 2.0.4 — `if(R(s,/HX-Reswap:/i)){g=s.getResponseHeader("HX-Reswap")}`, then
`g=m.swapOverride` after `htmx:beforeSwap` and `x=gn(o,g)` — and OOB fragments are processed
in `$e` **before** `_e(swapStyle,…)` hits its `case "none": return`, so the flash lands while
nothing is destroyed. That was read out of the vendored file, and it is still tested here
rather than trusted.

**Files:** `internal/web/handlers/app.go`, `internal/web/handlers/savedviews.go`,
`internal/store/vocabulary.go`, `internal/web/web_test.go` (new tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/web_test.go` (beside `TestVocabularyValidationRerendersTheForm`,
which is about the other half of the same branch):

```go
// TestAnInvariantRefusalFlashesAndSwapsNothing.
//
// handleStoreError's domain.ErrInvalid branch is what is left when something got
// past the form's own checks -- an invariant the store enforces, with no field to
// attach a message to. It answered 422 with the plain sentence "That request was
// not valid.", and app.js force-swaps on STATUS, not on body: once the elements
// around it declare hx-target, that sentence replaces a whole panel.
//
// So it stops being swapped at all: 422, an out-of-band flash, HX-Reswap: none.
// The operator is told; the form keeps everything they typed; nothing is
// destroyed.
//
// THE VECTOR IS A FOREIGN-KEY VIOLATION, chosen because almost every handler in
// this package catches domain.ErrInvalid itself (bulkApplyTag, OwnershipAssign,
// AssetStorageClaim, TeamReassignAndRetire all do) and a vector that never
// reaches handleStoreError would make this test pass for the wrong reason.
// deployCertificate inserts straight into certificate_service; an unknown
// service_id is a foreign key violation, which translateWriteErr maps to
// ErrInvalid, and afterCertificateWrite's validationErrors() does not recognise
// a wrapped sentinel. Verified against the UNMODIFIED handler before this test
// was written (plan Task 0 Step 2).
func TestAnInvariantRefusalFlashesAndSwapsNothing(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	certID := h.lookup(`SELECT id FROM certificate LIMIT 1`)
	if certID == "" {
		t.Fatal("no certificate in the fixture, so this vector cannot be driven -- " +
			"seed one rather than letting this test skip")
	}
	resp := h.post("/certificates/"+certID+"/services", url.Values{
		"csrf_token": {h.csrfToken("/certificates/" + certID)},
		"service_id": {"00000000-0000-7000-8000-000000000000"}, // no such service
	}, true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
	}
	if got := resp.Header.Get("HX-Reswap"); got != "none" {
		t.Errorf("HX-Reswap = %q, want \"none\". Without it app.js's forced swap "+
			"puts this body wherever the element points -- and every element in "+
			"this codebase now points somewhere", got)
	}
	if !strings.Contains(b, `hx-swap-oob=`) {
		t.Errorf("the body carries no out-of-band fragment, so with HX-Reswap: none "+
			"the operator is told nothing at all -- a silent refusal, which is "+
			"strictly worse than the sentence this replaced. Body: %s", b)
	}
	if !strings.Contains(b, `id="flash-dock"`) {
		t.Errorf("the out-of-band fragment does not name #flash-dock, so htmx fires "+
			"htmx:oobErrorNoTarget and drops it. Body: %s", b)
	}
	if !strings.Contains(b, "That request was not valid.") {
		t.Errorf("the flash carries no message. Body: %s", b)
	}
	// NOT A WHOLE PAGE and not a panel. The failure being fixed is a body big
	// enough to destroy something.
	if strings.Contains(b, "<html") || strings.Contains(b, "<!doctype") {
		t.Errorf("the body is a whole document. Body: %s", b)
	}
}

// TestAnInvariantRefusalStillAnswersPlainTextToAPlainPost.
//
// The out-of-band fragment above is meaningless without htmx: a browser with
// JavaScript off, navigating a plain method="post" form, would render
// `<div id="flash-dock" hx-swap-oob="beforeend">...` as the entire page. That is
// worse than the sentence it replaced, so the branch keeps the sentence for a
// request that did not come from htmx.
//
// ONE BRANCH IN ONE SHARED ERROR MAPPER, not the per-handler HX-Request check
// CLAUDE.md forbids -- there is no render.Respond shape for "a response with no
// primary fragment", and inventing one for a single call site would be a bigger
// change than the branch.
func TestAnInvariantRefusalStillAnswersPlainTextToAPlainPost(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	certID := h.lookup(`SELECT id FROM certificate LIMIT 1`)
	if certID == "" {
		t.Fatal("no certificate in the fixture")
	}
	resp := h.post("/certificates/"+certID+"/services", url.Values{
		"csrf_token": {h.csrfToken("/certificates/" + certID)},
		"service_id": {"00000000-0000-7000-8000-000000000000"},
	}, false) // NOT an HX-Request
	b := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", resp.StatusCode, b)
	}
	if !strings.Contains(b, "That request was not valid.") {
		t.Errorf("a JavaScript-off operator got %q instead of a sentence", b)
	}
	if strings.Contains(b, `hx-swap-oob=`) {
		t.Errorf("a plain form post was answered with an out-of-band fragment, "+
			"which a browser renders as the whole page. Body: %s", b)
	}
	if got := resp.Header.Get("HX-Reswap"); got != "" {
		t.Errorf("HX-Reswap = %q on a non-HTMX response; harmless but it means the "+
			"branch is not where it looks", got)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/web/ -run TestAnInvariantRefusal -count=1
```

The first must fail on `HX-Reswap` being empty; the second must already pass. Observe both —
the second is the regression guard and it is only evidence if it was green before the change.

- [ ] **Step 3: Implement**

`internal/web/handlers/app.go`, replacing the `ErrInvalid` case at l.441:

```go
	case errors.Is(err, domain.ErrInvalid):
		a.refuseInvariant(w, r)
```

and, below `handleStoreError`:

```go
// refuseInvariant answers a store invariant violation without destroying the
// caller's form.
//
// domain.ErrInvalid is NOT a field validation failure. It is what is left when
// something got past the form's own checks -- "a pool cannot hold itself", "a
// retired asset houses nobody", "references something that does not exist" --
// so there is no field to mark and nothing for the operator to correct in place.
// A person who mistyped a value gets validationErrors() and a proper field-level
// 422 long before reaching here.
//
// IT USED TO BE http.Error's PLAIN SENTENCE, AND THAT STOPPED BEING SAFE. app.js
// force-swaps on STATUS, not on body (see its htmx:beforeSwap listener and the
// comment above it), so the sentence was swapped in like any partial: untargeted,
// it wiped a form's insides. Once every hx-post element declares a target -- which
// is what docs/htmx-swap-targets-design.md does -- it would replace a whole panel
// with one line of text, and for the retire forms that panel is the page's main
// content. Declaring a target made this path strictly worse, so this path changed
// in the same branch.
//
// Three options, two of them worse than what they replace:
//   - 400. htmx does not swap it and the operator sees nothing happen. That is
//     the silent failure the whole work package exists to remove.
//   - 422 with the plain text. The panel-replacement above.
//   - 422, an out-of-band flash, HX-Reswap: none. Chosen.
//
// It does NOT break "a refusal is rendered, not flashed"
// (refusal_status_test.go). That test's property is narrow and stated: a
// function that CLASSIFIES a refusal -- calls refusalMessages or
// validationErrors -- must not FLASH one. handleStoreError calls neither. And
// the harm that rule names ("the redirect discards the form, so an operator who
// mistyped one field retypes all of them") cannot occur here: there is no
// redirect, and HX-Reswap: none leaves the form exactly as it was, typed values
// and all.
//
// The message is unchanged from what http.Error sent. Saying MORE would mean
// putting a wrapped store error in front of an operator, which is a separate
// question with a separate answer.
func (a *App) refuseInvariant(w http.ResponseWriter, r *http.Request) {
	const message = "That request was not valid."
	if !render.IsHTMX(r) {
		// A plain browser navigation has nowhere to put an out-of-band fragment:
		// it would render `<div id="flash-dock" hx-swap-oob=...>` as the entire
		// document. The sentence is still the right answer for that caller.
		http.Error(w, message, http.StatusUnprocessableEntity)
		return
	}
	// Set before the body: Partial writes the status line.
	//
	// HX-Reswap overrides the element's own hx-swap (htmx.min.js 2.0.4 reads it
	// into swapOverride, and $e processes hx-swap-oob BEFORE the primary swap,
	// so "none" suppresses the destruction and keeps the flash).
	w.Header().Set("HX-Reswap", "none")
	// oobFlash, so the template name lives in exactly one place -- and its own
	// doc comment is the definition of this case: "a handler can report what
	// happened without the caller having arranged anywhere to put the message."
	flash := oobFlash("error", message)
	a.Render.Partial(w, http.StatusUnprocessableEntity, flash.Template, flash.Data)
}
```

`render` is already imported in `app.go`; confirm rather than assume.

- [ ] **Step 4: Amend the two comments that describe the old behaviour**

Both make a claim about this branch that has stopped being true. A stale comment that reads
as proof is what this repo's own censuses exist to prevent.

`internal/web/handlers/savedviews.go:135`, replace *"a bare "That request was not valid." text
body, returned to a plain (non-hx-post) form post, would navigate the whole tab away"* with:

```go
// handleStoreError gives: since the swap-target sweep it answers an
// HX-Request with an out-of-band flash and HX-Reswap: none, which destroys
// nothing but also redraws nothing -- so the operator keeps their filters and
// is told the write failed, without the saved-view menu coming back showing
// what they actually asked for. A plain (non-hx-post) form post still gets the
// bare sentence and would navigate the whole tab away.
```

`internal/store/vocabulary.go:315`, replace *"answers 422 with the bare string "That request
was not valid." -- no form, no field highlighted, and whatever was typed lost"* with:

```go
	// ...falls through to handleStoreError, which answers 422 with an
	// out-of-band flash and HX-Reswap: none -- no form, no field highlighted,
	// and nothing pointing at which of these two is wrong. The typed values
	// survive now, which they did not before the swap-target sweep, but the
	// operator is still left guessing. The template already has the per-field
	// hooks; they were unreachable from this path. Found by review.
```

- [ ] **Step 5: Run the suite**

```bash
make test; echo "make test exit: $?"
```

Two existing tests assert the body is **not** `"That request was not valid."`
(`savedviews_test.go:523,619`). They stay green — that path never reached this branch.
`TestARefusalIsRenderedNotFlashed` (`handlers/refusal_status_test.go`) stays green because
`refuseInvariant` is reached from `handleStoreError`, which calls neither `refusalMessages`
nor `validationErrors`, and because `oobFlash` is not `setFlash`. **Confirm both by running,
not by reading this paragraph.**

- [ ] **Step 6: Prove each guard can fail**

| Mutation | Test that must go red |
|---|---|
| Delete `w.Header().Set("HX-Reswap", "none")` | `TestAnInvariantRefusalFlashesAndSwapsNothing`, on the header assertion |
| Swap `flash.Template` for a template with no `hx-swap-oob` (e.g. `"flash"`) | same test, on the `hx-swap-oob=` and `#flash-dock` assertions. **Two assertions, one mutation** — deliberate: `HX-Reswap: none` with a non-OOB body is a completely silent refusal, and it has to be impossible to reach by deleting one line |
| Delete the `if !render.IsHTMX(r)` branch | `TestAnInvariantRefusalStillAnswersPlainTextToAPlainPost`, on the `hx-swap-oob=` assertion |
| Change the status to `http.StatusBadRequest` | both tests, on the status assertion — and note the layer: app.js only force-swaps 422, so a 400 here would also make every one of these refusals invisible in a browser, which **no Go test can see**. That half is covered by the E2E spec in Task 8 |

- [ ] **Step 7: Commit**

```bash
git add -A && make lint
git commit   # why: this branch was survivable only because no element declared a
             # target; with targets it would replace a page's main panel with one
             # sentence, so it stops being swapped at all and becomes a flash
```

---

### Task 5: The sweep — class 1, one commit per template file

Two commits (the other three class-1 surfaces landed in Tasks 1-3). Every element names the id
of the partial its handler re-renders on 422, with `outerHTML`. Each target was checked
against every render path that can show the element (failure mode (c)) — the per-element note
is in "The 38, resolved" above, and the implementer re-checks rather than trusting it.

Each file gets one comment block at the first element it applies to, and the later elements in
the same file point at it. The voice to match is
`web/templates/partials/identities.html:199-214`, the reference text for this work package.

- [ ] **Step 1: `partials/certificates.html` — five elements → `#certificate`**

Add to all five open tags (`:203`, `:219`, `:244`, `:262`, `:282` at `bc74a71`):

```
hx-target="#certificate" hx-swap="outerHTML"
```

Comment block above the first of them (`:203`, the asset-undeploy form):

```html
              {{/* hx-target="#certificate" hx-swap="outerHTML" on every posting
                   element in this panel, and the id is this partial's own root
                   (certificate_panel, above). CLAUDE.md: "Swap targets are
                   declared in the template, not chosen by the handler. Default
                   to hx-swap='outerHTML' on a wrapping element with a stable
                   id."

                   Read off the handler, not guessed: every one of these routes
                   ends in afterCertificateWrite or CertificateUpdate, both of
                   which answer a validation failure with renderCertificate ->
                   Respond(..., "certificate_detail", "certificate_panel", ...).
                   The 422 body is therefore this whole panel, and without a
                   target htmx's default puts it inside the <form> that sent it:
                   every heading rendered twice, two elements carrying
                   id="certificate", and every later hx-target="#certificate"
                   resolving to whichever the browser finds first. That exact
                   failure was seen in a browser on the identity rotation form
                   (068d91c).

                   #certificate is certificate_panel's unconditional root, so it
                   is present on every render that can show these forms -- the
                   forms are inside {{if $.IsAdmin}} blocks WITHIN it, never
                   outside. Checked per element, not assumed: a target inside a
                   conditional the element is not fails at REQUEST time with
                   htmx:targetError, silently.

                   Success is unaffected: render.Redirect answers an HX-Request
                   with HX-Redirect, and htmx returns from handleAjaxResponse at
                   that header before it resolves a target or picks a swap. */}}
```

Commit: `why: these five all re-render #certificate on a refusal; saying so is what stops the
panel nesting inside the button that asked for it`

- [ ] **Step 2: `partials/teams.html` — four elements, three destinations**

- `:173` (`POST /teams/{id}`, the edit form) → `hx-target="#team" hx-swap="outerHTML"`.
  `TeamUpdate` → `renderTeam` → `team_panel`, root `<div id="team">` (l.52), outside the
  `{{if .IsAdmin}}` the form sits in. ✔
- `:272` (`reassign-retire`) → `hx-target="#team-retire-confirm" hx-swap="outerHTML"`.
  `TeamReassignAndRetire` has **three** 422 sites (empty target, `validationErrors`,
  `ErrInvalid`), all `renderTeamRetireConfirm` → `team_retire_confirm`, root
  `<div id="team-retire-confirm">` (l.222), outside the `{{if eq .Counts.Total 0}}` branch. ✔
- `:235` and `:298` (both `POST /teams/{id}/retire`) → `hx-target="this" hx-swap="none"`.
  `TeamRetire` (`teams.go:179`) has exactly two answers: `render.Redirect` on success and
  `handleStoreError` on failure. There is no fragment.

Comment for the two retire forms (written once at `:235`, pointed at from `:298`). **This is
the canonical class-2 comment; Task 6 points at it rather than repeating it.**

```html
        {{/* hx-target="this" hx-swap="none", and that is a DECLARATION rather
             than an exemption -- docs/htmx-swap-targets-design.md, rule 1:
             "hx-swap='none' is a legitimate declaration, not an exemption."

             Argued from the handler, which is what rule 4 demands of any claim
             that a response has no in-band body: TeamRetire (handlers/teams.go)
             answers render.Redirect on success and handleStoreError on failure,
             and nothing else. handleStoreError's only swappable status is 422
             (app.js force-swaps that alone), and since the ErrInvalid branch
             became an out-of-band flash it sends HX-Reswap: none itself -- so
             the server and this attribute agree, and the flash still lands
             because htmx processes hx-swap-oob before it consults the swap
             style at all.

             NOT a nearby panel id: naming one would mean that the day somebody
             widens app.js's force-swap to 409, a whole panel is replaced by
             whatever handleStoreError wrote. "this" plus "none" says what is
             true -- this route answers with a navigation or a flash, never a
             fragment -- and the sibling reassign-retire form four panels up
             names a real target for exactly the opposite reason. */}}
```

Commit: `why: the edit and reassign forms render real fragments and now say where; the two
retire forms never render one, and say that instead`

- [ ] **Step 3: Verify the sweep's class-1 half**

```bash
python3 /tmp/claude-1000/hx/scan.py | grep -c 'target=-'   # expect 38 - 10 = 28
make test; echo "make test exit: $?"
```

---

### Task 6: The sweep — class 2, twenty-eight elements

Twenty-eight elements, `hx-target="this" hx-swap="none"` on every one, eleven commits. The
comment written at Task 5 Step 2 is the canonical version; each file carries a short form
naming **its own** handler, because the claim being made is about that handler:

```html
{{/* hx-target="this" hx-swap="none" -- a declaration, not an exemption
     (docs/htmx-swap-targets-design.md rule 1), argued from the handler as
     rule 4 requires: <HandlerName> (handlers/<file>.go) answers
     <flash + render.Redirect | render.Redirect> on success and
     handleStoreError on failure, and renders no fragment on any path. See
     partials/teams.html's retire forms for the full reasoning. */}}
```

**Do not paste the long block eleven times.** One canonical copy, ten pointers.

| Step | File | Elements | Handler(s) named in the comment | Commit |
|---|---|---|---|---|
| 1 | `pages/asset_detail.html` | 6 | `AssetRetire`, `AssetApplyTemplate`, `IPAddressRetire`, `InterfaceRetire`, `LinkRetire`, `AssetStorageClaim` | yes |
| 2 | `pages/net_group_detail.html` | 4 | `NetworkGroupMemberRetire`, `NetworkUplinkRetire`, `NetworkAttachmentRetire`, `NetworkGroupRetire` (all `reach_repair.go`) | yes |
| 3 | `partials/costs.html` | 3 | `afterCostWrite` — **and note it is already on `refusalFlashExceptions` by name**, which is the argument, pre-written and already reviewed | yes |
| 4 | `pages/prefix_list.html` | 2 | `PrefixRetire`, `IPRangeRetire` | yes |
| 5 | `pages/bundle_detail.html` | 1 | `BundleRetire` | yes |
| 6 | `pages/fhrp_detail.html` | 1 | `FHRPRetire` | yes |
| 7 | `pages/network_list.html` | 1 | `NetworkAnchorRetire` | yes |
| 8 | `pages/service_detail.html` | 1 | `EndpointRetire` | yes |
| 9 | `partials/health.html` | 1 | `HealthOverrideClear` — also on `refusalFlashExceptions` by name | yes |
| 10 | `partials/journal.html` | 1 | `JournalRetire`. **Say in the comment that this is why Ruling 4 needs no answer here**: the partial serves eight routes and needs no id from the dict and none of its own | yes |
| 11 | the five buttons: `pages/catalogue.html`, `pages/environment_list.html`, `partials/custom_field_form.html`, `partials/rows.html`, `partials/tag_form.html` | 5 | `DeviceTypeComponentRetire`, `EnvironmentRetire`, `CustomFieldRetire`, `DependencyRetire`, `TagRetire` | yes, one commit |

Step 11's commit message carries the worked contrast, because it is the clearest evidence in
the branch that the rule is read off the handler and not applied uniformly:

```
why: five buttons whose handlers answer with a navigation, never a fragment.
partials/rows.html is the proof this is read per handler and not applied by tag
name: the Verify button one line above targets #dep-{{.Dep.ID}} because
DependencyVerify really does PartialWithOOB a dependency_row, and the Retire
button beside it declares none because DependencyRetire redirects. Two buttons,
one cell, two answers.
```

- [ ] **Step 12: Verify the sweep is complete**

```bash
python3 /tmp/claude-1000/hx/scan.py > /tmp/claude-1000/hx/after.txt
grep -c 'target=-' /tmp/claude-1000/hx/after.txt     # MUST be 0
wc -l < /tmp/claude-1000/hx/after.txt                # MUST still be 90
diff <(cut -f1 /tmp/claude-1000/hx/before.txt) <(cut -f1 /tmp/claude-1000/hx/after.txt)
```

The count must still be 90. **If it is not, an element was created or destroyed by a hand
edit** — the sweep adds attributes and never elements. The `diff` of file:line pairs will show
shifted line numbers from Tasks 1-3, which is expected; a *new or missing* entry is not.

```bash
make test; echo "make test exit: $?"
```

---

### Task 7: The census — a new file, starting empty

`internal/web/edit_form_version_test.go` is the right precedent and the wrong host: different
population (~90 `hx-post` elements against ~30 versioned routes), different failure (where a
response lands, not an optimistic-concurrency guard), different matching (this joins nothing).
One file holding two unrelated exemption maps argued on different grounds would leave a reader
hitting a failure unsure which rule they broke.

**Files:** `internal/web/hx_target_test.go` (new)

- [ ] **Step 1: Write the census**

```go
// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// CLAUDE.md, "HTTP and HTMX conventions":
//
//	"Swap targets are declared in the template, not chosen by the handler.
//	 Default to hx-swap='outerHTML' on a wrapping element with a stable id."
//
// That rule was written down, never enforced, and violated in 38 of 90 places.
// It is not decoration: it interlocks with app.js's htmx:beforeSwap listener,
// which force-swaps a 422 because htmx 2 would not, "so the server does the
// right thing and the operator sees nothing happen at all". An untargeted
// <form> swallows that swap into its own innerHTML and an untargeted <button>
// into the button, so the refusal arrives and lands inside the control that
// asked for it. Seen in a browser on the identity rotation form, fixed in
// 068d91c; docs/htmx-swap-targets-design.md is the full argument.
//
// WHY STATIC. Several of the ninety are retire forms on entities that need a
// live fixture in a specific state before a refusal is reachable at all, and
// the ones hardest to set up are the ones most likely to be missed -- so
// behavioural coverage would thin out precisely where it matters. The same
// reasoning TestEveryEditFormCarriesItsVersion gives, and it holds more
// strongly here. The behavioural half is tests/e2e/specs/refusal-targets.spec.js,
// on the surfaces with a reachable refusal.
//
// WHY ITS OWN FILE rather than a second check inside the version census:
// different population (every hx-post element, buttons and unversioned retire
// routes included, against that test's "routes whose handler reaches
// submittedVersion"), different failure (where a response lands, not an
// optimistic-concurrency guard), and different matching -- that test's
// matchRoutes and its asymmetric-wildcard rule exist because it JOINS templates
// to routes. This joins nothing: an element either declares the attributes or it
// does not.

// hxTargetExempt are posting elements that legitimately declare no swap target.
//
// A backlog, like versionExemptRoutes and the store's unreachableRepairPaths.
// An entry is a claim that THIS ELEMENT'S RESPONSE HAS NO IN-BAND BODY UNDER ANY
// STATUS ITS HANDLER CAN PRODUCE, and it has to be argued from the handler --
// never from app.js's current listener. Whoever adds an entry writes down what
// they read to believe it. Empty is the goal, and it is where this starts.
//
// THE OBVIOUS CRITERION -- "a route that can never return a force-swapped
// status" -- IS REJECTED, and the reason is one line in a different file. The
// set of force-swapped statuses is one comparison in app.js. Widening it to 409
// (reachable from every versioned edit form and from handleStoreError's
// ErrConflict branch) would silently re-arm every element exempted on that
// basis. An exemption whose validity rests on a line nobody consulted is not an
// exemption; it is a fuse.
//
// Note what is NOT here: the twenty-eight elements whose handler answers only
// with a redirect or a flash. They are not exempt -- they declare
// hx-target="this" hx-swap="none", which is a declaration and says something
// true. See partials/teams.html's retire forms.
var hxTargetExempt = map[string]string{}

// hxTargetFloor is a "did the scan stop matching the markup" control, not a
// budget and not a target.
//
// Every census in this repository has one, because a scan that matches nothing
// reports success. The population was 90 on 2026-09-16 and will grow. IF THIS
// NUMBER EVER HAS TO BE RAISED TO KEEP THE TEST PASSING, THE SCAN IS BROKEN,
// NOT THE FLOOR.
const hxTargetFloor = 80

func TestEveryPostingElementDeclaresItsSwapTarget(t *testing.T) {
	root := repoRoot(t)

	elements := postingElements(t, root)
	if len(elements) < hxTargetFloor {
		t.Fatalf("found only %d hx-post elements across the template tree, floor is %d; "+
			"the scan has stopped matching the markup and this test is checking nothing",
			len(elements), hxTargetFloor)
	}

	ids := declaredIDs(t, root)
	// The other half of the same control: a target-existence check against an
	// empty id set would pass every target in the tree.
	if len(ids) < 100 {
		t.Fatalf("found only %d id attributes across the template tree; the id scan "+
			"has stopped matching the markup, so every target below would resolve "+
			"against nothing and pass", len(ids))
	}

	for _, el := range elements {
		where := el.file + ":" + el.line

		if why, exempt := hxTargetExempt[where]; exempt {
			if el.target != "" || el.swap != "" {
				t.Errorf("%s declares hx-target=%q hx-swap=%q but is listed as exempt: %q. "+
					"Delete the entry.", where, el.target, el.swap, why)
			}
			continue
		}

		// BOTH, NOT JUST THE TARGET. htmx's default swap is innerHTML, so a form
		// declaring hx-target="#team" and no swap puts the re-rendered #team
		// partial INSIDE #team -- the same duplicated-heading failure one level
		// up, and harder to see because the outer shell still looks right. Worse,
		// the fragment's own root carries id="team", so the DOM then holds two
		// elements with that id and every later hx-target="#team" resolves to
		// whichever the browser finds first. A half-declared target is a slower
		// version of the bug.
		if el.target == "" {
			t.Errorf("%s posts to %q and declares no hx-target.\n"+
				"htmx's default target is the element itself, so a 422 -- which "+
				"app.js force-swaps -- lands inside the form or the button that "+
				"sent it. Add hx-target and hx-swap, or add %q to hxTargetExempt "+
				"with the reason, argued from the handler.", where, el.action, where)
			continue
		}
		if el.swap == "" {
			t.Errorf("%s declares hx-target=%q and no hx-swap.\n"+
				"htmx defaults to innerHTML, which nests the response inside the "+
				"element it names -- and when the response's own root carries that "+
				"id, the DOM ends up holding two of them. hx-swap='outerHTML' is "+
				"the default this codebase uses; hx-swap='none' is a legitimate "+
				"declaration for a route that answers with a navigation or a flash.",
				where, el.target)
			continue
		}

		// THE OTHER DIRECTION: does the id it names exist anywhere in the tree?
		// Catches failure mode (c) -- a target inside a conditional the element
		// is not -- which otherwise fails at REQUEST time with htmx:targetError
		// and is silent in every log this project has.
		//
		// IT WILL NOT CATCH A CONDITIONAL ID, and this comment must not read as
		// stronger than it is: an id that exists in the file but only inside
		// {{if .IsAdmin}} passes here and can still be absent at render time.
		// That half is the per-element check in the plan and the browser spec.
		if !strings.HasPrefix(el.target, "#") {
			// An extended selector: "this", "closest ...", "find ...", "next",
			// "previous". Declared, so the rule above is satisfied; there is no
			// id to look for. "this" is what the twenty-eight
			// redirect-or-flash routes use.
			continue
		}
		want := normaliseID(strings.TrimPrefix(el.target, "#"))
		found := false
		for id := range ids {
			if globsOverlap(want, id) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s targets %q and no element in web/templates declares that id.\n"+
				"htmx resolves the target when the REQUEST is built, not when the "+
				"response arrives, so this fires htmx:targetError and does nothing "+
				"at all -- the silent refusal this census exists to prevent.",
				where, el.target)
		}
	}
}

// hxElement is one posting element and what it declares.
type hxElement struct {
	file, line   string
	action       string
	target, swap string
}

var (
	// <form ...> or <button ...>, attributes possibly spanning lines. One rule
	// for both tags: the blast radius differs and that is not a reason to split
	// the rule, because a two-rule census has to decide which applies from the
	// tag name and the population is not stable -- partials/rows.html already
	// has a <button hx-post> carrying its own target and swap.
	hxOpenRe   = regexp.MustCompile(`(?is)<(?:form|button)\b[^>]*>`)
	hxPostRe   = regexp.MustCompile(`(?is)\bhx-post="([^"]*)"`)
	hxTargetRe = regexp.MustCompile(`(?is)\bhx-target="([^"]*)"`)
	hxSwapRe   = regexp.MustCompile(`(?is)\bhx-swap="([^"]*)"`)
	// An id attribute on any element. hx-swap-oob is deliberately not excluded:
	// #flash-dock is declared by partials/flash.html and by layouts/base.html,
	// and both are real places a response can land.
	hxIDRe = regexp.MustCompile(`(?is)\bid="([^"]*)"`)
	// A template expression anywhere in an attribute value, the way
	// edit_form_version_test.go's exprRe normalises an action.
	hxExprRe = regexp.MustCompile(`{{[^}]*}}`)
)

// postingElements finds every hx-post element in the template tree.
//
// Glob per subdirectory rather than filepath.Walk: the three directories are the
// whole tree, and gosec's G122 rejects a file read inside a Walk callback
// (symlink TOCTOU). The same swap respond_partial_test.go and the version census
// both already made.
func postingElements(t *testing.T, root string) []hxElement {
	t.Helper()
	var out []hxElement
	templatesDir := filepath.Join(root, "web", "templates")
	for _, sub := range []string{"pages", "partials", "layouts"} {
		matches, err := filepath.Glob(filepath.Join(templatesDir, sub, "*.html"))
		if err != nil {
			t.Fatalf("globbing %s: %v", sub, err)
		}
		for _, path := range matches {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("reading %s: %v", path, readErr)
			}
			page := string(raw)
			rel := sub + "/" + filepath.Base(path)
			for _, loc := range hxOpenRe.FindAllStringIndex(page, -1) {
				tag := page[loc[0]:loc[1]]
				post := hxPostRe.FindStringSubmatch(tag)
				if post == nil {
					continue
				}
				el := hxElement{
					file:   rel,
					line:   strconv.Itoa(1 + strings.Count(page[:loc[0]], "\n")),
					action: post[1],
				}
				if m := hxTargetRe.FindStringSubmatch(tag); m != nil {
					el.target = m[1]
				}
				if m := hxSwapRe.FindStringSubmatch(tag); m != nil {
					el.swap = m[1]
				}
				out = append(out, el)
			}
		}
	}
	return out
}

// declaredIDs is every id attribute in the tree, normalised.
func declaredIDs(t *testing.T, root string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	templatesDir := filepath.Join(root, "web", "templates")
	for _, sub := range []string{"pages", "partials", "layouts"} {
		matches, err := filepath.Glob(filepath.Join(templatesDir, sub, "*.html"))
		if err != nil {
			t.Fatalf("globbing %s: %v", sub, err)
		}
		for _, path := range matches {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("reading %s: %v", path, readErr)
			}
			for _, m := range hxIDRe.FindAllStringSubmatch(string(raw), -1) {
				ids[normaliseID(m[1])] = true
			}
		}
	}
	return ids
}

// normaliseID collapses every template expression to a single "*", so
// "#user-row-{{.User.ID}}" and id="user-row-{{$.User.ID}}" both become
// "user-row-*". The same normalisation matchRoutes performs on an action, and
// for the same reason: the markup does not say what the expression renders to.
func normaliseID(s string) string { return hxExprRe.ReplaceAllString(s, "*") }

// globsOverlap reports whether two '*'-globs can describe the same string.
//
// BOTH SIDES CARRY WILDCARDS and they do not line up -- "#{{.Prefix}}-form"
// against id="{{.Prefix}}-form" is the common case, and
// partials/projects.html:391 takes its whole target from the dict
// ("{{.Target}}", normalised to "*"), which therefore matches every id in the
// tree. That is the honest answer for it: nothing in the markup says where it
// lands, and this census cannot know. It is one element, it is in the 52 that
// already declared both attributes, and docs/htmx-swap-targets-design.md's
// ruling 4 ("the partial owns its id") says it is the one place that shape
// should not have been used. Recorded here rather than silently passing.
//
// Exponential in the worst case and irrelevant: the inputs are id-length
// strings with at most two stars.
func globsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return allStars(a) && allStars(b)
	}
	if a[0] == '*' {
		return globsOverlap(a[1:], b) || globsOverlap(a, b[1:])
	}
	if b[0] == '*' {
		return globsOverlap(a, b[1:]) || globsOverlap(a[1:], b)
	}
	return a[0] == b[0] && globsOverlap(a[1:], b[1:])
}

func allStars(s string) bool { return strings.Trim(s, "*") == "" }
```

If `strconv` is already imported by another file in `package web_test` that is fine — Go
imports are per-file. Check that no other file in the package already declares
`normaliseID`, `globsOverlap` or `allStars` before writing.

- [ ] **Step 2: Run it, and watch the positive controls work**

```bash
go test ./internal/web/ -run TestEveryPostingElementDeclaresItsSwapTarget -count=1 -v
```

It must pass. Then prove each control:

```bash
# (a) the element floor
sed -i 's/^const hxTargetFloor = 80/const hxTargetFloor = 200/' internal/web/hx_target_test.go
go test ./internal/web/ -run TestEveryPostingElementDeclaresItsSwapTarget -count=1   # must FAIL on the floor
git checkout internal/web/hx_target_test.go

# (b) the id-set control
sed -i 's/len(ids) < 100/len(ids) < 100000/' internal/web/hx_target_test.go
go test ./internal/web/ -run TestEveryPostingElementDeclaresItsSwapTarget -count=1   # must FAIL on the id scan
git checkout internal/web/hx_target_test.go
```

- [ ] **Step 3: Prove the census can fail, and record which line went red**

This is the mutation check `docs/htmx-swap-targets-design.md` success criterion 3 requires to
be **recorded in the PR body, not asserted**. Run all four; a red test is not proof, which
line went red is.

| Mutation | Expected failure |
|---|---|
| Remove `hx-target="#certificate"` from one `partials/certificates.html` element | `…:203 posts to "…" and declares no hx-target` |
| Remove `hx-swap="outerHTML"` from the same element, leaving the target | `…:203 declares hx-target="#certificate" and no hx-swap` — **a different message**, which is the point: half-declared is its own failure, not a variant of undeclared |
| Change one target to `#certificate-lst` | `…:282 targets "#certificate-lst" and no element … declares that id` |
| Change `hx-target="this"` to `hx-target="#nope"` on `partials/journal.html`'s retire form | the id-existence failure — proving the `strings.HasPrefix(el.target, "#")` branch does not swallow a real mistake |

Restore after each. Record all four in the PR body with the exact message each produced.

- [ ] **Step 4: Commit**

```bash
git add -A && make lint
git commit   # why: a rule this codebase wrote down, never enforced and broke in 38
             # of 90 places; the map is empty because the sweep landed first, and
             # empty is what "empty is the goal" is supposed to look like
```

---

### Task 8: Browser evidence, and the surface that cannot give it

Static coverage cannot see the two signatures that matter: a refusal producing **no visible
change**, and the DOM holding **two elements with the target id**. Both are cheap to assert in
a browser and neither is what a diff review looks at.

**Files:** `tests/e2e/specs/refusal-targets.spec.js` (new), `docs/E2E.md`

- [ ] **Step 1: Read `docs/E2E.md` before writing anything**

Non-optional. The suite fails in ways that look nothing like their cause; `INV_E2E_BASE_URL`
unset means the whole suite reports itself **skipped**, which is the one legitimate runtime
skip here. The instance needs `INV_SEED=true INV_SEED_COMPANY=true`.

- [ ] **Step 2: Write the spec**

Every refusal below **writes nothing** — a validation failure returns before any store call —
so this spec needs neither the `INV_E2E_DISPOSABLE` opt-in `saved-views.spec.js` uses nor the
hostname denylist `user-administration.spec.js` uses. **Say that at the top of the file, and
say it about each test**, per `docs/E2E.md`'s rule that a spec states its own writes loudly.

Per surface, the three assertions:

```js
// 1. the refusal is visible
await expect(page.locator('#certificate-form .field-error')).toBeVisible();
// 2. the operator keeps what they typed
await expect(page.locator('#c-subject')).toHaveValue('backwards.example.com');
// 3. the DOM holds exactly ONE element with the target id
await expect(page.locator('#certificate-form')).toHaveCount(1);
```

Assertion 3 is the one that cannot be faked: it catches an `innerHTML` swap nesting a fragment
whose own root carries the id it was targeted at.

| Test | Refusal driven | Notes |
|---|---|---|
| certificates create | `Valid from` 2030-01-01, `Expires` 2020-01-01 | Both `type="date"` with no `min`/`max`, so a browser submits it. No vocabulary fixture needed |
| team create | `Code` = `netsec`, `Name` = a single space | `required` only rejects empty; `checkRequired` trims. Assert the **code** survives |
| user create | `Username` = `sweep-e2e`, `Password` = `short` | `type="password" required` has no `minlength`. Nothing is created — assert `/users` still does not list `sweep-e2e` afterwards |
| team reassign-retire | empty `target_team_id`, submitted with `form.noValidate = true` | **Flagged, and the spec file must say so at length.** `<select required>` with an empty first option blocks the only non-destructive refusal client-side, and `TeamOptions` excludes retired teams so no selectable option is refusable. The alternative — choosing a valid target — permanently retires a team and writes `change_log`, which `docs/E2E.md` forbids against a shared instance. `noValidate` bypasses only the client-side gate; the request, the 422, the target and the swap are all real, and they are the subject |
| identity rotation (control) | a rotation date in the future, submitted with `input.removeAttribute('max')` | Already correct since `068d91c`. It is here to prove the assertions can pass, so that four reds elsewhere mean a real defect rather than a broken spec. `max="{{.Today}}"` is the same client-side gate and the form's own comment says so |

Add a sixth, on the Task 4 path, because no Go test can see it:

```js
// handleStoreError's ErrInvalid path: 422 + HX-Reswap: none + an out-of-band
// flash. HX-Reswap appears nowhere in this codebase outside the vendored
// htmx.min.js, so whether htmx honours it HERE, composed with app.js's forced
// swap on 422, is a question only a browser answers.
//
// What would be true if this were broken: either the panel this form lives in
// is replaced by one sentence (HX-Reswap ignored), or nothing happens at all
// and the operator is told nothing (the flash dropped).
test('an invariant refusal flashes and destroys nothing', async ({ page }) => {
  // ... drive POST /certificates/{id}/services with a nonexistent service_id
  await expect(page.locator('#flash-dock .flash-error')).toBeVisible();
  await expect(page.locator('#certificate')).toHaveCount(1);
  await expect(page.locator('#certificate .panel-head h2').first()).toHaveText('What it is');
});
```

That last assertion is what catches the panel having been replaced by a sentence.

- [ ] **Step 3: Run it**

```bash
INV_SEED=true INV_SEED_COMPANY=true make dev   # in one terminal
INV_E2E_BASE_URL=http://localhost:8088 make e2e
```

- [ ] **Step 4: Prove the spec can fail**

Revert `hx-target="#certificate-form"` on the certificates create form, re-run, record which
assertion went red. It must be **assertion 1 or 3**, not a timeout — a timeout means the
locator is wrong and the test was decoration. Restore.

- [ ] **Step 5: Document the surface that cannot be covered, in `docs/E2E.md`**

Add a section, in the voice of "Where the ownership report's mutation is covered instead":

```markdown
## Why health override clear is not in refusal-targets.spec.js

`HealthOverrideClear` is on `refusalFlashExceptions` **by name**
(`internal/web/handlers/refusal_status_test.go`), with the reason already
written: "there is nothing to preserve. Clearing an override is a button with
no fields, so a re-render would hand back an empty form the operator never
filled in." Its refusal is a flash and a redirect, it declares
`hx-target="this" hx-swap="none"`, and there is no fragment, no field error and
no typed value — so the three assertions this spec makes everywhere else have
nothing to assert against. Reaching its refusal at all needs a second clear of
an already-cleared override, and the Clear button is gone by then.

Its success path is covered by the Go web suite. This is a split, not a gap,
and it is recorded here so the next reader does not read the absence as an
oversight.
```

- [ ] **Step 6: Commit**

```bash
git add -A && make lint
git commit   # why: the static census cannot see the two signatures that matter --
             # a refusal producing no visible change, and two elements sharing the
             # target id -- and neither is what a diff review looks at
```

---

### Task 9: Roadmap, and the full gate

- [ ] **Step 1: `docs/ROADMAP.md`**

```markdown
**WP-?? · HTMX swap targets** — S — **DONE 2026-09-16**

Every `hx-post` element in `web/templates` declares `hx-target` and `hx-swap`, enforced by
`internal/web/hx_target_test.go` with an empty exemption map. Three handlers stopped
answering a validation failure by re-rendering a list the form is not inside:
`CertificateCreate`, `TeamCreate` and `UserCreate` now re-render `certificate_form`,
`team_create_form` and `user_form`. `handleStoreError`'s `domain.ErrInvalid` branch stopped
being swapped at all — 422, an out-of-band flash and `HX-Reswap: none`, the first use of that
header in this codebase.

**NOT covered, deliberately:** widening `app.js`'s force-swap beyond 422 (409 is the obvious
candidate, it changes the behaviour of all 90 elements at once and needs its own evidence, and
it is exactly the change that would re-arm any status-based exemption — which is why the
exemption criterion refuses to depend on it); plain `method="post"` forms with no `hx-post`
(outside this census, correct by a different route — they navigate and never swap); redesigning
which partial each handler re-renders on 422 in general (only the three surfaces where
targeting alone inverted the failure were touched); a behavioural test per element.
```

- [ ] **Step 2: The full gate**

```bash
make lint
make test; echo "make test exit: $?"
```

- [ ] **Step 3: The evidence gate, for the PR body**

State, before any reviewer is invoked: *what would be true if this were broken, and what was
run to show it is not.*

1. **If it were broken, a refusal would produce no visible change.** Driven in a browser on
   five surfaces; the field error is in the DOM and the typed value is in the input on each.
   Identity rotation is the already-fixed control, so four greens elsewhere are not a broken
   spec reporting success.
2. **If it were broken, the DOM would hold two elements with the target id.**
   `toHaveCount(1)` per surface.
3. **If the census were broken, it would pass while matching nothing.** Element floor raised
   to 200 → red on the floor; id-set floor raised to 100000 → red on the id scan; four
   attribute mutations → four *distinct* messages, recorded verbatim.
4. **If a success path had changed, a create would stop redirecting.** Three Go tests assert
   204 + `HX-Redirect` on the three surfaces whose handler changed. Structurally it cannot:
   htmx 2.0.4 returns from `handleAjaxResponse` at the `HX-Redirect` branch *before* it
   resolves a target or picks a swap.
5. **If `HX-Reswap` were not honoured here, the panel would be replaced by one sentence.**
   Asserted in Go on the header and the OOB body, and in the browser on the panel still
   holding its own heading.
6. **Skips, stated:** no unit test was written for the twenty-eight class-2 markup edits
   individually — the census covers all of them and a per-element behavioural test is argued
   against in the spec. Health override clear has no browser refusal test, for the reason now
   written into `docs/E2E.md`.

- [ ] **Step 4: Commit**

```bash
git add -A && make lint
git commit   # why: the roadmap entry says what was NOT done, because this entry
             # has been found once before marked with its scope unbuilt
```

---

## Self-review notes

- **Spec coverage:** Ruling 1 (certificates, teams — plus users, D1) is Tasks 1-3 and lands
  before any of the 38 gets a target. Ruling 2 is Task 4 and lands before the sweep, because
  it is what makes the sweep safe for the twenty-eight retire forms. Ruling 3 (the census
  proves each named id exists) is Task 7 Step 1's second direction, with the conditional-id
  limitation written into the comment rather than left implied. Ruling 4 (the partial owns its
  id) is satisfied by not taking an id from a dict anywhere, and is **moot** for `journal.html`
  and `costs.html` because neither needs an id. Ruling 5 (floor at 80, labelled a smoke floor)
  is `hxTargetFloor`.
- **Sequencing hazards, stated:** Tasks 1-3 must land before **any** of the 38 is targeted, or
  targeting the three create forms makes their refusals invisible — strictly worse than today.
  Task 4 must land before Task 6, or the twenty-eight class-2 elements spend a commit range in
  which an `ErrInvalid` could replace a panel with a sentence. Task 7 must land last and cannot
  pass until Tasks 1-6 have.
- **The 52 were verified pairwise** (Task 0 Step 1) rather than assumed. Zero carry a target
  without a swap, so none joined scope — the spec left this open and it is now closed.
- **`partials/projects.html:391` is the one place Ruling 4's shape is already violated.** In
  the 52, out of scope, and recorded in `globsOverlap`'s comment rather than silently passing.
- **Every class-2 declaration is a claim about a handler**, and every one was read: the table
  in "The 38, resolved" names what each handler actually answers. If a future change gives one
  of them a 422 fragment, `hx-swap="none"` will silently swallow it — the one real cost of this
  approach, and the reason the comment beside each names the handler rather than gesturing at
  a rule.
````

---

## Notes on completeness

- **Nothing was truncated.** No `{ ... }` test bodies; every Go test is written out in full,
  every attribute value is literal, every target is named per element.
- **One deliberate abbreviation:** Task 8's Playwright spec is specified per-test (refusal
  vector, three assertions, locators, gating rationale) rather than written out as JS, because
  `e2e-tester` owns that file and needs the live DOM to pick locators. Say the word and I'll
  produce it as an addendum.
